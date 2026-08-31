#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# npm-vuln-gate.sh — fail-closed dependency-vulnerability gate for the JavaScript surface.
#
# WHY IT EXISTS. Until 2026-08-07 this repository had a release-blocking gate for Go
# dependencies (scripts/govulncheck-gate.sh) and NOTHING AT ALL for npm: measured, zero
# occurrences of `npm audit|pnpm audit|osv-scanner|dependency-review|snyk` across
# .github/workflows and the Taskfile. That is not a small hole. The console is BUILT by these
# packages and then EMBEDDED in the published binary (core/internal/webui/dist), so a
# compromised build-time dependency travels inside the artifact whose signature the README
# advertises — the one surface the supply-chain story claims to cover.
#
# WHAT IT IS NOT. `npm audit` has no reachability analysis: unlike govulncheck it cannot say
# "your code calls it". So severity is the only lever there is, and this gate says so out loud
# rather than implying a precision it does not have. It blocks on HIGH and CRITICAL and
# REPORTS everything else, and it prints the whole census on every run so nothing hides behind
# the threshold.
#
# THE THREE ANSWERS, and this is the part that matters. A scan that did not happen is not a
# scan that found nothing. Every failure to run — tool missing, workspace that will not
# resolve, registry unreachable, JSON that does not parse — exits 2 and says the gate certifies
# NOTHING. It never degrades into a green. The Go gate learned this the hard way in 2026-08-01
# (a `|| true` made a release-blocking gate print "no called vulnerabilities" with exit 0 when
# the binary was missing), and this one is built with that lesson already applied.
set -euo pipefail

# GIT_DIR OUTRANKS THE `cd` BELOW, and this gate's whole workspace list comes from `git ls-files`.
# git exports GIT_DIR to hooks from a LINKED WORKTREE, so run from one — which is how every
# parallel session works here — this gate would enumerate the lockfiles of the repository it was
# invoked FROM rather than the one it just cd'd into, and then report that tree's verdict as this
# one's. It also pairs that git call with a `mktemp -d` sandbox, which is the second half of the
# same hazard: the session-numbers battery was measured in 2026-08-06 driving throwaway git
# commands straight into the live repository and leaving PR #526's branch on a fixture commit.
# Fail closed: a missing sanitiser is "I could not isolate", never "isolation was not needed".
#
# The derived ratchet in the git-env isolation gate found this file the moment it landed —
# it computes the class (git + mktemp) instead of carrying a hand-written list, so a new member
# cannot join quietly. It was right, and this is not merely compliance: without it the gate
# measures the wrong tree.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

ALLOW="${NPM_VULN_ALLOW:-${ROOT}/.npm-vuln-allow.yaml}"
TODAY="${NPM_VULN_TODAY:-$(date -u +%Y-%m-%d)}"
BLOCKING_SEVERITIES="${NPM_VULN_BLOCK:-high critical}"

# The audit binaries are INJECTABLE for the same reason govulncheck is in its gate: the only
# honest way to prove a gate fails closed is to break its tool on purpose and watch it refuse.
# scripts/test-gates-failclosed.sh does exactly that with /bin/false.
PNPM_BIN="${NPM_VULN_PNPM:-pnpm}"
NPM_BIN="${NPM_VULN_NPM:-npm}"

command -v python3 >/dev/null 2>&1 || {
	echo "npm-vuln-gate: python3 not found — the gate certifies nothing." >&2
	exit 2
}

# DERIVED, never hand-written. A hand-maintained list of workspaces is a list that goes stale
# the day somebody adds one, and the whole point of a supply-chain gate is that it cannot be
# silently narrowed. Every tracked lockfile in the repository is a workspace this gate covers.
# `git ls-files` rather than `find` so untracked scratch trees and node_modules cannot inject
# or hide a workspace.
mapfile -t LOCKFILES < <(git ls-files -- '*pnpm-lock.yaml' '*package-lock.json' | sort)
if [ "${#LOCKFILES[@]}" -eq 0 ]; then
	echo "npm-vuln-gate: no tracked lockfile found at all; refusing to grade nothing." >&2
	exit 2
fi

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

echo "npm-vuln-gate: ${#LOCKFILES[@]} tracked lockfile(s); blocking severities: ${BLOCKING_SEVERITIES}"

i=0
for lock in "${LOCKFILES[@]}"; do
	dir="$(dirname "${lock}")"
	case "$(basename "${lock}")" in
		pnpm-lock.yaml) tool="${PNPM_BIN}" ;;
		package-lock.json) tool="${NPM_BIN}" ;;
		*) echo "npm-vuln-gate: unrecognised lockfile ${lock}; refusing." >&2; exit 2 ;;
	esac
	i=$((i + 1))
	out="${tmp}/${i}.json"
	# `audit` exits NONZERO when it finds advisories, which is not an error — so the exit
	# status cannot be the health check here. The stream's SHAPE is: valid JSON carrying a
	# recognised advisory container. Anything else is "could not look".
	( cd "${dir}" && timeout 300 "${tool}" audit --json ) >"${out}" 2>"${out}.err" || true
	printf '%s\n' "${dir}" >"${out}.dir"
	printf '%s\n' "${tool}" >"${out}.tool"
	if [ ! -s "${out}" ]; then
		echo "npm-vuln-gate: ${tool} audit in ${dir} produced NO output; the gate certifies nothing." >&2
		sed 's/^/    /' "${out}.err" >&2 || true
		exit 2
	fi
