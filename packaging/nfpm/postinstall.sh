#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# nFPM ships this file as the Debian postinst, the RPM %post and the APK
# .post-install and .post-upgrade. Creates a hardened, no-login system user and
# the data directory. It does NOT enable the service, and on DEB/RPM it does not
# start or restart it. Package type comes from the packaged stamp (or, for
# packages that predate the stamp, from which unit this package installed) —
# never from whether systemctl happens to be on PATH — and a closed table of the
# arguments each package manager passes:
#   systemd  configure [OLD]                       deb  install, or upgrade from OLD
#   systemd  abort-upgrade|abort-remove|abort-deconfigure ...   deb  unwind
#   systemd  N (digits; 1 = install)               rpm  install (1) or upgrade (2 or more)
#   systemd  (none)                                manual run: install effects only
#   openrc   [NEW [OLD]]                           apk  install or upgrade (the stamp decides the start)
# Any other combination is refused by name before any effect.
# A Debian upgrade treats the replaced prerm as one that predates the upgrade
# contract (and so stopped and disabled olivares) only when no prerm record names
# the old version it is given, and that version sorts before the first contract
# version by "dpkg --compare-versions". A service a deconfigure stopped is started
# again from that prerm's record only on abort-deconfigure; configure consumes the
# record without a start and prints the start instruction instead.
# On the appliance (the package-phase bridge is installed) it never starts or
# restarts the product and prints only the appliance notice: the bridge's finish
# phase owns that start.
set -eu

pkg_init() {
  if [ -r /usr/lib/olivares/package-init ]; then
    read -r OLIVARES_PKG_INIT < /usr/lib/olivares/package-init || return 1
    case "$OLIVARES_PKG_INIT" in
      systemd|openrc) return 0 ;;
      *) echo "error: olivares package: refused: package-init-unknown — package-init is '$OLIVARES_PKG_INIT'" >&2
         receipt unknown unknown refused:package-init-unknown; return 1 ;;
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
  echo "error: olivares package: refused: package-init-unknown — cannot determine package init from installed units" >&2
  receipt unknown unknown refused:package-init-unknown
  return 1
}

olv_script=postinstall
# --- v1 package records: the agreed m-5 schema (pkg-l1-m5-schema/RESPONSE-D-M5-CONTRAST.md).
# The same block is in every maintainer script, because nFPM ships each file alone.
# pkg-phases gets one line per record in a fixed key order (at most 512 bytes); the
# key=value records begin with schema= and the common fields (at most 1024 bytes).
# A failed write is printed, never silent, and the package action goes on.
olv_phases=/run/olivares.pkg-phases
olv_old=unknown
olv_new=unknown
olv_stopped=n/a
olv_prior=n/a
olv_pid=$$
olv_ppid=$PPID
# olv_ver VALUE: VALUE when it is [0-9A-Za-z.+~:-]{1,40}, else unknown.
olv_ver() {
  case "${1-}" in ''|*[!0-9A-Za-z.+~:-]*) echo unknown; return 0 ;; esac
  if [ "${#1}" -le 40 ]; then echo "$1"; else echo unknown; fi
}
# olv_starttime PID: field 22 of /proc/PID/stat, counted after the last ')', since the
# command name (field 2) may hold spaces and parentheses; unknown if unreadable.
olv_starttime() {
  olv_st=
  if [ -r "/proc/$1/stat" ]; then read -r olv_st 2>/dev/null <"/proc/$1/stat" || true; fi
  case "$olv_st" in *\)*) ;; *) echo unknown; return 0 ;; esac
  olv_st=${olv_st##*\)}
  set -- $olv_st
  if [ $# -lt 20 ]; then echo unknown; return 0; fi
  eval "olv_st=\${20}"
  case "$olv_st" in ''|*[!0-9]*) echo unknown ;; *) echo "$olv_st" ;; esac
}
olv_pid_start=$(olv_starttime "$olv_pid")
olv_ppid_start=$(olv_starttime "$olv_ppid")
olv_boot=unknown
if [ -r /proc/sys/kernel/random/boot_id ]; then
  olv_b=
  read -r olv_b 2>/dev/null </proc/sys/kernel/random/boot_id || true
  case "$olv_b" in
    *[!0-9a-f-]*|'') ;;
    *) if [ "${#olv_b}" -eq 36 ]; then olv_boot=$olv_b; fi ;;
  esac
