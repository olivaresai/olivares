#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# firstboot-predicates.sh — the assertions of the delivered first-boot battery, reused instead
# of copied.
#
# appliance/layer/base/fixture/firstboot-battery.sh is the battery the appliance layer shipped
# with: the named assertions about a first-boot record, each with its negative control. Those
# assertions read FILES - a state record, a journal, a flag - and they know nothing about where
# those files were captured. The layer's battery captures them from a container; the image
# phase captures the same files from a virtual machine. Copying the predicates would be two
# definitions of one contract, and the copy would be the one that goes stale.
#
# So this loads them. Two sections of that file - its harness and its assertions - are shell
# functions with no container call in them, and they are what gets loaded here. The self-test
# (appliance/test/battery-selftest.sh) checks exactly that: the loaded text defines the named
# predicates, it contains no container call, and the predicates still answer both ways.
#
# It is sourced, not run. The caller defines `say`, `failures` and `evidence` first, which is
# what the delivered harness writes its findings through.

firstboot_predicates_here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# firstboot_predicates_source — the delivered battery this reuses.
firstboot_predicates_source() {
  printf '%s\n' "$firstboot_predicates_here/../../layer/base/fixture/firstboot-battery.sh"
}

# firstboot_predicates_extract — its harness and its assertions, as text. The two section
# markers are the delivered file's own; a delivered file that stops having them yields nothing,
# and the self-test goes red rather than this loading something else by accident.
firstboot_predicates_extract() {
  local file
  file=$(firstboot_predicates_source)
  if [ ! -r "$file" ]; then
    printf 'firstboot-predicates: the delivered battery is not at %s\n' "$file" >&2
    return 2
  fi
  awk '
    /^# ---- the harness/ { inside = 1 }
    /^# ---- the fixture/ { inside = 0 }
    inside { print }
  ' "$file"
}

# firstboot_predicates_load — define them in this shell.
firstboot_predicates_load() {
  local text
  text=$(firstboot_predicates_extract) || return 2
  [ -n "$text" ] || { printf 'firstboot-predicates: nothing to load\n' >&2; return 2; }
  eval "$text"
}
