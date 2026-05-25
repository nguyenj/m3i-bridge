# Raspberry Pi Offline Appliance

This workflow creates an SD card that installs and starts `m3i-bridge` with no
login on the Raspberry Pi. The Pi has no Wi-Fi and no internet access at
runtime. Bluetooth stays enabled because the bridge scans Keiser BLE adverts.

## Hardware Target

- Raspberry Pi with Bluetooth support. The primary target is Raspberry Pi Zero
  2 W.
- Raspberry Pi OS Lite.
- Matching `m3i-bridge-linux-*` CI artifact or release tarball.
- ANT USB stick connected to a USB data port. Pi Zero-class boards need a Micro
  USB OTG adapter.
- A reliable 5 V power supply.

For the lowest memory footprint on a Zero 2 W, use Raspberry Pi OS Lite 32-bit
and the `linux-armv7` artifact. Use `linux-arm64` only when the SD card is
flashed with Raspberry Pi OS Lite 64-bit.

## Build Or Download The Payload

From GitHub Actions, download the artifact that matches your OS architecture and
unzip it on your workstation. It should contain:

- `m3i-bridge`
- `lib/`
- `install.sh`
- `m3i-bridge.service`
- `99-coospo-ant.rules`
- `m3i-bridge-firstboot.sh`
- `m3i-bridge-preflight.sh`
- `m3i-bridge-diagnostics.sh`
- `m3i-bridge-diagnostics.service`
- `prepare-bootfs.sh`

For tagged releases, download the matching `.tar.gz` instead. Do not download
anything on the Pi.

## Prepare The SD Card

1. Open Raspberry Pi Imager on your workstation.
2. Choose your Raspberry Pi model.
3. Choose `Raspberry Pi OS Lite`. For Zero 2 W, choose the 32-bit Lite image.
4. In OS customization, set a local username and password so Raspberry Pi OS
   does not start its interactive first-boot user prompt. You do not need to
   log in with this account.
5. Do not configure Wi-Fi.
6. Do not enable SSH, Raspberry Pi Connect, VNC, or a desktop.
7. Write the SD card.
8. Remove and reinsert the SD card so the FAT boot partition is mounted on your
   workstation.
9. Run the boot preparation script from the unzipped artifact or local checkout.

For a downloaded CI artifact directory on a 32-bit install:

```sh
./m3i-bridge-linux-armv7/prepare-bootfs.sh \
  /media/$USER/bootfs \
  ./m3i-bridge-linux-armv7
```

For a release tarball on a 32-bit install:

```sh
tar -xzf m3i-bridge-linux-armv7.tar.gz
./m3i-bridge-linux-armv7/prepare-bootfs.sh \
  /media/$USER/bootfs \
  ./m3i-bridge-linux-armv7.tar.gz
```

Replace `/media/$USER/bootfs` with the actual boot partition mount path. On
macOS it will usually be under `/Volumes/bootfs`.

The script copies the payload, adds the first-boot installer, appends the
`systemd.run` boot hook to `cmdline.txt`, and adds `dtoverlay=disable-wifi` to
`config.txt`.

## First Boot Behavior

1. Eject the SD card from the workstation.
2. Plug the ANT USB stick into a USB data port. On a Pi Zero-class board, use a
   Micro USB OTG adapter and do not use the `PWR IN` port for the ANT stick.
3. Insert the SD card.
4. Power the Pi.
5. First boot installs the binary, service, diagnostics service, and ANT udev
   rule, disables common network manager services, removes the first-boot hook,
   then reboots.
6. Second boot starts `m3i-bridge.service` automatically.
7. Diagnostics are written to `m3i-bridge-diagnostics.log` on the boot
   partition at about 75 seconds, 5 minutes, and 10 minutes after each normal
   boot.

No login is required.

## What Gets Disabled

The installer disables onboard Wi-Fi in two ways:

- Adds `dtoverlay=disable-wifi` to the boot config.
- Runs `nmcli radio wifi off` and `rfkill block wifi` when available.

It also disables common IP network services:

- `NetworkManager.service`
- `NetworkManager-wait-online.service`
- `wpa_supplicant.service`
- `dhcpcd.service`
- `systemd-networkd.service`
- `systemd-networkd-wait-online.service`
- `avahi-daemon.service`
- `systemd-timesyncd.service`
- `apt-daily.timer`
- `apt-daily-upgrade.timer`

For the no-login appliance workflow, it also disables Raspberry Pi OS
first-boot/user setup services that are not needed after the boot payload is
installed:

- `userconf-pi.service`
- `userconfig.service`
- `apply_noobs_os_config.service`
- `cloud-init-main.service`
- `cloud-init.service`
- `cloud-init-local.service`
- `cloud-config.service`
- `cloud-final.service`
- `cloud-init.target`
- `cloud-config.target`
- `udisks2.service`

Do not add `dtoverlay=disable-bt`. Bluetooth is required for the Keiser scan.

## ANT Dongle Handling

The first-boot installer installs `99-coospo-ant.rules`, which grants libusb
access to the CooSpo and Garmin ANT USB stick IDs. The normal service starts
after udev trigger and has `Restart=always`, so if the ANT stick appears late
the process keeps retrying.

The bridge auto-detects both common Dynastream product IDs:

- `0fcf:1008` ANTUSB2
- `0fcf:1009` ANTUSB-m

The bridge service runs as root on the offline appliance. This avoids
headless-only failures from BlueZ D-Bus or libusb permission differences across
Raspberry Pi OS releases.

Before each bridge start, `m3i-bridge-preflight.sh` keeps Wi-Fi blocked and
unblocks/powers Bluetooth. This is required because some Raspberry Pi OS images
can preserve an rfkill block on `hci0` even when Bluetooth is installed.

The CI payload also includes the target `libusb` runtime library under `lib/`.
The installer copies it to `/usr/local/lib/m3i-bridge`, so the Pi does not need
`apt` or an internet connection to satisfy the ANT USB dependency.

## Expected Runtime

After the second boot:

- `m3i-bridge.service` is enabled.
- Wi-Fi and network managers are disabled.
- Bluetooth and `hciuart.service` remain enabled.
- The ANT USB rule is installed.
- The service opens the ANT+ power meter channel immediately and broadcasts
  zero power/cadence until the bike advertises realtime stats. This lets your
  Garmin pair after boot even before you start pedaling.
- The service also opens a separate ANT+ Bike Speed channel. It maps Keiser's
  cumulative distance into virtual wheel revolutions; it does not estimate
  speed from power, cadence, or gear.

On the Garmin, remove any old bridge sensor entries first, then add these
sensors after the Pi has reached the second boot. Keep the watch close to the
ANT USB stick while pairing.

- Power Meter: records Keiser power and cadence.
- Speed Sensor: records Keiser-derived distance and Garmin-calculated speed.
  Start pedaling before searching for this sensor, then set the speed sensor
  wheel size to `2000 mm`.

Do not add a separate cadence sensor for the bridge; cadence is already in the
Power Meter channel.

If you ever need to inspect the card without logging into the Pi, mount the SD
card on your workstation and read these files from the boot partition:

- `m3i-bridge-firstboot.log`: one-time installer result.
- `m3i-bridge-diagnostics.log`: runtime status, service logs, Bluetooth state,
  ANT USB detection, udev rules, and recent kernel messages.

## Offline Updates

Prepare a new SD card the same way with the new CI artifact or release tarball.
For this no-login appliance workflow, replacing the SD card is cleaner than
trying to update the running Pi over a network it intentionally does not have.
