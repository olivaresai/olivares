#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Custody of the install record, as the Arch Linux scriptlets handle it. The same cells as
# scripts/test-package-manifest-custody.sh, run the way pacman runs them: nFPM (v2.47.0
# arch/arch.go writeScripts) copies each scriptlet body into a function of .INSTALL, and
# pacman sources .INSTALL and calls post_install NEW, post_upgrade NEW OLD or pre_remove OLD.
# The scriptlets are rewritten into a throwaway root by one fixed prefix table (checked to
# change nothing else); account tools, systemctl and the uninstall engine are recording
# stubs, and the engine stub refuses a record the way localinstall.Load does. No root, no
# container, and no host path outside the throwaway root is touched.
#
# Cells, each against an unrelated victim file where one applies:
#   W  the write: a link, a FIFO or a directory at the record's name, a link planted again
#      just before root's final operation, and a hard link swapped in after root placed the
#      record; the staging directory's safety, filesystem and rename refusals.
#   M  the migration: account-ownership claims in a record the service account can replace
#      are never imported; root's own record carries them.
#   R  the readers: when the engine refuses the record (link, FIFO, directory, untrusted),
#      pre_remove still completes with the safe stop and touches no victim.
# Exit 0: every cell holds. Exit 1: a measured cell failed. Exit 2: could not look.
set -euo pipefail
LC_ALL=C
export LC_ALL
me=test-archlinux-record-custody

could_not_look() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}
root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
nfpm="$root/packaging/nfpm"
for tool in bash python3 mktemp mkfifo timeout stat ln; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
for name in archlinux-postinstall.sh archlinux-preremove.sh archlinux-postremove.sh; do
	[[ -f "$nfpm/$name" ]] || could_not_look "missing $nfpm/$name"
