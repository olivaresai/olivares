#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-check-secrets.sh — the regression battery for scripts/check-secrets.sh.
#
# It builds throwaway repositories in a temporary directory and asserts the answers the gate
# has to keep straight: clean, named finding, could-not-look, a finding outside the ref, the
# redaction canary, and — the actual 2026-08-04 cause — whose configuration it is applying. Nothing here touches this repository, and no case plants a
# string in it: the decoy is an obviously-fake private-key block written into a scratch repo
# that is deleted on exit.
#
# MUTATION RESULTS, MEASURED 2026-08-04 — written down as they came out, not as expected.
# A test is not done until you break the branch it guards and watch it go red FOR ITS OWN
# REASON, and a mutant that does not apply reports "all green", which reads as a pass.
#
#   M1  reachability column always answers "IN"        -> case 4 red, ALONE
#   M2  grade on gitleaks' exit code, not the report   -> cases 2 AND 4 red (both are the
#                                                         naming property; the gate stops
#                                                         naming anything at all)
#   M3  unparseable report reported as CLEAN           -> case 5 red, ALONE
#   M4  drop --redact                                  -> NOTHING goes red. Recorded because
#                                                         it is the honest result: this gate
#                                                         prints RuleID/File/Line/Commit/
#                                                         Author/Date/Fingerprint and never
#                                                         the Secret or Match field, so
#                                                         --redact alone is not load-bearing
#                                                         for the OUTPUT. It stays as defence
#                                                         in depth for the report FILE.
#   M5  drop --redact AND print the Secret field       -> case 6 red, ALONE. This is the real
#                                                         leak path, and case 6 is the canary
#                                                         that stands on it.
#   M6  remove the config-vs-base comparison entirely  -> cases 7 AND 8 red
#   M7  fall back silently when the requested base
#       does not resolve                               -> case 8 red, ALONE
#   M8  restore the `.*_test.go$` path exemption       -> case 9 red, ALONE
#       (A-04: the path used to hide a real private
#       key; the canary is an ephemeral RSA in
#       probe_test.go). A no-fire mutant of case 9
#       is restoring that path: the decoy is still
#       planted, the gate stays green, and that is
#       the defect.
#   M9  restore the `.*/testdata/.*` path exemption    -> case 10 red, ALONE
#
# So case 6 does not prove --redact works; it proves the gate never puts the secret in the
# log by either route. That distinction is the difference between a test and a decoration.
#
# Exit 0 = every case passed. Exit 1 = a case failed (named). Exit 2 = could not run the
# battery at all (no gitleaks, no git) — which is NOT a pass.
set -u

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does
# from every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Measured 2026-08-06;
# it left the branch of PR #526 pointing at a fixture commit. Fail closed: a missing
# sanitiser is "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "test-check-secrets: not inside a git repository — could not run" >&2
	exit 2
}
GATE="$ROOT/scripts/check-secrets.sh"
[ -x "$GATE" ] || [ -f "$GATE" ] || {
	echo "test-check-secrets: $GATE not found — could not run" >&2
	exit 2
}
command -v gitleaks >/dev/null 2>&1 || {
	echo "test-check-secrets: gitleaks is not on PATH — could not run the battery." >&2
	echo "test-check-secrets: this is NOT a pass. Install gitleaks (the CI secrets job does)." >&2
	exit 2
}

