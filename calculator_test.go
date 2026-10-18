package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func repeatedPointers(n int, value float64) []*float64 {
	values := make([]float64, n)
	for i := range values {
		values[i] = value
	}
	return ptrs(values)
}

func requireForecast(t *testing.T, surface *OpenMeteoResponse, peers ...*OpenMeteoResponse) *RiskForecast {
	t.Helper()
	f, err := BuildRiskForecast(surface, peers, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(f); err != nil {
		t.Fatalf("forecast cannot be encoded: %v", err)
	}
	return f
}

func TestPrecipRiskUsesHighestThreshold(t *testing.T) {
	for _, tc := range []struct {
		total, peak float64
		want        RiskLevel
	}{
		{48, 1.99, RiskHigh}, {48, 2, RiskHigh}, {48, 4.99, RiskHigh},
		{30, 2, RiskHigh}, {5, 5, RiskHigh}, {10, 1, RiskModerate},
		{2, 2, RiskModerate}, {9, 1, RiskLow},
	} {
		if got := precipRiskLevel(tc.total, tc.peak); got != tc.want {
			t.Errorf("total=%v peak=%v: got %s, want %s", tc.total, tc.peak, got, tc.want)
		}
	}
	// Ensure both the brief and daily summary receive the corrected level.
	surface := makeSurfaceResp(1000, 24, 15)
	surface.Hourly.Precipitation = repeatedPointers(24, 2)
	f := requireForecast(t, surface)
	if f.Days[0].RiskLevel != RiskHigh {
		t.Fatalf("48 mm day classified %s", f.Days[0].RiskLevel)
	}
	for _, key := range f.Brief.KeyRisks {
		if key.Type == KeyPrecip && key.Level != RiskHigh {
			t.Fatalf("brief precipitation risk: %s", key.Level)
		}
	}
}

func TestThermalRiskIncludesCalmHours(t *testing.T) {
	for _, tc := range []struct {
		temp float64
		want RiskLevel
	}{{-25, RiskHigh}, {-10, RiskHigh}, {0, RiskModerate}, {5, RiskLow}} {
		f := requireForecast(t, makeSurfaceResp(1000, 24, tc.temp))
		if f.Days[0].RiskLevel != tc.want {
			t.Errorf("calm %vC: got %s, want %s", tc.temp, f.Days[0].RiskLevel, tc.want)
		}
		if tc.want != RiskLow && f.Brief.DominantRisk != CatThermal {
			t.Errorf("calm %vC: brief omits thermal risk", tc.temp)
		}
		if f.Snapshot.WindChillMin != nil {
			t.Fatal("calm air should not produce a wind-chill measurement")
		}
	}
	// A warmer windy hour must not hide a much colder calm hour.
	surface := makeSurfaceResp(1000, 24, 5)
	surface.Hourly.WindSpeed10m = repeatedPointers(24, 10)
	surface.Hourly.Temperature2m[0] = repeatedPointers(1, -25)[0]
	surface.Hourly.WindSpeed10m[0] = repeatedPointers(1, 0)[0]
	f := requireForecast(t, surface)
	if f.Days[0].RiskLevel != RiskHigh || f.Brief.DominantRisk != CatThermal {
		t.Fatal("cold calm hour lost when other hours have wind chill")
	}
	if f.Brief.TopSignals.TempFeel != TempVeryCold {
		t.Fatal("temperature signal hides cold calm hour")
	}
}

func TestDailyAndBriefFreezingRiskAgree(t *testing.T) {
	for _, tc := range []struct {
		name       string
		t850, t700 float64
		wantLevel  *int
	}{
		{"crossing", 1, -9, newInt(1650)},
		{"below profile", -1, -9, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			surface := makeSurfaceResp(1000, 24, 5)
			surface.Hourly.Temperature850hPa = repeatedPointers(24, tc.t850)
			surface.Hourly.Temperature700hPa = repeatedPointers(24, tc.t700)
			surface.Hourly.Temperature500hPa = repeatedPointers(24, -20)
			f := requireForecast(t, surface)
			if f.Brief.DominantRisk != CatFreezing || f.Days[0].DominantRisk != CatFreezing || f.Days[0].RiskLevel != RiskModerate {
				t.Fatalf("inconsistent freezing risks: brief=%s day=%+v", f.Brief.DominantRisk, f.Days[0])
			}
			if tc.wantLevel != nil && (f.Days[0].FreezingLevelMin == nil || *f.Days[0].FreezingLevelMin != *tc.wantLevel) {
				t.Fatalf("expected freezing level %d", *tc.wantLevel)
			}
		})
	}
}

func newInt(v int) *int { return &v }

