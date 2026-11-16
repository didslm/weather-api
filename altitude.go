package main

import (
	"fmt"
	"math"
	"sort"
)

// ── Altitude focus ────────────────────────────────────────────────────────────
//
// A hike happens at a specific altitude, and the model's "surface" values are not
// at that altitude: the model sees smoothed terrain that in the Balkans commonly
// sits 500–1000 m below a real summit. Its 10 m wind is the wind over that
// smoothed surface (often a few km/h) while the summit sticks into a free-air flow
// several times stronger, and its visibility says nothing about a summit that is
// inside the cloud deck.
//
// When the caller supplies a target elevation, the vertical profile (pressure
// levels 925…500 hPa) is interpolated to that altitude and used for:
//
//   - temperature — see profileTempAt in downscale.go;
//   - wind        — the free-air wind at the altitude becomes the "ridge wind" that
//                   drives the wind risk, and floors the surface wind and gusts;
//   - cloud       — near-saturated air at the altitude means the hiker is in cloud,
//                   which caps visibility;
//   - freezing    — the freezing level is judged against the altitude instead of a
//                   fixed 2200 m (see freezingLevelRisk).
//
// Without a target elevation nothing here runs and the legacy behaviour (700 hPa
// as the ridge wind, fixed freezing thresholds) is kept, because the caller's
// point may be a valley trailhead and lowering the ridge wind to valley level
// would hide the hazard above it.
//
// The exposure constants below are physically motivated defaults, not fitted
// values. They assume exposed terrain (ridge/summit) at the target altitude and
// are the first thing to tune once graded verification data exists.

const (
	summitExposureFactor = 0.8   // sustained wind on exposed terrain ≈ this × free-air wind
	summitGustFactor     = 1.4   // gust ≈ this × exposed sustained wind
	inCloudRHPercent     = 97.0  // RH at/above this at the target altitude ⇒ in cloud
	inCloudVisibilityM   = 300.0 // visibility cap while in cloud
	surfaceAnomalyDecayM = 1500  // height above terrain over which the surface (diurnal/inversion) signal fades
	freezingMarginM      = 200   // freezing level within this distance above the hike counts as "at" it
	profileSlackM        = 100.0 // allow this much extrapolation beyond the lowest/highest level
)

// levelSeries holds one pressure level of the vertical profile. Values are NaN
// where the model did not provide them.
type levelSeries struct {
	hPa    int
	temp   []float64
	height []float64
	wind   []float64
	rh     []float64
}

type profileVar int

const (
	profTemp profileVar = iota
	profWind
	profRH
)

func (l *levelSeries) values(v profileVar) []float64 {
	switch v {
	case profTemp:
		return l.temp
	case profWind:
		return l.wind
	default:
		return l.rh
	}
}

// profileAt linearly interpolates a profile variable to altitude z (metres MSL)
// for hour i. It returns false when fewer than two levels carry the variable or z
// lies outside the levels' height range (beyond profileSlackM) — extrapolating a
// free-air profile down into the boundary layer is not trustworthy.
func (s *hourlySeries) profileAt(i int, z float64, v profileVar) (float64, bool) {
	type pt struct{ h, val float64 }
	pts := make([]pt, 0, len(s.levels))
	for k := range s.levels {
		l := &s.levels[k]
		vals := l.values(v)
		if i >= len(vals) || i >= len(l.height) {
			continue
		}
		if finite(vals[i]) && finite(l.height[i]) {
			pts = append(pts, pt{l.height[i], vals[i]})
		}
	}
	if len(pts) < 2 {
		return 0, false
	}
	sort.Slice(pts, func(a, b int) bool { return pts[a].h < pts[b].h })
	lo, hi := pts[0], pts[len(pts)-1]
	if z < lo.h-profileSlackM || z > hi.h+profileSlackM {
		return 0, false
	}
	if z <= lo.h {
		return lo.val, true
	}
	if z >= hi.h {
		return hi.val, true
	}
	for k := 0; k < len(pts)-1; k++ {
		a, b := pts[k], pts[k+1]
		if z >= a.h && z <= b.h {
			if b.h == a.h {
				return a.val, true
			}
			return a.val + (z-a.h)/(b.h-a.h)*(b.val-a.val), true
		}
	}
	return 0, false
}

// applyAltitudeExposure mutates s so wind and visibility describe exposed terrain
// at targetElev, and records the altitude on the series so risk thresholds that
// depend on it (freezing level) can use it. It fills the altitude fields of adj.
func applyAltitudeExposure(s *hourlySeries, targetElev float64, adj *ElevationAdjustment) {
	if s == nil || len(s.time) == 0 {
		return
	}
	alt := targetElev
	s.altitude = &alt

	n := len(s.time)
	ridge := append([]float64(nil), s.ridge()...)
	windHours, cloudHours24 := 0, 0
	freeAirMax24 := math.Inf(-1)
	for i := 0; i < n; i++ {
		if w, ok := s.profileAt(i, targetElev, profWind); ok && i < len(ridge) {
			windHours++
			ridge[i] = w
			exposed := w * summitExposureFactor
			if exposed > s.windSpeed10m[i] {
				s.windSpeed10m[i] = exposed
			}
			if g := exposed * summitGustFactor; g > s.windGusts10m[i] {
				s.windGusts10m[i] = g
			}
			if i < 24 && w > freeAirMax24 {
				freeAirMax24 = w
			}
		}
		if rh, ok := s.profileAt(i, targetElev, profRH); ok && rh >= inCloudRHPercent {
			if s.visibility[i] > inCloudVisibilityM {
				s.visibility[i] = inCloudVisibilityM
			}
			if i < 24 {
				cloudHours24++
			}
		}
	}
	s.ridgeWind = ridge

	if adj == nil {
		return
	}
	if windHours > 0 {
		v := round1(freeAirMax24)
		adj.FreeAirWindMaxKmh = &v
		adj.WindNote = fmt.Sprintf("free-air wind at %.0f m from model levels (%d/%d hrs); surface wind/gusts floored for exposed terrain", targetElev, windHours, n)
	} else {
		adj.WindNote = "no model levels bracket this altitude; ridge wind is 700 hPa (~3000 m)"
	}
	if len(s.levels) > 0 {
		c := cloudHours24
		adj.InCloudHours = &c
	}
}

// ridge returns the wind series that drives the wind risk: the free-air wind at
// the target altitude when altitude focus ran, else the 700 hPa wind.
func (s *hourlySeries) ridge() []float64 {
	if s.ridgeWind != nil {
		return s.ridgeWind
	}
	return s.wind700
}

// freezingLevelFor returns the 0 °C level (metres MSL) for hour i: the model's own
// diagnostic when present, else interpolated from the 850/700/500 hPa levels.
func (s *hourlySeries) freezingLevelFor(i int) *float64 {
	if i < len(s.freezingLevel) && finite(s.freezingLevel[i]) {
		v := s.freezingLevel[i]
		return &v
	}
	return freezingLevelAt(s.temp850[i], s.temp700[i], s.temp500[i], s.height850[i], s.height700[i], s.height500[i])
}
