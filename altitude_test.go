package main

import (
	"math"
	"strings"
	"testing"
)

// altitudeSeries builds a one-hour series with a three-level profile:
// 925 hPa @ 800 m, 800 hPa @ 2000 m, 700 hPa @ 3000 m.
func altitudeSeries(temps, winds, rhs [3]float64) *hourlySeries {
	heights := [3]float64{800, 2000, 3000}
	hPa := [3]int{925, 800, 700}
	s := &hourlySeries{
		time:             []string{"2026-01-10T06:00"},
		temperature2m:    []float64{0},
		windSpeed10m:     []float64{5},
		windGusts10m:     []float64{12},
		windDirection10m: []float64{270},
		precipitation:    []float64{0},
		rain:             []float64{0},
		snowfall:         []float64{0},
		precipType:       []int{0},
		visibility:       []float64{20000},
		cape:             []float64{0},
		cin:              []float64{0},
		cloudLow:         []float64{0},
		cloudMid:         []float64{0},
		cloudHigh:        []float64{0},
		temp850:          []float64{math.NaN()},
		temp700:          []float64{temps[2]},
		temp500:          []float64{math.NaN()},
		height850:        []float64{1500},
		height700:        []float64{3000},
		height500:        []float64{5600},
		wind850:          []float64{0},
		wind700:          []float64{winds[2]},
		wind500:          []float64{0},
		freezingLevel:    []float64{math.NaN()},
	}
	for k := 0; k < 3; k++ {
		s.levels = append(s.levels, levelSeries{
			hPa:    hPa[k],
			temp:   []float64{temps[k]},
			height: []float64{heights[k]},
			wind:   []float64{winds[k]},
			rh:     []float64{rhs[k]},
		})
	}
	return s
}

func TestProfileAtInterpolatesAndRejectsOutOfRange(t *testing.T) {
	s := altitudeSeries([3]float64{10, 4, -2}, [3]float64{10, 20, 60}, [3]float64{50, 60, 70})

	if got, ok := s.profileAt(0, 2500, profWind); !ok || math.Abs(got-40) > 1e-9 {
		t.Fatalf("wind at 2500 m = %.2f ok=%v, want 40", got, ok)
	}
	if got, ok := s.profileAt(0, 1400, profTemp); !ok || math.Abs(got-7) > 1e-9 {
		t.Fatalf("temp at 1400 m = %.2f ok=%v, want 7", got, ok)
	}
	// Within the slack below the lowest level: clamped, not extrapolated.
	if got, ok := s.profileAt(0, 750, profTemp); !ok || got != 10 {
		t.Fatalf("temp at 750 m = %.2f ok=%v, want clamped 10", got, ok)
	}
	if _, ok := s.profileAt(0, 300, profTemp); ok {
		t.Fatalf("300 m is far below the lowest level and must not be extrapolated")
	}
	if _, ok := s.profileAt(0, 4000, profTemp); ok {
		t.Fatalf("4000 m is above the highest level and must not be extrapolated")
	}
}

func TestProfileAtSkipsMissingLevels(t *testing.T) {
	s := altitudeSeries([3]float64{10, math.NaN(), -2}, [3]float64{10, 20, 60}, [3]float64{50, 60, 70})
	// 800 hPa temp missing ⇒ interpolate straight from 800 m to 3000 m.
	want := 10 + (1900.0-800)/(3000-800)*(-12)
	if got, ok := s.profileAt(0, 1900, profTemp); !ok || math.Abs(got-want) > 1e-9 {
		t.Fatalf("temp = %.3f ok=%v, want %.3f", got, ok, want)
	}
}

func TestDownscaleFollowsInversionInsteadOfFixedLapse(t *testing.T) {
	// Winter inversion: the valley floor (800 m) holds a -6 °C cold pool under
	// free air that is +2 °C at 800 m and still +1 °C at 2000 m. A fixed 6.5 °C/km
	// lapse would put the 2000 m summit near -14 °C; the free air there is +1 °C.
	s := altitudeSeries([3]float64{2, 1, -5}, [3]float64{5, 10, 20}, [3]float64{40, 40, 40})
	s.temperature2m[0] = -6

	adj := downscaleSeries(s, 800, 2000)
	if adj == nil || !strings.Contains(adj.LapseNote, "model temperature profile (1/1 hrs)") {
		t.Fatalf("expected profile-based note, got %+v", adj)
	}
	// keep = 1 - 1200/1500 = 0.2 of the -8 °C surface anomaly ⇒ 1 - 1.6 = -0.6.
	if got := s.temperature2m[0]; math.Abs(got-(-0.6)) > 1e-9 {
		t.Fatalf("summit temp %.2f, want -0.6", got)
	}
}

