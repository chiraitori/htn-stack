package main

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
)

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
