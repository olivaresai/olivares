#!/bin/bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# images.sh — what KIWI NG runs inside the image root after config.sh and after its own SELinux labeling, before it
# creates the image (KIWI 11.0.4 tasks/system_build.py: call_config_script, setup_selinux_file_contexts, then
# call_image_script). It changes nothing. On Fedora it checks, each check failing the build, that the paths the
# appliance's policy module labels already carry the labels its file contexts give them (setfiles -n -v would change
# none), and that nothing asks for a relabel on first boot, which Fedora runs with enforcement lowered.
set -euxo pipefail

# KIWI NG's build environment, if this runs under KIWI (both files are KIWI's own).
test -f /.kconfig && . /.kconfig
test -f /.profile && . /.profile

case ",${kiwi_profiles:-}," in
  *,fedora44-server-amd64,*|*,fedora44-desktop-amd64,*) base=fedora44 ;;
  *,debian13-server-amd64,*) base=debian13 ;;
  *) printf 'images.sh: no base profile in kiwi_profiles=%s\n' "${kiwi_profiles:-}" >&2; exit 1 ;;
esac
[ "$base" = fedora44 ] || exit 0

# The units the desktop overlay enables must be in the image, each check failing the build: gdm,
# which the overlay's display-manager.service alias points at (config.sh checks the alias), and
# sshd, which multi-user.target wants and whose stock configuration serves SFTP.
case ",${kiwi_profiles:-}," in
  *,fedora44-desktop-amd64,*)
    for unit in gdm.service sshd.service; do
      if [ ! -e "/usr/lib/systemd/system/$unit" ]; then
        printf 'images.sh: %s, which the desktop overlay enables, is not in the image\n' "$unit" >&2
        exit 1
      fi
    done ;;
esac

# The installed packages' paths the module labels (olivares, olivares-appliance-base), which must be there, and the
# Appliance Console package's, which this image does not install: those are checked when present.
labeled=(/usr/bin/olivares /usr/bin/appliance-firstboot /etc/systemd/system/olivares.service.d /var/lib/olivares
         /var/lib/olivares-appliance)
labeled_when_present=(/usr/libexec/olivares /etc/olivares-portal)
paths=()
for path in "${labeled[@]}"; do
  if [ ! -e "$path" ]; then
    printf 'images.sh: %s, which the policy module labels, is not in the image\n' "$path" >&2
    exit 1
  fi
  paths+=("$path")
done
for path in "${labeled_when_present[@]}"; do
  if [ -e "$path" ]; then
    paths+=("$path")
  else
    printf 'images.sh: %s is not in this image; its label is not checked\n' "$path"
  fi
done
status=0
relabel=$(setfiles -n -v /etc/selinux/targeted/contexts/files/file_contexts "${paths[@]}" 2>&1) || status=$?
if [ "$status" -ne 0 ] || grep -qi 'relabel' <<<"$relabel"; then
  printf 'images.sh: setfiles -n -v finds labels the policy would change, or cannot check them (exit %s):\n%s\n' \
    "$status" "$relabel" >&2
  exit 1
fi
if [ -e /.autorelabel ]; then
  printf 'images.sh: /.autorelabel asks for a relabel on first boot, which runs with enforcement lowered\n' >&2
  exit 1
fi
