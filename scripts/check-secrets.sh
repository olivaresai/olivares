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
# THE ACTUAL CAUSE, measured by another contributor and verified here by ancestry:
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
#   Git read failed mid-scan   -> rc=0, report file written, contents `[]`, and the log says
#                                 `ERR [git] fatal: …` + `ERR error="stderr is not empty"`
#
# A missing config and a real leak are THE SAME EXIT CODE. Grading on rc alone therefore
# reports "there is a secret" when the truth is "I could not look" — the same fail-open
# class as docs/SECURITY-HARDENING.md's three-answer rule. And the report file alone is not enough either
# (SG5 F2): a scan that never started leaves no parseable JSON, but a scan that could not
# read its history leaves `[]`. So a scan counts as done only with a parseable report, no
# ERR/FTL/PNC line in the scanner's log, and — for the all-ref sweep — a captured subject
# that still answers its census afterwards.
#
# Exit 0 = clean: the scan ran and found nothing.
# Exit 1 = dirty: the scan ran and found something; every finding is named below, redacted.
# Exit 2 = COULD NOT LOOK: no git repo, no gitleaks, no config, or no parseable report.
#          This is NOT "clean" and must never be reported as such.
set -u
set -o pipefail

# ⛔ THE SCANNER FOLLOWS GIT_DIR, SO THE GATE ISOLATES BEFORE ITS FIRST GIT CALL (SG7, 2026-09-11).
# Measured on disposable repositories: with a foreign GIT_DIR exported — which git itself does for
# hooks run from a linked worktree — this gate scanned THAT repository's history and named its
# finding, in both scopes, while judging the checkout it was run from. After the shared sanitiser
# the repository is found by discovery from the working directory, which is the checkout that
# invoked the gate; the snapshot and its children set their own GIT_DIR explicitly afterwards.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

# WHERE THIS SCRIPT'S OWN PIECES LIVE, resolved BEFORE the `cd` below. The gate reads its
# RULES out of the checkout it is judging — that is the 2026-08-04 lesson and it stays — but
# its own helper is part of the tool, not of the tree under test: a throwaway repository
# built by the battery has no `scripts/` at all, and resolving the helper against the scanned
# root looked for it there and called a real finding COULD NOT LOOK.
SELFDIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)"

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
# The attribution phase gets three more scratch files, created only if there is anything to
# attribute. They are declared here, empty, so the trap can name them under `set -u` without
# the clean path paying for temporary files it never writes.
RAW=""
BODY=""
ATTRIB=""
ATTRIBLOG=""
SNAP=""
cleanup() {
	rm -f "$REPORT" "$SCANLOG" ${RAW:+"$RAW"} ${BODY:+"$BODY"} ${ATTRIB:+"$ATTRIB"} ${ATTRIBLOG:+"$ATTRIBLOG"}
	[ -z "$SNAP" ] || rm -rf -- "$SNAP"
}
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
#   · por defecto, `HEAD` — per-commit admission: full history reachable from HEAD
#     (pre-push and the CI secrets job). Leftover refs are not observed.
#   · `--all-refs` (o `OLIVARES_SECRETS_SCOPE=all`) — explicit whole-repository audit:
#     recorre todo, nombra cada hallazgo con su alcanzabilidad y su ref, y **no cobra**
#     lo que no sea alcanzable desde HEAD.
# The all-ref sweep remains available; it is not the per-commit CI contract.
SCAN_SCOPE="${OLIVARES_SECRETS_SCOPE:-head}"
for _a in "$@"; do [ "$_a" = "--all-refs" ] && SCAN_SCOPE=all; done

# ⛔ SCHEDULER PARALLELISM OF THE ONE ALL-REF DETECT (R89-C1, 2026-09-12). After the narrowed
# extraction the sweep's remaining cost is gitleaks rule matching, and a Go binary with GOMAXPROCS
# unset schedules on every CPU it sees — on a shared runner host, every job asks for all of them
# (an internal design note (not shipped)). So the sweep defaults to 4 and keeps a positive value
# the caller already set. This is SCHEDULER parallelism, not reserved CPU: 4 cannot create cores that
# another job owns. What is scanned, the rules, the attribution and the 0/1/2 do not change.
# A malformed explicit value is refused before any work, like an explicit base that does not resolve
# is never replaced: the Go runtime would ignore it and use every CPU, and substituting 4 would apply a
# limit nobody asked for. HEAD scope (pre-push) gets no default, no check and no new line.
gomaxprocs_origin=""
if [ "$SCAN_SCOPE" = "all" ]; then
	_gmp_positive='^0*[1-9][0-9]{0,8}$'
	if [ -z "${GOMAXPROCS:-}" ]; then
		export GOMAXPROCS=4
		gomaxprocs_origin="all-ref default"
	elif [[ "$GOMAXPROCS" =~ $_gmp_positive ]]; then
		gomaxprocs_origin="caller"
	else
		cannot_look "GOMAXPROCS='$(printf '%s' "$GOMAXPROCS" | tr -c '[:alnum:]._+-' '?' | cut -c1-32)' is not a positive integer; Go would ignore it and schedule the sweep on every visible CPU. Unset it (all-ref default 4) or set a positive integer."
	fi
	unset _gmp_positive
