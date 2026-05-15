package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

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
