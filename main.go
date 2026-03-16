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

// ── HTTP handler ──────────────────────────────────────────────────────────────

func handleForecast(w http.ResponseWriter, r *http.Request) {
	latStr := r.URL.Query().Get("lat")
	lonStr := r.URL.Query().Get("lon")
	if latStr == "" || lonStr == "" {
		http.Error(w, `{"error":"lat and lon query params required"}`, http.StatusBadRequest)
		return
	}
	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid lat"}`, http.StatusBadRequest)
		return
	}
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid lon"}`, http.StatusBadRequest)
		return
	}

	if cached := cacheGet(lat, lon); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		json.NewEncoder(w).Encode(cached)
		return
	}

	ecmwf, spreads, sources, err := FetchAll(lat, lon)
	if err != nil {
		log.Printf("FetchAll error: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	forecast, err := BuildRiskForecast(ecmwf, spreads, sources)
	if err != nil {
		log.Printf("BuildRiskForecast error: %v", err)
		http.Error(w, `{"error":"failed to build forecast"}`, http.StatusInternalServerError)
		return
	}

	cachePut(lat, lon, forecast)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	json.NewEncoder(w).Encode(forecast)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	http.HandleFunc("/forecast", handleForecast)
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
