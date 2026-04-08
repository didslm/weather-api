package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const baseURL = "https://api.open-meteo.com"

var forecastHTTPClient = &http.Client{Timeout: 8 * time.Second}

// Surface fields from /v1/forecast (high-res, altitude-corrected).
var surfaceFields = "temperature_2m,wind_speed_10m,wind_gusts_10m,wind_direction_10m," +
	"precipitation,rain,snowfall,visibility,cape," +
	"cloud_cover_low,cloud_cover_mid,cloud_cover_high"

// Upper-air + special fields from /v1/ecmwf (unique to ECMWF).
var ecmwfFields = "precipitation_type,convective_inhibition," +
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

// FetchAll fetches surface data from /v1/forecast (high-res, altitude-corrected)
// and upper-air data from /v1/ecmwf, then merges them. GFS/GEM/ICON are fetched
// for spread/confidence.
func FetchAll(lat, lon float64, forecastDays int) (merged *OpenMeteoResponse, spread []*OpenMeteoResponse, sources []string, err error) {
	if forecastDays < 1 {
		forecastDays = 1
	}
	if forecastDays > 16 {
		forecastDays = 16
	}

	type endpoint struct {
		name   string
		path   string
		fields string
	}
	endpoints := []endpoint{
		{"forecast", "/v1/forecast", surfaceFields},
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
			resp, fetchErr := fetchModel(ep.path, ep.fields, lat, lon, forecastDays)
			results[i] = modelResult{name: ep.name, response: resp, err: fetchErr}
		}(i, ep)
	}
	wg.Wait()

	// Surface forecast is required.
	if results[0].err != nil || results[0].response == nil {
		if results[0].err != nil {
			return nil, nil, nil, fmt.Errorf("forecast fetch failed: %w", results[0].err)
		}
		return nil, nil, nil, fmt.Errorf("forecast fetch failed: empty response")
	}

	surface := results[0].response
	sources = []string{"forecast"}

	// Merge ECMWF upper-air data into the surface response.
	if results[1].err == nil && results[1].response != nil {
		mergeUpperAir(surface, results[1].response)
		sources = append(sources, "ecmwf")
	}
	merged = surface

	for _, r := range results[2:] {
		if r.err == nil && r.response != nil {
			spread = append(spread, r.response)
			sources = append(sources, r.name)
		}
	}
	return merged, spread, sources, nil
}

// mergeUpperAir copies ECMWF upper-air and special fields into the surface
// response, aligning by timestamp. If ECMWF has fewer time steps (e.g. for
// longer forecasts), missing hours get nil values which derefFloats handles
// with defaults.
func mergeUpperAir(surface, ecmwf *OpenMeteoResponse) {
	if surface.Hourly == nil || ecmwf.Hourly == nil {
		return
	}
	s := surface.Hourly
	e := ecmwf.Hourly

	// Build time-to-index lookup for ECMWF.
	eIdx := make(map[string]int, len(e.Time))
	for i, t := range e.Time {
		eIdx[t] = i
	}

	n := len(s.Time)
	type field struct {
		src  []*float64
		dest *[]*float64
	}
	fields := []field{
		{e.PrecipitationType, &s.PrecipitationType},
		{e.ConvectiveInhibition, &s.ConvectiveInhibition},
		{e.Temperature850hPa, &s.Temperature850hPa},
		{e.Temperature700hPa, &s.Temperature700hPa},
		{e.Temperature500hPa, &s.Temperature500hPa},
		{e.GeopotentialHeight850hPa, &s.GeopotentialHeight850hPa},
		{e.GeopotentialHeight700hPa, &s.GeopotentialHeight700hPa},
		{e.GeopotentialHeight500hPa, &s.GeopotentialHeight500hPa},
		{e.WindSpeed850hPa, &s.WindSpeed850hPa},
		{e.WindSpeed700hPa, &s.WindSpeed700hPa},
		{e.WindSpeed500hPa, &s.WindSpeed500hPa},
		{e.WindDirection850hPa, &s.WindDirection850hPa},
		{e.WindDirection700hPa, &s.WindDirection700hPa},
		{e.WindDirection500hPa, &s.WindDirection500hPa},
	}

	for _, f := range fields {
		if f.src == nil {
			continue
		}
		aligned := make([]*float64, n)
		for i, t := range s.Time {
			if j, ok := eIdx[t]; ok && j < len(f.src) {
				aligned[i] = f.src[j]
			}
		}
		*f.dest = aligned
	}
}

func fetchModel(path, fields string, lat, lon float64, forecastDays int) (*OpenMeteoResponse, error) {
	u, _ := url.Parse(baseURL + path)
	q := u.Query()
	q.Set("latitude", fmt.Sprintf("%f", lat))
	q.Set("longitude", fmt.Sprintf("%f", lon))
	q.Set("hourly", fields)
	q.Set("forecast_days", strconv.Itoa(forecastDays))
	q.Set("timezone", "auto")
	u.RawQuery = q.Encode()

	resp, err := forecastHTTPClient.Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("request %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d from %s body=%q", resp.StatusCode, path, string(body))
	}

	var result OpenMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode %s response failed: %w", path, err)
	}
	return &result, nil
}