# The battery needs a scratch directory it can EXECUTE from: case 5 stands up a gitleaks
# shim, and a shim that cannot exec is a case that silently tests nothing. In this project's
# container /tmp is mounted `noexec` (measured 2026-08-04), so the obvious `mktemp -d -t`
# produces a directory where every execve dies EACCES — and case 5 then runs the REAL
# gitleaks and passes for the wrong reason.
#
# The selection used to live here as a private helper. It moved to lib/exec-workdir.sh on
# 2026-08-07, when scripts/test-check-hooks-path.sh turned out to need the same fact and a
# SECOND edge of it: on a noexec mount `test -x` answers false even when the bit is set, so
# the mount breaks permission-bit assertions as well as execve. Two consumers of one
# hard-won environment fact is exactly the case for one file — a fix applied to a copy is a
# fix the other copy does not get.
_olivares_exec_workdir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/exec-workdir.sh"
# shellcheck source=/dev/null
. "$_olivares_exec_workdir" || {
	echo "FATAL: cannot source $_olivares_exec_workdir" >&2
	exit 2
}
unset _olivares_exec_workdir
WORK="$(olivares_pick_exec_workdir check-secrets-tests)" || {
	echo "test-check-secrets: no scratch directory allows execve (tried RUNNER_TEMP, TMPDIR, /tmp, HOME, /workspace/.olivares-tmptest)." >&2
	echo "test-check-secrets: could not run the battery. This is NOT a pass." >&2
	exit 2
}
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

fails=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1" >&2
	printf '       %s\n' "$2" >&2
	fails=$((fails + 1))
}

# An obviously-fake key block, ASSEMBLED AT RUNTIME and never stored as a literal.
#
# The first version of this function wrote the header out in one piece, and the gate this very
# file tests then flagged it — correctly, by rule, file and line: a full-history scan does not
# care that the string lives in a test. Allowlisting the path was the easy fix and the wrong
# one; it would blind the scanner to this file forever. Splitting the marker means no committed
# line matches the rule, while the file WRITTEN by the function still contains the real header
# and still trips the detector, which is the whole point of a decoy.
plant_decoy() {
	local head="-----BEGIN RSA PRI""VATE KEY-----"
	local tail="-----END RSA PRI""VATE KEY-----"
	{
		echo "$head"
		echo "NOTAREALKEYnotarealkeyNOTAREALKEYnotarealkeyNOTAREALKEYnotarealkey"
		echo "$tail"
	} >"$1"
}

new_repo() {
	local d="$WORK/$1"
	mkdir -p "$d"
	git -C "$d" init -q -b main
	git -C "$d" config user.email 'battery@example.invalid'
	git -C "$d" config user.name 'battery'
	cp "$ROOT/.gitleaks.toml" "$d/.gitleaks.toml"
	echo "hello" >"$d/readme.md"
	git -C "$d" add -A
	git -C "$d" commit -qm "base"
	printf '%s' "$d"
}

run_gate() { # run_gate <dir> <outfile> ; echoes the exit code
	local d="$1" out="$2"
	(cd "$d" && bash "$GATE" >"$out" 2>&1)
	echo $?
}

echo "test-check-secrets: twelve cases"

# ── 1 · clean repository answers CLEAN, exit 0 ────────────────────────────────────────
d="$(new_repo clean)"
out="$WORK/1.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "0" ]; then
	fail "1 clean repo -> exit 0" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif ! grep -q "CLEAN" "$out"; then
	fail "1 clean repo says CLEAN" "exit 0 but the word CLEAN is not in the output"
else
	pass "1 clean repo -> exit 0 and says CLEAN"
fi

# ── 2 · a finding in the merged history is NAMED, not merely counted ──────────────────
d="$(new_repo dirty)"
plant_decoy "$d/deploy.pem"
git -C "$d" add -A
git -C "$d" commit -qm "adds the decoy"
sha="$(git -C "$d" rev-parse HEAD)"
out="$WORK/2.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "2 finding -> exit 1" "got exit $rc"
else
	missing=""
	grep -q "private-key" "$out" || missing="$missing rule"
	grep -q "deploy.pem" "$out" || missing="$missing file"
	grep -q "${sha:0:12}" "$out" || missing="$missing commit"
	grep -q "IN what you are merging" "$out" || missing="$missing reachability"
	if [ -n "$missing" ]; then
		fail "2 finding is NAMED" "the output never says:$missing — this is the '\''leaks found: 1'\'' defect"
	else
		pass "2 finding -> exit 1, and names rule + file + commit + reachability"
	fi
fi

