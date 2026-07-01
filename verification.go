package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── Forecast verification log ─────────────────────────────────────────────────
//
// Every freshly computed forecast is appended (as JSONL) with its issue time and
// per-day predictions. This is capture-only: it starts the clock on a gradeable
// forecast history so a later verification pass can score predictions against
// observations. It never touches the risk engine and must never break a request.
//
// Config:
//   VERIFICATION_LOG_PATH     file path for the JSONL log (default forecast_log.jsonl)
//   VERIFICATION_LOG_DISABLED set truthy to turn logging off entirely

const (
	verificationLogPathEnv     = "VERIFICATION_LOG_PATH"
	verificationLogDisabledEnv = "VERIFICATION_LOG_DISABLED"
	defaultVerificationLogPath = "forecast_log.jsonl"
)

// forecastLogRecord is one gradeable prediction: a single forecast day as issued.
type forecastLogRecord struct {
	IssuedAt       string   `json:"issued_at"` // RFC3339, UTC
	Lat            float64  `json:"lat"`
	Lon            float64  `json:"lon"`
	TargetDate     string   `json:"target_date"` // YYYY-MM-DD
	LeadDays       int      `json:"lead_days"`   // target_date - issue_date, in days
	RiskLevel      string   `json:"risk_level"`
	DominantRisk   string   `json:"dominant_risk"`
	Confidence     string   `json:"confidence"`
	TempMinC       float64  `json:"temp_min_c"`
	TempMaxC       float64  `json:"temp_max_c"`
	WindMaxKmh     float64  `json:"wind_max_kmh"`
	GustMaxKmh     float64  `json:"gust_max_kmh"`
	PrecipMm       float64  `json:"precip_mm"`
	SnowCm         float64  `json:"snow_cm"`
	VisibilityMinM float64  `json:"visibility_min_m"`
	Sources        []string `json:"sources"`
}

// verificationLogger appends forecast records as JSONL. The file handle is opened
// lazily on first write so constructing a logger (including at package init) has
// no filesystem side effects.
type verificationLogger struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

var (
	defaultVerificationLogger = newVerificationLoggerFromEnv()

	// logForecastSnapshot is invoked for each freshly computed forecast. It is a
	// package var so tests can swap it out and it stays off the request hot path
	// on cache hits.
	logForecastSnapshot = func(lat, lon float64, f *RiskForecast) {
		defaultVerificationLogger.record(lat, lon, f, time.Now().UTC())
	}
)

func newVerificationLoggerFromEnv() *verificationLogger {
	if isTruthyEnv(os.Getenv(verificationLogDisabledEnv)) {
		return nil
	}
	path := strings.TrimSpace(os.Getenv(verificationLogPathEnv))
	if path == "" {
		path = defaultVerificationLogPath
	}
	return &verificationLogger{path: path}
}

// record appends one line per forecast day. Safe to call on a nil logger (logging
// disabled) and recovers from any panic so it can never take down a request.
func (v *verificationLogger) record(lat, lon float64, f *RiskForecast, issuedAt time.Time) {
	if v == nil || f == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("verification log panic: %v", r)
		}
	}()

	records := buildForecastLogRecords(lat, lon, f, issuedAt)
	if len(records) == 0 {
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.f == nil {
		if err := v.open(); err != nil {
			log.Printf("verification log open failed path=%s err=%v", v.path, err)
			return
		}
	}

	enc := json.NewEncoder(v.f)
	for i := range records {
		if err := enc.Encode(&records[i]); err != nil {
			log.Printf("verification log write failed path=%s err=%v", v.path, err)
			return
		}
	}
}

func (v *verificationLogger) open() error {
	if dir := filepath.Dir(v.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(v.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	v.f = f
	return nil
}

// buildForecastLogRecords flattens a forecast into one gradeable record per day.
// Days with an unparseable date are skipped rather than logged with a bad lead time.
func buildForecastLogRecords(lat, lon float64, f *RiskForecast, issuedAt time.Time) []forecastLogRecord {
	if f == nil {
		return nil
	}
	issuedAt = issuedAt.UTC()
	issueDate := toDateOnly(issuedAt)

	out := make([]forecastLogRecord, 0, len(f.Days))
	for _, d := range f.Days {
		target, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			continue
		}
		leadDays := int(toDateOnly(target).Sub(issueDate).Hours() / 24)
		out = append(out, forecastLogRecord{
			IssuedAt:       issuedAt.Format(time.RFC3339),
			Lat:            lat,
			Lon:            lon,
			TargetDate:     d.Date,
			LeadDays:       leadDays,
			RiskLevel:      d.RiskLevel.String(),
			DominantRisk:   string(d.DominantRisk),
			Confidence:     string(d.Confidence),
			TempMinC:       round1(d.TempMin),
			TempMaxC:       round1(d.TempMax),
			WindMaxKmh:     round1(d.WindMax),
			GustMaxKmh:     round1(d.GustMax),
			PrecipMm:       round1(d.PrecipSum),
			SnowCm:         round1(d.SnowSum),
			VisibilityMinM: round1(d.VisibilityMin),
			Sources:        f.Sources,
		})
	}
	return out
}

func isTruthyEnv(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
