#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# gen-vex.sh names the analyzed module as the product of each statement and writes module
# purls with plain slashes. govulncheck v1.3.0 writes "Unknown Product" and
# pkg:golang/golang.org%2Fx%2Fcrypto, which Trivy cannot tie to the engine binary, so an image
# scan kept GO-2026-5932 open although the statement said not_affected. The stub below replays
# the statement govulncheck v1.3.0 wrote for cmd/olivares on 2026-10-29.
set -euo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/bin" "$work/mod"
printf 'module example.com/engine/cmd/tool\n\ngo 1.26\n' >"$work/mod/go.mod"
cat >"$work/bin/govulncheck" <<'EOF'
#!/bin/sh
cat <<'DOC'
{"@context":"https://openvex.dev/ns/v0.2.0","@id":"govulncheck/vex:1","author":"Unknown Author","version":1,
 "statements":[{"vulnerability":{"@id":"https://pkg.go.dev/vuln/GO-2026-5932","name":"GO-2026-5932"},
  "products":[{"@id":"Unknown Product","subcomponents":[{"@id":"pkg:golang/golang.org%2Fx%2Fcrypto@v0.58.0"}]}],
  "status":"not_affected","justification":"vulnerable_code_not_present"}]}
DOC
EOF
chmod +x "$work/bin/govulncheck"

PATH="$work/bin:$PATH" bash "$root/scripts/gen-vex.sh" --dir "$work/mod" --out "$work/vex.json" >/dev/null
# Trivy 0.76.1 suppressed GO-2026-5932 on the engine binary only with both: the main module as
# the product and the subcomponent purl with plain slashes, the form Trivy gives the package.
got="$(jq -c '.statements[].products' "$work/vex.json")"
want='[{"@id":"pkg:golang/example.com/engine/cmd/tool","subcomponents":[{"@id":"pkg:golang/golang.org/x/crypto@v0.58.0"}]}]'
if [ "$got" != "$want" ]; then
	echo "test-gen-vex: FAIL — products $got, want $want" >&2
	exit 1
fi
# The verdict itself is govulncheck's, unchanged.
jq -e '.statements[0] | .status == "not_affected" and .justification == "vulnerable_code_not_present"' \
	"$work/vex.json" >/dev/null || { echo "test-gen-vex: FAIL — the statement changed" >&2; exit 1; }
echo "test-gen-vex: ok"