func TestDownscaleDownhillKeepsSurfaceAnomaly(t *testing.T) {
	s := altitudeSeries([3]float64{8, 2, -4}, [3]float64{5, 10, 20}, [3]float64{40, 40, 40})
	s.temperature2m[0] = 0 // 2 °C colder than the free air at 2000 m

	downscaleSeries(s, 2000, 800)
	// Free air warms 6 °C on the way down; the -2 °C surface anomaly is kept.
	if got := s.temperature2m[0]; math.Abs(got-6) > 1e-9 {
		t.Fatalf("valley temp %.2f, want 6", got)
	}
}

func TestDownscaleFallsBackWhenProfileDoesNotReachTerrain(t *testing.T) {
	s := altitudeSeries([3]float64{10, 4, -2}, [3]float64{5, 10, 20}, [3]float64{40, 40, 40})
	s.temperature2m[0] = 12

	adj := downscaleSeries(s, 300, 1300) // terrain far below the lowest level
	if adj == nil || strings.Contains(adj.LapseNote, "profile") {
		t.Fatalf("expected lapse fallback, got %+v", adj)
	}
	if got := s.temperature2m[0]; math.Abs(got-(12-6.5)) > 1e-9 {
		t.Fatalf("temp %.2f, want standard-lapse 5.5", got)
	}
}

func TestAltitudeExposureUsesFreeAirWind(t *testing.T) {
	s := altitudeSeries([3]float64{10, 4, -2}, [3]float64{10, 20, 60}, [3]float64{50, 60, 70})
	adj := &ElevationAdjustment{}

	applyAltitudeExposure(s, 2500, adj) // free-air wind 40 km/h

	if got := s.ridge()[0]; math.Abs(got-40) > 1e-9 {
		t.Fatalf("ridge wind %.1f, want 40 (not the 700 hPa 60)", got)
	}
	if got := s.windSpeed10m[0]; math.Abs(got-32) > 1e-9 {
		t.Fatalf("surface wind %.1f, want 32", got)
	}
	if got := s.windGusts10m[0]; math.Abs(got-44.8) > 1e-9 {
		t.Fatalf("gust %.1f, want 44.8", got)
	}
	if s.wind700[0] != 60 {
		t.Fatalf("700 hPa series must stay untouched for the snapshot")
	}
	if adj.FreeAirWindMaxKmh == nil || *adj.FreeAirWindMaxKmh != 40 {
		t.Fatalf("unexpected free-air metadata: %+v", adj)
	}
	if adj.InCloudHours == nil || *adj.InCloudHours != 0 {
		t.Fatalf("expected 0 in-cloud hours, got %+v", adj.InCloudHours)
	}
}

func TestAltitudeExposureNeverLowersModelWind(t *testing.T) {
	s := altitudeSeries([3]float64{10, 4, -2}, [3]float64{10, 20, 60}, [3]float64{50, 60, 70})
	s.windSpeed10m[0], s.windGusts10m[0] = 50, 90

	applyAltitudeExposure(s, 2500, nil)

	if s.windSpeed10m[0] != 50 || s.windGusts10m[0] != 90 {
		t.Fatalf("model wind was lowered: %.1f / %.1f", s.windSpeed10m[0], s.windGusts10m[0])
	}
}

func TestAltitudeExposureKeepsLegacyRidgeBelowProfile(t *testing.T) {
	s := altitudeSeries([3]float64{10, 4, -2}, [3]float64{10, 20, 60}, [3]float64{50, 60, 70})
	adj := &ElevationAdjustment{}

	applyAltitudeExposure(s, 400, adj)

	if s.ridge()[0] != 60 || s.windSpeed10m[0] != 5 {
		t.Fatalf("expected legacy wind below the profile, got ridge %.1f surface %.1f", s.ridge()[0], s.windSpeed10m[0])
	}
	if adj.FreeAirWindMaxKmh != nil || !strings.Contains(adj.WindNote, "700 hPa") {
		t.Fatalf("unexpected metadata: %+v", adj)
	}
}

