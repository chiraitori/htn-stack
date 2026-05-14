#include <WiFi.h>
#include <esp_now.h>
#include <esp_wifi.h>
#include <esp_idf_version.h>

// ESP32-C3: pump actuator node.
// Flash this same sketch to both C3 boards, changing NODE_ID for each board.

const char *NODE_ID = "pump-1"; // Change the second board to "pump-2".

// IMPORTANT: Avoid GPIO2, GPIO8, GPIO9 (strapping pins on C3).
// GPIO3 is also used during boot and can glitch LOW, turning on an
// active-low relay before setup() runs.  Use a safe GPIO instead.
const uint8_t RELAY_PIN = 10;
const bool RELAY_ACTIVE_LOW = true;

// Built-in LED on ESP32-C3 Super Mini (GPIO8, active LOW).
const uint8_t LED_PIN = 8;
const bool LED_ACTIVE_LOW = true;

// ESP-NOW must use the same WiFi channel as the S3 gateway.
// The C3 scans channels automatically until it receives a heartbeat/command.
const uint8_t ESPNOW_MIN_CHANNEL = 1;
const uint8_t ESPNOW_MAX_CHANNEL = 13;
const unsigned long CHANNEL_SCAN_INTERVAL_MS = 300;

// If no packet from S3 for this long, consider disconnected.
const unsigned long GATEWAY_TIMEOUT_MS = 15000;

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

bool pumpOn = false;
uint32_t lastCommandSeq = 0;

// --- LED status tracking ---
unsigned long lastGatewayContactMs = 0;
bool gatewayConnected = false;
unsigned long ledBlinkUntilMs = 0;
bool ledBlinkState = false;
unsigned long lastBlinkToggleMs = 0;
uint8_t currentEspNowChannel = ESPNOW_MIN_CHANNEL;
unsigned long lastChannelScanMs = 0;
const unsigned long BLINK_INTERVAL_MS = 100;
const unsigned long BLINK_DURATION_MS = 600; // 3 blinks = 6 toggles @ 100ms

void setPump(bool on) {
  pumpOn = on;
  bool relayLevel = RELAY_ACTIVE_LOW ? !on : on;
  digitalWrite(RELAY_PIN, relayLevel ? HIGH : LOW);
  Serial.printf("[Relay] %s\n", on ? "ON" : "OFF");
}

void onGatewayContact() {
  lastGatewayContactMs = millis();
  if (!gatewayConnected) {
    gatewayConnected = true;
    Serial.printf("[LED] Gateway CONNECTED on channel %u\n", currentEspNowChannel);
  }
  // Trigger blink burst on every received packet.
  ledBlinkUntilMs = millis() + BLINK_DURATION_MS;
}

// Helper: handles active-low inversion for the built-in LED.
void ledWrite(bool on) {
  digitalWrite(LED_PIN, (LED_ACTIVE_LOW ? !on : on) ? HIGH : LOW);
}

void updateLed() {
  unsigned long now = millis();

  // Check gateway timeout.
  if (gatewayConnected && (now - lastGatewayContactMs > GATEWAY_TIMEOUT_MS)) {
    gatewayConnected = false;
    ledBlinkUntilMs = 0;
    ledWrite(false);
    Serial.println("[LED] Gateway DISCONNECTED (timeout)");
    return;
  }

  if (!gatewayConnected) {
    ledWrite(false);
    return;
  }

  // Blink burst active?
  if (now < ledBlinkUntilMs) {
    if (now - lastBlinkToggleMs >= BLINK_INTERVAL_MS) {
      ledBlinkState = !ledBlinkState;
      ledWrite(ledBlinkState);
      lastBlinkToggleMs = now;
    }
  } else {
    // Solid ON while connected.
    ledWrite(true);
  }
}

bool isTargetForThisNode(const char *target) {
  return strcmp(target, "all") == 0 || strcmp(target, NODE_ID) == 0;
}

void ensurePeer(const uint8_t *mac) {
  if (esp_now_is_peer_exist(mac)) {
    return;
  }

  esp_now_peer_info_t peer = {};
  memcpy(peer.peer_addr, mac, 6);
  peer.channel = 0;
  peer.encrypt = false;
  esp_now_add_peer(&peer);
}

