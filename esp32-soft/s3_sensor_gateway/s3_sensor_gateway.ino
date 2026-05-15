#include <Arduino.h>
#include <PubSubClient.h>
#include <WiFi.h>
#include <WiFiManager.h>
#include <esp_now.h>
#include <esp_idf_version.h>

// ESP32-S3: sensor gateway.
// - Reads soil + DHT11 sensors.
// - Publishes sensor data to MQTT server.
// - Receives MQTT pump commands and forwards them to ESP32-C3 pump nodes via ESP-NOW.
// - WiFiManager: auto-creates AP captive portal if WiFi is not configured.

// Default MQTT settings.
const char *MQTT_HOST = "51.79.255.192";
const uint16_t MQTT_PORT = 1883;
const char *MQTT_CLIENT_ID = "esp32-s3-sensor-gateway";

// Hold BOOT button (GPIO0) for 3 seconds to reset WiFi and re-enter setup portal.
const uint8_t RESET_BTN_PIN = 0;
const unsigned long RESET_HOLD_MS = 3000;

const char *TOPIC_SENSOR_DATA = "garden/sensor/data";
const char *TOPIC_PUMP_CONTROL = "garden/control/pump";
const char *TOPIC_PUMP_STATUS = "garden/pump/status";
const char *TOPIC_GARDEN_CONFIG = "garden/config/state";
const char *TOPIC_GARDEN_CONFIG_GET = "garden/config/get";

const uint8_t ESPNOW_BROADCAST_MAC[6] = {0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF};

const uint8_t DHT_PIN = 18;
const uint8_t SOIL_PIN = 16;
const uint8_t RGB_LED_PIN = 48; // Built-in WS2812 on many ESP32-S3 DevKit boards.

// Capacitive soil sensor: higher raw value means drier soil, lower means wetter soil.
// Calibrated from current ESP32-S3 readings: dry air ~3400-3450, wet soil/water ~1430.
const int SOIL_WET_RAW = 1430;
const int SOIL_DRY_RAW = 3450;
const unsigned long SENSOR_PUBLISH_INTERVAL_MS = 8000;
const unsigned long MQTT_RECONNECT_INTERVAL_MS = 3000;
const unsigned long HEARTBEAT_INTERVAL_MS = 5000;
const unsigned long MQTT_FAILSAFE_OFF_MS = 30000;
const unsigned long DEVICE_ONLINE_TIMEOUT_MS = 15000;
const unsigned long RGB_ANIMATION_INTERVAL_MS = 120;

const char *EXPECTED_NODE = "pump-1";

enum MessageType : uint8_t {
  MSG_PUMP_COMMAND = 1,
  MSG_PUMP_STATUS = 2,
  MSG_HEARTBEAT = 3,
};

enum PumpState : uint8_t {
  PUMP_OFF = 0,
  PUMP_ON = 1,
};

struct __attribute__((packed)) PumpPacket {
  uint8_t version;
  uint8_t type;
  uint32_t seq;
  char target[16];
  uint8_t state;
};

WiFiClient wifiClient;
PubSubClient mqtt(wifiClient);

unsigned long lastSensorPublishMs = 0;
unsigned long lastMqttReconnectMs = 0;
unsigned long lastHeartbeatMs = 0;
unsigned long lastMqttConnectedMs = 0;
bool mqttFailsafeOffSent = false;
bool mqttFailsafeStatusPending = false;
unsigned long lastPumpSeenMs = 0;
unsigned long lastRgbAnimationMs = 0;
uint8_t rainbowStep = 0;
uint32_t commandSeq = 0;

struct DhtReading {
  bool ok;
  float temperature;
  float humidity;
};

int soilPercentFromRaw(int raw) {
  int percent = map(raw, SOIL_DRY_RAW, SOIL_WET_RAW, 0, 100);
  return constrain(percent, 0, 100);
}

void setStatusLed(uint8_t red, uint8_t green, uint8_t blue) {
  neopixelWrite(RGB_LED_PIN, red, green, blue);
}

void wheelColor(uint8_t position, uint8_t &red, uint8_t &green, uint8_t &blue) {
  position = 255 - position;
  if (position < 85) {
    red = 255 - position * 3;
    green = 0;
    blue = position * 3;
    return;
  }

  if (position < 170) {
    position -= 85;
    red = 0;
    green = position * 3;
    blue = 255 - position * 3;
    return;
  }

  position -= 170;
  red = position * 3;
  green = 255 - position * 3;
  blue = 0;
}

bool isDeviceOnline(unsigned long lastSeenMs) {
  return lastSeenMs > 0 && millis() - lastSeenMs <= DEVICE_ONLINE_TIMEOUT_MS;
}

bool pumpNodeOnline() {
  return isDeviceOnline(lastPumpSeenMs);
}

