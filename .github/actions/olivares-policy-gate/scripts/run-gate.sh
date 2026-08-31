# shellcheck shell=bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# REUSE-IgnoreStart
# (This script contains the string SPDX-License-Identifier in its comments; the
#  marker pair stops `reuse lint` from mis-parsing those as license declarations.
#  The file's own license is the header two lines up, outside this block.)
#
# run-gate.sh — the SHARED policy-gate runner. The GitHub composite
# action and the GitLab CI component both shell out to THIS file so the gate
# behaves identically in either pipeline (one source of truth, not two drifting
# copies). It runs the policy pack against a customer's manifests/IaC:
#
#   1. conftest test <target> -p <policy_dir>/conftest/policy   (OPA/Rego)
#   2. kyverno apply <policy_dir>/kyverno --resource <target>   (Kyverno CLI)
#   3. (best-effort) terraform fmt -check + terraform validate when *.tf is present
#      AND a terraform binary is on PATH.
#
# CONTRACT: this is a GATE. Any policy violation makes a step exit non-zero, and
# `set -e` propagates that as the script's exit status, which the CI job inherits
# and fails on. The terraform leg is advisory only when terraform is absent (we
# never claim a check we did not run), but a terraform that IS present and reports
# unformatted/invalid config is a hard failure like the rest.
#
# It installs PINNED CLI versions — never `latest` — driven entirely by the
# caller's inputs, so a customer reproduces the exact gate the release used and a
# tool bump is an explicit, reviewable change (no moving targets).
#
# Inputs are passed as environment variables (the CI layers set them from their
# own typed inputs):
#   POLICY_DIR        directory holding conftest/policy and kyverno/   (required)
#   TARGET            the manifests / IaC directory or file to scan    (required)
#   CONFTEST_VERSION  conftest release to install, e.g. 0.56.0         (required)
#   KYVERNO_VERSION   kyverno CLI release to install, e.g. 1.13.2      (required)
#   FAIL_ON_WARN      'true' adds `--fail-on-warn` to conftest         (default false)
#   BIN_DIR           where to install the CLIs (default: a fresh mktemp -d)
#
# No network is touched beyond the two pinned release downloads from the projects'
# own GitHub release assets (open-policy-agent/conftest, kyverno/kyverno).
#
# Strict bash; relies on bash arrays and `set -o pipefail`.

set -euo pipefail

log()  { printf '\033[1;34m[policy-gate]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[policy-gate] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# --- required inputs ---------------------------------------------------------
: "${POLICY_DIR:?POLICY_DIR is required (the policy pack dir)}"
: "${TARGET:?TARGET is required (the manifests/IaC dir or file to scan)}"
: "${CONFTEST_VERSION:?CONFTEST_VERSION is required (pin it; never 'latest')}"
: "${KYVERNO_VERSION:?KYVERNO_VERSION is required (pin it; never 'latest')}"
FAIL_ON_WARN="${FAIL_ON_WARN:-false}"

[ -e "$TARGET" ]      || fail "TARGET '$TARGET' does not exist"
[ -d "$POLICY_DIR" ]  || fail "POLICY_DIR '$POLICY_DIR' does not exist"

CONFTEST_POLICY="${POLICY_DIR}/conftest/policy"
KYVERNO_POLICY="${POLICY_DIR}/kyverno"
[ -d "$CONFTEST_POLICY" ] || fail "no conftest policy at '$CONFTEST_POLICY'"
[ -d "$KYVERNO_POLICY" ]  || fail "no kyverno policy at '$KYVERNO_POLICY'"

BIN_DIR="${BIN_DIR:-$(mktemp -d)}"
mkdir -p "$BIN_DIR"
export PATH="${BIN_DIR}:${PATH}"

# Normalise an OS/arch pair for the release asset names the two projects publish.
os="$(uname -s | tr '[:upper:]' '[:lower:]')"   # linux / darwin
case "$(uname -m)" in
  x86_64|amd64) arch_conftest="x86_64"; arch_kyverno="x86_64" ;;
  aarch64|arm64) arch_conftest="arm64"; arch_kyverno="arm64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

