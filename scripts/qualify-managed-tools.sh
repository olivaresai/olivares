#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Measure real managed installs at the old and shipped Compose resource limits.
# Run only on a Docker CI runner; projects and volumes belong to this run.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
bash scripts/check-compose-ready.sh owner
bash scripts/check-compose-ready.sh compose-version "$(docker compose version --short)"
files=(-f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.ready-no-ports.ci.yml)
# Refuse pre-existing resources before arming cleanup.
for project in "$READY_POSITIVE_PROJECT" "$READY_NEGATIVE_PROJECT"; do
    containers=$(docker ps -aq --filter "label=com.docker.compose.project=$project")
    test -z "$containers"
    volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$project")
    test -z "$volumes"
    networks=$(docker network ls -q --filter "label=com.docker.compose.project=$project")
    test -z "$networks"
done
work=$(mktemp -d "${RUNNER_TEMP:?}/managed-tools.XXXXXX")
cleanup() {
    rc=$?
    trap - EXIT
    for project in "$READY_POSITIVE_PROJECT" "$READY_NEGATIVE_PROJECT"; do
        docker compose -p "$project" "${files[@]}" down --volumes --remove-orphans || rc=1
    done
    rm -rf "$work"
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cat > "$work/baseline.json" <<'JSON'
{"services":{"olivares":{"deploy":{"resources":{"limits":{"cpus":"1.0","memory":"1G"}}}}}}
JSON
version=$(python3 -c 'import json; print(json.load(open("cmd/olivares/internal/toolinstall/qualified/opencode.json"))["tag_name"].removeprefix("v"))')
for profile in baseline defaults; do
    project=$READY_POSITIVE_PROJECT
    overrides=()
    if [[ "$profile" == baseline ]]; then
        project=$READY_NEGATIVE_PROJECT
        overrides=(-f "$work/baseline.json")
    fi
    compose=(docker compose -p "$project" "${files[@]}" "${overrides[@]}")
    "${compose[@]}" config --format json > "$work/config.json"
    bash scripts/check-compose-ready.sh effective-config "$work/config.json" "$project" "$OLIVARES_IMAGE" olivares
    "${compose[@]}" up --wait --wait-timeout 120
    cid=$("${compose[@]}" ps -q olivares)
    # Record actual enforced limits and image identity, not just YAML intent.
    docker inspect "$cid" | python3 -c '
import json, sys
c = json.load(sys.stdin)[0]
h = c["HostConfig"]
assert h["ReadonlyRootfs"] and c["Config"]["User"] == "65532:65532"
assert not h["PortBindings"]
assert h["CapDrop"] == ["ALL"]
assert any(opt.startswith("no-new-privileges") for opt in h["SecurityOpt"])
expected = (1000000000, 1073741824) if sys.argv[1] == "baseline" else (0, 2147483648)
assert (h["NanoCpus"], h["Memory"]) == expected
print(json.dumps({"profile": sys.argv[1], "image": c["Image"], "nano_cpus": h["NanoCpus"], "memory_bytes": h["Memory"]}))
' "$profile"
    docker exec "$cid" olivares version -o json
    if ! docker exec -i "$cid" python3 - install "$profile" "$version" < scripts/managed-tools-journey.py; then
        if [[ "$profile" != baseline ]]; then exit 1; fi
        echo 'baseline: install/status failed; defaults must still pass independently'
        "${compose[@]}" down --volumes --remove-orphans
        continue
    fi
    "${compose[@]}" up --force-recreate --wait --wait-timeout 120
    cid=$("${compose[@]}" ps -q olivares)
    if ! docker exec -i "$cid" python3 - recreated "$profile" "$version" < scripts/managed-tools-journey.py; then
        if [[ "$profile" != baseline ]]; then exit 1; fi
        echo 'baseline: recreated tool status failed; defaults must still pass independently'
    fi
    "${compose[@]}" down --volumes --remove-orphans
done
