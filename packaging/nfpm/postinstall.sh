#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Runs after .deb/.rpm install and after .apk install or upgrade. Creates a
# hardened, no-login system user and the data directory. It does NOT enable or
# start the service. Package type comes from the packaged stamp (or, for
# packages that predate the stamp, from which unit this package installed) —
# never from whether systemctl happens to be on PATH.
set -eu

pkg_init() {
  if [ -r /usr/lib/olivares/package-init ]; then
    read -r OLIVARES_PKG_INIT < /usr/lib/olivares/package-init || return 1
    case "$OLIVARES_PKG_INIT" in
      systemd|openrc) return 0 ;;
      *) echo "error: unknown package-init: $OLIVARES_PKG_INIT" >&2; return 1 ;;
    esac
  fi
  if [ -x /etc/init.d/olivares ] && [ ! -e /usr/lib/systemd/system/olivares.service ]; then
    OLIVARES_PKG_INIT=openrc
    return 0
  fi
  if [ -e /usr/lib/systemd/system/olivares.service ] && [ ! -e /etc/init.d/olivares ]; then
    OLIVARES_PKG_INIT=systemd
    return 0
  fi
  echo "error: cannot determine package init from installed units" >&2
  return 1
}

pkg_init

# Create the service group + user across glibc (useradd/groupadd) and Alpine/busybox
# (addgroup/adduser) toolchains. Idempotent: skip if they already exist.
group_created=false
user_created=false
if [ -r /var/lib/olivares/install-manifest.json ]; then
  if grep -Fq '"group_created": true' /var/lib/olivares/install-manifest.json; then group_created=true; fi
  if grep -Fq '"user_created": true' /var/lib/olivares/install-manifest.json; then user_created=true; fi
fi
if ! getent group olivares >/dev/null 2>&1; then
  if groupadd --system olivares 2>/dev/null || addgroup -S olivares 2>/dev/null; then
    group_created=true
  fi
fi
if ! getent passwd olivares >/dev/null 2>&1; then
  if useradd --system --gid olivares --home-dir /var/lib/olivares \
          --shell /usr/sbin/nologin --comment "Olivares AI" olivares 2>/dev/null \
    || adduser -S -D -H -G olivares -h /var/lib/olivares -s /sbin/nologin olivares 2>/dev/null; then
    user_created=true
  fi
fi

mkdir -p /var/lib/olivares /etc/olivares
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
# OpenRC start-stop-daemon opens output_log after switching to command_user.
# A data-dir log was EACCES (Alpine 3.22 OpenRC 0.62.6). The packaged unit
# writes /var/log/olivares.log instead.
if [ "$OLIVARES_PKG_INIT" = openrc ]; then
  if [ ! -e /var/log/olivares.log ]; then
    : >/var/log/olivares.log
  fi
  chown "$olivares_uid:$olivares_gid" /var/log/olivares.log
  chmod 0640 /var/log/olivares.log
  log_uid=$(stat -c '%u' /var/log/olivares.log)
  if [ "$log_uid" != "$olivares_uid" ]; then
    echo "error: /var/log/olivares.log is not owned by olivares (log=$log_uid want=$olivares_uid)" >&2
    exit 1
  fi
fi
dir_uid=$(stat -c '%u' /var/lib/olivares)
if [ "$dir_uid" != "$olivares_uid" ]; then
  echo "error: data dir is not owned by olivares (dir=$dir_uid want=$olivares_uid)" >&2
  exit 1
fi
if [ ! -e /etc/olivares/olivares.env ] && [ -f /usr/share/olivares/olivares.env.example ]; then
  cp /usr/share/olivares/olivares.env.example /etc/olivares/olivares.env
  chmod 0640 /etc/olivares/olivares.env
  chown "root:$olivares_gid" /etc/olivares/olivares.env
fi

if [ "$OLIVARES_PKG_INIT" = openrc ]; then
  unit_path=/etc/init.d/olivares
  unit_mode=0755
else
  unit_path=/usr/lib/systemd/system/olivares.service
  unit_mode=0644
fi

# Package-owned paths are listed but not marked managed: dpkg/rpm/apk removes
# them. The same v2 manifest still drives plan/preserve/purge and bounds an
# explicit data purge to the release-index install_layout.
cat > /var/lib/olivares/install-manifest.json <<EOF
{
  "schema": "olivares.ai/local-install/v2",
  "mode": "system",
  "init": "$OLIVARES_PKG_INIT",
  "data_dir": "/var/lib/olivares",
  "config": "/etc/olivares/olivares.env",
  "files": [
    {"path": "/usr/bin/olivares", "role": "binary", "mode": "0755", "managed": false},
    {"path": "/etc/olivares/olivares.env", "role": "config", "mode": "0640", "managed": false},
    {"path": "$unit_path", "role": "unit", "mode": "$unit_mode", "managed": false}
  ],
  "account": {"user": "olivares", "group": "olivares", "user_created": $user_created, "group_created": $group_created},
  "manifest": "/var/lib/olivares/install-manifest.json"
}
EOF
chmod 0640 /var/lib/olivares/install-manifest.json
chown root:olivares /var/lib/olivares/install-manifest.json 2>/dev/null || true

upgrade_stamp=/run/olivares.pkg-upgrade-was-active
if [ "$OLIVARES_PKG_INIT" = systemd ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
if [ "$OLIVARES_PKG_INIT" = openrc ] && [ -f "$upgrade_stamp" ]; then
  if command -v rc-service >/dev/null 2>&1; then
    rc-service olivares start
  fi
  rm -f "$upgrade_stamp"
fi

if [ "$OLIVARES_PKG_INIT" = openrc ]; then
  cat <<'EOF'
Olivares AI installed.
  1. Review /etc/olivares/olivares.env (listeners default to loopback-only).
  2. Start it:   sudo rc-service olivares start
     Optional enable (not done by the package): sudo rc-update add olivares default
  3. First-boot setup token is in /var/log/olivares.log
     (and in the system log if syslogd is running):
       sed -n '/FIRST-BOOT SETUP/,/========================/p' /var/log/olivares.log
       logread | sed -n '/FIRST-BOOT SETUP/,/========================/p'
The package does not enable or start the service.
Docs: https://github.com/olivaresai/olivares  ·  verify a release: scripts/verify-release.sh
EOF
else
  cat <<'EOF'
Olivares AI installed.
  1. Review /etc/olivares/olivares.env (listeners default to loopback-only).
  2. Start it:   sudo systemctl enable --now olivares
  3. First-boot setup token is printed to the journal:  journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
Docs: https://github.com/olivaresai/olivares  ·  verify a release: scripts/verify-release.sh
EOF
fi
