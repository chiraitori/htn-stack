package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

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
