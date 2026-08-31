#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# govulncheck-gate.sh — fail-closed dependency-vulnerability gate (E4).
#
# `task vuln` runs govulncheck and reports; this is the RELEASE-BLOCKING form.
# It scans every module, isolates the CALLED vulnerabilities (a trace that
# reaches a vulnerable symbol — govulncheck's own "your code calls it" verdict),
# and fails unless each is covered by a dated, justified entry in
# .govulncheck-allow.yaml. An EXPIRED allowlist entry also fails, so a temporary
# exception can never become a silent forever-exception. A vulnerability WITH an
# upstream fix is never allowlisted — the dependency is bumped instead.
#
# go.work caveat (golang/go#50745): govulncheck ./... only covers the current
# module, so this runs it per workspace module plus cloud/control-plane.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

GOVC="${GOVULNCHECK:-govulncheck}"
if ! command -v "${GOVC}" >/dev/null 2>&1; then
	# `go install` honours GOBIN when set — runner installations define a
	# per-runner GOBIN (e.g. /home/runner/actions-runner-3/gobin) precisely so
	# parallel runners do not race on a shared bin dir, and the binary lands
	# THERE, not in GOPATH/bin (measured: run 30610521531, "not found" seconds
	# after a successful install). Check GOBIN first, then the GOPATH default.
	GOBIN_DIR="$(go env GOBIN)"
	if [ -n "${GOBIN_DIR}" ] && [ -x "${GOBIN_DIR}/govulncheck" ]; then
		GOVC="${GOBIN_DIR}/govulncheck"
	elif [ -x "$(go env GOPATH)/bin/govulncheck" ]; then
		GOVC="$(go env GOPATH)/bin/govulncheck"
	else
		echo "govulncheck-gate: govulncheck not found — run 'task tools'." >&2
		exit 1
	fi
fi

ALLOW="${ROOT}/.govulncheck-allow.yaml"
TODAY="$(date -u +%Y-%m-%d)"

mapfile -t MODULES < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

# A scan that did not happen is not a scan that found nothing. Until 2026-08-01 both
# lines below ended in `2>/dev/null || true`, so govulncheck failing for ANY reason —
# binary missing, module that does not build, network gone while it fetches the vuln
# database, OOM kill mid-write — left an empty file that the parser read as zero
# findings, and this release-blocking gate printed "no called vulnerabilities" and
# exited 0. Demonstrated: `GOVULNCHECK=/bin/false bash scripts/govulncheck-gate.sh`
# printed exactly that, exit 0.
#
# Measured 2026-08-01 with the pinned govulncheck v1.3.0, and unlike gosec the exit
# status here IS informative: 0 on a successful scan (clean or not), 1 when the tool
# cannot run and 1 when the module does not compile. A broken module also emits a
# stream containing ONLY the `config` header — no `progress`, no `SBOM` — which is why
# the parser additionally insists on a well-formed stream rather than trusting rc alone.
scan() { # scan <label> <dir> <outfile> [env-assignment]
	local label="$1" dir="$2" out="$3" goenv="${4:-}" rc=0
	( cd "${dir}" && env ${goenv} "${GOVC}" -json ./... ) >"${out}" 2>"${out}.err" || rc=$?
	if [ "${rc}" -ne 0 ]; then
		echo "govulncheck-gate: scan of ${label} FAILED (exit ${rc}); the gate certifies nothing." >&2
		sed 's/^/    /' "${out}.err" >&2
		exit 2
	fi
	printf '%s\n' "${label}" >"${out}.label"
}

i=0
for m in "${MODULES[@]}"; do
	i=$((i + 1))
	scan "${m}" "${m}" "${tmp}/${i}.json"
