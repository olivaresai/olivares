#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# APK pre-upgrade: stop a running OpenRC service so files can be replaced.
# Do not disable it and do not start it here. If it was active, postinstall
# (used as post-upgrade) starts it again. Inactive upgrades stay inactive.
# apk-tools passes NEW-VERSION OLD-VERSION; any other arguments are refused
# by name. The init on disk is the installed package's: a legacy APK without
# an OpenRC stamp keeps today's no-op.
set -eu

upgrade_stamp=/run/olivares.pkg-upgrade-was-active

olv_script=apk-preupgrade
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
  receipt apk upgrade "refused:$1"
  exit 1
}
is_apk_version() {
  case "${1-}" in [0-9]*) ;; *) return 1 ;; esac
  case "$1" in *[!0-9A-Za-z._+~-]*) return 1 ;; esac
}

if [ $# -ne 2 ] || ! is_apk_version "$1" || ! is_apk_version "$2"; then
  refuse argument-class-inconsistent "the APK pre-upgrade takes NEW-VERSION OLD-VERSION, got '$*'"
fi
if [ -r /usr/lib/olivares/package-init ]; then
  stamp_init=
  read -r stamp_init < /usr/lib/olivares/package-init || true
  if [ "$stamp_init" = systemd ]; then
    refuse argument-class-inconsistent "package-init systemd names a deb or rpm install, not an APK"
  fi
fi

# APK passes NEW-VERSION OLD-VERSION to the pre-upgrade (apk-tools).
olv_new=$(olv_ver "$1")
olv_old=$(olv_ver "$2")
olv_stopped=no
olv_prior=unknown
rm -f "$upgrade_stamp"

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
  return 1
}

if ! pkg_init || [ "$OLIVARES_PKG_INIT" != openrc ] ||
  ! command -v rc-service >/dev/null 2>&1 || [ ! -x /etc/init.d/olivares ]; then
  receipt apk upgrade ok
  exit 0
fi
olv_prior=inactive
if rc-service olivares status >/dev/null 2>&1; then
  olv_prior=active
  rc-service olivares stop
  olv_stopped=yes
  : >"$upgrade_stamp"
  chmod 0600 "$upgrade_stamp"
fi
receipt apk upgrade ok
