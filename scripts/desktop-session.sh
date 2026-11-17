#!/bin/sh
# Runs a command inside a headless Linux desktop session: an X display (Xvfb),
# a window manager (openbox) and a D-Bus session bus with the accessibility
# bus on it. That is what a desktop target needs on a machine without a screen
# (CI, a container).
#
#   scripts/desktop-session.sh tamesu run suites/notes.yaml
#
# Needs: xvfb openbox dbus x11-utils at-spi2-core libatk-bridge2.0-0
set -eu

if [ "$#" -eq 0 ]; then
  echo "usage: $0 COMMAND [ARG...]" >&2
  exit 2
fi

started=""
cleanup() {
  for pid in $started; do
    kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT INT TERM

export DISPLAY="${DISPLAY:-:99}"
if ! xdpyinfo >/dev/null 2>&1; then
  Xvfb "$DISPLAY" -screen 0 1280x800x24 -nolisten tcp >/dev/null 2>&1 &
  started="$started $!"
  i=0
  until xdpyinfo >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -gt 50 ]; then
      echo "desktop-session: Xvfb did not start on $DISPLAY" >&2
      exit 1
    fi
    sleep 0.2
  done
  openbox >/dev/null 2>&1 &
  started="$started $!"
fi

dbus-run-session -- "$@"
