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
	cacheRadiusKm = 1.0
	earthRadiusKm = 6371.0
)

type cacheEntry struct {
	lat, lon  float64
	forecast  *RiskForecast
	timestamp time.Time
}

var (
	cacheMu sync.RWMutex
	entries []*cacheEntry
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
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	for _, e := range entries {
		if time.Since(e.timestamp) > cacheTTL {
			continue
		}
		if haversineKm(lat, lon, e.lat, e.lon) <= cacheRadiusKm {
			return e.forecast
		}
	}
	return nil
}

func cachePut(lat, lon float64, f *RiskForecast) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	// Evict expired entries and overwrite an existing nearby entry if present.
	now := time.Now()
	fresh := entries[:0]
	replaced := false
	for _, e := range entries {
		if time.Since(e.timestamp) > cacheTTL {
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

func getForecast(lat, lon float64) (*RiskForecast, bool, error) {
	if cached := cacheGet(lat, lon); cached != nil {
		return cached, true, nil
	}
	ecmwf, spreads, sources, err := FetchAll(lat, lon)
	if err != nil {
		return nil, false, err
	}
	forecast, err := BuildRiskForecast(ecmwf, spreads, sources)
	if err != nil {
		return nil, false, err
	}
	cachePut(lat, lon, forecast)
	return forecast, false, nil
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
	limit := min(10, len(f.Days))
	days := make([]SimpleDay, 0, limit)
	for i := 0; i < limit; i++ {
		d := f.Days[i]
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
	forecast, hit, err := getForecast(lat, lon)
	if err != nil {
		log.Printf("getForecast error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
		writeJSONError(w, http.StatusBadGateway, "forecast upstream failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
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
	forecast, hit, err := getForecast(lat, lon)
	if err != nil {
		log.Printf("getForecast error path=%s lat=%.5f lon=%.5f err=%v", r.URL.Path, lat, lon, err)
		writeJSONError(w, http.StatusBadGateway, "forecast upstream failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	if err := json.NewEncoder(w).Encode(toSimpleDays(forecast)); err != nil {
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
	http.HandleFunc("/health", withRecovery("health", handleHealth))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("weather-risk-api listening on %s", addr)
	log.Printf("Usage: GET /forecast?lat=41.33&lon=19.82")
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
