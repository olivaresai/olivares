#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# maintainer-scripts-test.sh — olivares-appliance-base's maintainer scripts under both package
# managers that install it: dpkg, which passes action words (postinst "configure" and the
# version it last configured, prerm "remove" or "upgrade", postrm "remove", "purge" or
# "upgrade"), and rpm, which passes the number of this package's instances left when the step
# completes (%post 1 on an installation and 2 on an upgrade, %preun and %postun 0 on an erase
# and 1 on an upgrade, where rpm runs the new %post before the old %preun and %postun).
#
# Each scenario runs the real scripts, in the order the package manager runs them, against a
# sandbox: every absolute path a script names is rewritten into it (a path the harness does not
# know is refused, so a script cannot reach the host), and systemctl and install are stand-ins
# on PATH that record each call and keep the first-boot unit's enablement in a file. The same
# expectations hold for both conventions:
#   - a first installation enables the first-boot unit, creates the Appliance Console's
#     directory and starts nothing;
#   - an upgrade, or a reinstallation of the installed version, keeps the operator's choice;
#   - a removal disables the unit and keeps the first-boot record, and a reinstallation after
#     it restores the enablement the removal found; after Debian's purge it is a first
#     installation again (rpm has no purge);
#   - no script names any unit but the first-boot unit, starts anything or exits non-zero.
# Each scenario runs once under dash and once under bash, the two /bin/sh the targets use.
#
# usage: maintainer-scripts-test.sh [SCRIPT_DIR]   (default: this file's directory)
#   exit 0 every scenario held, 1 one did not, 2 the harness could not run
set -uo pipefail

scripts=$(cd "${1:-$(dirname "$0")}" && pwd) || exit 2
work=$(mktemp -d "${TMPDIR:-/tmp}/maintainer-scripts.XXXXXX") || exit 2
trap 'rm -rf "$work"' EXIT
failures=0
unit=olivares-appliance-firstboot.service
record=/var/lib/olivares-appliance/state.json

for s in postinstall preremove postremove; do
  [ -r "$scripts/$s.sh" ] || { echo "UNABLE: $scripts/$s.sh is missing"; exit 2; }
done

# The stand-ins. systemctl keeps the unit's enablement in $SANDBOX/enabled and logs every call.
mkdir -p "$work/bin"
cat > "$work/bin/systemctl" <<'EOF'
#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$SANDBOX/calls"
[ ! -e "$SANDBOX/fail-$1" ] || exit 23
case "$*" in
  "daemon-reload") ;;
  "enable olivares-appliance-firstboot.service") : > "$SANDBOX/enabled" ;;
  "disable olivares-appliance-firstboot.service") rm -f "$SANDBOX/enabled" ;;
  "--quiet is-enabled olivares-appliance-firstboot.service") [ -e "$SANDBOX/enabled" ] ;;
  *) exit 0 ;;
esac
EOF
cat > "$work/bin/install" <<'EOF'
#!/bin/sh
printf 'install %s\n' "$*" >> "$SANDBOX/calls"
[ ! -e "$SANDBOX/fail-install" ] || exit 23
eval "last=\${$#}"
mkdir -p "$last"
EOF
cat > "$work/bin/appliance-firstboot" <<'EOF'
#!/bin/sh
printf 'appliance-firstboot %s\n' "$*" >> "$SANDBOX/calls"
[ "$*" = hand-over-host-settings ] || exit 24
[ ! -e "$SANDBOX/fail-handoff" ] || exit 1
EOF
chmod 0755 "$work/bin/systemctl" "$work/bin/install" "$work/bin/appliance-firstboot"

# sandbox NAME — a fresh root in which systemd runs and the state directory exists; prints it.
sandbox() {
  local root="$work/$1"
  rm -rf "$root"
  mkdir -p "$root/var/lib/olivares-appliance" "$root/run/systemd/system" "$root/etc" "$root/scripts"
  : > "$root/calls"
  local s text
  for s in postinstall preremove postremove; do
    text=$(sed -e "s#/var/lib/olivares-appliance#$root/var/lib/olivares-appliance#g" \
      -e "s#/etc/olivares-portal#$root/etc/olivares-portal#g" \
      -e "s#/run/systemd/system#$root/run/systemd/system#g" "$scripts/$s.sh")
    # Past the shebang, the only absolute paths left must be the sandbox's and /dev/null.
    if sed '1d' <<< "$text" | grep -v '^[[:space:]]*#' | sed -e "s#$root/[^[:space:]\"']*##g" -e 's#/dev/null##g' |
      grep -q '/[a-z]'; then
      echo "UNABLE: $s.sh names an absolute path this harness does not sandbox"
      exit 2
    fi
    printf '%s\n' "$text" > "$root/scripts/$s.sh"
  done
  printf '%s\n' "$root"
}

# run ROOT SHELL SCRIPT ARGS... — one maintainer script, as the package manager runs it.
run() {
  local root=$1 shell=$2 script=$3
  shift 3
  if ! SANDBOX=$root PATH="$work/bin:/usr/bin:/bin" "$shell" "$root/scripts/$script.sh" "$@" >> "$root/output" 2>> "$root/stderr"; then
    echo "FAIL $label: $script.sh $* exited non-zero: $(tail -3 "$root/stderr")"
    failures=$((failures + 1))
  fi
}

