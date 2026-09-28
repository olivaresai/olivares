#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Script-level upgrade, removal and purge matrix for the nFPM maintainer scripts
# (PKG-L1). It EXECUTES the shipped packaging/nfpm/*.sh, and the ccf7ea20 copies in
# scripts/fixtures/package-upgrade/ccf7ea20/ for the old side, in the order each
# package manager calls them:
#   Debian Policy 6.6   old-prerm upgrade NEW; old-postrm upgrade NEW; new-postinst configure OLD
#   rpm-scriptlets(7)   new %post 2; old %preun 1; old %postun 1  (erase: %preun 0; %postun 0)
#   apk-tools           .pre-upgrade NEW OLD; .post-upgrade NEW OLD  (deinstall: one version)
# Each run happens in a throwaway root: the script's absolute product paths get that
# root as a prefix, by one fixed table, and nothing else in the script changes (checked).
# systemctl, rc-service, rc-update, the olivares uninstall engine and the account tools
# are recording stubs over a small state model. This is not a package manager and not an
# init system: the built-archive matrix (scripts/test-package-upgrade-matrix.sh) runs the
# real control archives on the hosted vehicle.
#
# r2 adds the read-r1 cells: the Debian recovery bound to the transaction (fresh install,
# remove then reinstall, upgrade then unwind, downgrade then re-upgrade), every refused
# combination, deconfigure and its unwind, postremove without evidence, the appliance
# banner, and the systemd enablement states beyond enabled, disabled and masked.
# dpkg is used only for "dpkg --compare-versions", a pure comparison; any other dpkg
# call is recorded as a package operation and fails its cell.
#
# Exit 0: every cell holds. Exit 1: a measured cell failed. Exit 2: could not look.
# The old→new rows measure the ccf7ea20 prerm's stop and disable and report it as a
# DEFECT with the recovery the new postinstall names; they are never counted as a pass
# of the upgrade contract.
set -euo pipefail

LC_ALL=C
export LC_ALL
me=test-package-upgrade-scripts

could_not_look() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
new_dir="$root/packaging/nfpm"
old_dir="$root/scripts/fixtures/package-upgrade/ccf7ea20"
for tool in sh python3 sha256sum mktemp grep sed find; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
[[ -x /usr/bin/dpkg ]] || could_not_look "missing /usr/bin/dpkg (needed only for dpkg --compare-versions)"
scratch_parent="${TMPDIR:-}"
case "$scratch_parent" in /*) ;; *) could_not_look 'TMPDIR must be absolute' ;; esac
[[ -d "$scratch_parent" ]] || could_not_look "TMPDIR is absent: $scratch_parent"

# The old side is the exact ccf7ea20 bytes. An edited copy is not a measurement.
while read -r sum name; do
	[[ -f "$old_dir/$name" ]] || could_not_look "old fixture missing: $old_dir/$name"
	[[ "$(sha256sum "$old_dir/$name" | cut -d' ' -f1)" == "$sum" ]] ||
		could_not_look "old fixture $name is not the ccf7ea20 script (digest differs)"
done <<'SUMS'
489b5be0f670199511eaf5b9263e0dc0f92949d2999c8a4f08a4cb560a70d31a preremove.sh
4b83ee7c5da56bec10fd0bc7fc4bffab3788a7672e64fc3d0fbea7f93d55e75a postinstall.sh
728bf915d0f42f3e3ac7bb39d413a58df7b8b4aaff74eda89012dcd1fe888a5f postremove.sh
973dcb4103367a93c720fa845668c4bdc35b3735b4495834cb9bff1dc7608019 apk-preupgrade.sh
SUMS
for name in preremove.sh postinstall.sh postremove.sh apk-preupgrade.sh; do
	[[ -f "$new_dir/$name" ]] || could_not_look "missing $new_dir/$name"
done

scratch="$(mktemp -d "$scratch_parent/pkg-upgrade-scripts.XXXXXX")"
cleanup() { case "$scratch" in "$scratch_parent"/pkg-upgrade-scripts.*) rm -rf -- "$scratch" ;; esac; }
trap cleanup EXIT INT TERM

failures=0
defects=0
sys_path=
run_timeout=
run_env=
run_sh=
checker="$root/scripts/fixtures/package-upgrade/records-v1/check-records-v1.py"
[[ -f "$checker" ]] || could_not_look "missing $checker"
n=0
ok() { n=$((n + 1)); printf 'ok %d - %s\n' "$n" "$*"; }
not_ok() { n=$((n + 1)); failures=$((failures + 1)); printf 'not ok %d - %s\n' "$n" "$*"; }
defect() { n=$((n + 1)); defects=$((defects + 1)); printf 'DEFECT %d - %s\n' "$n" "$*"; }

# --- sandbox ------------------------------------------------------------------------
# The fixed prefix table. Only these product paths move under the case root.
rewrite() {
	python3 - "$1" "$2" "$3" <<'PY'
import re, sys
src, dst, box = sys.argv[1], sys.argv[2], sys.argv[3]
text = open(src, encoding="utf-8").read()
prefixes = [
    "/usr/lib/olivares", "/usr/lib/systemd/system", "/usr/libexec/olivares",
    "/usr/bin/olivares", "/usr/share/olivares", "/etc/init.d/olivares",
    "/etc/olivares", "/var/lib/olivares", "/var/log/olivares", "/run/olivares",
    "/etc/systemd/system", "/boot", "/proc",
]
pat = re.compile(r"(?<![\w./-])(" + "|".join(re.escape(p) for p in prefixes) + r")")
out = pat.sub(lambda m: box + m.group(1), text)
if out.replace(box, "") != text:
    raise SystemExit("rewrite changed more than the path prefixes")
open(dst, "w", encoding="utf-8").write(out)
PY
}

# new_box NAME: a fresh case root with stubs. Service state lives in $box/state.
new_box() {
	box="$scratch/$1"
	mkdir -p "$box/bin" "$box/state" "$box/run" "$box/usr/lib/olivares" "$box/usr/bin" \
		"$box/var/lib/olivares" "$box/etc/olivares" "$box/var/log" "$box/usr/share/olivares"
	: >"$box/calls"
	ln -s /proc "$box/proc"   # r4: /proc is rewritten; a cell may replace the link
	printf 'disabled\n' >"$box/state/enabled"
	printf 'inactive\n' >"$box/state/active"
	printf 'no\n' >"$box/state/rc-default"
	cat >"$box/bin/systemctl" <<'STUB'
#!/bin/sh
# Model: enabled|enabled-runtime|linked|static|indirect|disabled|masked x active|inactive.
# Assumptions owed to a real systemd guest (HM-07), not measured here: "disable" turns
# enabled and linked into disabled, leaves a mask, static, indirect and enabled-runtime as
# they are, and "--now" still stops.
S="$OLIVARES_TEST_BOX/state"
printf 'systemctl %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
now=no
verb=
for a in "$@"; do
  case "$a" in --now) now=yes ;; --*) ;; *) [ -z "$verb" ] && verb=$a ;; esac
done
en=$(cat "$S/enabled"); ac=$(cat "$S/active")
case "$verb" in
  daemon-reload) exit 0 ;;
  is-enabled) echo "$en"; case "$en" in enabled|enabled-runtime|linked|static|indirect) exit 0 ;; esac; exit 1 ;;
  is-active) echo "$ac"; [ "$ac" = active ] || exit 3 ;;
  disable) case "$en" in enabled|linked) echo disabled >"$S/enabled" ;; esac; [ $now = yes ] && echo inactive >"$S/active"; exit 0 ;;
  enable) [ "$en" = masked ] && { echo "Unit file is masked." >&2; exit 1; }; echo enabled >"$S/enabled"; [ $now = yes ] && echo active >"$S/active"; exit 0 ;;
  start|restart|reload-or-restart) [ "$en" = masked ] && { echo "Unit is masked." >&2; exit 1; }; echo active >"$S/active" ;;
  try-restart|condrestart) [ "$en" = masked ] && exit 1; exit 0 ;;
  stop) echo inactive >"$S/active" ;;
  mask) echo masked >"$S/enabled"; [ $now = yes ] && echo inactive >"$S/active"; exit 0 ;;
  unmask) [ "$en" = masked ] && echo disabled >"$S/enabled"; exit 0 ;;
  *) echo "systemctl stub: unmodelled verb $verb" >&2; exit 1 ;;
esac
STUB
	cat >"$box/bin/rc-service" <<'STUB'
#!/bin/sh
S="$OLIVARES_TEST_BOX/state"
printf 'rc-service %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
[ "$1" = olivares ] || exit 1
case "$2" in
  status) [ "$(cat "$S/active")" = active ] ;;
  start) echo active >"$S/active" ;;
  stop) echo inactive >"$S/active" ;;
  *) exit 1 ;;
esac
STUB
	cat >"$box/bin/rc-update" <<'STUB'
#!/bin/sh
S="$OLIVARES_TEST_BOX/state"
printf 'rc-update %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
case "$1" in
  add) echo yes >"$S/rc-default" ;;
  del) echo no >"$S/rc-default" ;;
esac
exit 0
STUB
	# The uninstall engine's service effect, per uninstall.go stopService: systemd
	# "disable --now"; OpenRC "stop" plus "rc-update del" when in the runlevel.
	cat >"$box/usr/bin/olivares" <<'STUB'
#!/bin/sh
printf 'olivares %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
[ "$1" = uninstall ] || exit 0
case "$2" in
  --preserve|--purge)
    init=$(sed -n 's/.*"init": "\([a-z]*\)".*/\1/p' "$OLIVARES_TEST_BOX/var/lib/olivares/install-manifest.json")
    case "$init" in
      systemd) systemctl disable --now olivares ;;
      openrc) rc-service olivares stop; [ "$(cat "$OLIVARES_TEST_BOX/state/rc-default")" = yes ] && rc-update del olivares default ;;
    esac ;;
esac
exit 0
STUB
	# dpkg-query reports the installed version this cell sets; dpkg only compares versions.
	cat >"$box/bin/dpkg-query" <<'STUB'
#!/bin/sh
printf 'dpkg-query %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
[ -f "$OLIVARES_TEST_BOX/state/dpkg-version" ] || exit 1
cat "$OLIVARES_TEST_BOX/state/dpkg-version"
STUB
	cat >"$box/bin/dpkg" <<'STUB'
#!/bin/sh
[ "${1-}" = --compare-versions ] && exec /usr/bin/dpkg "$@"
printf 'dpkg %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
exit 1
STUB
	for tool in getent groupadd useradd addgroup adduser update-initramfs dracut; do
		printf '#!/bin/sh\nprintf "%%s %%s\\n" %s "$*" >>"$OLIVARES_TEST_BOX/calls"\nexit 0\n' "$tool" >"$box/bin/$tool"
	done
	# The service account resolves to the unprivileged test user.
	printf '#!/bin/sh\ncase "$1" in -u) exec /usr/bin/id -u ;; -g) exec /usr/bin/id -g ;; esac\nexit 1\n' >"$box/bin/id"
	# Observing shadows (r3b): rm, mv, chown, chmod and stat log each call and then run the
	# real tool with the same arguments. They never stand in for the operation under test.
	# A cell may drop one-shot hooks in $box/hooks: window-<name> fires when root removes
	# <name> (the old create) or just before root renames onto it (the new protocol), and
	# after-<name> fires after root's rename onto <name>, or when root chowns <name> by path.
	mkdir -p "$box/hooks"
	cat >"$box/bin/olivares-test-hook" <<'STUB'
#!/bin/sh
# olivares-test-hook KIND PATH: fire $box/hooks/KIND-<basename of PATH> once.
h="$OLIVARES_TEST_BOX/hooks/$1-${2##*/}"
[ -f "$h" ] || exit 0
/usr/bin/mv "$h" "$h.fired"
printf 'hook %s %s\n' "$1" "$2" >>"$OLIVARES_TEST_BOX/calls"
/bin/sh "$h.fired" "$2"
STUB
	cat >"$box/bin/rm" <<'STUB'