fi
# Named before detect so a log that dies inside gitleaks still says which admission
# contract was requested. HEAD observation is not all-ref coverage.
if [ "$SCAN_SCOPE" = "all" ]; then
	say "check-secrets: admission scope=all — whole-repository audit; charges only HEAD-reachable findings."
else
	say "check-secrets: admission scope=head — current HEAD history; refs HEAD does not reach are not observed."
fi
scan_args=(--no-banner --redact --verbose --no-color -c "$ROOT/.gitleaks.toml" --report-format json --report-path "$REPORT")

# ⛔ ONE CAPTURED SUBJECT FOR THE SWEEP (SG4, 2026-09-11). SG3 reproduced it: the guard below
# proved a claim about the refs, config and diff.renames it read, and gitleaks then re-read
# `--all`, the config and diff.renames for itself — a ref landing in between was scanned under
# an exemption nobody proved, and a HEAD-reachable finding came out CLEAN. A digest compared
# before and after would not close it (A→B→A restores the digest after B was read). So the
# input is captured ONCE into an owned metadata snapshot that shares the live objects read-only
# (scripts/secrets-scan-snapshot.py), and the guard, the scanner, the counts and the attribution
# all read THAT, with system/global config switched off because the snapshot carries the
# effective copy. What was not captured is not qualified, and the lines below name the subject.
snap_env=()
snap_tips=""
if [ "$SCAN_SCOPE" = "all" ]; then
	snap_info=""
	if SNAP="$(mktemp -d -t check-secrets-subject.XXXXXX 2>/dev/null)" &&
		snap_info="$(python3 "${SELFDIR:-.}/secrets-scan-snapshot.py" "$SNAP" 2>"$SNAP/capture.err")"; then
		snap_head="" snap_refs="" snap_wt="" snap_owr="" snap_refsha="" snap_cfgsha="" snap_renames=""
		while IFS='=' read -r _k _v; do
			case "$_k" in
			head) snap_head="$_v" ;; refs) snap_refs="$_v" ;; worktree_heads) snap_wt="$_v" ;; other_worktree_ref_tips) snap_owr="$_v" ;;
			refs_sha256) snap_refsha="$_v" ;; config_sha256) snap_cfgsha="$_v" ;; diff_renames) snap_renames="$_v" ;;
			esac
		done <<EOF
$snap_info
EOF
		snap_tips="$(tr '\n' ' ' <"$SNAP/tips")"
		snap_tips="${snap_tips% }"
		snap_env=(GIT_DIR="$SNAP" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null)
		say "check-secrets: captured subject — HEAD ${snap_head:0:12} · ${snap_refs} ref(s) + ${snap_wt} worktree HEAD(s) + ${snap_owr:-0} other-worktree ref tip(s) (refs sha256=${snap_refsha:0:12}) · config sha256=${snap_cfgsha:0:12} · diff.renames=${snap_renames}"
	else
		_why="$(tail -1 "${SNAP:-/nonexistent}/capture.err" 2>/dev/null)"
		[ -z "$SNAP" ] || rm -rf -- "$SNAP"
		SNAP=""
		say "check-secrets: NO CAPTURED SUBJECT — ${_why:-the snapshot could not be created}."
		# Not the live store instead: measured, a grafts file sent that sweep to 0 commits and CLEAN.
		cannot_look "the all-ref sweep has no captured subject to scan"
	fi
fi