# ── 3 · a missing config is COULD NOT LOOK (2), never a finding (1) ───────────────────
# This is the whole point of the wrapper: gitleaks answers 1 to BOTH, measured 2026-08-04.
d="$(new_repo noconfig)"
rm -f "$d/.gitleaks.toml"
out="$WORK/3.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" = "1" ]; then
	fail "3 missing config -> exit 2, not 1" "got exit 1: the gate is reporting 'could not look' as a finding"
elif [ "$rc" != "2" ]; then
	fail "3 missing config -> exit 2" "got exit $rc"
elif ! grep -q "COULD NOT LOOK" "$out"; then
	fail "3 missing config says COULD NOT LOOK" "exit 2 but the phrase is absent"
else
	pass "3 missing config -> exit 2 and says COULD NOT LOOK"
fi

# ── 4 · a finding NOT reachable from HEAD is called out as such ───────────────────────
# The 2026-08-04 case, reproduced: the object exists in the clone, on another ref, and is
# not part of what you would merge. A gate that cannot say this accuses the wrong branch.
d="$(new_repo stale-ref)"
git -C "$d" checkout -q -b abandoned
plant_decoy "$d/leftover.pem"
git -C "$d" add -A
git -C "$d" commit -qm "a branch nobody is merging"
git -C "$d" checkout -q main
out="$WORK/4.out"
# El caso ejercita el MODO BARRIDO (--all-refs), que es el que mira todos los refs. El gate de push
# va deliberadamente acotado a HEAD: medido el 2026-08-16, el barrido cuesta 345 s contra 239 s y
# añade 9 hallazgos de los buzones en refs/remotes/origin/status* — nueve avisos por push y por
# carril sobre los que nadie actuaría. Dos modos, no un compromiso.
rc="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$out")"
# ⛔ ESPERA 0, NO 1, DESDE EL 2026-08-16 — y el cambio es de VEREDICTO, no de vista. El hallazgo se
# sigue encontrando y se sigue NOMBRANDO con su ref (las dos comprobaciones de abajo no se tocan);
# lo que cambia es que no se COBRA a un push que no lo introduce. Medido ese día: el checkout del
# runner self-hosted persiste entre jobs, así que `main` salia roja por 12 hallazgos de la rama de
# otro PR — 834 de 6.284 commits escaneados no pertenecian al ref. El informe ya decia "NOT
# reachable from HEAD" mientras el gate fallaba igual: sabia la respuesta y no la usaba.
if [ "$rc" != "0" ]; then
	fail "4 finding on another ref -> exit 0" "got exit $rc (no lo introduce este push: se informa, no se cobra)"
elif ! grep -q "NOT reachable from HEAD" "$out"; then
	fail "4 names it as unreachable" "the gate found it but never says it is outside what you are merging"
elif ! grep -q "abandoned" "$out"; then
	fail "4 names the ref carrying it" "it says unreachable but does not say which ref carries the commit"
else
	pass "4 finding on another ref -> exit 0, named as NOT reachable, with the ref"
fi

# ── 5 · an unreadable report is COULD NOT LOOK, never CLEAN ───────────────────────────
# Simulated with a gitleaks shim that exits 0 and writes garbage where the report goes: the
# fail-open shape this gate exists to forbid.
d="$(new_repo badreport)"
shim="$WORK/shim"
mkdir -p "$shim"
cat >"$shim/gitleaks" <<'SHIM'
#!/usr/bin/env bash
# Writes something that is NOT a JSON array to the --report-path, then claims success.
prev=""
for a in "$@"; do
  if [ "$prev" = "--report-path" ]; then printf 'not json at all' > "$a"; fi
  prev="$a"
done
echo "INF 1 commits scanned."
exit 0
SHIM
chmod +x "$shim/gitleaks"
out="$WORK/5.out"
rc="$( (cd "$d" && PATH="$shim:$PATH" bash "$GATE" >"$out" 2>&1); echo $?)"
if [ "$rc" = "0" ]; then
	fail "5 unreadable report -> exit 2, not 0" "got exit 0: the gate called an unparseable report CLEAN"
