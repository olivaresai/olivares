#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Hermetic DIST-24-12 battery: local HTTP/TLS fixtures plus a real source mutant, wiring mutants
# of the self-hosted Compose dispatch, and its runtime refusals on JSON and environment fixtures.
# shellcheck disable=SC2016 # the wiring anchors and mutants match literal $VAR and ${{ }} text
set -euo pipefail
LC_ALL=C
export LC_ALL

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
scratch_parent="${TMPDIR:-}"
case "$scratch_parent" in /*) ;; *)
	printf '%s\n' 'test-compose-ready: COULD NOT CHECK — TMPDIR must be absolute' >&2
	exit 2
	;;
esac
[[ -d "$scratch_parent" ]] || {
	printf 'test-compose-ready: COULD NOT CHECK — TMPDIR is absent: %s\n' "$scratch_parent" >&2
	exit 2
}
for tool in awk cp env go grep ln mkdir mktemp mv python3 rm sed; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'test-compose-ready: COULD NOT CHECK — missing %s\n' "$tool" >&2
		exit 2
	}
done

scratch="$(mktemp -d "$scratch_parent/dist24-12.XXXXXX")"
cleanup() {
	case "$scratch" in "$scratch_parent"/dist24-12.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT INT TERM
mkdir -p "$scratch/control/readyzprobe" "$scratch/mutant/readyzprobe" "$scratch/go-cache"
cp "$root"/cmd/olivares/internal/readyzprobe/*.go "$scratch/control/readyzprobe/"
cp "$root"/cmd/olivares/internal/readyzprobe/*.go "$scratch/mutant/readyzprobe/"
printf 'module olivares.local/compose-ready-test\n\ngo 1.26\n' >"$scratch/control/go.mod"
cp "$scratch/control/go.mod" "$scratch/mutant/go.mod"

(
	cd "$scratch/control"
	GOWORK=off GOCACHE="$scratch/go-cache" GOTOOLCHAIN=local go test ./readyzprobe
) >"$scratch/control.out" 2>&1
printf '%s\n' 'ok 1 - local HTTP/TLS fixtures prove ready, not-ready and unmeasurable outcomes'

hits="$(grep -cF 'if response.StatusCode == http.StatusOK {' "$scratch/mutant/readyzprobe/probe.go")"
[[ "$hits" -eq 1 ]] || {
	printf 'test-compose-ready: COULD NOT CHECK — mutation anchor count=%s, want 1\n' "$hits" >&2
	exit 2
}
sed 's/if response.StatusCode == http.StatusOK {/if true {/' \
	"$scratch/mutant/readyzprobe/probe.go" >"$scratch/mutant/readyzprobe/probe.go.next"
mv "$scratch/mutant/readyzprobe/probe.go.next" "$scratch/mutant/readyzprobe/probe.go"
set +e
(
	cd "$scratch/mutant"
	GOWORK=off GOCACHE="$scratch/go-cache" GOTOOLCHAIN=local go test ./readyzprobe
) >"$scratch/mutant.out" 2>&1
mutant_rc=$?
set -e
if [[ "$mutant_rc" -eq 0 ]] || ! grep -Fq 'TestCheckRejectsReadyzDown' "$scratch/mutant.out"; then
	printf 'not ok - always-yes source mutant survived (rc=%s)\n' "$mutant_rc" >&2
	sed -n '1,100p' "$scratch/mutant.out" >&2
	exit 1
fi
printf 'ok 2 - mutant always-yes turns the readyz-down witness red (mutant rc=%s)\n' "$mutant_rc"

# The wiring contract must see what the self-hosted Compose dispatch depends on. Each mutant
# restores one defect in a scratch view of this tree where only the dispatch workflow and the
# CI-only no-ports layer are copies; the unmutated view must still pass.
workflow=.github/workflows/compose-ready.yml
mainline=.github/workflows/mainline-ci.yml
cache_action=.github/actions/olivares-tool-cache/action.yml
no_ports=deploy/compose/docker-compose.ready-no-ports.ci.yml
compose=deploy/compose/docker-compose.yml
agentops=deploy/compose/docker-compose.agentops.yml
postgres=deploy/compose/docker-compose.postgres.yml
view() (
	shopt -s dotglob nullglob
	dest="$scratch/$1"
	link_except() {
		local rel="$1" entry name keep skip
		shift
		mkdir -p "$dest/$rel"
		for entry in "$root/$rel"/*; do
			name="${entry##*/}"
			skip=0
			for keep in "$@"; do
				if [[ "$name" == "$keep" ]]; then skip=1; fi
			done
			if [[ "$skip" -eq 0 ]]; then ln -s "$entry" "$dest/$rel/$name"; fi
		done
	}
	link_except . .github deploy
	link_except .github workflows actions
	link_except .github/workflows compose-ready.yml mainline-ci.yml
	link_except .github/actions olivares-tool-cache
	link_except .github/actions/olivares-tool-cache action.yml
	link_except deploy compose
	link_except deploy/compose docker-compose.ready-no-ports.ci.yml docker-compose.yml docker-compose.agentops.yml docker-compose.postgres.yml
	cp "$root/$workflow" "$dest/$workflow"
	cp "$root/$mainline" "$dest/$mainline"
	cp "$root/$cache_action" "$dest/$cache_action"
	cp "$root/$no_ports" "$dest/$no_ports"
	cp "$root/$compose" "$dest/$compose"
	cp "$root/$agentops" "$dest/$agentops"
	cp "$root/$postgres" "$dest/$postgres"
)
anchor() {
	local file="$1" want="$2" pattern="$3" hits
	hits="$(grep -cE -- "$pattern" "$root/$file" || true)"
	[[ "$hits" -eq "$want" ]] || {
		printf 'test-compose-ready: COULD NOT CHECK — wiring anchor %s count=%s in %s, want %s\n' "$pattern" "$hits" "$file" "$want" >&2
		exit 2
	}
}
wiring() {
	local number="$1" name="$2" want_rc="$3" needle="$4" file="$5" rc
	shift 5
	view "$name"
	"$@" <"$root/$file" >"$scratch/$name/$file"
	set +e
	OLIVARES_ROOT="$scratch/$name" bash "$root/scripts/check-compose-ready.sh" >"$scratch/$name.out" 2>&1
	rc=$?
	set -e
	if [[ "$rc" -ne "$want_rc" ]] || ! grep -Fq -- "$needle" "$scratch/$name.out"; then
		printf 'not ok %s - %s (rc=%s, want %s)\n' "$number" "$name" "$rc" "$want_rc" >&2
		sed -n '1,40p' "$scratch/$name.out" >&2
		exit 1
	fi
	printf 'ok %s - %s (rc=%s)\n' "$number" "$name" "$rc"
}
anchor "$workflow" 2 '^          OLIVARES_ADMIN_PASSWORD: '
anchor "$workflow" 1 'uses: \./\.github/actions/olivares-tool-cache'
anchor "$workflow" 1 '^    runs-on: '
anchor "$workflow" 3 '\$\{\{ github\.repository_id \}\}'
anchor "$workflow" 3 '\$\{\{ github\.run_id \}\}'
anchor "$workflow" 3 '\$\{\{ github\.run_attempt \}\}'
anchor "$workflow" 1 '^      OLIVARES_IMAGE: '
anchor "$workflow" 4 '-p "\$READY_POSITIVE_PROJECT"'
anchor "$workflow" 4 '-p "\$READY_NEGATIVE_PROJECT"'
anchor "$workflow" 4 '^            -f deploy/compose/docker-compose\.ready-no-ports\.ci\.yml( \\)?$'
anchor "$workflow" 1 'check-compose-ready\.sh effective-config "\$config" "\$READY_POSITIVE_PROJECT"'
anchor "$workflow" 1 '^          docker image rm "\$OLIVARES_IMAGE"$'
anchor "$workflow" 1 '^          if ! listed="\$\(docker image ls '
anchor "$no_ports" 1 '^    ports: !override \[\]$'
anchor "$no_ports" 1 '^    container_name: !reset null$'
anchor "$compose" 1 '^name: olivares$'
anchor "$compose" 1 '^    container_name: olivares$'

