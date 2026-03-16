package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// ── Internal series types ─────────────────────────────────────────────────────

type hourlySeries struct {
	time             []string
	temperature2m    []float64
	windSpeed10m     []float64
	windGusts10m     []float64
	windDirection10m []float64
	precipitation    []float64
	rain             []float64
	snowfall         []float64
	precipType       []int
	visibility       []float64
	cape             []float64
	cin              []float64
	cloudLow         []float64
	cloudMid         []float64
	cloudHigh        []float64
	temp850          []float64
	temp700          []float64
	temp500          []float64
	height850        []float64
	height700        []float64
	height500        []float64
	wind850          []float64
	wind700          []float64
	wind500          []float64
	windDir850       []float64
	windDir700       []float64
	windDir500       []float64
}

type spreadSeries struct {
	time          []string
	temperature2m []float64
	windSpeed10m  []float64
	precipitation []float64
}

type precipWindow struct {
	total float64
	start *string
	end   *string
	peak  *string
	ptype string
}

type freezingData struct {
	minLevel  *int
	trend     int
	below1500 bool
	above5600 bool
}

// ── Entry point ───────────────────────────────────────────────────────────────

func BuildRiskForecast(
	ecmwfResp *OpenMeteoResponse,
	spreadResps []*OpenMeteoResponse,
	sources []string,
) (*RiskForecast, error) {
	ecmwf := toHourlySeries(ecmwfResp)
	if ecmwf == nil {
		return nil, fmt.Errorf("failed to parse ECMWF response")
	}

	var spreads []*spreadSeries
	for _, r := range spreadResps {
		if s := toSpreadSeries(r); s != nil {
			spreads = append(spreads, s)
		}
	}

	brief := buildGuideBrief(ecmwf, spreads)
	snapshot := buildSnapshot(ecmwf)
	days := buildDailySummaries(ecmwf)

	tz := ""
	if ecmwfResp != nil {
		tz = ecmwfResp.Timezone
	}

	return &RiskForecast{
		Timezone: tz,
		Sources:  sources,
		Brief:    brief,
		Snapshot: snapshot,
		Days:     days,
	}, nil
}

// ── Guide brief ───────────────────────────────────────────────────────────────

func buildGuideBrief(s *hourlySeries, spreads []*spreadSeries) GuideBrief {
	horizon := min(24, len(s.time))

	maxRidgeWind := maxSlice(s.wind700[:horizon])
	maxGust := maxSlice(s.windGusts10m[:horizon])
	minVis := minSlice(s.visibility[:horizon])
	maxCape := maxSlice(s.cape[:horizon])
	maxPrecipRate := maxSlice(s.precipitation[:horizon])
	totalPrecip := sumSlice(s.precipitation[:horizon])
	minWindChill := windChillMin(s, horizon)
	tempMin := minSlice(s.temperature2m[:horizon])
	freezingFlags := hasFreezingPrecip(s.precipType[:horizon])

	windLevel := windRiskLevel(maxRidgeWind, maxGust)
	precipLevel := precipRiskLevel(totalPrecip, maxPrecipRate)
	visLevel := visibilityRiskLevel(minVis)
	thunderLevel := thunderRiskLevel(maxCape)
	freezeLevel := freezingRiskLevel(freezingFlags, s, horizon)
	thermalLevel := thermalRiskLevel(minWindChill)

	riskLevels := []struct {
		cat   RiskCategory
		level RiskLevel
	}{
		{CatWind, windLevel},
		{CatPrecip, precipLevel},
		{CatVisibility, visLevel},
		{CatThunder, thunderLevel},
		{CatFreezing, freezeLevel},
		{CatThermal, thermalLevel},
	}

	dominant := dominantRisk(riskLevels)
	var secondary []RiskCategory
	for _, r := range riskLevels {
		if r.cat != dominant && r.level != RiskLow {
			secondary = append(secondary, r.cat)
		}
	}

	confidence := confidenceInfo(s, spreads, horizon)

	return GuideBrief{
		TopSignals: buildTopSignals(maxRidgeWind, maxGust, totalPrecip, maxPrecipRate, minWindChill, tempMin, confidence.level),
		ConfidenceDetail: confidence.detail,
		DominantRisk:    dominant,
		SecondaryRisks:  orEmpty(secondary),
		TimingNotes:     timingNotes(s, horizon),
		ThresholdNotes:  thresholdNotes(dominant),
		Implications:    implications(dominant),
		RiskDrivers:     riskDrivers(s, horizon),
		KeyRisks:        buildKeyRisks(s, horizon, windLevel, precipLevel, visLevel, thunderLevel, freezeLevel, thermalLevel, dominant),
	}
}

// ── Snapshot ──────────────────────────────────────────────────────────────────