void setEspNowChannel(uint8_t channel) {
  if (channel < ESPNOW_MIN_CHANNEL || channel > ESPNOW_MAX_CHANNEL) {
    channel = ESPNOW_MIN_CHANNEL;
  }

  currentEspNowChannel = channel;
  esp_wifi_set_channel(currentEspNowChannel, WIFI_SECOND_CHAN_NONE);
}

void scanGatewayChannelIfNeeded() {
  if (gatewayConnected) {
    return;
  }

  unsigned long now = millis();
  if (now - lastChannelScanMs < CHANNEL_SCAN_INTERVAL_MS) {
    return;
  }
  lastChannelScanMs = now;

  uint8_t nextChannel = currentEspNowChannel + 1;
  if (nextChannel > ESPNOW_MAX_CHANNEL) {
    nextChannel = ESPNOW_MIN_CHANNEL;
  }

  setEspNowChannel(nextChannel);
  Serial.printf("[ESP-NOW] scanning channel %u\n", currentEspNowChannel);
}

void sendStatus(const uint8_t *gatewayMac, uint32_t seq) {
  ensurePeer(gatewayMac);

  PumpPacket packet = {};
  packet.version = 1;
  packet.type = MSG_PUMP_STATUS;
  packet.seq = seq;
  strlcpy(packet.target, NODE_ID, sizeof(packet.target));
  packet.state = pumpOn ? PUMP_ON : PUMP_OFF;

  esp_err_t err = esp_now_send(gatewayMac, reinterpret_cast<uint8_t *>(&packet), sizeof(packet));
  Serial.printf("[ESP-NOW] status seq=%lu result=%s\n", static_cast<unsigned long>(seq), err == ESP_OK ? "OK" : "FAIL");
}

void handleEspNowReceive(const uint8_t *srcMac, const uint8_t *data, int len) {
  if (srcMac == nullptr || data == nullptr || len != sizeof(PumpPacket)) {
    return;
  }

  PumpPacket packet;
  memcpy(&packet, data, sizeof(packet));

  if (packet.version != 1) {
    return;
  }

  // Heartbeat from S3 gateway — just update connection status.
  if (packet.type == MSG_HEARTBEAT) {
    onGatewayContact();
    Serial.println("[ESP-NOW] heartbeat received");
    return;
  }

  if (packet.type != MSG_PUMP_COMMAND) {
    return;
  }

  // Mark gateway as alive on any valid command too.
  onGatewayContact();

  if (!isTargetForThisNode(packet.target)) {
    Serial.printf("[ESP-NOW] command for %s ignored by %s\n", packet.target, NODE_ID);
    return;
  }

  if (packet.seq <= lastCommandSeq) {
    Serial.printf("[ESP-NOW] duplicate/old seq=%lu ignored\n", static_cast<unsigned long>(packet.seq));
    sendStatus(srcMac, packet.seq);
    return;
  }

  lastCommandSeq = packet.seq;
  setPump(packet.state == PUMP_ON);
  Serial.printf(
    "[ESP-NOW] command accepted target=%s state=%s seq=%lu\n",
    packet.target,
    packet.state == PUMP_ON ? "ON" : "OFF",
    static_cast<unsigned long>(packet.seq));

  sendStatus(srcMac, packet.seq);
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

void setupEspNow() {
  WiFi.mode(WIFI_STA);
  WiFi.disconnect();

  setEspNowChannel(currentEspNowChannel);

  if (esp_now_init() != ESP_OK) {
    Serial.println("[ESP-NOW] init failed");
    return;
  }

  esp_now_register_recv_cb(onEspNowReceive);
  Serial.printf("[ESP-NOW] ready node=%s scanning channels %u-%u mac=%s\n", NODE_ID, ESPNOW_MIN_CHANNEL, ESPNOW_MAX_CHANNEL, WiFi.macAddress().c_str());
}

void setup() {
  // Relay — safe init to prevent glitch.
  pinMode(RELAY_PIN, INPUT_PULLUP);
  digitalWrite(RELAY_PIN, RELAY_ACTIVE_LOW ? HIGH : LOW);
  pinMode(RELAY_PIN, OUTPUT);
  setPump(false);

  // Status LED — off until gateway contact.
  pinMode(LED_PIN, OUTPUT);
  ledWrite(false);

  Serial.begin(115200);
  delay(500);

  setupEspNow();
}

void loop() {
  scanGatewayChannelIfNeeded();
  updateLed();
  delay(50); // Fast loop for smooth LED blinking.
}
