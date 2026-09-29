#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# console-label.sh — the console label the boot batteries wait for, read from the host layer's own banner rather than
# kept as a second literal.
#
# The appliance layer installs appliance/layer/base/units/tty1-banner.txt in /etc/issue.d, and agetty prints it on
# every console, the serial one included: its first line is the label, followed by agetty's host name escape (\n).
#
# banner_label BANNER — prints that label (the first line without the escape and the blanks around it); exit 1 when
# BANNER is unreadable or names no label.
banner_label() {
  local label
  label=$(head -n 1 "$1" 2>/dev/null | sed 's/[[:space:]]*\\n[[:space:]]*$//; s/[[:space:]]*$//')
  [ -n "$label" ] || return 1
  printf '%s\n' "$label"
}