func buildSnapshot(s *hourlySeries) RiskSnapshot {
	horizon := min(24, len(s.time))
	pw := calcPrecipWindow(s, horizon)
	fd := calcFreezingData(s, horizon)
	chill := windChillMin(s, horizon)
	cinMin := minSlicePtrOrNil(s.cin[:horizon])

	return RiskSnapshot{
		WindSurfaceMax:    maxSlice(s.windSpeed10m[:horizon]),
		WindGustMax:       maxSlice(s.windGusts10m[:horizon]),
		Ridge1500Max:      maxSlice(s.wind850[:horizon]),
		Ridge3000Max:      maxSlice(s.wind700[:horizon]),
		Ridge5600Max:      maxSlice(s.wind500[:horizon]),
		PrecipTotal:       pw.total,
		PrecipStart:       pw.start,
		PrecipEnd:         pw.end,
		PrecipPeak:        pw.peak,
		PrecipType:        pw.ptype,
		FreezingLevelMin:  fd.minLevel,
		FreezingTrend:     fd.trend,
		FreezingBelow1500: fd.below1500,
		FreezingAbove5600: fd.above5600,
		CloudLowMax:       maxSlice(s.cloudLow[:horizon]),
		CloudMidMax:       maxSlice(s.cloudMid[:horizon]),
		CloudHighMax:      maxSlice(s.cloudHigh[:horizon]),
		CloudDescriptor:   cloudDescriptor(s, horizon),
		VisibilityMin:     minSlice(s.visibility[:horizon]),
		TempMin:           minSlice(s.temperature2m[:horizon]),
		TempMax:           maxSlice(s.temperature2m[:horizon]),
		WindChillMin:      chill,
		SnowTotal:         sumSlice(s.snowfall[:horizon]),
		WindLoading:       hasWindLoading(s, horizon),
		CapeMax:           maxSlice(s.cape[:horizon]),
		CinMin:            cinMin,
	}
}

// ── Daily summaries ───────────────────────────────────────────────────────────

func buildDailySummaries(s *hourlySeries) []DailyRiskSummary {
	// Group hourly indices by date
	dateIndex := map[string][]int{}
	dateOrder := []string{}
	for i, t := range s.time {
		date := strings.SplitN(t, "T", 2)[0]
		if _, ok := dateIndex[date]; !ok {
			dateOrder = append(dateOrder, date)
		}
		dateIndex[date] = append(dateIndex[date], i)
	}

	var summaries []DailyRiskSummary
	for _, date := range dateOrder {
		indices := dateIndex[date]

		temps := pickFloats(s.temperature2m, indices)
		winds := pickFloats(s.windSpeed10m, indices)
		gusts := pickFloats(s.windGusts10m, indices)
		windDirDaily := 0
		if len(indices) > 0 {
			maxWIdx := indices[0]
			for _, idx := range indices {
				if s.windSpeed10m[idx] > s.windSpeed10m[maxWIdx] {
					maxWIdx = idx
				}
			}
			windDirDaily = int(math.Round(s.windDirection10m[maxWIdx]))
		}
		precip := pickFloats(s.precipitation, indices)
		snow := pickFloats(s.snowfall, indices)
		ridge := pickFloats(s.wind700, indices)
		vis := pickFloats(s.visibility, indices)
		cape := pickFloats(s.cape, indices)
		ptypes := pickInts(s.precipType, indices)

		maxRidge := maxSlice(ridge)
		maxGust := maxSlice(gusts)
		minVis := minSlice(vis)
		maxCape := maxSlice(cape)
		totalPrecip := sumSlice(precip)
		maxPrecipRate := maxSlice(precip)
		freezingFlags := hasFreezingPrecip(ptypes)

		// Wind chill for thermal risk
		var windChills []float64
		for _, idx := range indices {
			temp := s.temperature2m[idx]
			wind := s.windSpeed10m[idx]
			if wind > 4.8 && temp < 10 {
				chill := 13.12 + 0.6215*temp - 11.37*math.Pow(wind, 0.16) + 0.3965*temp*math.Pow(wind, 0.16)
				windChills = append(windChills, chill)
			}
		}
		var minChill *float64
		if len(windChills) > 0 {
			v := minSlice(windChills)
			minChill = &v
		}

		windLevel := windRiskLevel(maxRidge, maxGust)
		precipLevel := precipRiskLevel(totalPrecip, maxPrecipRate)
		visLevel := visibilityRiskLevel(minVis)
		thunderLevel := thunderRiskLevel(maxCape)
		var freezeLevel RiskLevel
		if freezingFlags {
			freezeLevel = RiskHigh
		}
		thermalLevel := thermalRiskLevel(minChill)

		riskLevels := []struct {
			cat   RiskCategory
			level RiskLevel
		}{
			{CatWind, windLevel},
			{CatPrecip, precipLevel},
			{CatVisibility, visLevel},
			{CatThunder, thunderLevel},
			{CatFreezing, freezeLevel},
			{CatThermal, thermalLevel},
		}
		dailyDominant := dominantRisk(riskLevels)

		individual := []RiskLevel{windLevel, precipLevel, visLevel, thunderLevel, freezeLevel, thermalLevel}
		maxRisk := maxRiskLevel(individual)
		moderateCount := 0
		for _, r := range individual {
			if r == RiskModerate {
				moderateCount++
			}
		}
		var overallRisk RiskLevel
		switch {
		case maxRisk == RiskHigh:
			overallRisk = RiskHigh
		case moderateCount >= 3:
			overallRisk = RiskHigh
		case maxRisk == RiskModerate:
			overallRisk = RiskModerate
		default:
			overallRisk = RiskLow
		}

		// Freezing level for this day
		var freezingLevels []float64
		for _, idx := range indices {
			if fl := freezingLevelAt(s.temp850[idx], s.temp700[idx], s.temp500[idx], s.height850[idx], s.height700[idx], s.height500[idx]); fl != nil {
				freezingLevels = append(freezingLevels, *fl)
			}
		}
		var freezingMin *int
		freezingTrendVal := 0
		if len(freezingLevels) > 0 {
			v := int(minSlice(freezingLevels))
			freezingMin = &v
			if len(freezingLevels) > 1 {
				delta := freezingLevels[len(freezingLevels)-1] - freezingLevels[0]
				switch {
				case delta > 200:
					freezingTrendVal = 1
				case delta < -200:
					freezingTrendVal = -1
				}
			}
		}

		confidenceEst := ConfHigh
		if maxRidge > 60 || totalPrecip > 30 || minVis < 500 {
			confidenceEst = ConfMedium
		}

		summaries = append(summaries, DailyRiskSummary{
			Date:             date,
			DominantRisk:     dailyDominant,
			RiskLevel:        overallRisk,
			TempMin:          minSlice(temps),
			TempMax:          maxSlice(temps),
			WindMax:          maxSlice(winds),
			GustMax:          maxSlice(gusts),
			WindDirection:    windDirDaily,
			PrecipSum:        sumSlice(precip),
			SnowSum:          sumSlice(snow),
			FreezingLevelMin: freezingMin,
			FreezingTrend:    freezingTrendVal,
			VisibilityMin:    minVis,
			Confidence:       confidenceEst,
			Segments:         buildDaySegments(s, indices),
		})
	}
	return summaries
}

