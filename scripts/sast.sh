#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# sast.sh — static application security testing (SAST) over every Go module.
#
# WHY: the gate had structural lints, govulncheck (dependency CVEs) and gitleaks
# (secrets), but NO source-level SAST — nothing flagged a new SQL-injection,
# command-injection, SSRF or weak-crypto sink. This closes that gap with gosec
# (E1). gosec is a pure-Go tool installed via `go install` (task tools),
# checksum-verified through the module proxy — no curl|bash, consistent with the
# rest of the toolchain.
#
# Two modes:
#   scripts/sast.sh                 # BLOCKING gate: only the high-signal rules in
#                                   # .gosec.json block_rules; ANY finding fails.
#   scripts/sast.sh --report FILE   # INFORMATIONAL full scan (every rule) to a
#                                   # JSON file; never fails (feeds security:report).
#
# The blocking gate runs one curated set of rules (SQL/command/template injection,
# SSRF, weak crypto, TLS misconfig) and requires ZERO findings. Every genuine
# exception carries a dedicated `// #nosec Gxxx -- <reason>` comment: the flags
# -nosec-require-rules and -nosec-require-justification make a bare or reason-less
# #nosec fail, so a suppression can never be a silent blanket. The high-false-
# positive rules (name-matched credentials, unhandled errors, integer overflow,
# file-path-from-variable, permissions) are non-blocking but stay visible in the
# --report scan. Rationale per rule class: docs/security/SECURITY-GATE.md.
#
# go.work caveat (golang/go#50745): `gosec ./...` only covers the current module,
# so — like vuln/build/test — this runs gosec once per workspace module, plus the
# out-of-workspace cloud/control-plane module (GOWORK=off).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

GOSEC="${GOSEC:-gosec}"
if ! command -v "${GOSEC}" >/dev/null 2>&1; then
	if [ -x "$(go env GOPATH)/bin/gosec" ]; then
		GOSEC="$(go env GOPATH)/bin/gosec"
	else
		echo "sast: gosec not found — run 'task tools' (installs the pinned gosec)." >&2
		exit 1
	fi
fi

# The blocking rule set lives in .gosec.json (block_rules): the single, reviewed
# source of truth for what the SAST gate treats as release-blocking.
mapfile -t BLOCK_RULES < <(python3 -c '
import json,sys
with open(".gosec.json") as f: d=json.load(f)
print("\n".join(d["block_rules"]))
')
[ "${#BLOCK_RULES[@]}" -gt 0 ] || { echo "sast: no block_rules in .gosec.json" >&2; exit 1; }
INCLUDE="$(IFS=,; echo "${BLOCK_RULES[*]}")"

# Enumerate workspace modules from go.work, then append the out-of-workspace cloud
# module (built with GOWORK=off, same as build:cloud / test:cloud).
mapfile -t MODULES < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
[ "${#MODULES[@]}" -gt 0 ] || { echo "sast: no modules in go.work" >&2; exit 1; }

REPORT=""
if [ "${1:-}" = "--report" ]; then
	REPORT="${2:?--report needs an output path}"
fi

run_gosec_block() {
	# Blocking scan for one module dir. -exclude-generated drops go:generate'd files
	# (genpb, SDK op-layers). require-rules/justification enforce annotation quality.
	# cloud/control-plane is outside go.work, so it scans with GOWORK=off (as build:cloud).
	#
	# NOT -quiet. Measured 2026-08-01 with the pinned gosec v2.28.0: under -quiet a
	# CLEAN module and a module that DOES NOT COMPILE both produce exit 0 and zero
	# bytes on stdout — byte for byte the same thing — so the gate could not tell
	# "nothing to report" from "nothing was scanned". Without -quiet, gosec always
	# emits the JSON envelope (Issues, Stats, Golang errors), and the difference is
	# legible. The exit status discriminates nothing either way: with -no-fail it is
	# 0 in all three cases (clean, findings, broken module).
	local dir="$1"; shift
	local goenv=""
	case "${dir}" in
	cloud/control-plane) goenv="GOWORK=off" ;;
	esac
	( cd "${dir}" && env ${goenv} "${GOSEC}" -no-fail \
		-include="${INCLUDE}" \
		-exclude-generated \
		-nosec-require-rules \
		-nosec-require-justification \
		-fmt=json "$@" ./... )
}