# ⛔ NARROWED EXTRACTION OF THE SWEEP (SG2, 2026-09-11). Measured on the all-ref history: ~93 % of
# the Git CPU of `git log -p` goes to diffing the append-only mailboxes under an internal design note (not shipped),
# 14-23 MiB each, and gitleaks throws every one of those fragments away BEFORE any rule, because the
# global allowlist `an internal design note (not shipped)*\.md$` exempts them (detect.go checkCommitOrPathAllowed). So the sweep
# may ask Git not to diff exactly `an internal design note (not shipped)**/*.md` — and ONLY when the guard below
# proves that removes nothing the rules would have seen. Otherwise it is the full extraction, with
# the same 0/1/2. Measured counterexamples the guard exists for (an internal design note (not shipped)
# secrets-exemption-equivalence-20260911):
#   · an exempt mailbox that is ADDED can steal the rename source of a NON-exempt file: without the
#     mailbox the file pairs with that source and loses added lines — a finding disappears;
#   · a mailbox renamed OUT to a scanned path arrives as a full add (extra material, other lines);
#   · a line feed in a mailbox name is excluded by the glob but NOT exempted by Go's `.`.
# Nothing wider than that glob, never another extension or an internal design note (not shipped) as a whole.
NARROW_GLOB='sessions/status/inbox/**/*.md'
narrow_touch=""
narrow_risky=""
if [ "$SCAN_SCOPE" = "all" ]; then
	if [ "${OLIVARES_SECRETS_NARROW_EXTRACTION:-1}" = "0" ]; then
		say "check-secrets: full extraction — narrowing disabled by OLIVARES_SECRETS_NARROW_EXTRACTION=0."
	elif [ -z "$SNAP" ]; then
		say "check-secrets: full extraction — no captured subject to prove the narrowing on."
	else
		narrow_decision="$(env -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG_COUNT "${snap_env[@]}" \
			python3 - "$NARROW_GLOB" "$SNAP/gitleaks.toml" "$SNAP/tips" <<'PY' 2>/dev/null
import re, subprocess, sys


def answer(*words):
    print(*words)
    sys.exit(0)


try:
    import tomllib
    cfg = tomllib.load(open(sys.argv[2], 'rb'))       # the captured bytes the scanner reads
    TIPS = open(sys.argv[3]).read().split()          # captured worktree HEADs live --all adds
except Exception:
    answer('fallback', 'this config cannot be read here, so the mailbox exemption is not proven')
GLOB = sys.argv[1]
PIN = r'sessions/.*\.md$'
GOEXEMPT = re.compile(r'sessions/.*\.md\Z')   # Go: '.' never matches LF, '$' is end of text
lists = [a for a in (cfg.get('allowlists') or []) if isinstance(a, dict)]
if isinstance(cfg.get('allowlist'), dict):
    lists.append(cfg['allowlist'])
if not any(not a.get('targetRules') and str(a.get('condition', '')).upper() in ('', 'OR', '||')
           and PIN in (a.get('paths') or []) for a in lists):
    answer('fallback', 'no unconditional global allowlist in this config carries sessions/.*\\.md$')
# The pairing argument below counts only ADDED and DELETED files as rename candidates. With copy
# detection a MODIFIED mailbox becomes a copy source too, which that argument does not cover.
mode = subprocess.run(['git', 'config', '--get', 'diff.renames'], capture_output=True)
if mode.returncode not in (0, 1) or (mode.returncode == 0 and mode.stdout.strip().lower() not in
                                     (b'', b'true', b'yes', b'on', b'1', b'false', b'no', b'off', b'0')):
    answer('fallback', 'diff.renames is not a plain on/off here, so copy detection is not covered by the guard')


def git(*args):
    p = subprocess.run(['git', *args], capture_output=True)
    if p.returncode != 0:
        answer('fallback', 'git could not answer the narrowing guard (%s)' % args[0])
    return p.stdout


def entries(raw):
    """-z --name-status -> [(letter, src, dst)]; positions, never a separator guess."""
    toks = raw.split(b'\0'); out = []; i = 0
    while i < len(toks):
        st = toks[i][1:] if toks[i].startswith(b'\n') else toks[i]
        if not re.fullmatch(rb'[ACDMRTUXB][0-9]{0,3}', st):
            i += 1
            continue
        if st[:1] in (b'R', b'C'):
            out.append((st[:1], toks[i + 1], toks[i + 2])); i += 3
        else:
            out.append((st[:1], toks[i + 1], toks[i + 1])); i += 2
    return out


def exempt(p):
    return bool(GOEXEMPT.search(p.decode('utf-8', 'surrogateescape')))


def in_glob(p):
    return p.startswith(b'sessions/status/inbox/') and p.endswith(b'.md')


listing = git('log', '--all', *TIPS, '--full-history', '--no-renames', '--format=%x01%H %P', '--name-status', '-z',
              '--', ':(glob)' + GLOB)
