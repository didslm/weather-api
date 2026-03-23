package main

import "testing"
import "time"

func TestInLiveWindow(t *testing.T) {
	today := time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC)

	if !inLiveWindow(today, today, 10) {
		t.Fatalf("expected live window for today + 10 days")
	}

	if !inLiveWindow(today, today.AddDate(0, 0, 6), 10) {
		t.Fatalf("expected live window for day+6 + 10 days")
	}

	if inLiveWindow(today, today.AddDate(0, 0, 7), 10) {
		t.Fatalf("expected outside live window for day+7 + 10 days")
	}

	if inLiveWindow(today, today.AddDate(0, 0, -1), 3) {
		t.Fatalf("expected past start date to be outside live window")
	}
}

func TestExtractWindow(t *testing.T) {
	days := []DailyRiskSummary{
		{Date: "2026-03-23"},
		{Date: "2026-03-24"},
		{Date: "2026-03-25"},
		{Date: "2026-03-26"},
	}

	start := time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC)
	window := extractWindow(days, start, 3)
	if len(window) != 3 {
		t.Fatalf("expected window length 3, got %d", len(window))
	}
	if window[0].Date != "2026-03-24" || window[2].Date != "2026-03-26" {
		t.Fatalf("unexpected window result: %+v", window)
	}

	missing := extractWindow(days, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), 2)
	if missing != nil {
		t.Fatalf("expected nil for missing start date")
	}
}

func TestGenerateLongRangeDaysShape(t *testing.T) {
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	out := generateLongRangeDays(41.33, 19.82, start, 10)
	if len(out) != 10 {
		t.Fatalf("expected 10 days, got %d", len(out))
	}

	first := out[0]
	last := out[9]
	if first.Date != "2026-10-05" || last.Date != "2026-10-14" {
		t.Fatalf("unexpected date range: %s -> %s", first.Date, last.Date)
	}
	if first.Confidence != "low" {
		t.Fatalf("expected low confidence, got %q", first.Confidence)
	}
	if first.WindDirection < 0 || first.WindDirection > 359 {
		t.Fatalf("invalid wind direction: %d", first.WindDirection)
	}
}
