package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// ── Spatial in-memory cache (1 km radius) ────────────────────────────────────

const (
	cacheTTL       = 10 * time.Minute
	cacheRadiusKm  = 1.0
	earthRadiusKm  = 6371.0
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
	Date             string  `json:"date"`
	Risk             string  `json:"risk"`
	Condition        string  `json:"condition"`
	TempMin          float64 `json:"temp_min"`
	TempMax          float64 `json:"temp_max"`
	WindMax          float64 `json:"wind_max"`
	GustMax          float64 `json:"gust_max"`
	PrecipMm         float64 `json:"precip_mm"`
	SnowCm           float64 `json:"snow_cm"`
	FreezingLevelM   *int    `json:"freezing_level_m"`
	VisibilityMin    float64 `json:"visibility_min_m"`
	Confidence       string  `json:"confidence"`
	DominantRisk     string  `json:"dominant_risk"`
}

type SimpleForecast struct {
	Timezone string      `json:"timezone"`
	Sources  []string    `json:"sources"`
	Days     []SimpleDay `json:"days"`
}

func toSimple(f *RiskForecast) SimpleForecast {
	days := make([]SimpleDay, len(f.Days))
	for i, d := range f.Days {
		// Pick the dominant condition across the day's segments
		cond := string(CondClear)
		for _, seg := range d.Segments {
			if conditionOrdinal(seg.Condition) > conditionOrdinal(Condition(cond)) {
				cond = string(seg.Condition)
			}
		}
		days[i] = SimpleDay{
			Date:           d.Date,
			Risk:           d.RiskLevel.String(),
			Condition:      cond,
			TempMin:        round1(d.TempMin),
			TempMax:        round1(d.TempMax),
			WindMax:        round1(d.WindMax),
			GustMax:        round1(d.GustMax),
			PrecipMm:       round1(d.PrecipSum),
			SnowCm:         round1(d.SnowSum),
			FreezingLevelM: d.FreezingLevelMin,
			VisibilityMin:  d.VisibilityMin,
			Confidence:     string(d.Confidence),
			DominantRisk:   string(d.DominantRisk),
		}
	}
	return SimpleForecast{Timezone: f.Timezone, Sources: f.Sources, Days: days}
}

func conditionOrdinal(c Condition) int {
	return map[Condition]int{
		CondClear: 0, CondPartly: 1, CondCloudy: 2,
		CondWindy: 3, CondRain: 4, CondSnow: 5, CondStorm: 6,
	}[c]
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func handleForecast(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("lat") == "" || r.URL.Query().Get("lon") == "" {
		http.Error(w, `{"error":"lat and lon query params required"}`, http.StatusBadRequest)
		return
	}
	lat, lon, err := parsLatLon(r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}
	forecast, hit, err := getForecast(lat, lon)
	if err != nil {
		log.Printf("getForecast error: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	json.NewEncoder(w).Encode(forecast)
}

func handleSimpleForecast(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("lat") == "" || r.URL.Query().Get("lon") == "" {
		http.Error(w, `{"error":"lat and lon query params required"}`, http.StatusBadRequest)
		return
	}
	lat, lon, err := parsLatLon(r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}
	forecast, hit, err := getForecast(lat, lon)
	if err != nil {
		log.Printf("getForecast error: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	json.NewEncoder(w).Encode(toSimple(forecast))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	http.HandleFunc("/forecast", handleForecast)
	http.HandleFunc("/forecast/simple", handleSimpleForecast)
	http.HandleFunc("/health", handleHealth)

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
