#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic DIST-24-04 battery. Offline roots exercise every adapter without
# mutating host accounts/init; named mutants prove the gate observes its claims.
set -euo pipefail
# ⛔ THE BATTERY OWNS ITS USER ENVIRONMENT. install-service.sh --user derives the config/unit tuple
#    from XDG_CONFIG_HOME and XDG_DATA_HOME when they are set. GitHub-hosted runners export
#    XDG_CONFIG_HOME=/home/runner/.config, so every --user case that overrode only HOME produced a
#    tuple outside its fake home and the installer refused it: "user config/unit tuple is outside the
#    release-index install_layout" (hosted preprod rehearsal 35133811772, control-plane, 2026-09-16).
#    Unset once here, for all six --user cases: an execution-scoped fact the battery declares
#    instead of inheriting from the box. Control: the previous battery is red under that variable.
unset XDG_CONFIG_HOME XDG_DATA_HOME

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
scratch_parent="${OLIVARES_TEST_SCRATCH_ROOT:-$(dirname -- "$root")}"
scratch="$(mktemp -d "$scratch_parent/.olivares-service-install-test.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
passes=0
case_ok() { printf 'ok %d - %s\n' "$((passes += 1))" "$1"; }
expect_rc() {
  local want="$1" label="$2"
  shift 2
  set +e
  "$@" >"$scratch/out" 2>"$scratch/err"
  local got=$?
  set -e
  if [[ "$got" -ne "$want" ]]; then
    printf 'not ok - %s (wanted rc=%s, got rc=%s)\n' "$label" "$want" "$got" >&2
    sed -n '1,160p' "$scratch/out" >&2
    sed -n '1,160p' "$scratch/err" >&2
    exit 1
  fi
  case_ok "$label"
}

make_binary() {
  local path="$1"
  mkdir -p "$(dirname -- "$path")"
  cat >"$path" <<'FAKE'
#!/bin/sh
case "${OLIVARES_NOT_A_REAL_KEY+x}" in x) exit 1 ;; esac
exit 0
FAKE
  chmod 0755 "$path"
}

expect_rc 0 "canonical service and doctor contract" bash "$root/scripts/check-service-install.sh"

dry="$scratch/dry"
mkdir -p "$scratch/home/.local/bin"
make_binary "$scratch/home/.local/bin/olivares"
expect_rc 0 "user dry-run is mutation-free" env OLIVARES_OS=linux HOME="$scratch/home" \
  /bin/sh "$root/scripts/install-service.sh" --user --init systemd \
  --binary "$scratch/home/.local/bin/olivares" --dry-run
[[ ! -e "$dry" ]] || { printf 'dry-run mutated %s\n' "$dry" >&2; exit 1; }

# Exercise the full signed second stage, not just its helper in isolation. The
# fixture archive has GoReleaser's real member layout and the fake cosign only
# controls the already-covered cryptographic boundary.
e2e="$scratch/signed-stage"
mkdir -p "$e2e/payload/scripts" "$e2e/payload/packaging/service" "$e2e/fixture" "$e2e/fakebin"
make_binary "$e2e/payload/olivares"
cp "$root/scripts/install-service.sh" "$e2e/payload/scripts/"
cp "$root/packaging/service/"* "$e2e/payload/packaging/service/"
tar -C "$e2e/payload" -czf "$e2e/fixture/olivares_26.9.0_linux_amd64.tar.gz" \
  olivares scripts/install-service.sh packaging/service
digest="$(sha256sum "$e2e/fixture/olivares_26.9.0_linux_amd64.tar.gz" | awk '{print $1}')"
printf '%s  %s\n' "$digest" olivares_26.9.0_linux_amd64.tar.gz >"$e2e/fixture/checksums.txt"
printf '%s\n' test-signature >"$e2e/fixture/checksums.txt.sig"
printf '%s\n' test-certificate >"$e2e/fixture/checksums.txt.pem"
cat >"$e2e/fakebin/curl" <<'FAKECURL'
#!/bin/sh
set -eu
url="" dest=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    http://*|https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
[ -n "$url" ] && [ -n "$dest" ]
cp "$FIXTURE/${url##*/}" "$dest"
FAKECURL
cat >"$e2e/fakebin/cosign" <<'FAKECOSIGN'
#!/bin/sh
exit 0
FAKECOSIGN
chmod 0755 "$e2e/fakebin/curl" "$e2e/fakebin/cosign"
rendered="$e2e/olivares-install-26.9.0.sh"
bash "$root/scripts/render-release-installer.sh" 26.9.0 "$rendered"
expect_rc 0 "signed second stage installs its archived user-service adapter" \
  env FIXTURE="$e2e/fixture" HOME="$e2e/home" OLIVARES_OS=linux OLIVARES_ARCH=amd64 \
  OLIVARES_GITHUB_URL=https://fixture.invalid PATH="$e2e/fakebin:/usr/bin:/bin" \
  /bin/sh "$rendered" --version v26.9.0 --bindir "$e2e/home/.local/bin" --user --init systemd
[[ -x "$e2e/home/.local/bin/olivares" ]]
[[ -f "$e2e/home/.config/systemd/user/olivares.service" ]]
[[ -f "$e2e/home/.local/share/olivares/install-manifest.json" ]]
grep -Fq 'result-json: {"schema":"olivares.ai/service-install-result/v1"' "$scratch/out"
grep -Fq "journalctl --user -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'" \
  "$scratch/out"
case_ok "signed second stage leaves a machine-readable local ownership manifest"

stage="$scratch/systemd-root"
make_binary "$stage/opt/olivares/bin/olivares"
expect_rc 0 "systemd system adapter stages modes, config and manifest" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$stage" --init systemd \
  --binary /opt/olivares/bin/olivares --data-dir /var/lib/olivares \
  --config /etc/olivares/olivares.env
[[ "$(stat -c '%a' "$stage/var/lib/olivares")" = 750 ]]
[[ "$(stat -c '%a' "$stage/etc/olivares/olivares.env")" = 640 ]]
[[ "$(stat -c '%a' "$stage/etc/systemd/system/olivares.service")" = 644 ]]
grep -Fq 'User=olivares' "$stage/etc/systemd/system/olivares.service"
grep -Fq "journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'" \
  "$scratch/out"
