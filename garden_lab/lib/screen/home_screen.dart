import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:mqtt_client/mqtt_client.dart';
import 'package:mqtt_client/mqtt_server_client.dart';

const _configuredMqttHost = String.fromEnvironment('MQTT_HOST');
const _configuredMqttPort = int.fromEnvironment(
  'MQTT_PORT',
  defaultValue: 1883,
);
const _mqttRetryInterval = Duration(seconds: 5);
const _topicSensorData = 'garden/sensor/data';
const _topicPumpControl = 'garden/control/pump';
const _topicAIInsight = 'garden/ai/insight';

enum MqttConnectionPhase { disconnected, connecting, connected }

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, this.autoConnect = true});

  final bool autoConnect;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  MqttServerClient? _client;
  StreamSubscription<List<MqttReceivedMessage<MqttMessage?>>>?
  _updatesSubscription;
  Timer? _retryTimer;

  MqttConnectionPhase _connectionPhase = MqttConnectionPhase.disconnected;
  String? _connectionError;
  int _connectAttempt = 0;

  double temperature = 0.0;
  double humidity = 0.0;
  int soilMoisture = 0;
  bool isPumpOn = false;
  String weatherSummary = 'Chưa có dữ liệu';
  String aiSuggestion = 'Chưa có gợi ý';

  bool get isConnected => _connectionPhase == MqttConnectionPhase.connected;
  bool get isConnecting => _connectionPhase == MqttConnectionPhase.connecting;

  String get brokerHost {
    if (_configuredMqttHost.isNotEmpty) {
      return _configuredMqttHost;
    }
    if (kIsWeb) {
      return 'localhost';
    }
    switch (defaultTargetPlatform) {
      case TargetPlatform.android:
        return '10.0.2.2';
      default:
        return '127.0.0.1';
    }
  }

  @override
  void initState() {
    super.initState();
    if (!widget.autoConnect) {
      return;
    }
    _startRetryLoop();
    _connectMqtt();
  }

  void _startRetryLoop() {
    _retryTimer?.cancel();
    _retryTimer = Timer.periodic(_mqttRetryInterval, (_) {
      if (!mounted || isConnected || isConnecting) {
        return;
      }
      _connectMqtt();
    });
  }

  Future<void> _connectMqtt({bool force = false}) async {
    if (!mounted || isConnecting || (isConnected && !force)) {
      return;
    }

    if (force) {
      await _disposeMqttClient(disconnect: true);
    }

    setState(() {
      _connectionPhase = MqttConnectionPhase.connecting;
      _connectionError = null;
      _connectAttempt += 1;
    });

    final clientId = 'flutter_client_${DateTime.now().millisecondsSinceEpoch}';
    final nextClient = MqttServerClient(brokerHost, clientId)
      ..port = _configuredMqttPort
      ..logging(on: false)
      ..keepAlivePeriod = 20
      ..autoReconnect = false
      ..onDisconnected = _onDisconnected
      ..onConnected = _onConnected
      ..onSubscribed = _onSubscribed;

    nextClient.connectionMessage = MqttConnectMessage()
        .withClientIdentifier(clientId)
        .startClean()
        .withWillQos(MqttQos.atLeastOnce);

    try {
      final status = await nextClient.connect();
      if (!mounted) {
        nextClient.disconnect();
        return;
      }

      if (status?.state != MqttConnectionState.connected) {
        final returnCode = status?.returnCode?.name ?? 'unknown';
        nextClient.disconnect();
        _markDisconnected('Broker từ chối kết nối: $returnCode');
        return;
      }

      await _updatesSubscription?.cancel();
      _client = nextClient;
      _subscribeToTopics(nextClient);
      _listenForMqttUpdates(nextClient);

      setState(() {
        _connectionPhase = MqttConnectionPhase.connected;
        _connectionError = null;
      });
    } catch (e) {
      nextClient.disconnect();
      _markDisconnected(
        'Không kết nối được tới $brokerHost:$_configuredMqttPort',
      );
      debugPrint('MQTT connect exception: $e');
    }
  }

  void _subscribeToTopics(MqttServerClient client) {
    client.subscribe(_topicSensorData, MqttQos.atMostOnce);
    client.subscribe(_topicPumpControl, MqttQos.atMostOnce);
    client.subscribe(_topicAIInsight, MqttQos.atMostOnce);
  }

  void _listenForMqttUpdates(MqttServerClient client) {
    _updatesSubscription = client.updates?.listen(
      (messages) {
        if (messages.isEmpty) {
          return;
        }

        final message = messages.first.payload;
        if (message is! MqttPublishMessage) {
          return;
        }

        final payload = MqttPublishPayload.bytesToStringAsString(
          message.payload.message,
        );
        final topic = messages.first.topic;
        debugPrint('Topic <$topic> payload: $payload');

        if (!mounted) {
          return;
        }

        switch (topic) {
          case _topicSensorData:
            _handleSensorPayload(payload);
            break;
          case _topicPumpControl:
            _handlePumpPayload(payload);
            break;
          case _topicAIInsight:
            _handleInsightPayload(payload);
            break;
        }
      },
      onError: (Object error) {
        debugPrint('MQTT stream error: $error');
        _markDisconnected('Luồng MQTT bị lỗi');
      },
      cancelOnError: false,
    );
  }

  void _handleSensorPayload(String payload) {
    try {
      final data = jsonDecode(payload) as Map<String, dynamic>;
      setState(() {
        temperature = (data['temperature'] as num? ?? 0).toDouble();
        humidity = (data['humidity'] as num? ?? 0).toDouble();
        soilMoisture = (data['soil_moisture'] as num? ?? 0).toInt();
      });
    } catch (e) {
      debugPrint('Sensor JSON parse error: $e');
    }
  }

  void _handlePumpPayload(String payload) {
    final command = payload.trim().toUpperCase();
    if (command != 'ON' && command != 'OFF') {
      return;
    }

    setState(() {
      isPumpOn = command == 'ON';
    });
  }

  void _handleInsightPayload(String payload) {
    try {
      final insight = jsonDecode(payload) as Map<String, dynamic>;
      setState(() {
        weatherSummary = (insight['weather'] ?? 'Không rõ').toString();
        aiSuggestion = (insight['ai_suggestion'] ?? 'Không có gợi ý')
            .toString();
      });
    } catch (e) {
      debugPrint('Insight JSON parse error: $e');
    }
  }

  void _onConnected() => debugPrint('MQTT connected');

  void _onDisconnected() {
    debugPrint('MQTT disconnected');
    _markDisconnected('Mất kết nối MQTT');
  }

  void _onSubscribed(String topic) => debugPrint('Subscribed to $topic');

  void _markDisconnected(String message) {
    if (!mounted) {
      return;
    }

    setState(() {
      _connectionPhase = MqttConnectionPhase.disconnected;
      _connectionError = message;
    });
  }

  Future<void> _manualRefresh() => _connectMqtt(force: true);

  void _togglePump(bool value) {
    final client = _client;
    if (!isConnected || client == null) {
      return;
    }

    final builder = MqttClientPayloadBuilder()..addString(value ? 'ON' : 'OFF');
    client.publishMessage(
      _topicPumpControl,
      MqttQos.exactlyOnce,
      builder.payload!,
    );
    setState(() {
      isPumpOn = value;
    });
  }

  Future<void> _disposeMqttClient({required bool disconnect}) async {
    await _updatesSubscription?.cancel();
    _updatesSubscription = null;

    final client = _client;
    _client = null;
    if (disconnect) {
      client?.disconnect();
    }
  }

  @override
  void dispose() {
    _retryTimer?.cancel();
    _disposeMqttClient(disconnect: true);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final colorScheme = Theme.of(context).colorScheme;
    final textTheme = Theme.of(context).textTheme;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Hệ Thống Tưới Tự Động'),
        centerTitle: true,
        actions: [
          Padding(
            padding: const EdgeInsets.only(right: 8.0),
            child: _buildConnectionAction(colorScheme),
          ),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: _manualRefresh,
        child: SingleChildScrollView(
          physics: const AlwaysScrollableScrollPhysics(),
          padding: const EdgeInsets.all(16.0),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              if (!isConnected) ...[
                _buildConnectionBanner(colorScheme, textTheme),
                const SizedBox(height: 24),
              ],
              Text(
                'Trạng Thái Vườn',
                style: textTheme.titleLarge?.copyWith(
                  fontWeight: FontWeight.bold,
                  color: colorScheme.onSurface,
                ),
              ),
              const SizedBox(height: 16),
              Row(
                children: [
                  Expanded(
                    child: _buildSensorCard(
                      context,
                      title: 'Nhiệt Độ',
                      value: '${temperature.toStringAsFixed(1)}°C',
                      icon: Icons.thermostat_rounded,
                      containerColor: colorScheme.errorContainer,
                      onContainerColor: colorScheme.onErrorContainer,
                    ),
                  ),
                  const SizedBox(width: 16),
                  Expanded(
                    child: _buildSensorCard(
                      context,
                      title: 'Độ Ẩm',
                      value: '${humidity.toStringAsFixed(1)}%',
                      icon: Icons.water_drop_rounded,
                      containerColor: colorScheme.tertiaryContainer,
                      onContainerColor: colorScheme.onTertiaryContainer,
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              _buildSensorCard(
                context,
                title: 'Độ Ẩm Đất',
                value: '$soilMoisture%',
                icon: Icons.grass_rounded,
                containerColor: colorScheme.primaryContainer,
                onContainerColor: colorScheme.onPrimaryContainer,
              ),
              const SizedBox(height: 24),
              Text(
                'AI & Thời Tiết',
                style: textTheme.titleLarge?.copyWith(
                  fontWeight: FontWeight.bold,
                  color: colorScheme.onSurface,
                ),
              ),
              const SizedBox(height: 16),
              Card(
                elevation: 0,
                color: colorScheme.secondaryContainer,
                shape: RoundedRectangleBorder(
                  borderRadius: BorderRadius.circular(24),
                ),
                child: Padding(
                  padding: const EdgeInsets.all(20),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          Icon(
                            Icons.cloud_rounded,
                            color: colorScheme.onSecondaryContainer,
                          ),
                          const SizedBox(width: 12),
                          Text(
                            'Thời Tiết',
                            style: textTheme.titleMedium?.copyWith(
                              fontWeight: FontWeight.w700,
                              color: colorScheme.onSecondaryContainer,
                            ),
                          ),
                        ],
                      ),
                      const SizedBox(height: 8),
                      Text(
                        weatherSummary,
                        style: textTheme.bodyLarge?.copyWith(
                          color: colorScheme.onSecondaryContainer.withValues(
                            alpha: 0.9,
                          ),
                        ),
                      ),
                      const SizedBox(height: 20),
                      Row(
                        children: [
                          Icon(
                            Icons.psychology_rounded,
                            color: colorScheme.onSecondaryContainer,
                          ),
                          const SizedBox(width: 12),
                          Text(
                            'Gợi Ý Từ AI',
                            style: textTheme.titleMedium?.copyWith(
                              fontWeight: FontWeight.w700,
                              color: colorScheme.onSecondaryContainer,
                            ),
                          ),
                        ],
                      ),
                      const SizedBox(height: 8),
                      Text(
                        aiSuggestion,
                        style: textTheme.bodyLarge?.copyWith(
                          color: colorScheme.onSecondaryContainer.withValues(
                            alpha: 0.9,
                          ),
                          height: 1.5,
                        ),
                      ),
                    ],
                  ),
                ),
              ),
              const SizedBox(height: 24),
              Text(
                'Điều Khiển Máy Bơm',
                style: textTheme.titleLarge?.copyWith(
                  fontWeight: FontWeight.bold,
                  color: colorScheme.onSurface,
                ),
              ),
              const SizedBox(height: 16),
              Card(
                elevation: 0,
                color: isPumpOn
                    ? colorScheme.primaryContainer
                    : colorScheme.surfaceContainerHighest,
                shape: RoundedRectangleBorder(
                  borderRadius: BorderRadius.circular(24),
                ),
                child: Padding(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 20,
                    vertical: 20,
                  ),
                  child: Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      Expanded(
                        child: Row(
                          children: [
                            Icon(
                              Icons.water_drop_rounded,
                              color: isPumpOn
                                  ? colorScheme.onPrimaryContainer
                                  : colorScheme.onSurfaceVariant,
                              size: 32,
                            ),
                            const SizedBox(width: 16),
                            Flexible(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    'Máy Bơm Nước',
                                    style: textTheme.titleMedium?.copyWith(
                                      fontWeight: FontWeight.bold,
                                      color: isPumpOn
                                          ? colorScheme.onPrimaryContainer
                                          : colorScheme.onSurfaceVariant,
                                    ),
                                  ),
                                  const SizedBox(height: 4),
                                  Text(
                                    isConnected
                                        ? (isPumpOn ? 'Đang tưới' : 'Đã tắt')
                                        : 'Chờ kết nối MQTT',
                                    style: textTheme.bodyMedium?.copyWith(
                                      color: isPumpOn
                                          ? colorScheme.onPrimaryContainer
                                          : colorScheme.onSurfaceVariant,
                                    ),
                                  ),
                                ],
                              ),
                            ),
                          ],
                        ),
                      ),
                      Switch(
                        value: isPumpOn,
                        onChanged: isConnected ? _togglePump : null,
                      ),
                    ],
                  ),
                ),
              ),
              const SizedBox(height: 32),
            ],
          ),
        ),
      ),
    );
  }

  Widget _buildConnectionAction(ColorScheme colorScheme) {
    if (isConnecting) {
      return const Padding(
        padding: EdgeInsets.all(12.0),
        child: SizedBox(
          width: 20,
          height: 20,
          child: CircularProgressIndicator(strokeWidth: 2),
        ),
      );
    }

    return IconButton(
      tooltip: isConnected ? 'Đã kết nối MQTT' : 'Kết nối lại MQTT',
      onPressed: _manualRefresh,
      icon: Icon(
        isConnected ? Icons.cloud_done_rounded : Icons.refresh_rounded,
        color: isConnected ? colorScheme.primary : colorScheme.error,
      ),
    );
  }

  Widget _buildConnectionBanner(ColorScheme colorScheme, TextTheme textTheme) {
    final message = isConnecting
        ? 'Đang kết nối tới MQTT server $brokerHost:$_configuredMqttPort'
        : '${_connectionError ?? 'Đang mất kết nối MQTT'}. Tự thử lại mỗi ${_mqttRetryInterval.inSeconds} giây.';

    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      decoration: BoxDecoration(
        color: colorScheme.errorContainer,
        borderRadius: BorderRadius.circular(16),
      ),
      child: Row(
        children: [
          Icon(
            isConnecting ? Icons.sync_rounded : Icons.error_outline_rounded,
            color: colorScheme.onErrorContainer,
            size: 20,
          ),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  message,
                  style: textTheme.labelLarge?.copyWith(
                    color: colorScheme.onErrorContainer,
                  ),
                ),
                const SizedBox(height: 2),
                Text(
                  'Lần thử: $_connectAttempt',
                  style: textTheme.bodySmall?.copyWith(
                    color: colorScheme.onErrorContainer.withValues(alpha: 0.8),
                  ),
                ),
              ],
            ),
          ),
          TextButton.icon(
            onPressed: isConnecting ? null : _manualRefresh,
            icon: const Icon(Icons.refresh_rounded, size: 18),
            label: const Text('Thử lại'),
          ),
        ],
      ),
    );
  }

  Widget _buildSensorCard(
    BuildContext context, {
    required String title,
    required String value,
    required IconData icon,
    required Color containerColor,
    required Color onContainerColor,
  }) {
    final textTheme = Theme.of(context).textTheme;

    return Card(
      elevation: 0,
      color: containerColor,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
      child: Padding(
        padding: const EdgeInsets.all(20.0),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(icon, color: onContainerColor, size: 32),
            const SizedBox(height: 16),
            Text(
              title,
              style: textTheme.labelLarge?.copyWith(
                color: onContainerColor.withValues(alpha: 0.8),
              ),
            ),
            const SizedBox(height: 4),
            Text(
              value,
              style: textTheme.headlineSmall?.copyWith(
                fontWeight: FontWeight.bold,
                color: onContainerColor,
              ),
            ),
          ],
        ),
      ),
    );
  }
}