#!/bin/sh
printf 'rm %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
/usr/bin/rm "$@"; rc=$?
for a in "$@"; do case "$a" in -*) ;; *) olivares-test-hook window "$a" ;; esac; done
exit $rc
STUB
	cat >"$box/bin/mv" <<'STUB'
#!/bin/sh
printf 'mv %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
dst=; t=no
for a in "$@"; do case "$a" in -T) t=yes ;; -*) ;; *) dst=$a ;; esac; done
if [ "$t" = yes ] && [ -f "$OLIVARES_TEST_BOX/state/mv-without-T" ]; then
  echo "mv: unrecognized option: T" >&2; exit 1   # a platform whose mv lacks -T
fi
olivares-test-hook window "$dst"
/usr/bin/mv "$@"; rc=$?
[ "$rc" -eq 0 ] && olivares-test-hook after "$dst"
exit $rc
STUB
	cat >"$box/bin/chown" <<'STUB'
#!/bin/sh
for a in "$@"; do case "$a" in -*) ;; */*) olivares-test-hook after "$a" ;; esac; done
printf 'chown %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
if [ -f "$OLIVARES_TEST_BOX/state/chown-fails-staged" ]; then
  case "$*" in *install-manifest.json.new) echo "chown: changing group: Operation not permitted" >&2; exit 1 ;; esac
fi
# The sandbox has one user: root and olivares both stand for it, as id -u does. The real
# chown still runs on the same path.
u=$(/usr/bin/id -u); g=$(/usr/bin/id -g)
for a in "$@"; do
  shift
  case "$a" in root:olivares|root:*|olivares:olivares) a="$u:$g" ;; esac
  set -- "$@" "$a"
done
exec /usr/bin/chown "$@"
STUB
	cat >"$box/bin/chmod" <<'STUB'
#!/bin/sh
printf 'chmod %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
exec /usr/bin/chmod "$@"
STUB
	cat >"$box/bin/stat" <<'STUB'
#!/bin/sh
# With state/stage-other-fs, the staging directory reports another device (a mount the
# single-UID sandbox cannot make); everything else is the real stat.
if [ -f "$OLIVARES_TEST_BOX/state/stage-other-fs" ] && [ "$1 $2" = "-c %d" ]; then
  case "$3" in */var/lib/olivares-package) echo $(( $(/usr/bin/stat -c %d "$3") + 1 )); exit 0 ;; esac
fi
exec /usr/bin/stat "$@"
STUB
	/usr/bin/chmod 0755 "$box/bin/"* "$box/usr/bin/olivares"
}

# ship FORMAT: lay down the files that package format installs.
ship() {
	case "$1" in
	deb | rpm)
		mkdir -p "$box/usr/lib/systemd/system"
		: >"$box/usr/lib/systemd/system/olivares.service"
		printf 'systemd\n' >"$box/usr/lib/olivares/package-init"
		;;
	apk)
		mkdir -p "$box/etc/init.d"
		printf '#!/sbin/openrc-run\n' >"$box/etc/init.d/olivares"
		chmod 0755 "$box/etc/init.d/olivares"
		printf 'openrc\n' >"$box/usr/lib/olivares/package-init"
		;;
	esac
}
# unship: the package manager removed the package files (data and config stay).
unship() {
	rm -f "$box/usr/lib/systemd/system/olivares.service" "$box/usr/lib/olivares/package-init" \
		"$box/etc/init.d/olivares"
}
appliance() {
	mkdir -p "$box/usr/libexec/olivares"
	printf '#!/bin/sh\nexit 0\n' >"$box/usr/libexec/olivares/olivares-product-package-phase"
	chmod 0755 "$box/usr/libexec/olivares/olivares-product-package-phase"
}
set_state() { printf '%s\n' "$1" >"$box/state/enabled"; printf '%s\n' "$2" >"$box/state/active"; }
state() { printf '%s/%s' "$(cat "$box/state/enabled")" "$(cat "$box/state/active")"; }

# run SIDE SCRIPT ARGS...: run one maintainer script (old|new) in the case root.
# Output goes to $box/out/<k>.{stdout,stderr,rc}; the rc is also in $last_rc.
run() {
	local side=$1 name=$2 dir k
	shift 2
	case "$side" in old) dir=$old_dir ;; new) dir=$new_dir ;; esac
	mkdir -p "$box/out" "$box/sut"
	k=$(($(find "$box/out" -name '*.rc' | wc -l) + 1))
	rewrite "$dir/$name" "$box/sut/$side-$name" "$box" || could_not_look "could not stage $side $name"
	printf '== %s %s %s\n' "$side" "$name" "$*" >>"$box/calls"
	set +e
	env OLIVARES_TEST_BOX="$box" PATH="$box/bin:${sys_path:-/usr/bin:/bin}" ${run_env:+"$run_env"} \
		${run_timeout:+timeout "$run_timeout"} ${run_sh:-sh} "$box/sut/$side-$name" "$@" >"$box/out/$k.stdout" 2>"$box/out/$k.stderr"
	last_rc=$?
	set -e
	printf '%s\n' "$last_rc" >"$box/out/$k.rc"
	last_out="$box/out/$k"
}
# Every service-changing call. Read-only queries (is-enabled, is-active, status) and
# daemon-reload are not in this set.
mutations() {
	grep -E '^(systemctl (.* )?(enable|disable|start|stop|restart|try-restart|reload-or-restart|condrestart|mask|unmask)( |$)|rc-service olivares (start|stop|restart)|rc-update |olivares uninstall --(preserve|purge)|update-initramfs|dracut|dpkg )' \
		"$box/calls" || true
}
pending_field() { [[ ! -f "$box/run/olivares.pkg-pending" ]] || sed -n "s/^$1=//p" "$box/run/olivares.pkg-pending"; }

seed_install() { # FORMAT SIDE: fresh install by that side's postinstall
	ship "$1"
	case "$1" in
	deb) run "$2" postinstall.sh configure '' ;;
	rpm) run "$2" postinstall.sh 1 ;;
	apk) run "$2" postinstall.sh 1.0.0-r0 ;;
	esac
	[[ "$last_rc" -eq 0 ]] || { not_ok "$case_id: seed install by $2 postinstall rc=$last_rc: $(tr '\n' ' ' <"$last_out.stderr")"; return 1; }
	rm -f "$box/run/olivares.pkg-pending"
	: >"$box/calls"
}

states=("enabled active" "enabled inactive" "disabled active" "disabled inactive" "masked active" "masked inactive")

# --- 1. new→new DEB/RPM: each prior state is kept (the first cell is the negative control)
for fmt in deb rpm; do
	for s in "${states[@]}"; do
		read -r en ac <<<"$s"
		case_id="new->new $fmt $en/$ac"
		new_box "nn-$fmt-$en-$ac"
		seed_install "$fmt" new || continue
		set_state "$en" "$ac"
		# Snapshot-style versions below the first contract version: only the prerm's
		# record, bound to the version it replaces, tells this upgrade is new->new.
		printf '1.0.0\n' >"$box/state/dpkg-version"
		if [[ "$fmt" == deb ]]; then
			run new preremove.sh upgrade 2.0.0; r1=$last_rc
			run new postremove.sh upgrade 2.0.0; r2=$last_rc
			run new postinstall.sh configure 1.0.0; r3=$last_rc; post_out=$last_out
		else
			run new postinstall.sh 2; r1=$last_rc; post_out=$last_out
			run new preremove.sh 1; r2=$last_rc
			run new postremove.sh 1; r3=$last_rc
		fi
		m="$(mutations)"
		if [[ "$r1$r2$r3" != 000 ]]; then
			not_ok "$case_id: script exit codes $r1/$r2/$r3, want 0/0/0"
		elif [[ "$(state)" != "$en/$ac" ]]; then
			not_ok "$case_id: state kept across upgrade — got $(state), want $en/$ac (calls: $(tr '\n' ';' <<<"$m"))"
		elif [[ -n "$m" ]]; then
			not_ok "$case_id: the package changed the service: $(tr '\n' ';' <<<"$m")"
		elif [[ -e "$box/run/olivares.pkg-pending" ]]; then
			not_ok "$case_id: a pending condition was left on an upgrade that kept the state: $(tr '\n' ' ' <"$box/run/olivares.pkg-pending")"
		elif [[ -e "$box/run/olivares.pkg-removed-init" ]]; then
			not_ok "$case_id: the upgrade left a removal record"
		elif grep -q 'predates the upgrade contract' "$post_out.stdout"; then
			not_ok "$case_id: the postinstall warned about a replaced package that honors the contract (n-1)"
		else
			ok "$case_id: state kept, no service call, no pending condition, no warning"
		fi
	done
done

