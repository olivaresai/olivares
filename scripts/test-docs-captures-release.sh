#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Drives scripts/docs-captures-release.sh (or $RELEASE_SCRIPT) through a fake registry: a curl shim that answers the
# token, the index, the manifest and the layer, so the script runs from its first request to the check of the version
# of the extracted binary, with no network and without starting an engine. It exists because the script broke at run
# time (an edit removed three definitions) and `bash -n` could not see it. Each case is refused or accepted as the
# script's own checks say: a cut layer is resumed, a corrupt one fails its digest, an HTTP error stops with its cause,
# a manifest with no size is refused, a tag that resolves to another image is refused.
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
SCRIPT="${RELEASE_SCRIPT:-$ROOT/scripts/docs-captures-release.sh}"
for tool in jq tar gzip sha256sum; do command -v "$tool" >/dev/null || { echo "FAIL $tool is needed" >&2; exit 2; }; done
# The shim and the fake binary must execute, and /tmp is noexec in these containers: use the repository's own probe.
. "$ROOT/scripts/lib/exec-tmpdir.sh"
EXEC_TMP="$(olivares_exec_tmpdir)" || { echo "FAIL no executable temporary directory; set OLIVARES_EXEC_TMPDIR" >&2; exit 2; }
T="$(mktemp -d "$EXEC_TMP/release-test.XXXXXX")"
trap 'rm -rf "$T"' EXIT

# A registry in $T/<name>: one layer holding usr/local/bin/olivares (it prints a version that is NOT the tag, so the
# run ends at the version check), a manifest, an index, and the curl shim.
registry() { # name, layer mode, declared size ("" = the real one)
  local d="$T/$1" size layer manifest
  mkdir -p "$d/tree/usr/local/bin" "$d/bin"
  printf '#!/bin/sh\necho "olivares 9.9 (fake)"\n' >"$d/tree/usr/local/bin/olivares"
  chmod 0755 "$d/tree/usr/local/bin/olivares"
  tar -czf "$d/layer.tgz" -C "$d/tree" usr
  layer="$(sha256sum "$d/layer.tgz" | cut -d' ' -f1)"
  size="${3:-$(stat -c %s "$d/layer.tgz")}"
  printf '{"layers":[{"digest":"sha256:%s","size":%s}]}' "$layer" "$size" >"$d/manifest.json"
  manifest="$(sha256sum "$d/manifest.json" | cut -d' ' -f1)"
  printf '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"sha256:%s"}]}' "$manifest" >"$d/index.json"
  sha256sum "$d/index.json" | cut -d' ' -f1 >"$d/index.digest"
  printf '%s' "$2" >"$d/blob.mode"
  cat >"$d/bin/curl" <<'SHIM'
#!/usr/bin/env bash
out="" hdr="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -D) hdr="$2"; shift ;;
    -C | -H | --max-time | --max-filesize | --retry | --retry-delay | --max-redirs | --proto | --proto-redir) shift ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
d="$FAKE_REGISTRY"
case "$url" in
  *auth.docker.io/token*) printf '{"token":"fake"}' ;;
  */manifests/sha256:*) cp "$d/manifest.json" "$out" ;;
  */manifests/*)
    cp "$d/index.json" "$out"
    printf 'HTTP/2 200\r\ndocker-content-digest: sha256:%s\r\n\r\n' "$(cat "$d/index.digest")" >"$hdr" ;;
  */blobs/*)
    case "$(cat "$d/blob.mode")" in
      http-error) echo "curl: (22) The requested URL returned error: 404" >&2; exit 22 ;;
      cut-once)
        if [ ! -e "$d/cut.done" ]; then
          touch "$d/cut.done"
          head -c "$(($(stat -c %s "$d/layer.tgz") / 2))" "$d/layer.tgz" >"$out"
          echo "curl: (18) transfer closed with bytes remaining to read" >&2
          exit 18
        fi
        tail -c +"$(($(stat -c %s "$out") + 1))" "$d/layer.tgz" >>"$out" ;;
      corrupt) head -c "$(stat -c %s "$d/layer.tgz")" /dev/zero >"$out" ;;
      *) cp "$d/layer.tgz" "$out" ;;
    esac ;;
  *) echo "fake curl: unexpected url $url" >&2; exit 99 ;;
esac
SHIM
  chmod +x "$d/bin/curl"
}

fail=0
expect() { # case name, registry name, wanted text, [expected digest]
  local d="$T/$2" digest="${4:-sha256:$(cat "$T/$2/index.digest")}" out
  out="$(TMPDIR="$T" FAKE_REGISTRY="$d" PATH="$d/bin:$PATH" bash "$SCRIPT" 0.1 "$digest" 2>&1)" && out="(it exited 0) $out"
  if [[ "$out" == *"$3"* ]]; then printf 'ok   %s\n' "$1"; else printf 'FAIL %s: wanted "%s" in:\n%s\n' "$1" "$3" "$out" >&2; fail=1; fi
}

registry cut cut-once;       expect 'a layer cut once is resumed to the declared size and the run reaches the version check' cut 'is not release 0.1'
registry cutmsg cut-once;    expect 'the cut is reported, not hidden'                                                       cutmsg 'connection cut'
registry bad corrupt;        expect 'a corrupt layer fails its digest'                                                      bad 'failed its digest'
registry http http-error;    expect 'an HTTP error stops the run with its cause'                                            http 'curl exit 22'
registry nosize whole 0;     expect 'a manifest with no usable size is refused'                                             nosize 'declares no usable size'
registry other whole;        expect 'a tag that resolves to another image is refused'                                       other 'expected sha256:0000' "sha256:$(printf '0%.0s' {1..64})"
[ "$fail" = 0 ]