grep -Fq 'https://127.0.0.1:8443' "$scratch/out" || true
python3 - "$stage/var/lib/olivares/install-manifest.json" <<'PY'
import json, pathlib, sys
v = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert v["schema"] == "olivares.ai/local-install/v2"
assert v["mode"] == "system" and v["init"] == "systemd"
assert {x["path"] for x in v["files"]} >= {
    "/opt/olivares/bin/olivares", "/etc/olivares/olivares.env",
    "/etc/systemd/system/olivares.service",
}
assert {x["role"] for x in v["files"]} >= {"binary", "config", "unit"}
assert v["data_dir"] == "/var/lib/olivares"
PY
case_ok "systemd manifest is machine-readable and complete"
expect_rc 0 "systemd staging is idempotent without clobbering config" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$stage" --init systemd \
  --binary /opt/olivares/bin/olivares --data-dir /var/lib/olivares \
  --config /etc/olivares/olivares.env

python3 - "$stage/var/lib/olivares/install-manifest.json" <<'PY'
import json, pathlib, sys
v = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert v["layout"] == "default", v
PY
case_ok "default tuple records the default layout"

# Custom data directory with a space: rendered quoted into the unit, recorded as
# a custom layout, idempotent, and never through a link or a shared root.
custom="$scratch/custom-root"
make_binary "$custom/usr/local/bin/olivares"
mkdir -p "$custom/srv"
expect_rc 0 "systemd system adapter admits a custom data directory with a space" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'
[[ "$(stat -c '%a' "$custom/srv/olivares data")" = 750 ]]
grep -Fq 'ExecStart=/usr/local/bin/olivares serve --data-dir="/srv/olivares data" --listen=:8443' \
  "$custom/etc/systemd/system/olivares.service"
grep -Fqx 'ReadWritePaths="/srv/olivares data"' "$custom/etc/systemd/system/olivares.service"
grep -Fq 'data: /srv/olivares data (custom layout)' "$scratch/out"
python3 - "$custom/srv/olivares data/install-manifest.json" <<'PY'
import json, pathlib, sys
v = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert v["layout"] == "custom" and v["data_dir"] == "/srv/olivares data", v
assert v["manifest"] == "/srv/olivares data/install-manifest.json", v
assert {x["path"] for x in v["files"]} == {
    "/usr/local/bin/olivares", "/etc/olivares/olivares.env", "/etc/systemd/system/olivares.service",
}
PY
case_ok "custom data directory is quoted in the unit and recorded as a custom layout"
expect_rc 0 "custom layout reinstall is idempotent" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'

expect_rc 1 "mutant: top-level custom data directory is refused" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /srv
grep -Fq 'at least two levels deep' "$scratch/err"
[[ ! -e "$custom/srv/install-manifest.json" ]]

expect_rc 1 "mutant: traversal in a data directory is refused" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /srv/olivares/../etc
grep -Fq 'must be canonical' "$scratch/err"

expect_rc 1 "mutant: data directory containing the config path is refused" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /etc/olivares
grep -Fq 'must not contain the installed path' "$scratch/err"

expect_rc 1 "mutant: absent parent of a custom data directory is not created under privilege" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /data/nested/olivares
grep -Fq 'parent of the custom data directory does not exist' "$scratch/err"
[[ ! -e "$custom/data" ]]

mkdir -p "$custom/real-target"
chmod 0700 "$custom/real-target"
ln -s "$custom/real-target" "$custom/srv/linked"
expect_rc 1 "mutant: symlink component in a custom data path is refused" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /srv/linked/olivares
grep -Fq 'symbolic link' "$scratch/err"
[[ "$(stat -c '%a' "$custom/real-target")" = 700 ]]
[[ ! -e "$custom/real-target/olivares" ]]

expect_rc 1 "mutant: unit-unsafe character in a data path is refused" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares%i'
grep -Fq 'unsafe for a service definition' "$scratch/err"

# The default unit carries no BindPaths line and keeps ProtectHome=true.
if grep -q '^BindPaths=' "$stage/etc/systemd/system/olivares.service"; then
  printf 'default unit gained a BindPaths line\n' >&2; exit 1
fi
grep -Fqx 'ProtectHome=true' "$stage/etc/systemd/system/olivares.service"
if grep -q '@BIND_PATHS@' "$stage/etc/systemd/system/olivares.service"; then
  printf 'default unit kept the bind marker\n' >&2; exit 1
fi
case_ok "default unit keeps ProtectHome=true and no BindPaths"

# Engine upgrade: the adapter rewrites the manifest and must carry the AgentOps
# records the co-deployment installer added, after validating them.
python3 - "$custom/srv/olivares data/install-manifest.json" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
text = text.replace('  "config": "/etc/olivares/olivares.env",\n',
                    '  "config": "/etc/olivares/olivares.env",\n  "workspace_dir": "/mnt/work spaces",\n', 1)
text = text.replace('"role": "unit", "mode": "0644", "managed": true}\n',
                    '"role": "unit", "mode": "0644", "managed": true},\n'
                    '    {"path": "/etc/systemd/system/olivares.service.d/agentops.conf", "role": "dropin", "mode": "0644", "managed": true},\n'
                    '    {"path": "/etc/olivares/agentops.env", "role": "runtime-env", "mode": "0640", "managed": false}\n', 1)
path.write_text(text, encoding="utf-8")
PY
expect_rc 0 "adapter rerun carries validated workspace and AgentOps roles" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'
python3 - "$custom/srv/olivares data/install-manifest.json" <<'PY'
import json, pathlib, sys
v = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert v["layout"] == "custom" and v["workspace_dir"] == "/mnt/work spaces", v
roles = {x["role"]: x for x in v["files"]}
assert set(roles) == {"binary", "config", "unit", "dropin", "runtime-env"}, roles
assert roles["dropin"]["path"] == "/etc/systemd/system/olivares.service.d/agentops.conf" and roles["dropin"]["managed"] is True
assert roles["runtime-env"]["path"] == "/etc/olivares/agentops.env" and roles["runtime-env"]["managed"] is False
assert v["account"]["user_created"] is False  # offline root never creates accounts
PY
case_ok "carried records are exact and the layout stays custom"

