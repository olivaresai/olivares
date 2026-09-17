#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# FIXTURE, not a product hook: the pre-OpenRC pre-remove, reduced to the lines
# scripts/test-nfpm-openrc.sh reads. It classifies the package by whether systemctl is
# on PATH, so on Alpine it validates the ownership record and stops NOTHING: it never
# asks OpenRC to stop the service. (The gate greps this file for that OpenRC stop
# command, so this comment must not spell it out.) Built into a throwaway package and
# inspected; never installed. Deliberately wrong — do not correct it. See README.md.
set -eu

if [ -x /usr/bin/olivares ] && [ -r /var/lib/olivares/install-manifest.json ]; then
  if command -v systemctl >/dev/null 2>&1; then
    /usr/bin/olivares uninstall --preserve --data-dir /var/lib/olivares
  else
    # APK carries the same package-owned systemd unit but Alpine has no
    # systemd daemon to stop. Validate the ownership record without inventing
    # a successful systemctl action; apk removes its own paths next.
    /usr/bin/olivares uninstall --plan --data-dir /var/lib/olivares >/dev/null
  fi
elif command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now olivares >/dev/null 2>&1 || true
fi
