package main

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestDownscaleSeriesStandardLapse(t *testing.T) {
	// No usable pressure profile ⇒ standard 6.5 °C/km.
	s := &hourlySeries{
		temperature2m: []float64{10, 10, 10},
		precipitation: []float64{0, 0, 0},
		rain:          []float64{0, 0, 0},
		snowfall:      []float64{0, 0, 0},
		precipType:    []int{0, 0, 0},
		temp850:       []float64{0, 0, 0},
		temp700:       []float64{0, 0, 0},
		height850:     []float64{1500, 1500, 1500},
		height700:     []float64{3000, 3000, 3000},
	}

	adj := downscaleSeries(s, 1000, 2000) // +1000 m
	if adj == nil {
		t.Fatalf("expected an adjustment")
	}
	if adj.TargetM != 2000 || adj.ModelM != 1000 {
		t.Fatalf("unexpected adjustment metadata: %+v", adj)
	}
	if !strings.Contains(adj.LapseNote, "standard") {
		t.Fatalf("expected standard lapse note, got %q", adj.LapseNote)
	}

	want := 10.0 - 6.5
	for i, got := range s.temperature2m {
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("hour %d: temp %.4f, want %.4f", i, got, want)
		}
	}
}

func TestDownscaleSeriesModelLapse(t *testing.T) {
	// t850=2, t700=-7 over a 1500 m layer ⇒ lapse = 9/1.5 = 6.0 °C/km.
	s := &hourlySeries{
		temperature2m: []float64{5},
		precipitation: []float64{0},
		rain:          []float64{0},
		snowfall:      []float64{0},
		precipType:    []int{0},
		temp850:       []float64{2},
		temp700:       []float64{-7},
		height850:     []float64{1500},
		height700:     []float64{3000},
	}

	adj := downscaleSeries(s, 1500, 2500) // +1000 m
	if adj == nil || !strings.Contains(adj.LapseNote, "model 850/700") {
		t.Fatalf("expected model-derived lapse note, got %+v", adj)
	}
	want := 5.0 - 6.0
	if math.Abs(s.temperature2m[0]-want) > 1e-9 {
		t.Fatalf("temp %.4f, want %.4f", s.temperature2m[0], want)
	}
}

func TestDownscaleSeriesRejectsImplausibleLapse(t *testing.T) {
	// Inverted layer (t850 < t700) would yield a negative lapse ⇒ fall back to standard.
	lapse, ok := localLapseCPerKm(&hourlySeries{
		temp850:   []float64{-5},
		temp700:   []float64{5},
		height850: []float64{1500},
		height700: []float64{3000},
	}, 0)
	if ok {
		t.Fatalf("expected fallback for inverted layer")
	}
	if lapse != standardLapseCPerKm {
		t.Fatalf("expected standard lapse, got %.2f", lapse)
	}
}

func TestDownscaleReclassifiesRainToSnow(t *testing.T) {
	s := &hourlySeries{
		temperature2m: []float64{6},
		precipitation: []float64{10},
		rain:          []float64{10},
		snowfall:      []float64{0},
		precipType:    []int{1}, // rain
		temp850:       []float64{0},
		temp700:       []float64{0},
		height850:     []float64{1500},
		height700:     []float64{3000},
	}

	downscaleSeries(s, 500, 2500) // +2000 m, standard 6.5 ⇒ 6 - 13 = -7 °C

	if s.temperature2m[0] > 0 {
		t.Fatalf("expected sub-freezing corrected temp, got %.2f", s.temperature2m[0])
	}
	if s.rain[0] != 0 {
		t.Fatalf("expected rain zeroed, got %.2f", s.rain[0])
	}
	if s.precipType[0] != 2 {
		t.Fatalf("expected precip type snow(2), got %d", s.precipType[0])
	}
	if math.Abs(s.snowfall[0]-7.0) > 1e-9 { // 10 mm * 0.7
		t.Fatalf("expected 7.0 cm snow, got %.2f", s.snowfall[0])
	}
}