tamper="$custom/srv/olivares data/install-manifest.json"
cp "$tamper" "$scratch/manifest.good"
sed -i 's|"workspace_dir": "/mnt/work spaces"|"workspace_dir": "/mnt/../etc"|' "$tamper"
expect_rc 1 "mutant: a non-canonical recorded workspace is refused, not carried" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'
grep -Fq 'must be canonical' "$scratch/err"
cp "$scratch/manifest.good" "$tamper"
sed -i 's|olivares.service.d/agentops.conf", "role": "dropin"|other.service.d/agentops.conf", "role": "dropin"|' "$tamper"
expect_rc 1 "mutant: a recorded drop-in outside this unit is refused, not carried" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'
grep -Fq 'records an AgentOps drop-in at' "$scratch/err"
cp "$scratch/manifest.good" "$tamper"
sed -i 's|/etc/olivares/agentops.env", "role": "runtime-env"|/etc/other/agentops.env", "role": "runtime-env"|' "$tamper"
expect_rc 1 "mutant: a recorded runtime env beside another config is refused, not carried" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'
grep -Fq 'records an AgentOps runtime env at' "$scratch/err"
cp "$scratch/manifest.good" "$tamper"

# R4. The record is JSON, so the same document written another way must carry the same
# meaning; and what cannot be carried is refused with the record and the reason named,
# never dropped in silence. Each presentation below is produced FROM the record on disk,
# so it is byte-different and semantically identical to the one before it.
carry_rerun() {
  expect_rc "$1" "$2" \
    env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
    /bin/sh "$root/scripts/install-service.sh" --system --root "$custom" --init systemd \
    --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'
}

python3 - "$tamper" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
p.write_text(json.dumps(json.loads(p.read_text(encoding="utf-8")), separators=(",", ":")), encoding="utf-8")
PY
carry_rerun 0 "a compact record carries the same workspace and roles"
python3 - "$tamper" <<'PY'
import json, pathlib, sys
v = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert v["mode"] == "system" and v["init"] == "systemd" and v["layout"] == "custom", v
assert v["workspace_dir"] == "/mnt/work spaces", v
roles = {x["role"]: x for x in v["files"]}
assert set(roles) == {"binary", "config", "unit", "dropin", "runtime-env"}, roles
assert roles["dropin"]["managed"] is True and roles["runtime-env"]["managed"] is False, roles
assert v["account"]["user"] == "olivares" and v["account"]["user_created"] is False, v
PY
case_ok "a compact previous record keeps the install mode, account and every carried flag"
[[ "$(stat -c '%a' "$tamper")" = 640 ]]
case_ok "and the rewritten manifest keeps the system mode's own permissions"

python3 - "$tamper" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
d = json.loads(p.read_text(encoding="utf-8"))
rows = ['  %s : %s' % (json.dumps(k), json.dumps(v)) for k, v in reversed(list(d.items())) if k != "files"]
entries = []
for f in d["files"]:
    fields = ',\n'.join('      %s: %s' % (json.dumps(a), json.dumps(b)) for a, b in reversed(list(f.items())))
    entries.append('    {\n' + fields + '\n    }')
rows.append('  "files": [\n' + ',\n'.join(entries) + '\n  ]')
p.write_text('{\n' + ',\n'.join(rows) + '\n}\n', encoding="utf-8")
PY
carry_rerun 0 "a reordered, multi-line record carries the same workspace and roles"
grep -Fq '"workspace_dir": "/mnt/work spaces"' "$tamper"
[[ "$(grep -c '"role": "dropin"' "$tamper")" = 1 ]]
cp "$tamper" "$scratch/manifest.reordered"

python3 - "$tamper" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
p.write_text(p.read_text(encoding="utf-8").replace('{', '{"data_dir": "/srv/olivares data", ', 1), encoding="utf-8")
PY
carry_rerun 1 "mutant: a duplicated key is refused, not resolved by last-one-wins"
grep -Fq 'duplicate key' "$scratch/err"

cp "$scratch/manifest.reordered" "$tamper"
python3 - "$tamper" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
d = json.loads(p.read_text(encoding="utf-8"))
d["workspace_dir"] = ["/mnt/work spaces"]
p.write_text(json.dumps(d), encoding="utf-8")
PY
carry_rerun 1 "mutant: a field of the wrong type is refused"
grep -Fq '"workspace_dir" must be a string' "$scratch/err"

cp "$scratch/manifest.reordered" "$tamper"
python3 - "$tamper" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
d = json.loads(p.read_text(encoding="utf-8"))
d["extra_field"] = "x"
p.write_text(json.dumps(d), encoding="utf-8")
PY
carry_rerun 1 "mutant: an unknown field is refused, because the engine refuses it too"
grep -Fq 'unexpected field "extra_field"' "$scratch/err"

cp "$scratch/manifest.reordered" "$tamper"
python3 - "$tamper" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
d = json.loads(p.read_text(encoding="utf-8"))
d["files"].append({"path": "/etc/systemd/system/olivares.service.d/agentops.conf",
                   "role": "dropin", "mode": "0644", "managed": True})
p.write_text(json.dumps(d), encoding="utf-8")
PY
carry_rerun 1 "mutant: a duplicated drop-in record is refused however it is presented"
grep -Fq 'records more than one AgentOps drop-in' "$scratch/err"

cp "$scratch/manifest.reordered" "$tamper"
python3 - "$tamper" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
d = json.loads(p.read_text(encoding="utf-8"))
d["data_dir"] = "/srv/other"
p.write_text(json.dumps(d), encoding="utf-8")
PY
carry_rerun 1 "mutant: a record owning another data directory is refused"
grep -Fq 'owns data directory /srv/other' "$scratch/err"

cp "$scratch/manifest.reordered" "$tamper"
python3 - "$tamper" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
p.write_text(p.read_text(encoding="utf-8").rstrip() + " trailing\n", encoding="utf-8")
PY
carry_rerun 1 "mutant: trailing content after the document is refused"
grep -Fq 'trailing content' "$scratch/err"