wiring 3 unmutated-view 0 'compose-ready contract: OK' "$workflow" cat
wiring 4 admin-password-omitted 1 "without ['OLIVARES_ADMIN_PASSWORD']" "$workflow" \
	sed -e '/^          OLIVARES_ADMIN_PASSWORD: /d'
wiring 5 task-install-omitted 1 'without first installing the pinned Task' "$workflow" \
	sed -e '/uses: \.\/\.github\/actions\/olivares-tool-cache/d'
wiring 6 hosted-runner-fallback 1 'must route its job to' "$workflow" \
	sed -e "s/^    runs-on: .*/    runs-on: \${{ vars.CI_RUNNER || 'ubuntu-latest' }}/"
wiring 7 owner-without-repository 1 'READY_POSITIVE_PROJECT does not vary with repository_id' "$workflow" \
	sed -e 's/\${{ github\.repository_id }}/1/g'
wiring 8 owner-without-run 1 'READY_POSITIVE_PROJECT does not vary with run_id' "$workflow" \
	sed -e 's/\${{ github\.run_id }}/1/g'
wiring 9 owner-without-attempt 1 'READY_POSITIVE_PROJECT does not vary with run_attempt' "$workflow" \
	sed -e 's/\${{ github\.run_attempt }}/1/g'
