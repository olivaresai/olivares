#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-workflow-node-setup.sh — every workflow job that invokes node (directly, via
# pnpm/npm, or through a task whose commands run node) must install it EXPLICITLY, with
# actions/setup-node, BEFORE the first consuming step.
#
# WHY (P0, 2026-07-30). control-plane ran five node lints (task lint:i18n and friends)
# on the assumption that the runner ships node — true of ubuntu-latest, false of the
# self-hosted runner CI_RUNNER points at. Its only setup-node sat far below, next to the
# openapi steps it was added for, so run 30558139324 died in 58s at 'console i18n
# parity' and no mainline-ci run concluded after the runner move: the required contexts
# could never report and nothing could merge. "The runner has the tool" is a
# convenience of hosted runners, never a contract; this gate pins the node instance of
# that class. (Other tools of the same class — docker, jq, gh, curl on self-hosted
# jobs — are declared residuals where not explicitly installed; see the P0 report.)
#
# Mechanics: for each job (2-space YAML key under jobs:), flag any step line that runs
# node/pnpm/npm directly or invokes a task from the known node-consuming set, and
# require an actions/setup-node line EARLIER IN THE SAME JOB. Line-based on purpose —
# it is a lint on declared shape, cheap enough for the pre-push gate.
set -uo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
WFDIR="${OLIVARES_NODE_SETUP_WORKFLOWS:-$ROOT/.github/workflows}"
TASKFILE="${OLIVARES_NODE_SETUP_TASKFILE:-$ROOT/Taskfile.yml}"

# THE TASK LIST IS DERIVED, NOT TYPED (2026-08-06). It used to be one hand-written
# string with the instruction "this must grow with the Taskfile" written above it —
# and a list that grows by remembering does not grow. Measured on 2026-08-06: TEN
# Taskfile tasks ran node/pnpm/npm/npx and were absent from it (build:web, fmt,
# fmt:check, lint:brand-parity, lint:commits, lint:web, sdk:test:ts, setup,
# test:license-worker, web:codegen). Two of them were already used by real workflow
# steps. The probe that proved the hole is one file:
#
#   jobs: {worker: {steps: [checkout, {run: task test:license-worker}]}}   -> OK, exit 0
#
# A job running a task that shells out to npm, with no setup-node anywhere, approved.
# That is the exact shape of the 58s death this gate exists to prevent.
#
# So the DIRECT executors now come from the Taskfile itself, every run, and the only
# hand-kept part is the INDIRECT set below — tasks that reach node one hop down through
# `deps:`/`- task:` and therefore carry no node token of their own. Resolving go-task's
# graph properly was tried and abandoned (see D-2 in .githooks/pre-push): both `deps:`
# spellings produced zero edges. Five is a set small enough to declare with a reason.
NODE_TASKS_INDIRECT='openapi:check|sdk:check|web:check|web:e2e|commit:lint'

# Derivation, and the three answers apply to it too: a Taskfile we cannot read, or one
# that yields ZERO direct node tasks, means the consumer half of this gate is blind. An
# empty derivation would silently degrade the check to "only literal npm lines count" —
# green, examining a rule it no longer has.
if [ ! -r "$TASKFILE" ]; then
	echo "check-workflow-node-setup: CANNOT LOOK — no readable Taskfile at $TASKFILE." >&2
	echo "  The node-consuming task set is derived from it; without it this gate cannot" >&2
	echo "  tell a node consumer from any other step, and it will not approve on a guess." >&2
	exit 2
fi
NODE_TASKS_DIRECT="$(awk '
	/^  [a-zA-Z0-9:_-]+:$/ { cur = $1; sub(/:$/, "", cur); next }
	cur != "" && /(^|[^a-zA-Z-])(node|pnpm|npm|npx)[[:space:]]/ { print cur }
' "$TASKFILE" | sort -u | paste -sd '|' -)"
if [ -z "$NODE_TASKS_DIRECT" ]; then
	echo "check-workflow-node-setup: CANNOT LOOK — derived ZERO node-consuming tasks from" >&2
	echo "  $TASKFILE. This repository has always had some; a Taskfile that yields none means" >&2
	echo "  the derivation stopped matching, not that the tasks stopped running node." >&2
	exit 2
fi
NODE_TASKS="${NODE_TASKS_DIRECT}|${NODE_TASKS_INDIRECT}"

