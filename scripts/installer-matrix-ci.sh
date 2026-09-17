#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Live DIST-24-05 leg. Linux families run inside their named container; macOS
# runs on the hosted runner. Both install the public v26.8.0 payload through the
# tracked verifier, then exercise this commit's service/doctor candidate.
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
lib="$root/scripts/installer-matrix-lib.sh"
family=""
image=""
candidate=""
release_version=26.9.0
inside=0

usage() {
	printf '%s\n' 'usage: installer-matrix-ci.sh --family NAME --candidate ABSOLUTE_PATH [--image IMAGE] [--release-version X.Y.Z]'
}

while [[ "$#" -gt 0 ]]; do
	case "$1" in
		--family) family="${2:-}"; shift 2 ;;
		--image) image="${2:-}"; shift 2 ;;
		--candidate) candidate="${2:-}"; shift 2 ;;
		--release-version) release_version="${2:-}"; shift 2 ;;
		--inside) inside=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) printf 'installer-matrix: unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
	esac
done

[[ -n "$family" ]] || { usage >&2; exit 2; }
[[ "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
	printf 'installer-matrix: invalid release version: %s\n' "$release_version" >&2
	exit 2
}

case "$family" in
	debian|ubuntu|fedora|opensuse-leap|alpine|macos) ;;
	*) bash "$lib" platform "$family"; exit $? ;;
esac