wiring 10 commit-shared-image-tag 1 "OLIVARES_IMAGE uses the expression 'github.sha'" "$workflow" \
	sed -e 's/^      OLIVARES_IMAGE: .*/      OLIVARES_IMAGE: olivares-compose-ready:${{ github.sha }}/'
wiring 11 fixed-project-in-cleanup 1 "'name: remove negative fixture' runs Compose outside its owned project" "$workflow" \
	awk '/-p "\$READY_NEGATIVE_PROJECT"/ && ++seen == 4 { sub(/-p "\$READY_NEGATIVE_PROJECT"/, "-p olivares-ready-negative") } { print }'
wiring 12 cleanup-crosses-ownership 1 "'name: remove positive fixture' does not clean exactly the project and layers" "$workflow" \
	awk '/-p "\$READY_POSITIVE_PROJECT"/ && ++seen == 4 { sub(/READY_POSITIVE_PROJECT/, "READY_NEGATIVE_PROJECT") } { print }'
layer_cases=(positive-leg positive-cleanup negative-leg negative-cleanup)
layer_steps=(
	'name: reference SQLite Compose reaches healthy'
	'name: remove positive fixture'
	'name: running Postgres standby makes compose wait return one'
	'name: remove negative fixture'
)
for occurrence in 1 2 3 4; do
	wiring "$((12 + occurrence))" "no-ports-layer-missing-${layer_cases[occurrence - 1]}" 1 \
		"'${layer_steps[occurrence - 1]}' runs Compose without the final no-ports layer" "$workflow" \
		awk -v n="$occurrence" '/docker-compose\.ready-no-ports\.ci\.yml/ && ++seen == n { next } { print }'
done
wiring 17 plain-ports-list 1 'must contain only services.olivares.container_name: !reset null' "$no_ports" \
	sed -e 's/ports: !override \[\]/ports: []/'
# The second thing that layer undoes: a container name is unique per daemon, so dropping the
# reset would collide two runs of this workflow on one runner.
wiring 17b dropped-container-name-reset 1 'must contain only services.olivares.container_name: !reset null' "$no_ports" \
	sed -e '/container_name: !reset null/d'
# And the base file must keep BOTH of the things that layer exists to undo, or the layer is
# inert and nothing says so.
wiring 17c base-without-project-name 1 'must name its project' "$compose" \
	sed -e '/^name: olivares$/d'
wiring 17d base-without-container-name 1 'must pin container_name: olivares' "$compose" \
	sed -e '/^    container_name: olivares$/d'

# The base file's one host project mount: OLIVARES_PROJECT_DIR at /project, or the declared
# olivares-project volume, never PWD (the checkout, for the documented commands) or a
# relative default that would resolve against deploy/compose/.
anchor "$compose" 1 '^      - "\$\{OLIVARES_PROJECT_DIR:-olivares-project\}:/project"$'
anchor "$compose" 1 '^  olivares-project:$'
anchor "$workflow" 1 '^          docker exec "\$cid" test -d /project -a ! -e /project/deploy/compose/docker-compose\.yml$'
wiring 17e base-without-project-mount 1 'must mount the host project folder at /project' "$compose" \
	sed -e '/:\/project"$/d'