touched = 0
added_or_deleted = []
for rec in listing.split(b'\x01')[1:]:
    head, _, rest = rec.partition(b'\0')
    ids = head.split()
    touched += 1
    found = entries(rest)
    for letter, _, path in found:
        if not exempt(path):
            answer('fallback', 'a path under the mailbox glob is not exempt by sessions/.*\\.md$ (for example a line feed in its name)')
    if len(ids) <= 2 and any(letter in (b'A', b'D') for letter, _, _ in found):
        added_or_deleted.append(ids[0].decode())
risky = 0
for sha in added_or_deleted:
    found = entries(git('diff-tree', '--no-renames', '-r', '--root', '--name-status', '-z', sha))
    if not (any(l == b'A' and not in_glob(p) for l, _, p in found) and any(l == b'D' for l, _, _ in found)):
        continue   # no non-exempt add to pair, or no source to pair it with: rename detection cannot move
    risky += 1
    if risky > 500:
        answer('fallback', 'more than 500 commits would need a rename-pairing check')
    kept = lambda raw: sorted((l, s, d) for l, s, d in entries(raw) if l != b'D' and not exempt(d))
    full = kept(git('log', '--no-walk', '--format=', '--name-status', '-z', sha))
    narrow = kept(git('log', '--no-walk', '--format=', '--name-status', '-z', sha, '--', '.', ':(exclude,glob)' + GLOB))
    if full != narrow:
        answer('fallback', 'commit %s: skipping the mailbox changes the rename pairing of a non-exempt path' % sha[:12])
answer('engage', touched, risky)
PY
)"
		read -r narrow_verdict narrow_a narrow_b <<EOF
$narrow_decision
EOF
		if [ "${narrow_verdict:-}" = "engage" ] && [ -n "${narrow_a:-}" ] && [ -n "${narrow_b:-}" ]; then
			narrow_touch="$narrow_a"
			narrow_risky="$narrow_b"
			say "check-secrets: extraction narrowed — Git will not diff ${NARROW_GLOB} (exempt before any rule; guard passed)."
		else
			say "check-secrets: full extraction — ${narrow_decision#fallback }"
		fi
	fi
fi
if [ "$SCAN_SCOPE" != "all" ]; then
	scan_args+=(--log-opts="HEAD")
elif [ -n "$SNAP" ]; then
	# One argv value; gitleaks splits it on spaces and restores none of its empty-opts defaults,
	# so all three are written back here, unchanged. The captured worktree HEADs follow `--all`
	# because the snapshot has no worktrees for `--all` to find, and the scanner reads the
	# captured config bytes from inside the snapshot, never the checkout's file again.
	all_opts="--full-history --all --diff-filter=tuxdb${snap_tips:+ $snap_tips}"
	[ -z "$narrow_touch" ] || all_opts="$all_opts -- . :(exclude,glob)${NARROW_GLOB}"
	scan_args=(--source "$SNAP" --no-banner --redact --verbose --no-color -c "$SNAP/gitleaks.toml"
		--report-format json --report-path "$REPORT" --log-opts="$all_opts")
fi
if [ "$SCAN_SCOPE" = "all" ]; then
	# Printed right before the silence of the detect, so a log that ends in a timeout still says
	# which parallelism the scanner was given and who chose it.
	say "check-secrets: GOMAXPROCS=${GOMAXPROCS:-unset} (${gomaxprocs_origin}) for gitleaks detect — Go scheduler parallelism, not reserved CPU."
fi
env -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG_COUNT "${snap_env[@]}" gitleaks detect "${scan_args[@]}" >"$SCANLOG" 2>&1
rc=$?

[ -f "$REPORT" ] || {
	say "check-secrets: gitleaks exited $rc and wrote NO report — it never scanned." >&2
	say "--- gitleaks output ---" >&2
	cat "$SCANLOG" >&2
	cannot_look "gitleaks produced no report (exit $rc); the exit code alone cannot distinguish this from a real finding"
}

