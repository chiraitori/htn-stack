class PumpHistoryEvent {
  const PumpHistoryEvent({
    required this.timestamp,
    required this.state,
    required this.source,
    required this.clientId,
    this.nodeId,
    this.mac,
    this.reason,
    this.confirmed = false,
    this.manual = false,
  });

  final DateTime timestamp;
  final String state;
  final String source;
  final String clientId;
  final String? nodeId;
  final String? mac;
  final String? reason;
  final bool confirmed;
  final bool manual;

  bool get isOn => state.toUpperCase() == 'ON';

  String get id =>
      '${timestamp.toIso8601String()}|${state.toUpperCase()}|$source|$clientId';

  factory PumpHistoryEvent.fromJson(Map<String, dynamic> json) {
    final timestampText = (json['timestamp'] ?? '').toString();
    final timestamp = DateTime.tryParse(timestampText)?.toLocal();

    return PumpHistoryEvent(
      timestamp: timestamp ?? DateTime.now(),
      state: (json['state'] ?? 'OFF').toString().toUpperCase(),
      source: (json['source'] ?? 'mqtt').toString(),
      clientId: (json['client_id'] ?? '').toString(),
      nodeId: (json['node_id'] ?? '').toString().trim().isEmpty
          ? null
          : (json['node_id'] ?? '').toString(),
      mac: (json['mac'] ?? '').toString().trim().isEmpty
          ? null
          : (json['mac'] ?? '').toString(),
      reason: (json['reason'] ?? '').toString().trim().isEmpty
          ? null
          : (json['reason'] ?? '').toString(),
      confirmed: json['confirmed'] as bool? ?? false,
      manual: json['manual'] as bool? ?? false,
    );
  }
}
