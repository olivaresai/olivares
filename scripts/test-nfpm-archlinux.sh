#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Arch Linux package contract, without goreleaser, pacman or root.
#   1. The nfpms block builds `archlinux` with the same licence and doc contents as
#      deb/rpm/apk, the systemd unit and a `systemd` package-init stamp, the env file
#      as a pacman backup file, no data-dir node, and Arch-only scriptlets.
#   2. .INSTALL is assembled the way nFPM v2.47.0 writes it (arch/arch.go
#      writeScripts: `function NAME() {` + file body + `}`) and must parse under bash.
#   3. The scriptlets run as functions against a path-prefixed copy with stubbed
#      account and service tools: install creates the account once, upgrade does not,
#      no scriptlet enables, starts or restarts the service, and removal keeps data.
# What this cannot see: the real package bytes and pacman itself. The Arch container
# leg of the package matrix installs, upgrades and removes the real package.
set -euo pipefail
LC_ALL=C
export LC_ALL

could_not_look() {
	printf 'test-nfpm-archlinux: NO HE PODIDO MIRAR — %s\n' "$*" >&2
	exit 2
}
fail() {
	printf 'not ok - %s\n' "$*" >&2
	exit 1
}
root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || could_not_look 'TMPDIR must be an existing absolute directory'
for tool in bash cat chmod grep mkdir mktemp python3 rm sed; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
cleanup() {
	case "$scratch" in "$tmp_root"/nfpm-archlinux-test.*) rm -rf -- "$scratch" ;; *) ;; esac
}
scratch="$(mktemp -d "$tmp_root/nfpm-archlinux-test.XXXXXX")"; trap cleanup EXIT INT TERM
checks=0
ok() { checks=$((checks + 1)); printf 'ok %02d - %s\n' "$checks" "$1"; }

