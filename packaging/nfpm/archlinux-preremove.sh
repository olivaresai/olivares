#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Arch Linux pre_remove. nFPM copies this body into `function pre_remove() { ... }`
# in .INSTALL; pacman calls it as pre_remove OLD before `pacman -R` deletes the
# package files. pacman never calls it on an upgrade, so it does not have to tell an
# upgrade from a removal. Preserve stops and disables the service and keeps data,
# configuration, keys and the service account; pacman then removes the package paths.
set -eu

# The install record sits in the service-owned directory, so the service account can put
# something else at its name. The engine refuses anything but root's own regular file
# before any change; the removal then takes the same safe stop as a package without the
# record, and never fails on it.
olv_engine=skipped
if [ -x /usr/bin/olivares ] && [ -r /var/lib/olivares/install-manifest.json ]; then
  if /usr/bin/olivares uninstall --preserve --data-dir /var/lib/olivares; then
    olv_engine=ran
  else
    olv_engine=refused
    echo "olivares package: install record refused by the uninstall engine; stopping the service without it" >&2
  fi
fi
if [ "$olv_engine" != ran ] && command -v systemctl >/dev/null 2>&1; then
  # No usable v2 ownership record: stop the unit safely and invent no path list.
  systemctl disable --now olivares >/dev/null 2>&1 || true
fi
