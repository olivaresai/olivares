#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-secrets.sh — gitleaks over the history, and it must be able to SAY WHOM IT ACCUSES.
#
# WHY THIS EXISTS. On 2026-08-04 the `secrets` job went red on PR #465 with exactly one line
# of evidence:
#
#     leaks found: 1
#
# No rule. No file. No commit. Two people then spent an afternoon on it, ran ten full-history
# scans between them, and every one came back clean — because both were scanning with `main`'s
# config while the job was not.
#
# THE ACTUAL CAUSE, measured by another lane and verified here by ancestry:
#   `.gitleaks.toml` is read BY RELATIVE PATH out of the checkout. Commit 2edf170f, which adds
#   the nine-line exception for a webhook fixture whose value decodes to the hexadecimal digits
#   in order, is on `main` and is NOT an ancestor of refs/pull/465/merge. So the job judged the
#   branch by the branch's own older rules. `git merge-base --is-ancestor 2edf170f
#   refs/pull/465/merge` -> false; the two configs differ by exactly those nine lines. Nothing
#   was wrong with the branch and nothing needed allowlisting: the red clears on rebase.
#
# THE HYPOTHESIS THIS FILE FIRST CARRIED WAS WRONG, and it is left on the record because the
# way it failed is the lesson. It said the finding was a leftover object on the self-hosted
# runner's persistent checkout, and offered a commit-count discrepancy as proof (the job's 2403
# against 1935 "reachable"). That 1935 was computed from 26ac5f3b — a head that had ALREADY
# been reported stale earlier the same day — and it mixed merge-inclusive and non-merge
# populations. A confident number over the wrong baseline reads exactly like evidence.
#
# So this wrapper exists to make the gate answer the questions it could not answer:
#   WHICH rule fired, in WHICH file, at WHICH commit — with the secret redacted;
#   IS THAT COMMIT PART OF WHAT YOU ARE MERGING, or something this clone merely carries; and
#   WHOSE RULES AM I APPLYING — because a gate that reads its configuration from the checkout
#   judges a branch by the rules that branch carries, and an exception added on the base branch
#   does not protect a branch that has not rebased. That red looks like a finding and is a lag.
#
# THE EXIT CODE OF gitleaks CANNOT TELL YOU WHETHER IT LOOKED. Measured, not assumed:
#
#   scan with no findings      -> rc=0, report file written, contents `[]`
#   scan with findings         -> rc=1, report file written, one object per finding
#   config file missing        -> rc=1, report file NOT WRITTEN
#
# A missing config and a real leak are THE SAME EXIT CODE. Grading on rc alone therefore
# reports "there is a secret" when the truth is "I could not look" — the same fail-open
# class as docs/SECURITY-HARDENING.md's three-answer rule. The discriminator used here is the REPORT FILE:
# a scan that ran leaves parseable JSON, a scan that never started leaves nothing.
#
# Exit 0 = clean: the scan ran and found nothing.
# Exit 1 = dirty: the scan ran and found something; every finding is named below, redacted.
# Exit 2 = COULD NOT LOOK: no git repo, no gitleaks, no config, or no parseable report.
#          This is NOT "clean" and must never be reported as such.
set -u
set -o pipefail

say() { printf '%s\n' "$*"; }
cannot_look() {
	say "check-secrets: COULD NOT LOOK — $1" >&2
	say "check-secrets: this is not a clean verdict. Fix the tooling and run again." >&2
	exit 2
}

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || cannot_look "not inside a git repository"
cd "$ROOT" || cannot_look "cannot enter the repository root '$ROOT'"

command -v gitleaks >/dev/null 2>&1 || cannot_look "gitleaks is not on PATH"
[ -f "$ROOT/.gitleaks.toml" ] || cannot_look "missing config $ROOT/.gitleaks.toml"
command -v python3 >/dev/null 2>&1 || cannot_look "python3 is not on PATH (needed to read the report)"

REPORT="$(mktemp -t gitleaks-report.XXXXXX.json)" || cannot_look "cannot create a temporary report file"
SCANLOG="$(mktemp -t gitleaks-scan.XXXXXX.log)" || cannot_look "cannot create a temporary scan log"
# Deliberately NOT `&& rm`: a trailing && suspends errexit for the whole group, which is how
# five verifications once hung off a command that always returned 0 (see the errexit lesson
# in sessions/). Cleanup goes in a trap, where it cannot change anybody's verdict.
cleanup() { rm -f "$REPORT" "$SCANLOG"; }
trap cleanup EXIT

