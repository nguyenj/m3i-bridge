#!/usr/bin/env bash
# Prepare a freshly-imaged Raspberry Pi OS Lite boot partition so m3i-bridge
# installs itself on first boot with no login and no network.
set -euo pipefail

usage() {
  cat <<EOF
usage: $0 <bootfs-path> <release-tarball-or-ci-artifact-dir>

Example:
  $0 /media/\$USER/bootfs ./m3i-bridge-linux-armv7.tar.gz
  $0 /media/\$USER/bootfs ./m3i-bridge-linux-armv7

The bootfs path is the FAT boot partition mounted by your workstation after
Raspberry Pi Imager writes the SD card.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

[[ $# -eq 2 ]] || {
  usage >&2
  exit 1
}

BOOTFS="$1"
PAYLOAD="$2"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
FIRSTBOOT_SRC="${SCRIPT_DIR}/m3i-bridge-firstboot.sh"

[[ -d "$BOOTFS" ]] || die "bootfs path does not exist: ${BOOTFS}"
[[ -f "${BOOTFS}/cmdline.txt" ]] || die "cmdline.txt not found in ${BOOTFS}"
[[ -f "${BOOTFS}/config.txt" ]] || die "config.txt not found in ${BOOTFS}"
[[ -f "$FIRSTBOOT_SRC" ]] || die "firstboot script not found: ${FIRSTBOOT_SRC}"

if grep -q 'systemd.run=' "${BOOTFS}/cmdline.txt"; then
  die "cmdline.txt already contains systemd.run=. Reflash the card without OS customisation or merge the hooks manually."
fi

if [[ -f "$PAYLOAD" ]]; then
  payload_name="$(basename "$PAYLOAD")"
  if [[ "$payload_name" != m3i-bridge-linux-*.tar.gz ]]; then
    payload_name=m3i-bridge-linux-payload.tar.gz
  fi
  cp "$PAYLOAD" "${BOOTFS}/${payload_name}"
elif [[ -d "$PAYLOAD" && -f "${PAYLOAD}/install.sh" && -f "${PAYLOAD}/m3i-bridge" ]]; then
  payload_name="$(basename "$PAYLOAD")"
  if [[ "$payload_name" != m3i-bridge-linux-* ]]; then
    payload_name=m3i-bridge-linux-payload
  fi
  target_dir="${BOOTFS}/${payload_name}"
  rm -rf "$target_dir"
  mkdir -p "$target_dir"
  cp -a "${PAYLOAD}/." "$target_dir/"
else
  die "payload must be a release tarball or a CI artifact directory containing install.sh and m3i-bridge: ${PAYLOAD}"
fi

cp "$FIRSTBOOT_SRC" "${BOOTFS}/m3i-bridge-firstboot.sh"
chmod 0755 "${BOOTFS}/m3i-bridge-firstboot.sh" 2>/dev/null || true

if ! grep -Eq '^[[:space:]]*dtoverlay=disable-wifi([[:space:]]*(#.*)?)?$' "${BOOTFS}/config.txt"; then
  cat >>"${BOOTFS}/config.txt" <<EOF

# Disable onboard Wi-Fi for the m3i-bridge appliance.
# Bluetooth remains enabled for Keiser BLE scanning.
dtoverlay=disable-wifi
EOF
fi

RUN_ARGS="systemd.run=/boot/firmware/m3i-bridge-firstboot.sh systemd.run_success_action=reboot systemd.unit=kernel-command-line.target"
CMDLINE_CONTENT="$(tr '\n' ' ' <"${BOOTFS}/cmdline.txt" | sed -E 's/[[:space:]]+$//')"
printf '%s %s\n' "$CMDLINE_CONTENT" "$RUN_ARGS" >"${BOOTFS}/cmdline.txt"

sync

cat <<EOF
Prepared ${BOOTFS}.

Next steps:
  1. Eject/unmount the SD card.
  2. Insert the ANT USB stick into a USB data port. Pi Zero-class boards need an OTG adapter.
  3. Insert the SD card and power the Pi.
  4. First boot installs m3i-bridge, disables networking, and reboots.
  5. Second boot starts m3i-bridge automatically.
EOF
