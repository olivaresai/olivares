#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-docs-site-deploy-target.sh — runs the REAL first step of docs-site-deploy.yml, not a copy of it.
#
# That step decides which docs site a dispatch may publish: a fixed table keyed by
# `github.repository_id`, then the explicit PUBLISH confirmation, then the main-only ref. Every later
# step (credential refusal, pinned deploy, live verification) consumes its two outputs. A second
# implementation of the table in this file would test itself, so the step's `run:` block is extracted
# from the workflow and executed with the inputs GitHub would give it.
#
# Three answers: 0 all checks pass · 1 a check failed · 2 could not look (workflow unreadable, or the
# step not found exactly once).
set -uo pipefail
export LC_ALL=C

RAIZ="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
WF="$RAIZ/.github/workflows/docs-site-deploy.yml"
cannot() {
	echo "test-docs-site-deploy-target: NO HE PODIDO MIRAR: $*" >&2
	exit 2
}
[ -r "$WF" ] || cannot "falta $WF"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/docs-target.XXXXXX")" || cannot "sin directorio temporal"
trap 'rm -rf -- "$WORK"' EXIT

# The block scalar under `run: |` of the step with `id: target` (steps at six spaces, keys at eight,
# body at ten). Any non-blank line indented less than the body ends it.
awk '
	grab && $0 !~ /^          / && $0 !~ /^[[:space:]]*$/ { grab = 0; in_step = 0 }
	grab { sub(/^          /, ""); print; next }
	/^      - / { in_step = 0 }
	/^        id: target[[:space:]]*$/ { in_step = 1; hits++; next }
	in_step && /^        run: \|[[:space:]]*$/ { grab = 1; next }
	END { if (hits != 1) exit 3 }
' "$WF" >"$WORK/guard.sh" || cannot "el paso 'id: target' no aparece exactamente una vez en $WF"
[ -s "$WORK/guard.sh" ] || cannot "el paso 'id: target' no tiene bloque run"

pasados=0
fallados=0
check() { # etiqueta esperado obtenido
	if [ "$2" = "$3" ]; then
		printf '  ok   %-64s %s\n' "$1" "$3"
		pasados=$((pasados + 1))
	else
		printf '  FAIL %-64s esperado=%s obtenido=%s\n' "$1" "$2" "$3"
		fallados=$((fallados + 1))
	fi
}

# guard <script> <repository_id> <ref> <confirm> -> prints rc; outputs in $WORK/out, log in $WORK/log
guard() {
	: >"$WORK/out"
	(cd "$WORK" && env -i PATH="$PATH" REPOSITORY_ID="$2" REPOSITORY="owner/name-under-test" \
		REF="$3" CONFIRM="$4" GITHUB_OUTPUT="$WORK/out" bash "$1") >"$WORK/log" 2>&1
	echo $?
}
outputs() { tr '\n' '|' <"$WORK/out"; }

HUB=1263277317     # private development hub: its existing production dispatch target
PUBLIC=1268548683  # olivaresai/olivares: carries this workflow, production target
PREPROD=1269477539 # olivares.preprod: the Community preprod repository
MAIN=refs/heads/main
G="$WORK/guard.sh"

echo "== positive: the three mapped repositories, from main, with PUBLISH"
check "hub id -> exit 0" 0 "$(guard "$G" "$HUB" "$MAIN" PUBLISH)"
check "hub id -> production config and host" "config=wrangler.jsonc|host=docs.olivares.ai|" "$(outputs)"
check "public Community id -> exit 0" 0 "$(guard "$G" "$PUBLIC" "$MAIN" PUBLISH)"
check "public Community id -> production config and host" "config=wrangler.jsonc|host=docs.olivares.ai|" "$(outputs)"
check "preprod id -> exit 0" 0 "$(guard "$G" "$PREPROD" "$MAIN" PUBLISH)"
check "preprod id -> preprod config and host" \
	"config=wrangler.preprod.jsonc|host=docs-preprod.olivaresai.dev|" "$(outputs)"

echo "== negative: unmapped repository ids refuse and emit no target"
for id in 1 "" "*" "$PREPROD " "${PREPROD}9" "x$HUB" "$HUB;true"; do
	check "id '$id' -> exit 1" 1 "$(guard "$G" "$id" "$MAIN" PUBLISH)"
	check "id '$id' -> no outputs" "" "$(outputs)"