done
scratch_parent="${TMPDIR:-}"
[[ "$scratch_parent" == /* && -d "$scratch_parent" ]] || could_not_look 'TMPDIR must be an existing absolute directory'
scratch="$(mktemp -d "$scratch_parent/arch-record-custody.XXXXXX")"
cleanup() {
	chmod -R u+w "$scratch" 2>/dev/null || true
	case "$scratch" in "$scratch_parent"/arch-record-custody.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT INT TERM

cells=0 failures=0
ok() { cells=$((cells + 1)); printf 'ok %d - %s\n' "$cells" "$*"; }
not_ok() { cells=$((cells + 1)); failures=$((failures + 1)); printf 'not ok %d - %s\n' "$cells" "$*"; }

# rewrite SRC DST BOX: only the fixed product-path prefixes move under BOX.
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

M=var/lib/olivares/install-manifest.json
# new_box NAME [FORMAT]: a fresh throwaway root with stubs, shipped for FORMAT (deb|apk).
new_box() {
	box="$scratch/$1"
	mkdir -p "$box/bin" "$box/state" "$box/run" "$box/hooks" "$box/usr/lib/olivares" "$box/usr/bin" \
		"$box/var/lib/olivares" "$box/etc/olivares" "$box/var/log" "$box/usr/share/olivares"
	chmod 0755 "$box" "$box/var" "$box/var/lib"
	: >"$box/calls"
	ln -s /proc "$box/proc"
	printf 'disabled\n' >"$box/state/enabled"
	printf 'inactive\n' >"$box/state/active"
	case "${2:-deb}" in
	deb)
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
	cat >"$box/bin/systemctl" <<'STUB'
#!/bin/sh
printf 'systemctl %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
case "$1" in
  is-enabled) cat "$OLIVARES_TEST_BOX/state/enabled"; exit 1 ;;
  is-active) cat "$OLIVARES_TEST_BOX/state/active"; exit 3 ;;
esac
exit 0
STUB
	for tool in rc-service rc-update update-initramfs dracut; do
		printf '#!/bin/sh\nprintf "%s %%s\\n" "$*" >>"$OLIVARES_TEST_BOX/calls"\nexit 0\n' "$tool" >"$box/bin/$tool"
	done
	# Accounts: a state file per identity. The service account resolves to the test user.
	cat >"$box/bin/getent" <<'STUB'
#!/bin/sh
case "$1" in group|passwd) [ -e "$OLIVARES_TEST_BOX/state/account-$1" ] ;; *) exit 2 ;; esac
STUB
	for tool in groupadd addgroup; do
		printf '#!/bin/sh\nprintf "%s %%s\\n" "$*" >>"$OLIVARES_TEST_BOX/calls"\n: >"$OLIVARES_TEST_BOX/state/account-group"\n' "$tool" >"$box/bin/$tool"
	done
	for tool in useradd adduser; do
		printf '#!/bin/sh\nprintf "%s %%s\\n" "$*" >>"$OLIVARES_TEST_BOX/calls"\n: >"$OLIVARES_TEST_BOX/state/account-passwd"\n' "$tool" >"$box/bin/$tool"
	done
	printf '#!/bin/sh\ncase "$1" in -u) exec /usr/bin/id -u ;; -g) exec /usr/bin/id -g ;; esac\nexit 1\n' >"$box/bin/id"
	cat >"$box/bin/dpkg" <<'STUB'
#!/bin/sh
[ "${1-}" = --compare-versions ] && [ -x /usr/bin/dpkg ] && exec /usr/bin/dpkg "$@"
printf 'dpkg %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
exit 1
STUB
	printf '#!/bin/sh\nprintf "dpkg-query %%s\\n" "$*" >>"$OLIVARES_TEST_BOX/calls"\nexit 1\n' >"$box/bin/dpkg-query"
	# The uninstall engine: refuses as localinstall.Load does (a link, not a regular file,
	# or a record the owner check would refuse), else performs the service effect only.
	cat >"$box/usr/bin/olivares" <<'STUB'
#!/bin/sh
printf 'olivares %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
[ "$1" = uninstall ] || exit 0
m="$OLIVARES_TEST_BOX/var/lib/olivares/install-manifest.json"
if [ -L "$m" ] || [ ! -f "$m" ] || [ -f "$OLIVARES_TEST_BOX/state/record-untrusted" ]; then
  echo "local install manifest must be a regular file owned by root, not a link: $m" >&2
  exit 2
fi
case "$2" in --preserve|--purge) systemctl disable --now olivares ;; esac
exit 0
STUB
	# Observing shadows: each logs, fires a one-shot hook, then runs the real tool.
	# window-<name> fires after root removes <name> or just before root renames onto it;
	# after-<name> fires after root's rename onto <name>, or before root chowns or chmods it.
	cat >"$box/bin/olivares-test-hook" <<'STUB'
#!/bin/sh
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
  echo "mv: unrecognized option: T" >&2; exit 1
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
u=$(/usr/bin/id -u); g=$(/usr/bin/id -g)
for a in "$@"; do
  shift
  case "$a" in root:*|olivares:*|*:olivares) a="$u:$g" ;; esac
  set -- "$@" "$a"
done
exec /usr/bin/chown "$@"
STUB
	cat >"$box/bin/chmod" <<'STUB'
#!/bin/sh
for a in "$@"; do case "$a" in -*|[0-7][0-7][0-7]|[0-7][0-7][0-7][0-7]) ;; */*) olivares-test-hook after "$a" ;; esac; done
printf 'chmod %s\n' "$*" >>"$OLIVARES_TEST_BOX/calls"
exec /usr/bin/chmod "$@"
STUB
	cat >"$box/bin/stat" <<'STUB'
#!/bin/sh
if [ -f "$OLIVARES_TEST_BOX/state/stage-other-fs" ] && [ "$1 $2" = "-c %d" ]; then
  case "$3" in */var/lib/olivares-package) echo $(( $(/usr/bin/stat -c %d "$3") + 1 )); exit 0 ;; esac
fi
exec /usr/bin/stat "$@"
STUB
	chmod 0755 "$box/bin/"* "$box/usr/bin/olivares"
	printf 'victim\n' >"$box/victim"
	chmod 0600 "$box/victim"
}
plant_hook() { printf '%s\n' "$3" >"$box/hooks/$1-$2"; }