elif [ "$rc" != "2" ]; then
	fail "5 unreadable report -> exit 2" "got exit $rc"
else
	pass "5 unreadable report -> exit 2 (could not look), not clean"
fi

# ── 6 · the printed finding never carries the secret ──────────────────────────────────
# --redact is what makes naming a finding safe. If it is dropped, the gate publishes in the
# CI log exactly the thing it is defending. The decoy body is the canary.
d="$(new_repo redaction)"
plant_decoy "$d/creds.pem"
git -C "$d" add -A
git -C "$d" commit -qm "decoy for the redaction check"
out="$WORK/6.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "6 redaction case finds the decoy" "got exit $rc, expected 1"
elif grep -q "NOTAREALKEYnotarealkey" "$out"; then
	fail "6 output is redacted" "the decoy's body appears in the output — --redact is not in effect"
else
	pass "6 the named finding is redacted (the secret never reaches the log)"
fi

# ── 7 · the branch's config differs from the base's, and the gate SAYS SO ─────────────
# THE ACTUAL 2026-08-04 CAUSE. gitleaks reads .gitleaks.toml out of the checkout, so a branch
# that has not rebased is judged by its own older rules: an exception added on the base does
# not exist for it. Ten clean scans were run against the base's config while the job used the
# branch's, and nothing in any output ever mentioned the config. This case is that line.
d="$(new_repo config-lag)"
git -C "$d" checkout -q -b base-branch
printf '\n[[allowlist.regexes]]\n# added on the base only\nregex = "does-not-matter"\n' >>"$d/.gitleaks.toml"
git -C "$d" add -A
git -C "$d" commit -qm "base gains an exception the branch never sees"
git -C "$d" checkout -q main
out="$WORK/7.out"
rc="$( (cd "$d" && OLIVARES_SECRETS_BASE_REF=base-branch bash "$GATE" >"$out" 2>&1); echo $?)"
if ! grep -q "DIFFERS from base-branch" "$out"; then
	fail "7 says the config differs from the base" "the gate never mentions that it is applying a different .gitleaks.toml than the base (exit $rc)"
elif ! grep -q "sha256=" "$out"; then
	fail "7 identifies the config it used" "no config hash in the output"
else
	pass "7 config lag -> the gate names it, so a lag is not read as a finding"
fi

# ── 8 · no base to compare against is COULD NOT COMPARE, never silence ────────────────
d="$(new_repo no-base)"
out="$WORK/8.out"
rc="$( (cd "$d" && OLIVARES_SECRETS_BASE_REF=refs/heads/definitely-not-here bash "$GATE" >"$out" 2>&1); echo $?)"
if ! grep -q "COULD NOT COMPARE" "$out"; then
	fail "8 unresolvable base is stated" "the gate was asked for a base that does not exist and said nothing (exit $rc)"
elif grep -q "DIFFERS from" "$out"; then
	fail "8 does not fall back silently" "it answered about a DIFFERENT base than the one requested"
else
	pass "8 unresolvable base -> says COULD NOT COMPARE, and does not silently pick another"
fi

# ── 9 · A-04: a private key in *_test.go is a FINDING, not a path exemption ───────────
# 2026-08-06 licencias-sweep-2 A-04: `.*_test.go$` exempted the entire detector.
# The same ephemeral RSA was findings=0 in a test file and findings=1 in a
# production path. The canary plants the decoy under the name that used to hide
# it. A mutant that restores the path exemption makes THIS case go green.
d="$(new_repo a04-testgo)"
plant_decoy "$d/probe_test.go"
git -C "$d" add -A
git -C "$d" commit -qm "ephemeral RSA under a _test.go name"
out="$WORK/9.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "9 private key in _test.go -> exit 1" "got exit $rc — the path still exempts the detector (A-04)"
elif ! grep -q "probe_test.go" "$out"; then
	fail "9 names the _test.go file" "exit 1 but probe_test.go is not in the output"