void updateStatusLed() {
  unsigned long now = millis();

  if (!mqtt.connected()) {
    setStatusLed(12, 0, 0);
    return;
  }

  if (!pumpNodeOnline()) {
    setStatusLed(12, 8, 0);
    return;
  }

  if (now - lastRgbAnimationMs < RGB_ANIMATION_INTERVAL_MS) {
    return;
  }
  lastRgbAnimationMs = now;

  uint8_t red;
  uint8_t green;
  uint8_t blue;
  wheelColor(rainbowStep, red, green, blue);
  rainbowStep += 7;

  // Keep the onboard LED readable without being painfully bright.
  setStatusLed(red / 8, green / 8, blue / 8);
}

bool waitForDhtLevel(uint8_t level, uint32_t timeoutUs) {
  uint32_t start = micros();
  while (digitalRead(DHT_PIN) == level) {
    if (micros() - start > timeoutUs) {
      return false;
    }
    yield();
  }
  return true;
}

DhtReading readDht11Safe() {
  uint8_t data[5] = {0, 0, 0, 0, 0};

  pinMode(DHT_PIN, OUTPUT_OPEN_DRAIN);
  digitalWrite(DHT_PIN, LOW);
  delay(20);
  digitalWrite(DHT_PIN, HIGH);
  delayMicroseconds(40);
  pinMode(DHT_PIN, INPUT_PULLUP);

  if (!waitForDhtLevel(HIGH, 100)) {
    return {false, 0, 0};
  }
  if (!waitForDhtLevel(LOW, 100)) {
    return {false, 0, 0};
  }
  if (!waitForDhtLevel(HIGH, 100)) {
    return {false, 0, 0};
  }

  for (uint8_t bit = 0; bit < 40; bit++) {
    if (!waitForDhtLevel(LOW, 70)) {
      return {false, 0, 0};
    }

    uint32_t highStart = micros();
    if (!waitForDhtLevel(HIGH, 100)) {
      return {false, 0, 0};
    }
    uint32_t highTime = micros() - highStart;

    data[bit / 8] <<= 1;
    if (highTime > 40) {
      data[bit / 8] |= 1;
    }
  }

  uint8_t checksum = data[0] + data[1] + data[2] + data[3];
  if (checksum != data[4]) {
    return {false, 0, 0};
  }

  return {
    true,
    static_cast<float>(data[2]) + static_cast<float>(data[3]) / 10.0f,
    static_cast<float>(data[0]) + static_cast<float>(data[1]) / 10.0f,
  };
}

String extractJsonString(const String &json, const char *key) {
  String needle = String("\"") + key + "\"";
  int keyPos = json.indexOf(needle);
  if (keyPos < 0) {
    return "";
  }

  int colonPos = json.indexOf(':', keyPos + needle.length());
  if (colonPos < 0) {
    return "";
  }

  int quoteStart = json.indexOf('"', colonPos + 1);
  if (quoteStart < 0) {
    return "";
  }

  int quoteEnd = json.indexOf('"', quoteStart + 1);
  if (quoteEnd < 0) {
    return "";
  }

  return json.substring(quoteStart + 1, quoteEnd);
}

bool parsePumpCommand(String payload, char *target, size_t targetLen, PumpState &state) {
  payload.trim();
  String upperPayload = payload;
  upperPayload.toUpperCase();

  strlcpy(target, "all", targetLen);

  if (upperPayload == "ON" || upperPayload == "1" || upperPayload == "TRUE") {
    state = PUMP_ON;
    return true;
  }

  if (upperPayload == "OFF" || upperPayload == "0" || upperPayload == "FALSE") {
    state = PUMP_OFF;
    return true;
  }

  if (!payload.startsWith("{")) {
    return false;
  }

  String jsonTarget = extractJsonString(payload, "target");
  if (jsonTarget.length() == 0) {
    jsonTarget = extractJsonString(payload, "node_id");
  }
  if (jsonTarget.length() > 0) {
    jsonTarget.toCharArray(target, targetLen);
  }

  String jsonState = extractJsonString(payload, "state");
  if (jsonState.length() == 0) {
    jsonState = extractJsonString(payload, "command");
  }
  jsonState.trim();
  jsonState.toUpperCase();

  if (jsonState == "ON" || jsonState == "1" || jsonState == "TRUE") {
    state = PUMP_ON;
    return true;
  }

  if (jsonState == "OFF" || jsonState == "0" || jsonState == "FALSE") {
    state = PUMP_OFF;
    return true;
  }

  return false;
}

void trackPumpNode(const char *nodeId) {
  unsigned long now = millis();

  if (strcmp(nodeId, EXPECTED_NODE) == 0) {
    lastPumpSeenMs = now;
  }
}