# THREE ANSWERS, NOT TWO (2026-08-05): clean / dirty / I COULD NOT LOOK. This gate
# used to have only two, and the third was silently reported as the first. Measured
# — it printed OK, exit 0, in all of these:
#
#   - the workflow directory does not exist (a moved .github/, a bad checkout, a
#     typo in OLIVARES_NODE_SETUP_WORKFLOWS): the glob matches nothing, the loop
#     body never runs, `fail` stays 0.
#   - the directory exists and is EMPTY: same path, same green.
#   - every workflow is named *.yaml: GitHub Actions accepts BOTH extensions, this
#     glob only ever matched *.yml, so a repo could migrate its filenames and the
#     gate would keep approving a set of files it had stopped reading.
#
# In each case the gate examined ZERO files and said the codebase was fine. A gate
# that cannot see its subject must say so, not approve it.
if [ ! -d "$WFDIR" ]; then
	echo "check-workflow-node-setup: CANNOT LOOK — no workflow directory at $WFDIR." >&2
	echo "  Nothing was examined, so nothing can be approved. Point" >&2
	echo "  OLIVARES_NODE_SETUP_WORKFLOWS at the workflows, or restore .github/workflows." >&2
	exit 2
fi

examined=0
fail=0
for f in "$WFDIR"/*.yml "$WFDIR"/*.yaml; do
	[ -f "$f" ] || continue
	examined=$((examined + 1))
	out="$(awk -v tasks="$NODE_TASKS" '
		/^  [a-zA-Z_-]+:$/ {
			job = $1; sub(/:$/, "", job)
			has_setup = 0
			next
		}
		/uses:.*actions\/setup-node/ { has_setup = 1 }
		{
			line = $0
			sub(/#.*/, "", line)
			consumer = 0
			if (line ~ /(^|[[:space:]"(&;|])(node|pnpm|npm)[[:space:]]/ && line ~ /run:|^[[:space:]]+/ && line !~ /uses:/) {
				# direct node/pnpm/npm invocation inside a run script line
				if (line ~ /(^|[[:space:]"(&;|])(node|pnpm|npm)[[:space:]]+[a-zA-Z@.\/-]/) consumer = 1
			}
			if (line ~ ("task +(" tasks ")([[:space:]]|$)")) consumer = 1
			if (consumer && !has_setup) {
				printf "%s: job %s runs a node consumer before any setup-node: %s\n", FILENAME, job, $0
			}
		}
	' "$f")"
	if [ -n "$out" ]; then
		printf '%s\n' "$out" >&2
		fail=1
	fi

	# THE STEP THAT INSTALLS THE TOOL IS ITSELF A CONSUMER (2026-08-05). The rule above asks
	# that setup-node precede every node consumer, and pnpm/action-setup is placed BEFORE it
	# on purpose: actions/setup-node's `cache: pnpm` needs pnpm to already exist. But that
	# action's default self-installer is a NODE script, so on a runner with no preinstalled
	# node it dies before the node it depends on is installed — a chicken-and-egg the first
	# rule cannot see, because the offending step is a `uses:`, not a `run:`. Measured:
	# mainline-ci run 30982224609 lost BOTH `web` and `control-plane` exactly there, with
	# "self-installer exits with code 1". `standalone: true` fetches the pnpm binary directly
	# and breaks the cycle.
	out2="$(awk '
		/^  [a-zA-Z_-]+:$/ { job = $1; sub(/:$/, "", job); has_setup = 0; pending = 0; next }
		/uses:.*actions\/setup-node/ { has_setup = 1 }
		/standalone:[[:space:]]*true/ { pending = 0 }
		/^      - / {
			if (pending) { printf "%s:%d: job %s: pnpm/action-setup precedes setup-node without standalone: true\n", FILENAME, pending, job; pending = 0 }
			if ($0 ~ /uses:.*pnpm\/action-setup/ && !has_setup) pending = FNR
		}
		END { if (pending) printf "%s:%d: job %s: pnpm/action-setup precedes setup-node without standalone: true\n", FILENAME, pending, job }
	' "$f")"
	if [ -n "$out2" ]; then
		printf '%s\n' "$out2" >&2
		fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo "" >&2
	echo "check-workflow-node-setup: FAIL — install node explicitly (pinned actions/setup-node," >&2
	echo "node-version 24) BEFORE the first consuming step. Hosted preinstallation is not a contract." >&2
	exit 1
fi

if [ "$examined" -eq 0 ]; then
	echo "check-workflow-node-setup: CANNOT LOOK — $WFDIR holds no *.yml or *.yaml workflow." >&2
	echo "  Zero files were examined. That is not a clean verdict, and this gate will not" >&2
	echo "  report one for a subject it never read." >&2
	exit 2
fi

echo "check-workflow-node-setup: OK — $examined workflow file(s) examined; every node-consuming step follows an explicit setup-node in its job"
