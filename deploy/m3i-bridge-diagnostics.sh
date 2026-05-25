#!/usr/bin/env bash
# Write a no-login diagnostic snapshot to the Raspberry Pi boot partition.
set -u

BOOT_DIR=/boot/firmware
if [[ ! -d "$BOOT_DIR" ]]; then
  BOOT_DIR=/boot
fi

LOG_PATH="${BOOT_DIR}/m3i-bridge-diagnostics.log"
exec >"$LOG_PATH" 2>&1

section() {
  printf '\n## %s\n' "$1"
}

run() {
  printf '\n$ %s\n' "$*"
  "$@" || printf 'exit=%s\n' "$?"
}

section "time"
run date -Is

section "system"
run uname -a
if [[ -f /etc/os-release ]]; then
  run sed -n '1,120p' /etc/os-release
fi

section "m3i-bridge binary"
run ls -l /usr/local/bin/m3i-bridge
run file /usr/local/bin/m3i-bridge
run sha256sum /usr/local/bin/m3i-bridge
run ldd /usr/local/bin/m3i-bridge

section "systemd services"
run systemctl is-enabled m3i-bridge.service
run systemctl is-active m3i-bridge.service
run systemctl status m3i-bridge.service --no-pager
run systemctl status bluetooth.service --no-pager
run systemctl status hciuart.service --no-pager
run systemctl cat m3i-bridge.service
run systemctl list-units --type=service --type=target --all 'cloud-*' '*userconf*' '*userconfig*' --no-pager

section "process"
run ps -eo pid,ppid,user,stat,pcpu,pmem,args

section "m3i-bridge journal"
run journalctl -u m3i-bridge.service -b -n 240 --no-pager

section "bluetooth journal"
run journalctl -u bluetooth.service -u hciuart.service -b -n 160 --no-pager

section "bluetooth state"
if command -v rfkill >/dev/null 2>&1; then
  run rfkill list
fi
if command -v bluetoothctl >/dev/null 2>&1; then
  run bluetoothctl show
fi
if command -v hciconfig >/dev/null 2>&1; then
  run hciconfig -a
fi

section "usb devices"
if command -v lsusb >/dev/null 2>&1; then
  run lsusb
fi
run find /sys/bus/usb/devices -maxdepth 2 -type f -name idVendor -exec sh -c 'for f; do d=$(dirname "$f"); printf "%s vendor=%s product=%s product_name=%s\n" "$d" "$(cat "$d/idVendor" 2>/dev/null)" "$(cat "$d/idProduct" 2>/dev/null)" "$(cat "$d/product" 2>/dev/null)"; done' sh {} +

section "udev"
run ls -l /etc/udev/rules.d/99-coospo-ant.rules
run sed -n '1,120p' /etc/udev/rules.d/99-coospo-ant.rules

section "kernel messages"
run sh -c "dmesg | grep -Ei 'usb|bluetooth|hci|0fcf|ant|brcm|uart' | tail -n 240"

sync