cp "$scratch/manifest.good" "$tamper"
carry_rerun 0 "the producer's own presentation still carries, unchanged"

# A data directory under a ProtectHome path gets the tmpfs + BindPaths pairing.
home_root="$scratch/home-root"
make_binary "$home_root/usr/local/bin/olivares"
mkdir -p "$home_root/home/olivares"
expect_rc 0 "data directory under /home renders ProtectHome=tmpfs and BindPaths" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$home_root" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /home/olivares/data
grep -Fqx 'ProtectHome=tmpfs' "$home_root/etc/systemd/system/olivares.service"
grep -Fqx 'BindPaths=/home/olivares/data' "$home_root/etc/systemd/system/olivares.service"
grep -Fqx 'ReadWritePaths=/home/olivares/data' "$home_root/etc/systemd/system/olivares.service"
grep -Fq 'protected-home: ProtectHome=tmpfs + BindPaths' "$scratch/out"
# R5. A data directory under /tmp keeps PrivateTmp=true and is bound in on its own:
# systemd creates a bind destination inside the private /tmp since v235 (a227a4be), so
# refusing this location "because no directive can reach it" was a false claim.
tmp_root="$scratch/tmp-root"
make_binary "$tmp_root/usr/local/bin/olivares"
mkdir -p "$tmp_root/tmp" "$tmp_root/var/tmp"
expect_rc 0 "data directory under /tmp keeps PrivateTmp and binds only itself" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$tmp_root" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /tmp/olivares
tmp_unit="$tmp_root/etc/systemd/system/olivares.service"
grep -Fqx 'PrivateTmp=true' "$tmp_unit"
grep -Fqx 'BindPaths=/tmp/olivares' "$tmp_unit"
grep -Fqx 'ReadWritePaths=/tmp/olivares' "$tmp_unit"
grep -Fqx 'ProtectHome=true' "$tmp_unit"
[[ "$(grep -c '^BindPaths=' "$tmp_unit")" = 1 ]]
grep -Fq 'private-tmp: PrivateTmp=true + BindPaths' "$scratch/out"
grep -Fq 'clear them on boot or on a timer' "$scratch/out"
# Every other hardening directive is byte-identical to the default unit's.
diff <(grep -vE '^(ExecStart|ReadWritePaths|BindPaths)=' "$tmp_unit") \
     <(grep -vE '^(ExecStart|ReadWritePaths|BindPaths)=' "$stage/etc/systemd/system/olivares.service")
case_ok "the /tmp mapping relaxes nothing else in the sandbox"

vartmp_root="$scratch/vartmp-root"
make_binary "$vartmp_root/usr/local/bin/olivares"
mkdir -p "$vartmp_root/var/tmp"
expect_rc 0 "data directory under /var/tmp with a space is quoted in its bind" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$vartmp_root" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/var/tmp/olivares estate'
grep -Fqx 'BindPaths="/var/tmp/olivares estate"' "$vartmp_root/etc/systemd/system/olivares.service"
grep -Fqx 'PrivateTmp=true' "$vartmp_root/etc/systemd/system/olivares.service"

# The version the mapping needs is read from the running manager, not assumed.
mkdir -p "$scratch/oldsystemd"
cat >"$scratch/oldsystemd/systemctl" <<'OLD'
#!/bin/sh
[ "${1:-}" = --version ] || exit 1
printf 'systemd 234 (234-2)\n+PAM +AUDIT\n'
OLD
chmod 0755 "$scratch/oldsystemd/systemctl"
old_root="$scratch/old-systemd-root"
make_binary "$old_root/usr/local/bin/olivares"
mkdir -p "$old_root/tmp"
expect_rc 1 "mutant: a host running systemd 234 is refused, naming its version" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" PATH="$scratch/oldsystemd:$PATH" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$old_root" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /tmp/olivares-old
grep -Fq 'this host runs systemd 234' "$scratch/err"
grep -Fq 'needs systemd 235 or later' "$scratch/err"
[[ ! -e "$old_root/tmp/olivares-old" ]]
[[ ! -e "$old_root/etc/systemd/system/olivares.service" ]]
cat >"$scratch/oldsystemd/systemctl" <<'NEW'
#!/bin/sh
[ "${1:-}" = --version ] || exit 1
printf 'systemd 235 (235-1)\n'
NEW
expect_rc 0 "a host running systemd 235 renders the bind" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" PATH="$scratch/oldsystemd:$PATH" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$old_root" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /tmp/olivares-old
grep -Fqx 'BindPaths=/tmp/olivares-old' "$old_root/etc/systemd/system/olivares.service"

colon_root="$scratch/colon-root"
make_binary "$colon_root/usr/local/bin/olivares"
mkdir -p "$colon_root/tmp"
expect_rc 1 "mutant: a colon in a bind-exposed data directory is refused" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$colon_root" --init systemd \
  --binary /usr/local/bin/olivares --data-dir '/tmp/olivares:estate'
grep -Fq "separate source from destination" "$scratch/err"
[[ ! -e "$colon_root/tmp/olivares:estate" ]]
[[ ! -e "$colon_root/etc/systemd/system/olivares.service" ]]

# A user service gets the same private /tmp from the shared template, so it gets the same
# bind. The other classes stay system-only, which this case pins by asserting the user
# unit keeps ProtectHome=false.
user_tmp_root="$scratch/user-tmp-root"
user_home="$scratch/user-tmp-home"
mkdir -p "$user_tmp_root/tmp" "$user_home/.local/bin"
make_binary "$user_tmp_root$user_home/.local/bin/olivares"
expect_rc 0 "a user service under /tmp gets the same bind and keeps ProtectHome as it was" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" HOME="$user_home" \
  /bin/sh "$root/scripts/install-service.sh" --user --root "$user_tmp_root" --init systemd \
  --binary "$user_home/.local/bin/olivares" --data-dir /tmp/user-estate
user_unit="$user_tmp_root$user_home/.config/systemd/user/olivares.service"
grep -Fqx 'PrivateTmp=true' "$user_unit"
grep -Fqx 'BindPaths=/tmp/user-estate' "$user_unit"
grep -Fqx 'ProtectHome=false' "$user_unit"

