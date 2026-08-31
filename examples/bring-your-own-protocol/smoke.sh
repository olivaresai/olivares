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

export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOFLAGS="${GOFLAGS:--p=1}"

rm -rf "${WORK}"
mkdir -p "${WORK}"

echo "== generate content-source skeleton =="
(
  cd "${ROOT}"
  go run ./cmd/olivares connector init acme.generated-fabworks \
    --dir "${WORK}/generated" \
    --module example.com/fabworks/generated-fabworks \
    --template content-source \
    --sdk-path "${ROOT}/sdk"
)
test -f "${WORK}/generated/cmd/acme-generated-fabworks/main.go"

echo "== unit test fixture =="
(
  cd "${EXAMPLE}/erp-fixture"
  echo "    GOWORK=off go test ./...   (sin -timeout: rige el defecto de Go, 10m)"
  GOWORK=off go test ./...
)

echo "== unit test filled connector =="
(
  cd "${CONNECTOR}"
  echo "    GOWORK=off go test ./...   (sin -timeout: rige el defecto de Go, 10m)"
  GOWORK=off go test ./...
)

echo "== build plugin binary =="
BIN="${WORK}/acme-fabworks-erp"
(
  cd "${CONNECTOR}"
  GOWORK=off go build -trimpath -o "${BIN}" ./cmd/acme-fabworks-erp
)

echo "== boundary check =="
(
  cd "${CONNECTOR}"
  # `bash <script>`, like every other caller of this gate (Taskfile.yml:642). Invoking it as
  # ./scripts/check-boundary.sh needs the executable bit, which the file does NOT carry in git
  # (100644) — so this line died with "Permission denied", exit 126, in the `examples` job.
  # It is the only such call in the tree, measured: every committed 644 .sh invoked with `./`.
  bash scripts/check-boundary.sh
)

echo "== zero governance code grep =="
if rg -n 'github.com/olivaresai/olivares/(core|modules|connectors)|sourcescope|RetrievalScopeGate' "${CONNECTOR}" --glob '*.go'; then
  echo "governance code/imports found in the connector; expected SDK-only implementation" >&2
  exit 1
fi
echo "Connector contains no engine governance imports or scope-gate code."

echo "== offline sign and admission check =="
BUNDLE="${WORK}/acme-fabworks-erp.sigstore.json"
PUB="${WORK}/acme-fabworks-erp.pub"
(
  cd "${EXAMPLE}/admission-check"
  GOWORK=off go run . --binary "${BIN}" --bundle "${BUNDLE}" --pubkey "${PUB}"
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