# --- 1. the nfpms block and the builds that feed it --------------------------------
# config_py MODE: read .goreleaser.yaml as text (no YAML library on every host) and check
# one part of the Arch package contract. MODE: contents | amd64 | scripts.
config_py() {
	python3 - "$root/.goreleaser.yaml" "$1" <<'PY'
import re
import sys

text, mode = open(sys.argv[1], encoding="utf-8").read(), sys.argv[2]

def section(name):
    m = re.search(rf"^{name}:\n(.*?)(?=^\S)", text, re.S | re.M)
    assert m, f"no {name} section"
    return m.group(1)

def entries(block):
    parts = re.split(r"^  - ", block, flags=re.M)[1:]
    return ["  - " + p for p in parts]

def field(entry, key):
    m = re.search(rf"^\s*(?:- )?{key}: (.*)$", entry, re.M)
    return m.group(1).strip() if m else None

def flow(value):
    return [v.strip() for v in value.split("#")[0].strip().strip("[]").split(",")] if value else []

def contents_of(entry):
    m = re.search(r"^    contents:\n(.*?)(?=^    \S)", entry, re.S | re.M)
    out = []
    for item in re.split(r"^      - ", m.group(1), flags=re.M)[1:] if m else []:
        c = {}
        for line in item.splitlines():
            mm = re.match(r"\s*(src|dst|type|packager): (.*)$", line)
            if mm:
                c[mm.group(1)] = mm.group(2).strip().strip('"')
        out.append(c)
    return out

nfpms = entries(section("nfpms"))
arch_entries = [e for e in nfpms if "archlinux" in flow(field(e, "formats"))]
assert len(arch_entries) == 1, "exactly one nfpms entry builds archlinux"
arch = arch_entries[0]
arch_contents = [c for c in contents_of(arch) if c.get("packager") in (None, "archlinux")]

builds = {field(b, "id"): b for b in entries(section("builds"))}
def targets(build_id):
    b = builds[build_id]
    out = {(g, a) for g in flow(field(b, "goos")) for a in flow(field(b, "goarch"))}
    for ig in re.findall(r"- goos: (\w+)\n\s+goarch: (\w+)", b):
        out.discard(ig)
    return out

if mode == "contents":
    dsts = {c["dst"]: c for c in arch_contents}
    for doc in ("LICENSE", "NOTICE", "LICENSING.md", "DISCLAIMER.md", "LICENSES"):
        assert f"/usr/share/doc/olivares/{doc}" in dsts, f"{doc} missing under /usr/share/doc/olivares"
        assert f"/usr/share/licenses/olivares/{doc}" in dsts, f"{doc} missing under /usr/share/licenses/olivares"
        assert dsts[f"/usr/share/licenses/olivares/{doc}"]["src"] == doc
    assert dsts["/usr/lib/systemd/system/olivares.service"]["src"] == "packaging/systemd/olivares.service"
    assert dsts["/usr/lib/olivares/package-init"]["src"] == "packaging/nfpm/package-init-systemd.txt"
    assert dsts["/etc/olivares/olivares.env"]["type"] == "config|noreplace", "env file must be a pacman backup file"
    assert "/var/lib/olivares" not in dsts, "pacman would reset the data-dir owner on every upgrade"
    assert "/etc/init.d/olivares" not in dsts
elif mode == "amd64":
    # nfpm packages only the linux binaries of the builds an entry names
    # (goreleaser v2.17.0 internal/pipe/nfpm/nfpm.go findArtifacts, ByGooses).
    def linux(ids):
        return {t for i in ids for t in targets(i) if t[0] == "linux"}
    got = linux(flow(field(arch, "ids")))
    assert got == {("linux", "amd64")}, f"the Arch package is built from {sorted(got)}, not linux/amd64 only"
    others = [e for e in nfpms if e is not arch]
    assert len(others) == 1 and flow(field(others[0], "formats")) == ["deb", "rpm", "apk"], "the deb/rpm/apk entry"
    got = linux(flow(field(others[0], "ids")))
    assert got == {("linux", "amd64"), ("linux", "arm64")}, f"deb/rpm/apk are built from {sorted(got)}"
    archives = {field(a, "id"): a for a in entries(section("archives"))}
    got = set().union(*(targets(i) for i in flow(field(archives["olivares"], "ids"))))
    assert got == {(g, a) for g in ("linux", "darwin") for a in ("amd64", "arm64")}, f"base archives: {sorted(got)}"
    for d in entries(section("dockers")):
        ids, want = flow(field(d, "ids")), ("linux", field(d, "goarch"))
        assert any(want in targets(i) for i in ids), f"docker {field(d, 'id')} has no {want} binary in {ids}"
    # The builds that share the base binary differ only in their targets.
    def body(b):
        b = re.sub(r"^\s*#.*\n", "", b, flags=re.M)
        b = re.sub(r"^    ignore:\n(?:      .*\n)+", "", b, flags=re.M)
        b = re.sub(r"^    hooks:\n(?:      .*\n)+", "", b, flags=re.M)
        b = re.sub(r"^\s*(?:- )?(id|goos|goarch): .*\n", "", b, flags=re.M)
        return b.strip()
    base = [i for i in builds if i != "olivares-fips"]
    assert len({body(builds[i]) for i in base}) == 1, f"the base builds {base} are not in lockstep"
elif mode == "scripts":
    s = arch
    for key, script in (("postinstall", "archlinux-postinstall.sh"), ("preremove", "archlinux-preremove.sh"),
                        ("postremove", "archlinux-postremove.sh")):
        assert re.search(rf"^\s+{key}: packaging/nfpm/{script}$", s, re.M), f"{key}: {script}"
    assert "preinstall" not in s
    assert re.search(r"^\s+postupgrade: packaging/nfpm/archlinux-postinstall.sh$", s, re.M), "archlinux.scripts.postupgrade"
    assert "preupgrade" not in s, "an upgrade must not stop the service"
    assert re.search(r"^\s+packager: \S", s, re.M), "archlinux.packager"
    assert "file_name_template" not in s, "release tooling admits only olivares_<version>_* names"
PY
}
config_py contents || fail 'the Arch package lacks its licence texts, unit, stamp or backup env file'
ok 'the Arch package carries the licence texts under /usr/share/doc/olivares and /usr/share/licenses/olivares, the unit, a systemd stamp and a backup env file'
config_py amd64 || fail 'the Arch package is not linux/amd64 only, or the other outputs lost a target'
ok 'the Arch package is built from linux/amd64 only; deb/rpm/apk, the base archives and the images keep their targets; the base builds are in lockstep'
config_py scripts || fail 'the Arch package does not use the Arch scriptlets'
ok 'the Arch package uses the Arch scriptlets, with post_upgrade and no pre_upgrade'

# --- 2. .INSTALL as nFPM v2.47.0 writes it -------------------------------------------
nfpm_dir="$root/packaging/nfpm"
for script in archlinux-postinstall.sh archlinux-preremove.sh archlinux-postremove.sh; do
	[[ -f "$nfpm_dir/$script" && ! -L "$nfpm_dir/$script" ]] || fail "missing scriptlet $script"
