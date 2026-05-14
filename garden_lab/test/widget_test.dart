import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:garden_lab/screen/home_screen.dart';

void main() {
  testWidgets('renders the garden dashboard without MQTT auto connect', (
    tester,
  ) async {
    await tester.pumpWidget(
      const MaterialApp(home: HomeScreen(autoConnect: false)),
    );

    expect(find.text('Hệ Thống Tưới Tự Động'), findsOneWidget);
    expect(find.text('Trạng Thái Vườn'), findsOneWidget);
    expect(find.text('Điều Khiển Máy Bơm'), findsOneWidget);
  });
}
