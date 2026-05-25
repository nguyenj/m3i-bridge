#!/usr/bin/env bash
# One-time first-boot installer for a Raspberry Pi OS Lite SD card.
#
# This script is executed by systemd.run from cmdline.txt. It installs the
# m3i-bridge release payload from the boot partition, disables networking, and
# then lets systemd.run_success_action=reboot restart into the normal service.
set -euo pipefail

BOOT_DIR=/boot/firmware
if [[ ! -d "$BOOT_DIR" ]]; then
  BOOT_DIR=/boot
fi

LOG_PATH="${BOOT_DIR}/m3i-bridge-firstboot.log"
exec >>"$LOG_PATH" 2>&1

echo
echo "m3i-bridge first boot install starting at $(date -Is)"
rm -f "${BOOT_DIR}/m3i-bridge-diagnostics.log" "${BOOT_DIR}/m3i-bridge-firstboot.done"

die() {
  echo "error: $*" >&2
  exit 1
}

cleanup_cmdline() {
  local cmdline="${BOOT_DIR}/cmdline.txt"

  if [[ ! -f "$cmdline" ]]; then
    echo "warning: ${cmdline} not found; firstboot hook may remain configured" >&2
    return
  fi

  sed -i \
    -e 's#[[:space:]]*systemd.run=/boot/firmware/m3i-bridge-firstboot.sh##g' \
    -e 's#[[:space:]]*systemd.run=/boot/m3i-bridge-firstboot.sh##g' \
    -e 's#[[:space:]]*systemd.run_success_action=reboot##g' \
    -e 's#[[:space:]]*systemd.unit=kernel-command-line.target##g' \
    "$cmdline"
}

find_payload_tarball() {
  local candidate

  for candidate in \
    "${BOOT_DIR}/m3i-bridge-linux-armv7.tar.gz" \
    "${BOOT_DIR}"/m3i-bridge-linux-*.tar.gz
  do
    if [[ -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  return 1
}

find_payload_dir() {
  local candidate

  for candidate in \
    "${BOOT_DIR}/m3i-bridge-linux-armv7" \
    "${BOOT_DIR}"/m3i-bridge-linux-*
  do
    if [[ -d "$candidate" && -f "${candidate}/install.sh" && -f "${candidate}/m3i-bridge" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  return 1
}

WORK_DIR=/tmp/m3i-bridge-firstboot
rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

if payload_tarball="$(find_payload_tarball)"; then
  echo "installing from ${payload_tarball}"
  tar -xzf "$payload_tarball" -C "$WORK_DIR" --strip-components=1
elif payload_dir="$(find_payload_dir)"; then
  echo "installing from ${payload_dir}"
  cp -a "${payload_dir}/." "$WORK_DIR/"
elif [[ -f "${BOOT_DIR}/install.sh" && -f "${BOOT_DIR}/m3i-bridge" ]]; then
  echo "installing from release files copied directly to ${BOOT_DIR}"
  cp -a \
    "${BOOT_DIR}/install.sh" \
    "${BOOT_DIR}/m3i-bridge" \
    "${BOOT_DIR}/m3i-bridge.service" \
    "${BOOT_DIR}/99-coospo-ant.rules" \
    "${BOOT_DIR}/m3i-bridge-preflight.sh" \
    "${BOOT_DIR}/m3i-bridge-diagnostics.sh" \
    "${BOOT_DIR}/m3i-bridge-diagnostics.service" \
    "$WORK_DIR/"
  if [[ -d "${BOOT_DIR}/lib" ]]; then
    cp -a "${BOOT_DIR}/lib" "$WORK_DIR/"
  fi
else
  die "no m3i-bridge release payload found on ${BOOT_DIR}"
fi

chmod +x "${WORK_DIR}/install.sh"
if command -v file >/dev/null 2>&1; then
  file "${WORK_DIR}/m3i-bridge" || true
fi
"${WORK_DIR}/install.sh" --no-start

cleanup_cmdline
touch "${BOOT_DIR}/m3i-bridge-firstboot.done"
sync

echo "m3i-bridge first boot install complete at $(date -Is); rebooting"