# run FUNCTION ARGS...: pacman's call of one scriptlet function from .INSTALL, assembled as
# nFPM v2.47.0 writes it from the rewritten scriptlets; rc in $last_rc.
run_timeout=30
run() {
	local fn=$1 k name file
	shift
	mkdir -p "$box/out" "$box/sut"
	k=$(($(find "$box/out" -name '*.rc' | wc -l) + 1))
	: >"$box/sut/.INSTALL"
	for name in post_install post_upgrade pre_remove post_remove; do
		case "$name" in
		post_install | post_upgrade) file=archlinux-postinstall.sh ;;
		pre_remove) file=archlinux-preremove.sh ;;
		post_remove) file=archlinux-postremove.sh ;;
		esac
		rewrite "$nfpm/$file" "$box/sut/$file" "$box" || could_not_look "could not stage $file"
		{ printf 'function %s() {\n' "$name"; cat "$box/sut/$file"; printf '\n}\n\n'; } >>"$box/sut/.INSTALL"
	done
	printf '== %s %s\n' "$fn" "$*" >>"$box/calls"
	set +e
	env OLIVARES_TEST_BOX="$box" PATH="$box/bin:/usr/bin:/bin" timeout "$run_timeout" \
		bash -c '. "$1"; fn=$2; shift 2; "$fn" "$@"' _ "$box/sut/.INSTALL" "$fn" "$@" \
		>"$box/out/$k.stdout" 2>"$box/out/$k.stderr"
	last_rc=$?
	set -e
	printf '%s\n' "$last_rc" >"$box/out/$k.rc"
	last_out="$box/out/$k"
}
victim_ok() { [[ "$(cat "$box/victim")" == victim && "$(stat -c '%a' "$box/victim")" == 600 ]]; }
record_ok() { [[ ! -L "$box/$M" && -f "$box/$M" ]] && grep -q 'olivares.ai/local-install/v2' "$box/$M"; }
err() { tr '\n' ' ' <"$last_out.stderr" | cut -c1-200; }
installed() { : >"$box/state/account-group"; : >"$box/state/account-passwd"; }

# --- W: the write ---------------------------------------------------------------------------
case_id="W1 a link to a victim at the record's name"
new_box w1; installed
ln -s "$box/victim" "$box/$M"
run post_install 26.10.0-1
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc $(err)"
elif ! victim_ok; then not_ok "$case_id: the victim was rewritten or re-moded (mode $(stat -c %a "$box/victim"))"
elif ! record_ok; then not_ok "$case_id: the record is not a regular file after the install"
else ok "$case_id: the victim untouched; the link replaced by the record"; fi

case_id="W2 a link planted again just before root's final operation on the name"
new_box w2; installed
ln -s "$box/victim" "$box/$M"
plant_hook window install-manifest.json "/usr/bin/rm -f \"\$1\"; ln -s '$box/victim' \"\$1\""
run post_install 26.10.0-1
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc $(err)"
elif ! victim_ok; then not_ok "$case_id: the victim was rewritten or re-moded (mode $(stat -c %a "$box/victim"))"
elif ! record_ok; then not_ok "$case_id: the record is not a regular file after the install"
else ok "$case_id: the victim untouched; the name replaced without following the link"; fi

