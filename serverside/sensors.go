package main

import (
	"encoding/json"
	"log"
	"time"
)

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