# WHOSE RULES ARE THESE? Printed before the scan, on every run, because on 2026-08-04 this one
# line would have replaced an afternoon. gitleaks reads `.gitleaks.toml` out of the CHECKOUT,
# so a branch is judged by the rules the branch carries — and an exception that landed on the
# base branch does not exist for a branch that has not rebased. Same file name, different
# content, opposite verdict, and nothing in the output ever said so.
CFG="$ROOT/.gitleaks.toml"
cfghash="$( { sha256sum "$CFG" 2>/dev/null || shasum -a 256 "$CFG" 2>/dev/null; } | cut -c1-12)"
say "check-secrets: config .gitleaks.toml sha256=${cfghash:-?}"
# The base to compare against: an explicit override, then the CI-provided base branch, then the
# conventional one. An EXPLICIT request that does not resolve is NOT silently replaced by a
# fallback — someone asked to be judged against a named base, and quietly comparing against a
# different one answers a question nobody asked. Caught by case 8 of the battery.
base=""
if [ -n "${OLIVARES_SECRETS_BASE_REF:-}" ]; then
	if git rev-parse --verify --quiet "$OLIVARES_SECRETS_BASE_REF" >/dev/null 2>&1; then
		base="$OLIVARES_SECRETS_BASE_REF"
	else
		say "check-secrets: COULD NOT COMPARE — the requested base '$OLIVARES_SECRETS_BASE_REF' does not resolve."
		say "check-secrets:   Not falling back to another base: that would answer a different question."
		base="__unresolved__"
	fi
fi
if [ -z "$base" ]; then
	for cand in "${GITHUB_BASE_REF:+origin/$GITHUB_BASE_REF}" origin/main main; do
		[ -n "$cand" ] || continue
		if git rev-parse --verify --quiet "$cand" >/dev/null 2>&1; then base="$cand"; break; fi
	done
fi
if [ "$base" = "__unresolved__" ]; then
	: # already reported above
elif [ -z "$base" ]; then
	say "check-secrets: base ref not resolvable — COULD NOT COMPARE this config against a base."
elif ! git cat-file -e "$base:.gitleaks.toml" 2>/dev/null; then
	say "check-secrets: '$base' carries no .gitleaks.toml — nothing to compare against."
else
	basehash="$(git show "$base:.gitleaks.toml" 2>/dev/null | { sha256sum 2>/dev/null || shasum -a 256 2>/dev/null; } | cut -c1-12)"
	if [ -n "$basehash" ] && [ "$basehash" != "$cfghash" ]; then
		say "check-secrets: ⚠ this checkout's .gitleaks.toml DIFFERS from ${base}'s (${basehash})."
		say "check-secrets:   You are being judged by THIS branch's rules. An exception added on ${base}"
		say "check-secrets:   does not protect a branch that has not rebased — that red is a LAG, not a"
		say "check-secrets:   finding. Look before you touch anything:"
		say "check-secrets:     git diff ${base}:.gitleaks.toml -- .gitleaks.toml"
	fi
fi

