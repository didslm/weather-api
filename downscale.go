package main

import (
	"fmt"
	"math"
)

// ── Terrain (elevation) downscaling ───────────────────────────────────────────
//
// Numerical models resolve a coarse gridpoint whose elevation can be far from the
// actual trailhead or summit — in the Balkans a ridge can sit 1000–2000 m above
// the cell the model reports. Feeding raw gridpoint values into the risk engine
// then mis-states temperature, wind chill, and rain-vs-snow for the real location.
//
// downscaleSeries corrects surface temperature to a caller-supplied target
// elevation using a lapse rate derived from the model's own 850/700 hPa profile
// when available (falling back to the standard atmosphere), then reclassifies
// precipitation phase at the corrected temperature. Wind chill and all risk
// levels recompute downstream automatically because they read these series.
//
// Wind and in-cloud visibility at the target altitude are handled separately in
// altitude.go. Still not corrected: precip amount (orographic enhancement) and
// CAPE.

const (
	standardLapseCPerKm = 6.5 // ICAO standard atmosphere environmental lapse rate
	minLapseCPerKm      = 3.0 // reject implausibly small model-derived lapse rates
	maxLapseCPerKm      = 9.8 // dry adiabatic ceiling
	snowCeilingC        = 1.0 // at/below this corrected temp, precip is treated as snow
	rainFloorC          = 2.0 // at/above this corrected temp, precip is treated as rain
	mmToSnowCm          = 0.7 // ~7:1 snow:liquid-water ratio (Open-Meteo convention)
	minElevDeltaM       = 20  // skip trivial corrections (below model noise)
)

// downscaleSeries mutates s in place, correcting it from gridElev to targetElev.
// It returns the adjustment that was applied, or nil if the correction was
// negligible (target ~= gridpoint) or the series is empty.
func downscaleSeries(s *hourlySeries, gridElev, targetElev float64) *ElevationAdjustment {
	if s == nil || len(s.temperature2m) == 0 {
		return nil
	}
	deltaM := targetElev - gridElev
	if math.Abs(deltaM) < minElevDeltaM {
		return nil
	}
	deltaKm := deltaM / 1000.0

	localHours, profileHours := 0, 0
	n := len(s.temperature2m)
	for i := 0; i < n; i++ {
		if t, ok := profileTempAt(s, i, gridElev, targetElev); ok {
			profileHours++
			s.temperature2m[i] = t
			reclassifyPhase(s, i)
			continue
		}
		lapse, local := localLapseCPerKm(s, i)
		if local {
			localHours++
		}
		// Higher target elevation (positive delta) ⇒ colder.
		s.temperature2m[i] -= lapse * deltaKm
		reclassifyPhase(s, i)
	}

	note := fmt.Sprintf("standard %.1f°C/km", standardLapseCPerKm)
	if localHours > 0 {
		note = fmt.Sprintf("model 850/700 hPa lapse (%d/%d hrs), else standard %.1f°C/km", localHours, n, standardLapseCPerKm)
	}
	if profileHours > 0 {
		note = fmt.Sprintf("model temperature profile (%d/%d hrs), else %s", profileHours, n, note)
	}
	return &ElevationAdjustment{
		TargetM:   round1(targetElev),
		ModelM:    round1(gridElev),
		LapseNote: note,
	}
}

// profileTempAt returns the temperature at targetElev for hour i using the model's
// vertical temperature profile, or false when the profile does not bracket both
// elevations (the caller then falls back to a single lapse rate).
//
// The model's free-air temperature change between the two elevations is applied
// to the surface temperature, which — unlike one 850→700 hPa lapse rate — follows
// the real layering, including the valley inversions that make a fixed lapse
// badly too cold aloft on calm winter mornings. Going up, the surface's own
// departure from the free air (night-time cooling, daytime heating) is faded out
// with height, since a summit well above the terrain sits in the free air; going
// down it is kept, since valleys hold the strongest surface signal.
func profileTempAt(s *hourlySeries, i int, gridElev, targetElev float64) (float64, bool) {
	tTarget, ok := s.profileAt(i, targetElev, profTemp)
	if !ok {
		return 0, false
	}
	tGrid, ok := s.profileAt(i, gridElev, profTemp)
	if !ok {
		return 0, false
	}
	deltaM := targetElev - gridElev
	keep := 1.0
	if deltaM > 0 {
		keep = math.Max(0, 1-deltaM/surfaceAnomalyDecayM)
	}
	corrected := tTarget + keep*(s.temperature2m[i]-tGrid)

	// Guard against a broken profile: no real layer warms or cools faster than
	// dry-adiabatic plus a generous inversion allowance.
	limit := maxLapseCPerKm*math.Abs(deltaM)/1000 + 5
	change := corrected - s.temperature2m[i]
	if math.Abs(change) > limit {
		return 0, false
	}
	return corrected, true
}

// localLapseCPerKm derives the environmental lapse rate for hour i from the model's
// 850→700 hPa layer. Returns the standard lapse (and false) when the profile is
// missing or yields a physically implausible value.
func localLapseCPerKm(s *hourlySeries, i int) (float64, bool) {
	t850, t700 := s.temp850[i], s.temp700[i]
	h850, h700 := s.height850[i], s.height700[i]
	if !finite(t850) || !finite(t700) || !finite(h850) || !finite(h700) || h700 <= h850 {
		return standardLapseCPerKm, false
	}
	lapse := (t850 - t700) / ((h700 - h850) / 1000.0)
	if math.IsNaN(lapse) || lapse < minLapseCPerKm || lapse > maxLapseCPerKm {
		return standardLapseCPerKm, false
	}
	return lapse, true
}

// reclassifyPhase shifts precipitation between rain and snow for hour i based on
// the (already corrected) surface temperature. Freezing rain / drizzle / ice
// pellets stay frozen hazards when the correction goes colder, but freezing rain
// and drizzle need a sub-zero surface to glaze: at a clearly above-freezing
// corrected temperature (a lower, warmer target) they fall as plain rain.
func reclassifyPhase(s *hourlySeries, i int) {
	precip := s.precipitation[i]
	if precip <= 0 {
		return
	}
	t := s.precipType[i]
	temp := s.temperature2m[i]
	if isFreezingPrecip(t) && temp >= rainFloorC {
		s.rain[i] = math.Max(s.rain[i], precip)
		s.precipType[i] = 1 // rain
		return
	}
	if isFreezingPrecip(t) || t == 4 { // freezing rain/drizzle (3/6) or ice pellets (4)
		return
	}
	switch {
	case temp <= snowCeilingC && (isRainType(t) || t == 0):
		s.snowfall[i] = math.Max(s.snowfall[i], precip*mmToSnowCm)
		s.rain[i] = 0
		s.precipType[i] = 2 // snow
	case temp >= rainFloorC && isSnowType(t):
		s.rain[i] = math.Max(s.rain[i], precip)
		s.snowfall[i] = 0
		s.precipType[i] = 1 // rain
	}
}

func isRainType(t int) bool { return t == 1 || t == 5 || t == 8 } // rain, drizzle, rain showers
func isSnowType(t int) bool { return t == 2 || t == 7 || t == 9 } // snow, snow grains, snow showers
