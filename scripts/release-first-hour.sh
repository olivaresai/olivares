#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# This deliberately caches an old official :latest and builds the candidate under the
# release tag, so it runs only in GitHub Actions: on a hosted runner's disposable daemon,
# or on a shared self-hosted daemon, where those two tags are daemon-wide and one run owns
# them at a time (the lock below). Container names, ports, volumes and the network are the
# run's own either way (deploy/compose/docker-compose.first-hour.ci.yml and the project name).
set -euo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
[[ $# == 0 ]] || { echo 'release-first-hour takes no arguments' >&2; exit 2; }
[[ ${GITHUB_ACTIONS:-} == true ]] || {
  echo 'release-first-hour runs only in GitHub Actions: it retags the daemon'\''s official :latest' >&2
  exit 2
}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
# Fail closed instead of racing another run for the daemon-wide tags. Released with the process.
exec 9>"${FIRST_HOUR_LOCK:-/tmp/olivares-first-hour.lock}"
flock -n 9 || {
  echo 'another release-first-hour run owns this Docker daemon'\''s image tags; rerun when it ends' >&2
  exit 1
}
version=$(sed '/^#/d; /^$/d' RELEASE-VERSION)
[[ $version =~ ^[0-9]+\.[0-9]+$ ]] || exit 2
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]] || exit 2
project=first-hour-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}
work=$(mktemp -d "${RUNNER_TEMP:?}/first-hour.XXXXXX")
chmod 700 "$work"
export FIRST_HOUR_SECRETS_FILE="$work/browser-secrets.json"
printf '[]\n' > "$FIRST_HOUR_SECRETS_FILE"
chmod 600 "$FIRST_HOUR_SECRETS_FILE"
export FIRST_HOUR_ARTIFACTS="$RUNNER_TEMP/first-hour-artifacts-candidate"
mkdir -p "$FIRST_HOUR_ARTIFACTS"
unset OLIVARES_IMAGE COMPOSE_FILE COMPOSE_PROFILES
image=docker.io/olivaresai/olivares:$version
latest=docker.io/olivaresai/olivares:latest
owned_tags=()
declare -A previous=()
compose=(docker compose -p "$project" -f deploy/compose/docker-compose.yml
  -f deploy/compose/docker-compose.first-hour.ci.yml)
