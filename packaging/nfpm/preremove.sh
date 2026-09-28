#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# nFPM ships this file as the Debian prerm, the RPM %preun and the APK .pre-deinstall
# (APK upgrades use apk-preupgrade.sh instead). The format comes from the package-init
# stamp this package shipped and a closed table of the arguments each package manager
# passes, never from $1 alone and never from host systemctl presence:
#   systemd  remove ...                      deb  removal
#   systemd  deconfigure ...                 deb  stop only (enablement kept)
#   systemd  upgrade|failed-upgrade ...      deb  upgrade
#   systemd  N (digits; 0 = erase)           rpm  removal (0) or upgrade (1 or more)
#   openrc   VERSION                         apk  removal
# Any other combination is refused by name before any effect. An upgrade never stops,
# disables or uninstalls: administrator enablement, mask and running state stay as they
# are. A Debian upgrade records the version it replaces and the version it installs, so
# the new postinst can tell this prerm from one that predates the contract; a query that
# fails records an empty version, which never matches. Deconfigure stops a running
# service and records that it ran, so abort-deconfigure alone can start it again;
# enablement never changes, and a removal consumes that record. Removal preserves
# data/config/keys and their identity; apk/dpkg/rpm then remove package-owned paths.
set -eu

olv_script=preremove
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

# Empty OLIVARES_PKG_INIT means no evidence: only an upgrade call can proceed then.
pkg_init() {
  OLIVARES_PKG_INIT=
  if [ -r /usr/lib/olivares/package-init ]; then
    read -r OLIVARES_PKG_INIT < /usr/lib/olivares/package-init || true
    case "$OLIVARES_PKG_INIT" in
      systemd|openrc) return 0 ;;
      *) refuse package-init-unknown "package-init is '$OLIVARES_PKG_INIT'" ;;
    esac
  fi
  if [ -x /etc/init.d/olivares ] && [ ! -e /usr/lib/systemd/system/olivares.service ]; then
    OLIVARES_PKG_INIT=openrc
  elif [ -e /usr/lib/systemd/system/olivares.service ] && [ ! -e /etc/init.d/olivares ]; then
    OLIVARES_PKG_INIT=systemd
  fi
}

format=
action=
pkg_init
if [ "$OLIVARES_PKG_INIT" = openrc ]; then
  if [ $# -eq 1 ] && is_apk_version "$1"; then
    format=apk action=remove
  else
    refuse argument-class-inconsistent "package-init openrc with arguments '$*' (apk passes one version)"
  fi
else
  [ $# -gt 0 ] || refuse argument-missing "no maintainer-script argument"
  case "$1" in
    remove) format=deb action=remove ;;
    deconfigure) format=deb action=deconfigure ;;
    upgrade|failed-upgrade) format=deb action=$1 ;;
    *)
      if [ $# -eq 1 ] && is_count "$1"; then
        format=rpm
        if [ "$1" -eq 0 ]; then action=remove; else action=upgrade; fi
      elif is_apk_version "$1"; then
        refuse argument-class-inconsistent "package-init ${OLIVARES_PKG_INIT:-absent} with arguments '$*'"
      else
        refuse argument-class-unknown "'$1' is neither a Debian prerm action nor an RPM count"
      fi
      ;;
  esac
  # Behavior change from ccf7ea20: with no package-init stamp and no single packaged
  # unit, a removal used to run "systemctl disable --now" and exit 0; it now refuses.
  # Only a deleted stamp reaches this, and the refusal names it.
  if [ -z "$OLIVARES_PKG_INIT" ] && { [ "$action" = remove ] || [ "$action" = deconfigure ]; }; then
    refuse package-init-unknown "no package-init stamp and no single packaged unit"
  fi
fi

if [ "$action" = deconfigure ]; then
  # dpkg deconfigures this package when a package being installed breaks it, then
  # configures it again or unwinds with abort-deconfigure. Stop a running service so
  # that package can proceed and record that it ran; enablement and mask stay.
  unit_active=unknown
  if command -v systemctl >/dev/null 2>&1; then
    unit_active=$(systemctl is-active olivares 2>/dev/null || true)
    case "$unit_active" in ''|*[!a-z-]*) unit_active=unknown ;; esac
  fi
  olv_prior=$(olv_active "$unit_active")
  olv_stopped=no
  olv_record deconfigured /run/olivares.pkg-deconfigured deb "active=$unit_active" || true
  if [ "$unit_active" = active ]; then
    systemctl stop olivares
    olv_stopped=yes
  fi
  receipt "$format" "$action" ok
  exit 0
