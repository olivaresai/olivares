#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Built-archive upgrade matrix for the product packages (PKG-L1). HOSTED ONLY: it builds
# real .deb/.rpm/.apk with goreleaser from the nfpms block and runs the real package
# manager (dpkg, rpm, apk) in disposable, network-none, unprivileged containers:
#   old     the ccf7ea20 maintainer scripts (scripts/fixtures/package-upgrade/ccf7ea20), 1.0.0
#   new1    the tree's maintainer scripts, 1.0.0
#   new2    the tree's maintainer scripts, 2.0.0
# Cells: new->new (new1->new2) and old->new (old->new2) for enabled|disabled|masked x
# active|inactive; removal and purge of new2 against removal of old; the appliance (the
# bridge executable present). The service manager is a recording stub over a state model,
# and the olivares binary is a test double that reproduces only the service effect of
# `uninstall --preserve|--purge` (uninstall.go stopService). The package manager, the
# control archives and the call order are real; systemd and OpenRC are not.
#
# Exit 0: every cell holds. Exit 1: a measured cell failed. Exit 2: a tool, image, build
# or container could not be observed (never a pass). Old->new cells report the old
# prerm's stop and disable as a DEFECT with the named recovery; they are never counted as
# a pass of the upgrade contract.
#   --selftest-static   no docker and no build: cell script syntax and judge controls.
# Images are pinned by the official index digest. A tag is not a pin.
# The hosted vehicle may override a primary with the same digest form.
# Compatibility images: none. Fedora 43 and Alpine 3.22 are not contracted.
#   OLIVARES_MATRIX_DEB_IMAGE  Debian 13.7 by index digest
#   OLIVARES_MATRIX_RPM_IMAGE  Fedora 44 by index digest
#   OLIVARES_MATRIX_APK_IMAGE  Alpine 3.24.2 by index digest
set -euo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

LC_ALL=C
export LC_ALL
export GOMAXPROCS="${GOMAXPROCS:-2}"
me=test-package-upgrade-matrix
CONTAINER_PREFIX=olivares-pkg-matrix

could_not_look() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}
fail() {
	printf 'not ok - %s\n' "$*" >&2
	exit 1
}

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
old_dir="$root/scripts/fixtures/package-upgrade/ccf7ea20"
deb_image="${OLIVARES_MATRIX_DEB_IMAGE:-docker.io/library/debian@sha256:9cc080028c43b27d2074d63a5f9caf7166d731494965616c1a6d2827a004585c}"
rpm_image="${OLIVARES_MATRIX_RPM_IMAGE:-docker.io/library/fedora@sha256:43b29f65a41eb9c35e1cd5323e3bdf3b655c2357a9f4f1ff2f9c2798e5045d80}"
apk_image="${OLIVARES_MATRIX_APK_IMAGE:-docker.io/library/alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6}"

# --- the cell: POSIX sh, runs as root inside the container ---------------------------
emit_cell() {
	cat >"$1" <<'CELL'
#!/bin/sh
# Usage: cell.sh FMT SCENARIO ENABLED ACTIVE. /work holds the packages (read-only),
# /out receives the observations. The package manager and the control archives are real.
set -u
fmt=$1 scen=$2 en=$3 ac=$4
S=/var/lib/olivares-matrix-state
mkdir -p "$S" /out
printf '%s\n' disabled >"$S/enabled"; printf '%s\n' inactive >"$S/active"; printf '%s\n' no >"$S/rc-default"
: >/out/calls

# Stubs go to /usr/bin: rpm runs scriptlets with a fixed PATH, and nothing earlier on
# any image's PATH may shadow them.
for d in /usr/local/sbin /usr/local/bin /usr/sbin /sbin /bin; do
  for t in systemctl rc-service rc-update; do
    [ -e "$d/$t" ] && [ ! "$d" -ef /usr/bin ] && rm -f "$d/$t"
  done
done
cat >/usr/bin/systemctl <<'STUB'
#!/bin/sh
S=/var/lib/olivares-matrix-state
printf 'systemctl %s\n' "$*" >>/out/calls
now=no; verb=
for a in "$@"; do case "$a" in --now) now=yes ;; --*) ;; *) [ -z "$verb" ] && verb=$a ;; esac; done
en=$(cat "$S/enabled"); ac=$(cat "$S/active")
case "$verb" in
  daemon-reload) exit 0 ;;
  is-enabled) echo "$en"; [ "$en" = enabled ] ;;
  is-active) echo "$ac"; [ "$ac" = active ] || exit 3 ;;
  disable) [ "$en" = enabled ] && echo disabled >"$S/enabled"; [ $now = yes ] && echo inactive >"$S/active"; exit 0 ;;
  enable) [ "$en" = masked ] && exit 1; echo enabled >"$S/enabled"; [ $now = yes ] && echo active >"$S/active"; exit 0 ;;
  start|restart|reload-or-restart) [ "$en" = masked ] && exit 1; echo active >"$S/active" ;;
  try-restart|condrestart) [ "$en" = masked ] && exit 1; exit 0 ;;
  stop) echo inactive >"$S/active" ;;
  mask) echo masked >"$S/enabled"; exit 0 ;;
  unmask) [ "$en" = masked ] && echo disabled >"$S/enabled"; exit 0 ;;
  *) exit 1 ;;
