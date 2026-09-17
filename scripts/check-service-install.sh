#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Static DIST-24-04 contract. The companion battery executes the adapters and
# mutates each invariant so this checker cannot silently become decorative.
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
  scripts/install.sh scripts/install-bootstrap.sh scripts/install-service.sh \
  packaging/service/systemd.service packaging/service/openrc.sh \
  packaging/service/launchd.xml cmd/olivares/cmd_doctor.go \
  docs/RELEASE-INSTALLER.md; do
  require_file "$path"
done
for path in scripts/install.sh scripts/install-bootstrap.sh scripts/install-service.sh; do
  [[ -x "$root/$path" ]] || fail "$path is not executable"
  /bin/sh -n "$root/$path" || fail "$path does not parse as POSIX shell"
done

scan_rc=0
grep -R -nE '(^|[;&|])[[:space:]]*sudo[[:space:]]+|--insecure|0\.0\.0\.0|demo seed' \
  "$root/scripts/install.sh" "$root/scripts/install-bootstrap.sh" \
  "$root/scripts/install-service.sh" "$root/packaging/service" >/dev/null || scan_rc=$?
case "$scan_rc" in
  0) fail "service installation contains implicit privilege, insecure TLS/listener or demo activation" ;;
  1) ;;
  *) blind "could not scan service installation for unsafe activation: grep rc=$scan_rc" ;;
esac

python3 - "$root" <<'PY' || fail "service/doctor source-order and archive contract is broken"
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
service = (root / "scripts/install-service.sh").read_text(encoding="utf-8")
installer = (root / "scripts/install.sh").read_text(encoding="utf-8")
bootstrap = (root / "scripts/install-bootstrap.sh").read_text(encoding="utf-8")
goreleaser = (root / ".goreleaser.yaml").read_text(encoding="utf-8")
doctor = (root / "cmd/olivares/cmd_doctor.go").read_text(encoding="utf-8")
main = (root / "cmd/olivares/main.go").read_text(encoding="utf-8")

assert service.count("validate_config_names\n") >= 2
start = service.index('if [ "$start" -eq 1 ]; then')
validate = service.index("  validate_config_names", start)
for token in ("systemctl daemon-reload", "rc-update add", "launchctl bootstrap"):
    assert service.index(token, start) > validate
for token in ('/livez', '/readyz', 'install-manifest.json', 'result-json:'):
    assert token in service

# The first-boot wait is bounded in three independent places, and the diagnosis
# it produces on failure comes from the installed binary, never from a response
# body. Counting 60 attempts of an unbounded request is not a 60-second wait.
assert 'while [ "$attempts" -lt 60 ]' not in service
for token in ('FIRST_BOOT_DEADLINE=', 'FIRST_BOOT_MAX_ATTEMPTS=',
              'FIRST_BOOT_DIAGNOSIS_TIMEOUT=',
              '--connect-timeout "$(first_boot_clip "$FIRST_BOOT_CONNECT_TIMEOUT")"',
              '--max-time "$(first_boot_clip "$FIRST_BOOT_REQUEST_TIMEOUT")"'):
    assert token in service, token
assert '$FIRST_BOOT_MAX_ATTEMPTS' in service and '$FIRST_BOOT_DEADLINE' in service

# The remaining budget is refreshed before the request, not once per attempt, and
# each request's own limits are clipped to it. A raw limit would let a request
# that starts inside the deadline finish outside it.
loop = service[service.index('  probe_ok=0\n  probe_late=0'):service.index('  if [ "$probe_ok" -ne 1 ]; then')]
assert loop.index('first_boot_tick') < loop.index('curl -fsS')
assert '--connect-timeout "$FIRST_BOOT_CONNECT_TIMEOUT"' not in service
assert '--max-time "$FIRST_BOOT_REQUEST_TIMEOUT"' not in service
assert loop.count('curl -fsS') == 1 and 'for probe_path in livez readyz' in loop

# A success measured after the deadline is refused, and said to be refused.
assert 'probe_late=1' in loop
assert "a late answer is not this wait's result" in service

# The optional final probe carries its own bound, named as separate from the wait.
assert '--timeout "${FIRST_BOOT_DIAGNOSIS_TIMEOUT}s"' in service
assert 'separate from the wait above' in service

