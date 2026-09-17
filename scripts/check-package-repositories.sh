#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Static, network-free half of DIST-24-06. The behavioural half is
# test-package-repositories.sh; the hosted workflow owns real native clients.
set -uo pipefail
LC_ALL=C
export LC_ALL

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
failures=0
fail() { printf 'package repository contract: FAIL — %s\n' "$*" >&2; failures=$((failures + 1)); }
blind() { printf 'package repository contract: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

for tool in bash grep python3; do
	command -v "$tool" >/dev/null 2>&1 || blind "required tool is unavailable: $tool"
done
files=(
	.github/workflows/package-repositories.yml
	scripts/package_repository_lib.py
	scripts/package_repository_test_fixtures.py
	scripts/render-package-repositories.py
	scripts/verify-package-repositories.py
	scripts/generate-package-repository-test-key.sh
	scripts/package-repository-client-ci.sh
	scripts/check-package-repositories.sh
	scripts/test-package-repositories.sh
	INSTALL.md
	docs/RELEASE-INSTALLER.md
	docs-site/src/content/docs/how-to/install-from-packages.md
)
for path in "${files[@]}"; do
	[[ -f "$root/$path" ]] || fail "missing $path"
done
for path in \
	scripts/generate-package-repository-test-key.sh \
	scripts/package-repository-client-ci.sh \
	scripts/check-package-repositories.sh \
	scripts/test-package-repositories.sh; do
	bash -n "$root/$path" || fail "$path does not parse as bash"
done

python3 - "$root" <<'PY' || fail 'producer/verifier/workflow/task/document contract is broken'
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
python_files = (
    "scripts/package_repository_lib.py",
    "scripts/package_repository_test_fixtures.py",
    "scripts/render-package-repositories.py",
    "scripts/verify-package-repositories.py",
)
for name in python_files:
    source = (root / name).read_text(encoding="utf-8")
    compile(source, name, "exec")

renderer = (root / "scripts/render-package-repositories.py").read_text(encoding="utf-8")
verifier = (root / "scripts/verify-package-repositories.py").read_text(encoding="utf-8")
keygen = (root / "scripts/generate-package-repository-test-key.sh").read_text(encoding="utf-8")
client = (root / "scripts/package-repository-client-ci.sh").read_text(encoding="utf-8")
battery = (root / "scripts/test-package-repositories.sh").read_text(encoding="utf-8")
workflow = (root / ".github/workflows/package-repositories.yml").read_text(encoding="utf-8")
taskfile = (root / "Taskfile.yml").read_text(encoding="utf-8")
hook = (root / ".githooks/pre-push").read_text(encoding="utf-8")
mainline = (root / ".github/workflows/mainline-ci.yml").read_text(encoding="utf-8")

for token in (
    "Packages.gz", "Release.gpg", "InRelease", "pool/main/o/olivares",
    "repodata", "repomd.xml.asc", "primary.xml.gz", "APKINDEX.tar.gz",
    ".SIGN.RSA256.", "repository-manifest.json", 'choices=("stable", "security")',
):
    assert token in renderer, token
for token in (
    "OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE",
    "OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE",
    "OLIVARES_PACKAGE_REPO_APK_PRIVATE_KEY_FILE",
    "OLIVARES_PACKAGE_REPO_TEST_ONLY",
    "NO PUEDO FIRMAR",
):
    assert token in renderer, token
assert "exit 2" in keygen and "TEST ONLY" in keygen
for token in (
    "externally trusted OpenPGP public key", "externally trusted APK public key",
    "byte-identical", "manifest identity/version/channel differs", "six native package records",
):
    assert token in verifier, token

for label in (
    "mutant-wrong-signing-key", "mutant-package-without-index",
    "mutant-stable-serves-security", "mutant-real-key-absent",
):
    assert label in battery, label
assert "4/4 mutants red with positive controls" in battery
for token in (
    'apt-get -y install "olivares=$version"', "repo_gpgcheck=1", "dnf -y install",
    "apk update", 'apk add "olivares=$version"', "apk fetch", "cmp -s",
    "/repository/keys/olivares-package-repository.asc",
):
    assert token in client, token
assert "--allow-untrusted" not in client

assert "workflow_dispatch:" in workflow and "pull_request:" not in workflow
guard = "if: github.repository == 'olivaresai/olivares' || vars.OLIVARES_RELEASE_PROFILE == 'preprod'"
assert workflow.count(guard) == 1
assert "TEST-ONLY-package-repositories" in workflow
assert "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" in workflow
for forbidden in ("wrangler ", "aws s3", "rclone ", "gh release upload", "pages deploy"):
    assert forbidden not in workflow.lower(), forbidden

for target in ("lint:package-repos", "lint:package-repos:selftest"):
    assert f"  {target}:" in taskfile
    assert f"task {target}" in hook
    assert f"run: task {target}" in mainline

docs = [
    (root / "INSTALL.md").read_text(encoding="utf-8"),
    (root / "docs/RELEASE-INSTALLER.md").read_text(encoding="utf-8"),
    (root / "docs-site/src/content/docs/how-to/install-from-packages.md").read_text(encoding="utf-8"),
]
for document in docs:
    assert "DIST-24-06 proposed repositories" in document
    assert "No package-repository URL is live" in document
PY

shellcheck_bin="$(bash "$root/scripts/ensure-shellcheck.sh")" || blind 'cannot provision the pinned shellcheck'
[[ "$shellcheck_bin" == /* && -x "$shellcheck_bin" ]] || blind 'pinned shellcheck path is not executable'
"$shellcheck_bin" \
	"$root/scripts/generate-package-repository-test-key.sh" \
	"$root/scripts/package-repository-client-ci.sh" \
	"$root/scripts/check-package-repositories.sh" \
	"$root/scripts/test-package-repositories.sh" || fail 'shellcheck rejected a DIST-24-06 script'

if [[ "$failures" -ne 0 ]]; then
	printf 'package repository contract: %d failure(s)\n' "$failures" >&2
	exit 1
fi
printf '%s\n' 'package repository contract: OK — producers, trust domain, clients, workflow and honesty wiring'