# expect NAME CONDITION... — a named assertion of the current scenario.
expect() {
  local name=$1
  shift
  if "$@"; then
    echo "PASS $label: $name"
  else
    echo "FAIL $label: $name"
    failures=$((failures + 1))
  fi
}
enabled() { [ -e "$1/enabled" ]; }
disabled() { [ ! -e "$1/enabled" ]; }
called() { grep -qxF "systemctl $2" "$1/calls"; }
not_called() { ! grep -qxF "systemctl $2" "$1/calls"; }
console_dir() { [ -d "$1/etc/olivares-portal" ]; }
kept_record() { [ -s "$1$record" ]; }
# Every call names the first-boot unit or none, and none starts anything.
only_first_boot() {
  ! grep -v -x -e 'systemctl daemon-reload' -e "systemctl enable $unit" -e "systemctl disable $unit" \
    -e "systemctl --quiet is-enabled $unit" -e 'install -d -m 0755 -o root -g root .*/etc/olivares-portal' -e 'appliance-firstboot hand-over-host-settings' "$1/calls" | grep -q .
}
fresh_calls() { : > "$1/calls"; }
reported() { grep -qF "olivares-appliance-base: rpm $2 failed" "$1/stderr"; }

# The package manager's steps, per convention. Debian's first configure passes an empty version.
install_fresh() { case $pm in deb) run "$1" "$sh" postinstall configure '' ;; rpm) run "$1" "$sh" postinstall 1 ;; esac; }
upgrade() {
  case $pm in
    deb) run "$1" "$sh" preremove upgrade 2.0; run "$1" "$sh" postremove upgrade 2.0; run "$1" "$sh" postinstall configure 1.0 ;;
    rpm) run "$1" "$sh" postinstall 2; run "$1" "$sh" preremove 1; run "$1" "$sh" postremove 1 ;;
  esac
}
remove() {
  case $pm in
    deb) run "$1" "$sh" preremove remove; run "$1" "$sh" postremove remove ;;
    rpm) run "$1" "$sh" preremove 0; run "$1" "$sh" postremove 0 ;;
  esac
}
# A reinstallation after removal: dpkg passes the version the kept configuration belongs to.
reinstall_after_removal() { case $pm in deb) run "$1" "$sh" postinstall configure 1.0 ;; rpm) run "$1" "$sh" postinstall 1 ;; esac; }