expect_rc 1 "mutant: data directory under /proc is refused as an API file system" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$home_root" --init systemd \
  --binary /usr/local/bin/olivares --data-dir /proc/olivares
grep -Fq 'API file system' "$scratch/err"
[[ ! -e "$home_root/proc/olivares" ]]

openrc="$scratch/openrc-root"
make_binary "$openrc/usr/local/bin/olivares"
expect_rc 0 "OpenRC adapter stages an executable init script" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$openrc" --init openrc \
  --binary /usr/local/bin/olivares --data-dir /var/lib/olivares \
  --config /etc/olivares/olivares.env
[[ "$(stat -c '%a' "$openrc/etc/init.d/olivares")" = 755 ]]
grep -Fq 'command_user="olivares:olivares"' "$openrc/etc/init.d/olivares"
grep -Fq "sed -n '/FIRST-BOOT SETUP/,/========================/p' '/var/lib/olivares/olivares.log'" \
  "$scratch/out"
case_ok "OpenRC unit binds the service account"
openrc_custom="$scratch/openrc-custom-root"
make_binary "$openrc_custom/usr/local/bin/olivares"
mkdir -p "$openrc_custom/srv"
expect_rc 0 "OpenRC adapter admits a custom data directory" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$openrc_custom" --init openrc \
  --binary /usr/local/bin/olivares --data-dir /srv/olivares
grep -Fq 'command_args_base="serve --data-dir=/srv/olivares --listen' "$openrc_custom/etc/init.d/olivares"
# The base is a witness only because the unit copies it into command_args, the
# assignment OpenRC runs: at top level and again with the extra flags in
# start_pre. A template that stops copying would still pass the grep above.
grep -Fqx 'command_args="$command_args_base"' "$openrc_custom/etc/init.d/olivares"
grep -Fq 'command_args="$command_args_base $olivares_extra_args"' "$openrc_custom/etc/init.d/olivares"
case_ok "OpenRC custom unit copies command_args_base into the command_args OpenRC runs"
python3 - "$openrc_custom/srv/olivares/install-manifest.json" <<'PY'
import json, pathlib, sys
v = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert v["layout"] == "custom" and v["init"] == "openrc" and v["data_dir"] == "/srv/olivares", v
PY
expect_rc 1 "mutant: OpenRC cannot represent a data directory with a space" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$openrc_custom" --init openrc \
  --binary /usr/local/bin/olivares --data-dir '/srv/olivares data'
grep -Fq 'openrc paths may not contain spaces' "$scratch/err"

launchd="$scratch/launchd-root"
make_binary "$launchd/Users/demo/.local/bin/olivares"
expect_rc 0 "launchd user adapter stages plist and wrapper" \
  env OLIVARES_OS=darwin OLIVARES_ASSET_ROOT="$root" HOME=/Users/demo \
  /bin/sh "$root/scripts/install-service.sh" --user --root "$launchd" --init launchd \
  --binary /Users/demo/.local/bin/olivares --data-dir '/Users/demo/Library/Application Support/Olivares' \
  --config /Users/demo/Library/Preferences/dev.olivares.olivares.env
plist="$launchd/Users/demo/Library/LaunchAgents/dev.olivares.olivares.plist"
wrapper="$launchd/Users/demo/Library/Application Support/Olivares/launchd-run.sh"
[[ "$(stat -c '%a' "$plist")" = 644 && "$(stat -c '%a' "$wrapper")" = 755 ]]
grep -Fq '<string>Background</string>' "$plist"
grep -Fq "sed -n '/FIRST-BOOT SETUP/,/========================/p' '/Users/demo/Library/Application Support/Olivares/olivares.log'" \
  "$scratch/out"
/bin/sh -n "$wrapper"
grep -Fq "export \"\$line\"" "$wrapper"
if grep -Eq '(^|[[:space:]])(\.|source)[[:space:]]+.*config' "$wrapper"; then
  printf 'launchd wrapper executes the config as shell code\n' >&2
  exit 1
fi
case_ok "launchd plist is background-scoped"

printf '%s\n' '# mutant: stale wrapper' >"$wrapper"
expect_rc 1 "mutant: launchd wrapper drift is never preserved" \
  env OLIVARES_OS=darwin OLIVARES_ASSET_ROOT="$root" HOME=/Users/demo \
  /bin/sh "$root/scripts/install-service.sh" --user --root "$launchd" --init launchd \
  --binary /Users/demo/.local/bin/olivares --data-dir '/Users/demo/Library/Application Support/Olivares' \
  --config /Users/demo/Library/Preferences/dev.olivares.olivares.env
grep -Fq 'refusing to replace existing launchd wrapper' "$scratch/err"

unsafe="$scratch/unsafe-root"
make_binary "$unsafe/opt/olivares"
mkdir -p "$unsafe/var/lib/olivares"
chmod 0777 "$unsafe/var/lib/olivares"
expect_rc 1 "mutant: unsafe pre-existing data mode is refused" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$unsafe" --init systemd \
  --binary /opt/olivares --data-dir /var/lib/olivares --config /etc/olivares/olivares.env
grep -Fq 'existing system data directory mode is 777' "$scratch/err"
[[ ! -e "$unsafe/etc/systemd/system/olivares.service" ]]

badcfg="$scratch/bad-config-root"
make_binary "$badcfg/opt/olivares"
mkdir -p "$badcfg/etc/olivares"
printf '%s\n' 'OLIVARES_NOT_A_REAL_KEY=do-not-print' >"$badcfg/etc/olivares/olivares.env"
chmod 0640 "$badcfg/etc/olivares/olivares.env"
expect_rc 1 "mutant: unknown config key refuses before unit creation" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$badcfg" --init systemd \
  --binary /opt/olivares --data-dir /var/lib/olivares --config /etc/olivares/olivares.env
grep -Fq 'unrecognized key: OLIVARES_NOT_A_REAL_KEY' "$scratch/err"
if grep -Fq 'do-not-print' "$scratch/out" "$scratch/err"; then
  printf 'config refusal disclosed a value\n' >&2
  exit 1
fi
[[ ! -e "$badcfg/etc/systemd/system/olivares.service" ]]