# --- 2. old→new DEB/RPM: the ccf7ea20 prerm runs first; measured, never counted green
for fmt in deb rpm; do
	for s in "${states[@]}"; do
		read -r en ac <<<"$s"
		case_id="old->new $fmt $en/$ac"
		new_box "on-$fmt-$en-$ac"
		seed_install "$fmt" old || continue
		set_state "$en" "$ac"
		if [[ "$fmt" == deb ]]; then
			run old preremove.sh upgrade 26.10.1; r1=$last_rc
			run old postremove.sh upgrade 26.10.1; r2=$last_rc
			run new postinstall.sh configure 26.9.0; r3=$last_rc; post_out=$last_out; post_marker=3
		else
			run new postinstall.sh 2; r1=$last_rc; post_out=$last_out; post_marker=1
			run old preremove.sh 1; r2=$last_rc
			run old postremove.sh 1; r3=$last_rc
		fi
		after="$(state)"
		old_disable="$(grep -c '^systemctl disable --now olivares$' "$box/calls" || true)"
		# The new postinstall's own calls: it never changes the service here.
		new_calls="$(awk -v n="$post_marker" '/^== /{c++; next} c==n' "$box/calls" | grep -E '^(systemctl (enable|disable|start|stop|restart|try-restart|mask|unmask)|rc-)' || true)"
		cond="$(pending_field condition)"
		instr="$(pending_field instruction)"
		if [[ "$r1$r2$r3" != 000 ]]; then
			not_ok "$case_id: script exit codes $r1/$r2/$r3, want 0/0/0"
			continue
		fi
		if [[ "$old_disable" -ne 1 ]]; then
			not_ok "$case_id: the ccf7ea20 prerm did not run 'systemctl disable --now olivares' (measured $old_disable) — the old side is not what shipped"
			continue
		fi
		if [[ -n "$new_calls" ]]; then
			not_ok "$case_id: the new postinstall changed the service after an old prerm (it must never guess): $(tr '\n' ';' <<<"$new_calls")"
			continue
		fi
		# The recovery the new postinstall names, by what it could know.
		want_cond= want_instr=
		if [[ "$fmt" == deb ]]; then
			# No witness of the prior state survives the old postrm: the instruction is
			# conditional and names both cases; nothing is restored.
			if [[ "$after" == disabled/* ]]; then
				want_cond=legacy-prerm-upgrade
				want_instr='if olivares was enabled before this upgrade: systemctl enable --now olivares; if it was running but not enabled: systemctl start olivares'
			fi
		else
			# The new %post ran before the old %preun and recorded the prior state.
			case "$en/$ac" in
			enabled/active) want_cond=replaced-package-may-disable want_instr='systemctl enable --now olivares' ;;
			enabled/inactive) want_cond=replaced-package-may-disable want_instr='systemctl enable olivares' ;;
			disabled/active) want_cond=replaced-package-may-disable want_instr='systemctl start olivares' ;;
			esac
		fi
		if [[ "$cond" != "$want_cond" || "$instr" != "$want_instr" ]]; then
			not_ok "$case_id: pending condition '$cond' / instruction '$instr', want '$want_cond' / '$want_instr'"
			continue
		fi
		if [[ -n "$want_instr" ]] && ! grep -Fq "$want_instr" "$post_out.stdout"; then
			not_ok "$case_id: the new postinstall did not print the instruction '$want_instr'"
			continue
		fi
		# Every old->new row is the old prerm's defect, measured: never a pass.
		defect "$case_id: old prerm 'disable --now' measured ($en/$ac -> $after); recovery: pending ${want_cond:-none} — ${want_instr:-no instruction (nothing to restore, or masked, which is never unmasked)}"
	done
done

# --- 3. APK upgrades: the authorized pre-upgrade stop and the post-upgrade start --------
for side in new old; do
	for ac in active inactive; do
		case_id="$side->new apk $ac"
		new_box "apk-$side-$ac"
		seed_install apk "$side" || continue
		set_state disabled "$ac"
		printf 'yes\n' >"$box/state/rc-default"
		# apk-tools runs .pre-upgrade and .post-upgrade from the NEW package; only the
		# installed state comes from the old side.
		run new apk-preupgrade.sh 2.0.0-r0 1.0.0-r0; r1=$last_rc
		run new postinstall.sh 2.0.0-r0 1.0.0-r0; r2=$last_rc
		want_calls=''
		[[ "$ac" == active ]] && want_calls=$'rc-service olivares stop\nrc-service olivares start'
		got_calls="$(mutations)"
		if [[ "$r1$r2" != 00 ]]; then
			not_ok "$case_id: exit codes $r1/$r2"
		elif [[ "$got_calls" != "$want_calls" ]]; then
			not_ok "$case_id: service calls '$(tr '\n' ';' <<<"$got_calls")', want '$(tr '\n' ';' <<<"$want_calls")'"
		elif [[ "$(cat "$box/state/active")" != "$ac" || "$(cat "$box/state/rc-default")" != yes ]]; then
			not_ok "$case_id: state after upgrade $(cat "$box/state/active")/rc-default=$(cat "$box/state/rc-default")"
		elif grep -q '^systemctl' "$box/calls"; then
			not_ok "$case_id: an APK script called systemctl"
		else
			ok "$case_id: pre-upgrade stop and post-upgrade start from the stamp only when active; runlevel kept"
		fi
	done
done

# --- 4. removal and purge keep today's contract (same service effects as ccf7ea20) ----
removal_calls() { # SIDE FORMAT → prints the service-changing calls of a removal (+purge)
	local side=$1 fmt=$2
	new_box "rm-$side-$fmt"
	seed_install "$fmt" "$side" >/dev/null || return 1
	set_state enabled active
	printf 'yes\n' >"$box/state/rc-default"
	case "$fmt" in
	deb) run "$side" preremove.sh remove; unship; run "$side" postremove.sh remove; run "$side" postremove.sh purge ;;
	rpm) run "$side" preremove.sh 0; unship; run "$side" postremove.sh 0 ;;
	apk) run "$side" preremove.sh 1.0.0-r0; unship; run "$side" postremove.sh 1.0.0-r0 ;;
	esac
	grep -E '^(systemctl (disable|stop|enable|start|daemon-reload)|rc-service olivares (stop|start)|rc-update |olivares uninstall)' "$box/calls" |
		awk '!(/daemon-reload/ && seen++)' | sed "s|$box||g"
	printf 'final %s rc-default=%s data=%s record=%s\n' "$(state)" "$(cat "$box/state/rc-default")" \
		"$([[ -f "$box/var/lib/olivares/install-manifest.json" ]] && echo kept || echo gone)" \
		"$([[ -e "$box/run/olivares.pkg-removed-init" ]] && echo left || echo consumed)"
	grep -h . "$box"/out/*.rc | sort -u | tr '\n' ' '
}
for fmt in deb rpm apk; do
	case_id="removal+purge $fmt"
	old_r="$(removal_calls old "$fmt")" || true
	new_r="$(removal_calls new "$fmt")" || true
	if [[ "$new_r" != "$old_r" ]]; then
		not_ok "$case_id: removal effects differ from ccf7ea20: new [$(tr '\n' ';' <<<"$new_r")] old [$(tr '\n' ';' <<<"$old_r")]"
	elif ! grep -q 'data=kept' <<<"$new_r" || ! grep -q 'record=consumed' <<<"$new_r"; then
		not_ok "$case_id: data not kept or removal record left: $(tr '\n' ';' <<<"$new_r")"
	else
		ok "$case_id: same service effects as ccf7ea20 ($(tr '\n' ';' <<<"$new_r"))"
	fi
done

# --- 5. the format contract: each format admitted, each refused combination named -----
admit() { # CASE FORMAT SCRIPT ARGS...
	local id=$1 fmt=$2 script=$3
	shift 3
	new_box "admit-$n"
	ship "$fmt"
	[[ "$script" != postinstall.sh ]] && { run new postinstall.sh "$(case $fmt in deb) echo configure ;; rpm) echo 1 ;; apk) echo 1.0.0-r0 ;; esac)" >/dev/null; : >"$box/calls"; }
	run new "$script" "$@"
	if [[ "$last_rc" -ne 0 ]]; then
		not_ok "format contract admits $id: rc=$last_rc $(tr '\n' ' ' <"$last_out.stderr")"
	elif ! grep -Eq " script=${script%.sh} .* format=$fmt " "$box/run/olivares.pkg-phases" 2>/dev/null; then
		not_ok "format contract admits $id: no phase receipt naming ${script%.sh} $fmt"
	else
		ok "format contract admits $id"
	fi
}
admit 'deb postinst configure (install)' deb postinstall.sh configure ''
admit 'deb postinst abort-upgrade' deb postinstall.sh abort-upgrade 2.0.0
admit 'rpm %post 1 (install)' rpm postinstall.sh 1
admit 'apk .post-install NEW' apk postinstall.sh 1.0.0-r0
admit 'apk .post-upgrade NEW OLD' apk postinstall.sh 2.0.0-r0 1.0.0-r0
admit 'apk post-install without arguments (runtime witness)' apk postinstall.sh
admit 'deb prerm remove in-favour' deb preremove.sh remove in-favour other 1.0
admit 'deb prerm failed-upgrade' deb preremove.sh failed-upgrade 1.0.0 2.0.0
admit 'apk .pre-upgrade NEW OLD' apk apk-preupgrade.sh 2.0.0-r0 1.0.0-r0
admit 'deb postrm abort-install' deb postremove.sh abort-install

refuse() { # CASE INIT SCRIPT WANT ARGS...
	local id=$1 init=$2 script=$3 want=$4
	shift 4
	new_box "refuse-$n"
	case "$init" in
	systemd) ship deb ;;
	openrc) ship apk ;;
	record:*) ship deb; printf '%s\n' "${init#record:}" >"$box/run/olivares.pkg-removed-init" ;;
	*) ship deb; printf '%s\n' "$init" >"$box/usr/lib/olivares/package-init" ;;
	esac
	run new "$script" "$@"
	local m
	m="$(mutations)"
	if [[ "$last_rc" -eq 0 ]]; then
		not_ok "format contract refuses $id: rc=0"
	elif ! grep -Fq "refused: $want" "$last_out.stderr"; then
		not_ok "format contract refuses $id: stderr lacks 'refused: $want': $(tr '\n' ' ' <"$last_out.stderr")"
	elif [[ -n "$m" ]] || grep -q '^systemctl daemon-reload' "$box/calls"; then
		not_ok "format contract refuses $id: acted before refusing: $(tr '\n' ';' <"$box/calls")"
	elif ! grep -Eq " result=refused:$want " "$box/run/olivares.pkg-phases" 2>/dev/null; then
		not_ok "format contract refuses $id: no 'refused:$want' phase receipt"
	else
		ok "format contract refuses $id ($want)"
	fi
}
refuse 'systemd prerm with an APK version' systemd preremove.sh argument-class-inconsistent 1.2.3-r0
refuse 'systemd prerm without arguments' systemd preremove.sh argument-missing
refuse 'systemd prerm unknown Debian action' systemd preremove.sh argument-class-unknown frobnicate
refuse 'openrc pre-deinstall with a Debian action' openrc preremove.sh argument-class-inconsistent upgrade 2.0.0
refuse 'openrc pre-deinstall with upgrade arguments' openrc preremove.sh argument-class-inconsistent 2.0.0-r0 1.0.0-r0
refuse 'unknown package-init' upstart preremove.sh package-init-unknown remove
refuse 'rpm %post 0' systemd postinstall.sh argument-class-inconsistent 0
refuse 'openrc post-install with a Debian action' openrc postinstall.sh argument-class-inconsistent configure 1.0.0
refuse 'systemd postinst with an APK version' systemd postinstall.sh argument-class-inconsistent 2.0.0-r0
refuse 'unknown package-init in postinst' upstart postinstall.sh package-init-unknown configure
refuse 'systemd postrm with an APK version' systemd postremove.sh argument-class-inconsistent 1.0.0-r0
refuse 'openrc post-deinstall with a Debian action' openrc postremove.sh argument-class-inconsistent purge
refuse 'systemd pre-upgrade (APK hook) ' systemd apk-preupgrade.sh argument-class-inconsistent 2.0.0-r0 1.0.0-r0
refuse 'openrc pre-upgrade with one argument' openrc apk-preupgrade.sh argument-class-inconsistent 2.0.0-r0
# r2 (read-r1 m-1): every other refused combination, by name.
refuse 'postrm with a corrupt removal record' record:upstart postremove.sh removal-record-unknown remove
refuse 'postrm without arguments' systemd postremove.sh argument-missing
refuse 'postrm unknown Debian action' systemd postremove.sh argument-class-unknown frobnicate
refuse 'unknown package-init in postrm' upstart postremove.sh package-init-unknown upgrade 2.0.0
refuse 'postinst unknown Debian action' systemd postinstall.sh argument-class-unknown frobnicate
refuse 'openrc post-install with three versions' openrc postinstall.sh argument-class-inconsistent 3.0.0-r0 2.0.0-r0 1.0.0-r0
refuse 'rpm %preun count with an extra argument' systemd preremove.sh argument-class-inconsistent 1 extra
refuse 'rpm %post count with an extra argument' systemd postinstall.sh argument-class-inconsistent 2 extra

# --- 6. the appliance: the bridge is installed → no start or restart, a named pending --
for fmt in deb rpm apk; do
	case_id="appliance $fmt upgrade, active"
	new_box "appl-$fmt"
	seed_install "$fmt" new || continue
	appliance
	set_state enabled active
	case "$fmt" in
	deb) run new preremove.sh upgrade 2.0.0; run new postremove.sh upgrade 2.0.0; run new postinstall.sh configure 1.0.0; post_out=$last_out ;;
	rpm) run new postinstall.sh 2; post_out=$last_out; run new preremove.sh 1; run new postremove.sh 1 ;;
	apk) run new apk-preupgrade.sh 2.0.0-r0 1.0.0-r0; run new postinstall.sh 2.0.0-r0 1.0.0-r0; post_out=$last_out ;;
	esac
	starts="$(grep -E '^(systemctl (.* )?(enable|start|restart|try-restart|reload-or-restart)|rc-service olivares start)' "$box/calls" || true)"
	if [[ -n "$starts" ]]; then
		not_ok "$case_id: started or restarted on the appliance: $(tr '\n' ';' <<<"$starts")"
	elif [[ "$(pending_field condition)" != package_recovery_required ||
		"$(pending_field reason)" != product-start-owned-by-package-phase ]]; then
		not_ok "$case_id: pending condition '$(pending_field condition)/$(pending_field reason)', want package_recovery_required/product-start-owned-by-package-phase"
	elif ! grep -Fq 'olivares-product-package-phase' "$post_out.stdout"; then
		not_ok "$case_id: the postinstall did not name the package-phase owner"
	else
		ok "$case_id: no start or restart; pending package_recovery_required ($(pending_field prior))"
	fi
done

# --- 8. r2 M-1: the Debian recovery is bound to the transaction ------------------------
# clean_after CASE OUT: no pending record, no legacy notice, no upgrade record left.
clean_after() {
	if [[ -e "$box/run/olivares.pkg-pending" ]]; then
		not_ok "$1: pending condition reported: $(tr '\n' ' ' <"$box/run/olivares.pkg-pending")"
	elif grep -q 'predates the upgrade contract' "$2.stdout"; then
		not_ok "$1: the postinstall printed the legacy-prerm notice"
	elif [[ -e "$box/run/olivares.pkg-upgrade-state" ]]; then
		not_ok "$1: the upgrade record was left behind: $(tr '\n' ' ' <"$box/run/olivares.pkg-upgrade-state")"
	else
		return 0
	fi
	return 1
}
case_id="M-1 deb fresh install (configure without an old version) (guard)"
new_box m1-fresh
ship deb
run new postinstall.sh configure ''
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif clean_after "$case_id" "$last_out"; then ok "$case_id: no pending condition"; fi

case_id="M-1 deb remove, then reinstall of a contract version"
new_box m1-reinstall
seed_install deb new || true
set_state enabled active
run new preremove.sh remove; r1=$last_rc
unship
run new postremove.sh remove; r2=$last_rc
ship deb
# dpkg keeps the configured version of a config-files package: configure <old>.
run new postinstall.sh configure 26.10.0; r3=$last_rc; post_out=$last_out
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$(state)" != disabled/inactive ]]; then not_ok "$case_id: the removal effect changed: $(state)"
elif clean_after "$case_id" "$post_out"; then ok "$case_id: the operator's removal is not reported as a legacy prerm"; fi

case_id="M-1 deb upgrade, then unwind (abort-upgrade consumes the record)"
new_box m1-unwind
seed_install deb new || true
set_state enabled active
printf '26.10.0\n' >"$box/state/dpkg-version"
run new preremove.sh upgrade 26.10.1; r1=$last_rc
run new postremove.sh abort-upgrade 26.10.0 26.10.1; r2=$last_rc
run new postinstall.sh abort-upgrade 26.10.1; r3=$last_rc; post_out=$last_out
m="$(mutations)"
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$(state)" != enabled/active || -n "$m" ]]; then not_ok "$case_id: state $(state), calls $(tr '\n' ';' <<<"$m")"
elif clean_after "$case_id" "$post_out"; then ok "$case_id: state kept, record consumed, no pending"; fi

case_id="M-1 deb prerm upgrade fails over, then only postinst abort-upgrade runs"
# Policy 6.6: old-prerm upgrade fails, new-prerm failed-upgrade fails, and dpkg unwinds
# with old-postinst abort-upgrade alone. The record the prerm wrote must not outlive it.
new_box m1-unwind-postinst
seed_install deb new || true
set_state enabled active
printf '26.10.0\n' >"$box/state/dpkg-version"
run new preremove.sh upgrade 26.10.1; r1=$last_rc
run new postinstall.sh abort-upgrade 26.10.1; r2=$last_rc; post_out=$last_out
if [[ "$r1$r2" != 00 ]]; then not_ok "$case_id: exit codes $r1/$r2"
elif [[ "$(state)" != enabled/active || -n "$(mutations)" ]]; then not_ok "$case_id: state $(state)"
elif clean_after "$case_id" "$post_out"; then ok "$case_id: record consumed by postinst abort-upgrade"; fi

case_id="M-1 deb downgrade to ccf7ea20, then re-upgrade in the same boot"
new_box m1-downgrade
seed_install deb new || true
set_state enabled active
printf '26.10.1\n' >"$box/state/dpkg-version"
run new preremove.sh upgrade 26.9.0; r1=$last_rc
run new postremove.sh upgrade 26.9.0; r2=$last_rc
run old postinstall.sh configure 26.10.1; r3=$last_rc
run old preremove.sh upgrade 26.10.1; r4=$last_rc
run old postremove.sh upgrade 26.10.1; r5=$last_rc
run new postinstall.sh configure 26.9.0; r6=$last_rc; post_out=$last_out
want_instr='if olivares was enabled before this upgrade: systemctl enable --now olivares; if it was running but not enabled: systemctl start olivares'
if [[ "$r1$r2$r3$r4$r5$r6" != 000000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3/$r4/$r5/$r6"
elif [[ "$(grep -c '^systemctl disable --now olivares$' "$box/calls")" -ne 1 ]]; then not_ok "$case_id: the ccf7ea20 prerm's disable was not measured once"
elif [[ "$(pending_field condition)" != legacy-prerm-upgrade || "$(pending_field instruction)" != "$want_instr" ]]; then
	not_ok "$case_id: pending '$(pending_field condition)' / '$(pending_field instruction)', want legacy-prerm-upgrade (a stale record hid the defect)"
elif ! grep -Fq "$want_instr" "$post_out.stdout"; then not_ok "$case_id: the instruction was not printed"
else defect "$case_id: old prerm 'disable --now' measured (enabled/active -> $(state)); recovery: pending legacy-prerm-upgrade — $want_instr"; fi

case_id="n-1 rpm new->new with the contract marker missing: the current %preun withdraws the record"
# Without the marker the %post cannot tell a current %preun from an old one, so it
# records the state and warns; the current %preun then withdraws that record.
new_box n1-rpm-no-marker
seed_install rpm new || true
# r3b: the marker lives in the root-owned /var/lib/olivares-package (r2 and r3: the data directory).
rm -f "$box/var/lib/olivares-package/scripts-contract" "$box/var/lib/olivares/.package-scripts-contract"
set_state enabled active
run new postinstall.sh 2; r1=$last_rc
wrote="$(pending_field condition)"
run new preremove.sh 1; r2=$last_rc
run new postremove.sh 1; r3=$last_rc
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$wrote" != replaced-package-may-disable ]]; then not_ok "$case_id: the %post wrote '$wrote', want replaced-package-may-disable"
elif [[ -e "$box/run/olivares.pkg-pending" ]]; then not_ok "$case_id: the current %preun did not withdraw the record"
elif [[ "$(state)" != enabled/active || -n "$(mutations)" ]]; then not_ok "$case_id: state $(state)"
else ok "$case_id: recorded, then withdrawn; state kept"; fi

# --- 9. r2 m-2: deconfigure stops without disabling; its unwind restarts from the record
for s in "enabled active" "disabled inactive"; do
	read -r en ac <<<"$s"
	for next in abort-deconfigure configure; do
		case_id="m-2 deb deconfigure, then $next $en/$ac"
		new_box "m2-$next-$en-$ac"
		seed_install deb new || continue
		set_state "$en" "$ac"
		run new preremove.sh deconfigure in-favour other 1.0 removing olivares 26.10.0; r1=$last_rc
		if [[ "$next" == configure ]]; then
			run new postinstall.sh configure 26.10.0; r2=$last_rc; post_out=$last_out
		else
			run new postinstall.sh abort-deconfigure in-favour other 1.0 removing olivares 26.10.0; r2=$last_rc; post_out=$last_out
		fi
		m="$(mutations)"
		want=''
		want_state="$en/$ac"
		if [[ "$ac" == active && "$next" == configure ]]; then
			# r3 (read-r2 M2-1): configure consumes the record without a start and prints
			# the instruction; only abort-deconfigure restarts.
			want='systemctl stop olivares'
			want_state="$en/inactive"
		elif [[ "$ac" == active ]]; then
			want=$'systemctl stop olivares\nsystemctl start olivares'
		fi
		if [[ "$r1$r2" != 00 ]]; then not_ok "$case_id: exit codes $r1/$r2"
		elif [[ "$(state)" != "$want_state" ]]; then not_ok "$case_id: state $(state), want $want_state (calls: $(tr '\n' ';' <<<"$m"))"
		elif [[ "$m" != "$want" ]]; then not_ok "$case_id: service calls '$(tr '\n' ';' <<<"$m")', want '$(tr '\n' ';' <<<"$want")'"
		elif [[ -e "$box/run/olivares.pkg-pending" || -e "$box/run/olivares.pkg-deconfigured" ]]; then not_ok "$case_id: a pending condition or the deconfigure record was left"
		elif [[ "$want_state" != "$en/$ac" ]] && ! grep -Fq 'olivares was stopped when it was deconfigured; start it with: systemctl start olivares' "$post_out.stdout"; then
			not_ok "$case_id: the configure did not print the start instruction"
		elif [[ "$want_state" == "$en/$ac" && "$next" == configure ]] && grep -q 'stopped when it was deconfigured' "$post_out.stdout"; then
			not_ok "$case_id: an instruction was printed for a service that was not running"
		else ok "$case_id: enablement kept; $( [[ "$want_state" == "$en/$ac" ]] && echo 'state kept' || echo 'not restarted, instruction printed')"; fi
	done
done
case_id="m-2 appliance deb deconfigure, then abort-deconfigure enabled/active"
new_box m2-appliance
seed_install deb new || true
appliance
set_state enabled active
run new preremove.sh deconfigure in-favour other 1.0; r1=$last_rc
run new postinstall.sh abort-deconfigure in-favour other 1.0; r2=$last_rc
starts="$(grep -E '^systemctl (.* )?(enable|start|restart|try-restart)' "$box/calls" || true)"
if [[ "$r1$r2" != 00 ]]; then not_ok "$case_id: exit codes $r1/$r2"
elif [[ -n "$starts" || "$(state)" != enabled/inactive ]]; then not_ok "$case_id: state $(state), starts '$(tr '\n' ';' <<<"$starts")' (want enabled/inactive, no start)"
elif [[ "$(pending_field condition)" != package_recovery_required || "$(pending_field prior)" != active-before-deconfigure ]]; then
	not_ok "$case_id: pending '$(pending_field condition)' prior '$(pending_field prior)', want package_recovery_required / active-before-deconfigure"
else ok "$case_id: no start on the appliance; pending package_recovery_required (active-before-deconfigure)"; fi

# --- 10. r2 m-3: postremove without evidence records format=unknown ------------------
for args in purge 1.0.0-r0; do
	case_id="m-3 postremove $args with no removal record and no stamp"
	new_box "m3-$args"
	run new postremove.sh "$args"
	rec="$(grep -o 'script=[a-z-]* .* format=[a-z]*' "$box/run/olivares.pkg-phases" 2>/dev/null | sed 's/ maintscript=[^ ]*//' || true)"
	if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc $(tr '\n' ' ' <"$last_out.stderr")"
	elif [[ "$rec" != "script=postremove format=unknown" ]]; then not_ok "$case_id: receipt '$rec', want format unknown"
	elif [[ -n "$(mutations)" ]] || grep -q '^systemctl' "$box/calls"; then not_ok "$case_id: acted without evidence"
	else ok "$case_id: receipt '$rec', no effect"; fi
done

# --- 11. r2 m-4: on the appliance the postinstall prints only the appliance notice ----
for fmt in deb apk; do
	case_id="m-4 appliance $fmt fresh install prints no start or enable instruction"
	new_box "m4-$fmt"
	ship "$fmt"
	appliance
	case "$fmt" in deb) run new postinstall.sh configure '' ;; apk) run new postinstall.sh 1.0.0-r0 ;; esac
	if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
	elif grep -E 'Start it|enable --now|rc-service olivares start|rc-update add' "$last_out.stdout" >/dev/null; then
		not_ok "$case_id: printed '$(grep -E 'Start it|enable --now|rc-service olivares start|rc-update add' "$last_out.stdout" | head -1)'"
	elif ! grep -Fq 'olivares-product-package-phase' "$last_out.stdout"; then not_ok "$case_id: the appliance notice is missing"
	else ok "$case_id: appliance notice only"; fi