for sh in dash bash; do
  command -v "$sh" >/dev/null 2>&1 || { echo "UNABLE: $sh is not available"; exit 2; }
  for pm in deb rpm; do
    label="$pm/$sh install"
    r=$(sandbox "$pm-$sh-install") || exit 2
    install_fresh "$r"
    expect "the first-boot unit is enabled" enabled "$r"
    expect "the Appliance Console's directory exists" console_dir "$r"
    expect "every call names the first-boot unit or none and starts nothing" only_first_boot "$r"

    label="$pm/$sh verified record handoff"
    printf '{}\n' > "$r$record"
    fresh_calls "$r"
    upgrade "$r"
    expect "postinstall delegates only the protected handoff" grep -qxF 'appliance-firstboot hand-over-host-settings' "$r/calls"
    : > "$r/fail-handoff"
    upgrade "$r"
    expect "handoff refusal is reported without starting anything" only_first_boot "$r"
    rm -f "$r/fail-handoff" "$r$record"

    label="$pm/$sh upgrade, enabled"
    : > "$r/enabled"
    fresh_calls "$r"
    upgrade "$r"
    expect "the unit stays enabled" enabled "$r"
    expect "the upgrade neither disables nor enables" not_called "$r" "disable $unit"
    expect "no enable on upgrade" not_called "$r" "enable $unit"

    label="$pm/$sh upgrade, disabled by the operator"
    r=$(sandbox "$pm-$sh-upgrade-disabled") || exit 2
    install_fresh "$r"
    rm -f "$r/enabled"
    fresh_calls "$r"
    upgrade "$r"
    expect "the operator's choice is kept" disabled "$r"

    label="$pm/$sh removal and reinstallation, enabled"
    r=$(sandbox "$pm-$sh-remove-enabled") || exit 2
    install_fresh "$r"
    : > "$r/enabled"
    printf '{}\n' > "$r$record"
    remove "$r"
    expect "the removal asks systemd to disable the unit" called "$r" "disable $unit"
    expect "the removal disables the unit" disabled "$r"
    expect "the removal keeps the first-boot record" kept_record "$r"
    reinstall_after_removal "$r"
    expect "the reinstallation restores the enablement the removal found" enabled "$r"
    expect "every call names the first-boot unit or none and starts nothing" only_first_boot "$r"

    label="$pm/$sh removal and reinstallation, disabled by the operator"
    r=$(sandbox "$pm-$sh-remove-disabled") || exit 2
    install_fresh "$r"
    rm -f "$r/enabled"
    remove "$r"
    fresh_calls "$r"
    reinstall_after_removal "$r"
    expect "the reinstallation keeps the unit disabled, as the removal found it" disabled "$r"
    expect "no enable" not_called "$r" "enable $unit"

    label="$pm/$sh install into an image root, systemd not running"
    r=$(sandbox "$pm-$sh-image") || exit 2
    rmdir "$r/run/systemd/system"
    install_fresh "$r"
    expect "the first-boot unit is enabled offline" enabled "$r"
    expect "no daemon-reload without a running systemd" not_called "$r" "daemon-reload"

    label="$pm/$sh reinstallation of the installed version"
    r=$(sandbox "$pm-$sh-reinstall") || exit 2
    install_fresh "$r"
    rm -f "$r/enabled"
    upgrade "$r"
    expect "a reinstallation is an upgrade to the same version: the choice is kept" disabled "$r"

    if [ "$pm" = rpm ]; then
      for failure in install daemon-reload enable; do
        label="$pm/$sh postinstall $failure failure"
        r=$(sandbox "$pm-$sh-fail-$failure") || exit 2
        : > "$r/fail-$failure"
        install_fresh "$r"
        expect "the failure is named on stderr" reported "$r" "%post $failure"
        if [ "$failure" = enable ]; then
          expect "a failed enable leaves the unit disabled" disabled "$r"
        else
          expect "the remaining installation steps still enable the unit" enabled "$r"
        fi
        rm -f "$r/fail-$failure"
        remove "$r"
        reinstall_after_removal "$r"
        if [ "$failure" = enable ]; then
          expect "reinstallation keeps the failed enable disabled" disabled "$r"
        else
          expect "reinstallation restores the enablement removal found" enabled "$r"
        fi
        expect "failure handling starts no unit" only_first_boot "$r"
      done

      label="$pm/$sh installation marker write failure"
      r=$(sandbox "$pm-$sh-fail-installed-marker") || exit 2
      mkdir "$r/var/lib/olivares-appliance/.package-installed"
      install_fresh "$r"
      expect "the marker failure is named on stderr" reported "$r" "%post installation marker write"
      expect "an unrecorded installation stays disabled" disabled "$r"
      rmdir "$r/var/lib/olivares-appliance/.package-installed"
      remove "$r"
      reinstall_after_removal "$r"
      expect "reinstallation preserves the disabled removal" disabled "$r"

      for choice in enabled disabled; do
        label="$pm/$sh removal marker write failure, $choice"
        r=$(sandbox "$pm-$sh-fail-removed-marker-$choice") || exit 2
        install_fresh "$r"
        if [ "$choice" = disabled ]; then rm -f "$r/enabled"; fi
        mkdir "$r/var/lib/olivares-appliance/.package-removed"
        remove "$r"
        expect "the removal marker failure is named on stderr" reported "$r" "%preun removal marker write"
        expect "removal continues to disable the unit" disabled "$r"
        # Remove the write blocker: no successful %preun removal marker remains.
        rmdir "$r/var/lib/olivares-appliance/.package-removed"
        fresh_calls "$r"
        reinstall_after_removal "$r"
        if [ "$choice" = enabled ]; then
          expect "the successful enablement marker restores the enabled choice" enabled "$r"
        else
          expect "without a removal marker reinstallation still keeps disabled" disabled "$r"
          expect "no enable after the disabled removal" not_called "$r" "enable $unit"
        fi
      done

      label="$pm/$sh enablement marker write failure"
      r=$(sandbox "$pm-$sh-fail-enabled-marker") || exit 2
      install_fresh "$r"
      mkdir "$r/var/lib/olivares-appliance/.firstboot-enabled-at-removal"
      remove "$r"
      expect "the enablement marker failure is named on stderr" reported "$r" "%preun enablement marker write"
      expect "erase continues to disable the unit" disabled "$r"
      rmdir "$r/var/lib/olivares-appliance/.firstboot-enabled-at-removal"
      reinstall_after_removal "$r"
      expect "without an enablement marker reinstallation leaves disabled" disabled "$r"

      label="$pm/$sh postremove daemon-reload failure"
      r=$(sandbox "$pm-$sh-fail-postremove-reload") || exit 2
      install_fresh "$r"
      run "$r" "$sh" preremove 0
      : > "$r/fail-daemon-reload"
      run "$r" "$sh" postremove 0
      expect "postremove names the reload failure on stderr" reported "$r" "%postun daemon-reload"
      rm -f "$r/fail-daemon-reload"
      reinstall_after_removal "$r"
      expect "reinstallation restores the enabled choice" enabled "$r"
    fi

    if [ "$pm" = deb ]; then
      label="$pm/$sh purge after removal"
      r=$(sandbox "$pm-$sh-purge") || exit 2
      install_fresh "$r"
      rm -f "$r/enabled"
      remove "$r"
      run "$r" "$sh" postremove purge
      run "$r" "$sh" postinstall configure ''
      expect "after a purge the next installation is a first installation" enabled "$r"
    fi
  done
done

echo "failed: $failures"
[ "$failures" -eq 0 ]