func buildDaySegments(s *hourlySeries, indices []int) []DaySegment {
	type part struct {
		label DaySegmentLabel
		hours []int
	}
	parts := []part{
		{SegNight, rangeInts(0, 5)},
		{SegMorning, rangeInts(6, 11)},
		{SegAfternoon, rangeInts(12, 17)},
		{SegEvening, rangeInts(18, 23)},
	}
	var segments []DaySegment
	for _, p := range parts {
		var partIndices []int
		for _, idx := range indices {
			h := hourFromTime(s.time[idx])
			if contains(p.hours, h) {
				partIndices = append(partIndices, idx)
			}
		}
		temps := pickFloats(s.temperature2m, partIndices)
		winds := pickFloats(s.windSpeed10m, partIndices)
		gusts := pickFloats(s.windGusts10m, partIndices)
		precip := pickFloats(s.precipitation, partIndices)

		windDir := 0
		if len(partIndices) > 0 {
			maxWIdx := partIndices[0]
			for _, idx := range partIndices {
				if s.windSpeed10m[idx] > s.windSpeed10m[maxWIdx] {
					maxWIdx = idx
				}
			}
			windDir = int(math.Round(s.windDirection10m[maxWIdx]))
		}

		segments = append(segments, DaySegment{
			Label:         p.label,
			Condition:     dominantCondition(s, partIndices),
			TempLow:       safeMin(temps),
			TempHigh:      safeMax(temps),
			WindMax:       safeMax(winds),
			GustMax:       safeMax(gusts),
			WindDirection: windDir,
			PrecipAmount:  sumSlice(precip),
		})
	}
	return segments
}

func dominantCondition(s *hourlySeries, indices []int) Condition {
	if len(indices) == 0 {
		return CondClear
	}
	maxCape := maxSlice(pickFloats(s.cape, indices))
	maxWind := maxSlice(pickFloats(s.windSpeed10m, indices))
	precipSum := sumSlice(pickFloats(s.precipitation, indices))
	snowSum := sumSlice(pickFloats(s.snowfall, indices))
	ptypes := pickInts(s.precipType, indices)
	cloudAvg := avgSlice(pickMaxCloud(s, indices))

	switch {
	case maxCape >= 400:
		return CondStorm
	case isSnowBlock(ptypes, snowSum):
		return CondSnow
	case precipSum >= 0.5:
		return CondRain
	case maxWind >= 35:
		return CondWindy
	case cloudAvg < 25:
		return CondClear
	case cloudAvg < 60:
		return CondPartly
	default:
		return CondCloudy
	}
}

// ── Risk level functions ──────────────────────────────────────────────────────

func windRiskLevel(maxRidge, maxGust float64) RiskLevel {
	switch {
	case maxRidge >= 60 || maxGust >= 70:
		return RiskHigh
	case maxRidge >= 45 || maxGust >= 50:
		return RiskModerate
	default:
		return RiskLow
	}
}