func TestAltitudeExposureCapsVisibilityInCloud(t *testing.T) {
	s := altitudeSeries([3]float64{10, 4, -2}, [3]float64{10, 20, 60}, [3]float64{60, 96, 100})
	adj := &ElevationAdjustment{}

	applyAltitudeExposure(s, 2500, adj) // RH 98 %

	if s.visibility[0] != inCloudVisibilityM {
		t.Fatalf("visibility %.0f, want %.0f", s.visibility[0], inCloudVisibilityM)
	}
	if adj.InCloudHours == nil || *adj.InCloudHours != 1 {
		t.Fatalf("expected 1 in-cloud hour, got %+v", adj.InCloudHours)
	}
	if got := visibilityRiskLevel(s.visibility[0]); got != RiskModerate {
		t.Fatalf("in-cloud visibility risk %v, want MODERATE", got)
	}
}

func TestFreezingRiskIsRelativeToAltitude(t *testing.T) {
	level := 2000
	fd := freezingData{minLevel: &level}
	low, high := 1200.0, 1900.0

	if got := freezingLevelRisk(false, fd, nil); got != RiskModerate {
		t.Fatalf("legacy (no altitude) = %v, want MODERATE", got)
	}
	if got := freezingLevelRisk(false, fd, &low); got != RiskLow {
		t.Fatalf("hike 800 m below the freezing level = %v, want LOW", got)
	}
	if got := freezingLevelRisk(false, fd, &high); got != RiskModerate {
		t.Fatalf("hike 100 m below the freezing level = %v, want MODERATE", got)
	}

	level = 2600 // above the legacy 2200 m threshold
	summit := 2650.0
	if got := freezingLevelRisk(false, fd, nil); got != RiskLow {
		t.Fatalf("legacy at 2600 m = %v, want LOW", got)
	}
	if got := freezingLevelRisk(false, fd, &summit); got != RiskModerate {
		t.Fatalf("summit above the freezing level = %v, want MODERATE", got)
	}
	if got := freezingLevelRisk(true, fd, &low); got != RiskHigh {
		t.Fatalf("freezing precip = %v, want HIGH", got)
	}
}

func TestFreezingLevelPrefersModelDiagnostic(t *testing.T) {
	s := altitudeSeries([3]float64{10, 4, -2}, [3]float64{10, 20, 60}, [3]float64{50, 60, 70})
	s.temp850[0], s.temp700[0] = 5, -5 // interpolates to 2250 m
	if got := s.freezingLevelFor(0); got == nil || math.Abs(*got-2250) > 1e-9 {
		t.Fatalf("interpolated freezing level = %v, want 2250", got)
	}
	s.freezingLevel[0] = 2610
	if got := s.freezingLevelFor(0); got == nil || *got != 2610 {
		t.Fatalf("freezing level = %v, want the model's 2610", got)
	}
}

func TestMergeUpperAirKeepsSurfaceProfileAndFillsGaps(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	surface := &OpenMeteoResponse{Hourly: &OpenMeteoHourly{
		Time:              []string{"t0", "t1"},
		Temperature700hPa: []*float64{f(-1), nil},
	}}
	ecmwf := &OpenMeteoResponse{Hourly: &OpenMeteoHourly{
		Time:              []string{"t0", "t1"},
		Temperature700hPa: []*float64{f(-9), f(-3)},
		PrecipitationType: []*float64{f(1), f(2)},
	}}

	mergeUpperAir(surface, ecmwf)

	got := surface.Hourly.Temperature700hPa
	if *got[0] != -1 {
		t.Fatalf("surface value overwritten: %.1f", *got[0])
	}
	if got[1] == nil || *got[1] != -3 {
		t.Fatalf("gap not filled from ECMWF: %v", got[1])
	}
	if pt := surface.Hourly.PrecipitationType; len(pt) != 2 || *pt[1] != 2 {
		t.Fatalf("ECMWF-only field not merged: %v", pt)
	}
}
