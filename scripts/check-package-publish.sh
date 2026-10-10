#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Static, network-free DIST-24-06 F2 publication contract.
set -euo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
for tool in bash grep python3; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'package publish contract: COULD NOT CHECK — missing %s\n' "$tool" >&2
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
	scripts/test-rpm-repository.sh
	scripts/dnf-repository-client.sh
	scripts/render-rpm-repodata.py
	scripts/rpm-payload-sign.py
	packaging/repositories/dnf-repo.template
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
	scripts/test-package-publish.sh \
	scripts/test-package-publish-pacman.sh \
	scripts/test-rpm-repository.sh \
	scripts/dnf-repository-client.sh; do
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
dnf_client = read("scripts/dnf-repository-client.sh")
dnf_template = read("packaging/repositories/dnf-repo.template")
s3_driver = read("scripts/test-rpm-repository.sh")
fedora_44 = "registry.fedoraproject.org/fedora@sha256:539cadb5d8a43564d8abefd6eafdfcbcd4809070efbb900ec248229903db5911"
taskfile = read("Taskfile.yml")
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
    fedora_44,
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
    fedora_44,
    "alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b",
}
assert len(images) == 6 and set(images) == expected_images
assert all(images.count(image) == 2 for image in expected_images)
assert "pull_request:" not in workflow and "push:" not in workflow
assert "continue-on-error" not in workflow
assert workflow.index("--mode stage --apply") < workflow.index("clean apt client verifies staged HTTPS repository")
assert workflow.index("clean APK client verifies staged HTTPS repository") < workflow.index("--mode promote --apply")
assert workflow.index("--mode promote --apply") < workflow.index("clean apt client verifies promoted HTTPS repository")

# rpm is S3's signed path end to end: rendered by S3, adopted by the renderer,
# verified with S3's verifier, and checked by the DNF5 client with both gpg
# checks and the pinned key. No gpgcheck=0 rpm path and no test latch remain.
for token in (
    "bash scripts/test-rpm-repository.sh --self-test",
    'bash scripts/test-rpm-repository.sh --publish-rpm --assets "$DIST" --version "$RELEASE_VERSION"',
    '--public-key "$KEY_DIR/olivares-packages.asc" --out "$rpm_tree"',
    '--work "${RUNNER_TEMP}/rpm-publish-work"',
    'work="${RUNNER_TEMP}/rpm-publish-work"',
    '--out "$output" --rpm-tree "$rpm_tree"',
    '--apk-key "$KEY_DIR/${{ steps.key.outputs.apk_public_key_name }}" --rpm-s3',
    '--baseurl "$PACKAGE_ORIGIN/staging/$STAGING_ID/stable/rpm/\\$basearch"',
    '--baseurl "$PACKAGE_ORIGIN/stable/rpm/\\$basearch"',
    '--gpgkey "${{ steps.key.outputs.key_dir }}/olivares-packages.asc"',
    '--evidence-file "${RUNNER_TEMP}/package-evidence/rpm.ok" --staging-id "$STAGING_ID"',
):
    assert token in workflow, token
assert workflow.count("bash scripts/dnf-repository-client.sh") == 2
for absent in ("--family rpm", "gpgcheck=0", "fedora:latest", "OLIVARES_PACKAGE_REPO_TEST_ONLY"):
    assert absent not in workflow, absent
assert workflow.index("--publish-rpm") < workflow.index("scripts/render-package-repositories.py")
assert workflow.index("--mode stage --apply") < workflow.index("clean rpm client verifies staged HTTPS repository")
assert workflow.index("clean rpm client verifies staged HTTPS repository") < workflow.index("--mode promote --apply")
assert workflow.index("--mode promote --apply") < workflow.index("clean rpm client verifies promoted HTTPS repository")
assert "pkg_gpgcheck=1" in dnf_template and "repo_gpgcheck=1" in dnf_template
assert not re.search(r"(?m)^\s*gpgcheck=", dnf_template)
for token in ("REVIEWED_BASEURL", "render_repo_file", "repo_file_is_closed", "--evidence-file", "--staging-id"):
    assert token in dnf_client, token
