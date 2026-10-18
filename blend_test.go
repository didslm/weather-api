package main

import (
	"fmt"
	"math"
	"testing"
)

func TestMedian(t *testing.T) {
	tests := []struct {
		name string
		in   []float64
		want float64
	}{
		{"empty", nil, 0},
		{"single", []float64{5}, 5},
		{"odd", []float64{9, 1, 5}, 5},
		{"even", []float64{1, 2, 3, 4}, 2.5},
		{"unsorted even", []float64{7.1, 9.8, 6.9, 7.3}, 7.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := median(tt.in); math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("median(%v) = %.4f, want %.4f", tt.in, got, tt.want)
			}
		})
	}
}

func TestMedianDoesNotMutateInput(t *testing.T) {
	in := []float64{3, 1, 2}
	_ = median(in)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Fatalf("median mutated its input: %v", in)
	}
}

func TestBlendSeriesReplacesWithMedian(t *testing.T) {
	ecmwf := &hourlySeries{
		time:          []string{"2026-03-23T00:00", "2026-03-23T01:00"},
		temperature2m: []float64{7.1, 10.0},
		windSpeed10m:  []float64{20, 30},
		precipitation: []float64{0, 5},
	}
	spreads := []*spreadSeries{
		{
			time:          []string{"2026-03-23T00:00", "2026-03-23T01:00"},
			temperature2m: []float64{9.8, 11.0},
			windSpeed10m:  []float64{24, 36},
			precipitation: []float64{0, 0},
		},
		{
			time:          []string{"2026-03-23T00:00", "2026-03-23T01:00"},
			temperature2m: []float64{6.9, 12.0},
			windSpeed10m:  []float64{22, 33},
			precipitation: []float64{0, 1},
		},
	}

	blendSeries(ecmwf, spreads)

	// Hour 0 temps {7.1, 9.8, 6.9} -> median 7.1
	if math.Abs(ecmwf.temperature2m[0]-7.1) > 1e-9 {
		t.Fatalf("hour0 temp = %.4f, want 7.1", ecmwf.temperature2m[0])
	}
	// Hour 1 temps {10, 11, 12} -> median 11
	if math.Abs(ecmwf.temperature2m[1]-11.0) > 1e-9 {
		t.Fatalf("hour1 temp = %.4f, want 11", ecmwf.temperature2m[1])
	}
	// Hour 1 precip {5, 0, 1} -> median 1 (lone high forecast pulled down to consensus)
	if math.Abs(ecmwf.precipitation[1]-1.0) > 1e-9 {
		t.Fatalf("hour1 precip = %.4f, want 1", ecmwf.precipitation[1])
	}
	// Hour 1 wind {30, 36, 33} -> median 33
	if math.Abs(ecmwf.windSpeed10m[1]-33.0) > 1e-9 {
		t.Fatalf("hour1 wind = %.4f, want 33", ecmwf.windSpeed10m[1])
	}
}

func TestBlendSeriesNoSpreadsIsNoop(t *testing.T) {
	ecmwf := &hourlySeries{
		time:          []string{"2026-03-23T00:00"},
		temperature2m: []float64{7.1},
		windSpeed10m:  []float64{20},
		precipitation: []float64{3},
	}
	blendSeries(ecmwf, nil)
	if ecmwf.temperature2m[0] != 7.1 || ecmwf.windSpeed10m[0] != 20 || ecmwf.precipitation[0] != 3 {
		t.Fatalf("expected no change with no spreads, got %+v", ecmwf)
	}
}

func TestBlendSeriesAlignsByTimestamp(t *testing.T) {
	ecmwf := &hourlySeries{
		time:          []string{"2026-03-23T00:00", "2026-03-23T01:00"},
		temperature2m: []float64{7.0, 8.0},
		windSpeed10m:  []float64{10, 12},
		precipitation: []float64{0, 0},
	}
	// Spread only covers hour 1, and in reverse order — must match by timestamp.
	spreads := []*spreadSeries{
		{
			time:          []string{"2026-03-23T01:00"},
			temperature2m: []float64{20.0},
			windSpeed10m:  []float64{40},
			precipitation: []float64{0},
		},
	}

	blendSeries(ecmwf, spreads)

	// Hour 0 has no matching spread ⇒ unchanged.
	if ecmwf.temperature2m[0] != 7.0 {
		t.Fatalf("hour0 should be unchanged, got %.4f", ecmwf.temperature2m[0])
	}
	// Hour 1 blends {8, 20} ⇒ median 14.
	if math.Abs(ecmwf.temperature2m[1]-14.0) > 1e-9 {
		t.Fatalf("hour1 temp = %.4f, want 14", ecmwf.temperature2m[1])
	}
}

