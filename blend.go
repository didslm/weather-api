package main

import (
	"math"
	"sort"
)

// ── Multi-model blending ──────────────────────────────────────────────────────
//
// Surface temperature, wind speed, and precipitation are the only fields every
// upstream model provides (forecast/best-match + GFS + GEM + ICON). Rather than
// trusting a single model for those values, blendSeries replaces them with the
// per-timestamp median across all models that cover that hour. The median is
// robust to any one model's outlier run — a single model's spurious precip spike
// is dropped rather than driving a false risk signal, and a lone cold/warm bias is
// pulled back toward consensus.
//
// Ordering matters and is enforced by the caller (BuildRiskForecast):
//   - runs AFTER confidence is computed, which must see raw per-model disagreement;
//   - runs BEFORE elevation downscaling, so the lapse correction and precip-phase
//     reclassification operate on the consensus values.
//
// Rain/snow amounts and phase are reconciled with the blended precipitation.
// Other fields (gusts, visibility, CAPE, upper-air) stay with the primary source.
func blendSeries(ecmwf *hourlySeries, spreads []*spreadSeries) {
	if ecmwf == nil || len(spreads) == 0 {
		return
	}

	// Timestamp → index for each spread model, so blending is robust to models
	// that cover slightly different hour ranges.
	idxMaps := make([]map[string]int, len(spreads))
	for k, sp := range spreads {
		m := make(map[string]int, len(sp.time))
		for i, t := range sp.time {
			m[t] = i
		}
		idxMaps[k] = m
	}

	for i, t := range ecmwf.time {
		temps := []float64{ecmwf.temperature2m[i]}
		winds := []float64{ecmwf.windSpeed10m[i]}
		precips := []float64{ecmwf.precipitation[i]}
		for k, sp := range spreads {
			j, ok := idxMaps[k][t]
			if !ok {
				continue
			}
			if finite(sp.temperature2m[j]) {
				temps = append(temps, sp.temperature2m[j])
			}
			if finite(sp.windSpeed10m[j]) {
				winds = append(winds, sp.windSpeed10m[j])
			}
			if finite(sp.precipitation[j]) {
				precips = append(precips, sp.precipitation[j])
			}
		}
		oldPrecip := ecmwf.precipitation[i]
		ecmwf.temperature2m[i] = median(temps)
		ecmwf.windSpeed10m[i] = median(winds)
		ecmwf.precipitation[i] = median(precips)
		if len(precips) > 1 {
			reconcilePrecipitation(ecmwf, i, oldPrecip)
		}
	}
}

// Keep primary-source phase proportions when its amount changes. If the primary
// was dry, infer rain/snow from the consensus temperature using the same snow
// threshold as terrain downscaling. Special freezing types survive wet hours.
func reconcilePrecipitation(s *hourlySeries, i int, oldPrecip float64) {
	if i >= len(s.rain) || i >= len(s.snowfall) || i >= len(s.precipType) {
		return
	}
	precip := s.precipitation[i]
	if precip <= 0 {
		s.rain[i], s.snowfall[i], s.precipType[i] = 0, 0, 0
		return
	}
	if oldPrecip > 0 {
		ratio := precip / oldPrecip
		s.rain[i] = math.Min(precip, s.rain[i]*ratio)
		s.snowfall[i] = math.Min(precip*mmToSnowCm, s.snowfall[i]*ratio)
		return
	}
	s.rain[i], s.snowfall[i] = 0, 0
	switch {
	case isFreezingPrecip(s.precipType[i]) || s.precipType[i] == 4:
		return
	case s.temperature2m[i] <= snowCeilingC:
		s.snowfall[i], s.precipType[i] = precip*mmToSnowCm, 2
	default:
		s.rain[i], s.precipType[i] = precip, 1
	}
}

// median returns the middle value (or the mean of the two middle values for an
// even count). It does not mutate its input.
func median(vals []float64) float64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
