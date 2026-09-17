#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation matrix for the cosign WIRING checker (cmd/olivares/tools/checkcosignwiring,
# invoked through scripts/check-cosign-wiring.sh) plus the launcher's own behaviour.
#
# WHAT THIS PROTECTS. An adversarial review found the execution-time invariant correct and
# simultaneously not load-bearing: it authenticated a binary while GoReleaser and the chart
# publisher re-resolved `cosign` from PATH. No battery in this repository could have caught
# that, because each one tested a script in isolation and nothing tested the seams.
#
# THE FIRST VERSION OF THIS FILE WAS ITSELF VACUOUS, which is why every case here is a
# MUTATION rather than an assertion about the current tree. The same review broke the grep
# version with two ordinary edits, and both left it reporting `11 passed, 0 failed`:
#
#   * every `cmd: bash` changed to `cmd: true` — the counts of `cmd:` fields and of launcher
#     strings were unchanged, and nothing could execute;
#   * the chart publisher rewritten as `"$OLIVARES_COSIGN_BIN" sign --yes …` — a genuine
#     bypass of the per-invocation re-hash, containing no `cosign` token at all.
#
# Both are rows below. A checker that cannot fail on them is decoration.
#
# NO `set -e` HERE, DELIBERATELY. This file REPORTS failures through check(); `set -e` turns
# a failing assertion into a silent STOP, so the run ends after the last success and looks
# like a clean tail — the exact failure mode these batteries exist to catch, and one that
# bit this repository three times on 2026-07-25.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-cosign-wiring.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

# ${TMPDIR:-/tmp} may be mounted noexec (the dev container's /tmp is): stubs then
# die at execve with EACCES — see test-assert-cosign-binary.sh for the measured
# signature. Probe; fall back to a repo-local (exec) tempdir.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$WORK/.execprobe"

pass=0
fail=0
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-58s %s\n' "$1" "$2"
	fi
}