if [ -n "${REPORT}" ]; then
	# INFORMATIONAL full scan: every rule, merged JSON, never fails the caller — but it
	# must not under-report in silence either. This used to drop both gosec's stderr
	# (`|| true`) and every unreadable scan (`except Exception: pass`), so a module that
	# stopped building simply vanished from the totals and the report looked BETTER.
	# Now a module that could not be scanned is named in the JSON (skipped_modules /
	# unreadable_scans) and on stderr; the exit status stays 0 because this feed is
	# advisory, but the number it publishes is qualified by what it could not read.
	#
	# -quiet is absent here for the same measured reason as in the blocking gate: it
	# collapses "clean" and "did not compile" into the same zero bytes, and the exit
	# status cannot separate them either (always 0 under -no-fail). The per-module
	# verdict therefore comes from the JSON body, keyed on package-level Golang errors.
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp}"' EXIT
	i=0
	for m in "${MODULES[@]}"; do
		i=$((i + 1))
		( cd "${m}" && "${GOSEC}" -no-fail -exclude-generated -fmt=json ./... ) \
			>"${tmp}/${i}.json" 2>"${tmp}/${i}.err" || true
		printf '%s\n' "${m}" >"${tmp}/${i}.name"
	done
	( cd cloud/control-plane && GOWORK=off "${GOSEC}" -no-fail -exclude-generated -fmt=json ./... ) \
		>"${tmp}/cloud.json" 2>"${tmp}/cloud.err" || true
	printf '%s\n' "cloud/control-plane" >"${tmp}/cloud.name"
	python3 - "${tmp}" "${REPORT}" <<'PY'
import sys, json, glob, os
from collections import Counter

d, out = sys.argv[1], sys.argv[2]
issues = []
unreadable = []
skipped = []
for f in sorted(glob.glob(os.path.join(d, "*.json"))):
    namefile = f[:-len(".json")] + ".name"
    label = os.path.basename(f)
    if os.path.exists(namefile):
        with open(namefile) as fh:
            label = fh.read().strip() or label
    try:
        with open(f) as fh:
            doc = json.load(fh)
    except Exception as exc:
        unreadable.append("%s: %s" % (label, exc))
        continue
    if (doc.get("Golang errors") or {}).get(""):
        skipped.append(label)          # package-level failure: nothing was scanned
        continue
    issues += doc.get("Issues", [])
for label in skipped:
    print("sast --report: %s did not compile for gosec; recorded as skipped, not zero."
          % label, file=sys.stderr)
by = Counter((x["rule_id"], x["severity"]) for x in issues)
with open(out, "w") as fh:
    json.dump({"total": len(issues),
               "complete": not skipped and not unreadable,
               "skipped_modules": skipped,
               "unreadable_scans": unreadable,
               "by_rule": [{"rule": r, "severity": s, "count": n}
                           for (r, s), n in by.most_common()]},
              fh, indent=2)
print("sast --report: %d total informational findings -> %s" % (len(issues), out))
if skipped or unreadable:
    print("sast --report: INCOMPLETE — %d module(s) skipped, %d scan(s) unreadable; "
          "the total above is a floor, not a count."
          % (len(skipped), len(unreadable)), file=sys.stderr)
PY
	exit 0
fi

