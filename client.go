package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
)

const baseURL = "https://api.open-meteo.com"

var ecmwfFields = "temperature_2m,wind_speed_10m,wind_gusts_10m,wind_direction_10m," +
	"precipitation,rain,snowfall,precipitation_type,visibility,cape,convective_inhibition," +
	"cloud_cover_low,cloud_cover_mid,cloud_cover_high," +
	"temperature_850hPa,temperature_700hPa,temperature_500hPa," +
	"geopotential_height_850hPa,geopotential_height_700hPa,geopotential_height_500hPa," +
	"wind_speed_850hPa,wind_speed_700hPa,wind_speed_500hPa," +
	"wind_direction_850hPa,wind_direction_700hPa,wind_direction_500hPa"

var spreadFields = "temperature_2m,wind_speed_10m,precipitation"

type modelResult struct {
	name     string
	response *OpenMeteoResponse
	err      error
}

// FetchAll calls ECMWF (required) + GFS/GEM/ICON (optional) in parallel.
func FetchAll(lat, lon float64) (ecmwf *OpenMeteoResponse, spread []*OpenMeteoResponse, sources []string, err error) {
	type endpoint struct {
		name   string
		path   string
		fields string
	}
	endpoints := []endpoint{
		{"ecmwf", "/v1/ecmwf", ecmwfFields},
		{"gfs", "/v1/gfs", spreadFields},
		{"gem", "/v1/gem", spreadFields},
		{"icon", "/v1/dwd-icon", spreadFields},
	}

	results := make([]modelResult, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(i int, ep endpoint) {
			defer wg.Done()
			resp, fetchErr := fetchModel(ep.path, ep.fields, lat, lon)
			results[i] = modelResult{name: ep.name, response: resp, err: fetchErr}
		}(i, ep)
	}
	wg.Wait()

	if results[0].err != nil || results[0].response == nil {
		return nil, nil, nil, fmt.Errorf("ECMWF fetch failed: %w", results[0].err)
	}

	ecmwf = results[0].response
	sources = []string{"ecmwf"}

	for _, r := range results[1:] {
		if r.err == nil && r.response != nil {
			spread = append(spread, r.response)
			sources = append(sources, r.name)
		}
	}
	return ecmwf, spread, sources, nil
}

func fetchModel(path, fields string, lat, lon float64) (*OpenMeteoResponse, error) {
	u, _ := url.Parse(baseURL + path)
	q := u.Query()
	q.Set("latitude", fmt.Sprintf("%f", lat))
	q.Set("longitude", fmt.Sprintf("%f", lon))
	q.Set("hourly", fields)
	q.Set("forecast_days", "10")
	q.Set("timezone", "auto")
	u.RawQuery = q.Encode()

	resp, err := http.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
	}

	var result OpenMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