else
	pass "9 private key in _test.go -> exit 1 (path no longer exempts the detector)"
fi

# ── 10 · A-04: a private key in testdata/ is a FINDING, not a path exemption ──────────
d="$(new_repo a04-testdata)"
mkdir -p "$d/testdata"
plant_decoy "$d/testdata/probe.pem"
git -C "$d" add -A
git -C "$d" commit -qm "ephemeral RSA under testdata/"
out="$WORK/10.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "10 private key in testdata/ -> exit 1" "got exit $rc — testdata still exempts the detector (A-04)"
elif ! grep -q "testdata/probe.pem" "$out"; then
	fail "10 names the testdata file" "exit 1 but testdata/probe.pem is not in the output"
else
	pass "10 private key in testdata/ -> exit 1 (path no longer exempts the detector)"
fi

# ── 11 · a credential in a TRANSLATED README is a FINDING ────────────────────
# 2026-08-29. Both exist because a class-shaped allowlist — path
# `README\.[a-z]{2}\.md` plus the shape of a German compound — was written for
# this gate and RETIRED before landing when a contrast measured it instead of
# reading it.
#
# ⛔ WHICH OF THE TWO ACTUALLY WITNESSES AGAINST THAT CLASS, measured by putting
# the retired entry back: case 12 goes RED, case 11 stays GREEN. Case 11 is NOT
# a witness here and its name must not claim otherwise. The reason is worth
# keeping: in the mode this gate runs — over history — `condition = "AND"`
# composes correctly, so an `api_key = "..."` whose match does not have the
# `API <Compound>` shape fails the AND and is reported anyway. The class entry's
# third defect (in `--no-git`/`dir`, `paths` acts as a PRE-FILTER and the AND
# never applies, so that credential vanishes) is real but belongs to a mode this
# gate does not run, so it cannot be pinned from here. Case 11 stays as a plain
# regression guard: a credential in a translated README must be reported.
#
# The decoy values are ASSEMBLED AT RUNTIME for the reason plant_decoy already
# documents: a literal here would be a committed line matching the rule, and a
# full-history scan does not care that the string lives in a test.
d="$(new_repo readme-cred)"
{
	echo "# Doc"
	printf '%s%s = "%s%s"\n' 'api' '_key' 'V7q2Hs8Nx4Rt6Yw1' 'Bz5Kd9Mg3Pf0Lc'
} >"$d/README.es.md"
git -C "$d" add -A && git -C "$d" commit -qm "translated readme with a credential"
out="$WORK/11.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "11 credential in README.es.md -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif ! grep -q "README.es.md" "$out"; then
	fail "11 names README.es.md" "exit 1 but README.es.md is not in the output"
else
	pass "11 credential in a translated README -> exit 1 (regression guard; case 12 is the class witness)"
fi

# ── 12 · a namespaced token after the API keyword is a FINDING ───────────────
# The exact shape the retired class regex could not tell apart from prose:
# there is no reliable syntactic boundary between "long compound word" and
# "token with a namespace and a hyphen", which is why the exception that
# shipped is the EXACT historical one and not a class.
d="$(new_repo readme-nstoken)"
{
	echo "# Doc"
	printf 'Service access via %s, %s%s as the identifier.\n' 'API' 'PROD-Zk93Qv7Lm2' 'XpR8dTn4Wb'
} >"$d/README.fr.md"
git -C "$d" add -A && git -C "$d" commit -qm "translated readme with a namespaced token"
out="$WORK/12.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "12 namespaced token in README.fr.md -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif ! grep -q "README.fr.md" "$out"; then
	fail "12 names README.fr.md" "exit 1 but README.fr.md is not in the output"
else
	pass "12 API-adjacent namespaced token -> exit 1 (the class regex would have hidden it)"
fi

echo ""
if [ "$fails" -eq 0 ]; then
	echo "test-check-secrets: OK — 12/12"
	exit 0
fi
echo "test-check-secrets: $fails case(s) failed" >&2
exit 1
