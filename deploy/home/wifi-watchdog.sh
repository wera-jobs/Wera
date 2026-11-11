#!/bin/sh
# Keeps this PC's Wi-Fi up for the public site (run every 2 minutes by
# wifi-watchdog.timer, as root). The USB adapter (RTL8188EUS on the
# rtl8xxxu driver) sometimes stops talking to the router while the router
# is fine: the router drops it "for inactivity" and the adapter cannot
# even see the network until it is reset.
#   1. Down, or up without reaching the router: reconnect.
#   2. Still down at the next check: reload the adapter's driver, then reconnect.
CONN=TimeKeeper
DEV=wlx8416f9096ec9
DRIVER=rtl8xxxu
STATE=/run/wifi-watchdog.failures

up() {
	nmcli -t -f GENERAL.STATE device show "$DEV" 2>/dev/null | grep -q '(connected)' || return 1
	gw=$(ip -4 route show default dev "$DEV" 2>/dev/null | awk '{print $3; exit}')
	[ -n "$gw" ] && ping -c 2 -W 2 -I "$DEV" "$gw" >/dev/null 2>&1
}

if up; then
	rm -f "$STATE"
	exit 0
fi
n=$(($(cat "$STATE" 2>/dev/null || echo 0) + 1))
echo "$n" >"$STATE"
if [ "$n" -ge 2 ]; then
	echo "Wi-Fi down for $n checks: reloading the $DRIVER driver"
	modprobe -r "$DRIVER" && sleep 2 && modprobe "$DRIVER"
	sleep 8
fi
echo "Wi-Fi down: reconnecting $CONN"
nmcli connection up "$CONN" || exit 1
