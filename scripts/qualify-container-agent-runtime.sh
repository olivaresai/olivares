#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Exercise runtime dependencies and a fresh named volume; no provider credentials.
set -euo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

image=${1:?usage: qualify-container-agent-runtime.sh IMAGE OWNER}
owner=${2:?repository_id-run_id-run_attempt-variant is required}
[[ "$owner" =~ ^[0-9]+-[0-9]+-[0-9]+-[a-z0-9-]+$ ]] || exit 2
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
# Supplied by ARCH's 570ce799 in the composed candidate, not copied into images.
journey="$root/scripts/session-journey-smoke.sh"
test -r "$journey"
volume="olivares-runtime-$owner"
container="$volume"
docker info >/dev/null
if docker volume inspect "$volume" >/dev/null 2>&1; then
	echo "Refusing to replace existing volume $volume" >&2
	exit 2
fi
docker volume create "$volume" >/dev/null
cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

test "$(docker image inspect --format '{{.Config.User}}' "$image")" = 65532:65532
docker run --rm --name "$container" --read-only --cap-drop ALL \
	--security-opt no-new-privileges:true --tmpfs /tmp:rw,mode=1777 \
	--mount "type=volume,src=$volume,dst=/var/lib/olivares" \
	--entrypoint /bin/sh "$image" -eu -c '
test "$(id -u)" = 65532
test "$HOME" = /var/lib/olivares/home
test -w "$HOME"
for compiler in cc gcc g++ c++ clang go make claude codex grok opencode; do
    if command -v "$compiler" >/dev/null 2>&1; then
        echo "Unexpected build tool or agent CLI: $compiler" >&2
        exit 1
    fi
done
test -r /usr/share/doc/uv/LICENSE-MIT
node --version
npm --version
python3 --version
uvx --version
git --version
/usr/local/bin/olivares version -o json
git init -q "$HOME/repository"
git -C "$HOME/repository" -c user.name=Runtime -c user.email=runtime@example.invalid \
    -c commit.gpgsign=false commit -q --allow-empty -m smoke
mkdir -p /tmp/npm-fixture "$HOME/npm-fixture"
printf "%s\n" "{\"name\":\"runtime-smoke\",\"version\":\"1.0.0\",\"bin\":{\"runtime-smoke\":\"cli.js\"}}" >/tmp/npm-fixture/package.json
printf "%s\n" "#!/usr/bin/env node" "console.log(\"npx runtime OK\")" >/tmp/npm-fixture/cli.js
chmod +x /tmp/npm-fixture/cli.js
npm pack /tmp/npm-fixture --pack-destination /tmp --quiet
npm install --prefix "$HOME/npm-fixture" --offline --ignore-scripts --no-audit --no-fund \
    /tmp/runtime-smoke-1.0.0.tgz
cd "$HOME/npm-fixture"
npx --offline runtime-smoke
python3 - <<"PY"
from pathlib import Path
from zipfile import ZipFile
wheel = Path.home() / "runtime_smoke-1.0-py3-none-any.whl"
dist = "runtime_smoke-1.0.dist-info/"
with ZipFile(wheel, "w") as archive:
    archive.writestr("runtime_smoke.py", "def main(): print(\"uvx runtime OK\")\n")
    archive.writestr(dist + "METADATA", "Metadata-Version: 2.1\nName: runtime-smoke\nVersion: 1.0\n")
    archive.writestr(dist + "WHEEL", "Wheel-Version: 1.0\nGenerator: runtime-smoke\nRoot-Is-Purelib: true\nTag: py3-none-any\n")
    archive.writestr(dist + "entry_points.txt", "[console_scripts]\nruntime-smoke-python = runtime_smoke:main\n")
    archive.writestr(dist + "RECORD", "")
PY
uvx --offline --no-managed-python --from "$HOME/runtime_smoke-1.0-py3-none-any.whl" runtime-smoke-python
printf "persistent\n" >"$HOME/runtime-volume-proof"
'
# A second container must see both the installed package and the first home.
docker run --rm --name "$container" --read-only --cap-drop ALL \
	--security-opt no-new-privileges:true --tmpfs /tmp:rw,mode=1777 \
	--mount "type=volume,src=$volume,dst=/var/lib/olivares" \
	--entrypoint /bin/sh "$image" -eu -c '
test "$(cat "$HOME/runtime-volume-proof")" = persistent
cd "$HOME/npm-fixture"
npx --offline runtime-smoke
uvx --offline --no-managed-python --from "$HOME/runtime_smoke-1.0-py3-none-any.whl" runtime-smoke-python
'
# Exercise the shipped CMD, without listener overrides or host port publishing.
# Probe inside the container so Docker's host proxy cannot mask an IPv4-only bind.
docker run -d --name "$container" --read-only --cap-drop ALL \
	--security-opt no-new-privileges:true --tmpfs /tmp:rw,mode=1777 \
	--sysctl net.ipv6.conf.all.disable_ipv6=0 \
	--mount "type=volume,src=$volume,dst=/var/lib/olivares" "$image" >/dev/null
docker exec -i "$container" python3 - <<'PYTHON'
import http.client
import socket
import ssl
import time

# The fresh engine generates its own certificate. This is a local connectivity
# probe, not certificate qualification; no credentials or user data are sent.
context = ssl._create_unverified_context()
for host in ("127.0.0.1", "::1"):
    deadline = time.monotonic() + 90
    while True:
        try:
            conn = http.client.HTTPSConnection(host, 8443, context=context, timeout=2)
            try:
                conn.request("GET", "/readyz")
                response = conn.getresponse()
                if response.status != 200:
                    raise RuntimeError(f"{host} /readyz: HTTP {response.status}")
                response.read()
            finally:
                conn.close()
            with socket.create_connection((host, 8444), timeout=2):
                pass
            break
        except (OSError, RuntimeError, http.client.HTTPException):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.5)
    print(f"PASS default CMD: HTTPS readyz and gRPC TCP on {host}")
PYTHON
docker rm -f "$container" >/dev/null
docker image inspect --format 'Qualified image ID: {{.Id}}' "$image"
docker image inspect --format 'Runtime image size (uncompressed bytes): {{.Size}}' "$image"
# The real engine and its confined session runner, with ARCH's protocol CLI double.
# Keep Docker's default seccomp profile: inability to apply Landlock is a failure
# for Root to resolve from CI evidence, never an automatic unconfined fallback.
# The CLI double lives on the executable data volume; Docker's /tmp is noexec.
docker run --rm --name "$container" --read-only --cap-drop ALL \
	--security-opt no-new-privileges:true --tmpfs /tmp:rw,mode=1777 \
	--env TMPDIR=/var/lib/olivares/home \
	--mount "type=volume,src=$volume,dst=/var/lib/olivares" \
	--mount "type=bind,src=$journey,dst=/opt/olivares-ci/scripts/session-journey-smoke.sh,readonly" \
	--entrypoint /bin/bash "$image" \
	/opt/olivares-ci/scripts/session-journey-smoke.sh \
	--binary /usr/local/bin/olivares --label image
