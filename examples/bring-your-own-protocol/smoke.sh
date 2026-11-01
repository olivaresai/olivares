#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: Apache-2.0
#
# Offline smoke for the bring-your-own-protocol example:
# generate skeleton -> build/test filled connector -> boundary check -> sign ->
# verify deny-closed admission behavior -> print operator config material.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EXAMPLE="${ROOT}/examples/bring-your-own-protocol"
CONNECTOR="${EXAMPLE}/fabworks-connector"
WORK="${ROOT}/.examples-tmp/bring-your-own-protocol"

# The engine `task build:bin` already produced. The skeleton step used `go run ./cmd/olivares`,
# which recompiles the whole engine with other flags (measured on the builder: 84 s for the
# build, 232 s for the `go run` after it, 1 s for the binary) and sat silent until the CI job
# timeout. Same convention as govern-claude-code/smoke.sh and otel-genai-ingest.
ENGINE="${OLIVARES_BIN:-${ROOT}/bin/olivares}"

export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOFLAGS="${GOFLAGS:--p=1}"

# Every command that can wait runs under `bounded`, so a stall ends with the step's name instead
# of the job's mute timeout. A cold compile on a loaded runner is the slow case: raise the bound
# there instead of removing it.
STEP_TIMEOUT="${OLIVARES_SMOKE_STEP_TIMEOUT:-600}"
STEP="starting"
step() {
  STEP="$1"
  echo "== $1 =="
}
bounded() {
  local rc=0 start="${SECONDS}"
  timeout --kill-after=10 "${STEP_TIMEOUT}" "$@" || rc=$?
  case "${rc}" in
    124 | 137)
      if [ $((SECONDS - start)) -ge "${STEP_TIMEOUT}" ]; then
        echo "FAIL: ${STEP} timed out after ${STEP_TIMEOUT}s: $* (OLIVARES_SMOKE_STEP_TIMEOUT raises the bound)" >&2
      else
        echo "FAIL: ${STEP} was killed (rc ${rc}) after $((SECONDS - start))s: $*" >&2
      fi
      exit 1
      ;;
  esac
  return "${rc}"
}

rm -rf "${WORK}"
mkdir -p "${WORK}"

if [ ! -x "${ENGINE}" ]; then
  # Built into ${WORK}, which is wiped above: a fallback binary never outlives the run, so a
  # later run cannot mistake it for a fresh `task build:bin`.
  step "build the olivares engine (${ENGINE} is absent; task build:bin makes it)"
  ENGINE="${WORK}/olivares"
  (cd "${ROOT}" && CGO_ENABLED=0 bounded go build -trimpath -o "${ENGINE}" ./cmd/olivares)
fi

step "generate content-source skeleton"
bounded "${ENGINE}" connector init acme.generated-fabworks \
  --dir "${WORK}/generated" \
  --module example.com/fabworks/generated-fabworks \
  --template content-source \
  --sdk-path "${ROOT}/sdk"
test -f "${WORK}/generated/cmd/acme-generated-fabworks/main.go"

step "unit test fixture"
(
  cd "${EXAMPLE}/erp-fixture"
  echo "    GOWORK=off go test ./...   (sin -timeout: rige el defecto de Go, 10m)"
  GOWORK=off bounded go test ./...
)

step "unit test filled connector"
(
  cd "${CONNECTOR}"
  echo "    GOWORK=off go test ./...   (sin -timeout: rige el defecto de Go, 10m)"
  GOWORK=off bounded go test ./...
)

step "build plugin binary"
BIN="${WORK}/acme-fabworks-erp"
(
  cd "${CONNECTOR}"
  GOWORK=off bounded go build -trimpath -o "${BIN}" ./cmd/acme-fabworks-erp
)

step "boundary check"
(
  cd "${CONNECTOR}"
  # `bash <script>`, like every other caller of this gate (Taskfile.yml:642). Invoking it as
  # ./scripts/check-boundary.sh needs the executable bit, which the file does NOT carry in git
  # (100644) — so this line died with "Permission denied", exit 126, in the `examples` job.
  # It is the only such call in the tree, measured: every committed 644 .sh invoked with `./`.
  bounded bash scripts/check-boundary.sh
)

step "zero governance code grep"
scan_status=0
grep -rEn --include='*.go' \
  'github.com/olivaresai/olivares/(core|modules|connectors)|sourcescope|RetrievalScopeGate' \
  "${CONNECTOR}" || scan_status=$?
case "${scan_status}" in
  0)
    echo "governance code/imports found in the connector; expected SDK-only implementation" >&2
    exit 1
    ;;
  1)
    echo "Connector contains no engine governance imports or scope-gate code."
    ;;
  *)
    echo "could not scan connector governance code (grep exit ${scan_status})" >&2
    exit 2
    ;;
esac

step "offline sign and admission check"
BUNDLE="${WORK}/acme-fabworks-erp.sigstore.json"
PUB="${WORK}/acme-fabworks-erp.pub"
(
  cd "${EXAMPLE}/admission-check"
  GOWORK=off bounded go run . --binary "${BIN}" --bundle "${BUNDLE}" --pubkey "${PUB}"
)

SHA="$(sha256sum "${BIN}" | awk '{print $1}')"
echo "operator documents plugin block:"
cat <<JSON
{
  "documents": [
    {
      "name": "fabworks-erp",
      "plugin": {
        "path": "${BIN}",
        "sha256": "${SHA}",
        "bundle": "${BUNDLE}"
      },
      "config": {
        "base_url": "https://erp.internal.example",
        "token": "store:fabworks-erp-token"
      }
    }
  ]
}
JSON

echo "smoke ok"
