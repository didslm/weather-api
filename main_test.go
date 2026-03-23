package main

import (
	"fmt"
	"testing"
)

func TestToSimpleCapsAtTenDaysAndFormatsValues(t *testing.T) {
	days := make([]DailyRiskSummary, 0, 11)
	for i := 0; i < 11; i++ {
		days = append(days, DailyRiskSummary{
			Date:         fmt.Sprintf("2026-03-%02d", i+10),
			DominantRisk: CatPrecip,
			RiskLevel:    RiskModerate,
			TempMin:      4.14,
			TempMax:      11.86,
			WindMax:      28.04,
			GustMax:      41.66,
			PrecipSum:    12.25,
			SnowSum:      0,
			Confidence:   ConfHigh,
			Segments: []DaySegment{
				{Condition: CondRain},
				{Condition: CondCloudy},
			},
		})
	}

	simple := toSimpleDays(&RiskForecast{
		Timezone: "Europe/Berlin",
		Sources:  []string{"ecmwf", "gfs"},
		Days:     days,
	})

	if len(simple) != 10 {
		t.Fatalf("expected 10 days, got %d", len(simple))
	}

	day := simple[0]
	if day.Date != "2026-03-10" {
		t.Fatalf("unexpected date: %q", day.Date)
	}
	if day.Condition != "rain" {
		t.Fatalf("unexpected condition: %q", day.Condition)
	}
	if day.Summary != "Wet day" {
		t.Fatalf("unexpected summary: %q", day.Summary)
	}
	if day.Risk != "moderate" {
		t.Fatalf("unexpected risk: %q", day.Risk)
	}
	if day.Confidence != "high" {
		t.Fatalf("unexpected confidence: %q", day.Confidence)
	}
	if day.TempMinC != 4.1 || day.TempMaxC != 11.9 {
		t.Fatalf("unexpected temperatures: %.1f %.1f", day.TempMinC, day.TempMaxC)
	}
	if day.PrecipMm != 12.3 {
		t.Fatalf("unexpected precip: %.1f", day.PrecipMm)
	}
	if day.WindMaxKmh != 28.0 || day.GustMaxKmh != 41.7 {
		t.Fatalf("unexpected wind: %.1f %.1f", day.WindMaxKmh, day.GustMaxKmh)
	}
}

func TestSimpleConditionPriority(t *testing.T) {
	tests := []struct {
		name      string
		day       DailyRiskSummary
		condition string
		summary   string
	}{
		{
			name: "storm beats rain",
			day: DailyRiskSummary{
				DominantRisk: CatThunder,
				PrecipSum:    4,
				Segments: []DaySegment{
					{Condition: CondRain},
					{Condition: CondStorm},
				},
			},
			condition: "storm",
			summary:   "Thunderstorms possible",
		},
		{
			name: "low visibility when otherwise calm",
			day: DailyRiskSummary{
				VisibilityMin: 150,
				Segments: []DaySegment{
					{Condition: CondClear},
					{Condition: CondClear},
				},
			},
			condition: "low_visibility",
			summary:   "Very low visibility",
		},
		{
			name: "partly cloudy fallback",
			day: DailyRiskSummary{
				Segments: []DaySegment{
					{Condition: CondPartly},
					{Condition: CondCloudy},
				},
			},
			condition: "partly_cloudy",
			summary:   "Partly cloudy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition := simpleCondition(tt.day)
			if condition != tt.condition {
				t.Fatalf("expected %q, got %q", tt.condition, condition)
			}
			summary := simpleSummary(tt.day, condition)
			if summary != tt.summary {
				t.Fatalf("expected %q, got %q", tt.summary, summary)
			}
		})
	}
}

func TestBuildLiveWindowResponseUsesEnvelopeShape(t *testing.T) {
	forecast := &RiskForecast{
		Timezone: "Europe/Berlin",
		Sources:  []string{"ecmwf", "gfs"},
		Brief: GuideBrief{
			TopSignals: TopSignals{
				Confidence: ConfMedium,
			},
		},
		Days: []DailyRiskSummary{
			{
				Date:       "2026-03-23",
				RiskLevel:  RiskLow,
				Confidence: ConfHigh,
				Segments: []DaySegment{
					{Condition: CondClear},
					{Condition: CondPartly},
				},
			},
			{
				Date:       "2026-03-24",
				RiskLevel:  RiskModerate,
				Confidence: ConfMedium,
				Segments: []DaySegment{
					{Condition: CondRain},
					{Condition: CondCloudy},
				},
			},
		},
	}

	resp := buildLiveWindowResponse(forecast, "", 10, forecast.Days)
	if resp.Mode != "live_forecast" {
		t.Fatalf("unexpected mode: %q", resp.Mode)
	}
	if resp.Confidence != "medium" {
		t.Fatalf("unexpected confidence: %q", resp.Confidence)
	}
	if resp.Basis == "" {
		t.Fatalf("expected basis to be populated")
	}
	if resp.StartDate != "2026-03-23" {
		t.Fatalf("unexpected start date: %q", resp.StartDate)
	}
	if resp.DaysRequested != 10 {
		t.Fatalf("unexpected days requested: %d", resp.DaysRequested)
	}
	if len(resp.Days) != 2 {
		t.Fatalf("expected 2 day entries, got %d", len(resp.Days))
	}
	if resp.Timezone != "Europe/Berlin" {
		t.Fatalf("unexpected timezone: %q", resp.Timezone)
	}
	if len(resp.Sources) != 2 || resp.Sources[0] != "ecmwf" {
		t.Fatalf("unexpected sources: %+v", resp.Sources)
	}
}