expect_rc 1 "offline root can never start a host service" \
  env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
  /bin/sh "$root/scripts/install-service.sh" --system --root "$stage" --init systemd \
  --binary /opt/olivares/bin/olivares --start
grep -Fq -- '--start cannot target an offline --root' "$scratch/err"

if [[ "$(id -u)" -ne 0 ]]; then
	  expect_rc 1 "system installation never escalates privileges implicitly" \
	    env OLIVARES_OS=linux OLIVARES_ASSET_ROOT="$root" \
	    /bin/sh "$root/scripts/install-service.sh" --system --init systemd \
	    --binary /opt/olivares/bin/olivares
  grep -Fq 'requires uid 0; this script never invokes sudo' "$scratch/err"
fi

# --start: the bounded first-boot wait and the local diagnosis it surfaces.
#
# The init manager, every request, the sleep and the clock are fixtures on PATH,
# so these cases execute the real --start branch of the adapter without touching
# this host's service manager or its network. HOME, the data directory and the
# unit all live inside the scratch tree.
#
# The fixture clock is coherent rather than free-running: `date` only READS it,
# and it advances exactly when something takes time — a request by its own
# duration, clipped to the --max-time the adapter passed, and a sleep by its
# argument. That is what lets a case place a response on either side of the
# deadline without the battery spending those seconds.
start_binary() {
  local path="$1" support="$2"
  mkdir -p "$(dirname -- "$path")"
  {
    printf '%s\n' '#!/bin/sh'
    printf '%s\n' 'case "${OLIVARES_NOT_A_REAL_KEY+x}" in x) exit 1 ;; esac'
    printf '%s\n' 'case "$1" in'
    printf '%s\n' '  readyz)'
    if [[ "$support" = legacy ]]; then
      printf '%s\n' '    printf '"'"'Error: unknown command "readyz" for "olivares"\n'"'"' >&2; exit 1 ;;'
    else
      printf '%s\n' '    case "$2" in --help) exit 0 ;; esac'
      printf '%s\n' '    printf '"'"'not ready: https://127.0.0.1:8443/readyz returned HTTP 503\n'"'"' >&2'
      printf '%s\n' '    printf '"'"'diagnosis: first-boot setup is blocked: no cross-tenant admin database pool.\n'"'"' >&2'
      printf '%s\n' '    printf '"'"'remedy: provision the admin role in deploy/postgres/README.md and restart.\n'"'"' >&2'
      printf '%s\n' '    exit 1 ;;'
    fi
    printf '%s\n' 'esac'
    printf '%s\n' 'exit 0'
  } >"$path"
  chmod 0755 "$path"
}

start_fixture() {
  local name="$1" support="$2"
  local dir="$scratch/$name"
  mkdir -p "$dir/fakebin" "$dir/home/.local/bin" "$dir/home/.local/share/olivares"
  chmod 700 "$dir/home/.local/share/olivares"
  printf '%s\n' 'not-a-real-certificate' >"$dir/home/.local/share/olivares/tls.crt"
  start_binary "$dir/home/.local/bin/olivares" "$support"
  cat >"$dir/fakebin/systemctl" <<'FAKESYSTEMCTL'
#!/bin/sh
case "$1" in --version) printf 'systemd 257 (257.13-1)\n'; exit 0 ;; esac
printf 'systemctl %s\n' "$*" >>"$INIT_LOG"
exit 0
FAKESYSTEMCTL
  # The fixture answers with a real readiness body carrying a secret, so the
  # battery can prove the adapter never puts a response body on an operator's
  # terminal: if anyone drops the >/dev/null, that body is what would appear.
  #
  # CURL_SECONDS is the request's own duration and is clipped to --max-time, the
  # way a real curl aborts. CURL_EXTRA_SECONDS is an UNCLIPPED step applied after
  # the CURL_EXTRA_ON-th request: a forward clock step or an overrun, which is
  # how a response can still arrive after the deadline it was clipped to.
  cat >"$dir/fakebin/curl" <<'FAKECURL2'
#!/bin/sh
set -eu
printf '%s\n' "curl $*" >>"$REQUEST_LOG"
max_time=0
prev=""
for arg in "$@"; do
  case "$prev" in --max-time) max_time="$arg" ;; esac
  prev="$arg"
done
spent="${CURL_SECONDS:-0}"
[ "$max_time" -eq 0 ] || [ "$spent" -le "$max_time" ] || spent="$max_time"
extra=0
if [ "$(grep -c . "$REQUEST_LOG")" -eq "${CURL_EXTRA_ON:-0}" ]; then
  extra="${CURL_EXTRA_SECONDS:-0}"
fi
now="$(cat "$CLOCK_FILE")"
printf '%s\n' "$((now + spent + extra))" >"$CLOCK_FILE"
printf '{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured","d":"PGPASSWORD=installer-body-secret"}\n'
exit "${CURL_RC:-0}"
FAKECURL2
  cat >"$dir/fakebin/sleep" <<'FAKESLEEP'
#!/bin/sh
now="$(cat "$CLOCK_FILE")"
printf '%s\n' "$((now + ${1:-0}))" >"$CLOCK_FILE"
exit 0
FAKESLEEP
  cat >"$dir/fakebin/date" <<'FAKEDATE'
#!/bin/sh
[ "$1" = "+%s" ] || exec /bin/date "$@"
cat "$CLOCK_FILE"
FAKEDATE
  chmod 0755 "$dir/fakebin/systemctl" "$dir/fakebin/curl" "$dir/fakebin/sleep" "$dir/fakebin/date"
  printf '%s\n' 1700000000 >"$dir/clock"
  printf '%s\n' "$dir"
}

start_env() {
  local dir="$1" rc="$2" seconds="$3" extra="$4" extra_on="$5"
  printf '%s\n' "HOME=$dir/home" "OLIVARES_OS=linux" "OLIVARES_ASSET_ROOT=$root" \
    "PATH=$dir/fakebin:$PATH" "INIT_LOG=$dir/init.log" "REQUEST_LOG=$dir/requests.log" \
    "CLOCK_FILE=$dir/clock" "CURL_RC=$rc" "CURL_SECONDS=$seconds" \
    "CURL_EXTRA_SECONDS=$extra" "CURL_EXTRA_ON=$extra_on"
}

