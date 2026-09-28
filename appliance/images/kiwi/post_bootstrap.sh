#!/bin/bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# post_bootstrap.sh — what KIWI NG runs inside the image root after the bootstrap packages and
# before the image packages are installed.
#
# It selects the locale config.xml names (<locale>en_US</locale>) for generation. The locales
# package, installed with the image packages, generates the locales /etc/locale.gen selects
# when it is configured, and keeps a selection that is already there; without this file it
# generates none, and KIWI's locale step (systemd-firstboot --locale=en_US.UTF-8) refuses a
# locale that is not installed.
#
# /etc/locale.gen is Debian's mechanism only. On Fedora the locale is the glibc-langpack-en
# package the image installs, so a Fedora root (no /etc/debian_version) gets nothing here.
set -euo pipefail

if [ -f /etc/debian_version ]; then
  printf '%s\n' 'en_US.UTF-8 UTF-8' > /etc/locale.gen
fi
