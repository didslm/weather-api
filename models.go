package main

// ── Risk enums ────────────────────────────────────────────────────────────────

type RiskLevel int

const (
	RiskLow      RiskLevel = 0
	RiskModerate RiskLevel = 1
	RiskHigh     RiskLevel = 2
)

func (r RiskLevel) String() string {
	return [...]string{"LOW", "MODERATE", "HIGH"}[r]
}

func (r RiskLevel) MarshalJSON() ([]byte, error) {
	return []byte(`"` + r.String() + `"`), nil
}

type RiskCategory string

const (
	CatWind       RiskCategory = "WIND"
	CatPrecip     RiskCategory = "PRECIP"
	CatVisibility RiskCategory = "VISIBILITY"
	CatThunder    RiskCategory = "THUNDER"
	CatFreezing   RiskCategory = "FREEZING"
	CatThermal    RiskCategory = "THERMAL"
	CatNone       RiskCategory = "NONE"
)

type WindSignal string

const (
	WindCalm      WindSignal = "CALM"
	WindBreezy    WindSignal = "BREEZY"
	WindStrong    WindSignal = "STRONG"
	WindVeryStrong WindSignal = "VERY_STRONG"
)

type TempSignal string

const (
	TempComfortable TempSignal = "COMFORTABLE"
	TempCool        TempSignal = "COOL"
	TempCold        TempSignal = "COLD"
	TempVeryCold    TempSignal = "VERY_COLD"
)

type RainSignal string

const (
	RainDry      RainSignal = "DRY"
	RainLight    RainSignal = "LIGHT"
	RainLikely   RainSignal = "RAIN_LIKELY"
)

type ConfidenceLevel string

const (
	ConfHigh   ConfidenceLevel = "HIGH"
	ConfMedium ConfidenceLevel = "MEDIUM"
	ConfLow    ConfidenceLevel = "LOW"
)

type Condition string

const (
	CondClear  Condition = "CLEAR"
	CondPartly Condition = "PARTLY"
	CondCloudy Condition = "CLOUDY"
	CondWindy  Condition = "WINDY"
	CondRain   Condition = "RAIN"
	CondSnow   Condition = "SNOW"
	CondStorm  Condition = "STORM"
)

type DaySegmentLabel string

const (
	SegNight     DaySegmentLabel = "NIGHT"
	SegMorning   DaySegmentLabel = "MORNING"
	SegAfternoon DaySegmentLabel = "AFTERNOON"
	SegEvening   DaySegmentLabel = "EVENING"
)

type RiskKeyType string

const (
	KeyRidgeWind    RiskKeyType = "RIDGE_WIND"
	KeyGusts        RiskKeyType = "GUSTS"
	KeyPrecip       RiskKeyType = "PRECIP"
	KeyFreezingLevel RiskKeyType = "FREEZING_LEVEL"
	KeyVisibility   RiskKeyType = "VISIBILITY"
	KeyThunder      RiskKeyType = "THUNDER"
	KeyThermal      RiskKeyType = "THERMAL"
)

// ── Output models ─────────────────────────────────────────────────────────────

type TopSignals struct {
	Wind       WindSignal      `json:"wind"`
	TempFeel   TempSignal      `json:"temp_feel"`
	Rain       RainSignal      `json:"rain"`
	Exposure   bool            `json:"exposure"`
	Confidence ConfidenceLevel `json:"confidence"`
}

type RiskDriver struct {
	Category RiskCategory `json:"category"`
	Percent  int          `json:"percent"`
}

type RiskKey struct {
	Type  RiskKeyType `json:"type"`
	Level RiskLevel   `json:"level"`
	Value string      `json:"value"`
}

type GuideBrief struct {
	TopSignals      TopSignals      `json:"top_signals"`
	ConfidenceDetail string         `json:"confidence_detail"`
	DominantRisk    RiskCategory    `json:"dominant_risk"`
	SecondaryRisks  []RiskCategory  `json:"secondary_risks"`
	TimingNotes     []string        `json:"timing_notes"`
	ThresholdNotes  []string        `json:"threshold_notes"`
	Implications    []string        `json:"implications"`
	RiskDrivers     []RiskDriver    `json:"risk_drivers"`
	KeyRisks        []RiskKey       `json:"key_risks"`
}

