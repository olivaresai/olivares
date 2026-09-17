#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -uo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
failures=0
fail() { printf 'FAIL: %s\n' "$*" >&2; failures=$((failures + 1)); }
blind() { printf 'UNVERIFIED: %s\n' "$*" >&2; exit 2; }
require_file() { [[ -f "$root/$1" ]] || fail "missing $1"; }

for tool in bash grep python3; do
  command -v "$tool" >/dev/null 2>&1 || blind "required tool is unavailable: $tool"
done

for path in \
  scripts/install.sh \
  scripts/install-bootstrap.sh \
  scripts/render-release-installer.sh \
  deploy/distribution/install-endpoints.json \
  scripts/assert-cosign-binary.sh \
  docs/RELEASE-INSTALLER.md; do
  require_file "$path"
done

for path in scripts/install.sh scripts/install-bootstrap.sh scripts/render-release-installer.sh; do
  [[ -x "$root/$path" ]] || fail "$path is not executable"
done
/bin/sh -n "$root/scripts/install.sh" || fail "scripts/install.sh does not parse as POSIX shell"
/bin/sh -n "$root/scripts/install-bootstrap.sh" || fail "scripts/install-bootstrap.sh does not parse as POSIX shell"
bash -n "$root/scripts/render-release-installer.sh" || fail "renderer does not parse as bash"

marker='@OLIVARES_INSTALLER_VERSION@'
[[ "$(grep -Foc "$marker" "$root/scripts/install.sh")" -eq 1 ]] ||
  fail "installer source must contain exactly one render marker"
for path in scripts/install.sh scripts/install-bootstrap.sh; do
  grep -Fq 'resolve_cosign' "$root/$path" || fail "$path must resolve cosign (PATH, or the pinned temporary copy)"
  # shellcheck disable=SC2016 # the literal text of the call is what the contract pins
  grep -Fq '"$cosign_bin" verify-blob' "$root/$path" || fail "$path must verify with the resolved cosign"
  if grep -Fq 'have cosign || err' "$root/$path"; then
    fail "$path refuses when cosign is absent instead of using the pinned temporary copy"
  fi
done
# The cosign the installers fetch when none is on PATH must be the one this repository
# approves: same version, same SHA-256 per platform as scripts/assert-cosign-binary.sh.
if ! python3 - "$root" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
approved_text = (root / "scripts/assert-cosign-binary.sh").read_text(encoding="utf-8")
table = approved_text.split("<<'DIGESTS'", 1)[1].split("\nDIGESTS\n", 1)[0]
approved = {}
for line in table.strip().splitlines():
    digest, name = line.split()
    approved[name] = digest
version = re.search(r"cosign/releases/download/(v[0-9]+\.[0-9]+\.[0-9]+)/cosign_checksums\.txt", approved_text).group(1)
platforms = ["linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"]
for script in ("scripts/install.sh", "scripts/install-bootstrap.sh"):
    text = (root / script).read_text(encoding="utf-8")
    assert f"COSIGN_VERSION='{version}'" in text, f"{script}: COSIGN_VERSION is not {version}"
    body = text.split("cosign_digest() {", 1)[1].split("\n}\n", 1)[0]
    rows = dict(re.findall(r"^\s+([a-z0-9]+-[a-z0-9]+)\) printf '%s' ([0-9a-f]{64}) ;;$", body, re.M))
    assert sorted(rows) == sorted(platforms), f"{script}: pinned platforms {sorted(rows)}"
    for platform in platforms:
        assert rows[platform] == approved[f"cosign-{platform}"], f"{script}: {platform} digest differs from the approved table"
PY
then
  fail "installer cosign pins must equal scripts/assert-cosign-binary.sh (version and per-platform SHA-256)"
fi
grep -Fq 'non-interactive installation must pin --version' "$root/scripts/install-bootstrap.sh" ||
  fail "bootstrap must pin versions in non-interactive use"
grep -Fq 'bootstrap trust: this response is trusted through HTTPS' "$root/scripts/install-bootstrap.sh" ||
  fail "bootstrap must disclose its own HTTPS trust boundary"
if grep -nE 'OLIVARES_SKIP_COSIGN|have sudo|sudo (mv|install|cp)|exec sudo' \
  "$root/scripts/install.sh" "$root/scripts/install-bootstrap.sh" >/dev/null; then
  fail "installer contains a verification bypass or implicit sudo path"
fi

if ! python3 - "$root/deploy/distribution/install-endpoints.json" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as handle:
    value = json.load(handle)
assert value["schema"] == "olivares.ai/install-endpoints/v1"
assert value["status"] == "not-live"
assert value["source"] == "scripts/install-bootstrap.sh"
routes = {route["path"]: route for route in value["routes"]}
assert set(routes) == {"/install.sh", "/get"}
assert routes["/install.sh"] == {
    "path": "/install.sh",
    "behavior": "serve-source",
    "content_type": "text/x-shellscript; charset=utf-8",
}
assert routes["/get"] == {
    "path": "/get",
    "behavior": "redirect",
    "location": "/install.sh",
    "status_code": 307,
}
PY
then
  fail "install endpoint contract is invalid or claims to be live"
fi

if ! python3 - "$root/.goreleaser.yaml" <<'PY'
import pathlib
import sys

text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
hook = "- bash scripts/render-release-installer.sh {{ .Version }} dist/olivares-install-{{ .Version }}.sh {{ .IsSnapshot }}"
assert text.count(hook) == 1

def section(name: str) -> str:
    start = text.index(f"\n{name}:\n") + 1
    rest = text[start:]
    lines = rest.splitlines()
    kept = [lines[0]]
    for line in lines[1:]:
        if line and not line[0].isspace() and line.endswith(":"):
            break
        kept.append(line)
    return "\n".join(kept)

# A `before` hook writing into dist/ is refused by GoReleaser's later empty-dist check.
assert section("before").count(hook) == 0
assert section("builds").count(hook) == 1

glob = "- glob: dist/olivares-install-*.sh"
assert section("checksum").count(glob) == 1
assert section("release").count(glob) == 1
PY
then
  fail "GoReleaser must render, checksum and upload exactly one versioned installer asset"
fi

grep -Fq 'HTTPS bootstrap is a convenience path, not a pre-verification of its own bytes' \
  "$root/docs/RELEASE-INSTALLER.md" || fail "installer guide must state the bootstrap trust boundary"
grep -Fq "status is \`not-live\`" "$root/docs/RELEASE-INSTALLER.md" ||
  fail "installer guide must not claim the routes are already delegated"
if grep -nE 'one verified command|unless you (set|opt out)|OLIVARES_SKIP_COSIGN' \
  "$root/README.md" "$root/INSTALL.md" >/dev/null; then
  fail "public install copy overclaims curl-pipe verification or documents a bypass"
fi

if [[ "$failures" -ne 0 ]]; then
  printf 'release installer contract: %d failure(s)\n' "$failures" >&2
  exit 1
fi
printf 'release installer contract: OK\n'
