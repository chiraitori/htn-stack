package main

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (h *BackendLogicHook) saveHistory(clientID string, summary SensorBatchSummary, weather WeatherSummary, gardenCfg GardenConfigState, suggestion string) error {
	if h.history == nil {
		return nil
	}

	doc := bson.M{
		"type":       "sensor_batch",
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