// Confidence must reflect the raw per-model disagreement (computed before blending),
// while the reported values become the blended consensus. Surface 8 °C with two
// spread models at 10 °C ⇒ raw temp spread of 2 (MEDIUM confidence), but the blended
// median is a flat 10 °C (which alone would read as HIGH confidence).
func TestBuildRiskForecastBlendsValuesButKeepsRawConfidence(t *testing.T) {
	surface := makeSurfaceResp(1000, 24, 8)
	spreadA := makeSpreadResp(24, 10, 0, 0)
	spreadB := makeSpreadResp(24, 10, 0, 0)

	f, err := BuildRiskForecast(
		surface,
		[]*OpenMeteoResponse{spreadA, spreadB},
		[]string{"forecast", "gfs", "gem"},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if f.Brief.TopSignals.Confidence != ConfMedium {
		t.Fatalf("expected MEDIUM confidence from raw spread, got %q", f.Brief.TopSignals.Confidence)
	}
	if f.Days[0].Confidence != ConfMedium {
		t.Fatalf("expected daily MEDIUM confidence from raw spread, got %q", f.Days[0].Confidence)
	}
	// Blended temp is median{8,10,10} = 10 at every hour.
	if math.Abs(f.Snapshot.TempMax-10) > 1e-9 || math.Abs(f.Snapshot.TempMin-10) > 1e-9 {
		t.Fatalf("expected blended temp 10, got max=%.4f min=%.4f", f.Snapshot.TempMax, f.Snapshot.TempMin)
	}
}

// makeSpreadResp builds a minimal Open-Meteo response carrying only the three
// blendable fields, as the GFS/GEM/ICON spread endpoints return.
func makeSpreadResp(hours int, temp, wind, precip float64) *OpenMeteoResponse {
	times := make([]string, hours)
	temps := make([]float64, hours)
	winds := make([]float64, hours)
	precips := make([]float64, hours)
	for i := 0; i < hours; i++ {
		times[i] = fmt.Sprintf("2026-03-23T%02d:00", i)
		temps[i] = temp
		winds[i] = wind
		precips[i] = precip
	}
	return &OpenMeteoResponse{
		Timezone: "UTC",
		Hourly: &OpenMeteoHourly{
			Time:          times,
			Temperature2m: ptrs(temps),
			WindSpeed10m:  ptrs(winds),
			Precipitation: ptrs(precips),
		},
	}
}

func TestBlendedPrecipitationKeepsPhaseAmountsConsistent(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		temp, oldPrecip, rain, snow   float64
		ptype                         float64
		consensus, wantRain, wantSnow float64
		wantType                      int
	}{
		{"dry removes snow", -5, 1, 0, 0.7, 2, 0, 0, 0, 0},
		{"dry removes freezing rain", -5, 1, 1, 0, 3, 0, 0, 0, 0},
		{"snow scales down", -5, 4, 0, 2.8, 2, 2, 0, 1.4, 2},
		{"snow scales up", -5, 1, 0, 0.7, 2, 4, 0, 2.8, 2},
		{"rain scales down", 5, 4, 4, 0, 1, 2, 2, 0, 1},
		{"new cold precipitation", -5, 0, 0, 0, 0, 2, 0, 1.4, 2},
		{"new warm precipitation", 5, 0, 0, 0, 0, 2, 2, 0, 1},
		{"wet freezing hazard preserved", -5, 1, 1, 0, 3, 2, 2, 0, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			surface := makeSurfaceResp(1000, 1, tc.temp)
			surface.Hourly.Precipitation = ptrs([]float64{tc.oldPrecip})
			surface.Hourly.Rain = ptrs([]float64{tc.rain})
			surface.Hourly.Snowfall = ptrs([]float64{tc.snow})
			surface.Hourly.PrecipitationType = ptrs([]float64{tc.ptype})
			peers := []*OpenMeteoResponse{
				makeSpreadResp(1, tc.temp, 0, tc.consensus),
				makeSpreadResp(1, tc.temp, 0, tc.consensus),
			}
			s := toHourlySeries(surface)
			blendSeries(s, []*spreadSeries{toSpreadSeries(peers[0]), toSpreadSeries(peers[1])})
			if math.Abs(s.rain[0]-tc.wantRain) > 1e-9 || math.Abs(s.snowfall[0]-tc.wantSnow) > 1e-9 || s.precipType[0] != tc.wantType {
				t.Fatalf("rain=%v snow=%v type=%v; want %v %v %v", s.rain[0], s.snowfall[0], s.precipType[0], tc.wantRain, tc.wantSnow, tc.wantType)
			}
			f := requireForecast(t, surface, peers...)
			if math.Abs(f.Days[0].PrecipSum-tc.consensus) > 1e-9 || math.Abs(f.Days[0].SnowSum-tc.wantSnow) > 1e-9 {
				t.Fatalf("daily precipitation amounts contradict consensus: %+v", f.Days[0])
			}
			if tc.consensus == 0 && (toSimpleDays(f)[0].Condition == "snow" || f.Snapshot.PrecipType != "None" || f.Brief.DominantRisk == CatFreezing) {
				t.Fatal("dry consensus retains precipitation hazard")
			}
		})
	}
}
