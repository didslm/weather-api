package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Spatial in-memory cache (1 km radius) ────────────────────────────────────

const (
	cacheTTL      = 10 * time.Minute
	staleCacheTTL = 6 * time.Hour
	cacheRadiusKm = 1.0
	earthRadiusKm = 6371.0
	liveMaxDays   = 16
)

type cacheEntry struct {
	lat, lon  float64
	forecast  *RiskForecast
	timestamp time.Time
}

var (
	cacheMu sync.RWMutex
	entries []*cacheEntry

	fetchAllFn          = FetchAll
	buildRiskForecastFn = BuildRiskForecast
)

type cacheStatus string

const (
	cacheMiss  cacheStatus = "MISS"
	cacheHit   cacheStatus = "HIT"
	cacheStale cacheStatus = "STALE"
)

// haversineKm returns the great-circle distance in kilometres between two points.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	lat1r := lat1 * math.Pi / 180
	lat2r := lat2 * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1r)*math.Cos(lat2r)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func cacheGet(lat, lon float64) *RiskForecast {
	forecast, _ := cacheGetWithin(lat, lon, cacheTTL)
	return forecast
}

func cacheGetWithin(lat, lon float64, maxAge time.Duration) (*RiskForecast, time.Duration) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	var best *cacheEntry
	bestAge := maxAge + time.Second
	for _, e := range entries {
		age := time.Since(e.timestamp)
		if age > maxAge {
			continue
		}
		if haversineKm(lat, lon, e.lat, e.lon) <= cacheRadiusKm {
			if best == nil || age < bestAge {
				best = e
				bestAge = age
			}
		}
	}
	if best == nil {
		return nil, 0
	}
	return best.forecast, bestAge
}

func cachePut(lat, lon float64, f *RiskForecast) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	// Retain entries long enough to serve bounded stale responses on upstream failure.
	now := time.Now()
	fresh := entries[:0]
	replaced := false
	for _, e := range entries {
		if time.Since(e.timestamp) > staleCacheTTL {
			continue // drop expired
		}
		if !replaced && haversineKm(lat, lon, e.lat, e.lon) <= cacheRadiusKm {
			e.lat, e.lon = lat, lon
			e.forecast = f
			e.timestamp = now
			replaced = true
		}
		fresh = append(fresh, e)
	}
	entries = fresh
	if !replaced {
		entries = append(entries, &cacheEntry{lat: lat, lon: lon, forecast: f, timestamp: now})
	}
}

// ── Shared fetch helper ───────────────────────────────────────────────────────

func parsLatLon(r *http.Request) (float64, float64, error) {
	lat, err := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid lat")
	}
	lon, err := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid lon")
	}
	return lat, lon, nil
}

func getForecast(lat, lon float64) (*RiskForecast, cacheStatus, error) {
	if cached := cacheGet(lat, lon); cached != nil {
		return cached, cacheHit, nil
	}
	ecmwf, spreads, sources, err := fetchAllFn(lat, lon, 10)
	if err != nil {
		if stale, age := cacheGetWithin(lat, lon, staleCacheTTL); stale != nil {
			log.Printf("serving stale forecast lat=%.5f lon=%.5f age=%s err=%v", lat, lon, age.Round(time.Second), err)
			return stale, cacheStale, nil
		}
		return nil, cacheMiss, err
	}
	forecast, err := buildRiskForecastFn(ecmwf, spreads, sources)
	if err != nil {
		return nil, cacheMiss, err
	}
	cachePut(lat, lon, forecast)
	return forecast, cacheMiss, nil
}

func getForecastForDays(lat, lon float64, forecastDays int) (*RiskForecast, error) {
	ecmwf, spreads, sources, err := fetchAllFn(lat, lon, forecastDays)
	if err != nil {
		return nil, err
	}
	return buildRiskForecastFn(ecmwf, spreads, sources)
}

// ── Simple forecast response ──────────────────────────────────────────────────

type SimpleDay struct {
	Date          string  `json:"date"`
	Summary       string  `json:"summary"`
	Condition     string  `json:"condition"`
	Risk          string  `json:"risk"`
	Confidence    string  `json:"confidence"`
	TempMinC      float64 `json:"temp_min_c"`
	TempMaxC      float64 `json:"temp_max_c"`
	PrecipMm      float64 `json:"precip_mm"`
	SnowCm        float64 `json:"snow_cm"`
	WindMaxKmh    float64 `json:"wind_max_kmh"`
	GustMaxKmh    float64 `json:"gust_max_kmh"`
	WindDirection int     `json:"wind_direction"`
	VisibilityKm  float64 `json:"visibility_km"`
}