# ⛔ A PARSEABLE `[]` IS NOT A SCAN THAT LOOKED (SG5 F2, 2026-09-11). Measured with the pinned
# scanner and --no-color: objects of a captured ref pruned mid-scan gave rc=0, report `[]` and
# `ERR [git] fatal: bad object …` / `ERR error="stderr is not empty"` — and this gate said CLEAN
# over a planted key. A missing blob or tree does the same while every commit still counts.
# Matched on the level token alone (fail-closed if the wording changes), and only Git's own
# error lines are echoed, cut short — never a finding or scanned content.
scan_errors="$(grep -cE '^[^ ]+ (ERR|FTL|PNC) ' "$SCANLOG")"
[ "$?" -le 1 ] || cannot_look "could not read the scanner log to check it for read errors"
if [ "${scan_errors:-0}" -gt 0 ] 2>/dev/null; then
	grep -E '^[^ ]+ (ERR|FTL|PNC) \[git\]' "$SCANLOG" | head -3 | cut -c1-160 >&2
	cannot_look "the scanner logged ${scan_errors} error line(s) while reading history (gitleaks exit $rc); zero findings after a failed read is not clean"
fi

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
mine="$(env -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG_COUNT "${snap_env[@]}" git rev-list --count --no-merges HEAD 2>/dev/null || echo '?')"
# shellcheck disable=SC2086 # the captured worktree HEADs are object names, one word each
allrefs="$(env -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG_COUNT "${snap_env[@]}" git rev-list --count --no-merges --all $snap_tips 2>/dev/null || echo '?')"
say "check-secrets: gitleaks scanned ${scanned:-?} commit(s) · this ref carries ${mine} · every ref in this clone carries ${allrefs} (all non-merge)"
if [ -n "$SNAP" ]; then
	# The captured subject must still answer for itself after the scan: a commit object that left
	# the store turns this census into `?`, which used to be printed and passed over (SG5 F2).
	# An unborn HEAD is a state, not a failure. Blobs and trees are the scanner-log check's job.
	[ "$allrefs" != "?" ] || cannot_look "the captured refs no longer answer their census after the scan — an object they name is gone"
	[ "$mine" != "?" ] || [ "$snap_head" = "unborn" ] || cannot_look "the captured HEAD no longer answers its census after the scan"
	say "check-secrets: this verdict is about the captured subject (HEAD ${snap_head:0:12}, refs sha256=${snap_refsha:0:12});"
	say "check-secrets:   a ref, commit or config that arrived after the capture is NOT qualified by it."
fi
if [ -n "$narrow_touch" ]; then
	# The scanner counts commits it received a fragment for. With the mailboxes not diffed, a commit
	# whose ONLY changes are mailboxes sends none, so that number is no longer the census. Say so.
	say "check-secrets: NOTE — extraction was narrowed: Git did not diff ${NARROW_GLOB}; ${narrow_touch} commit(s)"
	say "check-secrets:        touch those paths. They are exempt before any rule by this config's global"
	say "check-secrets:        allowlist sessions/.*\\.md\$, and the rename pairing of every non-exempt path"
	say "check-secrets:        was checked unchanged (${narrow_risky} commit(s) needed that check). 'scanned' above"
	say "check-secrets:        counts commits that still carry a NON-EXEMPT fragment — it is NOT the non-merge"
	say "check-secrets:        census: commits whose only changes are those mailboxes are not in it."
fi
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
scope="$(CHECK_SECRETS_CFG="${SNAP:+$SNAP/gitleaks.toml}" python3 - <<'PY' 2>/dev/null
import os, re, subprocess, sys
try:
    toml = open(os.environ.get('CHECK_SECRETS_CFG') or '.gitleaks.toml', encoding='utf-8').read()
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
	if [ -n "$SNAP" ]; then
		# Patterns come from the captured config; the file list is the LIVE checkout's `git ls-files`.
		say "check-secrets: path scope — LIVE checkout inventory against the captured patterns (not the captured history):"
		say "check-secrets:   ${s_pats} exemption pattern(s) put ${s_exempt} of ${s_total} tracked file(s) (${s_pct}%) OUT of scope; $((s_total - s_exempt)) in scope"
	else
		say "check-secrets: path scope — ${s_pats} exemption pattern(s) put ${s_exempt} of ${s_total} tracked file(s) (${s_pct}%) OUT of scope; $((s_total - s_exempt)) scanned"
	fi
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