wiring 17f project-mount-defaults-to-pwd 1 'must mount the host project folder at /project' "$compose" \
	sed -e 's/:-olivares-project}/:-${PWD}}/'
wiring 17g project-mount-relative-fallback 1 'must mount the host project folder at /project' "$compose" \
	sed -e 's/:-olivares-project}/:-.}/'
# A Docker daemon refuses a CPU limit above its CPU count, so a shipped `cpus` above 1 is a
# minimum of that many CPUs and `up` fails on a 1-CPU host. No Compose file
# carries one, in any YAML spelling; a literal 1.0 stays accepted (the 26.10.1 value).
anchor "$compose" 1 '^        limits:$'
anchor "$compose" 1 '^          memory: 2G$'
anchor "$compose" 1 '^    restart: unless-stopped$'
anchor "$agentops" 1 '^        limits:$'
anchor "$postgres" 1 '^          cpus: "1\.0"$'
wiring 17h base-cpu-limit-above-one 1 "docker-compose.yml sets cpus '2.0'" "$compose" \
	sed -e '/^        limits:$/a\          cpus: "2.0"'
wiring 17i base-cpu-limit-interpolated 1 "docker-compose.yml sets cpus '\${OLIVARES_CPUS:-2.0}'" "$compose" \
	sed -e '/^        limits:$/a\          cpus: "${OLIVARES_CPUS:-2.0}"'
wiring 17j agentops-cpu-limit-above-one 1 "docker-compose.agentops.yml sets cpus '2.0'" "$agentops" \
	sed -e '/^        limits:$/a\          cpus: "2.0"'
wiring 17k base-cpu-limit-one 0 'compose-ready contract: OK' "$compose" \
	sed -e '/^        limits:$/a\          cpus: "1.0"'
wiring 17l base-cpu-limit-quoted-key 1 "docker-compose.yml sets cpus '2.0'" "$compose" \
	sed -e '/^        limits:$/a\          "cpus": "2.0"'
wiring 17m base-service-level-cpus 1 "docker-compose.yml sets cpus '2'" "$compose" \
	sed -e '/^    restart: unless-stopped$/i\    cpus: 2'
wiring 17n base-cpu-limit-flow-style 1 "docker-compose.yml sets cpus '2'" "$compose" \
	sed -e 's/^        limits:$/        limits: {cpus: 2, memory: 2G}/' -e '/^          memory: 2G$/d'
wiring 17o postgres-overlay-cpu-limit-above-one 1 "docker-compose.postgres.yml sets cpus '2.0'" "$postgres" \
	sed -e 's/^          cpus: "1\.0"$/          cpus: "2.0"/'
wiring 17p project-volume-undeclared 1 'must mount the host project folder at /project' "$compose" \
	sed -e '/^  olivares-project:$/d'
wiring 17q checkout-witness-dropped 1 'must check that /project in the running container is not the checkout' "$workflow" \
	sed -e '/docker exec "\$cid" test -d \/project -a ! -e \/project\/deploy\/compose\/docker-compose\.yml$/d'
wiring 18 effective-config-not-before-up 1 \
	"'name: reference SQLite Compose reaches healthy' does not refuse unsupported Compose or a published port before up" \
	"$workflow" sed -e '/check-compose-ready\.sh effective-config "\$config" "\$READY_POSITIVE_PROJECT"/d'
wiring 19 image-removal-forced 1 'must delete only its exact owned image tag' "$workflow" \
	sed -e 's/^          docker image rm "\$OLIVARES_IMAGE"$/          docker image rm --force "$OLIVARES_IMAGE"/'
wiring 20 image-removal-failure-swallowed 1 "swallows an inspection or removal failure: ['docker image rm" "$workflow" \
	sed -e 's/^          docker image rm "\$OLIVARES_IMAGE"$/& || true/'
wiring 21 image-inspection-failure-swallowed 1 "swallows an inspection or removal failure: ['if ! listed=" "$workflow" \
	sed -e "/^          if ! listed=/s/')\"; then\$/' 2>\/dev\/null || true)\"; then/"
