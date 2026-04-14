# Weather Risk API

A Go-based microservice designed for high-precision weather risk assessment, specifically tailored for mountain and outdoor activity safety. It merges multi-model meteorological data into actionable risk signals.

## Core Functionality

### 1. Multi-Model Risk Assessment
The service calculates risk levels (**LOW**, **MODERATE**, **HIGH**) across six critical categories:
*   **Wind:** Analyzes surface gusts and high-altitude ridge winds (up to 5600m/500hPa).
*   **Precipitation:** Monitors total accumulation, peak intensity, and precipitation types.
*   **Visibility:** Tracks minimum horizontal visibility for navigation safety.
*   **Thunder:** Assesses convective potential using CAPE (Convective Available Potential Energy).
*   **Freezing:** Detects freezing rain/drizzle and calculates freezing level trends (vertical profiles).
*   **Thermal:** Evaluates cold stress and frostbite risk using the North American Wind Chill index.

### 2. Intelligent Data Integration
*   **Primary Source:** Merges **Open-Meteo** high-resolution surface data with **ECMWF** (European Centre for Medium-Range Weather Forecasts) upper-air data.
*   **Confidence Scoring:** Compares forecasts from **GFS**, **GEM**, and **ICON** models. High variance between these models triggers a "Low Confidence" signal in the API response.
*   **Climatological Fallback:** For requests beyond the 16-day live forecast window, the service generates a synthetic "Long Range Outlook" based on latitudinal climatology.

### 3. Resilience & Performance
*   **Spatial Cache:** Uses an in-memory cache with a **1 km radius** and **10-minute TTL**.
*   **Stale-While-Revalidate:** If upstream APIs fail, the service can serve "STALE" cached data for up to 6 hours to maintain availability.
*   **Concurrent Fetching:** Utilizes Go routines to query multiple weather endpoints in parallel.

## API Endpoints

### `GET /forecast`
Returns a comprehensive risk report, including:
*   **Guide Brief:** Dominant risk, secondary risks, and specific safety implications.
*   **Snapshot:** A 24-hour look at max/min values (Wind, Temp, Cloud cover, etc.).
*   **Daily Summaries:** 10-day forecast with segmented data (Morning, Afternoon, Evening, Night).

### `GET /forecast/simple`
A condensed JSON response focusing on "SimpleDay" objects, ideal for mobile dashboards or quick status checks.

### `GET /forecast/window`
Allows querying specific date ranges.
*   **Params:** `lat`, `lon`, `start_date` (YYYY-MM-DD), `days`.

### `GET /health`
Simple health check returning `{"status":"ok"}`.

## Deployment

The service is containerized and ready for cloud deployment:
*   **Port:** 8080 (configurable via `PORT` env var).
*   **CI/CD:** Includes `railway.toml` for seamless deployment on Railway.app.
*   **Runtime:** Go 1.22.