esac
STUB
cat >/usr/bin/rc-service <<'STUB'
#!/bin/sh
S=/var/lib/olivares-matrix-state
printf 'rc-service %s\n' "$*" >>/out/calls
[ "$1" = olivares ] || exit 1
case "$2" in
  status) [ "$(cat "$S/active")" = active ] ;;
  start) echo active >"$S/active" ;;
  stop) echo inactive >"$S/active" ;;
  *) exit 1 ;;
esac
STUB
cat >/usr/bin/rc-update <<'STUB'
#!/bin/sh
S=/var/lib/olivares-matrix-state
printf 'rc-update %s\n' "$*" >>/out/calls
case "$1" in add) echo yes >"$S/rc-default" ;; del) echo no >"$S/rc-default" ;; esac
exit 0
STUB
for t in update-initramfs dracut; do
  printf '#!/bin/sh\nprintf "%s %%s\\n" "$*" >>/out/calls\nexit 0\n' "$t" >"/usr/bin/$t"
done
chmod 0755 /usr/bin/systemctl /usr/bin/rc-service /usr/bin/rc-update /usr/bin/update-initramfs /usr/bin/dracut

case "$fmt" in deb) ext=deb ;; rpm) ext=rpm ;; apk) ext=apk ;; esac
pm_install() {
  case "$fmt" in
    deb) dpkg -i "$1" ;;
    rpm) rpm -U --nodeps "$1" ;;
    apk) apk add --no-network --allow-untrusted "$1" ;;
  esac
}
pm_remove() {
  case "$fmt" in
    deb) dpkg -r olivares ;;
    rpm) rpm -e olivares ;;
    apk) apk del --no-network olivares ;;
  esac
}
step() {
  name=$1; shift
  printf '== %s\n' "$name" >>/out/calls
  "$@" >"/out/$name.out" 2>&1
  printf '%s\n' "$?" >"/out/$name.rc"
}

case "$scen" in
  new-new|appliance) seed=new1 up=new2 ;;
  old-new) seed=old up=new2 ;;
  removal) seed=new2 up= ;;
  removal-old) seed=old up= ;;
  *) echo "unknown scenario $scen" >/out/cell.error; exit 0 ;;
esac
step seed pm_install "/work/$seed.$ext"
mv /out/calls /out/seed.calls; : >/out/calls
rm -f /run/olivares.pkg-pending /run/olivares.pkg-phases
if [ "$scen" = appliance ]; then
  mkdir -p /usr/libexec/olivares
  printf '#!/bin/sh\nexit 0\n' >/usr/libexec/olivares/olivares-product-package-phase
  chmod 0755 /usr/libexec/olivares/olivares-product-package-phase
fi
printf '%s\n' "$en" >"$S/enabled"; printf '%s\n' "$ac" >"$S/active"; printf '%s\n' yes >"$S/rc-default"
if [ -n "$up" ]; then
  step upgrade pm_install "/work/$up.$ext"
else
  step remove pm_remove
  [ "$fmt" = deb ] && step purge dpkg -P olivares
