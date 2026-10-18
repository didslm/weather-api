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
// Not corrected here: wind, gusts, precip amount, visibility, CAPE, cloud. Those
// need orographic/exposure modelling rather than a clean lapse and are left for a
// later pass.

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

	localHours := 0
	n := len(s.temperature2m)
	for i := 0; i < n; i++ {
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
	return &ElevationAdjustment{
		TargetM:   round1(targetElev),
		ModelM:    round1(gridElev),
		LapseNote: note,
	}
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
// pellets are left untouched — they are frozen hazards regardless of the shift.
func reclassifyPhase(s *hourlySeries, i int) {
	precip := s.precipitation[i]
	if precip <= 0 {
		return
	}
	t := s.precipType[i]
	if isFreezingPrecip(t) || t == 4 { // freezing rain/drizzle (3/6) or ice pellets (4)
		return
	}
	temp := s.temperature2m[i]
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
