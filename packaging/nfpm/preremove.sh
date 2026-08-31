#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs before the package is removed: stop and disable the service if systemd manages it.
set -eu

if command -v systemctl >/dev/null 2>&1; then
  systemctl stop olivares >/dev/null 2>&1 || true
  systemctl disable olivares >/dev/null 2>&1 || true
fi