done
# THE MODULES go.work DOES NOT LIST, and they are not an afterthought: this gate blocks a
# RELEASE, and `go work edit -json` only ever named the eleven modules in the workspace. Every
# Go module outside it was invisible — the scan reported "no called vulnerabilities" about code
# it had never opened, which is the same class of unearned green the fail-closed work above
# exists to remove.
#
# `commercial/commerce` is the sharpest omission: it is the MONEY path — the commerce module
# behind licence issue and delivery — and it is a module of its own
# (module github.com/olivaresai/olivares-commerce), so it was never scanned once.
#
# Each is OPTIONAL BY TREE, not by convenience: the curated public export legitimately carries
# neither cloud/ nor commercial/, so an absent directory is only forgiven when this tree says
# it is that export. Absent without the marker is a REFUSAL — "the directory is not here" must
# never be spelled the same way as "it is here and it is clean".
for extra in cloud/control-plane commercial/commerce commercial/commerce-lint; do
	if [ -d "${extra}" ]; then
		# GOWORK=off so each resolves against its OWN go.mod. Without it the workspace's
		# module set wins and the scan silently measures the wrong dependency graph.
		scan "${extra}" "${extra}" "${tmp}/extra-$(echo "${extra}" | tr '/' '-').json" "GOWORK=off"
	elif [ -f .olivares-public-export ]; then
		echo "govulncheck-gate: no ${extra} (curated public export) — skipped."
	else
		echo "govulncheck-gate: ${extra} is MISSING and this tree carries no public-export marker; refusing." >&2
		exit 2
	fi
done

python3 - "${tmp}" "${ALLOW}" "${TODAY}" <<'PY'
import sys, json, glob, os, re
tmp, allowf, today = sys.argv[1], sys.argv[2], sys.argv[3]

# Parse the dated allowlist without a YAML dependency (controlled, simple format).
allow = {}  # osv id -> expires (YYYY-MM-DD)
cur = None
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

# Collect CALLED vulnerabilities from the govulncheck JSON streams.
#
# Every stream is checked for SHAPE before it is graded. A govulncheck run emits a
# `config` header object first; a module that failed to load emits that header and
# nothing else. Grading a stream without a header — or one that stops mid-object
# because the tool was killed — would silently drop every finding after the cut, so
# both refuse rather than report a clean workspace. The old code did `except
# json.JSONDecodeError: break`, which is precisely the silent drop.
called = {}
dec = json.JSONDecoder()
streams = sorted(glob.glob(os.path.join(tmp, '*.json')))
if not streams:
    print("vuln:gate: no govulncheck output at all; refusing to grade nothing.",
          file=sys.stderr)
    sys.exit(2)
for f in streams:
    label = f
    if os.path.exists(f + '.label'):
        with open(f + '.label') as fh:
            label = fh.read().strip() or f
    txt = open(f).read()
    idx, n = 0, len(txt)
    summaries, findings, kinds = {}, [], set()
    while idx < n:
        while idx < n and txt[idx] in ' \t\r\n':
            idx += 1
        if idx >= n:
            break
        try:
            obj, idx = dec.raw_decode(txt, idx)
        except json.JSONDecodeError as exc:
            print(f"vuln:gate: the govulncheck stream for {label} is truncated at byte "
                  f"{idx} ({exc}); refusing to grade a partial scan.", file=sys.stderr)
            sys.exit(2)
        kinds.update(obj.keys())
        if isinstance(obj.get('osv'), dict):
            o = obj['osv']
            summaries[o.get('id', '')] = (o.get('summary') or '').strip()
        if isinstance(obj.get('finding'), dict):
            findings.append(obj['finding'])
    if 'config' not in kinds:
        print(f"vuln:gate: the govulncheck stream for {label} has no config header; "
              f"it did not run. Refusing to call it clean.", file=sys.stderr)
        sys.exit(2)
    for fd in findings:
        trace = fd.get('trace') or []
        if any(fr.get('function') for fr in trace):  # reaches a vulnerable symbol
            osv = fd.get('osv', '')
            called[osv] = summaries.get(osv, '')

fail = False
if not called:
    print(f"vuln:gate: no called vulnerabilities across {len(streams)} scanned modules.")
for osv, summ in sorted(called.items()):
    exp = allow.get(osv)
    if not exp:
        print(f"vuln:gate: BLOCKING — called vulnerability {osv} is not allowlisted: {summ}")
        fail = True
    elif exp < today:
        print(f"vuln:gate: BLOCKING — allowlist entry {osv} EXPIRED {exp} (today {today}); re-review.")
        fail = True
    else:
        print(f"vuln:gate: {osv} temporarily accepted (allowlist expires {exp}).")

for osv, exp in sorted(allow.items()):
    if osv not in called and exp < today:
        print(f"vuln:gate: note — allowlist entry {osv} expired {exp} and is no longer needed; remove it.")

sys.exit(1 if fail else 0)
PY
