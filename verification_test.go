package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildForecastLogRecords(t *testing.T) {
	issued := time.Date(2026, 3, 23, 6, 30, 0, 0, time.UTC)
	f := &RiskForecast{
		Sources: []string{"ecmwf", "gfs"},
		Days: []DailyRiskSummary{
			{
				Date:          "2026-03-23",
				DominantRisk:  CatWind,
				RiskLevel:     RiskModerate,
				Confidence:    ConfHigh,
				TempMin:       -2.44,
				TempMax:       6.18,
				WindMax:       48.06,
				GustMax:       61.9,
				PrecipSum:     3.25,
				SnowSum:       1.1,
				VisibilityMin: 850,
			},
			{
				Date:      "2026-03-25",
				RiskLevel: RiskLow,
			},
			{
				Date: "not-a-date", // skipped
			},
		},
	}

	records := buildForecastLogRecords(45.25, 19.83, f, issued)
	if len(records) != 2 {
		t.Fatalf("expected 2 records (bad date skipped), got %d", len(records))
	}

	first := records[0]
	if first.IssuedAt != "2026-03-23T06:30:00Z" {
		t.Fatalf("unexpected issued_at: %q", first.IssuedAt)
	}
	if first.LeadDays != 0 {
		t.Fatalf("expected lead_days 0, got %d", first.LeadDays)
	}
	if first.RiskLevel != "MODERATE" {
		t.Fatalf("unexpected risk_level: %q", first.RiskLevel)
	}
	if first.DominantRisk != "WIND" {
		t.Fatalf("unexpected dominant_risk: %q", first.DominantRisk)
	}
	if first.Confidence != "HIGH" {
		t.Fatalf("unexpected confidence: %q", first.Confidence)
	}
	if first.TempMinC != -2.4 || first.TempMaxC != 6.2 {
		t.Fatalf("unexpected temps: %.1f %.1f", first.TempMinC, first.TempMaxC)
	}
	if first.WindMaxKmh != 48.1 || first.GustMaxKmh != 61.9 {
		t.Fatalf("unexpected wind: %.1f %.1f", first.WindMaxKmh, first.GustMaxKmh)
	}
	if first.VisibilityMinM != 850 {
		t.Fatalf("unexpected visibility: %.1f", first.VisibilityMinM)
	}
	if len(first.Sources) != 2 || first.Sources[0] != "ecmwf" {
		t.Fatalf("unexpected sources: %+v", first.Sources)
	}

	if records[1].LeadDays != 2 {
		t.Fatalf("expected lead_days 2 for target +2 days, got %d", records[1].LeadDays)
	}
}

func TestVerificationLoggerAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "forecast_log.jsonl")
	logger := &verificationLogger{path: path}

	forecast := &RiskForecast{
		Sources: []string{"ecmwf"},
		Days: []DailyRiskSummary{
			{Date: "2026-03-23", RiskLevel: RiskLow, Confidence: ConfHigh},
			{Date: "2026-03-24", RiskLevel: RiskHigh, Confidence: ConfLow},
		},
	}

	issued := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)
	logger.record(41.33, 19.82, forecast, issued)
	logger.record(41.33, 19.82, forecast, issued) // second call appends, does not truncate

	lines := readJSONLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("expected 4 records across two appends, got %d", len(lines))
	}
	if lines[0].TargetDate != "2026-03-23" || lines[0].Lat != 41.33 {
		t.Fatalf("unexpected first record: %+v", lines[0])
	}
	if lines[1].RiskLevel != "HIGH" {
		t.Fatalf("unexpected second record risk: %q", lines[1].RiskLevel)
	}
}

func TestVerificationLoggerNilAndEmptyAreNoops(t *testing.T) {
	var nilLogger *verificationLogger
	nilLogger.record(0, 0, &RiskForecast{Days: []DailyRiskSummary{{Date: "2026-03-23"}}}, time.Now())

	// A logger that never writes must not create a file (nil forecast, no days).
	path := filepath.Join(t.TempDir(), "should_not_exist.jsonl")
	logger := &verificationLogger{path: path}
	logger.record(0, 0, nil, time.Now())
	logger.record(0, 0, &RiskForecast{}, time.Now())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file to be created, stat err=%v", err)
	}
}

func readJSONLines(t *testing.T, path string) []forecastLogRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer file.Close()

	var out []forecastLogRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var rec forecastLogRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}