# fixture <name> -> path to a throwaway copy of the wiring-relevant tree
fixture() {
	local d="$WORK/$1"
	mkdir -p "$d/.github/workflows"
	cp "$ROOT/.goreleaser.yaml" "$d/.goreleaser.yaml"
	cp "$ROOT"/.github/workflows/*.yml "$d/.github/workflows/" 2>/dev/null
	printf '%s' "$d"
}

run_checker() { COSIGN_WIRING_ROOT="$1" bash "$ROOT/scripts/check-cosign-wiring.sh" 2>&1; }

# expect_reject <name> <root> <diagnostic-substring>
expect_reject() {
	local name="$1" root="$2" want="$3" out rc
	out="$(run_checker "$root")"
	rc=$?
	if [ "$rc" -eq 0 ]; then
		check "$name" "MUTATION ACCEPTED — the checker is vacuous here" 1
		return
	fi
	if ! grep -qF -- "$want" <<<"$out"; then
		check "$name" "rejected for the WRONG reason" 1
		printf '        wanted a diagnostic containing: %s\n' "$want"
		printf '%s\n' "$out" | sed 's/^/          /'
		return
	fi
	check "$name" "rejected" 0
}

# chart_auth_findings <workflow> — structural chart-publish wiring (P08).
# Requires job contents:read, and a verified-binary GHCR login before package/push.
chart_auth_findings() {
	python3 - "$1" <<'PY'
import re, sys, yaml

path = sys.argv[1]
findings = []
try:
    with open(path, encoding="utf-8") as fh:
        doc = yaml.safe_load(fh)
except Exception as e:
    print(f"cannot parse {path}: {e}")
    sys.exit(2)

jobs = (doc or {}).get("jobs") or {}
job = jobs.get("publish-chart")
if not isinstance(job, dict):
    print("missing jobs.publish-chart")
    sys.exit(2)

perms = job.get("permissions")
if not isinstance(perms, dict):
    findings.append("job permissions map is missing")
    perms = {}
if perms.get("contents") != "read":
    findings.append("job permissions missing contents: read")
if perms.get("packages") != "write":
    findings.append("job permissions lost packages: write")
if perms.get("id-token") != "write":
    findings.append("job permissions lost id-token: write")

if job.get("runs-on") != "ubuntu-latest":
    findings.append("hosted-runner boundary lost (runs-on is not ubuntu-latest)")

steps = job.get("steps")
if not isinstance(steps, list):
    print("publish-chart has no steps")
    sys.exit(2)

def run_text(step):
    if not isinstance(step, dict):
        return ""
    run = step.get("run")
    return run if isinstance(run, str) else ""

def uses_text(step):
    if not isinstance(step, dict):
        return ""
    uses = step.get("uses")
    return uses if isinstance(uses, str) else ""

assert_idxs = []
login_idxs = []
package_idxs = []
push_idxs = []
for i, step in enumerate(steps):
    run = run_text(step)
    uses = uses_text(step)
    if "docker/login-action" in uses:
        findings.append("third-party registry login action is present")
    if "scripts/assert-cosign-binary.sh" in run:
        assert_idxs.append(i)
    if "helm package" in run:
        package_idxs.append(i)
    if re.search(r"\bhelm push\b", run):
        push_idxs.append(i)
    # Verified-binary login: the isolated path, the login subcommand, password-stdin.
    if (
        "OLIVARES_COSIGN_BIN" in run
        and re.search(r"\blogin\b", run)
        and "--password-stdin" in run
    ):
        login_idxs.append(i)
        if "${{" in run:
            findings.append("cosign login interpolates a GitHub expression into the shell")
        if re.search(r"\becho\b", run):
            findings.append("cosign login pipes the token with echo")
        if not re.search(r"\bprintf\b", run):
            findings.append("cosign login does not pipe the token with printf")
        env = step.get("env") if isinstance(step.get("env"), dict) else {}
        if "GHCR_TOKEN" not in env:
            findings.append("cosign login missing GHCR_TOKEN env")
        if "GHCR_USER" not in env:
            findings.append("cosign login missing GHCR_USER env")

if not assert_idxs:
    findings.append("job never runs scripts/assert-cosign-binary.sh")
if not login_idxs:
    findings.append("no verified-binary registry login")
if not package_idxs:
    findings.append("no helm package step")
if not push_idxs:
    findings.append("no helm push step")

if assert_idxs and login_idxs and assert_idxs[0] >= login_idxs[0]:
    findings.append("verified-binary login does not follow the binary assertion")
if login_idxs and package_idxs and login_idxs[0] >= package_idxs[0]:
    findings.append("verified-binary login does not precede package publication")
if login_idxs and push_idxs and login_idxs[0] >= push_idxs[0]:
    findings.append("verified-binary login does not precede helm push")

for line in findings:
    print(line)
sys.exit(1 if findings else 0)
PY
}

expect_chart_auth_ok() {
	local name="$1" path="$2" out rc
	out="$(chart_auth_findings "$path" 2>&1)"
	rc=$?
	if [ "$rc" -eq 0 ]; then
		check "$name" "accepted" 0
		return
	fi
	check "$name" "REJECTED" 1
	printf '%s\n' "$out" | sed 's/^/          /'
}

expect_chart_auth_reject() {
	local name="$1" path="$2" want="$3" out rc
	out="$(chart_auth_findings "$path" 2>&1)"
	rc=$?
	if [ "$rc" -eq 0 ]; then
		check "$name" "MUTATION ACCEPTED — the chart-auth check is vacuous here" 1
		return
	fi
	if [ "$rc" -eq 2 ]; then
		check "$name" "could not look" 1
		printf '%s\n' "$out" | sed 's/^/          /'
		return
	fi
	if ! grep -qF -- "$want" <<<"$out"; then
		check "$name" "rejected for the WRONG reason" 1
		printf '        wanted a diagnostic containing: %s\n' "$want"
		printf '%s\n' "$out" | sed 's/^/          /'
		return
	fi
	check "$name" "rejected" 0
}

echo "cosign wiring — the invariant must be load-bearing, not adjacent"

# --- the live tree must PASS ---------------------------------------------------------------
out_live="$(run_checker "$ROOT")"
rc_live=$?
if [ "$rc_live" -eq 0 ]; then
	check "the repository as committed is correctly wired" "accepted" 0
else
	check "the repository as committed is correctly wired" "REJECTED" 1
	printf '%s\n' "$out_live" | sed 's/^/          /'
fi

# --- MUTATION 1: cmd: bash -> cmd: true (the edit that defeated the grep version) ----------
d="$(fixture m1)"
sed -i 's/^    cmd: bash$/    cmd: true/' "$d/.goreleaser.yaml"
expect_reject "a signing cmd: that cannot run the launcher" "$d" 'it must be `bash`'

# --- MUTATION 2: the verified PATHNAME used directly (the other one) -----------------------
d="$(fixture m2)"
# Single-quoted so the mutation is VALID YAML — an invalid one would be rejected by the
# parser and would prove nothing about the rule under test.
sed -i "s|run: bash scripts/cosign-verified.sh sign --yes \"\${CHART_OCI_NAME}@\${DIGEST}\"|run: '\"\$OLIVARES_COSIGN_BIN\" sign --yes \"\${CHART_OCI_NAME}@\${DIGEST}\"'|" "$d/.github/workflows/release-chart.yml"
expect_reject "the verified pathname used WITHOUT the re-check" "$d" "Even the verified pathname is a bypass"

# --- MUTATION 3: plain old bare cosign -----------------------------------------------------
d="$(fixture m3)"
sed -i 's|bash scripts/cosign-verified.sh sign --yes|cosign sign --yes|' "$d/.github/workflows/release-chart.yml"
expect_reject "a bare cosign publisher in a workflow" "$d" "without the launcher"

# --- MUTATION 4: launcher dropped from args[0] ---------------------------------------------
d="$(fixture m4)"
perl -0pi -e 's/^      - scripts\/cosign-verified\.sh\n//m' "$d/.goreleaser.yaml"
expect_reject "a signing item whose args[0] is not the launcher" "$d" "want \`bash scripts/cosign-verified.sh\`"

# --- MUTATION 5: the conditional installer loses its matching assertion condition ----------
d="$(fixture m5)"
python3 - "$d/.github/workflows/release.yml" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p).read()
i = s.index("- name: assert the cosign binary is the reviewed artifact", s.index("mirror-dockerhub:"))
j = s.index("run: bash scripts/assert-cosign-binary.sh", i)
block = s[i:j].replace("        if: steps.cfg.outputs.enabled == 'true'\n", "")
open(p, "w").write(s[:i] + block + s[j:])
PYEOF
expect_reject "a conditional installer whose assertion is unconditional" "$d" "must share one predicate"

# --- MUTATION 6: a job installs cosign and never authenticates it --------------------------
d="$(fixture m6)"
python3 - "$d/.github/workflows/release-chart.yml" <<'PYEOF'
import re, sys
# A MUTATION THAT DOES NOT APPLY IS A HARNESS BUG, NOT A VACUOUS CHECKER. This one silently
# stopped matching when the assertion step gained `--isolate`, and the row then reported
# "MUTATION ACCEPTED — the checker is vacuous here", which would have sent the next reader
# hunting a defect that was not there. Assert the edit took effect.
p = sys.argv[1]
s = open(p).read()
s2 = re.sub(r"      - name: assert the cosign binary is the reviewed artifact\n(?:        [^\n]*\n)+", "", s, count=1)
if s2 == s:
    sys.exit("MUTATION DID NOT APPLY: the assertion step shape changed; update this fixture.")
open(p, "w").write(s2)
PYEOF
expect_reject "a job that installs cosign but never authenticates it" "$d" "never runs scripts/assert-cosign-binary.sh"

# --- chart publish-chart: checkout permission and verified-binary login before package -----
echo "chart release auth — checkout permission and verified-binary registry login"

expect_chart_auth_ok "the committed release-chart job is wired for checkout and registry login" \
	"$ROOT/.github/workflows/release-chart.yml"

d="$(fixture chart-auth-contents)"
python3 - "$d/.github/workflows/release-chart.yml" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p).read()
s2 = s.replace("      contents: read  # actions/checkout\n", "", 1)
if s2 == s:
    sys.exit("MUTATION DID NOT APPLY: contents: read line shape changed; update this fixture.")
open(p, "w").write(s2)
PYEOF
expect_chart_auth_reject "a publish-chart job without contents: read" "$d/.github/workflows/release-chart.yml" \
	"job permissions missing contents: read"

d="$(fixture chart-auth-nologin)"
python3 - "$d/.github/workflows/release-chart.yml" <<'PYEOF'
import re, sys
p = sys.argv[1]
s = open(p).read()
s2 = re.sub(
    r"      - name: cosign registry login \(ghcr\.io\)\n(?:        [^\n]*\n)+",
    "",
    s,
    count=1,
)
if s2 == s:
    sys.exit("MUTATION DID NOT APPLY: cosign registry login step shape changed; update this fixture.")
open(p, "w").write(s2)
PYEOF
expect_chart_auth_reject "a publish-chart job without verified-binary registry login" \
	"$d/.github/workflows/release-chart.yml" "no verified-binary registry login"

d="$(fixture chart-auth-order)"
python3 - "$d/.github/workflows/release-chart.yml" <<'PYEOF'
import re, sys
p = sys.argv[1]
s = open(p).read()
pat = re.compile(r"      - name: cosign registry login \(ghcr\.io\)\n(?:        [^\n]*\n)+")
m = pat.search(s)
if not m:
    sys.exit("MUTATION DID NOT APPLY: cosign registry login step not found; update this fixture.")
login = m.group(0)
s2 = s[: m.start()] + s[m.end() :]
needle = "      - name: cosign sign the OCI manifest (keyless OIDC, by digest)\n"
if needle not in s2:
    sys.exit("MUTATION DID NOT APPLY: sign step not found after removing login.")
if "helm push" not in s2.split(needle, 1)[0]:
    sys.exit("MUTATION DID NOT APPLY: helm push is not before the sign step.")
s3 = s2.replace(needle, login + needle, 1)
if s3 == s2:
    sys.exit("MUTATION DID NOT APPLY: could not place login after helm push.")
open(p, "w").write(s3)
PYEOF
expect_chart_auth_reject "verified-binary login after helm push" \
	"$d/.github/workflows/release-chart.yml" "verified-binary login does not precede helm push"

# --- the launcher's own behaviour ------------------------------------------------------------
# Hermetic: a private copy of scripts/ with a digest table naming a stub, so this runs on any
# machine and needs no real cosign.
mkdir -p "$WORK/repo/scripts" "$WORK/bin"
cp "$ROOT/scripts/assert-cosign-binary.sh" "$ROOT/scripts/cosign-verified.sh" "$WORK/repo/scripts/"
{
	echo '#!/usr/bin/env bash'
	echo 'echo "STUB_ARGS=$*"'
	echo "echo 'GitVersion:    v2.6.4'"
} >"$WORK/bin/cosign"
chmod 0755 "$WORK/bin/cosign"
stub_sha="$(sha256sum "$WORK/bin/cosign" | awk '{print $1}')"
python3 - "$WORK/repo/scripts/assert-cosign-binary.sh" "$stub_sha" <<'PYEOF'
import re, sys
path, digest = sys.argv[1:3]
s = open(path).read()
s = re.sub(r"(read -r -d '' APPROVED_DIGESTS <<'DIGESTS' \|\| true\n).*?(DIGESTS\n)",
           lambda m: m.group(1) + f"{digest}  cosign-linux-amd64\n" + m.group(2), s, flags=re.S)
open(path, "w").write(s)
PYEOF

out_ok="$(OLIVARES_COSIGN_BIN="$WORK/bin/cosign" bash "$WORK/repo/scripts/cosign-verified.sh" sign-blob --yes f 2>/dev/null)"
grep -q "STUB_ARGS=sign-blob --yes f" <<<"$out_ok"
check "the launcher execs the verified binary with argv intact" "args preserved" $?

out_unset="$(bash "$WORK/repo/scripts/cosign-verified.sh" version 2>&1)"
rc_unset=$?
[ "$rc_unset" -ne 0 ] && grep -q "OLIVARES_COSIGN_BIN is not set" <<<"$out_unset"
check "it refuses when the invariant did not run" "no PATH fallback" $?

OLIVARES_COSIGN_BIN="bin/cosign" bash "$WORK/repo/scripts/cosign-verified.sh" version >/dev/null 2>&1
[ $? -ne 0 ]
check "it refuses a relative OLIVARES_COSIGN_BIN" "absolute required" $?

# THE case the launcher exists for: the bytes at that path change AFTER the job-level
# assertion. A pathname is not an executable identity.
printf '#!/usr/bin/env bash\necho SUBSTITUTED\n' >"$WORK/bin/cosign"
chmod 0755 "$WORK/bin/cosign"
out_sub="$(OLIVARES_COSIGN_BIN="$WORK/bin/cosign" bash "$WORK/repo/scripts/cosign-verified.sh" sign-blob --yes f 2>&1)"
rc_sub=$?
[ "$rc_sub" -ne 0 ]
check "substituted bytes at the verified path are refused" "re-authenticated" $?
! grep -q "SUBSTITUTED" <<<"$out_sub"
check "and the substituted binary never ran" "not executed" $?

echo
echo "cosign wiring: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
