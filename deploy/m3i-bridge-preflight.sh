#!/usr/bin/env bash
# Keep the appliance radio state explicit: Wi-Fi off, Bluetooth on.
set -euo pipefail

if command -v rfkill >/dev/null 2>&1; then
  rfkill block wifi >/dev/null 2>&1 || true
  rfkill unblock bluetooth >/dev/null 2>&1 || true
fi

if command -v hciconfig >/dev/null 2>&1; then
  hciconfig hci0 up >/dev/null 2>&1 || true
fi

if command -v bluetoothctl >/dev/null 2>&1; then
  printf 'power on\nquit\n' | bluetoothctl >/dev/null 2>&1 || true
fi
