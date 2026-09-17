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
grep -Fq 'have cosign || err' "$root/scripts/install.sh" || fail "second stage must require cosign"
grep -Fq 'have cosign || err' "$root/scripts/install-bootstrap.sh" || fail "bootstrap must require cosign"
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