func precipRiskLevel(total, maxRate float64) RiskLevel {
	switch {
	case maxRate >= 5:
		return RiskHigh
	case maxRate >= 2:
		return RiskModerate
	case total >= 30:
		return RiskHigh
	case total >= 10:
		return RiskModerate
	default:
		return RiskLow
	}
}

func visibilityRiskLevel(minVis float64) RiskLevel {
	switch {
	case minVis < 200:
		return RiskHigh
	case minVis < 1000:
		return RiskModerate
	default:
		return RiskLow
	}
}

func thunderRiskLevel(maxCape float64) RiskLevel {
	switch {
	case maxCape >= 1500:
		return RiskHigh
	case maxCape >= 600:
		return RiskModerate
	default:
		return RiskLow
	}
}

func freezingRiskLevel(freezingFlags bool, s *hourlySeries, horizon int) RiskLevel {
	if freezingFlags {
		return RiskHigh
	}
	fd := calcFreezingData(s, horizon)
	switch {
	case fd.below1500:
		return RiskModerate
	case fd.minLevel != nil && *fd.minLevel <= 2200:
		return RiskModerate
	default:
		return RiskLow
	}
}

func thermalRiskLevel(minChill *float64) RiskLevel {
	if minChill == nil {
		return RiskLow
	}
	switch {
	case *minChill <= -10:
		return RiskHigh
	case *minChill <= 0:
		return RiskModerate
	default:
		return RiskLow
	}
}

func dominantRisk(levels []struct {
	cat   RiskCategory
	level RiskLevel
}) RiskCategory {
	if len(levels) == 0 {
		return CatNone
	}
	maxOrd := RiskLow
	for _, r := range levels {
		if r.level > maxOrd {
			maxOrd = r.level
		}
	}
	if maxOrd == RiskLow {
		return CatNone
	}
	priority := []RiskCategory{CatWind, CatThunder, CatPrecip, CatVisibility, CatFreezing, CatThermal}
	for _, p := range priority {
		for _, r := range levels {
			if r.cat == p && r.level == maxOrd {
				return p
			}
		}
	}
	return CatNone
}

// ── Confidence ────────────────────────────────────────────────────────────────

type confidence struct {
	level  ConfidenceLevel
	detail string
}

func confidenceInfo(ecmwf *hourlySeries, spreads []*spreadSeries, horizon int) confidence {
	if len(spreads) == 0 {
		return confidence{ConfLow, "Single-source"}
	}

	var temps, winds, precips []float64
	temps = append(temps, avgSlice(ecmwf.temperature2m[:horizon]))
	winds = append(winds, avgSlice(ecmwf.windSpeed10m[:horizon]))
	precips = append(precips, avgSlice(ecmwf.precipitation[:horizon]))

	for _, sp := range spreads {
		h := min(horizon, len(sp.time))
		temps = append(temps, avgSlice(sp.temperature2m[:h]))
		winds = append(winds, avgSlice(sp.windSpeed10m[:h]))
		precips = append(precips, avgSlice(sp.precipitation[:h]))
	}

	tempSpread := maxSlice(temps) - minSlice(temps)
	windSpread := maxSlice(winds) - minSlice(winds)
	precipSpread := maxSlice(precips) - minSlice(precips)

	var lvl ConfidenceLevel
	switch {
	case windSpread >= 12 || precipSpread >= 3 || tempSpread >= 4:
		lvl = ConfLow
	case windSpread >= 6 || precipSpread >= 1.5 || tempSpread >= 2:
		lvl = ConfMedium
	default:
		lvl = ConfHigh
	}

	detail := fmt.Sprintf("Spread wind %.0f km/h | precip %.1f mm/h | temp %.1f°", windSpread, precipSpread, tempSpread)
	return confidence{lvl, detail}
}

// ── Top signals ───────────────────────────────────────────────────────────────

func buildTopSignals(maxRidge, maxGust, totalPrecip, maxPrecipRate float64, minChill *float64, tempMin float64, conf ConfidenceLevel) TopSignals {
	return TopSignals{
		Wind:       windSignal(maxRidge, maxGust),
		TempFeel:   tempSignal(minChill, tempMin),
		Rain:       rainSignal(totalPrecip, maxPrecipRate),
		Exposure:   maxRidge >= 45 || maxGust >= 50,
		Confidence: conf,
	}
}

func windSignal(maxRidge, maxGust float64) WindSignal {
	switch {
	case maxRidge >= 70 || maxGust >= 80:
		return WindVeryStrong
	case maxRidge >= 50 || maxGust >= 60:
		return WindStrong
	case maxRidge >= 25 || maxGust >= 35:
		return WindBreezy
	default:
		return WindCalm
	}
}

func tempSignal(minChill *float64, tempMin float64) TempSignal {
	feel := tempMin
	if minChill != nil {
		feel = *minChill
	}
	switch {
	case feel <= -10:
		return TempVeryCold
	case feel <= 0:
		return TempCold
	case feel <= 8:
		return TempCool
	default:
		return TempComfortable
	}
}