# --redact is what makes the report safe to print: measured on gitleaks v8, BOTH the `Secret`
# and the `Match` field come back as the literal string REDACTED, so naming a finding never
# publishes it. -v is what makes gitleaks speak at all.
# ⛔ --log-opts=HEAD: se juzga LO QUE SE FUSIONA, no todo ref que haya en la máquina.
#
# Medido el 2026-08-16: sin esta opción `gitleaks detect` recorre la historia de TODOS los refs del
# clon. En el runner self-hosted, cuyo checkout PERSISTE entre jobs, eso significa que `main` era
# acusada por 12 hallazgos de un commit que sólo vive en la rama de otro PR — y el propio informe lo
# decía («NOT reachable from HEAD»)  mientras el gate fallaba igual. 834 de 6.284 commits escaneados
# no pertenecían al ref medido.
#
# Y esto NO afloja el gate, que es la parte que importa: un secreto en OTRA rama sigue cazándose
# cuando se gatea ESA rama, que es donde su autor puede rotarlo. Lo que se elimina es acusar a un
# carril de algo que no está en lo suyo — el modo de fallo que la columna «reachability» de más
# abajo existe para NOMBRAR y que hasta hoy no evitaba.
#
# Es también lo que este mismo fichero ya prescribía en su ayuda: «Do NOT allowlist it from this
# branch». Dos intentos de allowlist fallaron antes de leer esa línea; el segundo, además, eximía
# una `api_key` real (2 hallazgos → 0 en un señuelo de alta entropía).
#
# ⛔ Y CORREGIDO OTRA VEZ EL MISMO DÍA, con las dos correcciones medidas. `--log-opts=HEAD` arregló
# la acusación falsa **ciegando el gate**: dejaba de ver los refs ajenos, así que ya no acusaba… y
# tampoco informaba. La batería lo cazó en CI (caso `4 finding on another ref`), defendiendo una
# propiedad que yo había destruido sin verla: encontrar el hallazgo y **nombrarlo como no alcanzable,
# con el ref que lo lleva**. Un secreto olvidado en una rama muerta sigue siendo un secreto.
#
# Mi segundo intento fue recuperar el barrido completo y decidir el veredicto por alcanzabilidad. Lo
# MEDÍ antes de darlo por bueno, y por eso no está aquí: **345 s contra 239 s** en este árbol, y los
# hallazgos que añade son **9, todos de los buzones en `refs/remotes/origin/status*`**, que son la
# clase de falso positivo ya conocida. O sea: **+106 s y nueve avisos en CADA push de CADA carril,
# para siempre, sobre los que nadie va a actuar** — que es exactamente el antipatrón que esta casa
# tiene con nombre («un número que sólo AVISA es un número sobre el que nadie actúa»).
#
# Las dos preguntas nunca fueron la misma, y ahora tienen dos MODOS en vez de un compromiso:
#   · por defecto, `HEAD` — el gate de push juzga lo que el push introduce, rápido y callado;
#   · `--all-refs` (o `OLIVARES_SECRETS_SCOPE=all`) — el BARRIDO: recorre todo, nombra cada hallazgo
#     con su alcanzabilidad y su ref, y **no cobra** lo que no sea alcanzable desde HEAD.
# El barrido es para CI y para quien limpie el clon de un runner, no para el carril que empuja.
SCAN_SCOPE="${OLIVARES_SECRETS_SCOPE:-head}"
for _a in "$@"; do [ "$_a" = "--all-refs" ] && SCAN_SCOPE=all; done
scan_args=(--no-banner --redact --verbose --no-color -c "$ROOT/.gitleaks.toml" --report-format json --report-path "$REPORT")
[ "$SCAN_SCOPE" = "all" ] || scan_args+=(--log-opts="HEAD")
gitleaks detect "${scan_args[@]}" >"$SCANLOG" 2>&1
rc=$?

[ -f "$REPORT" ] || {
	say "check-secrets: gitleaks exited $rc and wrote NO report — it never scanned." >&2
	say "--- gitleaks output ---" >&2
	cat "$SCANLOG" >&2
	cannot_look "gitleaks produced no report (exit $rc); the exit code alone cannot distinguish this from a real finding"
}

# How much did it actually walk, and how much of that is yours? This one line is the whole
# diagnosis of the 2026-08-04 red, and it prints on EVERY run — clean or dirty — so the
# discrepancy is visible before it costs anyone an afternoon.
# The counts must compare LIKE WITH LIKE, and the first draft of this did not. gitleaks
# reports the commits it got a patch for, and `git log -p` produces no patch for a merge, so
# its number tracks the NON-MERGE population: measured 2026-08-04 on this repository,
# gitleaks said 2509 against `rev-list --all --no-merges` = 2513 and `rev-list --all` = 2815.
# Comparing its figure against a merge-inclusive count of HEAD mixes two populations and can
# invent a discrepancy on a perfectly hermetic clone. Both sides are --no-merges here.
scanned="$(sed -n 's/.*INF \([0-9][0-9]*\) commits scanned.*/\1/p' "$SCANLOG" | tail -1)"
mine="$(git rev-list --count --no-merges HEAD 2>/dev/null || echo '?')"
allrefs="$(git rev-list --count --no-merges --all 2>/dev/null || echo '?')"
say "check-secrets: gitleaks scanned ${scanned:-?} commit(s) · this ref carries ${mine} · every ref in this clone carries ${allrefs} (all non-merge)"
if [ -n "${scanned:-}" ] && [ "$mine" != "?" ] && [ "$scanned" -gt "$mine" ] 2>/dev/null; then
	say "check-secrets: NOTE — the scan reached $((scanned - mine)) commit(s) that are NOT part of this ref."
	say "check-secrets:        Some belong to other branches in this clone. On a self-hosted runner, whose"
	say "check-secrets:        actions/checkout clone PERSISTS BETWEEN JOBS, they can also be leftovers of"
	say "check-secrets:        work that is not in this repository at all — which is how a red 'secrets' can"
	say "check-secrets:        accuse a branch that never carried the finding. If a finding appears below,"
	say "check-secrets:        read its 'reachability' line before you touch anything."
fi

