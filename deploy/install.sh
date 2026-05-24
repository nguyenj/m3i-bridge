#!/usr/bin/env bash
# Install/update the m3i-bridge service on a Raspberry Pi.
#
# Run as root on the Pi after the binary has been built or copied from CI.
# Offline/no-network appliance mode is the default:
#   sudo ./deploy/install.sh
#   sudo ./install.sh
#
# Steps performed:
#   1. Create the m3i-bridge system user (no login shell) if missing.
#   2. Install the udev rule for the ANT USB stick.
#   3. Install the systemd unit and the m3i-bridge binary.
#   4. Install a boot diagnostic service that writes to the boot partition.
#   5. Disable Wi-Fi, common IP network services, and no-login first-boot
#      setup services unless --keep-network is set.
#   6. Reload udev, enable + start the service unless --no-start is set.
set -euo pipefail

SERVICE_NAME=m3i-bridge
SERVICE_USER=m3i-bridge
DISABLE_NETWORK=1
NO_START=0

usage() {
  cat <<EOF
usage: sudo $0 [--keep-network] [--no-start]

Install/update the m3i-bridge Raspberry Pi service.

Options:
  --keep-network   Do not disable Wi-Fi or network manager services. This is
                   intended only for development installs.
  --disable-wifi   Accepted for compatibility. Offline/no-network mode is the
                   default.
  --no-start       Install files and enable the service, but do not reload udev
                   or start/restart the service. Use this from first-boot image
                   preparation scripts that reboot after installation.
  -h, --help       Show this help text.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --disable-wifi)
      DISABLE_NETWORK=1
      ;;
    --keep-network)
      DISABLE_NETWORK=0
      ;;
    --no-start)
      NO_START=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
  shift
done

if [[ $EUID -ne 0 ]]; then
  die "must be run as root"
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