wiring 22 image-prune-added 1 'must delete only its exact owned image tag' "$workflow" \
	sed -e 's/^          docker image rm "\$OLIVARES_IMAGE"$/&\n          docker image prune --force/'

# Pin drift in the actual cache action or a per-call override must still refuse.
wiring 23 cache-default-drift 1 'Task pin differs from mainline-ci.yml' "$cache_action" \
	sed -e '/^  task_version:/,/^  task_sha256:/s/default: "3.51.1"/default: "3.51.2"/'
wiring 24 cache-use-removed 1 'Task pins are not one version: []' "$mainline" \
	sed -e 's@uses: ./.github/actions/olivares-tool-cache@uses: ./.github/actions/unselected-tool-cache@g'
wiring 25 cache-override-drift 1 'Task pins are not one version:' "$mainline" \
	awk '/^        uses: \.\/\.github\/actions\/olivares-tool-cache$/ && ++seen == 1 { print; print "        with:\n          task_version: 3.51.2"; next } { print }'
wiring 26 cache-same-version-override 0 'compose-ready contract: OK' "$mainline" \
	awk '/^        uses: \.\/\.github\/actions\/olivares-tool-cache$/ && ++seen == 1 { print; print "        with:\n          task_version: 3.51.1"; next } { print }'
wiring 27 cache-dynamic-version 1 'must be a literal semantic version' "$cache_action" \
	sed -e '/^  task_version:/,/^  task_sha256:/s/default: "3.51.1"/default: "${{ vars.TASK_VERSION }}"/'

# Release qualification must follow the same bare MAJOR.MINOR tags as release.yml.
anchor "$workflow" 1 '^    tags: '
for trigger in '["2*", "3*"]' '["v[0-9]*.[0-9]*"]' '["[0-9]*.[0-9]*.[0-9]*"]' '[]'; do
	wiring 28 "release-trigger-$trigger" 1 'must qualify bare MAJOR.MINOR release tags' "$workflow" \
		awk -v tags="$trigger" '/^    tags:/ { print "    tags: " tags; next } { print }'
done

# The runtime refusals the workflow runs before touching Docker and before `up`, on JSON and
# environment fixtures shaped like `docker compose config --format json`. They are not a Docker
# witness: on the runner the effective configuration comes from the real Compose merge.
check="$root/scripts/check-compose-ready.sh"
expect() {
	local want_rc="$1" needle="$2" rc
	shift 2
	set +e
	"$@" >"$scratch/expect.out" 2>&1
	rc=$?
	set -e
	if [[ "$rc" -ne "$want_rc" ]] || ! grep -Fq -- "$needle" "$scratch/expect.out"; then
		printf 'not ok - %s (rc=%s, want %s)\n' "$*" "$rc" "$want_rc" >&2
		sed -n '1,20p' "$scratch/expect.out" >&2
		exit 1
	fi
}
ln -s "$root" "$scratch/checkout-link"
python3 - "$scratch/json" "$root" "$scratch/checkout-link" <<'PY'
import copy
import json
import pathlib
import sys

out = pathlib.Path(sys.argv[1])
out.mkdir()
owner = "101-202-3"
image = f"olivares-compose-ready:{owner}"


def stack(leg, volumes, services):
    project = f"olivares-ready-{owner}-{leg}"
    return {
        "name": project,
        "services": {name: {"image": "postgres:16-alpine" if name == "postgres" else image} for name in services},
        "networks": {"default": {"name": f"{project}_default"}},
        "volumes": {volume: {"name": f"{project}_{volume}"} for volume in volumes},
    }


positive = stack("positive", ["olivares-data"], ["olivares"])
negative = stack("negative", ["olivares-data", "olivares-standby-data", "pg-data"],
                 ["olivares", "olivares-standby", "postgres"])