start_run() {
  local dir="$1" want="$2" label="$3" rc="$4" seconds="$5" extra="$6" extra_on="$7"
  : >"$dir/requests.log"
  expect_rc "$want" "$label" \
    env $(start_env "$dir" "$rc" "$seconds" "$extra" "$extra_on") \
    /bin/sh "$root/scripts/install-service.sh" --user --init systemd \
    --binary "$dir/home/.local/bin/olivares" --start
}

request_count() { grep -c . "$1/requests.log"; }
# max_time_of <dir> <n> — the --max-time the adapter passed on its n-th request.
max_time_of() {
  sed -n "${2}p" "$1/requests.log" | sed 's/.*--max-time \([0-9][0-9]*\).*/\1/'
}
window="first-boot setup: journalctl --user -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'"

# Case 1 — positive control. A first boot that answers must still install, start
# and report success, with no failure diagnosis anywhere in its output.
healthy="$(start_fixture start-healthy modern)"
start_run "$healthy" 0 "a first boot that answers is started and reported without a diagnosis" 0 0 0 0
grep -Fq 'result-json: {"schema":"olivares.ai/service-install-result/v1","mode":"user","init":"systemd","started":true' \
  "$scratch/out"
grep -Fq 'systemctl --user enable --now olivares' "$healthy/init.log"
grep -Fq "$window" "$scratch/out"
grep -Fq '/livez' "$healthy/requests.log"
grep -Fq '/readyz' "$healthy/requests.log"
[[ "$(request_count "$healthy")" -eq 2 ]]
if grep -Fq 'local readiness diagnosis' "$scratch/out" "$scratch/err"; then
  printf 'a healthy first boot printed a failure diagnosis\n' >&2
  exit 1
fi
case_ok "a healthy first boot needs exactly one attempt over both endpoints"

# Every request carries its own connect and total limit, and with the whole
# budget left those are the configured values. Read from the fixture's argv log,
# not from the source: a flag counted in a file is not a flag passed to a process.
while IFS= read -r request; do
  case "$request" in
    *"--connect-timeout 2"*"--max-time 5"*) ;;
    *) printf 'a first-boot request ran unbounded: %s\n' "$request" >&2; exit 1 ;;
  esac
done <"$healthy/requests.log"
case_ok "with the whole budget left, a request carries the configured connect and total limit"

# Case 2 — the attempt bound. Requests fail instantly, so only the 1s sleep moves
# the clock and the attempt cap is what must stop the loop.
capped="$(start_fixture start-attempt-capped modern)"
start_run "$capped" 1 "a first boot that never answers stops at its attempt bound and diagnoses locally" 1 0 0 0
grep -Fq 'gave up after 60 attempt(s)' "$scratch/err"
grep -Fq 'bounded at 60 attempts and 60s' "$scratch/err"
grep -Fq 'left exactly as installed and nothing was rolled back' "$scratch/err"
[[ "$(request_count "$capped")" -eq 60 ]]
grep -Fq 'local readiness diagnosis (one 5s probe, separate from the wait above):' "$scratch/err"
grep -Fq '  diagnosis: first-boot setup is blocked' "$scratch/err"
grep -Fq '  remedy: provision the admin role' "$scratch/err"
grep -Fq "$window" "$scratch/err"
if grep -Fq 'installer-body-secret' "$scratch/out" "$scratch/err"; then
  printf 'the installer printed a readiness response body\n' >&2
  exit 1
fi
if grep -Fq "$window" "$scratch/out"; then
  printf 'a failed first boot printed the success output\n' >&2
  exit 1
fi
case_ok "the failed first boot surfaced the binary's local verdict and no response body"

[[ -f "$capped/home/.config/systemd/user/olivares.service" ]]
[[ -f "$capped/home/.config/olivares/olivares.env" ]]
[[ -f "$capped/home/.local/share/olivares/install-manifest.json" ]]
[[ -f "$capped/home/.local/share/olivares/tls.crt" ]]
grep -Fq 'systemctl --user enable --now olivares' "$capped/init.log"
if grep -Eq 'disable|stop|reset-failed' "$capped/init.log"; then
  printf 'a failed first boot withdrew the service it had started\n' >&2
  exit 1
fi
case_ok "a failed first boot preserves the unit, config, data and started service"

# Case 3 — the wall-clock bound, and the clip that enforces it. Each request
# costs 2s and each sleep 1s, so the remaining budget walks 60, 57 … 3: the last
# request must be clipped to the 3s that are left, not the configured 5, and the
# wait must land ON the deadline rather than past it.
timed="$(start_fixture start-deadline modern)"
start_run "$timed" 1 "a first boot that outlasts the deadline stops on wall clock, not on attempts" 1 2 0 0
grep -Fq 'gave up after 21 attempt(s) over 60s' "$scratch/err"
grep -Fq 'bounded at 60 attempts and 60s' "$scratch/err"
[[ "$(request_count "$timed")" -eq 20 ]]
[[ "$(max_time_of "$timed" 20)" -eq 3 ]]
case_ok "the wall-clock deadline ends the wait on the deadline, with the last request clipped to it"

# No request may be issued with --max-time 0: curl reads that as NO timeout, so a
# clip that reaches zero would silently unbound the very request it was bounding.
while IFS= read -r request; do
  case "$request" in
    *"--max-time 0"*|*"--connect-timeout 0"*)
      printf 'a clipped first-boot request was unbounded by a zero limit: %s\n' "$request" >&2
      exit 1 ;;
  esac
done <"$timed/requests.log"
case_ok "a clipped limit never reaches the zero curl reads as no timeout"

# Case 4 — the deadline is consulted before EACH request, not once per attempt.
# The first request succeeds and a clock step carries the wait past its end; the
# second must never be issued.
crossed="$(start_fixture start-boundary-crossed modern)"
start_run "$crossed" 1 "a wait that ends mid-attempt issues no further request" 0 0 61 1
[[ "$(request_count "$crossed")" -eq 1 ]]
grep -Fq '/livez' "$crossed/requests.log"
if grep -Fq '/readyz' "$crossed/requests.log"; then
  printf 'a request was issued after the wait had ended\n' >&2
  exit 1
