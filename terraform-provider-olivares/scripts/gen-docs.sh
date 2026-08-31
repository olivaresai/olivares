#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# gen-docs.sh — regenerate the Registry documentation for terraform-provider-olivares
# from the provider schema + the examples/ tree, using tfplugindocs.
#
# WHY a wrapper (not just `tfplugindocs generate`): tfplugindocs exports the
# schema by running `terraform init` against the public Registry, which (a)
# fails for an unpublished provider and (b) needs network to the Registry. We
# avoid both by exporting the schema OURSELVES from the locally-built binary via
# an OpenTofu (or Terraform) `dev_overrides` block — the OSS-default path
# — then feeding that JSON to tfplugindocs with --providers-schema.
#
# The schema JSON's top-level key is normalised to the bare short name
# "olivares" so tfplugindocs' --provider-name match (schema.go: exact short-name
# or registry.terraform.io/hashicorp/<name>) succeeds; the provider's real
# published address (registry.terraform.io/olivaresai/olivares) is unchanged.
#
# Requires: a Terraform-compatible CLI on PATH (tofu or terraform).
# Usage:  scripts/gen-docs.sh [--check]
#   (no args) regenerate docs/ in place
#   --check    regenerate into a temp dir and fail if docs/ is stale (CI gate)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# Pick a Terraform-compatible CLI: prefer tofu (OSS default), fall back to terraform.
TF_BIN="$(command -v tofu || command -v terraform || true)"
if [ -z "${TF_BIN}" ]; then
  echo "gen-docs: need 'tofu' or 'terraform' on PATH to export the provider schema" >&2
  exit 1
fi
echo "==> using ${TF_BIN} to export the provider schema"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
BIN_DIR="${WORK}/bin"
mkdir -p "${BIN_DIR}"

# 1. Build the provider binary into a dev_overrides directory.
echo "==> building provider"
go build -o "${BIN_DIR}/terraform-provider-olivares" .

# 2. dev_overrides CLI config so the CLI uses the local binary (no Registry/init).
cat >"${WORK}/dev.tfrc" <<EOF
provider_installation {
  dev_overrides {
    "olivaresai/olivares" = "${BIN_DIR}"
  }
  direct {}
}
EOF

# 3. Export the schema, then normalise the provider_schemas key to "olivares".
mkdir -p "${WORK}/cfg"
cat >"${WORK}/cfg/main.tf" <<'EOF'
terraform {
  required_providers {
    olivares = { source = "olivaresai/olivares" }
  }
}
EOF
echo "==> exporting schema"
( cd "${WORK}/cfg" && TF_CLI_CONFIG_FILE="${WORK}/dev.tfrc" "${TF_BIN}" providers schema -json ) >"${WORK}/schema.json"
python3 - "${WORK}/schema.json" <<'PY'
import json, sys
p = sys.argv[1]
with open(p) as f:
    s = json.load(f)
ps = s["provider_schemas"]
key = next(iter(ps))
ps["olivares"] = ps.pop(key)
with open(p, "w") as f:
    json.dump(s, f)
PY

# 4. Render docs with tfplugindocs. --rendered-website-dir is resolved relative
# to the provider dir, so the check renders to a relative temp dir we then diff.
if [ "${1:-}" = "--check" ]; then
  OUT=".docs-check-tmp"
  rm -rf "${OUT}"
  trap 'rm -rf "${WORK}" "${ROOT}/.docs-check-tmp"' EXIT
  echo "==> rendering docs to ${OUT} (check mode)"
  go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate \
    --provider-name olivares \
    --rendered-provider-name "Olivares AI" \
    --providers-schema "${WORK}/schema.json" \
    --rendered-website-dir "${OUT}"

  echo "==> checking docs/ is up to date"
  rc=0
  # Every generated page must exist in docs/ and match byte-for-byte.
  for f in $(cd "${OUT}" && find index.md resources data-sources -type f | sort); do
    if ! diff -q "docs/${f}" "${OUT}/${f}" >/dev/null 2>&1; then
      echo "  stale or missing: docs/${f}" >&2
      rc=1
    fi
  done
  # And docs/ must not carry a generated page the fresh render no longer produces.
  for f in $(cd docs && find index.md resources data-sources -type f 2>/dev/null | sort); do
    if [ ! -f "${OUT}/${f}" ]; then
      echo "  orphaned: docs/${f} (no longer generated)" >&2
      rc=1
    fi
  done
  if [ "${rc}" -ne 0 ]; then
    echo "gen-docs: docs/ is stale — run scripts/gen-docs.sh and commit the result" >&2
    exit 1
  fi
  echo "docs/ is up to date"
  exit 0
fi

echo "==> rendering docs to docs/"
go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate \
  --provider-name olivares \
  --rendered-provider-name "Olivares AI" \
  --providers-schema "${WORK}/schema.json" \
  --rendered-website-dir docs