# The failure path diagnoses before it exits, reports what the wait actually
# spent, and says the installation was left alone. An err() that runs before the
# diagnosis would exit first and print nothing.
diagnosis_def = service.index('first_boot_diagnosis() {')
diagnosis_call = service.index('\n    first_boot_diagnosis\n')
give_up = service.index('service started but /livez and /readyz were not both healthy')
assert diagnosis_def < diagnosis_call < give_up
for token in ('attempt(s) over', 'bounded at $FIRST_BOOT_MAX_ATTEMPTS attempts',
              'left exactly as installed and nothing was rolled back'):
    assert token in service, token

# The diagnosis delegates to the installed binary's own bounded local probe and
# detects an older binary instead of guessing.
assert 'readyz --help' in service
assert '"$binary_target" readyz \\' in service
assert "no 'olivares readyz' subcommand" in service
diagnosis_body = service[diagnosis_def:service.index('\n}\n', diagnosis_def)]
assert 'curl' not in diagnosis_body
assert 'olivares.ai/local-install/v2' in service
assert 'release-index install_layout' in service
window = "sed -n '/FIRST-BOOT SETUP/,/========================/p'"
assert service.count(window) == 4
assert f"journalctl -u olivares | {window}" in service
assert f"journalctl --user -u olivares | {window}" in service
assert f"{window} '$data_dir/olivares.log'" in service
assert 'refusing recursive ownership changes' in service
assert 'existing system data directory mode' in service
assert 'export \\"\\$line\\" || exit 1' in service
assert '. "$config"' not in service and 'source "$config"' not in service
openrc = (root / "packaging/service/openrc.sh").read_text(encoding="utf-8")
assert 'export "$line"' not in openrc
assert "source " not in openrc
assert "olivares_extra_args" in openrc
assert "start_pre()" in openrc

assert goreleaser.count("      - scripts/install-service.sh\n") == 2
assert goreleaser.count("      - packaging/service/\n") == 2
assert 'tar -xzf "$tmp/$archive"' in installer
assert 'scripts/install-service.sh packaging/service' in installer
assert installer.index('cosign verify-blob') < installer.index('tar -xzf "$tmp/$archive"')
assert '"--$service_mode"' in installer
assert installer.count('--managed-binary') == 2
assert '"--$service_mode"' in bootstrap
assert '--start requires --user or --system' in installer
assert '--start requires --user or --system' in bootstrap

assert 'newDoctorCmd()' in main
assert '"doctor": "observe"' in main
assert 'const doctorSchema = "olivares.ai/doctor/v1"' in doctor
assert 'code = exitcode.Usage // DIST-24-04' in doctor
assert 'Values from the env file are never read into output' in doctor
assert 'cmd.Stdout = io.Discard' in doctor and 'cmd.Stderr = io.Discard' in doctor
PY

for pair in \
  'packaging/service/systemd.service:@USER_LINE@' \
  'packaging/service/systemd.service:@DATA_DIR@' \
  'packaging/service/openrc.sh:@BINARY@' \
  'packaging/service/launchd.xml:@PROGRAM@'; do
  path="${pair%%:*}"
  marker="${pair#*:}"
  grep -Fq "$marker" "$root/$path" || fail "$path lost marker $marker"
done
grep -Fq 'ProtectSystem=strict' "$root/packaging/service/systemd.service" ||
  fail "systemd adapter lost filesystem hardening"
grep -Fq 'command_user="olivares:olivares"' "$root/packaging/service/openrc.sh" ||
  fail "OpenRC adapter lost its no-login service account"
grep -Fq '<key>UserName</key>' "$root/scripts/install-service.sh" ||
  fail "launchd system adapter lost its service-account binding"

grep -Fq 'olivares doctor' "$root/docs/RELEASE-INSTALLER.md" ||
  fail "installer guide does not document the local diagnostic"
grep -Fq 'measured defect, and 2 unmeasurable' "$root/docs/RELEASE-INSTALLER.md" ||
  fail "installer guide does not state the doctor exit contract"

if [[ "$failures" -ne 0 ]]; then
  printf 'service install contract: %d failure(s)\n' "$failures" >&2
  exit 1
fi
printf 'service install contract: OK\n'
