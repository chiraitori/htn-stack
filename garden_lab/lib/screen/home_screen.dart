import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:mqtt_client/mqtt_client.dart';
import 'package:mqtt_client/mqtt_server_client.dart';

const _configuredMqttHost = String.fromEnvironment(
  'MQTT_HOST',
  defaultValue: '51.79.255.192',
);
const _configuredMqttPort = int.fromEnvironment(
  'MQTT_PORT',
  defaultValue: 1883,
);
const _mqttRetryInterval = Duration(seconds: 5);
const _topicSensorData = 'garden/sensor/data';
const _topicPumpControl = 'garden/control/pump';
const _topicAIInsight = 'garden/ai/insight';
const _topicGardenConfigSet = 'garden/config/set';
const _topicGardenConfigGet = 'garden/config/get';
const _topicGardenConfigState = 'garden/config/state';
const _topicGardenConfigRecommend = 'garden/config/recommend';
const _plantTypes = [
  'rau ăn lá',
  'cà chua',
  'ớt',
  'hoa cảnh',
  'cây cảnh',
  'xương rồng',
];

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
  bool autoPumpEnabled = true;
  bool aiRecommend = true;
  double soilPumpOnBelow = 35;
  double soilPumpOffAbove = 45;
  String plantType = 'cây cảnh';
  String? manualOffResumeAt;
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
      _requestGardenConfig(nextClient);

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
    client.subscribe(_topicGardenConfigState, MqttQos.atLeastOnce);
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
          case _topicGardenConfigState:
            _handleGardenConfigPayload(payload);
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

  void _handleGardenConfigPayload(String payload) {
    try {
      final config = jsonDecode(payload) as Map<String, dynamic>;
      final nextPlantType = (config['plant_type'] ?? plantType).toString();
      final normalizedPlantType = _plantTypes.contains(nextPlantType)
          ? nextPlantType
          : plantType;

      setState(() {
        autoPumpEnabled =
            config['auto_pump_enabled'] as bool? ?? autoPumpEnabled;
        aiRecommend = config['ai_recommend'] as bool? ?? aiRecommend;
        plantType = normalizedPlantType;
        soilPumpOnBelow =
            (config['soil_pump_on_below'] as num? ?? soilPumpOnBelow)
                .toDouble();
        soilPumpOffAbove =
            (config['soil_pump_off_above'] as num? ?? soilPumpOffAbove)
                .toDouble();
        final resumeAt = (config['manual_off_resume_at'] ?? '').toString();
        manualOffResumeAt = resumeAt.isEmpty ? null : resumeAt;
      });
    } catch (e) {
      debugPrint('Garden config JSON parse error: $e');
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
      MqttQos.atLeastOnce,
      builder.payload!,
    );
    setState(() {
      isPumpOn = value;
    });
  }

  void _publishJsonTopic(String topic, Map<String, dynamic> payload) {
    final client = _client;
    if (!isConnected || client == null) {
      return;
    }

    final builder = MqttClientPayloadBuilder()
      ..addUTF8String(jsonEncode(payload));
    client.publishMessage(topic, MqttQos.atLeastOnce, builder.payload!);
  }

  void _requestGardenConfig([MqttServerClient? mqttClient]) {
    final client = mqttClient ?? _client;
    if (client == null) {
      return;
    }

    final builder = MqttClientPayloadBuilder()..addString('{}');
    client.publishMessage(
      _topicGardenConfigGet,
      MqttQos.atLeastOnce,
      builder.payload!,
    );
  }

  void _publishGardenConfig() {
    _publishJsonTopic(_topicGardenConfigSet, {
      'auto_pump_enabled': autoPumpEnabled,
      'ai_recommend': aiRecommend,
      'plant_type': plantType,
      'soil_pump_on_below': soilPumpOnBelow.round(),
      'soil_pump_off_above': soilPumpOffAbove.round(),
    });
  }

  void _requestAIRecommendation() {
    _publishJsonTopic(_topicGardenConfigRecommend, {'plant_type': plantType});
  }

  void _setPumpOnThreshold(double value) {
    setState(() {
      soilPumpOnBelow = value.roundToDouble();
      if (soilPumpOffAbove <= soilPumpOnBelow) {
        soilPumpOffAbove = (soilPumpOnBelow + 5).clamp(0, 100).toDouble();
      }
    });
  }

  void _setPumpOffThreshold(double value) {
    setState(() {
      soilPumpOffAbove = value.roundToDouble();
      if (soilPumpOnBelow >= soilPumpOffAbove) {
        soilPumpOnBelow = (soilPumpOffAbove - 5).clamp(0, 100).toDouble();
      }
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
                'Cấu Hình Tưới Theo Cây',
                style: textTheme.titleLarge?.copyWith(
                  fontWeight: FontWeight.bold,
                  color: colorScheme.onSurface,
                ),
              ),
              const SizedBox(height: 16),
              _buildGardenConfigCard(colorScheme, textTheme),
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

  Widget _buildGardenConfigCard(ColorScheme colorScheme, TextTheme textTheme) {
    return Card(
      elevation: 0,
      color: colorScheme.surfaceContainerHighest,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
      child: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            LayoutBuilder(
              builder: (context, constraints) {
                return DropdownMenu<String>(
                  key: ValueKey(plantType),
                  initialSelection: plantType,
                  width: constraints.maxWidth,
                  leadingIcon: const Icon(Icons.eco_rounded),
                  label: const Text('Loại cây'),
                  textStyle: textTheme.bodyLarge?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                  trailingIcon: Icon(
                    Icons.keyboard_arrow_down_rounded,
                    color: colorScheme.primary,
                  ),
                  selectedTrailingIcon: Icon(
                    Icons.keyboard_arrow_up_rounded,
                    color: colorScheme.primary,
                  ),
                  menuStyle: MenuStyle(
                    backgroundColor: WidgetStatePropertyAll(
                      colorScheme.surfaceContainer,
                    ),
                    surfaceTintColor: WidgetStatePropertyAll(
                      colorScheme.primary,
                    ),
                    elevation: const WidgetStatePropertyAll(3),
                    shadowColor: WidgetStatePropertyAll(
                      colorScheme.shadow.withValues(alpha: 0.18),
                    ),
                    shape: WidgetStatePropertyAll(
                      RoundedRectangleBorder(
                        borderRadius: BorderRadius.circular(24),
                      ),
                    ),
                  ),
                  dropdownMenuEntries: _plantTypes
                      .map(
                        (type) => DropdownMenuEntry(value: type, label: type),
                      )
                      .toList(),
                  onSelected: (value) {
                    if (value == null) {
                      return;
                    }
                    setState(() => plantType = value);
                  },
                );
              },
            ),
            const SizedBox(height: 12),
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              value: autoPumpEnabled,
              onChanged: (value) => setState(() => autoPumpEnabled = value),
              title: const Text('Tự bật/tắt bơm theo độ ẩm đất'),
              secondary: const Icon(Icons.auto_mode_rounded),
            ),
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              value: aiRecommend,
              onChanged: (value) => setState(() => aiRecommend = value),
              title: const Text('AI gợi ý theo loại cây'),
              secondary: const Icon(Icons.psychology_rounded),
            ),
            if (manualOffResumeAt != null) ...[
              const SizedBox(height: 8),
              Text(
                'Đang tắt tay, auto tưới sẽ bật lại sau $manualOffResumeAt',
                style: textTheme.bodyMedium?.copyWith(
                  color: colorScheme.onSurfaceVariant,
                ),
              ),
            ],
            const SizedBox(height: 8),
            _buildThresholdSlider(
              textTheme: textTheme,
              colorScheme: colorScheme,
              label: 'Bật bơm khi đất <= ${soilPumpOnBelow.round()}%',
              value: soilPumpOnBelow,
              onChanged: _setPumpOnThreshold,
            ),
            _buildThresholdSlider(
              textTheme: textTheme,
              colorScheme: colorScheme,
              label: 'Tắt bơm khi đất >= ${soilPumpOffAbove.round()}%',
              value: soilPumpOffAbove,
              onChanged: _setPumpOffThreshold,
            ),
            const SizedBox(height: 12),
            Wrap(
              spacing: 12,
              runSpacing: 12,
              children: [
                FilledButton.icon(
                  onPressed: isConnected ? _publishGardenConfig : null,
                  icon: const Icon(Icons.save_rounded),
                  label: const Text('Lưu cấu hình'),
                ),
                OutlinedButton.icon(
                  onPressed: isConnected ? _requestAIRecommendation : null,
                  icon: const Icon(Icons.tips_and_updates_rounded),
                  label: const Text('Gợi ý AI'),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildThresholdSlider({
    required TextTheme textTheme,
    required ColorScheme colorScheme,
    required String label,
    required double value,
    required ValueChanged<double> onChanged,
  }) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          label,
          style: textTheme.labelLarge?.copyWith(color: colorScheme.onSurface),
        ),
        Slider(
          value: value.clamp(0, 100).toDouble(),
          min: 0,
          max: 100,
          divisions: 100,
          label: '${value.round()}%',
          onChanged: onChanged,
        ),
      ],
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