void publishPumpStatus(const uint8_t *mac, const PumpPacket &packet) {
  trackPumpNode(packet.target);

  if (!mqtt.connected()) {
    return;
  }

  char macText[18];
  snprintf(
    macText,
    sizeof(macText),
    "%02X:%02X:%02X:%02X:%02X:%02X",
    mac[0],
    mac[1],
    mac[2],
    mac[3],
    mac[4],
    mac[5]);

  char payload[160];
  snprintf(
    payload,
    sizeof(payload),
    "{\"node_id\":\"%s\",\"state\":\"%s\",\"seq\":%lu,\"mac\":\"%s\"}",
    packet.target,
    packet.state == PUMP_ON ? "ON" : "OFF",
    static_cast<unsigned long>(packet.seq),
    macText);

  mqtt.publish(TOPIC_PUMP_STATUS, payload);
  Serial.printf("[MQTT] pump status: %s\n", payload);
}

void handleEspNowReceive(const uint8_t *srcMac, const uint8_t *data, int len) {
  if (srcMac == nullptr || data == nullptr || len != sizeof(PumpPacket)) {
    return;
  }

  PumpPacket packet;
  memcpy(&packet, data, sizeof(packet));
  if (packet.version != 1 || packet.type != MSG_PUMP_STATUS) {
    return;
  }

  publishPumpStatus(srcMac, packet);
}

#if ESP_IDF_VERSION_MAJOR >= 5
void onEspNowReceive(const esp_now_recv_info_t *info, const uint8_t *data, int len) {
  handleEspNowReceive(info == nullptr ? nullptr : info->src_addr, data, len);
}
#else
void onEspNowReceive(const uint8_t *mac, const uint8_t *data, int len) {
  handleEspNowReceive(mac, data, len);
}
#endif

void sendPumpCommand(const char *target, PumpState state) {
  PumpPacket packet = {};
  packet.version = 1;
  packet.type = MSG_PUMP_COMMAND;
  packet.seq = ++commandSeq;
  strlcpy(packet.target, target, sizeof(packet.target));
  packet.state = state;

  esp_err_t err = esp_now_send(ESPNOW_BROADCAST_MAC, reinterpret_cast<uint8_t *>(&packet), sizeof(packet));
  Serial.printf(
    "[ESP-NOW] command target=%s state=%s seq=%lu result=%s\n",
    packet.target,
    packet.state == PUMP_ON ? "ON" : "OFF",
    static_cast<unsigned long>(packet.seq),
    err == ESP_OK ? "OK" : "FAIL");
}

void onMqttMessage(char *topic, byte *payloadBytes, unsigned int length) {
  String payload;
  payload.reserve(length);
  for (unsigned int i = 0; i < length; i++) {
    payload += static_cast<char>(payloadBytes[i]);
  }

  Serial.printf("[MQTT] topic=%s payload=%s\n", topic, payload.c_str());

  String topicName = String(topic);
  if (topicName == TOPIC_GARDEN_CONFIG) {
    Serial.printf("[MQTT] garden config synced: %s\n", payload.c_str());
    return;
  }

  if (topicName != TOPIC_PUMP_CONTROL) {
    return;
  }

  char target[16];
  PumpState state = PUMP_OFF;
  if (!parsePumpCommand(payload, target, sizeof(target), state)) {
    Serial.println("[MQTT] invalid pump command, ignored");
    return;
  }

  sendPumpCommand(target, state);
}

// --- WiFiManager: WiFi only, MQTT is hardcoded ---
void connectWifiWithManager() {
  WiFiManager wm;

  // Timeout: if no one configures within 3 minutes, restart.
  wm.setConfigPortalTimeout(180);
  wm.setClass("invert");

  Serial.println("[WiFi] Starting WiFiManager...");
  bool connected = wm.autoConnect("GardenGateway-Setup");

  if (!connected) {
    Serial.println("[WiFi] Config portal timeout — restarting...");
    delay(1000);
    ESP.restart();
  }

  Serial.printf("[WiFi] connected IP=%s channel=%d mac=%s\n",
    WiFi.localIP().toString().c_str(), WiFi.channel(), WiFi.macAddress().c_str());
}

void checkResetButton() {
  if (digitalRead(RESET_BTN_PIN) == LOW) {
    unsigned long pressStart = millis();
    while (digitalRead(RESET_BTN_PIN) == LOW) {
      delay(50);
      if (millis() - pressStart >= RESET_HOLD_MS) {
        Serial.println("[WiFi] RESET button held — clearing WiFi and restarting...");
        WiFiManager wm;
        wm.resetSettings();
        delay(500);
        ESP.restart();
      }
    }
  }
}

