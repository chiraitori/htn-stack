package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

const (
	topicSensorData      = "garden/sensor/data"
	topicAIInsight       = "garden/ai/insight"
	topicPumpControl     = "garden/control/pump"
	topicGardenConfigSet = "garden/config/set"
	topicGardenConfigGet = "garden/config/get"
	topicGardenConfig    = "garden/config/state"
	topicGardenRecommend = "garden/config/recommend"
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
	manualOffResumeAt time.Time
}

func (h *BackendLogicHook) ID() string {
	return "backend-logic-hook"
}

func (h *BackendLogicHook) Init(config any) error {
	deps, ok := config.(*hookDeps)
	if !ok || deps == nil || deps.Broker == nil || deps.HTTPClient == nil {
		return fmt.Errorf("invalid hook config")
	}

	h.broker = deps.Broker
	h.cfg = deps.Config
	h.httpClient = deps.HTTPClient
	h.history = deps.History
	return nil
}

func (h *BackendLogicHook) Provides(b byte) bool {
	return b == mqtt.OnPublished
}

func (h *BackendLogicHook) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	switch pk.TopicName {
	case topicSensorData:
		payloadCopy := append([]byte(nil), pk.Payload...)
		go h.processSensorMessage(cl.ID, payloadCopy)
	case topicPumpControl:
		h.processPumpControlMessage(cl.ID, pk.Payload)
	case topicGardenConfigSet:
		h.processGardenConfigSetMessage(cl.ID, pk.Payload)
	case topicGardenConfigGet:
		h.publishGardenConfigState("request")
	case topicGardenConfig:
		return
	case topicGardenRecommend:
		h.processGardenConfigRecommendMessage(cl.ID, pk.Payload)
	default:
		return
	}
}

func normalizePlantType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "cây cảnh"
	}
	return normalized
}

func plantProfileFor(plantType string, fallbackOnBelow float64, fallbackOffAbove float64) PlantProfile {
	normalized := normalizePlantType(plantType)
	if profile, ok := plantProfiles[normalized]; ok {
		return profile
	}
	return PlantProfile{
		PlantType:        normalized,
		SoilPumpOnBelow:  fallbackOnBelow,
		SoilPumpOffAbove: fallbackOffAbove,
	}
}