# ⛔ NINGÚN BYTE SEPARA LOS CAMPOS: LOS SEPARA LA POSICIÓN. Y ESTO CORRIGE DOS FALLOS MEDIDOS.
#
# 1) El primero, del 2026-09-09 por la mañana: las filas iban unidas por TABULADOR y se leían con
#    `IFS=$'\t' read`, que COLAPSA tabuladores consecutivos porque el tabulador es whitespace de
#    IFS. Una fila sin Commit se leía como cuatro campos y la huella aterrizaba en `commit`.
#
# 2) El segundo lo devolvió root esa misma tarde, y es peor porque el primer arreglo lo introdujo:
#    se cambió el tabulador por US (0x1f) «porque no colapsa»… y **una ruta de Git VÁLIDA puede
#    contener 0x1f**. Contraejemplo exacto de root, reproducido y sellado en
#    `framing-correction/red/`: un solo hallazgo EN HEAD con `File = odd<US>path.txt` desplazaba
#    los campos, `commit` recibía el StartLine (`1`), y el gate respondía **0 / NOT IN THIS
#    REPOSITORY** sobre un hallazgo que SÍ está en HEAD. Un separador dentro del dato no es un
#    separador: es una colisión esperando a que alguien nombre un fichero.
#
# LA FORMA CORRECTA NO ELIGE UN BYTE MÁS RARO: elimina la elección. Cada campo se ESCAPA de forma
# reversible y se emite **en su propia línea**, ocho líneas por hallazgo. Como el escape no deja
# ningún LF dentro de un valor, «una línea = un campo» es exacto; un campo vacío es una línea
# vacía, y el límite de registro es la posición (8) validada contra el recuento declarado. La
# lectura usa `IFS= read -r`, que **no parte por nada**: no hay separador que colisionar.
#
# EL ESCAPE, reversible y mínimo (la tabla idéntica vive en `secrets-report-attribution.py`, que
# es quien lo deshace; el banco las cruza de punta a punta):
#
#     \  -> \\        TAB -> \t      LF -> \n      CR -> \r
#     cualquier otro byte de control (<0x20) y DEL (0x7f) -> \xNN
#     TODO LO DEMÁS SE QUEDA COMO ESTÁ — incluido UTF-8
#
# Por eso la salida de un hallazgo normal es **idéntica byte a byte** a la de antes: sólo cambia
# lo que no podía imprimirse sin mentir. Y una ruta que lleve un LF ya no puede fabricar una línea
# que parezca otro hallazgo o un veredicto: sale como `\n` dentro de su propia línea de campo.
#
# TIPOS: los siete campos de texto han de ser cadenas y `StartLine` cadena o entero. Un `Commit`
# que sea un objeto JSON no se convierte en texto con `str()` —eso daba **exit 0** con un veredicto
# inventado, medido en el caso R8 del reproductor—: es **COULD NOT LOOK (2)**.
#
# Cabecera: línea 1 el recuento, línea 2 cuántos campos necesitaron escape (0 en el caso normal).
# ⛔ LA SALIDA VA A UN FICHERO, NO A `$(...)`, Y NO ES ESTILO. La sustitución de comandos
# ELIMINA TODOS LOS SALTOS DE LÍNEA FINALES, así que un último campo vacío —una huella vacía, el
# caso corriente— desaparecía y el registro llegaba con SIETE líneas en vez de ocho. Medido: con
# `$(...)` los diez casos del reproductor salían 2. Un fichero conserva el byte final.
RAW="$(mktemp -t gitleaks-fields.XXXXXX.txt)" || cannot_look "cannot create a temporary field file"
python3 - "$REPORT" >"$RAW" 2>/dev/null <<'PY'
import json, sys

TEXT = ('RuleID', 'File', 'Commit', 'Author', 'Email', 'Date', 'Fingerprint')
ORDER = ('RuleID', 'File', 'StartLine', 'Commit', 'Author', 'Email', 'Date', 'Fingerprint')
SHORT = {'\\': '\\\\', '\t': '\\t', '\n': '\\n', '\r': '\\r'}


def escape(value):
    """Reversible, and a no-op for everything that can be printed as itself."""
    out = []
    for ch in value:
        if ch in SHORT:
            out.append(SHORT[ch])
        elif ch < ' ' or ch == '\x7f':
            out.append('\\x%02x' % ord(ch))
        else:
            out.append(ch)
    return ''.join(out)


try:
    with open(sys.argv[1], encoding='utf-8') as fh:
        data = json.load(fh)
except Exception:
    sys.exit(3)
if not isinstance(data, list):
    sys.exit(3)
rows = []
escaped = 0
for f in data:
    if not isinstance(f, dict):
        sys.exit(4)
    row = []
    for key in ORDER:
        v = f.get(key, '')
        if key in TEXT:
            if not isinstance(v, str):
                sys.exit(4)
        elif isinstance(v, bool) or not isinstance(v, (str, int)):
            sys.exit(4)   # StartLine: a number or a string, never a bool or a structure
        raw = v if isinstance(v, str) else str(v)
        cooked = escape(raw)
        if cooked != raw:
            escaped += 1
        row.append(cooked)
    rows.append(row)
