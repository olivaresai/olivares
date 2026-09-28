#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Arch Linux post_install and post_upgrade. nFPM (v2.47.0 arch/arch.go writeScripts)
# copies this body into `function post_install() { ... }` and
# `function post_upgrade() { ... }` in .INSTALL; pacman sources .INSTALL and calls
# post_install NEW or post_upgrade NEW OLD. The body therefore stays valid inside a
# function: no top-level `return`, and `exit` ends the scriptlet.
#
# Creates the hardened, no-login system user and the data directory and places the
# install manifest. It never enables, starts or restarts the service. pacman's own
# systemd hook reloads unit files, so this scriptlet does not call daemon-reload.
# The deb/rpm/apk scripts are separate files because their argument tables differ; the
# manifest custody below is theirs (the staged record, one "mv -T", root's install-state).
set -eu

refuse() {
  echo "error: olivares package: refused: $1 — $2" >&2
  exit 1
}

olivares_pkg_init=
if [ -r /usr/lib/olivares/package-init ]; then
  read -r olivares_pkg_init < /usr/lib/olivares/package-init || olivares_pkg_init=
fi
if [ "$olivares_pkg_init" != systemd ]; then
  refuse package-init-unknown "package-init is '$olivares_pkg_init', want systemd"
fi

# Custody: /var/lib/olivares belongs to the service account, so root never writes,
# chowns or chmods by path inside it. Root's own state lives in the root-owned
# /var/lib/olivares-package: it and its parent must be real directories, owned by
# the running (root) user, with no group or other write. It is created 0755 when
# absent; anything else refuses before any effect.
package_state_dir=/var/lib/olivares-package
olivares_manifest=/var/lib/olivares/install-manifest.json
olivares_stage=$package_state_dir/install-manifest.json.new
install_state=$package_state_dir/install-state
olivares_me=$(id -u)
state_dir_safe() {
  [ -d "$1" ] && [ ! -L "$1" ] || return 1
  [ "$(stat -c %u "$1")" = "$olivares_me" ] || return 1
  case "$(stat -c %A "$1")" in ?????w????|????????w?) return 1 ;; esac
}
state_dir_safe "${package_state_dir%/*}" ||
  refuse marker-directory-unsafe "${package_state_dir%/*} is not a directory owned by uid $olivares_me and closed to group and other writes"
if [ ! -e "$package_state_dir" ] && [ ! -L "$package_state_dir" ]; then
  mkdir -m 0755 "$package_state_dir" || refuse marker-directory-unsafe "cannot create $package_state_dir"
fi
state_dir_safe "$package_state_dir" ||
  refuse marker-directory-unsafe "$package_state_dir must be a real directory owned by uid $olivares_me with no group or other write"

# Whether this package created the account is root's own record, install-state, and
# nothing else. A manifest left in the service-owned directory is never read for it:
# the service account can replace that file. Without install-state both values stay
# false, the safe side (an uninstall then keeps the account).
group_created=false
user_created=false
if [ -f "$install_state" ] && [ ! -L "$install_state" ] &&
  read -r olv_is_first <"$install_state" && [ "$olv_is_first" = schema=olivares.pkg-install-state/v1 ]; then
  if grep -qx 'group_created=true' "$install_state"; then group_created=true; fi
  if grep -qx 'user_created=true' "$install_state"; then user_created=true; fi
elif [ -e "$olivares_manifest" ] || [ -L "$olivares_manifest" ]; then
  echo "olivares package: old-manifest-unread $olivares_manifest: legacy account claims not imported; an uninstall keeps the olivares account and group" >&2
fi
if ! getent group olivares >/dev/null 2>&1; then
  groupadd --system olivares
  group_created=true
fi
if ! getent passwd olivares >/dev/null 2>&1; then
  useradd --system --gid olivares --home-dir /var/lib/olivares --no-create-home \
    --shell /usr/bin/nologin --comment "Olivares AI" olivares
  user_created=true
fi

mkdir -p /var/lib/olivares
chmod 0750 /var/lib/olivares
olivares_uid=$(id -u olivares) || {
  echo "error: service user olivares was not created" >&2
  exit 1
}
olivares_gid=$(id -g olivares) || {
  echo "error: service group olivares was not created" >&2
  exit 1
}
chown "$olivares_uid:$olivares_gid" /var/lib/olivares
dir_uid=$(stat -c '%u' /var/lib/olivares)
if [ "$dir_uid" != "$olivares_uid" ]; then
  echo "error: data dir is not owned by olivares (dir=$dir_uid want=$olivares_uid)" >&2
  exit 1