func clampMoisture(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func normalizeThresholds(onBelow float64, offAbove float64) (float64, float64) {
	onBelow = clampMoisture(onBelow)
	offAbove = clampMoisture(offAbove)
	if offAbove <= onBelow {
		offAbove = onBelow + 5
	}
	if offAbove > 100 {
		offAbove = 100
		onBelow = offAbove - 5
	}
	return onBelow, offAbove
}

func (h *BackendLogicHook) gardenConfigState(source string) GardenConfigState {
	h.mu.Lock()
	defer h.mu.Unlock()

	manualOffResumeAt := ""
	if !h.manualOffResumeAt.IsZero() && time.Now().Before(h.manualOffResumeAt) {
		manualOffResumeAt = h.manualOffResumeAt.Format(time.RFC3339)
	}

	return GardenConfigState{
		AutoPumpEnabled:   h.cfg.AutoPumpEnabled,
		SoilPumpOnBelow:   h.cfg.SoilPumpOnBelow,
		SoilPumpOffAbove:  h.cfg.SoilPumpOffAbove,
		PlantType:         h.cfg.PlantType,
		AIRecommend:       h.cfg.AIRecommend,
		ManualOffResumeAt: manualOffResumeAt,
		ManualOffPauseMin: int(h.cfg.ManualOffPause / time.Minute),
		Source:            source,
	}
}

func (h *BackendLogicHook) publishGardenConfigState(source string) {
	state := h.gardenConfigState(source)
	payload, err := json.Marshal(state)
	if err != nil {
		log.Printf("[Backend] marshal garden config failed: %v", err)
		return
	}

	if err := h.broker.Publish(topicGardenConfig, payload, true, 1); err != nil {
		log.Printf("[Backend] publish garden config error: %v", err)
		return
	}

	log.Printf("[Backend] garden config state: %s", string(payload))
}

func (h *BackendLogicHook) processGardenConfigSetMessage(clientID string, payload []byte) {
	var update GardenConfigUpdate
	if err := json.Unmarshal(payload, &update); err != nil {
		log.Printf("[Backend] invalid garden config from %s: %v", clientID, err)
		return
	}

	h.mu.Lock()
	if update.AutoPumpEnabled != nil {
		h.cfg.AutoPumpEnabled = *update.AutoPumpEnabled
	}
	if update.AIRecommend != nil {
		h.cfg.AIRecommend = *update.AIRecommend
	}
	if update.PlantType != nil {
		h.cfg.PlantType = normalizePlantType(*update.PlantType)
	}
	if update.SoilPumpOnBelow != nil {
		h.cfg.SoilPumpOnBelow = *update.SoilPumpOnBelow
	}
	if update.SoilPumpOffAbove != nil {
		h.cfg.SoilPumpOffAbove = *update.SoilPumpOffAbove
	}
	h.cfg.SoilPumpOnBelow, h.cfg.SoilPumpOffAbove = normalizeThresholds(h.cfg.SoilPumpOnBelow, h.cfg.SoilPumpOffAbove)
	h.mu.Unlock()

	log.Printf("[Backend] garden config update from %s: %s", clientID, string(payload))
	h.publishGardenConfigState("app")
}

func (h *BackendLogicHook) processGardenConfigRecommendMessage(clientID string, payload []byte) {
	var req struct {
		PlantType string `json:"plant_type"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		log.Printf("[Backend] invalid garden recommend request from %s: %v", clientID, err)
		return
	}

	h.mu.Lock()
	profile := plantProfileFor(req.PlantType, h.cfg.SoilPumpOnBelow, h.cfg.SoilPumpOffAbove)
	h.cfg.PlantType = profile.PlantType
	h.cfg.SoilPumpOnBelow = profile.SoilPumpOnBelow
	h.cfg.SoilPumpOffAbove = profile.SoilPumpOffAbove
	h.cfg.AIRecommend = true
	h.mu.Unlock()

	log.Printf("[Backend] garden config recommendation for %s requested by %s", profile.PlantType, clientID)
	h.publishGardenConfigState("ai_recommend")
}

func (h *BackendLogicHook) processPumpControlMessage(clientID string, payload []byte) {
	command := strings.ToUpper(strings.TrimSpace(string(payload)))
	if command != "ON" && command != "OFF" {
		return
	}

	manualCommand := isManualPumpCommandClient(clientID)
	var manualResumeAt time.Time

	h.mu.Lock()
	h.pumpOn = command == "ON"
	if manualCommand {
		if command == "OFF" && h.cfg.ManualOffPause > 0 {
			h.manualOffResumeAt = time.Now().Add(h.cfg.ManualOffPause)
			manualResumeAt = h.manualOffResumeAt
		} else if command == "ON" {
			h.manualOffResumeAt = time.Time{}
		}
	}
	h.mu.Unlock()

	log.Printf("[Backend] pump control from %s: %s", clientID, command)
	if manualCommand && !manualResumeAt.IsZero() {
		log.Printf("[Backend] manual OFF pauses auto pump until %s", manualResumeAt.Format(time.RFC3339))
		h.publishGardenConfigState("manual_off")
	} else if manualCommand && command == "ON" {
		h.publishGardenConfigState("manual_on")
	}
}

func isManualPumpCommandClient(clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || clientID == mqtt.InlineClientId {
		return false
	}
	if clientID == "esp32-s3-sensor-gateway" {
		return false
	}
	return true
}

func (h *BackendLogicHook) processSensorMessage(clientID string, payload []byte) {
	var sensor SensorData
	if err := json.Unmarshal(payload, &sensor); err != nil {
		log.Printf("[Backend] invalid sensor payload: %v", err)
		return
	}

	log.Printf("[Backend] Sensor payload from %s: %s", clientID, string(payload))

	h.decidePumpCommand(sensor.SoilMoisture)

	batch, ok := h.enqueueSensorSample(clientID, sensor)
	if !ok {
		return
	}

	h.processSensorBatch(batch)
}

func (h *BackendLogicHook) enqueueSensorSample(clientID string, sensor SensorData) ([]sensorEnvelope, bool) {
	batchSize := h.cfg.SensorBatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.pending = append(h.pending, sensorEnvelope{ClientID: clientID, Data: sensor})
	if len(h.pending) < batchSize {
		log.Printf("[Backend] buffered %d/%d sensor samples", len(h.pending), batchSize)
		return nil, false
	}

	batch := append([]sensorEnvelope(nil), h.pending[:batchSize]...)
	h.pending = h.pending[batchSize:]
	return batch, true
}

func summarizeBatch(batch []sensorEnvelope) SensorBatchSummary {
	if len(batch) == 0 {
		return SensorBatchSummary{}
	}

	first := batch[0].Data
	minData := first
	maxData := first
	latest := batch[len(batch)-1].Data

	var sumTemp float64
	var sumHum float64
	var sumSoil float64

	for _, item := range batch {
		s := item.Data
		sumTemp += s.Temperature
		sumHum += s.Humidity
		sumSoil += s.SoilMoisture

		if s.Temperature < minData.Temperature {
			minData.Temperature = s.Temperature
		}
		if s.Humidity < minData.Humidity {
			minData.Humidity = s.Humidity
		}
		if s.SoilMoisture < minData.SoilMoisture {
			minData.SoilMoisture = s.SoilMoisture
		}

		if s.Temperature > maxData.Temperature {
			maxData.Temperature = s.Temperature
		}
		if s.Humidity > maxData.Humidity {
			maxData.Humidity = s.Humidity
		}
		if s.SoilMoisture > maxData.SoilMoisture {
			maxData.SoilMoisture = s.SoilMoisture
		}
	}

	count := float64(len(batch))
	return SensorBatchSummary{
		Count: len(batch),
		Avg: SensorData{
			Temperature:  sumTemp / count,
			Humidity:     sumHum / count,
			SoilMoisture: sumSoil / count,
		},
		Min:    minData,
		Max:    maxData,
		Latest: latest,
	}
}

func (h *BackendLogicHook) processSensorBatch(batch []sensorEnvelope) {
	if len(batch) == 0 {
		return
	}

	summary := summarizeBatch(batch)
	clientID := batch[len(batch)-1].ClientID
	log.Printf("[Backend] processing sensor batch: count=%d avg_temp=%.1f avg_humidity=%.1f avg_soil=%.1f", summary.Count, summary.Avg.Temperature, summary.Avg.Humidity, summary.Avg.SoilMoisture)

	weather, err := h.fetchCurrentWeather()
	if err != nil {
		log.Printf("[Weather API] fallback due to error: %v", err)
		weather = WeatherSummary{Description: "unknown", Temperature: 0, Humidity: 0}
	} else {
		log.Printf("[Weather API] current: %s, %.1fC", weather.Description, weather.Temperature)
	}

	gardenCfg := h.gardenConfigState("batch")
	suggestion := fallbackSuggestion(summary, weather, gardenCfg)
	if gardenCfg.AIRecommend {
		geminiSuggestion, err := h.requestGeminiSuggestion(summary, weather, gardenCfg)
		if err != nil {
			log.Printf("[AI Agent] fallback due to error: %v", err)
		} else {
			suggestion = geminiSuggestion
		}
	}
	log.Printf("[AI Agent] suggestion: %s", suggestion)

	if err := h.saveHistory(clientID, summary, weather, gardenCfg, suggestion); err != nil {
		log.Printf("[MongoDB] save history failed: %v", err)
	} else if h.history != nil {
		log.Printf("[MongoDB] history saved")
	}

	insightPayload, _ := json.Marshal(map[string]any{
		"weather":       weather.Description,
		"temperature":   weather.Temperature,
		"humidity":      weather.Humidity,
		"sample_count":  summary.Count,
		"sensor_avg":    summary.Avg,
		"ai_suggestion": suggestion,
		"garden_config": gardenCfg,
	})

	if err := h.broker.Publish(topicAIInsight, insightPayload, false, 1); err != nil {
		log.Printf("[Backend] publish insight error: %v", err)
	}
}

func (h *BackendLogicHook) decidePumpCommand(soilMoisture float64) string {
	now := time.Now()
	resumedFromManualOff := false

	h.mu.Lock()
	autoPumpEnabled := h.cfg.AutoPumpEnabled
	onBelow := h.cfg.SoilPumpOnBelow
	offAbove := h.cfg.SoilPumpOffAbove
	manualOffResumeAt := h.manualOffResumeAt
	if !manualOffResumeAt.IsZero() && !now.Before(manualOffResumeAt) {
		h.manualOffResumeAt = time.Time{}
		manualOffResumeAt = time.Time{}
		resumedFromManualOff = true
	}
	h.mu.Unlock()

	if !autoPumpEnabled {
		log.Printf("[Backend] auto pump disabled (soil=%.1f%%)", soilMoisture)
		return ""
	}

	if resumedFromManualOff {
		log.Printf("[Backend] manual OFF pause expired, auto pump resumed")
		h.publishGardenConfigState("manual_off_expired")
	}

	if !manualOffResumeAt.IsZero() && now.Before(manualOffResumeAt) {
		log.Printf(
			"[Backend] auto pump paused by manual OFF until %s (soil=%.1f%%)",
			manualOffResumeAt.Format(time.RFC3339),
			soilMoisture,
		)
		return ""
	}

	command := ""
	h.mu.Lock()
	if soilMoisture <= onBelow {
		command = "ON"
		h.pumpOn = true
	} else if soilMoisture >= offAbove {
		command = "OFF"
		h.pumpOn = false
	}
	h.mu.Unlock()

	if command == "" {
		log.Printf(
			"[Backend] pump hold (soil=%.1f%%, ON <= %.1f%%, OFF >= %.1f%%)",
			soilMoisture,
			onBelow,
			offAbove,
		)
		return ""
	}

	if err := h.broker.Publish(topicPumpControl, []byte(command), false, 1); err != nil {
		log.Printf("[Backend] publish pump command error: %v", err)
		return ""
	}

	log.Printf("[Backend] pump command: %s (soil=%.1f%%)", command, soilMoisture)
	return command
}

func (h *BackendLogicHook) fetchCurrentWeather() (WeatherSummary, error) {
	provider := strings.ToLower(strings.TrimSpace(h.cfg.WeatherProvider))
	switch provider {
	case "", "openweather", "openweathermap":
		return h.fetchCurrentWeatherOpenWeather()
	case "weatherapi", "weather.com", "weatherdotcom":
		return h.fetchCurrentWeatherWeatherAPI()
	default:
		return WeatherSummary{}, fmt.Errorf("unsupported WEATHER_PROVIDER: %s", h.cfg.WeatherProvider)
	}
}

func (h *BackendLogicHook) fetchCurrentWeatherOpenWeather() (WeatherSummary, error) {
	if h.cfg.OpenWeatherAPIKey == "" {
		return WeatherSummary{}, fmt.Errorf("OPENWEATHERMAP_API_KEY is empty")
	}

	url := fmt.Sprintf(
		"https://api.openweathermap.org/data/2.5/weather?lat=%.6f&lon=%.6f&appid=%s&units=metric&lang=vi",
		h.cfg.WeatherLat,
		h.cfg.WeatherLon,
		h.cfg.OpenWeatherAPIKey,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return WeatherSummary{}, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return WeatherSummary{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return WeatherSummary{}, fmt.Errorf("openweather status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Weather []struct {
			Description string `json:"description"`
		} `json:"weather"`
		Main struct {
			Temp     float64 `json:"temp"`
			Humidity int     `json:"humidity"`
		} `json:"main"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return WeatherSummary{}, err
	}

	desc := "unknown"
	if len(result.Weather) > 0 && result.Weather[0].Description != "" {
		desc = result.Weather[0].Description
	}

	return WeatherSummary{
		Description: desc,
		Temperature: result.Main.Temp,
		Humidity:    result.Main.Humidity,
	}, nil
}

func (h *BackendLogicHook) fetchCurrentWeatherWeatherAPI() (WeatherSummary, error) {
	if h.cfg.WeatherAPIKey == "" {
		return WeatherSummary{}, fmt.Errorf("WEATHERAPI_KEY is empty")
	}

	q := fmt.Sprintf("%.6f,%.6f", h.cfg.WeatherLat, h.cfg.WeatherLon)
	values := url.Values{}
	values.Set("key", h.cfg.WeatherAPIKey)
	values.Set("q", q)
	values.Set("lang", "vi")

	endpoint := "https://api.weatherapi.com/v1/current.json?" + values.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return WeatherSummary{}, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return WeatherSummary{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return WeatherSummary{}, fmt.Errorf("weatherapi status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Current struct {
			TempC     float64 `json:"temp_c"`
			Humidity  int     `json:"humidity"`
			Condition struct {
				Text string `json:"text"`
			} `json:"condition"`
		} `json:"current"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return WeatherSummary{}, err
	}

	desc := strings.TrimSpace(result.Current.Condition.Text)
	if desc == "" {
		desc = "unknown"
	}

	return WeatherSummary{
		Description: desc,
		Temperature: result.Current.TempC,
		Humidity:    result.Current.Humidity,
	}, nil
}

func (h *BackendLogicHook) requestGeminiSuggestion(summary SensorBatchSummary, weather WeatherSummary, gardenCfg GardenConfigState) (string, error) {
	if h.cfg.GeminiAPIKey == "" || strings.HasPrefix(h.cfg.GeminiAPIKey, "your_") {
		return "", fmt.Errorf("GEMINI_API_KEY is missing")
	}

	model := strings.TrimSpace(h.cfg.GeminiModel)
	if model == "" || strings.HasPrefix(model, "your_") {
		model = "gemini-2.5-flash"
	}

	maxRetries := h.cfg.GeminiMaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	prompt := fmt.Sprintf(
		"Ban la tro ly nong nghiep thong minh cho cay %s. Nguong hien tai: bat bom khi do am dat <= %.1f%%, tat bom khi >= %.1f%%. Day la %d mau cam bien gan nhat: nhiet do TB=%.1fC (min %.1f, max %.1f), do am khong khi TB=%.1f%% (min %.1f, max %.1f), do am dat TB=%.1f%% (min %.1f, max %.1f), mau moi nhat do am dat=%.1f%%. Thoi tiet hien tai: %s, nhiet do %.1fC, do am %d%%. Hay dua ra 1-2 cau bang tieng Viet tu nhien: co nen tuoi khong, neu can thi noi ro co nen tang/giam nguong do am cho loai cay nay khong.",
		gardenCfg.PlantType,
		gardenCfg.SoilPumpOnBelow,
		gardenCfg.SoilPumpOffAbove,
		summary.Count,
		summary.Avg.Temperature,
		summary.Min.Temperature,
		summary.Max.Temperature,
		summary.Avg.Humidity,
		summary.Min.Humidity,
		summary.Max.Humidity,
		summary.Avg.SoilMoisture,
		summary.Min.SoilMoisture,
		summary.Max.SoilMoisture,
		summary.Latest.SoilMoisture,
		weather.Description,
		weather.Temperature,
		weather.Humidity,
	)

	requestBody := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]string{{"text": prompt}},
			},
		},
	}

	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	models := uniqueNonEmpty([]string{model, "gemini-2.5-flash", "gemini-2.5-flash-lite"})
	var lastErr error

	for _, modelName := range models {
		for attempt := 1; attempt <= maxRetries; attempt++ {
			text, statusCode, err := h.callGeminiGenerate(modelName, encoded)
			if err == nil {
				if modelName != model {
					log.Printf("[AI Agent] switched model to %s", modelName)
				}
				return text, nil
			}

			lastErr = err
			if !shouldRetryGemini(statusCode, err) || attempt == maxRetries {
				break
			}

			delay := geminiRetryDelay(attempt)
			log.Printf("[AI Agent] gemini retry model=%s attempt=%d/%d after %s (%v)", modelName, attempt, maxRetries, delay, err)
			time.Sleep(delay)
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("gemini request failed")
	}
	return "", lastErr
}

func (h *BackendLogicHook) callGeminiGenerate(model string, encoded []byte) (string, int, error) {
	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", h.cfg.GeminiAPIKey)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return "", resp.StatusCode, err
	}

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("gemini status %d: %s", resp.StatusCode, string(body))
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return "", http.StatusOK, err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", http.StatusOK, fmt.Errorf("gemini returned no content")
	}

	text := strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text)
	if text == "" {
		return "", http.StatusOK, fmt.Errorf("gemini empty response")
	}

	return text, http.StatusOK, nil
}

func shouldRetryGemini(statusCode int, err error) bool {
	if err == nil {
		return false
	}

	switch statusCode {
	case 0:
		return true
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func geminiRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := time.Second
	delay := base * time.Duration(1<<(attempt-1))
	if delay > 8*time.Second {
		return 8 * time.Second
	}
	return delay
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))

	for _, v := range values {
		normalized := strings.TrimSpace(v)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	return out
}

func (h *BackendLogicHook) saveHistory(clientID string, summary SensorBatchSummary, weather WeatherSummary, gardenCfg GardenConfigState, suggestion string) error {
	if h.history == nil {
		return nil
	}

	doc := bson.M{
		"created_at": time.Now().UTC(),
		"client_id":  clientID,
		"sensor_batch": bson.M{
			"count": summary.Count,
			"avg": bson.M{
				"temperature":   summary.Avg.Temperature,
				"humidity":      summary.Avg.Humidity,
				"soil_moisture": summary.Avg.SoilMoisture,
			},
			"min": bson.M{
				"temperature":   summary.Min.Temperature,
				"humidity":      summary.Min.Humidity,
				"soil_moisture": summary.Min.SoilMoisture,
			},
			"max": bson.M{
				"temperature":   summary.Max.Temperature,
				"humidity":      summary.Max.Humidity,
				"soil_moisture": summary.Max.SoilMoisture,
			},
			"latest": bson.M{
				"temperature":   summary.Latest.Temperature,
				"humidity":      summary.Latest.Humidity,
				"soil_moisture": summary.Latest.SoilMoisture,
			},
		},
		"weather": bson.M{
			"description": weather.Description,
			"temperature": weather.Temperature,
			"humidity":    weather.Humidity,
		},
		"garden_config": bson.M{
			"auto_pump_enabled":   gardenCfg.AutoPumpEnabled,
			"soil_pump_on_below":  gardenCfg.SoilPumpOnBelow,
			"soil_pump_off_above": gardenCfg.SoilPumpOffAbove,
			"plant_type":          gardenCfg.PlantType,
			"ai_recommend":        gardenCfg.AIRecommend,
		},
		"ai_suggestion": suggestion,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := h.history.InsertOne(ctx, doc)
	return err
}

func fallbackSuggestion(summary SensorBatchSummary, weather WeatherSummary, gardenCfg GardenConfigState) string {
	if summary.Avg.SoilMoisture <= gardenCfg.SoilPumpOnBelow {
		if strings.Contains(strings.ToLower(weather.Description), "mua") {
			return fmt.Sprintf("Dat cua %s dang kho nhung troi co mua, tam hoan tuoi 15-30 phut de tiet kiem nuoc.", gardenCfg.PlantType)
		}
		return fmt.Sprintf("Do am dat cua %s thap, nen bat bom tuoi 3-5 phut.", gardenCfg.PlantType)
	}
	if summary.Avg.SoilMoisture >= gardenCfg.SoilPumpOffAbove {
		return fmt.Sprintf("Do am dat cua %s da du, nen tat bom va theo doi them.", gardenCfg.PlantType)
	}
	return fmt.Sprintf("Do am dat cua %s dang nam giua nguong tuoi, chua can doi trang thai bom.", gardenCfg.PlantType)
}

func loadConfig() AppConfig {
	_ = godotenv.Load(".env")

	cfg := AppConfig{
		WeatherProvider:   envOrDefault("WEATHER_PROVIDER", "openweather"),
		OpenWeatherAPIKey: strings.TrimSpace(os.Getenv("OPENWEATHERMAP_API_KEY")),
		WeatherAPIKey:     strings.TrimSpace(os.Getenv("WEATHERAPI_KEY")),
		GeminiAPIKey:      strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		GeminiModel:       strings.TrimSpace(os.Getenv("GEMINI_MODEL")),
		GeminiMaxRetries:  envIntOrDefault("GEMINI_MAX_RETRIES", 3),
		MongoURI:          strings.TrimSpace(os.Getenv("MONGODB_URI")),
		MongoDatabase:     envOrDefault("MONGODB_DATABASE", "garden_lab"),
		MongoCollection:   envOrDefault("MONGODB_COLLECTION", "sensor_history"),
		MQTTPort:          envOrDefault("MQTT_BROKER_PORT", "1883"),
		SensorBatchSize:   envIntOrDefault("SENSOR_BATCH_SIZE", 10),
		AutoPumpEnabled:   envBoolOrDefault("AUTO_PUMP_ENABLED", true),
		SoilPumpOnBelow:   envFloatOrDefault("SOIL_PUMP_ON_BELOW", 35),
		SoilPumpOffAbove:  envFloatOrDefault("SOIL_PUMP_OFF_ABOVE", 45),
		PlantType:         normalizePlantType(envOrDefault("PLANT_TYPE", "cây cảnh")),
		AIRecommend:       envBoolOrDefault("AI_RECOMMEND", true),
		ManualOffPause:    time.Duration(envIntOrDefault("MANUAL_OFF_AUTO_RESUME_MINUTES", 60)) * time.Minute,
		WeatherLat:        envFloatOrDefault("WEATHER_LAT", 10.847519),
		WeatherLon:        envFloatOrDefault("WEATHER_LON", 106.673947),
	}

	if cfg.GeminiModel == "" || strings.HasPrefix(cfg.GeminiModel, "your_") {
		cfg.GeminiModel = "gemini-2.5-flash"
	}
	cfg.SoilPumpOnBelow, cfg.SoilPumpOffAbove = normalizeThresholds(cfg.SoilPumpOnBelow, cfg.SoilPumpOffAbove)

	return cfg
}

func envOrDefault(key string, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	return value
}

func envFloatOrDefault(key string, defaultValue float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return defaultValue
	}
	return f
}

func envIntOrDefault(key string, defaultValue int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	v, err := strconv.Atoi(value)
	if err != nil || v <= 0 {
		return defaultValue
	}
	return v
}

func envBoolOrDefault(key string, defaultValue bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return defaultValue
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return defaultValue
	}
}

func setupMongo(cfg AppConfig) (*mongo.Client, *mongo.Collection) {
	if cfg.MongoURI == "" {
		log.Printf("[MongoDB] skip connect: MONGODB_URI is empty")
		return nil, nil
	}

	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		log.Printf("[MongoDB] connect failed: %v", err)
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		log.Printf("[MongoDB] ping failed: %v", err)
		_ = client.Disconnect(context.Background())
		return nil, nil
	}

	collection := client.Database(cfg.MongoDatabase).Collection(cfg.MongoCollection)
	log.Printf("[MongoDB] connected: db=%s collection=%s", cfg.MongoDatabase, cfg.MongoCollection)
	return client, collection
}

func main() {
	cfg := loadConfig()

	server := mqtt.New(&mqtt.Options{InlineClient: true})
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		log.Fatal("failed to add allow auth hook:", err)
	}

	mongoClient, historyCollection := setupMongo(cfg)
	defer func() {
		if mongoClient == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mongoClient.Disconnect(ctx); err != nil {
			log.Printf("[MongoDB] disconnect error: %v", err)
		}
	}()

	hookConfig := &hookDeps{
		Broker: server,
		Config: cfg,
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		History: historyCollection,
	}
	if err := server.AddHook(new(BackendLogicHook), hookConfig); err != nil {
		log.Fatal("failed to add backend logic hook:", err)
	}

	listenAddress := ":" + cfg.MQTTPort
	tcp := listeners.NewTCP(listeners.Config{ID: "t1", Address: listenAddress})
	if err := server.AddListener(tcp); err != nil {
		log.Fatal("failed to open MQTT port:", err)
	}

	go func() {
		if err := server.Serve(); err != nil {
			log.Fatal(err)
		}
	}()
	fmt.Printf("GARDEN SERVER: MQTT broker started on %s\n", listenAddress)
	fmt.Printf("Sensor batching: %d samples/request\n", cfg.SensorBatchSize)
	fmt.Printf("Auto pump: %t (plant=%s, AI=%t, ON <= %.1f%%, OFF >= %.1f%%, manual OFF pause=%s)\n", cfg.AutoPumpEnabled, cfg.PlantType, cfg.AIRecommend, cfg.SoilPumpOnBelow, cfg.SoilPumpOffAbove, cfg.ManualOffPause)
	fmt.Printf("Weather location: lat=%.6f lon=%.6f\n", cfg.WeatherLat, cfg.WeatherLon)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs

	fmt.Println("Shutting down MQTT broker...")
	server.Close()
}
