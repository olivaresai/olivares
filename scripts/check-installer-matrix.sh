#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Static DIST-24-05 wiring contract. Behavioural decisions live in the companion
# fixture battery and the hosted workflow; this prevents either cable disappearing.
set -uo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
failures=0
fail() { printf 'FAIL: %s\n' "$*" >&2; failures=$((failures + 1)); }
blind() { printf 'UNVERIFIED: %s\n' "$*" >&2; exit 2; }

for tool in bash grep python3; do
	command -v "$tool" >/dev/null 2>&1 || blind "required tool is unavailable: $tool"
done

for path in \
	.github/workflows/installer-matrix.yml \
	scripts/installer-matrix-lib.sh scripts/installer-matrix-ci.sh \
	scripts/check-installer-matrix.sh scripts/test-installer-matrix.sh \
	INSTALL.md docs/RELEASE-INSTALLER.md \
	docs-site/src/content/docs/how-to/install-from-packages.md; do
	[[ -f "$root/$path" ]] || fail "missing $path"
done
for path in scripts/installer-matrix-lib.sh scripts/installer-matrix-ci.sh \
	scripts/check-installer-matrix.sh scripts/test-installer-matrix.sh; do
	bash -n "$root/$path" || fail "$path does not parse as bash"
done

python3 - "$root" <<'PY' || fail "installer matrix workflow/task/document contract is broken"
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
workflow = (root / ".github/workflows/installer-matrix.yml").read_text(encoding="utf-8")
ci = (root / "scripts/installer-matrix-ci.sh").read_text(encoding="utf-8")
test = (root / "scripts/test-installer-matrix.sh").read_text(encoding="utf-8")
taskfile = (root / "Taskfile.yml").read_text(encoding="utf-8")
hook = (root / ".githooks/pre-push").read_text(encoding="utf-8")
mainline = (root / ".github/workflows/mainline-ci.yml").read_text(encoding="utf-8")
docs = "\n".join((root / p).read_text(encoding="utf-8") for p in (
    "INSTALL.md", "docs/RELEASE-INSTALLER.md",
    "docs-site/src/content/docs/how-to/install-from-packages.md",
))

for token in ("workflow_dispatch:", "pull_request:", "permissions:\n  contents: read"):
    assert token in workflow
guard = "if: github.repository == 'olivaresai/olivares' || vars.OLIVARES_RELEASE_PROFILE == 'preprod'"
assert workflow.count(guard) == 2
for family, image in (
    ("debian", "debian:stable-slim"), ("ubuntu", "ubuntu:24.04"),
    ("fedora", "fedora:latest"), ("opensuse-leap", "opensuse/leap:latest"),
    ("alpine", "alpine:latest"),
):
    assert f"family: {family}" in workflow and f"image: {image}" in workflow
# RUNNER PLACEMENT, pinned by exact source (R112). The Linux leg honors a configured
# pool only on the maintainer dispatch: the `pull_request` trigger can carry fork code,
# which pr-ci.yml's header forbids from reaching the self-hosted runners, and an
# unconfigured repository keeps the hosted default on every event. Pinning the whole
# expression is what makes those three properties testable from source without a
# dispatch; a looser substring would pass on a selector that dropped the event scope.
linux_runner = ("runs-on: ${{ github.event_name == 'workflow_dispatch'"
                " && vars.CI_RUNNER || 'ubuntu-latest' }}")
assert linux_runner in workflow
# macOS stays NATIVE and is never routed anywhere: a Linux runner cannot qualify a macOS
# installer. Both halves are asserted — the literal survives, and no runner selector
# reaches the job that owns it.
assert "runs-on: macos-14" in workflow
assert workflow.count("macos-installer:") == 1
assert "vars.CI_RUNNER" not in workflow.split("macos-installer:")[1]
# Exactly two jobs, so a third runner (or a leg silently duplicated onto Linux) is caught.
assert workflow.count("runs-on:") == 2
assert workflow.count("bash scripts/installer-matrix-ci.sh") == 2
assert "cosign-release: 'v2.6.4'" in workflow

for token in (
    'scripts/install.sh" --version "v$release_version"',
    '"$installed" version', ' doctor ', ' -o json', '--start',
    'bash "$lib" binary', 'bash "$lib" doctor', 'bash "$lib" redacted',
    'bash "$lib" no-sudo', 'NO HE PODIDO MIRAR',
):
    assert token in ci
for mutant in range(1, 7):
    roman = ("i", "ii", "iii", "iv", "v", "vi")[mutant - 1]
    assert f"mutant {roman}:" in test
assert "CI-ONLY:" in test

for target in ("lint:installer-matrix", "lint:installer-matrix:selftest"):
    assert f"  {target}:" in taskfile
    assert f"task {target}" in hook
    assert f"run: task {target}" in mainline
assert docs.count("DIST-24-05 qualification") == 3
assert "does not certify native package-manager installation" in docs
PY

if grep -nE '(^|[;&|])[[:space:]]*sudo[[:space:]]+' \
	"$root/scripts/installer-matrix-ci.sh" "$root/.github/workflows/installer-matrix.yml" >/dev/null; then
	fail "matrix contains an executable implicit-sudo path"
fi

if [[ "$failures" -ne 0 ]]; then
	printf 'installer matrix contract: %d failure(s)\n' "$failures" >&2
	exit 1
fi
printf 'installer matrix contract: OK\n'