func toSimpleDays(f *RiskForecast) []SimpleDay {
	return toSimpleDaysFromSlice(f.Days, 10)
}

func toSimpleDaysFromSlice(input []DailyRiskSummary, limit int) []SimpleDay {
	if limit < 1 {
		return []SimpleDay{}
	}
	limit = min(limit, len(input))
	days := make([]SimpleDay, 0, limit)
	for i := 0; i < limit; i++ {
		d := input[i]
		condition := simpleCondition(d)
		days = append(days, SimpleDay{
			Date:          d.Date,
			Summary:       simpleSummary(d, condition),
			Condition:     condition,
			Risk:          strings.ToLower(d.RiskLevel.String()),
			Confidence:    strings.ToLower(string(d.Confidence)),
			TempMinC:      round1(d.TempMin),
			TempMaxC:      round1(d.TempMax),
			PrecipMm:      round1(d.PrecipSum),
			SnowCm:        round1(d.SnowSum),
			WindMaxKmh:    round1(d.WindMax),
			GustMaxKmh:    round1(d.GustMax),
			WindDirection: d.WindDirection,
			VisibilityKm:  round1(d.VisibilityMin / 1000),
		})
	}
	return days
}

type WindowForecastResponse struct {
	StartDate     string      `json:"start_date"`
	DaysRequested int         `json:"days_requested"`
	Mode          string      `json:"mode"`
	Confidence    string      `json:"confidence"`
	Basis         string      `json:"basis"`
	Timezone      string      `json:"timezone"`
	Sources       []string    `json:"sources"`
	Days          []SimpleDay `json:"days"`
}

func buildLiveWindowResponse(forecast *RiskForecast, startDate string, days int, input []DailyRiskSummary) WindowForecastResponse {
	if days < 1 {
		days = 1
	}
	timezone := ""
	sources := []string{}
	confidence := "low"
	if forecast != nil {
		timezone = forecast.Timezone
		sources = forecast.Sources
		if c := strings.TrimSpace(string(forecast.Brief.TopSignals.Confidence)); c != "" {
			confidence = strings.ToLower(c)
		}
	}

	simple := toSimpleDaysFromSlice(input, days)
	if startDate == "" {
		if len(simple) > 0 {
			startDate = simple[0].Date
		} else {
			startDate = toDateOnly(time.Now().UTC()).Format("2006-01-02")
		}
	}

	return WindowForecastResponse{
		StartDate:     startDate,
		DaysRequested: days,
		Mode:          "live_forecast",
		Confidence:    confidence,
		Basis:         "open-meteo numerical weather prediction",
		Timezone:      timezone,
		Sources:       sources,
		Days:          simple,
	}
}

func simpleCondition(d DailyRiskSummary) string {
	switch {
	case hasSegmentCondition(d.Segments, CondStorm) || d.DominantRisk == CatThunder:
		return "storm"
	case d.SnowSum >= 1 || hasSegmentCondition(d.Segments, CondSnow):
		return "snow"
	case d.PrecipSum >= 1 || hasSegmentCondition(d.Segments, CondRain):
		return "rain"
	case d.VisibilityMin > 0 && d.VisibilityMin < 1000:
		return "low_visibility"
	case d.WindMax >= 35 || d.GustMax >= 50 || hasSegmentCondition(d.Segments, CondWindy):
		return "windy"
	}

	cloudySegments := 0
	partlySegments := 0
	for _, seg := range d.Segments {
		switch seg.Condition {
		case CondCloudy:
			cloudySegments++
		case CondPartly:
			partlySegments++
		}
	}

	switch {
	case cloudySegments >= 2:
		return "cloudy"
	case cloudySegments+partlySegments >= 2:
		return "partly_cloudy"
	default:
		return "clear"
	}
}