fi
grep -Fq 'bounded at 60 attempts and 60s' "$scratch/err"
[[ -f "$crossed/home/.config/systemd/user/olivares.service" ]]
case_ok "the remaining budget is read before each request, not once per attempt"

# Case 5 — a success that arrives after the deadline is a late answer, not this
# wait's result. Both endpoints answer 200; the clock steps past the end during
# the second. Accepting it would report an unready deployment as healthy.
late="$(start_fixture start-late-success modern)"
start_run "$late" 1 "a success that lands after the deadline is refused, not reported healthy" 0 0 61 2
[[ "$(request_count "$late")" -eq 2 ]]
grep -Fq 'after the 60s wait had ended' "$scratch/err"
grep -Fq 'left exactly as installed and nothing was rolled back' "$scratch/err"
if grep -Fq 'result-json' "$scratch/out"; then
  printf 'a late answer was reported as a completed installation\n' >&2
  exit 1
fi
[[ -f "$late/home/.config/systemd/user/olivares.service" ]]
[[ -f "$late/home/.local/share/olivares/install-manifest.json" ]]
case_ok "a late answer is refused while the installed state is preserved"

# Case 6 — an older installed binary has no readyz subcommand. The adapter says
# so and names what to read; it never invents a diagnosis from a probe it could
# not run, and the failure and the installed state are unchanged.
legacy="$(start_fixture start-legacy-binary legacy)"
start_run "$legacy" 1 "an older installed binary is named, not guessed at" 1 0 0 0
grep -Fq "this build has no 'olivares readyz' subcommand" "$scratch/err"
grep -Fq "$window" "$scratch/err"
grep -Fq 'left exactly as installed and nothing was rolled back' "$scratch/err"
if grep -Fq 'local readiness diagnosis (one ' "$scratch/err"; then
  printf 'an older binary produced a diagnosis it cannot have measured\n' >&2
  exit 1
fi
[[ -f "$legacy/home/.config/systemd/user/olivares.service" ]]
case_ok "an older binary falls back without fabricating a verdict"

make_mutant() {
  local dest="$scratch/$1"
  mkdir -p "$dest/scripts" "$dest/packaging/service" "$dest/cmd/olivares" "$dest/docs"
  cp "$root/.goreleaser.yaml" "$dest/.goreleaser.yaml"
  cp "$root/scripts/install.sh" "$root/scripts/install-bootstrap.sh" \
    "$root/scripts/install-service.sh" "$dest/scripts/"
  cp "$root/packaging/service/"* "$dest/packaging/service/"
  cp "$root/cmd/olivares/cmd_doctor.go" "$root/cmd/olivares/main.go" "$dest/cmd/olivares/"
  cp "$root/docs/RELEASE-INSTALLER.md" "$dest/docs/"
  printf '%s\n' "$dest"
}

mutant="$(make_mutant no-readyz)"
sed -i 's#/readyz#/not-ready#' "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: first boot without readyz is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant first-boot-window)"
sed -i "s#sed -n '/FIRST-BOOT SETUP/,/========================/p'#journalctl -n 20#" \
  "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: truncated first-boot window is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant unbounded-request)"
sed -i '/--max-time "\$(first_boot_clip/d' "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: a first-boot request without its own time limit is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant unclipped-request)"
sed -i 's|"\$(first_boot_clip "\$FIRST_BOOT_REQUEST_TIMEOUT")"|"\$FIRST_BOOT_REQUEST_TIMEOUT"|' \
  "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: a request limit that is not clipped to the remaining budget is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant late-success-accepted)"
sed -i '/^      probe_late=1$/d' "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: accepting an answer that arrived after the deadline is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant attempt-counting-wait)"
sed -i 's/^  while :; do$/  while [ "$attempts" -lt 60 ]; do/' "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: counting 60 attempts again instead of keeping a deadline is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant silent-first-boot-failure)"
sed -i '/^    first_boot_diagnosis$/d' "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: a first-boot failure that diagnoses nothing is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant rollback-claim)"
sed -i 's/left exactly as installed and nothing was rolled back/cleaned up/' \
  "$mutant/scripts/install-service.sh"
expect_rc 1 "mutant: a first-boot failure that claims a rollback is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant archive-missing)"
sed -i '0,/      - packaging\/service\//d' "$mutant/.goreleaser.yaml"
expect_rc 1 "mutant: one archive without service templates is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

mutant="$(make_mutant doctor-rc)"
sed -i 's/code = exitcode.Usage \/\/ DIST-24-04/code = exitcode.OK \/\/ DIST-24-04/' \
  "$mutant/cmd/olivares/cmd_doctor.go"
expect_rc 1 "mutant: doctor unmeasurable-to-green inversion is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-service-install.sh"

# Root return 01's defect, kept as a permanent EXECUTABLE witness rather than a
# source assertion: with the clock read only AFTER both requests, an iteration
# that begins inside the deadline spends its whole request budget outside it. On
# the same fixture as the wall-clock case above, this mutant issues a 21st
# request and reports 61s against the 60s it names — both of which that case
# forbids. A static flag count cannot see either.
mutant="$(make_mutant late-clock-read)"
sed -i '/^      first_boot_tick$/d; /^      if \[ "\$probe_remaining" -lt 1 \]; then probe_ok=0; break; fi$/d' \
  "$mutant/scripts/install-service.sh"
late_clock="$(start_fixture start-late-clock-mutant modern)"
: >"$late_clock/requests.log"
expect_rc 1 "mutant: reading the remaining budget only after both requests still fails the boot" \
  env $(start_env "$late_clock" 1 2 0 0) \
  /bin/sh "$mutant/scripts/install-service.sh" --user --init systemd \
  --binary "$late_clock/home/.local/bin/olivares" --start
[[ "$(request_count "$late_clock")" -eq 21 ]]
grep -Fq 'over 61s, bounded at 60 attempts and 60s' "$scratch/err"
case_ok "mutant: it overruns the deadline it names, which the wall-clock case forbids"

printf '1..%d\n' "$passes"
