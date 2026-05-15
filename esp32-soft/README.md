# Smart Garden ESP32 Firmware

Kiến trúc đã tách thành 2 loại firmware:

- `s3_sensor_gateway/s3_sensor_gateway.ino`: ESP32-S3 đọc cảm biến, gửi MQTT lên server, nhận lệnh bơm từ server/app rồi phát ESP-NOW cho các ESP32-C3.
- `c3_pump_node/c3_pump_node.ino`: ESP32-C3 node chấp hành relay bơm. Nạp cùng một sketch cho 2 board, chỉ đổi `NODE_ID`.

## Luồng dữ liệu

```text
ESP32-S3 sensors -> MQTT garden/sensor/data -> server
server/app -> MQTT garden/control/pump -> ESP32-S3 -> ESP-NOW -> ESP32-C3 pump nodes
ESP32-C3 status -> ESP-NOW -> ESP32-S3 -> MQTT garden/pump/status
```

## MQTT topics

- `garden/sensor/data`: S3 publish dữ liệu cảm biến.

  ```json
  {"temperature":29.5,"humidity":63.0,"soil_moisture":34}
  ```

- `garden/control/pump`: server/app publish lệnh bơm.

  Payload đơn giản, áp dụng cho cả 2 C3:

  ```text
  ON
  OFF
  ```

  Payload JSON, điều khiển từng node:

  ```json
  {"target":"pump-1","state":"ON"}
  {"target":"pump-2","state":"OFF"}
  {"target":"all","state":"OFF"}
  ```

- `garden/pump/status`: S3 publish trạng thái do C3 phản hồi.

## ESP32-S3 wiring

- Soil capacitive sensor output -> GPIO16
- DHT11 data/out -> GPIO18
- Built-in WS2812 RGB LED -> GPIO48, nếu board S3 của bạn dùng chân khác thì đổi `RGB_LED_PIN`
- GND chung cho tất cả module
- VCC theo module thực tế

Trong `s3_sensor_gateway.ino`, sửa các giá trị này trước khi nạp:

```cpp
const char *WIFI_SSID = "YOUR_WIFI_SSID";
const char *WIFI_PASSWORD = "YOUR_WIFI_PASSWORD";
const char *MQTT_HOST = "51.79.255.192";
```

Hiệu chỉnh cảm biến đất:

```cpp
const int SOIL_WET_RAW = 1430;
const int SOIL_DRY_RAW = 3450;
```

Mở Serial Monitor của S3 để xem WiFi channel:

```text
[WiFi] connected IP=... channel=6 mac=...
```

Firmware C3 mới sẽ tự quét channel 1-13 để tìm heartbeat từ S3, nên dòng channel này chỉ dùng để debug Serial Monitor.

## ESP32-C3 wiring

- Relay IN -> GPIO0 trên ESP32-C3 Super Mini, có thể đổi `RELAY_PIN`
- Nếu relay vẫn bật khi C3 vừa cấp nguồn, thêm điện trở kéo lên 10k từ IN relay lên 3V3, hoặc đổi sang chân không phải boot strap.
- Relay VCC/GND theo relay module
- GND relay và ESP32-C3 phải chung

Với board C3 thứ nhất:

```cpp
const char *NODE_ID = "pump-1";
```

Với board C3 thứ hai:

```cpp
const char *NODE_ID = "pump-2";
```

ESP-NOW cần cùng channel với WiFi của S3 gateway. Firmware C3 hiện tự quét channel 1-13, nên khi đổi hotspot/router thì không cần sửa code C3 nữa:

```cpp
const uint8_t ESPNOW_MIN_CHANNEL = 1;
const uint8_t ESPNOW_MAX_CHANNEL = 13;
```

Mặc định code đang để relay kích mức LOW, vì đa số module relay bật khi chân IN bị kéo xuống LOW:

```cpp
const bool RELAY_ACTIVE_LOW = true;
```

Nếu relay module của bạn bật khi chân IN lên HIGH, đổi thành `false`.

## Arduino libraries

Cài các thư viện:

- `DHT sensor library` by Adafruit
- `Adafruit Unified Sensor`
- `PubSubClient` by Nick O'Leary
- ESP32 Arduino core

## Server auto pump

Server tự publish lệnh `ON/OFF` lên `garden/control/pump` ngay khi nhận dữ liệu cảm biến nếu `AUTO_PUMP_ENABLED=true`.

Các biến môi trường có thể chỉnh:

```env
AUTO_PUMP_ENABLED=true
SOIL_PUMP_ON_BELOW=35
SOIL_PUMP_OFF_ABOVE=45
PLANT_TYPE=cây cảnh
AI_RECOMMEND=true
```

App Flutter cũng có thể đổi các giá trị này qua MQTT:

- Gửi cấu hình: `garden/config/set`
- Hỏi cấu hình hiện tại: `garden/config/get`
- Server trả cấu hình retained: `garden/config/state`
- Xin gợi ý nhanh theo loại cây: `garden/config/recommend`

Khi bật auto pump, logic là:

- Độ ẩm đất <= 35%: bật bơm.
- Độ ẩm đất >= 45%: tắt bơm.

Nếu app đổi ngưỡng, server dùng ngưỡng mới ngay cho các mẫu cảm biến kế tiếp. S3 subscribe `garden/config/state` để log cấu hình sau mỗi lần reconnect MQTT; C3 vẫn chỉ làm node chấp hành và nhận `ON/OFF` từ S3 qua ESP-NOW.

## Pump failsafe

- C3 tự tắt relay nếu không nhận heartbeat/command từ S3 trong 15 giây.
- S3 tự gửi `OFF` qua ESP-NOW nếu mất MQTT quá 30 giây.
- Khi MQTT kết nối lại sau failsafe, S3 publish `OFF` lên `garden/control/pump` và trạng thái `garden/pump/status` để app/server biết bơm đã tắt.

## S3 WS2812 status LED

S3 dùng LED WS2812 onboard để báo trạng thái:

- Đỏ nhẹ: chưa kết nối MQTT server.
- Vàng nhẹ: MQTT đã kết nối nhưng chưa thấy `pump-1`.
- Rainbow 7 màu: MQTT đã kết nối và thấy `pump-1`.

C3 đang dùng phải đặt đúng:

```cpp
const char *NODE_ID = "pump-1";
```