func rainSignal(total, maxRate float64) RainSignal {
	switch {
	case total >= 5 || maxRate >= 1.5:
		return RainLikely
	case total >= 1 || maxRate >= 0.5:
		return RainLight
	default:
		return RainDry
	}
}

// ── Risk drivers ──────────────────────────────────────────────────────────────

func riskDrivers(s *hourlySeries, horizon int) []RiskDriver {
	var wind, precip, vis, thunder, freezing int
	for i := 0; i < horizon; i++ {
		if s.wind700[i] >= 45 || s.windGusts10m[i] >= 50 {
			wind++
		}
		if s.precipitation[i] >= 2 {
			precip++
		}
		if s.visibility[i] < 1000 {
			vis++
		}
		if s.cape[i] >= 400 {
			thunder++
		}
		if isFreezingPrecip(s.precipType[i]) {
			freezing++
		}
	}
	total := wind + precip + vis + thunder + freezing
	if total == 0 {
		return nil
	}
	counts := []struct {
		cat   RiskCategory
		count int
	}{
		{CatWind, wind}, {CatPrecip, precip}, {CatVisibility, vis}, {CatThunder, thunder}, {CatFreezing, freezing},
	}
	var drivers []RiskDriver
	for _, c := range counts {
		if c.count > 0 {
			drivers = append(drivers, RiskDriver{c.cat, int(math.Round(float64(c.count) / float64(total) * 100))})
		}
	}
	sort.Slice(drivers, func(i, j int) bool { return drivers[i].Percent > drivers[j].Percent })
	return drivers
}

// ── Key risks ─────────────────────────────────────────────────────────────────

func buildKeyRisks(
	s *hourlySeries, horizon int,
	windLevel, precipLevel, visLevel, thunderLevel, freezeLevel, thermalLevel RiskLevel,
	dominant RiskCategory,
) []RiskKey {
	maxRidge := maxSlice(s.wind700[:horizon])
	maxGust := maxSlice(s.windGusts10m[:horizon])
	pw := calcPrecipWindow(s, horizon)
	fd := calcFreezingData(s, horizon)
	minVis := minSlice(s.visibility[:horizon])
	maxCape := maxSlice(s.cape[:horizon])

	thermalVal := thermalValue(s, horizon)

	all := []RiskKey{
		{KeyRidgeWind, windLevel, fmt.Sprintf("%.0f km/h", maxRidge)},
		{KeyGusts, windLevel, fmt.Sprintf("%.0f/%.0f km/h", maxGust, maxRidge)},
		{KeyPrecip, precipLevel, fmt.Sprintf("%.1f mm", pw.total)},
		{KeyFreezingLevel, freezeLevel, formatFreezingLevel(fd)},
		{KeyVisibility, visLevel, formatVisibility(minVis)},
		{KeyThunder, thunderLevel, fmt.Sprintf("%.0f J/kg", maxCape)},
		{KeyThermal, thermalLevel, thermalVal},
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Level != all[j].Level {
			return all[i].Level > all[j].Level
		}
		return string(all[i].Type) < string(all[j].Type)
	})

	top := all
	if len(top) > 5 {
		top = top[:5]
	}

	// Ensure dominant risk is represented
	dominantKeyType := dominantToKeyType(dominant)
	if dominantKeyType != "" {
		has := false
		for _, k := range top {
			if k.Type == dominantKeyType {
				has = true
				break
			}
		}
		if !has {
			for _, k := range all {
				if k.Type == dominantKeyType {
					if len(top) > 0 {
						top[len(top)-1] = k
					}
					break
				}
			}
		}
	}

	// Deduplicate by type
	seen := map[RiskKeyType]bool{}
	var result []RiskKey
	for _, k := range top {
		if !seen[k.Type] {
			seen[k.Type] = true
			result = append(result, k)
		}
	}
	return result
}

// ── Timing / threshold notes ──────────────────────────────────────────────────

func timingNotes(s *hourlySeries, horizon int) []string {
	var notes []string
	pw := calcPrecipWindow(s, horizon)
	if pw.start != nil && pw.end != nil {
		notes = append(notes, fmt.Sprintf("Precip window %s-%s", *pw.start, *pw.end))
	}
	early := min(6, horizon)
	if early >= 3 {
		earlyWind := avgSlice(s.wind700[:early])
		lateStart := max(0, horizon-early)
		lateWind := avgSlice(s.wind700[lateStart:horizon])
		if lateWind-earlyWind >= 10 {
			notes = append(notes, "Ridge winds strengthen later")
		}
	}
	maxCape := maxSlice(s.cape[:horizon])
	if maxCape >= 400 {
		for i, c := range s.cape[:horizon] {
			if c == maxCape {
				notes = append(notes, fmt.Sprintf("Convective peak ~%s", timeLabel(s.time[i])))
				break
			}
		}
	}
	if len(notes) > 3 {
		return notes[:3]
	}
	return notes
}

