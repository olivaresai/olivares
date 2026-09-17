#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Hermetic DIST-24-16 round-trip and source-mutation battery.
set -euo pipefail
LC_ALL=C
export LC_ALL

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
scratch_parent="${TMPDIR:-}"
case "$scratch_parent" in /*) ;; *)
	printf '%s\n' 'test-uninstall-migration: NO HE PODIDO MIRAR — TMPDIR must be absolute' >&2
	exit 2
	;;
esac
[[ -d "$scratch_parent" ]] || {
	printf 'test-uninstall-migration: NO HE PODIDO MIRAR — TMPDIR is absent: %s\n' "$scratch_parent" >&2
	exit 2
}
for tool in cat chmod cp go grep mkdir mktemp python3 rm sed; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'test-uninstall-migration: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done

scratch="$(mktemp -d "$scratch_parent/dist24-16.XXXXXX")"
cleanup() {
	case "$scratch" in "$scratch_parent"/dist24-16.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT INT TERM
mkdir -p "$scratch/go-cache"
export GOCACHE="$scratch/go-cache"

# Both public shell entry points must delegate to the already-installed binary
# before defining or touching a downloader. A hostile downloader shim makes any
# regression observable; exact argv pins the shared contract.
mkdir -p "$scratch/installed" "$scratch/fakebin"
cat >"$scratch/installed/olivares" <<'FAKEOLIVARES'
#!/bin/sh
printf '%s\n' "$*" >"$FAKE_UNINSTALL_LOG"
FAKEOLIVARES
cat >"$scratch/fakebin/curl" <<'FAKECURL'
#!/bin/sh
exit 99
FAKECURL
chmod 0755 "$scratch/installed/olivares" "$scratch/fakebin/curl"
for entrypoint in scripts/install.sh scripts/install-bootstrap.sh; do
	log="$scratch/${entrypoint##*/}.argv"
	FAKE_UNINSTALL_LOG="$log" PATH="$scratch/fakebin:$PATH" /bin/sh "$root/$entrypoint" \
		--uninstall --plan --data-dir /var/lib/olivares --bindir "$scratch/installed"
	grep -Fxq 'uninstall --plan --data-dir /var/lib/olivares' "$log" || {
		printf 'not ok - %s did not delegate exact uninstall argv\n' "$entrypoint" >&2
		exit 1
	}
done
set +e
/bin/sh "$root/scripts/install.sh" --uninstall --plan --yes --bindir "$scratch/installed" \
	>"$scratch/wrapper.out" 2>"$scratch/wrapper.err"
wrapper_rc=$?
set -e
if [[ "$wrapper_rc" -eq 0 ]] || ! grep -Fq -- '--yes is valid only' "$scratch/wrapper.err"; then
	printf '%s\n' 'not ok - installer accepted --yes with a non-purge action' >&2
	exit 1
fi

set +e
(
	cd "$root"
	go test ./core/dr -run '^(TestBundleRoundTrip|TestBundleIntegrityRejectsAlteredDigestAndPayload|TestBundleIntegrityRequiresExplicitLegacyException)$' -count=1
	go test ./cmd/olivares -run '^(TestUninstall|TestDRImport)' -count=1
) >"$scratch/control.out" 2>&1
control_rc=$?
set -e
if [[ "$control_rc" -ne 0 ]]; then
	printf 'not ok 1 - positive round-trip controls failed (rc=%s)\n' "$control_rc" >&2
	sed -n '1,160p' "$scratch/control.out" >&2
	exit 1
fi
printf '%s\n' 'ok 1 - install, seeded estate, authenticated export, purge, reinstall, import and doctor round-trip'

run_mutant() {
	kind="$1"
	source_rel="$2"
	package="$3"
	test_name="$4"
	dir="$scratch/$kind"
	mkdir -p "$dir"
	python3 - "$kind" "$root/$source_rel" "$dir/mutant.go" "$dir/overlay.json" <<'PY'
import json
import pathlib
import sys

kind, source_name, target_name, overlay_name = sys.argv[1:]
source = pathlib.Path(source_name)
text = source.read_text(encoding="utf-8")
mutations = {
    "unconfirmed-purge": (
        'if purge {\n\t\t\t\tif err := confirmDestructive',
        'if false {\n\t\t\t\tif err := confirmDestructive',
        1,
    ),
    "outside-index": (
        'return fmt.Errorf("unexpected %s path %q: absent from release-index install_layout", role, name)',
        'return nil',
        1,
    ),
    "altered-digest": (
        'if int64(len(body)) != declared.SizeBytes || got != declared.SHA256 {',
        'if got == declared.SHA256 && int64(len(body)) == declared.SizeBytes && false {',
        1,
    ),
    "preserve-deletes-data": (
        'if opts.Operation == Purge {',
        'if true {',
        2,
    ),
}
needle, replacement, count = mutations[kind]
if text.count(needle) != count:
    raise SystemExit(f"mutation anchor {kind} count={text.count(needle)}, want {count}")
mutant = text.replace(needle, replacement)
target = pathlib.Path(target_name)
target.write_text(mutant, encoding="utf-8")
pathlib.Path(overlay_name).write_text(
    json.dumps({"Replace": {str(source.resolve()): str(target.resolve())}}),
    encoding="utf-8",
)
PY
	set +e
	(
		cd "$root"
		go test -overlay "$dir/overlay.json" "$package" -run "^$test_name$" -count=1
	) >"$dir/out" 2>&1
	rc=$?
	set -e
	if [[ "$rc" -eq 0 ]] || ! grep -Fq "$test_name" "$dir/out"; then
		printf 'not ok - %s mutant survived (rc=%s)\n' "$kind" "$rc" >&2
		sed -n '1,120p' "$dir/out" >&2
		exit 1
	fi
	printf 'ok - mutant %s turns %s red (mutant rc=%s)\n' "$kind" "$test_name" "$rc"
}

run_mutant unconfirmed-purge cmd/olivares/cmd_uninstall.go ./cmd/olivares \
	TestUninstallPurgeRequiresConfirmationBeforeMutation
run_mutant outside-index cmd/olivares/internal/localinstall/manifest.go ./cmd/olivares \
	TestUninstallUnexpectedPathIsUsageAndPrecedesMutation
run_mutant altered-digest core/dr/integrity.go ./core/dr \
	TestBundleIntegrityRejectsAlteredDigestAndPayload
run_mutant preserve-deletes-data cmd/olivares/internal/localinstall/uninstall.go ./cmd/olivares \
	TestUninstallPreserveRetainsCustodyAndRemovesManagedSoftware

printf '%s\n' '1..5'
