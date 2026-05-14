package main

import (
	"fmt"
	"time"
	"os"
	"os/signal"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://127.0.0.1:1883")
	opts.SetClientID("ESP32_Device_01")

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error()) // Khởi động broker server main.go trước nhé
	}

	fmt.Println("ESP32 Emulator đã kết nối thành công vào Broker.")
	
	// Giả lập gửi dữ liệu cho server mỗi 8 giây
	go func() {
		for {
			time.Sleep(8 * time.Second)
			payload := fmt.Sprintf(`{"temperature": 29.5, "humidity": %.1f, "soil_moisture": %d}`, 60+float32(time.Now().Second()%10), 30+(time.Now().Second()%20))
			client.Publish("garden/sensor/data", 0, false, payload)
			fmt.Println("[ESP32] Gửi lên Broker:", payload)
		}
	}()

	// Nhận lệnh Bật/Tắt bơm từ App hoặc Server
	client.Subscribe("garden/control/pump", 1, func(c mqtt.Client, m mqtt.Message) {
		fmt.Printf("[ESP32] Nhận lệnh bơm: %s -> Kích hoạt Relay bơm nước.\n", string(m.Payload()))
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
