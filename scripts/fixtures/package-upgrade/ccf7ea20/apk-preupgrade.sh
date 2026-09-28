#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# APK pre-upgrade: stop a running OpenRC service so files can be replaced.
# Do not disable it and do not start it here. If it was active, postinstall
# (used as post-upgrade) starts it again. Inactive upgrades stay inactive.
set -eu

upgrade_stamp=/run/olivares.pkg-upgrade-was-active
rm -f "$upgrade_stamp"

pkg_init() {
  if [ -r /usr/lib/olivares/package-init ]; then
    read -r OLIVARES_PKG_INIT < /usr/lib/olivares/package-init || return 1
    case "$OLIVARES_PKG_INIT" in
      systemd|openrc) return 0 ;;
      *) echo "error: unknown package-init: $OLIVARES_PKG_INIT" >&2; return 1 ;;
    esac
  fi
  if [ -x /etc/init.d/olivares ] && [ ! -e /usr/lib/systemd/system/olivares.service ]; then
    OLIVARES_PKG_INIT=openrc
    return 0
  fi
  if [ -e /usr/lib/systemd/system/olivares.service ] && [ ! -e /etc/init.d/olivares ]; then
    OLIVARES_PKG_INIT=systemd
    return 0
  fi
  return 1
}

if ! pkg_init; then
  exit 0
fi

if [ "$OLIVARES_PKG_INIT" != openrc ]; then
  exit 0
fi
if ! command -v rc-service >/dev/null 2>&1; then
  exit 0
fi
if [ ! -x /etc/init.d/olivares ]; then
  exit 0
fi
if rc-service olivares status >/dev/null 2>&1; then
  rc-service olivares stop
  : >"$upgrade_stamp"
  chmod 0600 "$upgrade_stamp"
fi
