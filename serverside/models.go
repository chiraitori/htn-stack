package main

import (
	"net/http"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type AppConfig struct {
	WeatherProvider   string
	OpenWeatherAPIKey string
	WeatherAPIKey     string
	GeminiAPIKey      string
	GeminiModel       string
	GeminiMaxRetries  int
	MongoURI          string
	MongoDatabase     string
	MongoCollection   string
	MQTTPort          string
	SensorBatchSize   int
	AutoPumpEnabled   bool
	SoilPumpOnBelow   float64
	SoilPumpOffAbove  float64
	PlantType         string
	AIRecommend       bool
	ManualOffPause    time.Duration
	WeatherLat        float64
	WeatherLon        float64
}

type GardenConfigState struct {
	AutoPumpEnabled   bool    `json:"auto_pump_enabled"`
	SoilPumpOnBelow   float64 `json:"soil_pump_on_below"`
	SoilPumpOffAbove  float64 `json:"soil_pump_off_above"`
	PlantType         string  `json:"plant_type"`
	AIRecommend       bool    `json:"ai_recommend"`
	ManualOffResumeAt string  `json:"manual_off_resume_at,omitempty"`
	ManualOffPauseMin int     `json:"manual_off_pause_minutes"`
	Source            string  `json:"source"`
}

type GardenConfigUpdate struct {
	AutoPumpEnabled  *bool    `json:"auto_pump_enabled"`
	SoilPumpOnBelow  *float64 `json:"soil_pump_on_below"`
	SoilPumpOffAbove *float64 `json:"soil_pump_off_above"`
	PlantType        *string  `json:"plant_type"`
	AIRecommend      *bool    `json:"ai_recommend"`
}

type PumpHistoryEvent struct {
	Timestamp string `json:"timestamp"`
	State     string `json:"state"`
	Source    string `json:"source"`
	ClientID  string `json:"client_id"`
	Reason    string `json:"reason,omitempty"`
	Manual    bool   `json:"manual"`
}

type PumpHistoryState struct {
	Events []PumpHistoryEvent `json:"events"`
}

type PlantProfile struct {
	PlantType        string
	SoilPumpOnBelow  float64
	SoilPumpOffAbove float64
}

var plantProfiles = map[string]PlantProfile{
	"rau ăn lá":  {PlantType: "rau ăn lá", SoilPumpOnBelow: 45, SoilPumpOffAbove: 60},
	"cà chua":    {PlantType: "cà chua", SoilPumpOnBelow: 40, SoilPumpOffAbove: 55},
	"ớt":         {PlantType: "ớt", SoilPumpOnBelow: 35, SoilPumpOffAbove: 50},
	"hoa cảnh":   {PlantType: "hoa cảnh", SoilPumpOnBelow: 35, SoilPumpOffAbove: 50},
	"cây cảnh":   {PlantType: "cây cảnh", SoilPumpOnBelow: 30, SoilPumpOffAbove: 45},
	"xương rồng": {PlantType: "xương rồng", SoilPumpOnBelow: 15, SoilPumpOffAbove: 25},
}

type SensorData struct {
	Temperature  float64 `json:"temperature"`
	Humidity     float64 `json:"humidity"`
	SoilMoisture float64 `json:"soil_moisture"`
}

type WeatherSummary struct {
	Description string  `json:"description"`
	Temperature float64 `json:"temperature"`
	Humidity    int     `json:"humidity"`
}

type SensorBatchSummary struct {
	Count  int        `json:"count"`
	Avg    SensorData `json:"avg"`
	Min    SensorData `json:"min"`
	Max    SensorData `json:"max"`
	Latest SensorData `json:"latest"`
}

type sensorEnvelope struct {
	ClientID string
	Data     SensorData
}

type hookDeps struct {
	Broker     *mqtt.Server
	Config     AppConfig
	HTTPClient *http.Client
	History    *mongo.Collection
}

type BackendLogicHook struct {
	mqtt.HookBase
	broker     *mqtt.Server
	cfg        AppConfig
	httpClient *http.Client
	history    *mongo.Collection

	mu                sync.Mutex
	pending           []sensorEnvelope
	pumpOn            bool
	pumpHistory       []PumpHistoryEvent
	manualOffResumeAt time.Time
}
