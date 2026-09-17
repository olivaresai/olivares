#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Static, network-free DIST-24-06 F2 publication contract.
set -euo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
for tool in bash grep python3; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'package publish contract: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done

files=(
	.github/workflows/publish-packages.yml
	packaging/repositories/production-key-descriptor.json
	scripts/materialize-package-repository-key.sh
	scripts/assemble-package-repository-publish-tree.py
	scripts/publish-package-repositories.sh
	scripts/package-repository-client-ci.sh
	scripts/check-package-publish.sh
	scripts/test-package-publish.sh
)
for path in "${files[@]}"; do
	[[ -f "$root/$path" ]] || {
		printf 'package publish contract: FAIL — missing %s\n' "$path" >&2
		exit 1
	}
done
for path in \
	scripts/materialize-package-repository-key.sh \
	scripts/publish-package-repositories.sh \
	scripts/package-repository-client-ci.sh \
	scripts/check-package-publish.sh \
	scripts/test-package-publish.sh; do
	bash -n "$root/$path"
done

python3 - "$root" <<'PY'
import json
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
read = lambda name: (root / name).read_text(encoding="utf-8")
workflow = read(".github/workflows/publish-packages.yml")
publisher = read("scripts/publish-package-repositories.sh")
materializer = read("scripts/materialize-package-repository-key.sh")
assembler = read("scripts/assemble-package-repository-publish-tree.py")
client = read("scripts/package-repository-client-ci.sh")
taskfile = read("Taskfile.yml")
hook = read(".githooks/pre-push")
mainline = read(".github/workflows/mainline-ci.yml")

descriptor = json.loads(read("packaging/repositories/production-key-descriptor.json"))
assert descriptor == {
    "apk_public_key_name": "olivares-packages-apk.rsa.pub",
    "apk_public_key_sha256": "467c99df4b7f038a29669fedf24990dbafb879188a159653285b9781b086baa0",
    "environment": "production",
    "openpgp_fingerprint": "AE27B5B818C9BCFE485B4D8B155E5EC6550A6EF8",
    "purpose": "apt-rpm-apk-repository-metadata",
    "schema": "olivares.ai/package-repository-key/v1",
}

for token in (
    "workflow_dispatch:", "release_tag:", "name: packages-publish",
    "actions: read", "--certificate-identity \"$identity\"",
    "bash scripts/assert-cosign-binary.sh --isolate",
    "debian:stable-slim@sha256:04634311a8d5fc442b6eb06d792293c4f3e2268652ca7634e00ce8ef5cc0a28a",
    "fedora:latest@sha256:43b29f65a41eb9c35e1cd5323e3bdf3b655c2357a9f4f1ff2f9c2798e5045d80",
    "alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b",
    'wrangler@4.100.0', "OLIVARES_REPO_SIGNING_KEY",
    "OLIVARES_REPO_SIGNING_PASSPHRASE", "CLOUDFLARE_API_TOKEN",
    "EXPECTED_REVIEWER_ID: '106011039'", ".reviewer.id == $reviewer_id",
    "custom_branch_policies == true",
    ".total_count == 1", 'select(.type == "required_reviewers")] | length) == 1',
    "--mode stage --apply", "--mode promote --apply",
    "clean apt client verifies staged HTTPS repository",
    "clean rpm client verifies staged HTTPS repository",
    "clean APK client verifies staged HTTPS repository",
    "clean apt client verifies promoted HTTPS repository",
    "olivares-packages.gpg", "cancel-in-progress: false",
):
    assert token in workflow, token
images = re.findall(r"--image\s+(\S+)", workflow)
expected_images = {
    "debian:stable-slim@sha256:04634311a8d5fc442b6eb06d792293c4f3e2268652ca7634e00ce8ef5cc0a28a",
    "fedora:latest@sha256:43b29f65a41eb9c35e1cd5323e3bdf3b655c2357a9f4f1ff2f9c2798e5045d80",
    "alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b",
}
assert len(images) == 6 and set(images) == expected_images
assert all(images.count(image) == 2 for image in expected_images)
assert "pull_request:" not in workflow and "push:" not in workflow
assert "continue-on-error" not in workflow
assert workflow.index("--mode stage --apply") < workflow.index("clean apt client verifies staged HTTPS repository")
assert workflow.index("clean APK client verifies staged HTTPS repository") < workflow.index("--mode promote --apply")
assert workflow.index("--mode promote --apply") < workflow.index("clean apt client verifies promoted HTTPS repository")

for token in (
    "DRY-RUN", "OLIVARES_PACKAGE_PUBLISH_APPROVED", "4.100.0",
    "olivares-packages | olivares-packages-sandbox", "staging/$staging_id",
    "for family in apt rpm apk", '"$evidence_dir/$family.ok"',
    "root_files", "content_files",
    "for rel in \"${content_files[@]}\"", "for rel in \"${root_files[@]}\"",
    "rolling canonical objects back", "10007|does not exist|not found",
    "put_and_verify", "is_immutable_key", "refusing overwrite",
    "cache_for_key", "private, no-store", ".inventory.sha256",
):
    assert token in publisher, token
assert publisher.index('for rel in "${content_files[@]}"') < publisher.index('for rel in "${root_files[@]}"')

battery = read("scripts/test-package-publish.sh")
for token in (
    "mutant without client evidence", "mutant partial promotion",
    "mutant ambiguous canonical read", "FAKE_GET_ERROR_KEY",
    "mutant immutable package collision",
    "previous canonical package", "FAKE_FAIL_KEY=stable/apt/dists/stable/InRelease",
    "positive promotion writes content before signed discovery roots",
):
    assert token in battery, token

for token in (
    "member.isfile()", "member.name.startswith", "PurePosixPath",
    "O_NOFOLLOW", "set(names) != expected", "tracked production anchor",
    "OpenPGP secret does not match", "APK private key does not match",
):
    assert token in materializer, token
for token in (
    "stable/security renders disagree", "contains a non-regular entry",
    "differs from the materialized production anchor", "olivares-packages.gpg",
):
    assert token in assembler, token
for token in (
    "--repository-url", "https://packages[.]olivares[.]ai",
    "--evidence-file", "--staging-id", "curl --fail", "repo_gpgcheck=1",
):
    assert token in client, token

for target in ("lint:package-publish", "lint:package-publish:selftest"):
    assert f"  {target}:" in taskfile
    assert f"task {target}" in hook
    assert f"run: task {target}" in mainline

for document in (
    read("INSTALL.md"),
    read("docs/RELEASE-INSTALLER.md"),
    read("docs-site/src/content/docs/how-to/install-from-packages.md"),
):
    assert "DIST-24-06 proposed repositories" in document
    assert "No package-repository URL is live" in document
PY

printf '%s\n' 'package publish contract: OK — custody, protected dispatch, staged reread, native evidence and roots-last rollback wired'