done

# --- 12. r2 n-4: the other systemd enablement states ----------------------------------
# RPM old->new: the new %post records the state the old %preun will change.
for s in "static active systemctl start olivares" "enabled-runtime active systemctl start olivares" \
	"indirect active systemctl start olivares" "linked active systemctl start olivares" \
	"static inactive" "linked inactive"; do
	read -r en ac want_instr <<<"$s"
	case_id="n-4 old->new rpm $en/$ac"
	new_box "n4-rpm-$en-$ac"
	seed_install rpm old || continue
	set_state "$en" "$ac"
	run new postinstall.sh 2; r1=$last_rc
	run old preremove.sh 1; r2=$last_rc
	run old postremove.sh 1; r3=$last_rc
	want_cond=''
	[[ -n "$want_instr" ]] && want_cond=replaced-package-may-disable
	if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
	elif [[ "$(pending_field condition)" != "$want_cond" || "$(pending_field instruction)" != "$want_instr" ]]; then
		not_ok "$case_id: pending '$(pending_field condition)' / '$(pending_field instruction)', want '$want_cond' / '$want_instr'"
	else defect "$case_id: old %preun 'disable --now' measured ($en/$ac -> $(state)); recovery: ${want_instr:-none (nothing to restore)}"; fi
done
# DEB old->new: a static unit stays static after the old prerm, which still stopped it.
case_id="n-4 old->new deb static/active"
new_box n4-deb-static
seed_install deb old || true
set_state static active
run old preremove.sh upgrade 26.10.1; r1=$last_rc
run old postremove.sh upgrade 26.10.1; r2=$last_rc
run new postinstall.sh configure 26.9.0; r3=$last_rc
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$(pending_field condition)" != legacy-prerm-upgrade ]]; then not_ok "$case_id: pending '$(pending_field condition)', want legacy-prerm-upgrade (static/active -> $(state))"
else defect "$case_id: old prerm 'disable --now' measured (static/active -> $(state)); recovery: pending legacy-prerm-upgrade"; fi