fi
printf '%s/%s rc-default=%s\n' "$(cat "$S/enabled")" "$(cat "$S/active")" "$(cat "$S/rc-default")" >/out/final
[ -f /run/olivares.pkg-pending ] && cp /run/olivares.pkg-pending /out/pending
[ -f /run/olivares.pkg-phases ] && cp /run/olivares.pkg-phases /out/phases
if [ -e /run/olivares.pkg-removed-init ]; then echo left; else echo consumed; fi >/out/record
if [ -f /var/lib/olivares/install-manifest.json ]; then echo kept; else echo gone; fi >/out/data
chmod 0644 /out/* 2>/dev/null
echo observed >/out/observed
CELL
}

# --- the judge: one cell directory -> PASS|DEFECT|FAIL|BLIND, rc 0|0|1|2 --------------
judge_cell() { # DIR FMT SCENARIO ENABLED ACTIVE [OLD_REMOVAL_DIR]
	python3 - "$@" <<'PY'
import re, sys
from pathlib import Path
out, fmt, scen, en, ac = Path(sys.argv[1]), *sys.argv[2:6]
old_removal = Path(sys.argv[6]) if len(sys.argv) > 6 else None
def read(p, name, default=None):
    f = p / name
    return f.read_text(encoding="utf-8") if f.is_file() else default
if read(out, "observed") is None or read(out, "seed.rc") is None or read(out, "final") is None:
    print("BLIND"); print("cell produced no complete observation", file=sys.stderr); raise SystemExit(2)
if read(out, "seed.rc").strip() != "0":
    print("BLIND"); print(f"seed install rc={read(out, 'seed.rc').strip()}: {read(out, 'seed.out', '')[-400:]}", file=sys.stderr); raise SystemExit(2)
MUT = re.compile(r"^(systemctl (.* )?(enable|disable|start|stop|restart|try-restart|reload-or-restart|condrestart|mask|unmask)( |$)|rc-service olivares (start|stop|restart)|rc-update |olivares uninstall --(preserve|purge)|update-initramfs|dracut)")
calls = [l for l in read(out, "calls", "").splitlines() if not l.startswith("== ")]
muts = [l for l in calls if MUT.match(l)]
final = read(out, "final").strip()
state = final.split()[0]
pend = dict(l.split("=", 1) for l in read(out, "pending", "").splitlines() if "=" in l)
rcs = {n: read(out, f"{n}.rc", "").strip() for n in ("upgrade", "remove", "purge") if (out / f"{n}.rc").is_file()}
reasons, verdict = [], "PASS"
if any(v != "0" for v in rcs.values()):
    reasons.append(f"package manager rc {rcs}")
if any(l.startswith(("update-initramfs", "dracut")) for l in calls):
    reasons.append("a product script called update-initramfs or dracut")
if scen == "new-new":
    if fmt == "apk":
        want = ["rc-service olivares stop", "rc-service olivares start"] if ac == "active" else []
        if muts != want: reasons.append(f"service calls {muts}, want {want}")
        if "rc-default=yes" not in final: reasons.append(f"runlevel not kept: {final}")
        if any(l.startswith("systemctl") for l in calls): reasons.append("an APK script called systemctl")
    else:
        if state != f"{en}/{ac}": reasons.append(f"state {state}, want {en}/{ac}")
        if muts: reasons.append(f"the package changed the service: {muts}")
        if pend: reasons.append(f"pending condition left: {pend}")
    if read(out, "record").strip() != "consumed": reasons.append("an upgrade left a removal record")
elif scen == "old-new":
    if fmt == "apk":
        want = ["rc-service olivares stop", "rc-service olivares start"] if ac == "active" else []
        if muts != want: reasons.append(f"service calls {muts}, want {want}")
    else:
        verdict = "DEFECT"
        disables = [l for l in muts if l == "systemctl disable --now olivares"]
        others = [l for l in muts if l not in ("systemctl disable --now olivares",) and not l.startswith("olivares uninstall --preserve")]
        if len(disables) != 1: reasons.append(f"the ccf7ea20 prerm's 'disable --now' was measured {len(disables)} times, want 1")
        if others: reasons.append(f"a new script changed the service after the old prerm: {others}")
        want_c = want_i = None
        if fmt == "deb":
            if state.startswith("disabled/"):
                want_c = "legacy-prerm-upgrade"
                want_i = "if olivares was enabled before this upgrade: systemctl enable --now olivares; if it was running but not enabled: systemctl start olivares"
        else:
            want_c, want_i = {
                "enabled/active": ("replaced-package-may-disable", "systemctl enable --now olivares"),
                "enabled/inactive": ("replaced-package-may-disable", "systemctl enable olivares"),
                "disabled/active": ("replaced-package-may-disable", "systemctl start olivares"),
            }.get(f"{en}/{ac}", (None, None))
        if pend.get("condition") != want_c or pend.get("instruction") != want_i:
            reasons.append(f"pending {pend.get('condition')}/{pend.get('instruction')}, want {want_c}/{want_i}")
        if want_i and want_i not in read(out, "upgrade.out", ""):
            reasons.append("the instruction was not printed during the upgrade")
        print(f"measured {en}/{ac} -> {state}; recovery {want_c or 'none'}", file=sys.stderr)
elif scen in ("removal", "removal-old"):
    if read(out, "data").strip() != "kept": reasons.append("removal deleted the data directory")
    if read(out, "record").strip() != "consumed": reasons.append("removal left its record")
    if old_removal is not None:
        def effects(p):
            c = [l for l in read(p, "calls", "").splitlines() if MUT.match(l) or "daemon-reload" in l]
            seen, eff = False, []
            for l in c:
                if "daemon-reload" in l:
                    if seen: continue
                    seen = True
                eff.append(l)
            return eff + [read(p, "final", "").strip()]
        if effects(out) != effects(old_removal):
            reasons.append(f"removal effects {effects(out)} differ from ccf7ea20 {effects(old_removal)}")
elif scen == "appliance":
    starts = [l for l in muts if re.match(r"^(systemctl (.* )?(enable|start|restart|try-restart|reload-or-restart)|rc-service olivares start)", l)]
    if starts: reasons.append(f"started on the appliance: {starts}")
    if pend.get("condition") != "package_recovery_required" or pend.get("reason") != "product-start-owned-by-package-phase":
        reasons.append(f"pending {pend}, want package_recovery_required/product-start-owned-by-package-phase")
    # read-r1 m-4: on the appliance the postinstall prints only the appliance notice.
    banner = re.findall(r"^.*(?:Start it|enable --now|rc-service olivares start|rc-update add).*$", read(out, "upgrade.out", ""), re.M)
    if banner:
        reasons.append(f"the appliance postinstall printed a start or enable instruction: {banner[0].strip()}")
if reasons:
    for r in reasons: print(r, file=sys.stderr)
    print("FAIL"); raise SystemExit(1)
print(verdict); raise SystemExit(0)
PY
}

# Primary image pins. The expected digests are the official index digests; the
# lines under test are this script's defaults, not an environment override.
selftest_primary_images() {
	python3 - "$0" "$root/scripts/nfpm-apk-postinstall-runtime.sh" <<'PY' || fail "primary image pin cells"
import re, sys
from pathlib import Path

here = Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
runtime = Path(sys.argv[2]).read_text(encoding="utf-8")
failed = 0

def ok(msg):
    print(f"ok - {msg}")

def not_ok(msg):
    global failed
    failed += 1
    print(f"not ok - {msg}", file=sys.stderr)

asg = re.compile(
    r'^(deb_image|rpm_image|apk_image)="\$\{(OLIVARES_MATRIX_[A-Z0-9_]+):-(.*)\}"$'
)
cmt = re.compile(r"^#   (OLIVARES_MATRIX_(?:DEB|RPM|APK)_IMAGE) +(.*)$")
values, comments = {}, {}
for line in here:
    m = asg.match(line)
    if m:
        values.setdefault(m.group(1), []).append(m.group(3))
    m = cmt.match(line)
    if m:
        comments.setdefault(m.group(1), []).append(m.group(2))

def one(rows, label):
    if len(rows) != 1:
        not_ok(f"{label} count is {len(rows)}, want 1")
        return None
    return rows[0]

want = {
    "deb_image": (
        "OLIVARES_MATRIX_DEB_IMAGE",
        "Debian 13.7",
        "docker.io/library/debian@sha256:9cc080028c43b27d2074d63a5f9caf7166d731494965616c1a6d2827a004585c",
    ),
    "rpm_image": (
        "OLIVARES_MATRIX_RPM_IMAGE",
        "Fedora 44",
        "docker.io/library/fedora@sha256:43b29f65a41eb9c35e1cd5323e3bdf3b655c2357a9f4f1ff2f9c2798e5045d80",
    ),
    "apk_image": (
        "OLIVARES_MATRIX_APK_IMAGE",
        "Alpine 3.24.2",
        "docker.io/library/alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6",
    ),
}
digest_pin = re.compile(r"^docker\.io/library/[a-z0-9]+@sha256:[0-9a-f]{64}$")
runtime_index = re.search(
    r"^ALPINE_INDEX_DIGEST='(sha256:[0-9a-f]{64})'$", runtime, re.M
)
runtime_index = runtime_index.group(1) if runtime_index else ""

for name, (ckey, release, digest_ref) in want.items():
    got = one(values.get(name, []), f"{name} default")
    comment = one(comments.get(ckey, []), f"{ckey} comment")
    reasons = []
    if got != digest_ref:
        reasons.append("default is not that release's index digest")
    if comment is None or release not in comment or "by index digest" not in comment:
        reasons.append("comment does not name the release by index digest")
    if got is None or not digest_pin.fullmatch(got):
        reasons.append("default is a floating tag, not a digest pin")
    if name == "apk_image" and got != f"docker.io/library/alpine@{runtime_index}":
        reasons.append("default does not match the APK runtime index digest")
    if reasons:
        not_ok(f"default {name} does not name {release} by index digest ({'; '.join(reasons)})")
    else:
        ok(f"default {name} names {release} by index digest")

retired = "fedora:" + "42"
if any(retired in line for line in here):
    not_ok("a default still names the retired RPM release")
else:
    ok("no default names the retired RPM release")

compat = "# Compatibility images: none. Fedora 43 and Alpine 3.22 are not contracted."
ncompat = sum(1 for line in here if line == compat)
if ncompat == 1:
    ok("compatibility images: none contracted")
else:
    not_ok(f"compatibility decision is named {ncompat} times, want 1")

if failed:
    raise SystemExit(1)
PY
}

# --- static selftest: the cell parses, the judge reads each outcome by name ------------
synthetic() { # DIR ENABLED/ACTIVE CALLS... (one per argument)
	local d=$1 st=$2
	shift 2
	mkdir -p "$d"
	printf 'observed\n' >"$d/observed"
	printf '0\n' >"$d/seed.rc"
	printf '0\n' >"$d/upgrade.rc"
	printf '%s rc-default=yes\n' "$st" >"$d/final"
	printf 'consumed\n' >"$d/record"
	printf 'kept\n' >"$d/data"
	: >"$d/calls"
	local c
	for c in "$@"; do printf '%s\n' "$c" >>"$d/calls"; done
}
expect() { # WANT_RC WANT_WORD DIR ARGS...
	local want_rc=$1 want=$2 dir=$3 rc got
	shift 3
	set +e
	got="$(judge_cell "$dir" "$@" 2>"$dir/judge.err")"
	rc=$?
	set -e
	[[ "$rc" -eq "$want_rc" && "$got" == "$want" ]] ||
		fail "judge control $(basename "$dir"): rc=$rc verdict=$got, want $want_rc/$want ($(tr '\n' ' ' <"$dir/judge.err"))"
	printf 'ok - judge control %s: %s\n' "$(basename "$dir")" "$got"
}
selftest_static() {
	local t
	t="$(mktemp -d "$scratch_parent/pkg-matrix-static.XXXXXX")"
	# shellcheck disable=SC2064
	trap "rm -rf -- '$t'" RETURN
	emit_cell "$t/cell.sh"
	sh -n "$t/cell.sh" || fail "the cell script does not parse as POSIX sh"
	if grep -E '^[[:space:]]*docker run' "$0" | grep -E -- '--privileged|docker.sock' >/dev/null; then
		fail "a docker run invocation is privileged or names docker.sock"
	fi
	synthetic "$t/nn-holds" enabled/active 'systemctl daemon-reload' 'systemctl is-enabled olivares'
	expect 0 PASS "$t/nn-holds" deb new-new enabled active
	synthetic "$t/nn-disables" disabled/inactive 'olivares uninstall --preserve --data-dir /var/lib/olivares' 'systemctl disable --now olivares'
	expect 1 FAIL "$t/nn-disables" deb new-new enabled active
	synthetic "$t/on-defect" disabled/inactive 'olivares uninstall --preserve --data-dir /var/lib/olivares' 'systemctl disable --now olivares'
	printf 'condition=legacy-prerm-upgrade\ninstruction=%s\n' 'if olivares was enabled before this upgrade: systemctl enable --now olivares; if it was running but not enabled: systemctl start olivares' >"$t/on-defect/pending"
	grep '^instruction=' "$t/on-defect/pending" | cut -d= -f2- >"$t/on-defect/upgrade.out"
	expect 0 DEFECT "$t/on-defect" deb old-new enabled active
	synthetic "$t/on-no-old-prerm" enabled/active 'systemctl daemon-reload'
	expect 1 FAIL "$t/on-no-old-prerm" deb old-new enabled active
	synthetic "$t/appliance-start" enabled/active 'systemctl restart olivares'
	expect 1 FAIL "$t/appliance-start" deb appliance enabled active
	synthetic "$t/appliance-banner" enabled/active 'systemctl daemon-reload'
	printf 'condition=package_recovery_required\nreason=product-start-owned-by-package-phase\n' >"$t/appliance-banner/pending"
	printf '  2. Start it:   sudo systemctl enable --now olivares\n' >"$t/appliance-banner/upgrade.out"
	expect 1 FAIL "$t/appliance-banner" deb appliance enabled active
	synthetic "$t/initramfs" enabled/active 'update-initramfs -u'
	expect 1 FAIL "$t/initramfs" deb new-new enabled active
	mkdir -p "$t/blind"
	expect 2 BLIND "$t/blind" deb new-new enabled active
	trap - RETURN
	rm -rf -- "$t"
	selftest_primary_images
	printf '%s\n' 'ok - static selftest (cell syntax, docker flags, judge PASS/DEFECT/FAIL/BLIND controls, primary image pins)'
}

# --- argv ---------------------------------------------------------------------------
static_only=0
if [[ "${1:-}" == --selftest-static ]]; then
	static_only=1
	shift
fi
[[ $# -eq 0 ]] || could_not_look "unexpected argument: $*"
for tool in bash sh python3 sha256sum mktemp grep sed find; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
scratch_parent="${TMPDIR:-}"
case "$scratch_parent" in /*) ;; *) could_not_look 'TMPDIR must be absolute' ;; esac
[[ -d "$scratch_parent" ]] || could_not_look "TMPDIR is absent: $scratch_parent"

# The old set is built from the exact ccf7ea20 bytes. An edited copy is not a measurement.
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

selftest_static
if [[ "$static_only" -eq 1 ]]; then
	printf '%s\n' "$me: OK — static selftest only (the built-archive matrix was not run)"
	exit 0
fi

# --- tools for the hosted run ----------------------------------------------------------
for tool in docker git go; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "$tool is not on PATH (the matrix runs on the hosted vehicle)"
done
goreleaser_bin="${OLIVARES_GORELEASER:-$(command -v goreleaser 2>/dev/null || true)}"
[[ -n "$goreleaser_bin" && -x "$goreleaser_bin" ]] || could_not_look "goreleaser is not on PATH and OLIVARES_GORELEASER is unset"
gv="$("$goreleaser_bin" --version 2>/dev/null | sed -n 's/^GitVersion:[[:space:]]*//p' | sed -n '1p')" || true
[[ "$gv" =~ ^2\.[0-9]+\.[0-9]+ ]] || could_not_look "goreleaser at $goreleaser_bin is not v2 (got '${gv:-nothing}')"
docker info >/dev/null 2>&1 || could_not_look "docker daemon is not running"