# ...AND HOW MUCH OF THE TREE. The line above answers "how many commits", which is only half
# the scope, and the missing half is the one that misleads: gitleaks also exempts whole PATHS
# (.gitleaks.toml [allowlist].paths), so a CLEAN verdict has always been clean ABOUT A SUBSET
# without ever saying which. Measured 2026-08-07: 3005 of 10090 tracked files are exempt —
# 29.8% of the tree — and it is provable by mutation rather than inferred from the config: the
# same literal token is caught in core/prod.go and NOT caught in sessions/*.md.
#
# The exemptions are not wrong. Their per-file justification is sound and removing them would
# redden thousands of decoy fixtures. What was wrong is a verdict that reads as though it
# covered everything. So the number is DERIVED from the allowlist on every run — never
# hand-written, so it cannot go stale the day somebody adds a pattern — and printed clean or
# dirty, like the commit line.
#
# It is fail-loud about ITSELF, which is the same rule the rest of this script lives by: if a
# pattern cannot be compiled here, the line says the count is PARTIAL instead of quietly
# reporting a smaller exempt set and making the coverage look better than it is.
scope="$(python3 - <<'PY' 2>/dev/null
import re, subprocess, sys
try:
    toml = open('.gitleaks.toml', encoding='utf-8').read()
except OSError:
    sys.exit(3)
m = re.search(r"^paths\s*=\s*\[(.*?)^\]", toml, re.S | re.M)
pats = re.findall(r"'''(.*?)'''", m.group(1), re.S) if m else []
if not pats:
    sys.exit(3)
comp, bad = [], 0
for pat in pats:
    try:
        comp.append(re.compile(pat))
    except re.error:
        bad += 1
files = subprocess.run(['git', 'ls-files'], capture_output=True, text=True).stdout.splitlines()
if not files:
    sys.exit(3)
exempt = sum(1 for f in files if any(c.search(f) for c in comp))
print(f"{len(files)}\t{exempt}\t{len(pats)}\t{bad}")
PY
)"
if [ -n "${scope:-}" ]; then
	IFS=$'\t' read -r s_total s_exempt s_pats s_bad <<EOF
$scope
EOF
	s_pct="$(awk -v e="$s_exempt" -v t="$s_total" 'BEGIN{ if (t>0) printf "%.1f", e*100/t; else printf "?" }')"
	say "check-secrets: path scope — ${s_pats} exemption pattern(s) put ${s_exempt} of ${s_total} tracked file(s) (${s_pct}%) OUT of scope; $((s_total - s_exempt)) scanned"
	if [ "${s_bad:-0}" -gt 0 ] 2>/dev/null; then
		say "check-secrets: NOTE — ${s_bad} exemption pattern(s) could not be evaluated here, so the"
		say "check-secrets:        out-of-scope count above is a LOWER BOUND, not the whole of it."
	fi
else
	# Not fatal: the scan itself is unaffected. But a missing scope line must not be mistaken
	# for "nothing is exempt", which is the exact misreading this whole block exists to stop.
	say "check-secrets: NOTE — could not compute the path scope; this verdict does not state how"
	say "check-secrets:        much of the tree the allowlist exempts. Do not read it as full coverage."
fi

findings="$(python3 - "$REPORT" <<'PY' 2>/dev/null
import json, sys
try:
    with open(sys.argv[1], encoding='utf-8') as fh:
        data = json.load(fh)
except Exception:
    sys.exit(3)
if not isinstance(data, list):
    sys.exit(3)
print(len(data))
for f in data:
    print('\t'.join(str(f.get(k, '')) for k in
        ('RuleID', 'File', 'StartLine', 'Commit', 'Author', 'Email', 'Date', 'Fingerprint')))
PY
)"
pyrc=$?
[ "$pyrc" -eq 0 ] || {
	say "--- gitleaks output ---" >&2
	cat "$SCANLOG" >&2
	cannot_look "the report at $REPORT is not a JSON array (gitleaks exit $rc)"
}

n="$(printf '%s\n' "$findings" | head -1)"
[ -n "$n" ] || cannot_look "could not read the finding count out of the report"

if [ "$n" -eq 0 ]; then
	if [ "$rc" -ne 0 ]; then
		say "--- gitleaks output ---" >&2
		cat "$SCANLOG" >&2
		cannot_look "gitleaks exited $rc but reported zero findings — the scan did not complete"
	fi
	say "check-secrets: CLEAN — the scan ran and found nothing."
	exit 0
fi