print(len(rows))
print(escaped)
for row in rows:
    for field in row:
        print(field)
PY
pyrc=$?
if [ "$pyrc" -eq 4 ]; then
	# A field the report is not allowed to carry — a Commit that is an object, a StartLine that
	# is a list. `str()` used to turn those into text and the gate then graded the text: measured,
	# a Commit of `{"oid": "<sha>"}` came out as exit 0 with a verdict nobody could defend.
	say "--- gitleaks output ---" >&2
	cat "$SCANLOG" >&2
	cannot_look "the report at $REPORT carries a field of an unsupported type (gitleaks exit $rc)"
fi
[ "$pyrc" -eq 0 ] || {
	say "--- gitleaks output ---" >&2
	cat "$SCANLOG" >&2
	cannot_look "the report at $REPORT is not a JSON array (gitleaks exit $rc)"
}

n="$(sed -n '1p' "$RAW")"
[ -n "$n" ] || cannot_look "could not read the finding count out of the report"
case "$n" in ''|*[!0-9]*) cannot_look "the report reader gave an unreadable finding count" ;; esac

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
# ⛔ LA ATRIBUCIÓN SE CALCULA UNA VEZ, ANTES DEL BUCLE, Y AQUÍ ESTÁ MEDIDO POR QUÉ.
#
# Hasta el 2026-09-09 este tramo hacía dos cosas que crecen con el número de hallazgos, y las
# dos se pagaban enteras en el barrido de CI:
#
#   · clasificaba CADA hallazgo DOS VECES — una en el bucle de conteo y otra en el de
#     impresión— haciendo las mismas preguntas a git sobre el mismo grafo congelado;
#   · por cada hallazgo NO alcanzable ejecutaba `git for-each-ref --contains <commit>`, que
#     recorre la historia UNA VEZ POR REF del clon. Medido en esta caja sobre el commit que
#     el propio log del job nombra: **22 558 ms con 11 834 refs**, contra 24 ms del
#     `merge-base --is-ancestor` y 5 ms del `cat-file -e`. El coste no estaba en mirar: estaba
#     en preguntar N veces lo mismo.
#
# El job `102304614840` (run `34299976570`) lo pagó: el escáner CERRÓ a las 03:03:45.373Z y el
# TERM llegó a las 03:03:57.526Z. Lo que corría en medio era este bucle, a ~4,06 s por hallazgo
# sobre doce hallazgos, con 11,88 s de presupuesto. El barrido terminó y el gate murió MUDO
# nombrando lo que había encontrado — `2 = NO PUDE MIRAR`, sin veredicto.
#
# Lo que NO cambia, que es la parte que sostiene el gate: los cuatro estados son los mismos
# cuatro, decididos en el mismo ORDEN (sin commit → árbol de trabajo; el objeto no está →
# ausente; HEAD lo alcanza → tuyo; si no, sus tres primeros portadores), el corte de más abajo
# recibe los mismos conteos, se conservan TODOS los hallazgos y su orden de salida, y las refs
# nombradas son EXACTAMENTE las tres primeras que devolvía `for-each-ref --contains`, en su
# mismo orden. El equivalente lo comprueba `scripts/test-secrets-report-attribution.py`
# hallazgo a hallazgo contra el método anterior, que corre como oráculo.
#
# Y falla CERRADO, con dos guardas de recuento que son independientes: el cuerpo ha de traer
# OCHO líneas por hallazgo y la atribución DOS. Si el ayudante no está, no responde, o cualquiera
# de los dos recuentos no cuadra, esto es `COULD NOT LOOK` (2). "No pude atribuirlo" nunca es
# "está limpio". La segunda guarda es la que caza un marco roto: medido con el escape retirado,
# un LF en una ruta descuadra las líneas y la corrida sale 2 en vez de dar un veredicto torcido.
ATTRIBUTOR="${SELFDIR:-.}/secrets-report-attribution.py"
[ -f "$ATTRIBUTOR" ] || cannot_look "missing $ATTRIBUTOR — the findings cannot be attributed to commits"
BODY="$(mktemp -t gitleaks-fields.XXXXXX.txt)" || cannot_look "cannot create a temporary findings file"
ATTRIB="$(mktemp -t gitleaks-attrib.XXXXXX.txt)" || cannot_look "cannot create a temporary attribution file"
ATTRIBLOG="$(mktemp -t gitleaks-attrib.XXXXXX.log)" || cannot_look "cannot create a temporary attribution log"
escaped_n="$(sed -n '2p' "$RAW")"
tail -n +3 "$RAW" >"$BODY"
body_lines="$(wc -l <"$BODY" 2>/dev/null | tr -d ' ')"
[ "${body_lines:-0}" -eq $((n * 8)) ] 2>/dev/null ||
	cannot_look "the report reader emitted ${body_lines:-no} field line(s) for $n finding(s) — eight per finding is the framing"