scratch="$(mktemp -d "$scratch_parent/pkg-matrix.XXXXXX")"
container_names=()
cleanup() {
	local c
	for c in "${container_names[@]+"${container_names[@]}"}"; do
		case "$c" in "$CONTAINER_PREFIX"-*) docker rm -f "$c" >/dev/null 2>&1 || true ;; esac
	done
	case "$scratch" in "$scratch_parent"/pkg-matrix.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT INT TERM
mkdir -p "$scratch/gocache"
export GOCACHE="$scratch/gocache"

# --- three package sets from the real nfpms block ---------------------------------------
extract_nfpms() {
	python3 - "$1" "$2" <<'PY' || could_not_look "could not extract the nfpms block from $1"
from pathlib import Path
import sys
src = Path(sys.argv[1]).read_text(encoding="utf-8")
start, end = src.find("\nnfpms:\n"), src.find("\nhomebrew_casks:\n")
if start < 0 or end <= start:
    raise SystemExit("nfpms block not found")
Path(sys.argv[2]).write_text(src[start + 1 : end], encoding="utf-8")
PY
}
extract_nfpms "$root/.goreleaser.yaml" "$scratch/nfpms.yaml"
# build_set LABEL VERSION [SCRIPTS_DIR]: a throwaway project with the real packaging tree,
# the test-double binary and the nfpms block; the old set overlays the ccf7ea20 scripts.
build_set() {
	local label=$1 version=$2 overlay=${3:-} proj="$scratch/proj-$1" f
	mkdir -p "$proj/cmd/olivares" "$proj/packaging"
	cp -a "$root/packaging/." "$proj/packaging/"
	if [[ -n "$overlay" ]]; then
		for f in preremove.sh postinstall.sh postremove.sh apk-preupgrade.sh; do
			cp "$overlay/$f" "$proj/packaging/nfpm/$f"
		done
	fi
	for f in LICENSE NOTICE LICENSING.md DISCLAIMER.md; do cp "$root/$f" "$proj/"; done
	cp -a "$root/LICENSES" "$proj/LICENSES"
	cat >"$proj/cmd/olivares/main.go" <<'GO'
// Test double for the package matrix. It records its argv and reproduces only the
// service effect of `olivares uninstall --preserve|--purge` (uninstall.go stopService).
package main

import (
	"os"
	"os/exec"
	"strings"
)

func main() {
	if f, err := os.OpenFile("/out/calls", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString("olivares " + strings.Join(os.Args[1:], " ") + "\n")
		_ = f.Close()
	}
	if len(os.Args) < 3 || os.Args[1] != "uninstall" || (os.Args[2] != "--preserve" && os.Args[2] != "--purge") {
		return
	}
	manifest, _ := os.ReadFile("/var/lib/olivares/install-manifest.json")
	switch {
	case strings.Contains(string(manifest), `"init": "systemd"`):
		_ = exec.Command("systemctl", "disable", "--now", "olivares").Run()
	case strings.Contains(string(manifest), `"init": "openrc"`):
		_ = exec.Command("rc-service", "olivares", "stop").Run()
	}
}
GO
	printf 'module github.com/olivaresai/olivares\n\ngo 1.26\n' >"$proj/go.mod"
	{
		printf '%s\n' 'version: 2' 'project_name: olivares' 'builds:' \
			'  - id: olivares' '    main: ./cmd/olivares' '    binary: olivares' \
			'    goos: [linux]' '    goarch: [amd64]' '    env: [CGO_ENABLED=0]' \
			'snapshot:' "  version_template: \"$version\""
		cat "$scratch/nfpms.yaml"
	} >"$proj/.goreleaser.yaml"
	(
		cd "$proj"
		git init -q
		git config user.name fixture
		git config user.email fixture@example.invalid
		git add -- .goreleaser.yaml go.mod cmd packaging LICENSE NOTICE LICENSING.md DISCLAIMER.md LICENSES
		git -c core.hooksPath= -c commit.gpgsign=false commit -q --no-verify -s -m "test: package matrix project"
		git remote add origin https://example.invalid/olivares/olivares.git
		"$goreleaser_bin" release --snapshot --clean --config .goreleaser.yaml \
			--skip=before,publish,sign,sbom,docker,validate,archive,homebrew,ko,nix,scoop,snapcraft,winget,aur,announce,notarize,chocolatey,flatpak,makeself,mcp,srpm
	) >"$scratch/build-$label.out" 2>&1 || {
		sed -n '1,120p' "$scratch/build-$label.out" >&2
		could_not_look "goreleaser could not build the $label package set"
	}
	mkdir -p "$scratch/work"
	for f in deb rpm apk; do
		local a
		a="$(find "$proj/dist" -name "*.$f" -print | sed -n '1p')"
		[[ -n "$a" && -f "$a" ]] || could_not_look "the $label set has no .$f"
		cp "$a" "$scratch/work/$label.$f"
	done
}
build_set old 1.0.0 "$old_dir"
build_set new1 1.0.0
build_set new2 2.0.0
sha256sum "$scratch"/work/* | sed "s|$scratch/work/||" | tee "$scratch/artifacts.sha256"
emit_cell "$scratch/work/cell.sh"

for img in "$deb_image" "$rpm_image" "$apk_image"; do
	docker image inspect "$img" >/dev/null 2>&1 || docker pull "$img" >/dev/null 2>&1 ||
		could_not_look "image unavailable: $img"
	printf 'ok - image %s = %s\n' "$img" "$(docker image inspect --format '{{index .RepoDigests 0}}' "$img" 2>/dev/null || echo unknown)"
done

run_cell() { # ID FMT SCENARIO ENABLED ACTIVE
	local id=$1 fmt=$2 img cname out drc
	case "$fmt" in deb) img=$deb_image ;; rpm) img=$rpm_image ;; apk) img=$apk_image ;; esac
	out="$scratch/cells/$id"
	mkdir -p "$out"
	cname="$CONTAINER_PREFIX-$id-$$"
	container_names+=("$cname")
	set +e
	docker run --name "$cname" --rm --network none \
		--volume "$scratch/work:/work:ro" --volume "$out:/out" \
		"$img" /bin/sh /work/cell.sh "$fmt" "$3" "$4" "$5" >"$out.docker.out" 2>&1
	drc=$?
	set -e
	[[ "$drc" -eq 0 ]] || { sed -n '1,40p' "$out.docker.out" >&2; could_not_look "container $cname exited $drc"; }
}

failures=0
defects=0
cells=0
judge() { # ID FMT SCENARIO ENABLED ACTIVE [OLD_REMOVAL_ID]
	local id=$1 v rc
	shift
	cells=$((cells + 1))
	set +e
	v="$(judge_cell "$scratch/cells/$id" "$@" 2>"$scratch/cells/$id.judge.err")"
	rc=$?
	set -e
	case "$rc" in
	0) [[ "$v" == DEFECT ]] && defects=$((defects + 1))
		printf '%s %s — %s\n' "$v" "$id" "$(tr '\n' ' ' <"$scratch/cells/$id.judge.err")" ;;
	1) failures=$((failures + 1)); printf 'not ok %s — %s\n' "$id" "$(tr '\n' ' ' <"$scratch/cells/$id.judge.err")" ;;
	*) could_not_look "cell $id: $(tr '\n' ' ' <"$scratch/cells/$id.judge.err")" ;;
	esac
}

states=("enabled active" "enabled inactive" "disabled active" "disabled inactive" "masked active" "masked inactive")
for fmt in deb rpm; do
	for scen in new-new old-new; do
		for s in "${states[@]}"; do
			read -r en ac <<<"$s"
			run_cell "$fmt-$scen-$en-$ac" "$fmt" "$scen" "$en" "$ac"
			judge "$fmt-$scen-$en-$ac" "$fmt" "$scen" "$en" "$ac"
		done
	done
done
for scen in new-new old-new; do
	for ac in active inactive; do
		run_cell "apk-$scen-$ac" apk "$scen" disabled "$ac"
		judge "apk-$scen-$ac" apk "$scen" disabled "$ac"
	done
done
for fmt in deb rpm apk; do
	run_cell "$fmt-removal-old" "$fmt" removal-old enabled active
	run_cell "$fmt-removal" "$fmt" removal enabled active
	judge "$fmt-removal" "$fmt" removal enabled active "$scratch/cells/$fmt-removal-old"
	run_cell "$fmt-appliance" "$fmt" appliance enabled active
	judge "$fmt-appliance" "$fmt" appliance enabled active
done

printf '%s: %d cells, %d failed, %d old->new defects measured (reported, not counted green)\n' \
	"$me" "$cells" "$failures" "$defects"
[[ "$failures" -eq 0 ]] || exit 1
exit 0