done

python3 - "${tmp}" "${ALLOW}" "${TODAY}" "${BLOCKING_SEVERITIES}" <<'PY'
import sys, json, glob, os, re
tmp, allowf, today, blocking = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4].split()

# The dated allowlist, parsed without a YAML dependency — same controlled format as
# .govulncheck-allow.yaml so one reader understands both.
allow, cur = {}, None
if os.path.exists(allowf):
    for line in open(allowf):
        s = line.strip()
        if not s or s.startswith('#'):
            continue
        m = re.match(r'-?\s*id:\s*(\S+)', s)
        if m:
            cur = m.group(1)
            continue
        m = re.match(r'expires:\s*(\S+)', s)
        if m and cur:
            allow[cur] = m.group(1).strip('"\'')

GHSA = re.compile(r'GHSA-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{4}')

def advisories(doc):
    """Both shapes, because pnpm and npm disagree and the gate must read either.

    pnpm emits `advisories: {id: {...}}` (the npm v6 shape); npm 7+ emits
    `vulnerabilities: {name: {via: [...]}}`. A gate that understood only one would silently
    report a whole workspace as clean.
    """
    found = {}
    for v in (doc.get('advisories') or {}).values():
        sev = (v.get('severity') or '?').lower()
        m = GHSA.search(json.dumps(v))
        if m:
            found[m.group(0)] = sev
    for v in (doc.get('vulnerabilities') or {}).values():
        sev = (v.get('severity') or '?').lower()
        for via in (v.get('via') or []):
            if isinstance(via, dict):
                m = GHSA.search(via.get('url', '') or '')
                if m:
                    found[m.group(0)] = (via.get('severity') or sev).lower()
    return found

seen = {}      # ghsa -> severity
where = {}     # ghsa -> set(workspaces)
census = {}    # workspace -> {severity: n}
streams = sorted(glob.glob(os.path.join(tmp, '*.json')))
if not streams:
    print("npm-vuln-gate: no audit output at all; refusing to grade nothing.", file=sys.stderr)
    sys.exit(2)

for f in streams:
    d = open(f + '.dir').read().strip() if os.path.exists(f + '.dir') else f
    raw = open(f).read().strip()
    try:
        doc = json.loads(raw)
    except json.JSONDecodeError as exc:
        print(f"npm-vuln-gate: the audit stream for {d} does not parse ({exc}); "
              f"refusing to call it clean.", file=sys.stderr)
        sys.exit(2)
    # SHAPE CHECK. An empty object, or one with neither container, is not a clean workspace —
    # it is a workspace nobody measured. Only a document that carries a recognised container
    # (even an EMPTY one, which is how a genuinely clean tree answers) may be graded.
    if not isinstance(doc, dict) or not ({'advisories', 'vulnerabilities', 'metadata'} & set(doc)):
        print(f"npm-vuln-gate: the audit stream for {d} carries no advisory container; "
              f"it did not run. Refusing to call it clean.", file=sys.stderr)
        sys.exit(2)
    got = advisories(doc)
    census[d] = {}
    for ghsa, sev in got.items():
        seen[ghsa] = sev
        where.setdefault(ghsa, set()).add(d)
        census[d][sev] = census[d].get(sev, 0) + 1

# THE CENSUS IS ALWAYS PRINTED, clean or not. A gate that only speaks when it fails teaches
# its readers that silence means "covered", and silence is exactly what the absence of this
# gate looked like for the whole life of the repository.
print(f"npm-vuln-gate: census across {len(census)} workspace(s)")
for d in sorted(census):
    c = census[d]
    print(f"  {d:<34} " + (", ".join(f"{k}={v}" for k, v in sorted(c.items())) if c else "clean"))

fail = False
blockers = sorted(g for g, s in seen.items() if s in blocking)
for ghsa in blockers:
    exp = allow.get(ghsa)
    ws = ", ".join(sorted(where[ghsa]))
    if not exp:
        print(f"npm-vuln-gate: BLOCKING — {seen[ghsa]} advisory {ghsa} is not allowlisted ({ws})")
        fail = True
    elif exp < today:
        print(f"npm-vuln-gate: BLOCKING — allowlist entry {ghsa} EXPIRED {exp} (today {today}); re-review.")
        fail = True
    else:
        print(f"npm-vuln-gate: {ghsa} temporarily accepted (allowlist expires {exp}).")

for ghsa, exp in sorted(allow.items()):
    if ghsa not in seen and exp < today:
        print(f"npm-vuln-gate: note — allowlist entry {ghsa} expired {exp} and is no longer "
              f"present; remove it.")

if not blockers:
    print(f"npm-vuln-gate: no {'/'.join(blocking)} advisories across {len(census)} workspaces.")
sys.exit(1 if fail else 0)
PY