func TestDownscaleLeavesFreezingRainUntouched(t *testing.T) {
	s := &hourlySeries{
		temperature2m: []float64{6},
		precipitation: []float64{4},
		rain:          []float64{4},
		snowfall:      []float64{0},
		precipType:    []int{3}, // freezing rain — already a frozen hazard
		temp850:       []float64{0},
		temp700:       []float64{0},
		height850:     []float64{1500},
		height700:     []float64{3000},
	}

	downscaleSeries(s, 500, 2500)

	if s.precipType[0] != 3 || s.rain[0] != 4 || s.snowfall[0] != 0 {
		t.Fatalf("freezing rain should be left untouched, got type=%d rain=%.1f snow=%.1f",
			s.precipType[0], s.rain[0], s.snowfall[0])
	}
}

func TestDownscaleNoopWhenNegligible(t *testing.T) {
	s := &hourlySeries{
		temperature2m: []float64{10},
		precipitation: []float64{0},
		rain:          []float64{0},
		snowfall:      []float64{0},
		precipType:    []int{0},
		temp850:       []float64{0},
		temp700:       []float64{0},
		height850:     []float64{1500},
		height700:     []float64{3000},
	}

	adj := downscaleSeries(s, 1000, 1010) // 10 m < minElevDeltaM
	if adj != nil {
		t.Fatalf("expected nil adjustment for negligible delta")
	}
	if s.temperature2m[0] != 10 {
		t.Fatalf("expected temperature unchanged, got %.4f", s.temperature2m[0])
	}
}

func TestBuildRiskForecastAppliesElevation(t *testing.T) {
	resp := makeSurfaceResp(1000, 24, 8)

	target := 2500.0
	f, err := BuildRiskForecast(resp, nil, []string{"forecast"}, &target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Elevation == nil {
		t.Fatalf("expected elevation adjustment in forecast")
	}
	if f.Elevation.TargetM != 2500 || f.Elevation.ModelM != 1000 {
		t.Fatalf("unexpected elevation metadata: %+v", f.Elevation)
	}
	// +1500 m at standard lapse ⇒ ~9.75 °C colder than the 8 °C gridpoint.
	if f.Snapshot.TempMax >= 8 {
		t.Fatalf("expected corrected temp below gridpoint, got max %.2f", f.Snapshot.TempMax)
	}

	// No elevation ⇒ no adjustment and gridpoint temps preserved.
	base, err := BuildRiskForecast(resp, nil, []string{"forecast"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base.Elevation != nil {
		t.Fatalf("expected no elevation adjustment, got %+v", base.Elevation)
	}
}

// makeSurfaceResp builds a minimal valid Open-Meteo surface response of `hours`
// hours, all at temperature `temp`, suitable for BuildRiskForecast.
func makeSurfaceResp(elevation float64, hours int, temp float64) *OpenMeteoResponse {
	times := make([]string, hours)
	temps := make([]float64, hours)
	zeros := make([]float64, hours)
	vis := make([]float64, hours)
	for i := 0; i < hours; i++ {
		times[i] = fmt.Sprintf("2026-03-23T%02d:00", i)
		temps[i] = temp
		vis[i] = 10000
	}
	return &OpenMeteoResponse{
		Elevation: elevation,
		Timezone:  "UTC",
		Hourly: &OpenMeteoHourly{
			Time:             times,
			Temperature2m:    ptrs(temps),
			WindSpeed10m:     ptrs(zeros),
			WindGusts10m:     ptrs(zeros),
			WindDirection10m: ptrs(zeros),
			Precipitation:    ptrs(zeros),
			Rain:             ptrs(zeros),
			Snowfall:         ptrs(zeros),
			Visibility:       ptrs(vis),
			CloudCoverLow:    ptrs(zeros),
			CloudCoverMid:    ptrs(zeros),
			CloudCoverHigh:   ptrs(zeros),
		},
	}
}

func ptrs(vals []float64) []*float64 {
	out := make([]*float64, len(vals))
	for i := range vals {
		v := vals[i]
		out[i] = &v
	}
	return out
}
