# Garden Lab Flutter App

App tự retry kết nối MQTT mỗi 5 giây khi chưa kết nối được server.

## MQTT config

Mặc định app dùng:

- Android emulator: `10.0.2.2:1883`
- Desktop/mobile thật: `127.0.0.1:1883`
- Web: `localhost:1883`

Khi dùng MQTT broker trên VPS/OVH, build app với `--dart-define`:

```powershell
flutter run --dart-define=MQTT_HOST=YOUR_OVH_IP --dart-define=MQTT_PORT=1883
```

Build APK:

```powershell
flutter build apk --release --dart-define=MQTT_HOST=YOUR_OVH_IP --dart-define=MQTT_PORT=1883
```

Nếu dùng domain:

```powershell
flutter build apk --release --dart-define=MQTT_HOST=mqtt.example.com --dart-define=MQTT_PORT=1883
```