done
guard "$G" 1 "$MAIN" PUBLISH >/dev/null
grep -q 'has no docs deploy target' "$WORK/log" && d=si || d=no
check "unmapped refusal says why" si "$d"

echo "== negative: mapped repository, wrong ref"
for ref in refs/heads/feature refs/tags/v26.9.0 refs/heads/main2 ""; do
	check "preprod id, ref '$ref' -> exit 1" 1 "$(guard "$G" "$PREPROD" "$ref" PUBLISH)"
	check "preprod id, ref '$ref' -> no outputs" "" "$(outputs)"
done
guard "$G" "$PREPROD" refs/heads/feature PUBLISH >/dev/null
grep -q 'publishes https://docs-preprod.olivaresai.dev and may only run from main' "$WORK/log" && d=si || d=no
check "ref refusal names the selected host" si "$d"

echo "== negative: mapped repository, confirmation not given exactly"
for c in "" publish "PUBLISH " PUBLISHED; do
	check "hub id, confirm '$c' -> exit 1" 1 "$(guard "$G" "$HUB" "$MAIN" "$c")"
	check "hub id, confirm '$c' -> no outputs" "" "$(outputs)"
done

echo "== the selected config really declares the selected host"
for id in "$HUB" "$PUBLIC" "$PREPROD"; do
	guard "$G" "$id" "$MAIN" PUBLISH >/dev/null
	cfg="$(sed -n 's/^config=//p' "$WORK/out")"
	host="$(sed -n 's/^host=//p' "$WORK/out")"
	if [ -r "$RAIZ/docs-site/$cfg" ] && grep -Fq "\"pattern\": \"$host/*\"" "$RAIZ/docs-site/$cfg"; then d=si; else d=no; fi
	check "id $id: docs-site/$cfg routes $host/*" si "$d"
done
[ -r "$RAIZ/docs-site/preprod-worker.ts" ] && d=si || d=no
check "the preprod config's Worker entry exists" si "$d"

echo "== wiring: the outputs reach the deploy and the live verification, after the guard"
n() { grep -n -F -- "$1" "$WF" | head -1 | cut -d: -f1; }
first_step="$(grep -n -E '^      - ' "$WF" | head -1 | cut -d: -f1)"
check "the guard is the first step" "$((first_step + 1))" "$(n '        id: target')"
check "no secret is referenced before the guard's env ends" yes \
	"$([ "$(n 'secrets.')" -gt "$(n 'REPOSITORY_ID: ${{ github.repository_id }}')" ] && echo yes || echo no)"
check "the guard block interpolates no expression" 0 "$(grep -c -F '${{' "$G")"
check "deploy passes the selected config" 1 \
	"$(grep -c -F 'wrangler deploy --cwd docs-site --config "${DOCS_WRANGLER_CONFIG}"' "$WF")"
check "no run line deploys without --config" 0 \
	"$(grep -E '^[[:space:]]*run:.*wrangler deploy' "$WF" | grep -c -v -- '--config')"
check "config output wired to credential message and deploy" 2 \
	"$(grep -c -F 'DOCS_WRANGLER_CONFIG: ${{ steps.target.outputs.config }}' "$WF")"
check "live verification checks the selected host" 1 \
	"$(grep -c -F 'check-docs-site-live.sh --host "${DOCS_HOST}"' "$WF")"
check "host output wired to credential message and live check" 2 \
	"$(grep -c -F 'DOCS_HOST: ${{ steps.target.outputs.host }}' "$WF")"
check "pin check still runs before the deploy" yes \
	"$([ "$(n 'run: node scripts/check-wrangler-pin.mjs')" -lt "$(n 'wrangler deploy --cwd docs-site')" ] && echo yes || echo no)"
check "deploys still serialize without cancelling" 1 "$(grep -c -E '^  cancel-in-progress: false$' "$WF")"

echo "== control: the test notices a missing table row"
grep -v -F "$PREPROD)" "$G" >"$WORK/mutant.sh"
check "mutant without the preprod row refuses the preprod id" 1 "$(guard "$WORK/mutant.sh" "$PREPROD" "$MAIN" PUBLISH)"

echo "test-docs-site-deploy-target: $pasados passed, $fallados failed"
[ "$fallados" -eq 0 ] || exit 1
exit 0
