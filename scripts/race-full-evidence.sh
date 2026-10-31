#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
#
# race-full-evidence.sh — is there a GREEN race-full run whose head_sha IS this SHA?
#
# This diagnostic checks exact SHA only. The release preflight uses
# require-green-run.sh, which also accepts successful push/dispatch evidence on
# an identical Git tree. A negative here is not the release gate's verdict.
#
# THREE ANSWERS, because "no evidence" and "I could not look" decide differently:
#   0  evidence exists — the run id is printed
#   1  no green run on that SHA (whether or not runs exist for it)
#   2  COULD NOT LOOK — no token, the API refused, or the SHA given is too short
#
# Usage:  bash scripts/race-full-evidence.sh [SHA]
#         SHA defaults to the tip of the public repository's main.
#         OLIVARES_ORG_TOKEN_FILE overrides where the org token is read from.
#         OLIVARES_PUBLIC_REPO overrides the repository (default olivaresai/olivares).
set -uo pipefail

REPO="${OLIVARES_PUBLIC_REPO:-olivaresai/olivares}"
TOK="${OLIVARES_ORG_TOKEN_FILE:-/workspace/.secrets/olivaresai-org-token}"

if [ ! -f "$TOK" ]; then
	echo "race-full-evidence: COULD NOT LOOK — no org token at $TOK" >&2
	exit 2
fi
GH_TOKEN="$(cat "$TOK")"
export GH_TOKEN

SHA="${1:-}"
if [ -z "$SHA" ]; then
	SHA="$(gh api "repos/${REPO}/commits/main" --jq .sha 2>/dev/null)" || SHA=""
	if [ -z "$SHA" ]; then
		echo "race-full-evidence: COULD NOT LOOK — the API did not return the tip of main" >&2
		exit 2
	fi
fi

RUNS="$(gh api "repos/${REPO}/actions/workflows/race-full.yml/runs?per_page=100" 2>/dev/null)" || {
	echo "race-full-evidence: COULD NOT LOOK — the workflow-runs API call failed" >&2
	exit 2
}
# gh prints API errors on STDOUT as JSON, so a 403 arrives looking like data. Check the
# shape before trusting it: without this a permissions failure reads as "no runs".
case "$RUNS" in
	*'"workflow_runs"'*) : ;;
	*)
		echo "race-full-evidence: COULD NOT LOOK — response carries no workflow_runs (permissions?)" >&2
		exit 2
		;;
esac

printf '%s' "$RUNS" | SHA="$SHA" python3 -c '
import sys, os, json, datetime
d = json.load(sys.stdin)
sha = os.environ["SHA"]
# head_sha from the API is FORTY characters. Comparing it with == against an
# abbreviated SHA yields zero matches and reads as "no evidence" -- a perfectly
# formed negative, with no error and no suspicious zero, on the question that
# decides whether a tag can be cut. Match by PREFIX, and refuse a prefix so short
# it could match more than one commit.
if len(sha) < 7:
    print("race-full-evidence: COULD NOT LOOK - SHA given is shorter than 7 characters")
    sys.exit(2)
runs = [r for r in d.get("workflow_runs", []) if (r.get("head_sha") or "").startswith(sha)]
print("race-full-evidence: SHA %s - %d race-full run(s) on THAT sha" % (sha[:12], len(runs)))
green = None
for r in runs:
    a = datetime.datetime.fromisoformat(r["created_at"].replace("Z", "+00:00"))
    b = datetime.datetime.fromisoformat(r["updated_at"].replace("Z", "+00:00"))
    print("    %s %s/%s %s -> %d min" % (r["id"], r["status"], r.get("conclusion") or "-",
                                         r["created_at"], round((b - a).total_seconds() / 60)))
    if r.get("conclusion") == "success":
        green = r
if green:
    print("  => EVIDENCE EXISTS: run %s is success on the exact SHA" % green["id"])
    sys.exit(0)
# A run still in flight has conclusion None, so it is not success -- and treating that
# as "no evidence" confuses "not yet" with "failed". Those are different answers and the
# caller acts differently on each: one waits, the other stops the tag. Corrected after
# making exactly this mistake about another contributor push, using the rule this script exists
# to enforce.
running = [r for r in runs if r.get("status") != "completed"]
if running:
    print("  => STILL RUNNING on that SHA -- %d in flight, none concluded yet" % len(running))
    for r in running:
        print("     %s %s (started %s)" % (r["id"], r["status"], r["created_at"]))
    print("     This is NOT the same as no evidence: it is no evidence YET. Wait, do not re-dispatch.")
    sys.exit(2)
if runs:
    print("  => NO green run on that SHA (%d concluded, none successful)" % len(runs))
    sys.exit(1)
print("  => NO race-full run at all on that SHA")
print("     exact-SHA diagnostic only; release also checks identical-tree qualification")
sys.exit(1)
'