void setupEspNow() {
  if (esp_now_init() != ESP_OK) {
    Serial.println("[ESP-NOW] init failed");
    return;
  }

  esp_now_register_recv_cb(onEspNowReceive);

  esp_now_peer_info_t peer = {};
  memcpy(peer.peer_addr, ESPNOW_BROADCAST_MAC, 6);
  peer.channel = 0;
  peer.encrypt = false;

  if (!esp_now_is_peer_exist(ESPNOW_BROADCAST_MAC)) {
    esp_err_t err = esp_now_add_peer(&peer);
    Serial.printf("[ESP-NOW] add broadcast peer: %s\n", err == ESP_OK ? "OK" : "FAIL");
  }
}

void sendHeartbeatIfNeeded() {
  unsigned long now = millis();
  if (now - lastHeartbeatMs < HEARTBEAT_INTERVAL_MS) {
    return;
  }
  lastHeartbeatMs = now;

  PumpPacket packet = {};
  packet.version = 1;
  packet.type = MSG_HEARTBEAT;
  packet.seq = 0;
  strlcpy(packet.target, "all", sizeof(packet.target));
  packet.state = 0;

  esp_now_send(ESPNOW_BROADCAST_MAC, reinterpret_cast<uint8_t *>(&packet), sizeof(packet));
}

void reconnectMqttIfNeeded() {
  unsigned long now = millis();

  if (mqtt.connected()) {
    lastMqttConnectedMs = now;
    mqttFailsafeOffSent = false;
    return;
  }

  if (now - lastMqttReconnectMs < MQTT_RECONNECT_INTERVAL_MS) {
    return;
  }
  lastMqttReconnectMs = now;

  Serial.print("[MQTT] connecting...");
  if (!mqtt.connect(MQTT_CLIENT_ID)) {
    Serial.printf(" failed rc=%d\n", mqtt.state());
    return;
  }

  Serial.println(" connected");
  lastMqttConnectedMs = millis();
  mqttFailsafeOffSent = false;
  mqtt.subscribe(TOPIC_PUMP_CONTROL, 1);
  mqtt.subscribe(TOPIC_GARDEN_CONFIG, 1);
  mqtt.publish(TOPIC_GARDEN_CONFIG_GET, "{}");

  if (mqttFailsafeStatusPending) {
    mqtt.publish(TOPIC_PUMP_CONTROL, "OFF");
    mqtt.publish(TOPIC_PUMP_STATUS, "{\"node_id\":\"all\",\"state\":\"OFF\",\"reason\":\"mqtt_failsafe\"}");
    mqttFailsafeStatusPending = false;
    Serial.println("[Failsafe] MQTT reconnected -> reported pump OFF");
  }
}

void checkMqttFailsafe() {
  if (mqtt.connected()) {
    return;
  }

  unsigned long now = millis();
  if (now - lastMqttConnectedMs < MQTT_FAILSAFE_OFF_MS || mqttFailsafeOffSent) {
    return;
  }

  Serial.println("[Failsafe] MQTT disconnected too long -> pump OFF");
  sendPumpCommand("all", PUMP_OFF);
  mqttFailsafeOffSent = true;
  mqttFailsafeStatusPending = true;
}

void publishSensorDataIfNeeded() {
  unsigned long now = millis();
  if (now - lastSensorPublishMs < SENSOR_PUBLISH_INTERVAL_MS) {
    return;
  }
  lastSensorPublishMs = now;

  DhtReading dhtReading = readDht11Safe();
  int soilRaw = analogRead(SOIL_PIN);
  int soilMoisture = soilPercentFromRaw(soilRaw);

  if (!dhtReading.ok) {
    Serial.println("[Sensor] DHT read failed");
    return;
  }

  char payload[128];
  snprintf(
    payload,
    sizeof(payload),
    "{\"temperature\":%.1f,\"humidity\":%.1f,\"soil_moisture\":%d}",
    dhtReading.temperature,
    dhtReading.humidity,
    soilMoisture);

  if (mqtt.connected()) {
    mqtt.publish(TOPIC_SENSOR_DATA, payload);
    Serial.printf("[MQTT] sensor data: %s raw_soil=%d\n", payload, soilRaw);
  }
}

void setup() {
  Serial.begin(115200);
  delay(500);

  pinMode(RGB_LED_PIN, OUTPUT);
  setStatusLed(0, 0, 0);
  pinMode(RESET_BTN_PIN, INPUT_PULLUP);

  // WiFiManager: auto-connect or open captive portal.
  connectWifiWithManager();

  setupEspNow();

  mqtt.setServer(MQTT_HOST, MQTT_PORT);
  mqtt.setCallback(onMqttMessage);
}

void loop() {
  checkResetButton();
  reconnectMqttIfNeeded();
  mqtt.loop();
  checkMqttFailsafe();
  publishSensorDataIfNeeded();
  sendHeartbeatIfNeeded();
  updateStatusLed();
}