case_id="W3 a FIFO at the record's name does not block the install"
new_box w3; installed
mkfifo "$box/$M"
( sleep 5; while [ -p "$box/$M" ]; do exec 3>"$box/$M"; exec 3>&-; done ) & unblock=$!
start=$SECONDS
run post_install 26.10.0-1
took=$((SECONDS - start))
kill "$unblock" 2>/dev/null || true; wait "$unblock" 2>/dev/null || true
if [[ "$took" -ge 5 ]]; then not_ok "$case_id: the install waited ${took}s on the FIFO (rc=$last_rc)"
elif [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc $(err)"
elif ! record_ok; then not_ok "$case_id: the record is not a regular file after the install"
else ok "$case_id: finished in ${took}s; the FIFO replaced by the record"; fi

case_id="W4 a directory at the record's name refuses by name and stays empty"
new_box w4; installed
mkdir -p "$box/$M"
run post_install 26.10.0-1
if [[ "$last_rc" -eq 0 ]]; then not_ok "$case_id: rc=0"
elif ! grep -Fq 'refused: manifest-destination-not-replaceable' "$last_out.stderr" || ! grep -Fq 'move it aside' "$last_out.stderr"; then
	not_ok "$case_id: no named refusal with the instruction: $(err)"
elif [[ ! -d "$box/$M" || -n "$(ls -A "$box/$M")" ]]; then not_ok "$case_id: the directory was changed"
else ok "$case_id: refused by name, with the instruction; the directory untouched"; fi

case_id="W5 a hard link to a victim swapped in after root placed the record"
new_box w5; installed
plant_hook after install-manifest.json "/usr/bin/rm -f \"\$1\"; ln '$box/victim' \"\$1\""
run post_install 26.10.0-1
late="$(awk '/^hook after .*install-manifest.json$/{f=1; next} f' "$box/calls" | grep -E "^(chown|chmod|rm|mv) .*$box/$M\$" || true)"
if ! grep -q '^hook after ' "$box/calls"; then not_ok "$case_id: root never placed the record by a rename or chown the hook could observe (rc=$last_rc)"
elif [[ -n "$late" ]]; then not_ok "$case_id: root operated on the name after placing it: $(tr '\n' ';' <<<"$late" | sed "s|$box||g")"
elif ! victim_ok; then not_ok "$case_id: the victim was changed (mode $(stat -c %a "$box/victim"))"
elif [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc $(err)"
else ok "$case_id: no root path operation on the name after the rename; the victim untouched"; fi

for how in link writable file parent; do
	case_id="W6 an unsafe /var/lib/olivares-package ($how) refuses by name before any write"
	new_box "w6-$how"; installed
	case "$how" in
	link) mkdir -p "$box/elsewhere"; ln -s "$box/elsewhere" "$box/var/lib/olivares-package" ;;
	writable) mkdir -p "$box/var/lib/olivares-package"; chmod 0777 "$box/var/lib/olivares-package" ;;
	file) : >"$box/var/lib/olivares-package" ;;
	parent) chmod 0777 "$box/var/lib" ;;
	esac
	run post_install 26.10.0-1
	if [[ "$last_rc" -eq 0 ]]; then not_ok "$case_id: rc=0"
	elif ! grep -Fq 'refused: marker-directory-unsafe' "$last_out.stderr"; then not_ok "$case_id: $(err)"
	elif [[ -e "$box/$M" || -n "$(ls -A "$box/elsewhere" 2>/dev/null)" ]]; then not_ok "$case_id: something was written anyway"
	else ok "$case_id"; fi
done

case_id="W7 the staging directory on another filesystem refuses by name"
new_box w7; installed
: >"$box/state/stage-other-fs"
run post_install 26.10.0-1
if [[ "$last_rc" -eq 0 ]] || ! grep -Fq 'refused: manifest-staging-cross-filesystem' "$last_out.stderr"; then not_ok "$case_id: rc=$last_rc $(err)"
elif [[ -e "$box/$M" ]]; then not_ok "$case_id: a record was put in place anyway"
else ok "$case_id: refused; nothing renamed across filesystems"; fi

case_id="W8 an mv without -T refuses by name before any rename"
new_box w8; installed
: >"$box/state/mv-without-T"
run post_install 26.10.0-1
if [[ "$last_rc" -eq 0 ]] || ! grep -Fq 'refused: manifest-rename-unsupported' "$last_out.stderr"; then not_ok "$case_id: rc=$last_rc $(err)"
elif [[ -e "$box/$M" ]]; then not_ok "$case_id: a record was put in place anyway"
else ok "$case_id: refused before any rename"; fi

