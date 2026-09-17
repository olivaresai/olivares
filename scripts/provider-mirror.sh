#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Build a Terraform/OpenTofu PROVIDER NETWORK-MIRROR index from a directory of
# already-built provider zips (air-gap half). Given the goreleaser
# output dir (the terraform-provider-olivares_{VERSION}_{OS}_{ARCH}.zip files and
# their SHA256SUMS), it emits the two JSON documents a network mirror serves so an
# air-gapped consumer can install the provider with:
#
#   provider_installation {
#     network_mirror { url = "https://mirror.internal/providers/" }
#   }
#
# Layout produced under <out>/<HOST>/<NAMESPACE>/<TYPE>/ (default coordinate
# registry.terraform.io/olivaresai/olivares):
#   index.json          -> {"versions":{"X.Y.Z":{}}}
#   <version>.json      -> {"archives":{"<os>_<arch>":{"url":...,"hashes":["h1:..."]}}}
#
# WHY h1: (verified, not zh:) — the network-mirror protocol requires the h1: hash
# scheme; Terraform REJECTS zh: hashes from a network mirror ("this version of
# Terraform does not support any of the checksum formats given"). zh: is the hex
# SHA256 of the whole .zip (what SHA256SUMS holds); h1: is SHA256 over the SORTED
# per-entry "%x  %s\n" lines of the zip CONTENTS, base64-encoded — a DIFFERENT
# value that cannot be derived from SHA256SUMS. This script therefore computes h1:
# directly from each zip (golang.org/x/mod/sumdb/dirhash Hash1/HashZip), so we never
# fabricate a hash. Refs:
#   https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol
#   https://developer.hashicorp.com/terraform/language/files/dependency-lock#zh-and-h1-hashes
#
# OpenTofu consumes the SAME mirror unchanged (protocol-compatible provider
# artifacts); see terraform-provider-olivares/docs/PUBLISHING.md.
#
# Usage:
#   scripts/provider-mirror.sh --in dist/ --out mirror/ [--version 1.2.3] \
#       [--host registry.terraform.io] [--namespace olivaresai] [--type olivares] \
#       [--base-url ""]
#
# --version   restrict to one version (default: every version found in --in).
# --base-url  prefix prepended to each archive "url" (default: relative filename, as
#             the protocol allows resolving the url relative to the {version}.json).
#
# Requires: python3, find. NO network access — purely local file hashing.
set -euo pipefail

IN=""
OUT=""
ONLY_VERSION=""
HOST="registry.terraform.io"
NAMESPACE="olivaresai"
TYPE="olivares"
BASE_URL=""

usage() { sed -n '2,46p' "$0"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --in) IN="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --version) ONLY_VERSION="$2"; shift 2 ;;
    --host) HOST="$2"; shift 2 ;;
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --type) TYPE="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown arg: $1" >&2; usage >&2; exit 2 ;;
  esac
done

command -v python3 >/dev/null || { echo "error: python3 not found" >&2; exit 2; }
[ -n "$IN" ]  && [ -d "$IN" ]  || { echo "error: --in <dir of provider zips> required" >&2; exit 2; }
[ -n "$OUT" ]                  || { echo "error: --out <mirror dir> required" >&2; exit 2; }

DEST="${OUT%/}/${HOST}/${NAMESPACE}/${TYPE}"
mkdir -p "$DEST"

# All emission + h1: computation lives in python3 (zip reading + base64 + json are
# fiddly and error-prone in pure shell; this keeps the hash provably correct).
IN="$IN" DEST="$DEST" ONLY_VERSION="$ONLY_VERSION" TYPE="$TYPE" BASE_URL="$BASE_URL" python3 - <<'PY'
import base64, hashlib, json, os, re, sys, zipfile

src   = os.environ["IN"]
dest  = os.environ["DEST"]
only  = os.environ["ONLY_VERSION"]
ptype = os.environ["TYPE"]
base  = os.environ["BASE_URL"]

# terraform-provider-<type>_<version>_<os>_<arch>.zip
name_re = re.compile(
    r"^terraform-provider-" + re.escape(ptype) +
    r"_(?P<version>[^_]+)_(?P<os>[^_]+)_(?P<arch>[^_]+)\.zip$"
)

def h1(zip_path):
    """h1: hash of a provider zip = base64(sha256(sorted "<sha256hex>  <name>\\n"
    lines of the zip's entries)), per golang.org/x/mod/sumdb/dirhash Hash1/HashZip."""
    lines = []
    with zipfile.ZipFile(zip_path) as zf:
        for n in sorted(i.filename for i in zf.infolist()):
            digest = hashlib.sha256(zf.read(n)).hexdigest()
            lines.append("%s  %s\n" % (digest, n))
    summary = "".join(lines).encode("utf-8")
    return "h1:" + base64.standard_b64encode(hashlib.sha256(summary).digest()).decode("ascii")

# versions[version][ "<os>_<arch>" ] = {url, hashes}
versions = {}
for entry in sorted(os.listdir(src)):
    m = name_re.match(entry)
    if not m:
        continue
    ver = m.group("version")
    if only and ver != only:
        continue
    platform = "%s_%s" % (m.group("os"), m.group("arch"))
    url = (base.rstrip("/") + "/" + entry) if base else entry
    versions.setdefault(ver, {})[platform] = {
        "url": url,
        "hashes": [h1(os.path.join(src, entry))],
    }

if not versions:
    sys.stderr.write("error: no terraform-provider-%s_*.zip archives matched in %s\n" % (ptype, src))
    sys.exit(3)

# index.json — {"versions":{"X.Y.Z":{}}}
index = {"versions": {v: {} for v in versions}}
with open(os.path.join(dest, "index.json"), "w") as f:
    json.dump(index, f, indent=2, sort_keys=True)
    f.write("\n")

# <version>.json — {"archives":{"<os>_<arch>":{url,hashes}}}
for ver, archives in versions.items():
    with open(os.path.join(dest, ver + ".json"), "w") as f:
        json.dump({"archives": archives}, f, indent=2, sort_keys=True)
        f.write("\n")

for ver in sorted(versions):
    print("  %s  (%d platforms)" % (ver, len(versions[ver])))
print("wrote index.json + %d version file(s) to %s" % (len(versions), dest))
PY