if [ "${escaped_n:-0}" -gt 0 ] 2>/dev/null; then
	# Said ONCE, and only when something was actually escaped, so the normal report is untouched.
	say "check-secrets: NOTE — ${escaped_n} field(s) carry control characters; they are printed"
	say "check-secrets:        escaped (\\t \\n \\r \\xNN, and \\\\ for a literal backslash) so that a"
	say "check-secrets:        path can never forge a second finding or a verdict line."
	say ""
fi
# Only the Commit column crosses this boundary. Not the rule, not the path, not the author,
# and never anything the scanner read: the attributor cannot put in a log, a message or a
# file what it was never given. Field 4 of each eight-line record, taken by POSITION — there is
# no delimiter to be fooled by, which is the whole point of the framing above.
awk 'NR % 8 == 4' "$BODY" | env -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG_COUNT "${snap_env[@]}" python3 "$ATTRIBUTOR" >"$ATTRIB" 2>"$ATTRIBLOG"
arc=$?
if [ "$arc" -ne 0 ]; then
	say "--- attribution output ---" >&2
	cat "$ATTRIBLOG" >&2
	cannot_look "could not attribute the $n finding(s) to their commits (exit $arc)"
fi
attributed="$(wc -l <"$ATTRIB" 2>/dev/null | tr -d ' ')"
[ "${attributed:-0}" -eq $((n * 2)) ] 2>/dev/null ||
	cannot_look "the attribution answered ${attributed:-no} line(s) for $n finding(s) — two per finding is the contract"

reachable_n=0
unreachable_n=0
# ONE pass does the counting and the printing. `IFS= read -r` splits by NOTHING: each field is
# already on its own line, so there is no delimiter inside the data to collide with and no IFS
# rule to collapse an empty field. The attribution arrives on its own descriptor, two lines per
# finding, for the same reason — its `refs` ends in a space on purpose.
truncated() { cannot_look "the report reader's record framing is short — a field line is missing"; }
exec 9<"$ATTRIB" || cannot_look "cannot read back the attribution"
while IFS= read -r rule; do
	IFS= read -r file   || truncated
	IFS= read -r line   || truncated
	IFS= read -r commit || truncated
	IFS= read -r author || truncated
	IFS= read -r email  || truncated
	IFS= read -r date   || truncated
	IFS= read -r fp     || truncated
	IFS= read -r state <&9 ||
		cannot_look "the attribution ran out of answers before the findings did"
	IFS= read -r refs <&9 ||
		cannot_look "the attribution gave a state with no carrier line"
	# The column that stops the gate from accusing the wrong culprit: is this commit part of
	# what you are merging, or an object that only exists on this machine?
	case "$state" in
	worktree)
		where="WORKING TREE (uncommitted)"
		reachable_n=$((reachable_n + 1)) ;;                 # uncommitted = yours
	absent)
		where="NOT IN THIS REPOSITORY (the scanner saw it, this clone does not have it)"
		unreachable_n=$((unreachable_n + 1)) ;;
	head)
		where="IN what you are merging (reachable from HEAD)"
		reachable_n=$((reachable_n + 1)) ;;
	other)
		# A commit this clone carries that HEAD does not reach. If no ref carries it either,
		# it stays in this state WITHOUT a carrier: an invented ref would accuse a branch.
		where="NOT reachable from HEAD${refs:+ — carried by: $refs}"
		unreachable_n=$((unreachable_n + 1)) ;;
	*)
		cannot_look "the attribution answered an unknown state '$state' for a finding" ;;
	esac
	say "  rule       : ${rule}"
	say "  file       : ${file}:${line}"
	say "  commit     : ${commit:-<none>}  ${date:+($date)}"
	say "  author     : ${author}${email:+ <$email>}"
	say "  reachability: ${where}"
	say "  fingerprint: ${fp}"
	say ""
done <"$BODY"
exec 9<&-

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
# export-closure: hub-only docs/08-SECURITY-AND-COMPLIANCE.md — the numbered design series is internal; the export ships SECURITY.md, LICENSING.md and the trust/ package instead.
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
