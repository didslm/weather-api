package main

import "sort"

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
// Only these three fields are blended; the spread models don't carry the others
// (gusts, visibility, CAPE, upper-air), which stay with the primary source.
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
			temps = append(temps, sp.temperature2m[j])
			winds = append(winds, sp.windSpeed10m[j])
			precips = append(precips, sp.precipitation[j])
		}
		if len(temps) == 1 {
			continue // no other model covered this timestamp
		}
		ecmwf.temperature2m[i] = median(temps)
		ecmwf.windSpeed10m[i] = median(winds)
		ecmwf.precipitation[i] = median(precips)
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
