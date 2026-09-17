#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic witness for the WAF identification header used by check-docs-site-live.sh.
set -uo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
CHECK="$ROOT/scripts/check-docs-site-live.sh"
RUN="$ROOT/scripts/run-network-observation.sh"
EXEC_LIB="$ROOT/scripts/lib/exec-workdir.sh"
# shellcheck source=/dev/null
. "$EXEC_LIB" || { echo "test-docs-site-live-network: cannot source $EXEC_LIB" >&2; exit 2; }

work="$(olivares_pick_exec_workdir docs-live-network-test)" || {
	echo "test-docs-site-live-network: no executable scratch" >&2
	exit 2
}
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/bin" "$work/tree/docs-site/public" \
	"$work/tree/docs-site/src/content/docs/reference" \
	"$work/tree/docs-site/src/content/docs/es/how-to"
printf '/old/* /new/ 301\n' >"$work/tree/docs-site/public/_redirects"
printf '%s\n' '# reference' >"$work/tree/docs-site/src/content/docs/reference/index.mdx"
printf '%s\n' '# español' >"$work/tree/docs-site/src/content/docs/es/index.mdx"

# The fake edge returns the measured runner symptom (403) unless the exact agreed header arrives.
# With the header it implements the five response classes the real checker needs.
cat >"$work/bin/curl" <<'CURL'
#!/bin/sh
header=""
url=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	-H) header="${2:-}"; shift 2 ;;
	-w|-A|--max-time|-o) shift 2 ;;
	-sS) shift ;;
	*) url="$1"; shift ;;
	esac
done
if [ "$header" != "X-Olivares-Docs-Live: fixture-secret" ]; then
	printf '403 '
	exit 0
fi
case "$url" in
https://docs.example/) printf '%s ' "${FAKE_ROOT_CODE:-200}" ;;
https://docs.example/old/olivares-splat-probe) printf '301 https://docs.example/new/' ;;
https://docs.example/reference/|https://docs.example/es/) printf '200 ' ;;
*/olivares-negative-control-*/) printf '404 ' ;;
*) printf '404 ' ;;
esac
CURL
chmod +x "$work/bin/curl"

fails=0
out=$(PATH="$work/bin:$PATH" OLIVARES_CLONE="$work/tree" DOCS_LIVE_PROBE=fixture-secret \
	bash "$CHECK" --host docs.example 2>&1); rc=$?
if [ "$rc" -ne 0 ]; then
	echo "test-docs-site-live-network: exact bypass header expected rc=0, got $rc: $out" >&2
	fails=$((fails + 1))
fi

out=$(PATH="$work/bin:$PATH" OLIVARES_CLONE="$work/tree" DOCS_LIVE_PROBE= \
	bash "$CHECK" --host docs.example 2>&1); rc=$?
if [ "$rc" -ne 2 ]; then
	echo "test-docs-site-live-network: WAF 403 without header expected rc=2, got $rc: $out" >&2
	fails=$((fails + 1))
fi
case "$out" in *"NO HE PODIDO MIRAR"*"403"*) ;; *) echo "test-docs-site-live-network: 403 was not named as unobservable" >&2; fails=$((fails + 1)) ;; esac

# Once the bypass works, a non-WAF HTTP failure is an observed broken deployment and stays red.
out=$(PATH="$work/bin:$PATH" OLIVARES_CLONE="$work/tree" DOCS_LIVE_PROBE=fixture-secret \
	FAKE_ROOT_CODE=500 bash "$RUN" docs-broken -- bash "$CHECK" --host docs.example 2>&1); rc=$?
if [ "$rc" -ne 1 ]; then
	echo "test-docs-site-live-network: observed root 500 through wrapper expected rc=1, got $rc: $out" >&2
	fails=$((fails + 1))
fi

# A missing promise source belongs to the tree, not the runner's route. The wrapper must preserve
# that rc=1; otherwise deleting `_redirects` would make a deploy greener.
mkdir -p "$work/source-missing"
out=$(PATH="$work/bin:$PATH" OLIVARES_CLONE="$work/source-missing" DOCS_LIVE_PROBE=fixture-secret \
	bash "$RUN" docs-source -- bash "$CHECK" --host docs.example 2>&1); rc=$?
if [ "$rc" -ne 1 ]; then
	echo "test-docs-site-live-network: missing source through wrapper expected rc=1, got $rc: $out" >&2
	fails=$((fails + 1))
fi

# MUTANT: delete the header append from a copy. Even with the secret, the positive control must
# return 403/rc=2, proving this battery exercises the transport rather than only the parser.
sed '/OLIVARES_DOCS_LIVE_HEADER:/d' "$CHECK" >"$work/check-mutant.sh"
out=$(PATH="$work/bin:$PATH" OLIVARES_CLONE="$work/tree" DOCS_LIVE_PROBE=fixture-secret \
	bash "$work/check-mutant.sh" --host docs.example 2>&1); rc=$?
if [ "$rc" -ne 2 ]; then
	echo "test-docs-site-live-network: MUTANT without header expected rc=2, got $rc: $out" >&2
	fails=$((fails + 1))
fi

if [ "$fails" -ne 0 ]; then
	echo "test-docs-site-live-network: $fails failure(s)" >&2
	exit 1
fi
echo "test-docs-site-live-network: 5/5 (header · WAF 403 · observed 500 · missing source · mutant)"