for token in ("publish_rpm()", "check_tool_identity \"$image_id\"", "--init --rm --network none --security-opt no-new-privileges",
              "cleanup_publish_work"):
    assert token in s3_driver, token

for token in (
    "DRY-RUN", "OLIVARES_PACKAGE_PUBLISH_APPROVED", "4.100.0",
    "olivares-packages | olivares-packages-sandbox", "staging/$staging_id",
    "families=(apt rpm apk)", 'for family in "${families[@]}"', '"$evidence_dir/$family.ok"',
    "root_files", "content_files",
    "families+=(pacman)", "pacman_verify_tree", "pacman-render)",
    "*/pacman/*/olivares.db | */pacman/*/olivares.db.sig", "*.pkg.tar.zst | *.pkg.tar.zst.sig | keys/*",
    "*.sig) printf '%s\\n' application/pgp-signature", "pacman database is unsigned",
    "signature does not verify with $pacman_key_rel", "OLIVARES_PACMAN_SIGNING_FINGERPRINT",
    "OLIVARES_PACMAN_EXPECTED_FINGERPRINT", "is not the expected key", '-path "$tree/pacman/*"',

    "for rel in \"${content_files[@]}\"", "for rel in \"${root_files[@]}\"",
    "rolling canonical objects back", "10007|does not exist|not found",
    "put_and_verify", "is_immutable_key", "refusing overwrite",
    "cache_for_key", "private, no-store", ".inventory.sha256",
):
    assert token in publisher, token
assert publisher.index('for rel in "${content_files[@]}"') < publisher.index('for rel in "${root_files[@]}"')
# The pacman checks run before the dry-run exit, so an unsigned database never stages.
assert publisher.index("\tpacman_verify_tree\n\tfamilies+=(pacman)") < publisher.index("DRY-RUN")
# S3's rpm delivery.json and checksums.txt name the rpms and the signed repomd:
# they are promoted after both, last, and apt/apk classification is unchanged.
for token in ('pointer_files+=("$rel")', "*/rpm/*/delivery.json | */rpm/*/checksums.txt)"):
    assert token in publisher, token
assert (
    publisher.index('for rel in "${root_files[@]}"')
    < publisher.index('for rel in "${pointer_files[@]}"')
    < publisher.index("promote_one .inventory.sha256")
)

battery = read("scripts/test-package-publish.sh")
for token in (
    "mutant without client evidence", "mutant partial promotion",
    "mutant ambiguous canonical read", "FAKE_GET_ERROR_KEY",
    "mutant immutable package collision",
    "previous canonical package", "FAKE_FAIL_KEY=stable/apt/dists/stable/InRelease",
    "positive promotion writes content before signed discovery roots",
    "scripts/test-package-publish-pacman.sh", "18/18 cases green",
):
    assert token in battery, token
pacman_battery = read("scripts/test-package-publish-pacman.sh")
for token in (
    "case_unsigned_database", "case_wrongly_signed_database", "case_altered_database",
    "case_unsigned_package", "case_database_names_other_bytes", "case_promote_needs_pacman_evidence",
    "case_render_then_publish_roots_last", "TEST ONLY", "case_tree_wholly_signed_by_another_key",
    "case_pacman_without_expected_fingerprint", "case_toplevel_pacman_tree_is_checked",
):
    assert token in pacman_battery, token

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
    "checked by dnf-repository-client.sh, not this gpgcheck=0 client",
):
    assert token in client, token
for token in ("require_s3_rpm", "unsigned rpm render, not S3's signed tree", "olivares.ai/rpm-delivery/v1"):
    assert token in assembler, token

for target in ("lint:package-publish", "lint:package-publish:selftest"):
    assert f"  {target}:" in taskfile
    assert f"run: task {target}" in mainline

for document in (
    read("INSTALL.md"),
    read("docs/RELEASE-INSTALLER.md"),
    read("docs-site/src/content/docs/how-to/install-from-packages.md"),
):
    assert "Proposed package repositories" in document
    assert "No package-repository URL is live" in document
PY

printf '%s\n' 'package publish contract: OK — custody, protected dispatch, staged reread, native evidence and roots-last rollback wired'
