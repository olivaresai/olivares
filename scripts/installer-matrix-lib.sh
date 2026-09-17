#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Small, shared assertions for DIST-24-05. The hosted matrix and the hermetic
# fixture battery share doctor/platform/redaction decisions; installer trust and
# archive mutants execute scripts/install.sh directly. An unknown platform is the
# third answer (rc 2), never an empty green matrix leg.
set -uo pipefail

fail() {
	printf 'installer-matrix: FAIL — %s\n' "$*" >&2
	exit 1
}

blind() {
	printf 'installer-matrix: NO HE PODIDO MIRAR — %s\n' "$*" >&2
	exit 2
}

need() {
	command -v "$1" >/dev/null 2>&1 || blind "required tool is unavailable: $1"
}

stat_mode() {
	stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null ||
		blind "cannot measure mode of $1"
}

stat_uid() {
	stat -c '%u' "$1" 2>/dev/null || stat -f '%u' "$1" 2>/dev/null ||
		blind "cannot measure owner of $1"
}

[[ "$#" -ge 1 ]] || blind "missing predicate name"
command_name="$1"
shift

case "$command_name" in
	binary)
		[[ "$#" -eq 2 ]] || blind "binary needs FILE EXPECTED_UID"
		[[ "$2" =~ ^[0-9]+$ ]] || blind "expected uid is not numeric"
		[[ -f "$1" && -x "$1" ]] || fail "installed binary is absent or not executable: $1"
		mode="$(stat_mode "$1")"
		uid="$(stat_uid "$1")"
		[[ "$mode" == 755 ]] || fail "installed binary mode is $mode, expected 755"
		[[ "$uid" == "$2" ]] || fail "installed binary owner is uid $uid, expected $2"
		printf 'installer-matrix: binary path/mode/owner OK\n'
		;;
	doctor)
		[[ "$#" -eq 4 ]] || blind "doctor needs JSON EXPECTED_CURRENT EXPECTED_AVAILABLE EXPECTED_STATUS (- disables OTA comparison)"
		need python3
		python3 - "$1" "$2" "$3" "$4" <<'PY'
import json
import pathlib
import re
import sys

path, current, available, status = sys.argv[1:]
try:
    value = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
except Exception as exc:
    print(f"installer-matrix: FAIL — doctor output is not readable JSON: {exc}", file=sys.stderr)
    raise SystemExit(1)
if value.get("schema") != "olivares.ai/doctor/v1" or value.get("overall") != "healthy":
    print("installer-matrix: FAIL — doctor did not report the canonical healthy result", file=sys.stderr)
    raise SystemExit(1)
if (current, available, status) != ("-", "-", "-"):
    checks = [c for c in value.get("checks", []) if c.get("name") == "update-channel"]
    if len(checks) != 1 or checks[0].get("status") != "pass":
        print("installer-matrix: FAIL — doctor did not pass exactly one update-channel check", file=sys.stderr)
        raise SystemExit(1)
    match = re.fullmatch(r"current=([^ ]+) available=([^ ]+) status=([^ ]+)", checks[0].get("detail", ""))
    got = match.groups() if match else None
    if got != (current, available, status):
        print(f"installer-matrix: FAIL — OTA comparison differs from trusted fixture: {got!r}", file=sys.stderr)
        raise SystemExit(1)
print("installer-matrix: doctor JSON/exit contract OK")
PY
		;;
	redacted)
		[[ "$#" -eq 2 ]] || blind "redacted needs LOG SECRET_VALUE"
		[[ -r "$1" ]] || blind "cannot read log: $1"
		[[ -n "$2" ]] || blind "secret-value fixture cannot be empty"
		if grep -Fq -- "$2" "$1"; then
			fail "a configuration value appeared in installer/doctor output"
		fi
		printf 'installer-matrix: configuration values absent from log\n'
		;;
	no-sudo)
		[[ "$#" -eq 1 ]] || blind "no-sudo needs SENTINEL_PATH"
		[[ ! -e "$1" ]] || fail "the installer invoked the sudo sentinel"
		printf 'installer-matrix: no implicit sudo invocation\n'
		;;
	platform)
		[[ "$#" -ge 1 && "$#" -le 2 ]] || blind "platform needs FAMILY [OS_RELEASE]"
		family="$1"
		case "$family" in
			debian|ubuntu|fedora|opensuse-leap|alpine|macos) ;;
			*) blind "unsupported distribution '$family'" ;;
		esac
		if [[ "$family" != macos ]]; then
			[[ "$#" -eq 2 && -r "$2" ]] || blind "Linux family needs a readable os-release"
			id="$(sed -n 's/^ID=//p' "$2" | sed -n '1p' | tr -d '"')"
			case "$family:$id" in
				debian:debian|ubuntu:ubuntu|fedora:fedora|opensuse-leap:opensuse-leap|alpine:alpine) ;;
				*) fail "matrix family '$family' ran in os-release ID '$id'" ;;
			esac
		fi
		printf 'installer-matrix: platform %s recognized\n' "$family"
		;;
	*)
		blind "unknown predicate: $command_name"
		;;
esac