# --- install conftest (pinned) ----------------------------------------------
# Asset naming verified against github.com/open-policy-agent/conftest releases:
#   conftest_<version>_<Os>_<arch>.tar.gz, where <Os> is capitalised (Linux/Darwin).
install_conftest() {
  if command -v conftest >/dev/null 2>&1; then
    log "conftest already on PATH: $(conftest --version | head -1)"
    return
  fi
  local os_cap tarball url
  os_cap="$(printf '%s' "$os" | sed 's/^\(.\)/\U\1/')"   # linux -> Linux
  tarball="conftest_${CONFTEST_VERSION}_${os_cap}_${arch_conftest}.tar.gz"
  url="https://github.com/open-policy-agent/conftest/releases/download/v${CONFTEST_VERSION}/${tarball}"
  log "installing conftest v${CONFTEST_VERSION} from ${url}"
  curl -fsSL "$url" -o "${BIN_DIR}/conftest.tar.gz" || fail "download failed: ${url}"
  tar -xzf "${BIN_DIR}/conftest.tar.gz" -C "$BIN_DIR" conftest
  chmod +x "${BIN_DIR}/conftest"
  rm -f "${BIN_DIR}/conftest.tar.gz"
  log "conftest installed: $(conftest --version | head -1)"
}

# --- install kyverno CLI (pinned) -------------------------------------------
# Asset naming verified against github.com/kyverno/kyverno releases:
#   kyverno-cli_v<version>_<os>_<arch>.tar.gz (lowercase os, ships ./kyverno).
install_kyverno() {
  if command -v kyverno >/dev/null 2>&1; then
    log "kyverno already on PATH: $(kyverno version | head -1)"
    return
  fi
  local tarball url
  tarball="kyverno-cli_v${KYVERNO_VERSION}_${os}_${arch_kyverno}.tar.gz"
  url="https://github.com/kyverno/kyverno/releases/download/v${KYVERNO_VERSION}/${tarball}"
  log "installing kyverno CLI v${KYVERNO_VERSION} from ${url}"
  curl -fsSL "$url" -o "${BIN_DIR}/kyverno.tar.gz" || fail "download failed: ${url}"
  tar -xzf "${BIN_DIR}/kyverno.tar.gz" -C "$BIN_DIR" kyverno
  chmod +x "${BIN_DIR}/kyverno"
  rm -f "${BIN_DIR}/kyverno.tar.gz"
  log "kyverno installed: $(kyverno version | head -1)"
}

# --- the gate ----------------------------------------------------------------
run_conftest() {
  local args=(test "$TARGET" -p "$CONFTEST_POLICY")
  if [ "$FAIL_ON_WARN" = "true" ]; then
    args+=(--fail-on-warn)
  fi
  log "conftest ${args[*]}"
  conftest "${args[@]}"
}

run_kyverno() {
  log "kyverno apply ${KYVERNO_POLICY} --resource ${TARGET}"
  # `kyverno apply` exits non-zero when a policy with validationFailureAction
  # Enforce is violated; that is exactly the gate signal we want to propagate.
  kyverno apply "$KYVERNO_POLICY" --resource "$TARGET"
}

# Best-effort terraform leg: only when the target tree actually holds *.tf AND a
# terraform binary is present. Absent terraform => SKIP (we do not silently pass a
# check we never ran). Present terraform + bad config => hard FAIL.
run_terraform() {
  local tf_root
  if [ -d "$TARGET" ]; then
    tf_root="$TARGET"
  else
    tf_root="$(dirname "$TARGET")"
  fi
  if ! find "$tf_root" -maxdepth 2 -name '*.tf' -print -quit | grep -q .; then
    log "terraform: no *.tf under ${tf_root}; skipping terraform leg"
    return 0
  fi
  if ! command -v terraform >/dev/null 2>&1; then
    log "terraform: *.tf present but no terraform on PATH; skipping (advisory)"
    return 0
  fi
  log "terraform fmt -check -recursive ${tf_root}"
  terraform -chdir="$tf_root" fmt -check -recursive
  log "terraform init -backend=false && validate (${tf_root})"
  terraform -chdir="$tf_root" init -backend=false -input=false >/dev/null
  terraform -chdir="$tf_root" validate
}

main() {
  install_conftest
  install_kyverno
  run_conftest
  run_kyverno
  run_terraform
  log "policy gate PASSED"
}

main "$@"
# REUSE-IgnoreEnd