cleanup() {
  local rc=$?
  trap - EXIT
  # Capture the entire engine lifetime before down, including failures after
  # startup. Raw logs and browser credentials never enter the upload directory.
  if ! "${compose[@]}" logs --no-color --timestamps olivares > "$work/engine.log" 2>&1; then
    echo 'FAIL: engine log collection failed' >&2
    rc=1
  fi
  python3 scripts/sanitize-first-hour.py "$work/engine.log" \
    "$FIRST_HOUR_ARTIFACTS/engine.log" "$FIRST_HOUR_SECRETS_FILE" || rc=1
  "${compose[@]}" down --volumes --remove-orphans >/dev/null || rc=1
  # Only the tags this run took, after its containers are gone; no force, no prune. A tag that
  # existed before is put back on its image, so another workload's :latest survives the run.
  if ((${#owned_tags[@]})); then
    docker image rm "${owned_tags[@]}" >/dev/null || rc=1
    for tag in "${owned_tags[@]}"; do
      [[ -z ${previous[$tag]:-} ]] || docker tag "${previous[$tag]}" "$tag" || rc=1
    done
  fi
  rm -rf "$work"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# A Compose that ignores the layer's tags would keep the fixed container name and publish both
# ports on every interface. Read the effective configuration before anything is pulled or started.
"${compose[@]}" config --format json | python3 -c '
import json, sys
service = json.load(sys.stdin)["services"]["olivares"]
ports = service.get("ports") or []
if (service.get("container_name") or len(ports) != 1 or ports[0].get("host_ip") != "127.0.0.1"
        or str(ports[0].get("target")) != "8443" or ports[0].get("published")):
    raise SystemExit("FAIL: effective Compose config is not the run-private layer: "
                     + json.dumps({"container_name": service.get("container_name"), "ports": ports}))
'
# Remember what the two daemon-wide tags pointed at, if anything.
for tag in "$latest" "$image"; do
  previous[$tag]=$(docker image inspect --format '{{.Id}}' "$tag" 2>/dev/null || true)
done

# Exactly the cached-image condition of a returning user. Do not pull :latest or
# force --pull=always during up: that would hide a broken release manifest.
docker pull "$latest"
owned_tags+=("$latest")
# No tag or publication: reuse every canonical build setting and hook. Only
# snapshot's version template changes so the pre-tag binary has RELEASE-VERSION.
[[ $(git rev-parse HEAD) == "${GITHUB_SHA:?}" ]]
status=$(git status --porcelain)
[[ -z $status ]]
export FIRST_HOUR_VERSION="$version"
# Replace the one version_template of the existing snapshot section; a second
# snapshot key is invalid YAML, and a changed shape must fail here, not in goreleaser.
python3 -B - .goreleaser.yaml "$work/goreleaser.yaml" <<'PYSNAPSHOT'
import pathlib, re, sys
text = pathlib.Path(sys.argv[1]).read_text(encoding='utf-8')
text, count = re.subn(r"^(snapshot:\n  version_template: ).*$", r"\1'{{ .Env.FIRST_HOUR_VERSION }}'",
                      text, flags=re.M)
if count != 1:
    raise SystemExit("FAIL: .goreleaser.yaml needs exactly one snapshot version_template, found %d" % count)
pathlib.Path(sys.argv[2]).write_text(text, encoding='utf-8')
PYSNAPSHOT
mkdir -p "$work/runtime"
goreleaser build --snapshot --clean --single-target --id olivares \
  --config "$work/goreleaser.yaml" --output "$work/runtime/olivares"
sha256sum "$work/runtime/olivares" | cut -d ' ' -f1 > "$FIRST_HOUR_ARTIFACTS/build-sha256.txt"
cp Dockerfile.release "$work/runtime/Dockerfile"
# The build's before hooks generated the release notice.
bash scripts/assemble-runtime-context.sh "$work/runtime" .license-notices/NOTICE-community
docker build -t "$image" "$work/runtime"
owned_tags+=("$image")
# No engine overrides: same image selection, command, policy, TLS, mounts and
# limits as the README, except the run-private container name and ports of the CI layer.
# Empty project volume; no seeded user, profile or passkey.
"${compose[@]}" up --wait --wait-timeout 120
cid=$("${compose[@]}" ps -q olivares)
test -n "$cid"
# The daemon's own record of every published port, not only the console's: loopback or fail.
if docker port "$cid" | grep -qv -- '-> 127\.0\.0\.1:'; then
  echo 'FAIL: the engine publishes a port beyond loopback' >&2
  exit 1
fi
# Identity probes use no optional image utilities. Cleanup publishes redacted logs.
docker cp "$cid:/usr/local/bin/olivares" "$work/olivares"
sha256sum "$work/olivares" | tee "$FIRST_HOUR_ARTIFACTS/engine-sha256.txt"
for tool in claude codex opencode; do
  docker exec "$cid" /usr/local/bin/olivares agent tool detect --driver "$tool" --output json |
    python3 -c 'import json, sys; candidates = json.load(sys.stdin); assert isinstance(candidates, list); print(sys.argv[1] + (": present" if candidates else ": absent"))' "$tool"
done | tee "$FIRST_HOUR_ARTIFACTS/tools-before-setup.txt"
docker inspect --format '{{.Image}}' "$cid" | tee "$FIRST_HOUR_ARTIFACTS/running-image.txt"
docker image inspect --format '{{.Id}}' "$image" > "$FIRST_HOUR_ARTIFACTS/candidate-image.txt"
docker exec "$cid" /usr/local/bin/olivares version --output json | tee "$FIRST_HOUR_ARTIFACTS/binary-version.json"
# Stop before the browser if Compose selected stale bytes or the binary was
# stamped incorrectly. A source tag or image label alone cannot prove identity.
if ! python3 - "$FIRST_HOUR_ARTIFACTS" "$version" <<'PY'
import base64, hashlib, json, os, re, sys
from pathlib import Path
evidence, version = Path(sys.argv[1]), sys.argv[2]
def require(condition, message):
    if not condition:
        raise SystemExit(message)
require(evidence.joinpath('running-image.txt').read_text() == evidence.joinpath('candidate-image.txt').read_text(), 'Compose selected a different image')
binary = json.loads(evidence.joinpath('binary-version.json').read_text())
require(binary['version'] == version, 'binary version differs from release')
require(evidence.joinpath('engine-sha256.txt').read_text().split()[0] == evidence.joinpath('build-sha256.txt').read_text().strip(), 'running executable differs from canonical build')
require(re.fullmatch('[0-9a-f]{7,40}', binary['commit']) and os.environ['GITHUB_SHA'].startswith(binary['commit']), 'binary commit differs from candidate')
for field, key in [('license_key', 'OLIVARES_LICENSE_PUBKEY'), ('ota_key', 'OLIVARES_OTA_PUBKEY')]:
    fingerprint = hashlib.sha256(base64.b64decode(os.environ[key].strip(), validate=True)).hexdigest()[:8]
    require(binary[field] == 'release/' + fingerprint, field + ' differs from release anchor')
PY
then
  printf '%s\n' 'FAIL: release container identity mismatch' 'O1 J1-release-container: FAIL' 'O7 J7-release-container: FAIL' | tee "$FIRST_HOUR_ARTIFACTS/identity-failure.txt"
  exit 1
fi
"${compose[@]}" logs --no-color olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p' | python3 -c '
import re, sys
from pathlib import Path
tokens = re.findall(r"olst_[A-Z0-9]+", sys.stdin.read())
if not tokens:
    raise SystemExit("first boot did not emit a setup token")
Path(sys.argv[1]).write_text(tokens[0])
' "$work/setup-token"
chmod 600 "$work/setup-token"
export PLAYWRIGHT_SETUP_TOKEN_FILE="$work/setup-token"
export PLAYWRIGHT_RELEASE_VERSION="$version"
# Docker chose the host port; ask Compose for it. Loopback only: the setup token is live.
console=$("${compose[@]}" port olivares 8443)
[[ $console =~ ^127\.0\.0\.1:[0-9]+$ ]] || { echo "FAIL: console is not published on loopback: $console" >&2; exit 1; }
export PLAYWRIGHT_BASE_URL=https://$console
# The only stand-in is the provider, on the engine's loopback. The engine and
# console use their real APIs. The real OpenCode CLI is installed through the UI.
cat > "$work/provider.yml" <<YAML
services:
  provider-stub:
    image: $image
    network_mode: service:olivares
    entrypoint: [python3, /provider.py]
    volumes:
      - $root/web/e2e-release/provider.py:/provider.py:ro
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
YAML
compose+=(-f "$work/provider.yml")
"${compose[@]}" up -d provider-stub
# The browser must not inherit the daemon-lock descriptor: an orphan would keep the next run out.
pnpm --dir web exec playwright test --config playwright.release.config.ts first-hour.spec.ts --workers=1 9>&-
