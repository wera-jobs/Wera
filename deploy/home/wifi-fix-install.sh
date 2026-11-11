#!/bin/sh
# One-time setup, run with sudo: Wi-Fi power saving off, and the watchdog
# that reconnects (and resets a stuck adapter). See wifi-watchdog.sh.
set -e
here=$(dirname "$0")
install -m 755 "$here/wifi-watchdog.sh" /usr/local/sbin/wifi-watchdog
install -m 644 "$here/wifi-watchdog.service" "$here/wifi-watchdog.timer" /etc/systemd/system/
install -m 644 "$here/wifi-powersave-off.conf" /etc/NetworkManager/conf.d/wifi-powersave-off.conf
nmcli connection modify TimeKeeper 802-11-wireless.powersave 2
systemctl daemon-reload
systemctl enable --now wifi-watchdog.timer
systemctl reload NetworkManager
# Reconnect once so power saving is off now (the site blips for a few seconds).
nmcli connection up TimeKeeper
echo "Done: Wi-Fi power saving off, watchdog installed."