func thresholdNotes(dominant RiskCategory) []string {
	switch dominant {
	case CatWind:
		return []string{"Balance threshold: 50-60 km/h (exposed ridges)"}
	case CatVisibility:
		return []string{"Navigation threshold: 200-500 m visibility"}
	case CatThunder:
		return []string{"Lightning threshold: CAPE ~400 J/kg"}
	case CatPrecip:
		return []string{"Friction loss with sustained rain"}
	case CatFreezing:
		return []string{"Freezing precip can glaze rock/gear"}
	case CatThermal:
		return []string{"Wind chill <0C increases frostbite risk"}
	default:
		return nil
	}
}

func implications(dominant RiskCategory) []string {
	switch dominant {
	case CatWind:
		return []string{"Exposed ridge balance compromised", "Rope handling/communication degrade", "Limited shelter above treeline"}
	case CatThunder:
		return []string{"Lightning exposure above treeline", "Gust fronts precede rain"}
	case CatVisibility:
		return []string{"Route finding compromised", "Navigation errors in featureless terrain"}
	case CatPrecip:
		return []string{"Wet rock reduces friction", "Sustained precip increases heat loss"}
	case CatFreezing:
		return []string{"Ice accretion reduces security", "Mixed conditions increase slip risk"}
	case CatThermal:
		return []string{"Cold stress slows pace", "Dexterity loss affects hardware"}
	default:
		return []string{"Maintain conservative margins."}
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func windChillMin(s *hourlySeries, horizon int) *float64 {
	var minChill *float64
	for i := 0; i < horizon; i++ {
		temp := s.temperature2m[i]
		wind := s.windSpeed10m[i]
		if temp <= 10 && wind > 4.8 {
			chill := 13.12 + 0.6215*temp - 11.37*math.Pow(wind, 0.16) + 0.3965*temp*math.Pow(wind, 0.16)
			if minChill == nil || chill < *minChill {
				v := chill
				minChill = &v
			}
		}
	}
	return minChill
}

func hasFreezingPrecip(types []int) bool {
	for _, t := range types {
		if isFreezingPrecip(t) {
			return true
		}
	}
	return false
}

func isFreezingPrecip(t int) bool { return t == 3 || t == 6 }

func isSnowBlock(types []int, snowSum float64) bool {
	if snowSum >= 0.5 {
		return true
	}
	for _, t := range types {
		if t == 2 || t == 7 || t == 9 {
			return true
		}
	}
	return false
}

func hasWindLoading(s *hourlySeries, horizon int) bool {
	for i := 0; i < horizon; i++ {
		if s.snowfall[i] >= 1 && s.wind700[i] >= 40 {
			return true
		}
	}
	return false
}

func calcPrecipWindow(s *hourlySeries, horizon int) precipWindow {
	precip := s.precipitation[:horizon]
	total := sumSlice(precip)
	start := -1
	for i, v := range precip {
		if v >= 0.2 {
			start = i
			break
		}
	}
	if start == -1 {
		return precipWindow{total: total, ptype: "None"}
	}
	end := start
	for i := len(precip) - 1; i >= 0; i-- {
		if precip[i] >= 0.2 {
			end = i
			break
		}
	}
	peak := 0
	for i, v := range precip {
		if v > precip[peak] {
			peak = i
		}
	}
	sl := timeLabel(s.time[start])
	el := timeLabel(s.time[end])
	pl := timeLabel(s.time[peak])
	return precipWindow{total: total, start: &sl, end: &el, peak: &pl, ptype: dominantPrecipType(s.precipType[:horizon])}
}

func dominantPrecipType(types []int) string {
	counts := map[int]int{}
	for _, t := range types {
		if t != 0 {
			counts[t]++
		}
	}
	best, bestCount := 0, 0
	for t, c := range counts {
		if c > bestCount {
			best, bestCount = t, c
		}
	}
	if bestCount == 0 {
		return "None"
	}
	return precipTypeLabel(best)
}

func precipTypeLabel(code int) string {
	labels := map[int]string{
		1: "Rain", 2: "Snow", 3: "Freezing rain", 4: "Ice pellets",
		5: "Drizzle", 6: "Freezing drizzle", 7: "Snow grains",
		8: "Rain showers", 9: "Snow showers",
	}
	if l, ok := labels[code]; ok {
		return l
	}
	return "Mixed"
}

func calcFreezingData(s *hourlySeries, horizon int) freezingData {
	var levels []float64
	for i := 0; i < horizon; i++ {
		if fl := freezingLevelAt(s.temp850[i], s.temp700[i], s.temp500[i], s.height850[i], s.height700[i], s.height500[i]); fl != nil {
			levels = append(levels, *fl)
		}
	}

	below := true
	for i := 0; i < horizon; i++ {
		if s.temp850[i] > 0 {
			below = false
			break
		}
	}
	above := true
	for i := 0; i < horizon; i++ {
		if s.temp500[i] < 0 {
			above = false
			break
		}
	}

	if len(levels) == 0 {
		return freezingData{below1500: below, above5600: above}
	}
	minL := int(minSlice(levels))
	trend := int(levels[len(levels)-1] - levels[0])
	return freezingData{minLevel: &minL, trend: trend}
}

func freezingLevelAt(t850, t700, t500, h850, h700, h500 float64) *float64 {
	points := [][2]float64{{t850, h850}, {t700, h700}, {t500, h500}}
	sort.Slice(points, func(i, j int) bool { return points[i][1] < points[j][1] })
	for i := 0; i < len(points)-1; i++ {
		t1, h1 := points[i][0], points[i][1]
		t2, h2 := points[i+1][0], points[i+1][1]
		if (t1 >= 0 && t2 <= 0) || (t1 <= 0 && t2 >= 0) {
			if t1 == t2 {
				return &h1
			}
			ratio := (0 - t1) / (t2 - t1)
			v := h1 + ratio*(h2-h1)
			return &v
		}
	}
	return nil
}

func cloudDescriptor(s *hourlySeries, horizon int) string {
	low := maxSlice(s.cloudLow[:horizon])
	mid := maxSlice(s.cloudMid[:horizon])
	high := maxSlice(s.cloudHigh[:horizon])
	switch {
	case low >= 80 && mid >= 80 && high >= 80:
		return "OVC (all layers)"
	case low < 20 && mid < 20 && high < 20:
		return "Clear"
	case low >= 80 && mid < 80 && high < 80:
		return "OVC low"
	case mid >= 80 && low < 80:
		return "OVC mid"
	case high >= 80 && low < 80 && mid < 80:
		return "High overcast"
	default:
		return "Mixed"
	}
}

func formatFreezingLevel(fd freezingData) string {
	if fd.minLevel != nil {
		arrow := ""
		switch {
		case fd.trend >= 200:
			arrow = " ↑"
		case fd.trend <= -200:
			arrow = " ↓"
		}
		return fmt.Sprintf("%d m%s", *fd.minLevel, arrow)
	}
	if fd.below1500 {
		return "<=1500 m"
	}
	if fd.above5600 {
		return ">=5600 m"
	}
	return "n/a"
}

func formatVisibility(minVis float64) string {
	if minVis >= 10000 {
		return ">=10 km"
	}
	return fmt.Sprintf("%.1f km", minVis/1000)
}

func thermalValue(s *hourlySeries, horizon int) string {
	tempMin := minSlice(s.temperature2m[:horizon])
	tempMax := maxSlice(s.temperature2m[:horizon])
	chill := windChillMin(s, horizon)
	if chill != nil {
		return fmt.Sprintf("%.0f°", *chill)
	}
	return fmt.Sprintf("%.0f-%.0f°", tempMin, tempMax)
}

func timeLabel(t string) string {
	parts := strings.SplitN(t, "T", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return t
}

func dominantToKeyType(cat RiskCategory) RiskKeyType {
	switch cat {
	case CatWind:
		return KeyRidgeWind
	case CatPrecip:
		return KeyPrecip
	case CatVisibility:
		return KeyVisibility
	case CatThunder:
		return KeyThunder
	case CatFreezing:
		return KeyFreezingLevel
	case CatThermal:
		return KeyThermal
	}
	return ""
}

func hourFromTime(t string) int {
	after := strings.SplitN(t, "T", 2)
	if len(after) < 2 {
		return 0
	}
	h := 0
	fmt.Sscanf(after[1][:2], "%d", &h)
	return h
}

// ── Series parsing ────────────────────────────────────────────────────────────

const (
	defaultHeight850 = 1500.0
	defaultHeight700 = 3000.0
	defaultHeight500 = 5600.0
)

func toHourlySeries(r *OpenMeteoResponse) *hourlySeries {
	if r == nil || r.Hourly == nil {
		return nil
	}
	h := r.Hourly
	if len(h.Time) == 0 {
		return nil
	}
	// Require the key fields
	required := []interface{}{
		h.Temperature2m, h.WindSpeed10m, h.WindGusts10m, h.WindDirection10m,
		h.Precipitation, h.Rain, h.Snowfall, h.PrecipitationType, h.Visibility,
		h.CloudCoverLow, h.CloudCoverMid, h.CloudCoverHigh,
		h.Temperature850hPa, h.Temperature700hPa, h.Temperature500hPa,
		h.GeopotentialHeight850hPa, h.GeopotentialHeight700hPa, h.GeopotentialHeight500hPa,
		h.WindSpeed850hPa, h.WindSpeed700hPa, h.WindSpeed500hPa,
		h.WindDirection850hPa, h.WindDirection700hPa, h.WindDirection500hPa,
	}
	size := len(h.Time)
	for _, list := range required {
		switch v := list.(type) {
		case []*float64:
			if v == nil {
				return nil
			}
			if len(v) < size {
				size = len(v)
			}
}
	}
	if size == 0 {
		return nil
	}

	return &hourlySeries{
		time:             h.Time[:size],
		temperature2m:    derefFloats(h.Temperature2m, size, 0),
		windSpeed10m:     derefFloats(h.WindSpeed10m, size, 0),
		windGusts10m:     derefFloats(h.WindGusts10m, size, 0),
		windDirection10m: derefFloats(h.WindDirection10m, size, 0),
		precipitation:    derefFloats(h.Precipitation, size, 0),
		rain:             derefFloats(h.Rain, size, 0),
		snowfall:         derefFloats(h.Snowfall, size, 0),
		precipType:       derefFloatsAsInts(h.PrecipitationType, size, 0),
		visibility:       derefFloats(h.Visibility, size, 10000),
		cape:             derefFloats(h.Cape, size, 0),
		cin:              derefFloats(h.ConvectiveInhibition, size, 0),
		cloudLow:         derefFloats(h.CloudCoverLow, size, 0),
		cloudMid:         derefFloats(h.CloudCoverMid, size, 0),
		cloudHigh:        derefFloats(h.CloudCoverHigh, size, 0),
		temp850:          derefFloats(h.Temperature850hPa, size, 0),
		temp700:          derefFloats(h.Temperature700hPa, size, 0),
		temp500:          derefFloats(h.Temperature500hPa, size, 0),
		height850:        derefFloats(h.GeopotentialHeight850hPa, size, defaultHeight850),
		height700:        derefFloats(h.GeopotentialHeight700hPa, size, defaultHeight700),
		height500:        derefFloats(h.GeopotentialHeight500hPa, size, defaultHeight500),
		wind850:          derefFloats(h.WindSpeed850hPa, size, 0),
		wind700:          derefFloats(h.WindSpeed700hPa, size, 0),
		wind500:          derefFloats(h.WindSpeed500hPa, size, 0),
		windDir850:       derefFloats(h.WindDirection850hPa, size, 0),
		windDir700:       derefFloats(h.WindDirection700hPa, size, 0),
		windDir500:       derefFloats(h.WindDirection500hPa, size, 0),
	}
}

func toSpreadSeries(r *OpenMeteoResponse) *spreadSeries {
	if r == nil || r.Hourly == nil {
		return nil
	}
	h := r.Hourly
	if h.Temperature2m == nil || h.WindSpeed10m == nil || h.Precipitation == nil {
		return nil
	}
	size := len(h.Time)
	for _, l := range [][]*float64{h.Temperature2m, h.WindSpeed10m, h.Precipitation} {
		if len(l) < size {
			size = len(l)
		}
	}
	if size == 0 {
		return nil
	}
	return &spreadSeries{
		time:          h.Time[:size],
		temperature2m: derefFloats(h.Temperature2m, size, 0),
		windSpeed10m:  derefFloats(h.WindSpeed10m, size, 0),
		precipitation: derefFloats(h.Precipitation, size, 0),
	}
}

// ── Slice utilities ───────────────────────────────────────────────────────────

func maxSlice(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	m := s[0]
	for _, v := range s[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func minSlice(s []float64) float64 {
	if len(s) == 0 {
		return math.MaxFloat64
	}
	m := s[0]
	for _, v := range s[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func sumSlice(s []float64) float64 {
	var t float64
	for _, v := range s {
		t += v
	}
	return t
}

func avgSlice(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return sumSlice(s) / float64(len(s))
}

func safeMin(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return minSlice(s)
}

func safeMax(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return maxSlice(s)
}

func minSlicePtrOrNil(s []float64) *float64 {
	if len(s) == 0 {
		return nil
	}
	v := minSlice(s)
	return &v
}

func pickFloats(src []float64, indices []int) []float64 {
	out := make([]float64, 0, len(indices))
	for _, i := range indices {
		out = append(out, src[i])
	}
	return out
}

func pickInts(src []int, indices []int) []int {
	out := make([]int, 0, len(indices))
	for _, i := range indices {
		out = append(out, src[i])
	}
	return out
}

func pickMaxCloud(s *hourlySeries, indices []int) []float64 {
	out := make([]float64, 0, len(indices))
	for _, i := range indices {
		out = append(out, math.Max(s.cloudLow[i], math.Max(s.cloudMid[i], s.cloudHigh[i])))
	}
	return out
}

func maxRiskLevel(levels []RiskLevel) RiskLevel {
	m := RiskLow
	for _, l := range levels {
		if l > m {
			m = l
		}
	}
	return m
}

func derefFloats(src []*float64, size int, def float64) []float64 {
	out := make([]float64, size)
	for i := 0; i < size; i++ {
		if src != nil && i < len(src) && src[i] != nil {
			out[i] = *src[i]
		} else {
			out[i] = def
		}
	}
	return out
}

func derefFloatsAsInts(src []*float64, size int, def int) []int {
	out := make([]int, size)
	for i := 0; i < size; i++ {
		if src != nil && i < len(src) && src[i] != nil {
			out[i] = int(*src[i])
		} else {
			out[i] = def
		}
	}
	return out
}

func rangeInts(lo, hi int) []int {
	out := make([]int, hi-lo+1)
	for i := range out {
		out[i] = lo + i
	}
	return out
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
