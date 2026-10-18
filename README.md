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
*   **Multi-Model Blending:** Surface temperature, wind, and precipitation are combined across **forecast/best-match**, **GFS**, **GEM**, and **ICON** using a per-timestamp **median**, so no single model's outlier run drives a risk signal. Blending runs on the consensus *before* elevation downscaling.
*   **Confidence Scoring:** Compares forecasts from **GFS**, **GEM**, and **ICON** models at matching timestamps, using the largest hourly temperature, wind, or precipitation spread. Confidence is measured from the *raw* forecasts before blending, separately for the brief and each day. Single-source hours or incomplete comparison coverage result in low confidence.
*   **Consistent Risk Signals:** Rain and snow amounts follow the blended precipitation total. Thermal risk includes air temperature during calm hours, and daily summaries use the same freezing-level rules as the brief. Missing upper-air temperatures remain unknown; they do not create a synthetic freezing level.
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

**Terrain downscaling (optional):** Pass `elevation` (or `alt`) in metres to correct the forecast from the coarse model gridpoint to the actual trailhead/summit elevation — e.g. `?lat=42.6&lon=20.1&elevation=2500`. Surface temperature is adjusted with a lapse rate derived from the model's own 850/700 hPa profile (falling back to the standard 6.5 °C/km), wind chill recomputes from the corrected temperature, and precipitation phase is reclassified (rain ⇄ snow) so a valley "rain" forecast surfaces as summit snow/ice. The applied correction is echoed in an `elevation` object on the response. Wind, gusts, and precip amount are **not** yet elevation-corrected. Accepted on `/forecast`, `/forecast/simple`, and the live window of `/forecast/window`.

### `GET /forecast/simple`
A condensed JSON response focusing on "SimpleDay" objects, ideal for mobile dashboards or quick status checks.

### `GET /forecast/window`
Allows querying specific date ranges.
*   **Params:** `lat`, `lon`, `start_date` (YYYY-MM-DD), `days`.

### `GET /health`
Simple health check returning `{"status":"ok"}`.

## Forecast Verification Log

Every freshly computed forecast is appended (as JSONL, one line per forecast day) with its issue time, lead time, and predictions. This is **capture-only** — it builds a gradeable forecast history so a later verification pass can score predictions against observations. It never touches the risk engine and is skipped on cache hits.

*   **`VERIFICATION_LOG_PATH`** — file path for the log (default `forecast_log.jsonl`). On Railway, point this at a mounted volume so history survives redeploys.
*   **`VERIFICATION_LOG_DISABLED`** — set truthy (`1`/`true`/`yes`/`on`) to turn logging off.

## Deployment

The service is containerized and ready for cloud deployment:
*   **Port:** 8080 (configurable via `PORT` env var).
*   **CI/CD:** Includes `railway.toml` for seamless deployment on Railway.app.
*   **Runtime:** Go 1.22.
