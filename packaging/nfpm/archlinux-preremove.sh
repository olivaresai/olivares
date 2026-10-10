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

# The engine's table describes only its own effects, not pacman's removal.
printf '%s\n' \
  'olivares package: package manager will remove binary /usr/bin/olivares and unit /usr/lib/systemd/system/olivares.service' \
  'olivares package: config /etc/olivares/olivares.env follows pacman package-manager policy' \
  'olivares package: data /var/lib/olivares, keys and service account are preserved'

# The install record sits in the service-owned directory, so the service account can put
# something else at its name. The engine refuses anything but root's own regular file
# before any change (exit 2, exitcode.Usage); any other failure, such as a service stop
# that systemctl refuses, is exit 1. The removal then takes the same safe stop as a
# package without the record, and never fails on it.
olv_engine=skipped
if [ -x /usr/bin/olivares ] && [ -r /var/lib/olivares/install-manifest.json ]; then
  olv_engine_rc=0
  /usr/bin/olivares uninstall --preserve --data-dir /var/lib/olivares >/dev/null || olv_engine_rc=$?
  if [ "$olv_engine_rc" -eq 0 ]; then
    olv_engine=ran
  elif [ "$olv_engine_rc" -eq 2 ]; then
    olv_engine=refused
    echo "olivares package: install record refused by the uninstall engine; stopping the service without it" >&2
  else
    olv_engine=failed
    echo "olivares package: the uninstall engine failed (exit $olv_engine_rc); stopping the service without it" >&2
  fi
fi
if [ "$olv_engine" != ran ] && command -v systemctl >/dev/null 2>&1; then
  # No usable v2 ownership record: stop the unit safely and invent no path list. A stop
  # that fails is reported with its reason; the removal goes on.
  if ! olv_why=$(systemctl disable --now olivares 2>&1 >/dev/null); then
    # One printable line: systemctl may print several, and its text is not ours to trust.
    olv_why=$(printf '%s' "$olv_why" | tr -c '[:print:]' ' ')
    printf 'olivares package: the olivares service could not be stopped (systemctl disable --now olivares failed: %s); stop it yourself\n' "${olv_why:-no output}" >&2
  fi
fi
