#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Arch Linux post_remove. nFPM copies this body into `function post_remove() { ... }`
# in .INSTALL; pacman calls it as post_remove OLD after the package files are gone.
# pacman's own systemd hook reloads unit files. It deliberately does NOT delete
# /var/lib/olivares or the `olivares` account: that directory holds the append-only
# audit ledger, the audit signing key and TLS material. To purge, run the
# manifest-checked command BEFORE removing the package:
#   sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
set -eu

cat <<'NOTICE'
Olivares AI removed. /var/lib/olivares and the olivares account remain.
pacman keeps a modified /etc/olivares/olivares.env as olivares.env.pacsave.
NOTICE