say ""
say "check-secrets: DIRTY — $n finding(s). Each one is named below; secrets are REDACTED."
say ""
reachable_n=0
unreachable_n=0
while IFS=$'\t' read -r _rule _file _line commit _rest; do
	[ -n "${commit:-}" ] || { reachable_n=$((reachable_n + 1)); continue; } # working tree = yours
	if ! git cat-file -e "${commit}^{commit}" 2>/dev/null; then
		unreachable_n=$((unreachable_n + 1))
	elif git merge-base --is-ancestor "$commit" HEAD 2>/dev/null; then
		reachable_n=$((reachable_n + 1))
	else
		unreachable_n=$((unreachable_n + 1))
	fi
done < <(printf '%s\n' "$findings" | tail -n +2)

printf '%s\n' "$findings" | tail -n +2 | while IFS=$'\t' read -r rule file line commit author email date fp; do
	# The column that stops the gate from accusing the wrong culprit: is this commit part of
	# what you are merging, or an object that only exists on this machine?
	if [ -z "$commit" ]; then
		where="WORKING TREE (uncommitted)"
	elif ! git cat-file -e "${commit}^{commit}" 2>/dev/null; then
		where="NOT IN THIS REPOSITORY (the scanner saw it, this clone does not have it)"
	elif git merge-base --is-ancestor "$commit" HEAD 2>/dev/null; then
		where="IN what you are merging (reachable from HEAD)"
	else
		refs="$(git for-each-ref --contains "$commit" --format='%(refname)' 2>/dev/null | head -3 | tr '\n' ' ')"
		where="NOT reachable from HEAD${refs:+ — carried by: $refs}"
	fi
	say "  rule       : ${rule}"
	say "  file       : ${file}:${line}"
	say "  commit     : ${commit:-<none>}  ${date:+($date)}"
	say "  author     : ${author}${email:+ <$email>}"
	say "  reachability: ${where}"
	say "  fingerprint: ${fp}"
	say ""
done

say "How to read this:"
say "  · 'IN what you are merging'  -> your change. Remove the secret and ROTATE it; an"
say "    allowlist entry is only for a fixture that is provably not a credential."
say "  · 'NOT reachable from HEAD'  -> the finding is not in what you are merging. On a"
say "    self-hosted runner that usually means a stale ref left by another branch. Do NOT"
say "    allowlist it from this branch: clean the runner's clone instead."
say "  · 'NOT IN THIS REPOSITORY'   -> you are reading a report produced somewhere else."
say ""
# This script SHIPS (it is in the export manifest) and docs/SECURITY-HARDENING.md does NOT — the curation blocks it
# at the export curation script, line 129. Printing that path unconditionally sends a public reader to a
# document their tree does not contain, so the pointer is chosen from what is actually present.
# The published tree gets SECURITY.md, which ships and carries the reporting route.
# export-closure: hub-only docs/SECURITY-HARDENING.md-SECURITY-AND-COMPLIANCE.md — the numbered design series is internal; the export ships SECURITY.md, LICENSING.md and the trust/ package instead.
if [ -f "$ROOT/docs/08-SECURITY-AND-COMPLIANCE.md" ]; then
	say "Policy and justification route: docs/08-SECURITY-AND-COMPLIANCE.md §7."
else
	say "Policy and justification route: SECURITY.md."
fi

# ⛔ EL CORTE, y por qué está aquí y no en el barrido. Medido el 2026-08-16: sin separar estas dos
# preguntas, `main` salía roja por 12 hallazgos de un commit que sólo vivía en la rama de OTRO PR —
# 834 de 6.284 commits escaneados no pertenecían al ref medido, porque el checkout del runner
# self-hosted PERSISTE entre jobs. El informe ya decía «NOT reachable from HEAD» y el gate fallaba
# igual: sabía la respuesta y no la usaba.
#
# Se cobra por lo alcanzable desde HEAD —lo que el push introduce y su autor puede rotar— y se
# INFORMA de todo lo demás con su ref. Es el mismo principio que el gate de identidad tiene escrito:
# un gate sólo debe cobrarte por lo que puedes arreglar. Y no afloja nada: ese mismo secreto se cobra
# cuando se gatea SU rama, que es donde vive quien puede rotarlo.
if [ "$reachable_n" -eq 0 ] && [ "$unreachable_n" -gt 0 ]; then
	say "check-secrets: ⚠ ${unreachable_n} hallazgo(s), NINGUNO alcanzable desde HEAD."
	say "  No se cobra a este push: no introduce ninguno. Pero NO son inexistentes — están arriba con"
	say "  el ref que los lleva, y siguen siendo secretos que alguien debe rotar y limpiar del clon."
	say "  Si aparecen en el runner compartido, se limpia SU clon; no se hace un allowlist desde aquí."
	exit 0
fi
exit 1