# --- 13. r3 M2-1: the deconfigure record is honored only by abort-deconfigure -----------
# deconf_after CASE WANT_STATE: no start or enable after the deconfigure's stop, and the
# deconfigure record is gone.
deconf_after() {
	local starts
	starts="$(grep -E '^systemctl (.* )?(enable|start|restart|try-restart|reload-or-restart)' "$box/calls" || true)"
	if [[ -n "$starts" ]]; then not_ok "$1: the package started the service: $(tr '\n' ';' <<<"$starts")"
	elif [[ "$(state)" != "$2" ]]; then not_ok "$1: state $(state), want $2"
	elif [[ -e "$box/run/olivares.pkg-deconfigured" ]]; then not_ok "$1: the deconfigure record was left"
	else return 0; fi
	return 1
}
case_id="M2-1 deb deconfigure, then remove, then reinstall (configure 26.10.0)"
new_box m21-remove-reinstall
seed_install deb new || true
set_state enabled active
run new preremove.sh deconfigure in-favour other 1.0; r1=$last_rc
run new preremove.sh remove; r2=$last_rc
unship
run new postremove.sh remove; r3=$last_rc
ship deb
run new postinstall.sh configure 26.10.0; r4=$last_rc; post_out=$last_out
if [[ "$r1$r2$r3$r4" != 0000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3/$r4"
elif deconf_after "$case_id" disabled/inactive; then
	if grep -q 'stopped when it was deconfigured' "$post_out.stdout"; then not_ok "$case_id: the removal did not consume the record (the reinstall printed the deconfigure instruction)"
	else ok "$case_id: no start; the removal consumed the record"; fi
fi

case_id="M2-1 deb deconfigure, then remove and purge, then fresh install"
new_box m21-purge-fresh
seed_install deb new || true
set_state enabled active
run new preremove.sh deconfigure in-favour other 1.0; r1=$last_rc
run new preremove.sh remove; r2=$last_rc
unship
run new postremove.sh remove; r3=$last_rc
run new postremove.sh purge; r4=$last_rc
ship deb
run new postinstall.sh configure ''; r5=$last_rc
if [[ "$r1$r2$r3$r4$r5" != 00000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3/$r4/$r5"
elif deconf_after "$case_id" disabled/inactive; then ok "$case_id: no start on the fresh install"; fi

case_id="M2-1 deb deconfigure, then its files disappear (postrm disappear), then fresh install"
# The one Policy path where the postrm runs without prerm remove: it must consume the record.
new_box m21-disappear
seed_install deb new || true
set_state enabled active
run new preremove.sh deconfigure in-favour other 1.0; r1=$last_rc
unship
run new postremove.sh disappear other 1.0; r2=$last_rc
ship deb
run new postinstall.sh configure ''; r3=$last_rc; post_out=$last_out
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif deconf_after "$case_id" enabled/inactive; then
	if grep -q 'stopped when it was deconfigured' "$post_out.stdout"; then not_ok "$case_id: postrm disappear did not consume the record"
	else ok "$case_id: no start; postrm disappear consumed the record"; fi
fi

case_id="M2-1 appliance deb deconfigure, then configure enabled/active"
new_box m21-appliance-configure
seed_install deb new || true
appliance
set_state enabled active
run new preremove.sh deconfigure in-favour other 1.0; r1=$last_rc
run new postinstall.sh configure 26.10.0; r2=$last_rc; post_out=$last_out
if [[ "$r1$r2" != 00 ]]; then not_ok "$case_id: exit codes $r1/$r2"
elif deconf_after "$case_id" enabled/inactive; then
	if [[ "$(pending_field condition)" != package_recovery_required || "$(pending_field prior)" != active-before-deconfigure ]]; then
		not_ok "$case_id: pending '$(pending_field condition)' prior '$(pending_field prior)', want package_recovery_required / active-before-deconfigure"
	elif grep -q 'systemctl start olivares' "$post_out.stdout"; then not_ok "$case_id: the appliance printed a start instruction"
	else ok "$case_id: no start; pending package_recovery_required"; fi
fi

# --- 14. r3 m2-3: a masked unit is never started from the deconfigure record ------------
case_id="m2-3 deb deconfigure, then abort-deconfigure masked/active (guard)"
new_box m23-masked
seed_install deb new || true
set_state masked active
run new preremove.sh deconfigure in-favour other 1.0; r1=$last_rc
run new postinstall.sh abort-deconfigure in-favour other 1.0; r2=$last_rc
m="$(mutations)"
if [[ "$r1$r2" != 00 ]]; then not_ok "$case_id: exit codes $r1/$r2"
elif [[ "$m" != 'systemctl stop olivares' || "$(state)" != masked/inactive ]]; then
	not_ok "$case_id: calls '$(tr '\n' ';' <<<"$m")', state $(state); want only the stop and masked/inactive"
else ok "$case_id: stopped, never started, still masked"; fi

# --- 15. r3 m2-1: the contract-marker test fails closed without a working find ---------
for how in failing missing; do
	case_id="m2-1 rpm downgrade to ccf7ea20 and back with find $how: the %post still warns"
	new_box "m21-find-$how"
	seed_install rpm new || continue
	set_state enabled active
	run old postinstall.sh 2; run new preremove.sh 1; run new postremove.sh 1
	if [[ "$how" == failing ]]; then
		printf '#!/bin/sh\nexit 127\n' >"$box/bin/find"
		chmod 0755 "$box/bin/find"
	else
		# Every system tool but find: the scripts' own PATH lookup must miss it.
		mkdir -p "$box/sys"
		for f in /usr/bin/* /bin/*; do
			b=${f##*/}
			[[ "$b" == find || -e "$box/sys/$b" ]] || ln -s "$f" "$box/sys/$b"
		done
		sys_path="$box/sys"
	fi
	run new postinstall.sh 2; r1=$last_rc; post_out=$last_out
	sys_path=
	run old preremove.sh 1; r2=$last_rc
	run old postremove.sh 1; r3=$last_rc
	if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3 $(tr '\n' ' ' <"$post_out.stderr")"
	elif [[ "$(pending_field condition)" != replaced-package-may-disable ]] || ! grep -q 'predates the upgrade contract' "$post_out.stdout"; then
		not_ok "$case_id: no pending record or warning — an unavailable find was read as 'honors the contract'"
	else defect "$case_id: old %preun 'disable --now' measured (enabled/active -> $(state)); recovery: pending replaced-package-may-disable"; fi
done

case_id="m2-1 rpm old->new with a contract marker link planted by the service account"
# A link at the marker path, to a file newer than the manifest, must not read as
# "the installed scripts honor the contract": the old %preun still disables.
new_box m21-marker-link
seed_install rpm old || true
set_state enabled active
# r4: the target forges a marker that matches the current manifest's inode and mtime,
# so only the link test can refuse it.
printf 'upgrade-dispatch-v1\nmanifest_ino=%s\nmanifest_mtime=%s\n' "$(stat -c %i "$box/var/lib/olivares/install-manifest.json")" \
	"$(stat -c %y "$box/var/lib/olivares/install-manifest.json" | tr ' ' '_')" >"$box/fresh"
# r3b: planted where the postinst reads it, the root-owned /var/lib/olivares-package
# (as if that directory had been compromised), and at the r3 location in the data directory.
mkdir -p "$box/var/lib/olivares-package"
rm -f "$box/var/lib/olivares-package/scripts-contract"
ln -s "$box/fresh" "$box/var/lib/olivares-package/scripts-contract"
ln -s "$box/fresh" "$box/var/lib/olivares/.package-scripts-contract"
run new postinstall.sh 2; r1=$last_rc; post_out=$last_out
run old preremove.sh 1; r2=$last_rc
run old postremove.sh 1; r3=$last_rc
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$(pending_field condition)" != replaced-package-may-disable ]] || ! grep -q 'predates the upgrade contract' "$post_out.stdout"; then
	not_ok "$case_id: no pending record or warning — a linked marker was read as 'honors the contract'"
else defect "$case_id: old %preun 'disable --now' measured (enabled/active -> $(state)); recovery: pending replaced-package-may-disable"; fi

# --- 16. r3 m2-2: root never writes through a link planted in /var/lib/olivares ---------
for target in install-manifest.json .package-scripts-contract; do
	case_id="m2-2 deb postinst with a symlink planted at /var/lib/olivares/$target"
	new_box "m22-$target"
	seed_install deb new || continue
	printf 'victim\n' >"$box/victim"
	chmod 0600 "$box/victim"
	rm -f "$box/var/lib/olivares/$target"
	ln -s "$box/victim" "$box/var/lib/olivares/$target"
	run new postinstall.sh configure 26.10.0
	if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc $(tr '\n' ' ' <"$last_out.stderr")"
	elif [[ "$(cat "$box/victim")" != victim || "$(stat -c '%a' "$box/victim")" != 600 ]]; then
		not_ok "$case_id: the link target was rewritten or re-moded: '$(head -c 40 "$box/victim" | tr '\n' ' ')' mode $(stat -c '%a' "$box/victim")"
	elif [[ "$target" == install-manifest.json ]] && [[ -L "$box/var/lib/olivares/$target" || ! -f "$box/var/lib/olivares/$target" ]]; then
		not_ok "$case_id: $target is not a regular file after the postinst"
	elif [[ "$target" != install-manifest.json ]] && [[ -L "$box/var/lib/olivares-package/scripts-contract" || ! -f "$box/var/lib/olivares-package/scripts-contract" ]]; then
		# r3b: the marker lives in the root-owned /var/lib/olivares-package.
		not_ok "$case_id: no regular marker in /var/lib/olivares-package"
	else ok "$case_id: the link target untouched; $( [[ "$target" == install-manifest.json ]] && echo 'the link was replaced' || echo 'the marker is in the root-owned directory')"; fi
done

# --- 17. r3 Root N-10: a failed dpkg-query leaves old= empty and is never read as legacy -
case_id="N-10 deb prerm upgrade with dpkg-query failing, then configure 26.10.0"
new_box n10-query-fails
seed_install deb new || true
set_state disabled inactive
rm -f "$box/state/dpkg-version"
run new preremove.sh upgrade 26.10.1; r1=$last_rc
rec_old="$(grep -x 'old=.*' "$box/run/olivares.pkg-upgrade-state" 2>/dev/null || echo 'no record')"
run new postremove.sh upgrade 26.10.1; r2=$last_rc
run new postinstall.sh configure 26.10.0; r3=$last_rc; post_out=$last_out
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$rec_old" != 'old=unknown' ]]; then
	# r4: the v1 schema spells N-10's empty value "unknown"; it never matches.
	not_ok "$case_id: the prerm recorded '$rec_old', want old=unknown when the query fails"
elif clean_after "$case_id" "$post_out"; then ok "$case_id: old= empty; nothing reported"; fi

# --- 18. r3b m2-2: custody of the manifest and the marker (owner's protocol) -------------
# The manifest keeps its final name <data-dir>/install-manifest.json; the marker lives in
# the root-owned /var/lib/olivares-package. Planted objects are placed by one-shot hooks in
# the observing shadows: "window" fires when root removes the name (the r3 create) or just
# before root renames onto it (the r3b protocol); "after" fires after root put the file in
# place (after the rename, or on the r3 chown of the final name).
M=var/lib/olivares/install-manifest.json
K=var/lib/olivares/.package-scripts-contract
KNEW=var/lib/olivares-package/scripts-contract
# plant_hook KIND NAME COMMAND: a one-shot hook that runs COMMAND with $1 = the path.
plant_hook() { printf '%s\n' "$3" >"$box/hooks/$1-$2"; }
# custody_ok CASE: the manifest is a regular file carrying the v2 schema, the marker the
# postinst reads next time is a regular file in the root-owned directory, rc 0.
custody_ok() {
	if [[ "$last_rc" -ne 0 ]]; then not_ok "$1: rc=$last_rc $(tr '\n' ' ' <"$last_out.stderr" | cut -c1-160)"
	elif [[ -L "$box/$M" || ! -f "$box/$M" ]] || ! grep -q 'olivares.ai/local-install/v2' "$box/$M"; then
		not_ok "$1: install-manifest.json is not a regular file with the manifest"
	elif [[ -L "$box/$KNEW" || ! -f "$box/$KNEW" ]]; then not_ok "$1: no regular marker in /var/lib/olivares-package"
	else return 0; fi
	return 1
}
victim_ok() { [[ "$(cat "$box/victim")" == victim && "$(stat -c '%a' "$box/victim")" == 600 ]]; }
# refused_as CASE NAME: a named refusal with its receipt, and nothing written into the
# data directory under the manifest name by this run.
refused_as() {
	if [[ "$last_rc" -eq 0 ]]; then not_ok "$1: rc=0, want refused: $2"
	elif ! grep -Fq "refused: $2" "$last_out.stderr"; then not_ok "$1: stderr lacks 'refused: $2': $(tr '\n' ' ' <"$last_out.stderr" | cut -c1-160)"
	elif ! grep -Eq " result=refused:$2 " "$box/run/olivares.pkg-phases" 2>/dev/null; then not_ok "$1: no 'refused:$2' receipt"
	else return 0; fi
	return 1
}
for name in install-manifest.json .package-scripts-contract; do
	for kind in fifo device regular; do
		case_id="m2-2 $name: a $kind link planted at the name in the window before root's final operation"
		new_box "m22b-$kind-$name"
		ship deb
		printf 'victim\n' >"$box/victim"; chmod 0600 "$box/victim"
		case "$kind" in
		fifo) mkfifo "$box/fifo"; tgt="$box/fifo"; ( timeout 4 cat "$box/fifo" >"$box/captured" 2>/dev/null ) & reader=$! ;;
		device) tgt=/dev/null ;;
		regular) tgt="$box/victim" ;;
		esac
		# A FIFO is planted only in the window: a FIFO there from the start would block any
		# script that reads the name (the next cell covers that separately).
		[[ "$kind" == fifo ]] || ln -s "$tgt" "$box/var/lib/olivares/$name"
		plant_hook window "$name" "/usr/bin/rm -f \"\$1\"; ln -s '$tgt' \"\$1\""
		run new postinstall.sh configure ''
		if [[ "$kind" == fifo ]]; then wait "$reader" 2>/dev/null || true; fi
		if [[ "$kind" == fifo && -s "$box/captured" ]]; then not_ok "$case_id: root wrote $(wc -c <"$box/captured") bytes through the FIFO link"
		elif ! victim_ok; then not_ok "$case_id: the victim was rewritten or re-moded"
		elif custody_ok "$case_id"; then ok "$case_id: nothing written through it; manifest renamed into place, marker in the root-owned directory"; fi
	done