done
# write_install ROOTED_DIR OUT: one function per scriptlet, body copied verbatim.
write_install() {
	local dir="$1" out="$2" name file
	: >"$out"
	for name in post_install post_upgrade pre_remove post_remove; do
		case "$name" in
		post_install | post_upgrade) file=archlinux-postinstall.sh ;;
		pre_remove) file=archlinux-preremove.sh ;;
		post_remove) file=archlinux-postremove.sh ;;
		esac
		printf 'function %s() {\n' "$name" >>"$out"
		cat "$dir/$file" >>"$out"
		printf '\n}\n\n' >>"$out"
	done
}
write_install "$nfpm_dir" "$scratch/.INSTALL"
bash -n "$scratch/.INSTALL" || fail '.INSTALL assembled as nFPM writes it does not parse under bash'
ok '.INSTALL assembled as nFPM v2.47.0 writes it parses under bash'

# --- 3. the scriptlets as pacman calls them -----------------------------------------
# enables_service FILE: 0 when the file enables, starts or restarts the unit outside
# printed instructions (heredoc text is instruction, not action).
enables_service() {
	python3 - "$1" <<'PY'
import re
import sys

in_doc = None
for raw in open(sys.argv[1], encoding="utf-8"):
    line = raw.rstrip("\n")
    if in_doc:
        if line == in_doc:
            in_doc = None
        continue
    doc = re.search(r"<<-?'?(\w+)'?", line)
    code = line.split("#", 1)[0]
    if re.search(r"\b(systemctl|rc-service|rc-update|service)\b[^;&|]*\b(enable|start|restart|reenable|add)\b", code):
        sys.exit(0)
    if doc:
        in_doc = doc.group(1)
sys.exit(1)
PY
}
for script in archlinux-postinstall.sh archlinux-preremove.sh archlinux-postremove.sh; do
	if enables_service "$nfpm_dir/$script"; then fail "$script enables, starts or restarts the service"; fi
done
cp "$nfpm_dir/archlinux-postremove.sh" "$scratch/mutant-enable.sh"
printf '%s\n' 'systemctl enable --now olivares' >>"$scratch/mutant-enable.sh"
enables_service "$scratch/mutant-enable.sh" || fail 'the enable predicate misses a mutant that enables the service'
ok 'no Arch scriptlet enables, starts or restarts the service; the mutant that does is caught'

# A path-prefixed copy: every absolute path the scriptlets touch moves under $fake.
fake="$scratch/root"
stubs="$scratch/bin"
calls="$scratch/calls.log"
mkdir -p "$fake/usr/lib/olivares" "$fake/usr/bin" "$fake/etc/olivares" "$fake/var/lib" "$stubs"
printf 'systemd\n' >"$fake/usr/lib/olivares/package-init"
mkdir -p "$scratch/rooted"
for script in archlinux-postinstall.sh archlinux-preremove.sh archlinux-postremove.sh; do
	sed -e "s#/usr/lib/olivares/#$fake/usr/lib/olivares/#g" \
		-e "s#/var/lib/olivares#$fake/var/lib/olivares#g" \
		-e "s#/usr/bin/olivares#$fake/usr/bin/olivares#g" \
		"$nfpm_dir/$script" >"$scratch/rooted/$script"
done
grep -F -q "$fake/var/lib/olivares" "$scratch/rooted/archlinux-postinstall.sh" || could_not_look 'path prefix did not apply'
write_install "$scratch/rooted" "$scratch/rooted/.INSTALL"
state="$scratch/accounts"
mkdir -p "$state"
for tool in groupadd useradd systemctl; do
	cat >"$stubs/$tool" <<STUB