type RiskSnapshot struct {
	WindSurfaceMax  float64  `json:"wind_surface_max"`
	WindGustMax     float64  `json:"wind_gust_max"`
	Ridge1500Max    float64  `json:"ridge_1500_max"`
	Ridge3000Max    float64  `json:"ridge_3000_max"`
	Ridge5600Max    float64  `json:"ridge_5600_max"`
	PrecipTotal     float64  `json:"precip_total"`
	PrecipStart     *string  `json:"precip_start"`
	PrecipEnd       *string  `json:"precip_end"`
	PrecipPeak      *string  `json:"precip_peak"`
	PrecipType      string   `json:"precip_type"`
	FreezingLevelMin *int    `json:"freezing_level_min"`
	FreezingTrend   int      `json:"freezing_trend"`
	FreezingBelow1500 bool   `json:"freezing_below_1500"`
	FreezingAbove5600 bool   `json:"freezing_above_5600"`
	CloudLowMax     float64  `json:"cloud_low_max"`
	CloudMidMax     float64  `json:"cloud_mid_max"`
	CloudHighMax    float64  `json:"cloud_high_max"`
	CloudDescriptor string   `json:"cloud_descriptor"`
	VisibilityMin   float64  `json:"visibility_min"`
	TempMin         float64  `json:"temp_min"`
	TempMax         float64  `json:"temp_max"`
	WindChillMin    *float64 `json:"wind_chill_min"`
	SnowTotal       float64  `json:"snow_total"`
	WindLoading     bool     `json:"wind_loading"`
	CapeMax         float64  `json:"cape_max"`
	CinMin          *float64 `json:"cin_min"`
}

type DaySegment struct {
	Label         DaySegmentLabel `json:"label"`
	Condition     Condition       `json:"condition"`
	TempLow       float64         `json:"temp_low"`
	TempHigh      float64         `json:"temp_high"`
	WindMax       float64         `json:"wind_max"`
	GustMax       float64         `json:"gust_max"`
	WindDirection int             `json:"wind_direction"`
	PrecipAmount  float64         `json:"precip_amount"`
}

type DailyRiskSummary struct {
	Date             string          `json:"date"`
	DominantRisk     RiskCategory    `json:"dominant_risk"`
	RiskLevel        RiskLevel       `json:"risk_level"`
	TempMin          float64         `json:"temp_min"`
	TempMax          float64         `json:"temp_max"`
	WindMax          float64         `json:"wind_max"`
	GustMax          float64         `json:"gust_max"`
	WindDirection    int             `json:"wind_direction"`
	PrecipSum        float64         `json:"precip_sum"`
	SnowSum          float64         `json:"snow_sum"`
	FreezingLevelMin *int            `json:"freezing_level_min"`
	FreezingTrend    int             `json:"freezing_trend"`
	VisibilityMin    float64         `json:"visibility_min"`
	Confidence       ConfidenceLevel `json:"confidence"`
	Segments         []DaySegment    `json:"segments"`
}

type RiskForecast struct {
	Timezone string             `json:"timezone"`
	Sources  []string           `json:"sources"`
	Brief    GuideBrief         `json:"brief"`
	Snapshot RiskSnapshot       `json:"snapshot"`
	Days     []DailyRiskSummary `json:"days"`
}

// ── Open-Meteo API response models ───────────────────────────────────────────

type OpenMeteoResponse struct {
	Latitude  float64        `json:"latitude"`
	Longitude float64        `json:"longitude"`
	Timezone  string         `json:"timezone"`
	Hourly    *OpenMeteoHourly `json:"hourly"`
}

type OpenMeteoHourly struct {
	Time                     []string   `json:"time"`
	Temperature2m            []*float64 `json:"temperature_2m"`
	WindSpeed10m             []*float64 `json:"wind_speed_10m"`
	WindGusts10m             []*float64 `json:"wind_gusts_10m"`
	WindDirection10m         []*float64 `json:"wind_direction_10m"`
	Precipitation            []*float64 `json:"precipitation"`
	Rain                     []*float64 `json:"rain"`
	Snowfall                 []*float64 `json:"snowfall"`
	PrecipitationType        []*float64 `json:"precipitation_type"`
	Visibility               []*float64 `json:"visibility"`
	Cape                     []*float64 `json:"cape"`
	ConvectiveInhibition     []*float64 `json:"convective_inhibition"`
	CloudCoverLow            []*float64 `json:"cloud_cover_low"`
	CloudCoverMid            []*float64 `json:"cloud_cover_mid"`
	CloudCoverHigh           []*float64 `json:"cloud_cover_high"`
	Temperature850hPa        []*float64 `json:"temperature_850hPa"`
	Temperature700hPa        []*float64 `json:"temperature_700hPa"`
	Temperature500hPa        []*float64 `json:"temperature_500hPa"`
	GeopotentialHeight850hPa []*float64 `json:"geopotential_height_850hPa"`
	GeopotentialHeight700hPa []*float64 `json:"geopotential_height_700hPa"`
	GeopotentialHeight500hPa []*float64 `json:"geopotential_height_500hPa"`
	WindSpeed850hPa          []*float64 `json:"wind_speed_850hPa"`
	WindSpeed700hPa          []*float64 `json:"wind_speed_700hPa"`
	WindSpeed500hPa          []*float64 `json:"wind_speed_500hPa"`
	WindDirection850hPa      []*float64 `json:"wind_direction_850hPa"`
	WindDirection700hPa      []*float64 `json:"wind_direction_700hPa"`
	WindDirection500hPa      []*float64 `json:"wind_direction_500hPa"`
}