done
case_id="m2-2 install-manifest.json: a FIFO at the name does not block the postinst"
# A reader opening the FIFO blocks until a writer comes; the unblocker opens it for writing
# after 5 s. A postinst that never reads through the name finishes before that.
new_box m22b-fifo-name
ship deb
mkfifo "$box/$M"
# Every reader is released while the name is still a FIFO; once root replaced it, the
# unblocker stops. It is killed at the end in case it waits on a FIFO nobody reads.
( sleep 5; while [ -p "$box/$M" ]; do exec 3>"$box/$M"; exec 3>&-; done ) & unblock=$!
start=$SECONDS
run_timeout=30
run new postinstall.sh configure ''
run_timeout=
took=$((SECONDS - start))
kill "$unblock" 2>/dev/null || true; wait "$unblock" 2>/dev/null || true
if [[ "$took" -ge 5 ]]; then not_ok "$case_id: the postinst waited ${took}s on the FIFO (it read through the name)"
elif custody_ok "$case_id"; then ok "$case_id: finished in ${took}s; the FIFO was replaced by the rename"; fi
case_id="m2-2 install-manifest.json: a directory at the destination refuses by name"
new_box m22b-dir-static
ship deb
mkdir -p "$box/$M"
run new postinstall.sh configure ''
if refused_as "$case_id" manifest-destination-not-replaceable; then
	if ! grep -Fq 'move it aside' "$last_out.stderr"; then not_ok "$case_id: the refusal does not give the instruction"
	elif [[ ! -d "$box/$M" ]] || [[ -n "$(ls -A "$box/$M")" ]]; then not_ok "$case_id: the directory was changed"
	else ok "$case_id: refused by name, with the instruction; the directory untouched"; fi
fi
case_id="m2-2 install-manifest.json: a directory planted in the window refuses by name"
new_box m22b-dir-window
ship deb
plant_hook window install-manifest.json '/usr/bin/rm -f "$1"; mkdir "$1"'
run new postinstall.sh configure ''
if refused_as "$case_id" manifest-rename-failed; then
	if [[ -n "$(ls -A "$box/$M" 2>/dev/null)" ]]; then not_ok "$case_id: a file was moved into the planted directory"
	else ok "$case_id: refused by name; nothing moved into the directory"; fi
fi
case_id="m2-2 .package-scripts-contract: a directory at the old marker name no longer blocks the postinst"
new_box m22b-dir-marker
ship deb
mkdir -p "$box/$K"
run new postinstall.sh configure ''
if custody_ok "$case_id"; then ok "$case_id: postinst completes; the old name is untouched"; fi
case_id="m2-2 install-manifest.json: a hard link swapped in after root put the file in place"
new_box m22b-hardlink-after
ship deb
printf 'victim\n' >"$box/victim"; chmod 0600 "$box/victim"
plant_hook after install-manifest.json "/usr/bin/rm -f \"\$1\"; ln '$box/victim' \"\$1\""
run new postinstall.sh configure ''
late="$(awk '/^hook after .*install-manifest.json$/{f=1; next} f' "$box/calls" | grep -E "^(chown|chmod|rm|mv) .*$box/$M\$" || true)"
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif ! grep -q '^hook after ' "$box/calls"; then not_ok "$case_id: root never put the manifest in place by a rename or chown the hook could observe"
elif [[ -n "$late" ]]; then not_ok "$case_id: root operated on the manifest name after placing it: $(tr '\n' ';' <<<"$late")"
elif ! victim_ok; then not_ok "$case_id: the victim was changed"
else ok "$case_id: no root path operation on the name after the rename; the victim untouched"; fi
case_id="m2-2 install-manifest.json: the staging directory on another filesystem refuses by name"
new_box m22b-cross-fs
ship deb
: >"$box/state/stage-other-fs"
run new postinstall.sh configure ''
if refused_as "$case_id" manifest-staging-cross-filesystem; then
	if [[ -e "$box/$M" ]]; then not_ok "$case_id: a manifest was put in place anyway"
	else ok "$case_id: refused by name; nothing renamed across filesystems"; fi
fi
case_id="m2-2 install-manifest.json: an mv without -T refuses by name"
new_box m22b-no-T
ship deb
: >"$box/state/mv-without-T"
run new postinstall.sh configure ''
if refused_as "$case_id" manifest-rename-unsupported; then
	if [[ -e "$box/$M" ]]; then not_ok "$case_id: a manifest was put in place anyway"
	else ok "$case_id: refused by name before any rename"; fi
fi
for how in link writable file parent; do
	case_id="m2-2 an unsafe /var/lib/olivares-package ($how) refuses by name"
	new_box "m22b-state-$how"
	ship deb
	case "$how" in
	link) mkdir -p "$box/elsewhere"; ln -s "$box/elsewhere" "$box/var/lib/olivares-package" ;;
	writable) mkdir -p "$box/var/lib/olivares-package"; chmod 0777 "$box/var/lib/olivares-package" ;;
	file) : >"$box/var/lib/olivares-package" ;;
	parent) chmod 0777 "$box/var/lib" ;;   # a parent the service account could write
	esac
	run new postinstall.sh configure ''
	if refused_as "$case_id" marker-directory-unsafe; then
		if [[ -e "$box/$M" || -n "$(ls -A "$box/elsewhere" 2>/dev/null)" ]]; then not_ok "$case_id: something was written anyway"
		else ok "$case_id: refused by name before any write"; fi
	fi
done

# --- 19. r4: the m-5 record schema v1 and the read-r3b decisions ------------------------
# Every line and record the shipped scripts write is parsed by the fixture's checker
# (scripts/fixtures/package-upgrade/records-v1), in --grammar mode (boot=unknown allowed).
P=run/olivares.pkg-phases
host_boot="$(cat /proc/sys/kernel/random/boot_id)"
v1ok() { python3 "$checker" --grammar "$@" 2>"$box/v1check.err"; }
fieldof() { sed -n "s/.* $1=\\([^ ]*\\).*/\\1/p" <<<"$2"; }
# lineof SCRIPT: the last pkg-phases line of that script.
lineof() { { grep " script=$1 " "$box/$P" 2>/dev/null || true; } | tail -1; }
want_fields() { # CASE LINE KEY=VALUE...: each field equals its value
	local id=$1 l=$2 kv k v
	shift 2
	for kv in "$@"; do
		k=${kv%%=*} v=${kv#*=}
		if [[ "$(fieldof "$k" "$l")" != "$v" ]]; then not_ok "$id: $k=$(fieldof "$k" "$l"), want $v (line: ${l:0:160})"; return 1; fi
	done
	return 0
}
victim_fresh() { printf 'victim\n' >"$box/victim"; chmod 0600 "$box/victim"; }
v1_record() { # NAME BOOT OWN-LINES...: a v1 record with the common fields
	local name=$1 b=$2
	shift 2
	printf 'schema=olivares.pkg-%s/v1\nboot=%s\npid=4242\npid_start=1\nppid=4200\nppid_start=1\nbatch=none\nmaintscript=prerm\nformat=deb\nold=unknown\nnew=unknown\ncontract=upgrade-dispatch-v1\n' "$name" "$b"
	printf '%s\n' "$@"
}

case_id="r4 d1: under <data-dir> root does only the one mv -T (a link planted at install-manifest.json.new) (guard)"
new_box r4-d1
ship deb
victim_fresh
ln -s "$box/victim" "$box/var/lib/olivares/install-manifest.json.new"
run new postinstall.sh configure ''
under="$(grep -E "^(rm|chown|chmod) .*$box/var/lib/olivares/" "$box/calls" || true)"
mvs="$(grep -E "^mv .*$box/var/lib/olivares/" "$box/calls" || true)"
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif ! victim_ok; then not_ok "$case_id: the victim behind install-manifest.json.new was written or re-moded"
elif [[ -n "$under" ]]; then not_ok "$case_id: root path operations under the data directory: $(tr '\n' ';' <<<"$under" | sed "s|$box||g")"
elif [[ "$(grep -c . <<<"$mvs")" -ne 1 || "$mvs" != "mv -T "*"$box/$M" ]]; then not_ok "$case_id: want exactly one mv -T onto the final name, got: $(tr '\n' ';' <<<"$mvs" | sed "s|$box||g")"
else ok "$case_id: one mv -T; nothing else under the data directory; the planted link untouched"; fi

# The migration read of an older package's manifest: install-state absent.
grep_shim() {
	cat >"$box/bin/grep" <<'STUB'
#!/bin/sh
for a in "$@"; do case "$a" in */var/lib/olivares/install-manifest.json)
  printf 'grep %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
  if [ -f "$OLIVARES_TEST_BOX/state/fifo-before-grep" ]; then
    /usr/bin/rm -f "$OLIVARES_TEST_BOX/state/fifo-before-grep" "$a"; mkfifo "$a"
  fi ;;
esac; done
exec /usr/bin/grep "$@"
STUB
	/usr/bin/chmod 0755 "$box/bin/grep"
}
case_id="r4 d2 (J7): a FIFO swapped in before the migration read finishes within the bound"
new_box r4-j7
seed_install deb old || true
grep_shim
: >"$box/state/fifo-before-grep"
start=$SECONDS
run_timeout=30
run new postinstall.sh configure 26.9.0
run_timeout=
took=$((SECONDS - start))
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc after ${took}s (124: killed while blocked on the read)"
elif [[ "$took" -ge 10 ]]; then not_ok "$case_id: took ${took}s"
elif ! grep -q ' result=old-manifest-unread ' "$box/$P"; then not_ok "$case_id: no old-manifest-unread receipt"
elif ! grep -q '"user_created": false' "$box/$M" || ! grep -q '"group_created": false' "$box/$M"; then not_ok "$case_id: the values are not both false"
else ok "$case_id: finished in ${took}s; old-manifest-unread; both values false"; fi