#!/usr/bin/env bash
printf '%s %s\n' "$tool" "\$*" >>"$calls"
case "$tool" in
groupadd) : >"$state/group" ;;
useradd) : >"$state/user" ;;
esac
STUB
done
cat >"$stubs/getent" <<STUB
#!/usr/bin/env bash
case "\$1" in group) [[ -e "$state/group" ]] ;; passwd) [[ -e "$state/user" ]] ;; *) exit 2 ;; esac
STUB
cat >"$stubs/id" <<STUB
#!/usr/bin/env bash
# "id -u" alone is the caller (root in production); "id -u olivares" needs the account.
[[ \$# -lt 2 || -e "$state/user" ]] || exit 1
case "\$1" in -u) /usr/bin/id -u ;; -g) /usr/bin/id -g ;; *) exit 2 ;; esac
STUB
cat >"$stubs/chown" <<STUB
#!/usr/bin/env bash
printf 'chown %s\n' "\$*" >>"$calls"
STUB
cat >"$fake/usr/bin/olivares" <<STUB
#!/usr/bin/env bash
printf 'olivares %s\n' "\$*" >>"$calls"
STUB
chmod 0755 "$stubs"/* "$fake/usr/bin/olivares"
run_scriptlet() {
	local fn="$1"
	shift
	PATH="$stubs:$PATH" bash -c '. "$1"; fn="$2"; shift 2; "$fn" "$@"' _ "$scratch/rooted/.INSTALL" "$fn" "$@"
}

: >"$calls"
run_scriptlet post_install 26.10.0-1 >"$scratch/install.out" || fail 'post_install failed'
[[ "$(grep -c '^groupadd --system olivares$' "$calls")" -eq 1 ]] || fail 'post_install did not create the group once'
grep -Fxq "useradd --system --gid olivares --home-dir $fake/var/lib/olivares --no-create-home --shell /usr/bin/nologin --comment Olivares AI olivares" "$calls" ||
	fail 'post_install did not create the no-login system user'
[[ -d "$fake/var/lib/olivares" ]] || fail 'post_install did not create the data dir'
manifest="$fake/var/lib/olivares/install-manifest.json"
python3 - "$manifest" "$fake" <<'PY' || fail 'install manifest is not the v2 systemd record'
import json
import sys

m = json.load(open(sys.argv[1], encoding="utf-8"))
fake = sys.argv[2]
assert m["schema"] == "olivares.ai/local-install/v2" and m["init"] == "systemd"
assert m["account"] == {"user": "olivares", "group": "olivares", "user_created": True, "group_created": True}
# The copy under test has its paths prefixed; the prefix is removed before comparing.
paths = {f["path"].removeprefix(fake) for f in m["files"]}
assert paths == {"/usr/bin/olivares", "/etc/olivares/olivares.env", "/usr/lib/systemd/system/olivares.service"}, paths
assert m["data_dir"].removeprefix(fake) == "/var/lib/olivares"
assert not any(f["managed"] for f in m["files"])
PY
grep -F -q 'The package does not enable or start the service.' "$scratch/install.out" || fail 'post_install notice'
if grep -q '^systemctl' "$calls"; then fail 'post_install called systemctl'; fi
ok 'post_install creates the group, the no-login user, the data dir and the v2 manifest, and calls no systemctl'

: >"$calls"
run_scriptlet post_upgrade 26.10.1-1 26.10.0-1 >"$scratch/upgrade.out" || fail 'post_upgrade failed'
if grep -q -E '^(groupadd|useradd|systemctl)' "$calls"; then fail 'post_upgrade touched the account or the service'; fi
grep -F -q 'Olivares AI upgraded from 26.10.0-1 to 26.10.1-1.' "$scratch/upgrade.out" || fail 'post_upgrade notice'
grep -F -q '"user_created": true, "group_created": true' "$manifest" || fail 'post_upgrade lost the account provenance'
ok 'post_upgrade keeps the account and its provenance and neither starts nor restarts the service'

: >"$calls"
run_scriptlet pre_remove 26.10.1-1 || fail 'pre_remove failed'
grep -Fxq "olivares uninstall --preserve --data-dir $fake/var/lib/olivares" "$calls" || fail 'pre_remove did not preserve'
run_scriptlet post_remove 26.10.1-1 >"$scratch/remove.out" || fail 'post_remove failed'
[[ -f "$manifest" && -e "$state/user" ]] || fail 'removal deleted data or the account'
if grep -q -E '^(userdel|groupdel)' "$calls"; then fail 'removal deleted the account'; fi
ok 'pre_remove runs uninstall --preserve; post_remove keeps the data dir and the account'

rm -f -- "$fake/usr/lib/olivares/package-init"
if run_scriptlet post_install 26.10.0-1 >/dev/null 2>"$scratch/noinit.err"; then fail 'post_install ran without the package-init stamp'; fi
grep -F -q "package-init is '', want systemd" "$scratch/noinit.err" || fail 'missing stamp was not named'
ok 'post_install refuses by name when the systemd package-init stamp is absent'

printf 'test-nfpm-archlinux: OK — %d checks\n' "$checks"