first_existing_file() {
  local candidate
  for candidate in "$@"; do
    if [[ -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

BINARY_PATH="$(first_existing_file \
  "${SCRIPT_DIR}/m3i-bridge" \
  "${SCRIPT_DIR}/../m3i-bridge")" || die "could not find m3i-bridge binary next to install.sh or in the parent directory"

SERVICE_PATH="$(first_existing_file \
  "${SCRIPT_DIR}/m3i-bridge.service" \
  "${SCRIPT_DIR}/deploy/m3i-bridge.service" \
  "${SCRIPT_DIR}/../deploy/m3i-bridge.service")" || die "could not find m3i-bridge.service"

UDEV_RULE_PATH="$(first_existing_file \
  "${SCRIPT_DIR}/99-coospo-ant.rules" \
  "${SCRIPT_DIR}/deploy/99-coospo-ant.rules" \
  "${SCRIPT_DIR}/../deploy/99-coospo-ant.rules")" || die "could not find 99-coospo-ant.rules"

LIB_DIR_PATH=""
for candidate in \
  "${SCRIPT_DIR}/lib" \
  "${SCRIPT_DIR}/deploy/lib" \
  "${SCRIPT_DIR}/../lib"
do
  if [[ -d "$candidate" ]]; then
    LIB_DIR_PATH="$candidate"
    break
  fi
done

DIAGNOSTICS_SCRIPT_PATH="$(first_existing_file \
  "${SCRIPT_DIR}/m3i-bridge-diagnostics.sh" \
  "${SCRIPT_DIR}/deploy/m3i-bridge-diagnostics.sh" \
  "${SCRIPT_DIR}/../deploy/m3i-bridge-diagnostics.sh")" || die "could not find m3i-bridge-diagnostics.sh"

PREFLIGHT_SCRIPT_PATH="$(first_existing_file \
  "${SCRIPT_DIR}/m3i-bridge-preflight.sh" \
  "${SCRIPT_DIR}/deploy/m3i-bridge-preflight.sh" \
  "${SCRIPT_DIR}/../deploy/m3i-bridge-preflight.sh")" || die "could not find m3i-bridge-preflight.sh"

DIAGNOSTICS_SERVICE_PATH="$(first_existing_file \
  "${SCRIPT_DIR}/m3i-bridge-diagnostics.service" \
  "${SCRIPT_DIR}/deploy/m3i-bridge-diagnostics.service" \
  "${SCRIPT_DIR}/../deploy/m3i-bridge-diagnostics.service")" || die "could not find m3i-bridge-diagnostics.service"

disable_wifi() {
  local boot_config

  boot_config="$(first_existing_file /boot/firmware/config.txt /boot/config.txt || true)"
  if [[ -n "$boot_config" ]] && ! grep -Eq '^[[:space:]]*dtoverlay=disable-wifi([[:space:]]*(#.*)?)?$' "$boot_config"; then
    cat >>"$boot_config" <<EOF

# Disable onboard Wi-Fi for the m3i-bridge appliance.
# Bluetooth remains enabled for Keiser BLE scanning.
dtoverlay=disable-wifi
EOF
    echo "configured onboard Wi-Fi to stay disabled after reboot via ${boot_config}"
  elif [[ -n "$boot_config" ]]; then
    echo "onboard Wi-Fi boot overlay already present in ${boot_config}"
  else
    echo "warning: could not find boot config; onboard Wi-Fi may re-enable after reboot" >&2
  fi

  if command -v nmcli >/dev/null 2>&1; then
    nmcli radio wifi off >/dev/null 2>&1 || true
  fi
  if command -v rfkill >/dev/null 2>&1; then
    rfkill block wifi >/dev/null 2>&1 || true
    rfkill unblock bluetooth >/dev/null 2>&1 || true
  fi
}

disable_networking() {
  local unit

  disable_wifi

  for unit in \
    NetworkManager.service \
    NetworkManager-wait-online.service \
    wpa_supplicant.service \
    dhcpcd.service \
    systemd-networkd.service \
    systemd-networkd-wait-online.service \
    avahi-daemon.service \
    avahi-daemon.socket \
    systemd-timesyncd.service \
    apt-daily.service \
    apt-daily.timer \
    apt-daily-upgrade.service \
    apt-daily-upgrade.timer
  do
    systemctl disable --now "$unit" >/dev/null 2>&1 || systemctl disable "$unit" >/dev/null 2>&1 || true
  done
}

disable_no_login_setup() {
  local unit

  for unit in \
    userconf-pi.service \
    userconfig.service \
    apply_noobs_os_config.service \
    cloud-init-main.service \
    cloud-init.service \
    cloud-init-local.service \
    cloud-config.service \
    cloud-final.service \
    cloud-init.target \
    cloud-config.target \
    udisks2.service
  do
    systemctl stop "$unit" >/dev/null 2>&1 || true
    systemctl disable "$unit" >/dev/null 2>&1 || true
    systemctl mask "$unit" >/dev/null 2>&1 || true
  done

  pkill -f '/usr/bin/cloud-init --all-stages' >/dev/null 2>&1 || true
  pkill -f '/usr/lib/userconf-pi/userconf-service' >/dev/null 2>&1 || true
  pkill -f 'whiptail --inputbox Please enter new username' >/dev/null 2>&1 || true
}

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

install -d -m 0755 /etc/udev/rules.d /etc/systemd/system /usr/local/bin /usr/local/lib/m3i-bridge
install -m 0644 "$UDEV_RULE_PATH" /etc/udev/rules.d/99-coospo-ant.rules
install -m 0644 "$SERVICE_PATH"  "/etc/systemd/system/${SERVICE_NAME}.service"
install -m 0755 "$BINARY_PATH"   "/usr/local/bin/${SERVICE_NAME}"
install -m 0755 "$PREFLIGHT_SCRIPT_PATH" /usr/local/lib/m3i-bridge/preflight.sh
install -m 0755 "$DIAGNOSTICS_SCRIPT_PATH" /usr/local/lib/m3i-bridge/diagnostics.sh
install -m 0644 "$DIAGNOSTICS_SERVICE_PATH" /etc/systemd/system/m3i-bridge-diagnostics.service
if [[ -n "$LIB_DIR_PATH" ]]; then
  cp -a "${LIB_DIR_PATH}/." /usr/local/lib/m3i-bridge/
fi

if [[ "$DISABLE_NETWORK" -eq 1 ]]; then
  disable_networking
  disable_no_login_setup
fi

systemctl enable "${SERVICE_NAME}.service"
systemctl enable m3i-bridge-diagnostics.service

if [[ "$NO_START" -eq 0 ]]; then
  udevadm control --reload-rules
  udevadm trigger --subsystem-match=usb

  systemctl daemon-reload
  systemctl restart "${SERVICE_NAME}.service"
  echo "installed. follow logs with: journalctl -fu ${SERVICE_NAME}"
else
  echo "installed and enabled. reboot to start ${SERVICE_NAME}.service"
fi