case_id="r4 d2: group_created and user_created come from the root-owned install-state"
new_box r4-install-state
ship deb
mkdir -p "$box/var/lib/olivares-package"
v1_record install-state "$host_boot" group_created=true user_created=true >"$box/var/lib/olivares-package/install-state"
printf '{"user_created": false, "group_created": false}\n' >"$box/$M"
grep_shim
run new postinstall.sh configure 26.10.0
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif grep -q "^grep .*$box/$M" "$box/calls"; then not_ok "$case_id: the service-owned manifest was read although install-state exists"
elif ! grep -q '"user_created": true' "$box/$M" || ! grep -q '"group_created": true' "$box/$M"; then not_ok "$case_id: the manifest does not carry install-state's values"
elif ! v1ok "$box/var/lib/olivares-package/install-state"; then not_ok "$case_id: install-state is not a v1 record: $(cat "$box/v1check.err")"
else ok "$case_id: values from install-state; the manifest never read"; fi

case_id="r4 d3 (J8): a backdated copy of the manifest after a downgrade is not a fresh marker"
new_box r4-j8
seed_install rpm new || true
set_state enabled active
run old postinstall.sh 2; run new preremove.sh 1; run new postremove.sh 1
cp -p "$box/$M" "$box/m.copy"; /usr/bin/rm -f "$box/$M"; cp "$box/m.copy" "$box/$M"; touch -d '2000-01-01' "$box/$M"
run new postinstall.sh 2; r1=$last_rc; post_out=$last_out
run old preremove.sh 1; r2=$last_rc
run old postremove.sh 1; r3=$last_rc
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$(pending_field condition)" != replaced-package-may-disable ]] || ! grep -q 'predates the upgrade contract' "$post_out.stdout"; then
	not_ok "$case_id: no pending record or warning — the backdated copy read as 'honors the contract'"
else defect "$case_id: old %preun 'disable --now' measured (enabled/active -> $(state)); recovery: pending replaced-package-may-disable"; fi

case_id="r4 d3 (J8b): a copy of the placed manifest with its exact mtime (new inode) is not a fresh marker"
# Only the inode tells this copy from the file root placed: the mtime is the recorded one.
new_box r4-j8b
seed_install rpm new || true
set_state enabled active
cp -p "$box/$M" "$box/m.copy"; /usr/bin/rm -f "$box/$M"; /usr/bin/mv "$box/m.copy" "$box/$M"
run new postinstall.sh 2; r1=$last_rc; post_out=$last_out
wrote="$(pending_field condition)"
run new preremove.sh 1; r2=$last_rc
run new postremove.sh 1; r3=$last_rc
if [[ "$r1$r2$r3" != 000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3"
elif [[ "$wrote" != replaced-package-may-disable ]] || ! grep -q 'predates the upgrade contract' "$post_out.stdout"; then
	not_ok "$case_id: the %post read the copy as 'honors the contract' (pending '$wrote')"
elif [[ -e "$box/run/olivares.pkg-pending" || "$(state)" != enabled/active ]]; then not_ok "$case_id: the current %preun did not withdraw the record, or the state changed ($(state))"
else ok "$case_id: recorded and warned, then withdrawn by the current %preun; state kept"; fi

for fmt in deb rpm apk; do
	case_id="r4 d4 $fmt: /var/lib/olivares-package survives the upgrade and goes with the final removal"
	new_box "r4-d4-$fmt"
	seed_install "$fmt" new || continue
	case "$fmt" in
	deb) run new preremove.sh upgrade 2.0.0; run new postremove.sh upgrade 2.0.0; run new postinstall.sh configure 1.0.0 ;;
	rpm) run new postinstall.sh 2; run new preremove.sh 1; run new postremove.sh 1 ;;
	apk) run new apk-preupgrade.sh 2.0.0-r0 1.0.0-r0; run new postinstall.sh 2.0.0-r0 1.0.0-r0 ;;
	esac
	kept=no; [[ -d "$box/var/lib/olivares-package" ]] && kept=yes
	case "$fmt" in
	deb) run new preremove.sh remove; unship; run new postremove.sh remove
		[[ -d "$box/var/lib/olivares-package" ]] || kept=removed-by-remove
		run new postremove.sh purge ;;
	rpm) run new preremove.sh 0; unship; run new postremove.sh 0 ;;
	apk) run new preremove.sh 1.0.0-r0; unship; run new postremove.sh 1.0.0-r0 ;;
	esac
	if [[ "$kept" != yes ]]; then not_ok "$case_id: after the upgrade (and, for deb, remove before purge): $kept"
	elif [[ -e "$box/var/lib/olivares-package" ]]; then not_ok "$case_id: still there after the final removal: $(ls -A "$box/var/lib/olivares-package" | tr '\n' ' ')"
	elif [[ ! -d "$box/var/lib/olivares" ]]; then not_ok "$case_id: the product data directory was removed"
	else ok "$case_id: kept on upgrade$([[ $fmt == deb ]] && echo ' and remove'); removed on the final removal; data kept"; fi
done

case_id="r4 d5 (F2): a failed marker write refuses by name"
new_box r4-f2
ship deb
mkdir -p "$box/var/lib/olivares-package/scripts-contract"
run new postinstall.sh configure ''
if refused_as "$case_id" marker-write-failed; then ok "$case_id: refused marker-write-failed with its receipt"; fi
case_id="r4 d5 (F3): a failed chown of the staged manifest refuses by name"
new_box r4-f3
ship deb
: >"$box/state/chown-fails-staged"
run new postinstall.sh configure ''
if refused_as "$case_id" manifest-staging-failed; then
	if [[ -e "$box/$M" ]]; then not_ok "$case_id: the manifest was put in place without its group"
	else ok "$case_id: refused manifest-staging-failed; nothing renamed"; fi
fi

case_id="r4 v1: every pkg-phases line is the v1 line in its fixed key order"
new_box r4-lines
seed_install deb new || true
printf '26.10.0\n' >"$box/state/dpkg-version"
run new preremove.sh upgrade 26.10.1; run new postremove.sh upgrade 26.10.1; run new postinstall.sh configure 26.10.0
run new postinstall.sh frobnicate
n_lines=$(grep -c . "$box/$P" || true)
if [[ "$n_lines" -lt 4 ]]; then not_ok "$case_id: $n_lines lines"
elif ! v1ok "$box/$P"; then not_ok "$case_id: $(head -3 "$box/v1check.err" | tr '\n' ' ')"
else ok "$case_id: $n_lines lines, all v1"; fi

case_id="r4 v1: every key=value record begins with schema= and carries the common fields"
new_box r4-records
seed_install deb new || true
set_state enabled active
printf '26.10.0\n' >"$box/state/dpkg-version"
run new preremove.sh upgrade 26.10.1
cp "$box/run/olivares.pkg-upgrade-state" "$box/rec.upgrade-state" 2>/dev/null || : >"$box/rec.upgrade-state"
run new preremove.sh deconfigure in-favour other 1.0
cp "$box/run/olivares.pkg-deconfigured" "$box/rec.deconfigured" 2>/dev/null || : >"$box/rec.deconfigured"
appliance
run new postinstall.sh configure 26.10.0
cp "$box/run/olivares.pkg-pending" "$box/rec.pending" 2>/dev/null || : >"$box/rec.pending"
cp "$box/var/lib/olivares-package/install-state" "$box/rec.install-state" 2>/dev/null || : >"$box/rec.install-state"
if ! v1ok "$box"/rec.*; then not_ok "$case_id: $(tr '\n' ' ' <"$box/v1check.err" | sed "s|$box||g" | cut -c1-240)"
else ok "$case_id: upgrade-state, deconfigured, pending and install-state are v1 records"; fi

case_id="r4 v1: ppid_start is field 22 counted after the last ')' of a parent named 'sh) (x'"
new_box r4-pidstart
ship deb
mkdir -p "$box/weird"
cp "$(readlink -f "$(command -v sh)")" "$box/weird/sh) (x"
cat >"$box/weird/runner" <<'STUB'
#!/bin/sh
exec "$(dirname "$0")/sh) (x" -c 'cat /proc/$$/stat >"$OLIVARES_TEST_BOX/parent.stat"; sh "$@"; exit $?' runner "$@"
STUB
chmod 0755 "$box/weird/runner"
run_sh="$box/weird/runner"
run new postinstall.sh configure ''
run_sh=
want_ppid_start="$(python3 -c 'import sys; s=open(sys.argv[1]).read(); print(s[s.rindex(")")+1:].split()[19])' "$box/parent.stat" 2>/dev/null || echo none)"
l="$(lineof postinstall)"
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif ! grep -q ') (x)' "$box/parent.stat" 2>/dev/null; then not_ok "$case_id: the parent's comm is not 'sh) (x'"
elif want_fields "$case_id" "$l" "ppid_start=$want_ppid_start"; then
	if [[ "$(fieldof pid_start "$l")" != [0-9]* ]]; then not_ok "$case_id: pid_start=$(fieldof pid_start "$l")"
	else ok "$case_id: ppid_start=$want_ppid_start; pid_start=$(fieldof pid_start "$l")"; fi
fi

case_id="r4 v1: an unreadable /proc gives pid_start, ppid_start and boot unknown"
new_box r4-noproc
ship deb
/usr/bin/rm -f "$box/proc"; mkdir "$box/proc"
run new postinstall.sh configure ''
l="$(lineof postinstall)"
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif want_fields "$case_id" "$l" pid_start=unknown ppid_start=unknown boot=unknown; then
	if ! v1ok "$box/$P"; then not_ok "$case_id: $(cat "$box/v1check.err")"; else ok "$case_id: unknown, and still a v1 line"; fi
fi