fi

# The same v2 manifest the deb/rpm packages write. pacman owns and removes the
# package paths, so none is marked managed. It keeps its final name (the uninstall
# engine reads it there), but it is written complete, with its group and mode, in the
# root-owned staging directory, then moved by one rename: "mv -T" replaces whatever
# name the service account left there (a link, a FIFO, a file) without following it,
# and never moves the file into a directory. No root path operation touches the final
# name after the rename.
rm -f "$olivares_stage"
( umask 027; cat > "$olivares_stage" <<EOF
{
  "schema": "olivares.ai/local-install/v2",
  "mode": "system",
  "init": "systemd",
  "data_dir": "/var/lib/olivares",
  "config": "/etc/olivares/olivares.env",
  "files": [
    {"path": "/usr/bin/olivares", "role": "binary", "mode": "0755", "managed": false},
    {"path": "/etc/olivares/olivares.env", "role": "config", "mode": "0644", "managed": false},
    {"path": "/usr/lib/systemd/system/olivares.service", "role": "unit", "mode": "0644", "managed": false}
  ],
  "account": {"user": "olivares", "group": "olivares", "user_created": $user_created, "group_created": $group_created},
  "manifest": "/var/lib/olivares/install-manifest.json"
}
EOF
) || refuse manifest-staging-failed "cannot write $olivares_stage"
chown "root:$olivares_gid" "$olivares_stage" || refuse manifest-staging-failed "cannot set the group of $olivares_stage"
chmod 0640 "$olivares_stage" || refuse manifest-staging-failed "cannot set the mode of $olivares_stage"
if [ -d "$olivares_manifest" ] && [ ! -L "$olivares_manifest" ]; then
  rm -f "$olivares_stage"
  refuse manifest-destination-not-replaceable "$olivares_manifest is a directory; move it aside (sudo mv $olivares_manifest $olivares_manifest.blocked) and run the package operation again"
fi
if [ "$(stat -c %d "$package_state_dir")" != "$(stat -c %d "${olivares_manifest%/*}")" ]; then
  rm -f "$olivares_stage"
  refuse manifest-staging-cross-filesystem "$package_state_dir and ${olivares_manifest%/*} are on different filesystems, so the rename would not be atomic"
fi
rm -f "$package_state_dir/.mv-probe-a" "$package_state_dir/.mv-probe-b"
: >"$package_state_dir/.mv-probe-a"
if ! mv -T "$package_state_dir/.mv-probe-a" "$package_state_dir/.mv-probe-b" 2>/dev/null ||
  [ ! -f "$package_state_dir/.mv-probe-b" ]; then
  rm -f "$package_state_dir/.mv-probe-a" "$package_state_dir/.mv-probe-b" "$olivares_stage"
  refuse manifest-rename-unsupported "mv here has no -T (no-target-directory)"
fi
rm -f "$package_state_dir/.mv-probe-b"
if ! mv -T "$olivares_stage" "$olivares_manifest"; then
  rm -f "$olivares_stage"
  refuse manifest-rename-failed "cannot rename $olivares_stage onto $olivares_manifest (a directory there?); move it aside and run the package operation again"
fi
# Root's own record of whether this package created the account, read by the next run
# (the same first line and keys as the deb/rpm/apk scripts write).
if ! ( umask 077; rm -f "$install_state.tmp" &&
  printf 'schema=olivares.pkg-install-state/v1\ngroup_created=%s\nuser_created=%s\n' "$group_created" "$user_created" >"$install_state.tmp" &&
  mv -f "$install_state.tmp" "$install_state" ); then
  refuse install-state-write-failed "cannot write $install_state"
fi

if [ "$#" -ge 2 ]; then
  cat <<EOF
Olivares AI upgraded from $2 to $1.
The package does not restart the service. If it is running, restart it:
  sudo systemctl restart olivares
EOF
else
  cat <<'EOF'
Olivares AI installed.
  1. Review /etc/olivares/olivares.env.
  2. Start it:   sudo systemctl enable --now olivares
  3. First-boot setup token is printed to the journal:  journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
The package does not enable or start the service.
Docs: https://github.com/olivaresai/olivares  ·  verify a release: scripts/verify-release.sh
EOF
fi