fi
# batch=: copied from the bridge's file on the appliance, never minted.
olv_batch=none
if [ -x /usr/libexec/olivares/olivares-product-package-phase ]; then
  olv_batch=absent
  if [ -f /run/olivares-bridge/batch ] && [ ! -L /run/olivares-bridge/batch ]; then
    olv_v= olv_tx= olv_bn= olv_x=
    read -r olv_v olv_tx olv_bn olv_x 2>/dev/null </run/olivares-bridge/batch || olv_v=
    if [ "$olv_v" = v1 ] && [ -z "$olv_x" ] && [ "${#olv_tx}" -eq 32 ] && [ "${#olv_bn}" -le 20 ]; then
      case "$olv_tx" in
        *[!0-9a-f]*) ;;
        *) case "$olv_bn" in ''|0*|*[!0-9]*) ;; *) olv_batch="$olv_tx.$olv_bn" ;; esac ;;
      esac
    fi
  fi
fi
# olv_maint FORMAT: maintscript= for that format.
olv_maint() {
  case "$1" in
    deb) case "${DPKG_MAINTSCRIPT_NAME-}" in
           preinst|postinst|prerm|postrm) echo "$DPKG_MAINTSCRIPT_NAME" ;;
           *) echo unknown ;;
         esac ;;
    rpm|apk) echo n/a ;;
    *) echo unknown ;;
  esac
}
# olv_active STATE: the prior= value for a unit state.
olv_active() {
  case "$1" in active|inactive|failed) echo "$1" ;; *) echo unknown ;; esac
}
# receipt FORMAT ACTION RESULT: append this script's v1 line to pkg-phases.
receipt() {
  olv_f=$1
  case "$olv_f" in deb|rpm|apk) ;; *) olv_f=unknown ;; esac
  olv_a=$2
  case "$olv_a" in ''|*[!a-z-]*) olv_a=unknown ;; esac
  olv_now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  olv_line="v1 $olv_now script=$olv_script maintscript=$(olv_maint "$olv_f") format=$olv_f action=$olv_a result=$3 old=$(olv_ver "$olv_old") new=$(olv_ver "$olv_new") stopped=$olv_stopped prior=$olv_prior boot=$olv_boot pid=$olv_pid pid_start=$olv_pid_start ppid=$olv_ppid ppid_start=$olv_ppid_start batch=$olv_batch contract=upgrade-dispatch-v1"
  if [ "${#olv_line}" -ge 512 ]; then
    olv_line="v1 $olv_now script=$olv_script result=record-too-large"
  fi
  if [ -f "$olv_phases" ] && [ ! -L "$olv_phases" ]; then
    olv_size=$(wc -c <"$olv_phases" 2>/dev/null | tr -d ' ') || olv_size=0
    case "$olv_size" in ''|*[!0-9]*) olv_size=0 ;; esac
    if [ $((olv_size + ${#olv_line} + 1)) -gt 65536 ]; then
      mv -f "$olv_phases" "$olv_phases.1" 2>/dev/null || true
    fi
  fi
  if ! ( umask 077; printf '%s\n' "$olv_line" >>"$olv_phases" ) 2>/dev/null; then
    echo "olivares package: receipt-not-written $olv_phases" >&2
  fi
  return 0
}
# olv_record NAME FILE FORMAT KEY=VALUE...: write a v1 record, staged as FILE.tmp and
# moved into place; past 1024 bytes it is the short record-too-large record. A failed
# write is printed and receipted, and returns 1.
olv_record() {
  olv_rn=$1 olv_rf=$2 olv_rfmt=$3
  shift 3
  case "$olv_rfmt" in deb|rpm|apk) ;; *) olv_rfmt=unknown ;; esac
  olv_rec="schema=olivares.pkg-$olv_rn/v1
boot=$olv_boot
pid=$olv_pid
pid_start=$olv_pid_start
ppid=$olv_ppid
ppid_start=$olv_ppid_start
batch=$olv_batch
maintscript=$(olv_maint "$olv_rfmt")
format=$olv_rfmt
old=$(olv_ver "$olv_old")
new=$(olv_ver "$olv_new")
contract=upgrade-dispatch-v1"
  for olv_kv in "$@"; do
    olv_rec="$olv_rec
$olv_kv"
  done
  if [ $((${#olv_rec} + 1)) -gt 1024 ]; then
    olv_rec="schema=olivares.pkg-$olv_rn/v1
result=record-too-large"
  fi
  if ( umask 077; rm -f "$olv_rf.tmp" && printf '%s\n' "$olv_rec" >"$olv_rf.tmp" && mv -f "$olv_rf.tmp" "$olv_rf" ) 2>/dev/null; then
    return 0
  fi
  echo "olivares package: record-not-written $olv_rn" >&2
  receipt "$olv_rfmt" "${action:-unknown}" "record-not-written:$olv_rn"
  return 1
}
# olv_record_ok NAME FILE: a regular, non-link v1 record of this boot within the bound.
# Anything else is absent: stale, foreign, oversized or unknown records are never honored.
olv_record_ok() {
  [ -f "$2" ] && [ ! -L "$2" ] || return 1
  olv_rs=$(wc -c <"$2" 2>/dev/null | tr -d ' ') || return 1
  case "$olv_rs" in ''|*[!0-9]*) return 1 ;; esac
  [ "$olv_rs" -le 1024 ] || return 1
  olv_l1=
  read -r olv_l1 <"$2" || return 1
  [ "$olv_l1" = "schema=olivares.pkg-$1/v1" ] || return 1
  [ "$olv_boot" != unknown ] || return 1
  grep -qx "boot=$olv_boot" "$2"
}
# --- end of the v1 record block
refuse() {
  echo "error: olivares package: refused: $1 — $2" >&2
  receipt "${format:-unknown}" "${action:-unknown}" "refused:$1"
  exit 1
}
is_count() {
  case "${1-}" in ''|*[!0-9]*) return 1 ;; esac
}
is_apk_version() {
  case "${1-}" in [0-9]*) ;; *) return 1 ;; esac
  case "$1" in *[!0-9A-Za-z._+~-]*) return 1 ;; esac
}