# --- M: the migration ------------------------------------------------------------------------
legacy() { # a record the service account could have written, claiming the account
	cat >"$box/$M" <<'JSON'
{"schema": "olivares.ai/local-install/v2", "account": {"user": "olivares", "group": "olivares", "user_created": true, "group_created": true}}
JSON
}
case_id="M1 account claims in a replaceable record are not imported into root's state"
new_box m1; installed
legacy
run post_upgrade 26.10.0-1 26.9.0-1
if [[ "$last_rc" -ne 0 ]]; then not_ok "$case_id: rc=$last_rc $(err)"
elif ! grep -q '"user_created": false' "$box/$M" || ! grep -q '"group_created": false' "$box/$M"; then
	not_ok "$case_id: the new record carries the claims: $(grep -o '"[a-z]*_created": [a-z]*' "$box/$M" | tr '\n' ' ')"
elif grep -qx 'user_created=true\|group_created=true' "$box/var/lib/olivares-package/install-state" 2>/dev/null; then
	not_ok "$case_id: root's install-state carries the claims"
elif ! grep -Fq 'legacy account claims not imported' "$last_out.stderr"; then not_ok "$case_id: no notice: $(err)"
else ok "$case_id: both false; notice printed"; fi

case_id="M2 an account this package created stays recorded across the next install"
new_box m2
run post_install 26.10.0-1
first=$last_rc
run post_upgrade 26.10.1-1 26.10.0-1
if [[ "$first$last_rc" != 00 ]]; then not_ok "$case_id: rc=$first/$last_rc $(err)"
elif ! grep -q '^groupadd ' "$box/calls" || ! grep -q '^useradd ' "$box/calls"; then not_ok "$case_id: the first install created no account"
elif ! grep -q '"user_created": true' "$box/$M" || ! grep -q '"group_created": true' "$box/$M"; then
	not_ok "$case_id: the second install lost root's own record of the account"
else ok "$case_id: created, then carried by root's own record"; fi

# --- R: the readers --------------------------------------------------------------------------
# reader_case ID HOW FORMAT: seed an install, damage the record HOW, remove the package.
reader_case() {
	local id=$1 how=$2 fmt=$3 start took
	new_box "r-$how-$fmt" "$fmt"; installed
	run post_install 26.10.0-1
	[[ "$last_rc" -eq 0 ]] || { not_ok "$id: seed install rc=$last_rc $(err)"; return; }
	case "$how" in
	trusted) ;;
	untrusted) : >"$box/state/record-untrusted" ;;
	link) rm -f "$box/$M"; ln -s "$box/victim" "$box/$M" ;;
	fifo) rm -f "$box/$M"; mkfifo "$box/$M" ;;
	directory) rm -f "$box/$M"; mkdir -p "$box/$M" ;;
	esac
	: >"$box/calls"
	start=$SECONDS
	run pre_remove 26.10.0-1
	took=$((SECONDS - start))
	if [[ "$last_rc" -ne 0 ]]; then not_ok "$id: the removal failed, rc=$last_rc $(err)"
	elif [[ "$took" -ge 5 ]]; then not_ok "$id: the removal waited ${took}s"
	elif ! victim_ok; then not_ok "$id: the victim was changed"
	elif [[ "$how" == trusted ]]; then
		if [[ "$fmt" == deb ]] && ! grep -q '^olivares uninstall --preserve ' "$box/calls"; then not_ok "$id: the engine was not asked to preserve"
		elif grep -Fq 'install record refused' "$last_out.stderr"; then not_ok "$id: a trusted record was reported refused"
		else ok "$id: the engine ran on the trusted record"; fi
	elif ! grep -Fq 'install record refused' "$last_out.stderr"; then not_ok "$id: the refusal is not reported: $(err)"
	elif [[ "$fmt" == deb ]] && ! grep -q '^systemctl disable --now olivares$' "$box/calls"; then not_ok "$id: no safe stop after the refusal"
	else ok "$id: removal completed with the safe stop; nothing followed"; fi
}
reader_case "R0 control: a trusted record, pacman -R" trusted deb
reader_case "R1 the engine refuses an untrusted record, pacman -R" untrusted deb
reader_case "R2 a link to a victim at the record's name, pacman -R" link deb
reader_case "R3 a FIFO at the record's name, pacman -R" fifo deb
reader_case "R4 a directory at the record's name, pacman -R" directory deb

printf '%s: %d cells, %d failed\n' "$me" "$cells" "$failures"
[[ "$failures" -eq 0 ]]
