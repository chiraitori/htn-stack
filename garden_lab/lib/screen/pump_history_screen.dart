import 'package:flutter/material.dart';
import 'package:garden_lab/model/pump_history_event.dart';

class PumpHistoryScreen extends StatelessWidget {
  const PumpHistoryScreen({
    super.key,
    required this.events,
    required this.isConnected,
    required this.onRefresh,
    required this.onClear,
  });

  final List<PumpHistoryEvent> events;
  final bool isConnected;
  final Future<void> Function() onRefresh;
  final VoidCallback onClear;

  @override
  Widget build(BuildContext context) {
    final colorScheme = Theme.of(context).colorScheme;
    final textTheme = Theme.of(context).textTheme;

    return RefreshIndicator(
      onRefresh: onRefresh,
      child: ListView(
        physics: const AlwaysScrollableScrollPhysics(),
        padding: const EdgeInsets.all(16),
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  'Lịch Sử Bơm Nước',
                  style: textTheme.titleLarge?.copyWith(
                    fontWeight: FontWeight.bold,
                  ),
                ),
              ),
              IconButton.filledTonal(
                tooltip: 'Xóa lịch sử trên app',
                onPressed: events.isEmpty ? null : onClear,
                icon: const Icon(Icons.delete_sweep_rounded),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(
            isConnected
                ? 'Đang đồng bộ lịch sử gần nhất từ MQTT server'
                : 'Chờ kết nối MQTT để tải lịch sử mới nhất',
            style: textTheme.bodyMedium?.copyWith(
              color: colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 16),
          if (events.isEmpty)
            _EmptyHistory(colorScheme: colorScheme, textTheme: textTheme)
          else
            ...events.map((event) => _PumpHistoryTile(event: event)),
        ],
      ),
    );
  }
}

class _EmptyHistory extends StatelessWidget {
  const _EmptyHistory({required this.colorScheme, required this.textTheme});

  final ColorScheme colorScheme;
  final TextTheme textTheme;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(24),
      decoration: BoxDecoration(
        color: colorScheme.surfaceContainerHighest,
        borderRadius: BorderRadius.circular(24),
      ),
      child: Column(
        children: [
          Icon(
            Icons.history_toggle_off_rounded,
            size: 40,
            color: colorScheme.onSurfaceVariant,
          ),
          const SizedBox(height: 12),
          Text(
            'Chưa có lần bơm nào',
            style: textTheme.titleMedium?.copyWith(fontWeight: FontWeight.w700),
          ),
          const SizedBox(height: 4),
          Text(
            'Khi server hoặc app bật/tắt bơm, lịch sử sẽ hiện ở đây.',
            textAlign: TextAlign.center,
            style: textTheme.bodyMedium?.copyWith(
              color: colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      ),
    );
  }
}

class _PumpHistoryTile extends StatelessWidget {
  const _PumpHistoryTile({required this.event});

  final PumpHistoryEvent event;

  @override
  Widget build(BuildContext context) {
    final colorScheme = Theme.of(context).colorScheme;
    final textTheme = Theme.of(context).textTheme;
    final containerColor = event.isOn
        ? colorScheme.primaryContainer
        : colorScheme.surfaceContainerHighest;
    final onContainerColor = event.isOn
        ? colorScheme.onPrimaryContainer
        : colorScheme.onSurfaceVariant;

    return Card(
      elevation: 0,
      color: containerColor,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
      child: ListTile(
        contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
        leading: CircleAvatar(
          backgroundColor: onContainerColor.withValues(alpha: 0.12),
          foregroundColor: onContainerColor,
          child: Icon(
            event.isOn ? Icons.water_drop_rounded : Icons.water_drop_outlined,
          ),
        ),
        title: Text(
          event.isOn ? 'Bật bơm' : 'Tắt bơm',
          style: textTheme.titleMedium?.copyWith(
            color: onContainerColor,
            fontWeight: FontWeight.w700,
          ),
        ),
        subtitle: Padding(
          padding: const EdgeInsets.only(top: 4),
          child: Text(
            [
              _formatTime(event.timestamp),
              _sourceLabel(event.source, event.manual),
              if ((event.reason ?? '').isNotEmpty) event.reason!,
            ].join(' • '),
            style: textTheme.bodyMedium?.copyWith(
              color: onContainerColor.withValues(alpha: 0.82),
              height: 1.35,
            ),
          ),
        ),
      ),
    );
  }

  static String _sourceLabel(String source, bool manual) {
    if (manual || source == 'manual') {
      return 'Thủ công';
    }
    if (source == 'auto') {
      return 'Tự động';
    }
    return 'Thủ công';
  }

  static String _formatTime(DateTime time) {
    String two(int value) => value.toString().padLeft(2, '0');
    return '${two(time.hour)}:${two(time.minute)} ${two(time.day)}/${two(time.month)}/${time.year}';
  }
}