pkg_init

format=
action=
configured_old=
if [ "$OLIVARES_PKG_INIT" = openrc ]; then
  [ $# -le 2 ] || refuse argument-class-inconsistent "package-init openrc with arguments '$*' (apk passes at most two versions)"
  for olivares_arg in "$@"; do
    is_apk_version "$olivares_arg" ||
      refuse argument-class-inconsistent "package-init openrc with argument '$olivares_arg' (apk passes versions)"
  done
  format=apk
  case $# in 2) action=upgrade ;; 1) action=install ;; *) action=unspecified ;; esac
  if [ $# -eq 2 ]; then
    # APK passes NEW-VERSION OLD-VERSION to the post-upgrade (apk-tools).
    olv_new=$(olv_ver "$1")
    olv_old=$(olv_ver "$2")
  fi
elif [ $# -eq 0 ]; then
  format=unspecified action=install
else
  case "$1" in
    configure)
      format=deb configured_old=${2-}
      olv_old=$(olv_ver "$configured_old")
      if [ -n "$configured_old" ]; then action=upgrade; else action=install; fi ;;
    abort-upgrade|abort-remove|abort-deconfigure) format=deb action=$1 ;;
    triggered)
      # No dpkg trigger interest is declared; a later slice that adds one adds its class.
      format=deb
      refuse unknown-action "no trigger interest is declared for olivares" ;;
    *)
      if [ $# -eq 1 ] && is_count "$1" && [ "$1" -ge 1 ]; then
        format=rpm
        if [ "$1" -ge 2 ]; then action=upgrade; else action=install; fi
      elif is_count "$1" || is_apk_version "$1"; then
        refuse argument-class-inconsistent "package-init systemd with arguments '$*'"
      else
        refuse argument-class-unknown "'$1' is neither a Debian postinst action nor an RPM count"
      fi
      ;;
  esac
fi

# Custody: /var/lib/olivares belongs to the service account, so root never writes,
# chowns or chmods by path inside it. Root's own state lives in the root-owned
# /var/lib/olivares-package: it and its parent must be real directories, owned by
# the running (root) user, with no group or other write. It is created 0755 when
# absent; anything else refuses before any effect.
package_state_dir=/var/lib/olivares-package
contract_marker=$package_state_dir/scripts-contract
olivares_manifest=/var/lib/olivares/install-manifest.json
olivares_stage=$package_state_dir/install-manifest.json.new
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

# The contract marker: every postinstall of this contract writes it after it
# renames the manifest into place. Its first line is the contract, then the inode
# and mtime of the manifest it placed (read from the root-owned staging name; a
# rename keeps both). Any older postinstall rewrites the manifest, which changes
# the mtime, and a copy has another inode. So the installed package's scripts
# honor the upgrade contract only when the current manifest still has both. It
# is read here, before this run replaces the manifest.
replaced_honors_contract=no
olv_marker_first=
if [ -f "$contract_marker" ] && [ ! -L "$contract_marker" ]; then
  read -r olv_marker_first <"$contract_marker" || olv_marker_first=
fi
if [ "$olv_marker_first" = upgrade-dispatch-v1 ] &&
  [ -f "$olivares_manifest" ] && [ ! -L "$olivares_manifest" ]; then
  olv_want_ino=$(sed -n 's/^manifest_ino=//p' "$contract_marker")
  olv_want_mtime=$(sed -n 's/^manifest_mtime=//p' "$contract_marker")
  olv_cur_ino=$(stat -c %i "$olivares_manifest" 2>/dev/null || true)
  olv_cur_mtime=$(stat -c %y "$olivares_manifest" 2>/dev/null | tr ' ' '_')
  if [ -n "$olv_want_ino" ] && [ "$olv_cur_ino" = "$olv_want_ino" ] &&
    [ -n "$olv_want_mtime" ] && [ "$olv_cur_mtime" = "$olv_want_mtime" ]; then
    replaced_honors_contract=yes
  fi
fi

# Create the service group + user across glibc (useradd/groupadd) and Alpine/busybox
# (addgroup/adduser) toolchains. Idempotent: skip if they already exist.
# Whether this package created them is root's own record, install-state, in the
# root-owned directory, and nothing else. A manifest left by an older package is
# never read for them: it sits in the service-owned directory, so the service
# account can replace it, and the older postinstall copied these two values from
# whatever file stood at that name. Without install-state both stay false, the
# safe side (an uninstall then keeps the account), with the receipt
# old-manifest-unread; from this run on, install-state carries them.
group_created=false
user_created=false
install_state=$package_state_dir/install-state
if [ -f "$install_state" ] && [ ! -L "$install_state" ] &&
  read -r olv_is_first <"$install_state" && [ "$olv_is_first" = schema=olivares.pkg-install-state/v1 ]; then
  if grep -qx 'group_created=true' "$install_state"; then group_created=true; fi
  if grep -qx 'user_created=true' "$install_state"; then user_created=true; fi
elif [ -e "$olivares_manifest" ] || [ -L "$olivares_manifest" ]; then
  echo "olivares package: old-manifest-unread $olivares_manifest: legacy account claims not imported; an uninstall keeps the olivares account and group" >&2
  receipt "$format" "$action" old-manifest-unread
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

# deb/rpm and apk upgrades share this migration. APK normally uses OpenRC, but
# preserve or replace a previous native systemd drop-in if one is present.
/bin/sh /usr/share/olivares/migrate-agentops-dropin.sh /etc/systemd/system/olivares.service.d/agentops.conf

# Package-owned paths are listed but not marked managed: dpkg/rpm/apk removes
# them. The same v2 manifest still drives plan/preserve/purge and bounds an
# explicit data purge to the release-index install_layout.
# The manifest keeps its final name (the uninstall engine reads it there), but it is
# written complete, with its group and mode, in the root-owned staging directory,
# then moved by one rename: "mv -T" replaces whatever name the service account left
# there (a link, a FIFO, a device link, a file) without following it, and never
# moves the file into a directory. No root path operation touches the final name
# after the rename.
rm -f "$olivares_stage"
( umask 027; cat > "$olivares_stage" <<EOF
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
) || refuse manifest-staging-failed "cannot write $olivares_stage"
chown root:olivares "$olivares_stage" || refuse manifest-staging-failed "cannot set the group of $olivares_stage"
chmod 0640 "$olivares_stage" || refuse manifest-staging-failed "cannot set the mode of $olivares_stage"
if [ -d "$olivares_manifest" ] && [ ! -L "$olivares_manifest" ]; then
  rm -f "$olivares_stage"
  refuse manifest-destination-not-replaceable "$olivares_manifest is a directory; move it aside (sudo mv $olivares_manifest $olivares_manifest.blocked) and run the package operation again"
fi
if [ "$(stat -c %d "$package_state_dir")" != "$(stat -c %d "${olivares_manifest%/*}")" ]; then
  rm -f "$olivares_stage"
  refuse manifest-staging-cross-filesystem "$package_state_dir and ${olivares_manifest%/*} are on different filesystems, so the rename would not be atomic"
fi
# "mv -T" (no target directory) is GNU coreutils on DEB and RPM; busybox on APK is
# probed the same way. Without it the rename could move the file into a directory.
rm -f "$package_state_dir/.mv-probe-a" "$package_state_dir/.mv-probe-b"
: >"$package_state_dir/.mv-probe-a"
if ! mv -T "$package_state_dir/.mv-probe-a" "$package_state_dir/.mv-probe-b" 2>/dev/null ||
  [ ! -f "$package_state_dir/.mv-probe-b" ]; then
  rm -f "$package_state_dir/.mv-probe-a" "$package_state_dir/.mv-probe-b" "$olivares_stage"
  refuse manifest-rename-unsupported "mv here has no -T (no-target-directory)"
fi
rm -f "$package_state_dir/.mv-probe-b"
olv_placed_ino=$(stat -c %i "$olivares_stage")
olv_placed_mtime=$(stat -c %y "$olivares_stage" | tr ' ' '_')
if ! mv -T "$olivares_stage" "$olivares_manifest"; then
  rm -f "$olivares_stage"
  refuse manifest-rename-failed "cannot rename $olivares_stage onto $olivares_manifest (a directory there?); move it aside and run the package operation again"
fi
olv_record install-state "$install_state" "$format" \
  "group_created=$group_created" "user_created=$user_created" || true
if ! { rm -f "$contract_marker" &&
  printf 'upgrade-dispatch-v1\nmanifest_ino=%s\nmanifest_mtime=%s\n' "$olv_placed_ino" "$olv_placed_mtime" >"$contract_marker" &&
  chmod 0644 "$contract_marker"; } 2>/dev/null; then
  refuse marker-write-failed "cannot write $contract_marker"
fi

upgrade_stamp=/run/olivares.pkg-upgrade-was-active
upgrade_state=/run/olivares.pkg-upgrade-state
deconfigured=/run/olivares.pkg-deconfigured
pending=/run/olivares.pkg-pending
package_phase=/usr/libexec/olivares/olivares-product-package-phase
# The first release whose prerm honors the upgrade contract; "~" sorts before
# any of its pre-releases.
first_contract_version='26.10.0~'
unit_enabled=
unit_active=
if [ "$OLIVARES_PKG_INIT" = systemd ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  unit_enabled=$(systemctl is-enabled olivares 2>/dev/null || true)
  unit_active=$(systemctl is-active olivares 2>/dev/null || true)
  case "$unit_enabled" in ''|*[!a-z-]*) unit_enabled=unknown ;; esac
  case "$unit_active" in ''|*[!a-z-]*) unit_active=unknown ;; esac
fi

# The Debian records of this transaction are read, then consumed: configure and
# abort-upgrade end an upgrade; configure and abort-deconfigure end a deconfigure.
record_old=
deconfigured_active=no
if [ "$format" = deb ]; then
  if olv_record_ok upgrade-state "$upgrade_state"; then
    record_old=$(sed -n 's/^old=//p' "$upgrade_state")
    [ "$record_old" != unknown ] || record_old=
  fi
  case "$action" in
    install|upgrade|abort-upgrade) rm -f "$upgrade_state" ;;
  esac
  # Only abort-deconfigure honors the deconfigure record; configure (install or
  # upgrade) and abort-upgrade consume it without a start.
  case "$action" in
    install|upgrade|abort-upgrade|abort-deconfigure)
      if olv_record_ok deconfigured "$deconfigured" && grep -qx 'active=active' "$deconfigured"; then
        deconfigured_active=yes
      fi
      rm -f "$deconfigured" ;;
  esac
fi

# write_pending CONDITION REASON PRIOR INSTRUCTION: the visible pending record.
write_pending() {
  olv_record pending "$pending" "$format" \
    "condition=$1" "reason=$2" "prior=$3" "instruction=$4" || true
}

if [ -x "$package_phase" ]; then
  # The appliance: the package-phase bridge recorded the prior state before any
  # prerm and its finish phase is the only owner of the product start. This
  # script never starts or restarts the product here, even after an APK stop or
  # a deconfigure.
  olivares_prior="$unit_enabled/$unit_active"
  if [ "$OLIVARES_PKG_INIT" = openrc ]; then
    if [ -f "$upgrade_stamp" ]; then olivares_prior=active-before-upgrade; else olivares_prior=unknown; fi
  fi
  if [ "$deconfigured_active" = yes ]; then
    olivares_prior=active-before-deconfigure
  fi
  write_pending package_recovery_required product-start-owned-by-package-phase \
    "$olivares_prior" "$package_phase status-v1"
  cat <<EOF
Olivares AI: this appliance's package phase owner ($package_phase finish-v1)
  starts or restarts olivares after the package transaction; this package did not.
  Pending: package_recovery_required until that phase completes. Status: $package_phase status-v1
  If it does not complete, repair from the appliance console (tty1).
EOF
else
  if [ "$OLIVARES_PKG_INIT" = openrc ] && [ -f "$upgrade_stamp" ]; then
    if command -v rc-service >/dev/null 2>&1; then
      rc-service olivares start
    fi
    rm -f "$upgrade_stamp"
  fi
  if [ "$deconfigured_active" = yes ] && [ "$OLIVARES_PKG_INIT" = systemd ]; then
    case "$unit_enabled" in
      masked|masked-runtime) ;;
      *)
        if [ "$action" = abort-deconfigure ]; then
          # The deconfigure is undone: the service it stopped runs again.
          if command -v systemctl >/dev/null 2>&1; then
            systemctl start olivares
            unit_active=active
          fi
        elif [ "$action" = install ] || [ "$action" = upgrade ]; then
          echo "Olivares AI: olivares was stopped when it was deconfigured; start it with: systemctl start olivares"
        fi ;;
    esac
  fi
  if [ "$format" = deb ] && [ "$action" = upgrade ]; then
    if [ -n "$record_old" ] && [ "$record_old" = "$configured_old" ]; then
      : # This transaction's prerm honored the upgrade contract: nothing changed.
    elif command -v dpkg >/dev/null 2>&1 &&
      dpkg --compare-versions "$configured_old" lt "$first_contract_version"; then
      # The replaced version predates the contract: its prerm ran "uninstall
      # --preserve" (stop and disable), and its postrm removed the only trace, so
      # the prior state is unknown and nothing is restored. An enabled unit shows
      # the disable did not happen; a masked one is never unmasked.
      case "$unit_enabled" in
        enabled|masked|masked-runtime|unknown) ;;
        *)
          olivares_instruction='if olivares was enabled before this upgrade: systemctl enable --now olivares; if it was running but not enabled: systemctl start olivares'
          write_pending legacy-prerm-upgrade replaced-prerm-stopped-and-disabled unknown "$olivares_instruction"
          cat <<EOF
Olivares AI: the previously configured version $configured_old predates the upgrade contract
  (first contract version ${first_contract_version%\~}). Its prerm stops and disables olivares and
  records nothing, so the prior state is unknown and nothing was restored.
  Pending: legacy-prerm-upgrade ($pending)
  $olivares_instruction
EOF
          ;;
      esac
    fi
  elif [ "$format" = rpm ] && [ "$action" = upgrade ] && [ "$replaced_honors_contract" = no ]; then
    # RPM runs this %post before the replaced package's %preun. When the installed
    # package's scripts predate the contract (no fresh contract marker), that %preun
    # stops and disables olivares after this point. Record the state it would lose;
    # a current %preun withdraws the record.
    olivares_instruction=
    case "$unit_enabled/$unit_active" in
      enabled/active) olivares_instruction='systemctl enable --now olivares' ;;
      enabled/*) olivares_instruction='systemctl enable olivares' ;;
      # disable keeps these enablement states (or, for linked, cannot be undone by the
      # package unit); only the stop needs undoing.
      disabled/active|static/active|indirect/active|enabled-runtime/active|linked/active|linked-runtime/active)
        olivares_instruction='systemctl start olivares' ;;
      # disabled, static, indirect, enabled-runtime and linked while inactive, and
      # every masked state: nothing to restore.
    esac
    if [ -n "$olivares_instruction" ]; then
      write_pending replaced-package-may-disable replaced-preun-may-stop-and-disable \
        "$unit_enabled/$unit_active" "$olivares_instruction"
      cat <<EOF
Olivares AI: olivares was $unit_enabled/$unit_active before this upgrade, and the installed
  package's %preun predates the upgrade contract: it stops and disables it after this step.
  Pending: replaced-package-may-disable ($pending, withdrawn by a current %preun)
  If systemctl then reports it stopped or disabled: $olivares_instruction
EOF
    fi
  fi
  if [ "$action" = upgrade ] && [ "$unit_active" = active ]; then
    echo "Olivares AI: the running service keeps the previous version until restarted: sudo systemctl restart olivares"
  fi
fi
receipt "$format" "$action" ok

if [ -x "$package_phase" ]; then
  : # The appliance notice above is the whole message.
elif [ "$OLIVARES_PKG_INIT" = openrc ]; then
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