fi

if [ "$action" != remove ]; then
  # Upgrade: record what the administrator has, change nothing. The Debian postinst
  # matches old= against the version it is told it replaces. dpkg passes this prerm
  # only the new version, so the installed one comes from a read-only dpkg-query.
  # On RPM the new %post already ran and recorded the prior state as pending; this
  # %preun honors the contract, so that pending condition is withdrawn.
  if [ "$format" = deb ] && [ "$action" = upgrade ] && [ "$OLIVARES_PKG_INIT" = systemd ]; then
    unit_enabled=unknown
    unit_active=unknown
    if command -v systemctl >/dev/null 2>&1; then
      unit_enabled=$(systemctl is-enabled olivares 2>/dev/null || true)
      unit_active=$(systemctl is-active olivares 2>/dev/null || true)
      case "$unit_enabled" in ''|*[!a-z-]*) unit_enabled=unknown ;; esac
      case "$unit_active" in ''|*[!a-z-]*) unit_active=unknown ;; esac
    fi
    olivares_old=$(dpkg-query --show --showformat='${Version}' olivares 2>/dev/null || true)
    # An error, empty or ambiguous answer is recorded as old=unknown, never guessed.
    olv_old=$(olv_ver "$olivares_old")
    olv_new=$(olv_ver "${2-}")
    olv_record upgrade-state /run/olivares.pkg-upgrade-state deb \
      "enabled=$unit_enabled" "active=$unit_active" || true
  fi
  if [ "$format" = rpm ] && olv_record_ok pending /run/olivares.pkg-pending &&
    grep -qx 'condition=replaced-package-may-disable' /run/olivares.pkg-pending; then
    rm -f /run/olivares.pkg-pending
  fi
  receipt "$format" "$action" ok
  exit 0
fi

# A removal ends any deconfigure: its record must not start a later reinstall.
rm -f /run/olivares.pkg-deconfigured
printf '%s\n' "$OLIVARES_PKG_INIT" > /run/olivares.pkg-removed-init
chmod 0600 /run/olivares.pkg-removed-init

olv_stopped=no
olv_prior=unknown
if [ "$OLIVARES_PKG_INIT" = openrc ]; then
  if command -v rc-service >/dev/null 2>&1 && [ -x /etc/init.d/olivares ]; then
    if rc-service olivares status >/dev/null 2>&1; then
      olv_prior=active
      rc-service olivares stop
      olv_stopped=yes
    else
      olv_prior=inactive
    fi
  fi
  if command -v rc-update >/dev/null 2>&1; then
    rc-update del olivares default >/dev/null 2>&1 || true
  fi
  # The install record sits in the service-owned directory. When the engine refuses
  # it (a link, not a regular file, not root's), the removal still goes on: the
  # service is already stopped and out of the runlevel, and the plan is only a report.
  if [ -x /usr/bin/olivares ] && [ -r /var/lib/olivares/install-manifest.json ]; then
    if ! /usr/bin/olivares uninstall --plan --data-dir /var/lib/olivares >/dev/null; then
      echo "olivares package: install record refused by the uninstall engine; the service was stopped without it" >&2
    fi
  fi
  receipt "$format" "$action" ok
  exit 0
fi

if command -v systemctl >/dev/null 2>&1; then
  olv_prior=$(olv_active "$(systemctl is-active olivares 2>/dev/null || true)")
fi
# The install record sits in the service-owned directory, so the service account can
# put something else at its name. The engine refuses anything but root's own regular
# file before any change; the removal then takes the same safe stop as a package
# without the record, and never fails on it.
olv_engine=skipped
if [ -x /usr/bin/olivares ] && [ -r /var/lib/olivares/install-manifest.json ]; then
  if /usr/bin/olivares uninstall --preserve --data-dir /var/lib/olivares; then
    olv_engine=ran
  else
    olv_engine=refused
    echo "olivares package: install record refused by the uninstall engine; stopping the service without it" >&2
  fi
fi
if [ "$olv_engine" = ran ]; then
  olv_stopped=yes
elif command -v systemctl >/dev/null 2>&1; then
  # No usable v2 ownership record (a legacy package, or one the engine refused):
  # the safe stop, with no path list invented and nothing deleted.
  systemctl disable --now olivares >/dev/null 2>&1 || true
  olv_stopped=yes
fi
receipt "$format" "$action" ok