func simpleSummary(d DailyRiskSummary, condition string) string {
	switch condition {
	case "storm":
		return "Thunderstorms possible"
	case "snow":
		switch {
		case d.WindMax >= 35 || d.GustMax >= 50:
			return "Snow and wind"
		case d.SnowSum >= 5:
			return "Snow likely"
		default:
			return "Some snow"
		}
	case "rain":
		switch {
		case d.WindMax >= 35 || d.GustMax >= 50:
			return "Rain and wind"
		case d.PrecipSum >= 10:
			return "Wet day"
		default:
			return "Some rain"
		}
	case "low_visibility":
		if d.VisibilityMin < 200 {
			return "Very low visibility"
		}
		return "Low visibility"
	case "windy":
		if d.GustMax >= 70 {
			return "Very windy"
		}
		return "Windy"
	case "cloudy":
		return "Mostly cloudy"
	case "partly_cloudy":
		return "Partly cloudy"
	default:
		return "Mostly clear"
	}
}

func hasSegmentCondition(segments []DaySegment, target Condition) bool {
	for _, seg := range segments {
		if seg.Condition == target {
			return true
		}
	}
	return false
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func parseStartDate(r *http.Request) (time.Time, error) {
	startRaw := r.URL.Query().Get("start_date")
	if startRaw == "" {
		return time.Time{}, fmt.Errorf("start_date query param required (YYYY-MM-DD)")
	}
	startDate, err := time.Parse("2006-01-02", startRaw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid start_date, expected YYYY-MM-DD")
	}
	return startDate, nil
}

func parseDays(r *http.Request) (int, error) {
	daysRaw := strings.TrimSpace(r.URL.Query().Get("days"))
	if daysRaw == "" {
		return 10, nil
	}
	days, err := strconv.Atoi(daysRaw)
	if err != nil {
		return 0, fmt.Errorf("invalid days")
	}
	if days < 1 || days > 10 {
		return 0, fmt.Errorf("days must be between 1 and 10")
	}
	return days, nil
}

func toDateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func inLiveWindow(today, startDate time.Time, days int) bool {
	endDate := startDate.AddDate(0, 0, days-1)
	liveEnd := today.AddDate(0, 0, liveMaxDays-1)
	return !startDate.Before(today) && !endDate.After(liveEnd)
}

func extractWindow(days []DailyRiskSummary, startDate time.Time, count int) []DailyRiskSummary {
	startRaw := startDate.Format("2006-01-02")
	startIdx := -1
	for i, d := range days {
		if d.Date == startRaw {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return nil
	}
	endIdx := startIdx + count
	if endIdx > len(days) {
		return nil
	}
	return days[startIdx:endIdx]
}

func climateTempMean(lat float64, month time.Month) float64 {
	absLat := math.Abs(lat)
	zoneBase := 28.0 - 0.45*absLat
	if zoneBase < -12 {
		zoneBase = -12
	}

	seasonPhase := 2 * math.Pi * (float64(month) - 7.0) / 12.0
	seasonAmp := 4.0 + absLat*0.16
	season := seasonAmp * math.Cos(seasonPhase)
	if lat < 0 {
		season = -season
	}
	return zoneBase + season
}

func generateLongRangeDays(lat, lon float64, startDate time.Time, days int) []SimpleDay {
	out := make([]SimpleDay, 0, days)
	for i := 0; i < days; i++ {
		d := startDate.AddDate(0, 0, i)
		mean := climateTempMean(lat, d.Month())
		absLat := math.Abs(lat)
		spread := 4.0 + absLat*0.06
		dailyWave := math.Sin(float64(d.YearDay())*0.31 + lat*0.03 + lon*0.02)

		tempMin := mean - spread + dailyWave*1.1
		tempMax := mean + spread + dailyWave*1.3

		precip := math.Max(0, 1.2+2.2*math.Abs(math.Sin(float64(d.Month())*0.55))+dailyWave*1.5)
		wind := math.Max(6, 16+absLat*0.12+dailyWave*4)
		gust := wind * 1.45
		visibility := math.Max(4, 11-precip*0.7)
		snow := 0.0
		condition := "partly_cloudy"
		summary := "Seasonal outlook"
		risk := "low"

		switch {
		case tempMax <= 1 && precip >= 2:
			snow = precip * 0.9
			condition = "snow"
			summary = "Snow possible (climatology)"
			risk = "moderate"
		case precip >= 5:
			condition = "rain"
			summary = "Rain possible (climatology)"
			risk = "moderate"
		case gust >= 55:
			condition = "windy"
			summary = "Windy spell possible"
			risk = "moderate"
		case precip < 1:
			condition = "clear"
			summary = "Mostly dry pattern"
		}

		out = append(out, SimpleDay{
			Date:          d.Format("2006-01-02"),
			Summary:       summary,
			Condition:     condition,
			Risk:          risk,
			Confidence:    "low",
			TempMinC:      round1(tempMin),
			TempMaxC:      round1(tempMax),
			PrecipMm:      round1(precip),
			SnowCm:        round1(snow),
			WindMaxKmh:    round1(wind),
			GustMaxKmh:    round1(gust),
			WindDirection: int(math.Mod(math.Mod(200+float64(i)*19+lon*0.4, 360)+360, 360)),
			VisibilityKm:  round1(visibility),
		})
	}
	return out
}

func handleWindowForecast(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("lat") == "" || r.URL.Query().Get("lon") == "" {
		writeJSONError(w, http.StatusBadRequest, "lat and lon query params required")
		return
	}
	lat, lon, err := parsLatLon(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	startDate, err := parseStartDate(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	days, err := parseDays(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	startDate = toDateOnly(startDate)
	today := toDateOnly(time.Now().UTC())

	w.Header().Set("Content-Type", "application/json")

	if inLiveWindow(today, startDate, days) {
		forecast, fetchErr := getForecastForDays(lat, lon, liveMaxDays)
		if fetchErr != nil {
			log.Printf("getForecastForDays error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, fetchErr)
			writeJSONError(w, http.StatusBadGateway, "forecast upstream failed")
			return
		}
		window := extractWindow(forecast.Days, startDate, days)
		if len(window) != days {
			writeJSONError(w, http.StatusBadGateway, "forecast window unavailable from upstream")
			return
		}
		resp := buildLiveWindowResponse(forecast, startDate.Format("2006-01-02"), days, window)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("response encode error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
		}
		return
	}

	resp := WindowForecastResponse{
		StartDate:     startDate.Format("2006-01-02"),
		DaysRequested: days,
		Mode:          "long_range_outlook",
		Confidence:    "low",
		Basis:         "climatology-style synthetic baseline (placeholder)",
		Timezone:      "UTC",
		Sources:       []string{"internal_placeholder_normals_v1"},
		Days:          generateLongRangeDays(lat, lon, startDate, days),
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("response encode error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
	}
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func handleForecast(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("lat") == "" || r.URL.Query().Get("lon") == "" {
		writeJSONError(w, http.StatusBadRequest, "lat and lon query params required")
		return
	}
	lat, lon, err := parsLatLon(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	forecast, status, err := getForecast(lat, lon)
	if err != nil {
		log.Printf("getForecast error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
		writeJSONError(w, http.StatusBadGateway, "forecast upstream failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", string(status))
	if err := json.NewEncoder(w).Encode(forecast); err != nil {
		log.Printf("response encode error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
	}
}

func handleSimpleForecast(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("lat") == "" || r.URL.Query().Get("lon") == "" {
		writeJSONError(w, http.StatusBadRequest, "lat and lon query params required")
		return
	}
	lat, lon, err := parsLatLon(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	forecast, status, err := getForecast(lat, lon)
	if err != nil {
		log.Printf("getForecast error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
		writeJSONError(w, http.StatusBadGateway, "forecast upstream failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", string(status))

	resp := buildLiveWindowResponse(forecast, "", 10, forecast.Days)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("response encode error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func withRecovery(name string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic route=%s method=%s path=%s query=%q panic=%v\n%s", name, r.Method, r.URL.Path, r.URL.RawQuery, rec, debug.Stack())
				writeJSONError(w, http.StatusBadGateway, "forecast processing failed")
			}
			log.Printf("request route=%s method=%s path=%s duration_ms=%d", name, r.Method, r.URL.Path, time.Since(start).Milliseconds())
		}()
		next(w, r)
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	http.HandleFunc("/forecast", withRecovery("forecast", handleForecast))
	http.HandleFunc("/forecast/simple", withRecovery("forecast_simple", handleSimpleForecast))
	http.HandleFunc("/forecast/window", withRecovery("forecast_window", handleWindowForecast))
	http.HandleFunc("/health", withRecovery("health", handleHealth))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("weather-risk-api listening on %s", addr)
	log.Printf("Usage: GET /forecast?lat=41.33&lon=19.82")
	log.Printf("Usage: GET /forecast/window?lat=41.33&lon=19.82&start_date=2026-04-01&days=10")
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