case_id="r4 v1: format, old and new come only from their listed sources"
new_box r4-versions
seed_install deb new || true
printf '26.10.0\n' >"$box/state/dpkg-version"
run new preremove.sh upgrade 26.10.1
l1="$(lineof preremove)"
run new postinstall.sh configure 26.10.0
l2="$(lineof postinstall)"
run new postinstall.sh configure 26.10.0-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
l3="$(lineof postinstall)"
new_box r4-versions-apk
seed_install apk new || true
run new apk-preupgrade.sh 2.0.0-r0 1.0.0-r0
l4="$(lineof apk-preupgrade)"
run new postinstall.sh 2.0.0-r0 1.0.0-r0
l5="$(lineof postinstall)"
new_box r4-versions-rpm
seed_install rpm new || true
run new postinstall.sh 2
l6="$(lineof postinstall)"
if want_fields "$case_id (deb prerm upgrade)" "$l1" format=deb old=26.10.0 new=26.10.1 &&
	want_fields "$case_id (deb postinst configure)" "$l2" format=deb old=26.10.0 new=unknown &&
	want_fields "$case_id (a 44-character version)" "$l3" old=unknown &&
	want_fields "$case_id (apk pre-upgrade)" "$l4" format=apk new=2.0.0-r0 old=1.0.0-r0 &&
	want_fields "$case_id (apk post-upgrade)" "$l5" format=apk new=2.0.0-r0 old=1.0.0-r0 &&
	want_fields "$case_id (rpm %post)" "$l6" format=rpm old=unknown new=unknown; then
	ok "$case_id: deb prerm/postinst, apk pre/post-upgrade, rpm and an over-long version"
fi

case_id="r4 v1: stopped and prior on each stop line"
new_box r4-stop-a; seed_install deb new || true; set_state enabled active
run new preremove.sh deconfigure in-favour other 1.0; la="$(lineof preremove)"
new_box r4-stop-b; seed_install deb new || true; set_state enabled inactive
run new preremove.sh deconfigure in-favour other 1.0; lb="$(lineof preremove)"
new_box r4-stop-c; seed_install deb new || true; set_state enabled activating
run new preremove.sh deconfigure in-favour other 1.0; lc="$(lineof preremove)"
new_box r4-stop-d; seed_install deb new || true; set_state enabled active
run new preremove.sh remove; ld="$(lineof preremove)"
new_box r4-stop-e; seed_install apk new || true; set_state disabled active
run new apk-preupgrade.sh 2.0.0-r0 1.0.0-r0; le="$(lineof apk-preupgrade)"
new_box r4-stop-f; seed_install apk new || true; set_state disabled inactive
run new apk-preupgrade.sh 2.0.0-r0 1.0.0-r0; lf="$(lineof apk-preupgrade)"
run new postinstall.sh 2.0.0-r0 1.0.0-r0; lg="$(lineof postinstall)"
if want_fields "$case_id (deconfigure active)" "$la" stopped=yes prior=active &&
	want_fields "$case_id (deconfigure inactive)" "$lb" stopped=no prior=inactive &&
	want_fields "$case_id (deconfigure activating)" "$lc" stopped=no prior=unknown &&
	want_fields "$case_id (prerm remove active)" "$ld" stopped=yes prior=active &&
	want_fields "$case_id (apk stop active)" "$le" stopped=yes prior=active &&
	want_fields "$case_id (apk inactive)" "$lf" stopped=no prior=inactive &&
	want_fields "$case_id (postinstall)" "$lg" stopped=n/a prior=n/a; then
	ok "$case_id: yes/no with active, inactive, unknown; n/a where nothing stops"
fi

hexid=0123456789abcdef0123456789abcdef
for how in valid link malformed absent off; do
	case_id="r4 v1: batch= with the bridge's file $how"
	new_box "r4-batch-$how"
	ship deb
	[[ "$how" == off ]] || appliance
	mkdir -p "$box/run/olivares-bridge"
	case "$how" in
	valid | off) printf 'v1 %s 7\n' "$hexid" >"$box/run/olivares-bridge/batch" ;;
	link) printf 'v1 %s 7\n' "$hexid" >"$box/elsewhere-batch"; ln -s "$box/elsewhere-batch" "$box/run/olivares-bridge/batch" ;;
	malformed) printf 'v1 %s 07\n' "$hexid" >"$box/run/olivares-bridge/batch" ;;
	esac
	run new postinstall.sh configure ''
	case "$how" in valid) want="$hexid.7" ;; off) want=none ;; *) want=absent ;; esac
	if want_fields "$case_id" "$(lineof postinstall)" "batch=$want"; then ok "$case_id: batch=$want"; fi
done

case_id="r4 v1: maintscript= from DPKG_MAINTSCRIPT_NAME on deb, n/a on rpm and apk"
new_box r4-maint; ship deb
run_env=DPKG_MAINTSCRIPT_NAME=postinst; run new postinstall.sh configure ''; m1="$(lineof postinstall)"
run_env=DPKG_MAINTSCRIPT_NAME=bogus; run new postinstall.sh configure ''; m2="$(lineof postinstall)"
new_box r4-maint-rpm; ship rpm
run_env=DPKG_MAINTSCRIPT_NAME=postinst; run new postinstall.sh 1; m3="$(lineof postinstall)"
new_box r4-maint-apk; ship apk
run new postinstall.sh 1.0.0-r0; m4="$(lineof postinstall)"
run_env=
if want_fields "$case_id (deb postinst)" "$m1" maintscript=postinst &&
	want_fields "$case_id (deb bogus)" "$m2" maintscript=unknown &&
	want_fields "$case_id (rpm)" "$m3" maintscript=n/a &&
	want_fields "$case_id (apk)" "$m4" maintscript=n/a; then ok "$case_id"; fi

refuse 'triggered is an unknown action (r4)' systemd postinstall.sh unknown-action triggered /usr/lib/olivares

case_id="r4 v1: a failed pkg-phases append is loud, and the package action goes on"
new_box r4-append-fails
ship deb
mkdir -p "$box/$P"
run new postinstall.sh configure ''
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif ! grep -Fq "receipt-not-written $box/$P" "$last_out.stderr"; then not_ok "$case_id: stderr lacks 'receipt-not-written <path>': $(tr '\n' ' ' <"$last_out.stderr" | cut -c1-160)"
elif [[ ! -f "$box/$M" ]]; then not_ok "$case_id: the postinst stopped"
else ok "$case_id: receipt-not-written printed; postinst completed"; fi

case_id="r4 v1: a failed record write is loud, and the next script treats the record as absent"
new_box r4-record-fails
seed_install deb new || true
set_state enabled active
printf '26.10.0\n' >"$box/state/dpkg-version"
mkdir -p "$box/run/olivares.pkg-upgrade-state.tmp" "$box/run/olivares.pkg-deconfigured.tmp"
run new preremove.sh upgrade 26.10.1; r1=$last_rc; e1="$last_out.stderr"
run new postremove.sh upgrade 26.10.1
run new postinstall.sh configure 26.10.0; r2=$last_rc
pend_after_upgrade="$(pending_field condition)"
run new preremove.sh deconfigure in-favour other 1.0; r3=$last_rc; e3="$last_out.stderr"
run new postinstall.sh abort-deconfigure in-favour other 1.0; r4=$last_rc
if [[ "$r1$r2$r3$r4" != 0000 ]]; then not_ok "$case_id: exit codes $r1/$r2/$r3/$r4"
elif ! grep -q 'record-not-written upgrade-state' "$e1" || ! grep -q 'record-not-written deconfigured' "$e3"; then not_ok "$case_id: a failed record write was silent"
elif ! grep -q ' result=record-not-written:upgrade-state ' "$box/$P"; then not_ok "$case_id: no record-not-written receipt"
elif [[ -n "$pend_after_upgrade" ]]; then not_ok "$case_id: the absent upgrade record was read as legacy: $pend_after_upgrade"
elif grep -q '^systemctl start olivares' "$box/calls" || [[ "$(state)" != enabled/inactive ]]; then not_ok "$case_id: started without a deconfigure record ($(state))"
else ok "$case_id: loud; absent record means no legacy and no start"; fi

case_id="r4 v1: a record past 1024 bytes is replaced by the short record-too-large record"
new_box r4-too-large
ship deb
appliance
set_state "$(printf 'x%.0s' $(seq 1 1100))" active
run new postinstall.sh configure ''
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif [[ "$(cat "$box/run/olivares.pkg-pending" 2>/dev/null)" != $'schema=olivares.pkg-pending/v1\nresult=record-too-large' ]]; then
	not_ok "$case_id: pending is $(wc -c <"$box/run/olivares.pkg-pending" 2>/dev/null || echo 0) bytes: $(head -c 80 "$box/run/olivares.pkg-pending" 2>/dev/null | tr '\n' ' ')"
elif ! v1ok "$box/run/olivares.pkg-pending"; then not_ok "$case_id: $(cat "$box/v1check.err")"
else ok "$case_id: the short record, and it parses"; fi

case_id="r4 v1: upgrade-state and deconfigured are staged as .tmp and moved into place"
new_box r4-staged
seed_install deb new || true
set_state enabled active
printf '26.10.0\n' >"$box/state/dpkg-version"
run new preremove.sh upgrade 26.10.1
run new preremove.sh deconfigure in-favour other 1.0
if ! grep -Eq "^mv .*$box/run/olivares.pkg-upgrade-state.tmp $box/run/olivares.pkg-upgrade-state\$" "$box/calls"; then not_ok "$case_id: upgrade-state was not moved into place from .tmp"
elif ! grep -Eq "^mv .*$box/run/olivares.pkg-deconfigured.tmp $box/run/olivares.pkg-deconfigured\$" "$box/calls"; then not_ok "$case_id: deconfigured was not moved into place from .tmp"
else ok "$case_id"; fi

case_id="r4 v1: an append that would pass 65536 bytes rotates pkg-phases to pkg-phases.1"
new_box r4-rotate
ship deb
for i in $(seq 1 655); do printf '%099d\n' 0; done >"$box/$P"
before=$(wc -c <"$box/$P")
run new postinstall.sh configure ''
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
elif [[ ! -f "$box/$P.1" || "$(wc -c <"$box/$P.1")" -ne "$before" ]]; then not_ok "$case_id: pkg-phases.1 is not the $before-byte log"
elif [[ "$(grep -c . "$box/$P")" -ne 1 ]] || ! v1ok "$box/$P"; then not_ok "$case_id: pkg-phases after the rotation: $(wc -l <"$box/$P") lines"
else ok "$case_id: $before bytes moved to pkg-phases.1; one new v1 line"; fi

for how in other-boot unknown-schema; do
	case_id="r4 v1: a deconfigure record ($how) is never honored"
	new_box "r4-stale-$how"
	seed_install deb new || true
	set_state enabled inactive
	case "$how" in
	other-boot) v1_record deconfigured 00000000-0000-4000-8000-000000000000 active=active >"$box/run/olivares.pkg-deconfigured" ;;
	unknown-schema) v1_record deconfigured "$host_boot" active=active | sed '1s|/v1$|/v2|' >"$box/run/olivares.pkg-deconfigured" ;;
	esac
	run new postinstall.sh abort-deconfigure in-favour other 1.0
	if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc"
	elif grep -q '^systemctl start olivares' "$box/calls"; then not_ok "$case_id: started from it"
	else ok "$case_id: no start"; fi
done

# --- 7. boundary: no product script touches boot artifacts --------------------------
# /boot as a path component: /proc/sys/kernel/random/boot_id (r4, boot= of the v1
# records) is not a path under /boot.
if grep -nE '(^|[^A-Za-z0-9_./-])/boot($|/|[^A-Za-z0-9_.-])|update-initramfs|dracut' "$new_dir/preremove.sh" "$new_dir/postinstall.sh" \
	"$new_dir/postremove.sh" "$new_dir/apk-preupgrade.sh"; then
	not_ok 'boundary: a product script names /boot, update-initramfs or dracut'
elif grep -rqE '^(update-initramfs|dracut)' "$scratch"/*/calls; then
	not_ok 'boundary: a product script called update-initramfs or dracut'
else
	ok 'boundary: no /boot path, update-initramfs or dracut in the scripts or their calls'
fi

printf '%s: %d cells, %d failed, %d old->new defects measured (reported, not counted green)\n' \
	"$me" "$n" "$failures" "$defects"
[[ "$failures" -eq 0 ]] || exit 1
exit 0