if [[ "$inside" -eq 0 && "$family" != macos ]]; then
	[[ -n "$image" ]] || { printf 'installer-matrix: --image is required for Linux\n' >&2; exit 2; }
	[[ "$candidate" == /* && -x "$candidate" ]] || {
		printf 'installer-matrix: Linux candidate must be an absolute executable path\n' >&2
		exit 2
	}
	command -v docker >/dev/null 2>&1 || {
		printf 'installer-matrix: NO HE PODIDO MIRAR — docker is unavailable\n' >&2
		exit 2
	}
	# The workflow isolates the reviewed cosign OUT of PATH (assert-cosign-binary.sh --isolate)
	# and hands its location in OLIVARES_COSIGN_BIN, so a PATH lookup alone fails on every
	# hosted leg (public PR #31, 2026-09-17: six legs "cosign is unavailable"). The isolated
	# binary is preferred; PATH is the fallback for a local run.
	if [ -n "${OLIVARES_COSIGN_BIN:-}" ]; then
		[ -x "$OLIVARES_COSIGN_BIN" ] || {
			printf 'installer-matrix: NO HE PODIDO MIRAR — OLIVARES_COSIGN_BIN=%s is not an executable file\n' "$OLIVARES_COSIGN_BIN" >&2
			exit 2
		}
	else
		command -v cosign >/dev/null 2>&1 || {
			printf 'installer-matrix: NO HE PODIDO MIRAR — cosign is unavailable (neither OLIVARES_COSIGN_BIN nor PATH)\n' >&2
			exit 2
		}
	fi
	host_scratch="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/olivares-installer-${family}.XXXXXX")"
	# The container writes into the bind-mounted scratch as root. On a hosted runner the job
	# user cannot remove those files, and a cleanup that fails inside the EXIT trap turned a
	# green leg red (public PR #32, 2026-09-17: "rm: cannot remove …/live/bin/olivares:
	# Permission denied", exit 1 after every assertion passed). The scratch is therefore
	# emptied from inside a container of the same image, and the cleanup never decides the
	# verdict: the assertions above it do.
	cleanup_scratch() {
		docker run --rm -v "$host_scratch:/scratch" "$image" sh -c 'rm -rf /scratch/live /scratch/candidate' >/dev/null 2>&1 || true
		rm -rf -- "$host_scratch" 2>/dev/null || true
	}
	trap cleanup_scratch EXIT
	mkdir -p "$host_scratch/candidate"
	install -m 0755 "$candidate" "$host_scratch/candidate/olivares"
	cosign_path="${OLIVARES_COSIGN_BIN:-$(command -v cosign)}"
	# python3 is on the list because the doctor predicate in installer-matrix-lib.sh reads the
	# doctor JSON with it, and the minimal images ship without it (the debian/fedora/opensuse/
	# alpine legs answered "required tool is unavailable: python3" after every install step
	# had passed, 2026-09-17).
	# This program is expanded by /bin/sh inside the container.
	# shellcheck disable=SC2016
	bootstrap='case "$MATRIX_FAMILY" in
debian|ubuntu) export DEBIAN_FRONTEND=noninteractive; apt-get update; apt-get install -y --no-install-recommends bash ca-certificates coreutils curl gzip procps python3 tar ;;
fedora) dnf install -y bash ca-certificates coreutils curl gzip procps-ng python3 tar ;;
opensuse-leap) zypper --non-interactive refresh; zypper --non-interactive install -y bash ca-certificates coreutils curl gzip procps python3 tar ;;
alpine) apk add --no-cache bash ca-certificates coreutils curl gzip procps python3 tar ;;
*) echo "installer-matrix: NO HE PODIDO MIRAR — bootstrap family $MATRIX_FAMILY" >&2; exit 2 ;;
esac
exec bash /repo/scripts/installer-matrix-ci.sh --inside --family "$MATRIX_FAMILY" --candidate /matrix/candidate/olivares --release-version "$MATRIX_RELEASE_VERSION"'
	docker run --rm \
		--env MATRIX_FAMILY="$family" \
		--env MATRIX_RELEASE_VERSION="$release_version" \
		--volume "$root:/repo:ro" \
		--volume "$host_scratch:/matrix" \
		--volume "$cosign_path:/usr/local/bin/cosign:ro" \
		"$image" /bin/sh -eu -c "$bootstrap"
	exit $?
fi

if [[ "$inside" -eq 1 ]]; then
	root=/repo
	lib="$root/scripts/installer-matrix-lib.sh"
	candidate=/matrix/candidate/olivares
	work=/matrix/live
	os_name=linux
	init_name=systemd
	[[ -r /etc/os-release ]] || {
		printf 'installer-matrix: NO HE PODIDO MIRAR — container has no /etc/os-release\n' >&2
		exit 2
	}
	bash "$lib" platform "$family" /etc/os-release
else
	[[ "$family" == macos ]] || { printf 'installer-matrix: internal dispatch error\n' >&2; exit 2; }
	[[ "$candidate" == /* && -x "$candidate" ]] || {
		printf 'installer-matrix: macOS candidate must be an absolute executable path\n' >&2
		exit 2
	}
	bash "$lib" platform macos
	work="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/olivares-installer-macos.XXXXXX")"
	os_name=darwin
	init_name=launchd
fi

mkdir -p "$work/bin" "$work/fakebin" "$work/home/.local/bin" "$work/tmp"
chmod 0700 "$work/home" "$work/tmp"
export HOME="$work/home"
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_DATA_HOME="$HOME/.local/share"
export TMPDIR="$work/tmp"
# The fake init adapters start the engine from MATRIX_BINARY; it must be the binary the
# service adapter installed and validated, $HOME/.local/bin/olivares (the leg log said
# "nohup: failed to run command '…/bin/olivares': No such file or directory" while the
# --start phase waited 60 s for /livez and /readyz, 2026-09-17).
export MATRIX_BINARY="$HOME/.local/bin/olivares"
export MATRIX_DATA="$XDG_DATA_HOME/olivares"
export MATRIX_CONFIG="$XDG_CONFIG_HOME/olivares/olivares.env"
if [[ "$init_name" == launchd ]]; then
	# The user-mode launchd tuple is the ONLY one the service adapter accepts on macOS
	# (install-service.sh, release-index install_layout): the XDG pair above is the systemd
	# tuple, and the macos leg refused it as "user config/unit tuple is outside the
	# release-index install_layout" (2026-09-17, read from the printed leg log).
	export MATRIX_DATA="$HOME/Library/Application Support/Olivares"
	export MATRIX_CONFIG="$HOME/Library/Preferences/dev.olivares.olivares.env"
fi
export MATRIX_PID_FILE="$work/engine.pid"
export MATRIX_ENGINE_LOG="$work/engine.log"
export MATRIX_INIT_TRACE="$work/init.trace"
sudo_sentinel="$work/sudo-invoked"
export SUDO_SENTINEL="$sudo_sentinel"
secret_value='matrix-config-value-never-emit-dist-24-05'
all_log="$work/installer-doctor.log"
: >"$all_log"

cat >"$work/fakebin/sudo" <<'FAKESUDO'
#!/bin/sh
: >"$SUDO_SENTINEL"
exit 97
FAKESUDO

if [[ "$init_name" == systemd ]]; then
	cat >"$work/fakebin/systemctl" <<'FAKESYSTEMCTL'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$MATRIX_INIT_TRACE"
case " $* " in
  *" is-active "*)
    [ -s "$MATRIX_PID_FILE" ] && kill -0 "$(cat "$MATRIX_PID_FILE")" 2>/dev/null
    ;;
  *" enable --now olivares "*)
    if [ -s "$MATRIX_PID_FILE" ] && kill -0 "$(cat "$MATRIX_PID_FILE")" 2>/dev/null; then exit 0; fi
    env -u OLIVARES_ASSET_ROOT -u OLIVARES_OS \
      nohup "$MATRIX_BINARY" serve --data-dir="$MATRIX_DATA" \
      --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444 \
      --checkpoint-interval=1h </dev/null >"$MATRIX_ENGINE_LOG" 2>&1 &
    printf '%s\n' "$!" >"$MATRIX_PID_FILE"
    ;;
  *" daemon-reload "*) exit 0 ;;
  *) exit 1 ;;
esac
FAKESYSTEMCTL
else
	cat >"$work/fakebin/launchctl" <<'FAKELAUNCHCTL'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$MATRIX_INIT_TRACE"
case " $* " in
  *" print "*)
    [ -s "$MATRIX_PID_FILE" ] && kill -0 "$(cat "$MATRIX_PID_FILE")" 2>/dev/null
    ;;
  *" bootstrap "*)
    if [ -s "$MATRIX_PID_FILE" ] && kill -0 "$(cat "$MATRIX_PID_FILE")" 2>/dev/null; then exit 0; fi
    env -u OLIVARES_ASSET_ROOT -u OLIVARES_OS \
      nohup "$MATRIX_DATA/launchd-run.sh" </dev/null >"$MATRIX_ENGINE_LOG" 2>&1 &
    printf '%s\n' "$!" >"$MATRIX_PID_FILE"
    ;;
  *) exit 1 ;;
esac
FAKELAUNCHCTL
fi
chmod 0755 "$work/fakebin/"*
export PATH="$work/fakebin:/usr/local/bin:/usr/bin:/bin:$PATH"

cleanup_live() {
	# A red must say what it read. Every installer/service/doctor invocation below sends its
	# output to $all_log, so a failing one under `set -e` exits with nothing on the job log
	# (public PR #32 macos leg, 2026-09-17: two OK lines, then "exit code 1" and no cause).
	# On a non-zero exit the tail of both logs is printed before the scratch is removed.
	local rc=$?
	if [[ "$rc" -ne 0 ]]; then
		printf 'installer-matrix: exit %s — last 60 lines of %s:\n' "$rc" "$all_log" >&2
		tail -n 60 "$all_log" >&2 2>/dev/null || true
		if [[ -s "$MATRIX_ENGINE_LOG" ]]; then printf 'installer-matrix: last 30 lines of the engine log:\n' >&2; tail -n 30 "$MATRIX_ENGINE_LOG" >&2 2>/dev/null || true; fi
	fi
	if [[ -s "$MATRIX_PID_FILE" ]]; then
		pid="$(cat "$MATRIX_PID_FILE")"
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	if [[ "$inside" -eq 0 ]]; then rm -rf -- "$work"; fi
}
trap cleanup_live EXIT

# A missing network is not a false green: execute the promised dry-run, then return
# the explicit third answer because no real release bytes were measured.
release_base="https://github.com/olivaresai/olivares/releases/download/v$release_version"
if ! curl -fsSL --range 0-0 "$release_base/checksums.txt" -o /dev/null; then
	env OLIVARES_OS="$os_name" /bin/sh "$root/scripts/install.sh" \
		--version "v$release_version" --bindir "$HOME/.local/bin" --dry-run >>"$all_log" 2>&1
	printf 'installer-matrix: NO HE PODIDO MIRAR — public release network path unavailable; dry-run only\n' >&2
	exit 2
fi

# Real public release: current installer, real Fulcio/Rekor identity, signed checksums
# and the published archive. No service flag is passed in this phase.
env OLIVARES_OS="$os_name" /bin/sh "$root/scripts/install.sh" --version "v$release_version" \
	--bindir "$HOME/.local/bin" >>"$all_log" 2>&1
# The user-mode service adapter accepts exactly one binary route, $HOME/.local/bin/olivares
# (install-service.sh, the release-index install_layout). The matrix used to install into
# $work/bin and every leg died at "user binary path is outside the release-index
# install_layout" — read for the first time on 2026-09-17 once the leg log was printed.
installed="$HOME/.local/bin/olivares"
bash "$lib" binary "$installed" "$(id -u)"
"$installed" version >"$work/release-version.log" 2>&1
grep -Fq "$release_version" "$work/release-version.log" || {
	printf 'installer-matrix: public binary did not report v%s\n' "$release_version" >&2
	exit 1
}
cat "$work/release-version.log" >>"$all_log"
[[ ! -e "$MATRIX_DATA" && ! -e "$MATRIX_CONFIG" ]] || {
	printf 'installer-matrix: binary-only release install created service state\n' >&2
	exit 1
}
[[ ! -e "$MATRIX_INIT_TRACE" ]] || {
	printf 'installer-matrix: binary-only release install touched the init manager\n' >&2
	exit 1
}

# The public tag predates this candidate's service/doctor seam. Replace only the
# binary with this commit's static candidate, then exercise the exact helper the
# verified second stage carries. The report names this split rather than treating
# post-tag bytes as if v26.8.0 had contained them.
install -m 0755 "$candidate" "$installed"
bash "$lib" binary "$installed" "$(id -u)"

service_args=(--user --init "$init_name" --binary "$installed" --data-dir "$MATRIX_DATA" --config "$MATRIX_CONFIG")
env OLIVARES_OS="$os_name" OLIVARES_ASSET_ROOT="$root" \
	/bin/sh "$root/scripts/install-service.sh" "${service_args[@]}" >>"$all_log" 2>&1
[[ -f "$MATRIX_DATA/install-manifest.json" && -f "$MATRIX_CONFIG" ]] || {
	printf 'installer-matrix: opt-in service did not create its manifest/config\n' >&2
	exit 1
}
[[ ! -e "$MATRIX_INIT_TRACE" ]] || {
	printf 'installer-matrix: service started without the explicit --start phase\n' >&2
	exit 1
}
printf '# DIST-24-05 secret value must never be rendered by installer, doctor or engine\nOLIVARES_INGEST_TOKEN=%s\n' \
	"$secret_value" >"$MATRIX_CONFIG"
chmod 0600 "$MATRIX_CONFIG"

env OLIVARES_OS="$os_name" OLIVARES_ASSET_ROOT="$root" \
	/bin/sh "$root/scripts/install-service.sh" "${service_args[@]}" --start >>"$all_log" 2>&1
grep -Eq 'enable --now olivares|bootstrap gui/[0-9]+' "$MATRIX_INIT_TRACE" || {
	printf 'installer-matrix: explicit --start did not reach the selected init adapter\n' >&2
	exit 1
}

doctor_json="$work/doctor.json"
env -u OLIVARES_ASSET_ROOT -u OLIVARES_OS \
	"$installed" doctor --mode user --init "$init_name" --binary "$installed" \
	--data-dir "$MATRIX_DATA" --config "$MATRIX_CONFIG" \
	--server https://127.0.0.1:8443 --ca-cert "$MATRIX_DATA/tls.crt" \
	--timeout 15s -o json >"$doctor_json" 2>>"$all_log"
cat "$doctor_json" >>"$all_log"
bash "$lib" doctor "$doctor_json" - - -
bash "$lib" redacted "$all_log" "$secret_value"
bash "$lib" redacted "$MATRIX_ENGINE_LOG" "$secret_value"
bash "$lib" no-sudo "$sudo_sentinel"
bash "$lib" binary "$installed" "$(id -u)"
printf 'installer-matrix: LIVE OK — %s, public v%s + candidate service/doctor\n' "$family" "$release_version"