# BLOCKING gate: run every module, collect findings in the blocking rule set.
#
# FAIL CLOSED, and say what was found. Both properties were absent until 2026-08-01
# and both were load-bearing:
#
#   * The scan used to be `$(run_gosec_block ... 2>/dev/null || true)` and the count
#     `except Exception: print(0)`. Between them, ANY failure of gosec — a module that
#     does not build, a renamed flag, a missing binary — produced empty output, parsed
#     as zero findings, and the gate printed "sast: clean". A security gate that could
#     not run reported that it had passed. Dropping -quiet (see run_gosec_block) is
#     what makes that state observable at all; this loop then refuses to certify it.
#   * The per-finding printer was a python -c inside single quotes whose f-string used
#     escaped double quotes, so python saw literal backslashes and died with
#     "SyntaxError: f-string expression part cannot include a backslash" on EVERY run —
#     swallowed by `2>/dev/null || true`. The gate could only ever say "1 SAST
#     finding(s)" and never which one, on CI where the tree is not at hand. Measured
#     2026-07-31: run 30666187624 spent a red `sast` job saying exactly that. The
#     printer now uses %-formatting (no backslashes) and its errors are not discarded.
#   * "clean" is now reported WITH ITS MEASURE (modules, files, lines). A silent
#     collapse from 313 scanned files to 1 used to read the same as a healthy scan.
#
# What blocks and what does not, from the same measurements:
#   - Golang errors keyed by "" are PACKAGE-level load/compile failures: 0 across all
#     twelve real modules, 1 in a deliberately broken one. Those abort the gate.
#   - Golang errors keyed by a FILE path are type-resolution noise that depends on the
#     build cache — core reported 180 on a cold cache and 0 once warm, while scanning
#     its 313 files and finding the G404 both times. Those are summarised, never fatal.
echo "sast: gosec blocking rules = ${INCLUDE}"
findings=0
total_files=0
total_lines=0
scan_dir() {
	local label="$1" dir="$2"
	shift 2
	local outfile errfile rc=0
	outfile="$(mktemp)"
	errfile="$(mktemp)"
	run_gosec_block "${dir}" "$@" >"${outfile}" 2>"${errfile}" || rc=$?
	local report prc=0
	report="$(python3 - "${outfile}" "${label}" <<'PY'
import json, sys

path, label = sys.argv[1], sys.argv[2]
try:
    with open(path) as fh:
        doc = json.load(fh)
except Exception as exc:                       # empty or malformed: gosec did not run
    print("sast: %s produced no readable gosec output (%s); refusing to call it clean."
          % (label, exc), file=sys.stderr)
    sys.exit(3)

errs = doc.get("Golang errors") or {}
pkg_errs = errs.get("", [])
if pkg_errs:
    print("sast: %s did not compile for gosec; the gate certifies nothing." % label,
          file=sys.stderr)
    for e in pkg_errs[:5]:
        print("    %s" % (e.get("error", "") or "").replace("\n", "\n    "), file=sys.stderr)
    sys.exit(3)

stats = doc.get("Stats") or {}
files, lines = stats.get("files", 0), stats.get("lines", 0)
if not files:
    print("sast: %s reported zero scanned files; refusing to call it clean." % label,
          file=sys.stderr)
    sys.exit(3)

issues = doc.get("Issues", [])
print("%d %d %d" % (len(issues), files, lines))
for x in issues:
    print("   [%s %s/%s] %s:%s %s" % (
        x.get("rule_id", "?"), x.get("severity", "?"), x.get("confidence", "?"),
        x.get("file", "?"), x.get("line", "?"), (x.get("details") or "")[:70]))
file_errs = sum(len(v) for k, v in errs.items() if k)
if file_errs:
    print("   (%d type-resolution note(s) from gosec; not blocking)" % file_errs)
PY
	)" || prc=$?
	if [ "${prc}" -ne 0 ]; then
		# gosec's own stderr is worth seeing when the module could not be scanned.
		[ -s "${errfile}" ] && sed 's/^/    /' "${errfile}" >&2
		rm -f "${outfile}" "${errfile}"
		exit 2
	fi
	rm -f "${outfile}" "${errfile}"
	local head="${report%%$'\n'*}"
	local n files lines
	read -r n files lines <<<"${head}"
	total_files=$((total_files + files))
	total_lines=$((total_lines + lines))
	if [ "${n}" -gt 0 ]; then
		echo "==> ${label}: ${n} SAST finding(s) (of ${files} files scanned)"
		printf '%s\n' "${report#*$'\n'}"
		findings=$((findings + n))
	fi
}
for m in "${MODULES[@]}"; do
	scan_dir "${m}" "${m}"
done
if [ -d cloud/control-plane ]; then
	scan_dir "cloud/control-plane" "cloud/control-plane"
	scanned_cloud=1
elif [ -f .olivares-public-export ]; then
	echo "sast: no cloud/control-plane (curated public export) — skipped."
	scanned_cloud=0
else
	echo "sast: cloud/control-plane is MISSING and this tree carries no public-export marker; refusing." >&2
	exit 2
fi

if [ "${findings}" -gt 0 ]; then
	echo ""
	echo "sast: ${findings} blocking SAST finding(s). Fix them, or if a finding is a"
	echo "reviewed false positive add '// #nosec Gxxx -- <reason>' on the flagged line"
	echo "(see docs/security/SECURITY-GATE.md#justifying-a-finding)." >&2
	exit 1
fi
echo "sast: clean — 0 blocking findings across $((${#MODULES[@]} + 1)) modules, ${total_files} files, ${total_lines} lines."