fixtures = {"positive": positive, "negative": negative}
fixtures["positive-base-ports"] = copy.deepcopy(positive)
fixtures["positive-base-ports"]["services"]["olivares"]["ports"] = [
    {"mode": "ingress", "host_ip": "127.0.0.1", "target": 8443, "published": "8443", "protocol": "tcp"},
    {"mode": "ingress", "host_ip": "127.0.0.1", "target": 8444, "published": "8444", "protocol": "tcp"},
]
fixtures["negative-ephemeral-port"] = copy.deepcopy(negative)
fixtures["negative-ephemeral-port"]["services"]["olivares-standby"]["ports"] = [
    {"mode": "ingress", "target": 8443, "protocol": "tcp"},
]
fixtures["negative-other-attempt"] = copy.deepcopy(negative)
fixtures["negative-other-attempt"]["name"] = "olivares-ready-101-202-2-negative"
fixtures["positive-shared-volume"] = copy.deepcopy(positive)
fixtures["positive-shared-volume"]["volumes"]["olivares-data"] = {"name": "olivares-data", "external": True}
fixtures["negative-fixed-container-name"] = copy.deepcopy(negative)
fixtures["negative-fixed-container-name"]["services"]["postgres"]["container_name"] = "olivares-postgres"
fixtures["positive-commit-image"] = copy.deepcopy(positive)
fixtures["positive-commit-image"]["services"]["olivares"]["image"] = (
    "olivares-compose-ready:cde3b0a986392089ef358fd82eb03d6d43b71645"
)
# The /project mount as Compose resolves it from the checkout root: the default volume, a
# host project outside the checkout, and binds that hand sessions the checkout's deploy secrets.
root = pathlib.Path(sys.argv[2])
elsewhere = root.parent / f"{root.name}-project"
project_volume = {"type": "volume", "source": "olivares-project", "target": "/project", "volume": {}}
positive["volumes"]["olivares-project"] = {"name": f"{positive['name']}_olivares-project"}
positive["services"]["olivares"]["volumes"] = [project_volume]
for name, source in (("checkout", root), ("checkout-deploy", root / "deploy" / "compose"),
                     ("checkout-parent", root.parent), ("checkout-link", pathlib.Path(sys.argv[3])),
                     ("elsewhere", elsewhere)):
    fixtures[f"positive-project-{name}"] = copy.deepcopy(positive)
    fixtures[f"positive-project-{name}"]["services"]["olivares"]["volumes"] = [
        {"type": "bind", "source": str(source), "target": "/project", "bind": {"create_host_path": True}},
    ]
fixtures["positive-project-volumes-map"] = copy.deepcopy(positive)
fixtures["positive-project-volumes-map"]["services"]["olivares"]["volumes"] = {"/project": project_volume}
fixtures["positive-project-bind-no-source"] = copy.deepcopy(positive)
fixtures["positive-project-bind-no-source"]["services"]["olivares"]["volumes"] = [{"type": "bind", "target": "/project"}]
for name, document in fixtures.items():
    (out / f"{name}.json").write_text(json.dumps(document), encoding="utf-8")
(out / "truncated.json").write_text('{"name": ', encoding="utf-8")
PY
p=olivares-ready-101-202-3-positive
n=olivares-ready-101-202-3-negative
img=olivares-compose-ready:101-202-3
json="$scratch/json"
expect 0 "$p publishes no host port" bash "$check" effective-config "$json/positive.json" "$p" "$img" olivares
expect 0 "$n publishes no host port" \
	bash "$check" effective-config "$json/negative.json" "$n" "$img" olivares olivares-standby postgres
printf '%s\n' 'ok 28 - effective-config accepts both owned stacks without a published port'
expect 1 'service olivares publishes 2 host port(s)' \
	bash "$check" effective-config "$json/positive-base-ports.json" "$p" "$img" olivares
expect 1 'service olivares-standby publishes 1 host port(s)' \
	bash "$check" effective-config "$json/negative-ephemeral-port.json" "$n" "$img" olivares olivares-standby postgres
printf '%s\n' 'ok 29 - effective-config refuses the shipped published ports and an ephemeral one'
expect 1 "effective project name 'olivares-ready-101-202-2-negative'" \
	bash "$check" effective-config "$json/negative-other-attempt.json" "$n" "$img" olivares olivares-standby postgres
expect 1 "volume olivares-data is named 'olivares-data', outside project $p" \
	bash "$check" effective-config "$json/positive-shared-volume.json" "$p" "$img" olivares
expect 1 "service postgres fixes container_name 'olivares-postgres'" \
	bash "$check" effective-config "$json/negative-fixed-container-name.json" "$n" "$img" olivares olivares-standby postgres