func TestMissingProfileRemainsUnknown(t *testing.T) {
	for _, partial := range []bool{false, true} {
		surface := makeSurfaceResp(1000, 24, 20)
		if partial {
			// One known warm level cannot imply a freezing crossing.
			surface.Hourly.Temperature700hPa = repeatedPointers(24, 5)
		}
		f := requireForecast(t, surface)
		if f.Snapshot.FreezingLevelMin != nil || f.Days[0].FreezingLevelMin != nil || f.Snapshot.FreezingBelow1500 || f.Snapshot.FreezingAbove5600 {
			t.Fatalf("partial=%v: missing data interpreted as freezing level", partial)
		}
		if f.Brief.DominantRisk != CatNone {
			t.Fatalf("partial=%v: invented freezing risk: %s", partial, f.Brief.DominantRisk)
		}
		s := toHourlySeries(surface)
		if _, local := localLapseCPerKm(s, 0); local {
			t.Fatal("partial profile used to derive lapse rate")
		}
	}
	// An actual 0C observation is still meaningful.
	if fl := freezingLevelAt(0, -5, -20, 1500, 3000, 5600); fl == nil || *fl != 1500 {
		t.Fatal("valid zero temperature treated as missing")
	}
	if fl := freezingLevelAt(math.NaN(), 5, -5, 1500, 3000, 5600); fl == nil || *fl != 4300 {
		t.Fatal("valid upper layer lost when lower temperature is absent")
	}
}

func TestConfidenceTracksHourlyTimingAndCoverage(t *testing.T) {
	for _, field := range []string{"temperature", "wind", "precipitation"} {
		t.Run(field, func(t *testing.T) {
			surface := makeSurfaceResp(1000, 24, 5)
			peer := makeSpreadResp(24, 5, 0, 0)
			var a, b []*float64
			var high float64
			switch field {
			case "temperature":
				a, b, high = surface.Hourly.Temperature2m, peer.Hourly.Temperature2m, 20
			case "wind":
				a, b, high = surface.Hourly.WindSpeed10m, peer.Hourly.WindSpeed10m, 30
			case "precipitation":
				a, b, high = surface.Hourly.Precipitation, peer.Hourly.Precipitation, 5
			}
			for i := range a {
				if i < 12 {
					*a[i], *b[i] = 0, high
				} else {
					*a[i], *b[i] = high, 0
				}
			}
			f := requireForecast(t, surface, peer)
			if f.Brief.TopSignals.Confidence != ConfLow || f.Days[0].Confidence != ConfLow {
				t.Fatal("equal daily means hid hourly disagreement")
			}
		})
	}
	for _, mode := range []string{"single source", "no overlap", "partial overlap", "null peer"} {
		t.Run(mode, func(t *testing.T) {
			surface := makeSurfaceResp(1000, 24, 5)
			peer := makeSpreadResp(24, 5, 0, 0)
			peers := []*OpenMeteoResponse{peer}
			switch mode {
			case "single source":
				peers = nil
			case "no overlap":
				for i := range peer.Hourly.Time {
					peer.Hourly.Time[i] = "2026-03-24T00:00"
				}
			case "partial overlap":
				peer.Hourly.Time[0] = "2026-03-24T00:00"
			case "null peer":
				peer.Hourly.Temperature2m[0] = nil
			}
			f := requireForecast(t, surface, peers...)
			if f.Brief.TopSignals.Confidence != ConfLow || f.Days[0].Confidence != ConfLow {
				t.Fatalf("insufficient coverage reported confident: %s", f.Days[0].Confidence)
			}
		})
	}
}

func TestDailyConfidenceUsesOwnRawModelWindow(t *testing.T) {
	surface := makeSurfaceResp(1000, 48, 5)
	peer := makeSpreadResp(48, 5, 0, 0)
	start := time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC)
	for i := range surface.Hourly.Time {
		timestamp := start.Add(time.Duration(i) * time.Hour).Format("2006-01-02T15:04")
		surface.Hourly.Time[i], peer.Hourly.Time[i] = timestamp, timestamp
		if i >= 24 {
			*peer.Hourly.Temperature2m[i] = 13
		}
	}
	f := requireForecast(t, surface, peer)
	if f.Brief.TopSignals.Confidence != ConfHigh || f.Days[0].Confidence != ConfHigh || f.Days[1].Confidence != ConfLow {
		t.Fatalf("confidence does not match each day's raw spread: %+v", f.Days)
	}
	// Reordering peer data must not affect confidence.
	h := peer.Hourly
	for i, j := 0, len(h.Time)-1; i < j; i, j = i+1, j-1 {
		h.Time[i], h.Time[j] = h.Time[j], h.Time[i]
		h.Temperature2m[i], h.Temperature2m[j] = h.Temperature2m[j], h.Temperature2m[i]
	}
	reordered := requireForecast(t, surface, peer)
	for i := range f.Days {
		if f.Days[i].Confidence != reordered.Days[i].Confidence {
			t.Fatal("confidence depends on peer array order")
		}
	}
}

func TestConfidentSevereWeatherIsStillHighConfidence(t *testing.T) {
	surface := makeSurfaceResp(1000, 24, 15)
	surface.Hourly.Precipitation = repeatedPointers(24, 5)
	peer := makeSpreadResp(24, 15, 0, 5)
	f := requireForecast(t, surface, peer)
	if f.Days[0].RiskLevel != RiskHigh || f.Days[0].Confidence != ConfHigh {
		t.Fatalf("severity incorrectly reduces confidence: risk=%s confidence=%s", f.Days[0].RiskLevel, f.Days[0].Confidence)
	}
}