expect 1 "service olivares runs image 'olivares-compose-ready:cde3b0a" \
	bash "$check" effective-config "$json/positive-commit-image.json" "$p" "$img" olivares
expect 1 "effective services ['olivares'], want ['olivares', 'postgres']" \
	bash "$check" effective-config "$json/positive.json" "$p" "$img" olivares postgres
expect 1 "project 'olivares-ready-positive' and image '$img' are not one owned identity" \
	bash "$check" effective-config "$json/positive.json" olivares-ready-positive "$img" olivares
printf '%s\n' "ok 30 - effective-config refuses another attempt's project, a shared volume, a fixed container name, a commit-shared image, a missing service and an unowned project"
expect 2 'COULD NOT CHECK' bash "$check" effective-config "$json/truncated.json" "$p" "$img" olivares
expect 2 'COULD NOT CHECK' bash "$check" effective-config "$json/absent.json" "$p" "$img" olivares
expect 2 'COULD NOT CHECK' bash "$check" effective-config "$json/positive.json" "$p" "$img"
printf '%s\n' 'ok 31 - an unreadable effective configuration or a missing service list answers 2, not accepted'
expect 1 'Compose 2.24.3 predates 2.24.4' bash "$check" compose-version 2.24.3
expect 0 '2.24.4 >= 2.24.4' bash "$check" compose-version 2.24.4
expect 0 '2.29.1 >= 2.24.4' bash "$check" compose-version v2.29.1
expect 2 'COULD NOT CHECK' bash "$check" compose-version ''
printf '%s\n' 'ok 32 - compose-version requires 2.24.4 for the !override layer and refuses to guess'
expect 0 'compose-ready owner: 101-202-3' \
	env READY_POSITIVE_PROJECT="$p" READY_NEGATIVE_PROJECT="$n" OLIVARES_IMAGE="$img" bash "$check" owner
expect 1 "READY_POSITIVE_PROJECT='olivares-ready--202-3-positive' is not owned" \
	env READY_POSITIVE_PROJECT=olivares-ready--202-3-positive READY_NEGATIVE_PROJECT=olivares-ready--202-3-negative \
	OLIVARES_IMAGE=olivares-compose-ready:-202-3 bash "$check" owner
expect 1 'name different owners' \
	env READY_POSITIVE_PROJECT="$p" READY_NEGATIVE_PROJECT="$n" OLIVARES_IMAGE=olivares-compose-ready:101-202-4 bash "$check" owner
expect 1 'READY_NEGATIVE_PROJECT=None is not owned' \
	env -u READY_NEGATIVE_PROJECT READY_POSITIVE_PROJECT="$p" OLIVARES_IMAGE="$img" bash "$check" owner
printf '%s\n' 'ok 33 - owner refuses an empty repository id, mixed attempts and a missing project'
# Run from the checkout root, as the README and upgrade commands do, the session folder must
# not be the checkout: its deploy/compose holds the DR passphrase and the Postgres .env.
for leg in checkout checkout-deploy checkout-parent checkout-link; do
	expect 1 'service olivares mounts the Olivares checkout' \
		bash "$check" effective-config "$json/positive-project-$leg.json" "$p" "$img" olivares
done
expect 1 'set OLIVARES_PROJECT_DIR to your project folder' \
	bash "$check" effective-config "$json/positive-project-checkout.json" "$p" "$img" olivares
expect 0 "$p publishes no host port" \
	bash "$check" effective-config "$json/positive-project-elsewhere.json" "$p" "$img" olivares
expect 2 'service olivares volumes are not a list of mounts' \
	bash "$check" effective-config "$json/positive-project-volumes-map.json" "$p" "$img" olivares
expect 2 'service olivares binds None' \
	bash "$check" effective-config "$json/positive-project-bind-no-source.json" "$p" "$img" olivares
printf '%s\n' 'ok 34 - effective-config refuses a session mount of the checkout, inside, above or through a link, accepts a project elsewhere and cannot look at malformed mounts'
printf '%s\n' 'CI-ONLY: docker compose up --wait against the built non-root image and a running Postgres standby.'
printf '%s\n' '1..34'
