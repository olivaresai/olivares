#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-overlay-candidate-facts.sh — the overlay facts of ONE EXACT CANDIDATE, read from
# immutable objects. 0 the facts hold · 1 finding · 2 COULD NOT LOOK.
#
#   check-overlay-candidate-facts.sh --enterprise-repo DIR --enterprise-commit E40 \
#                                    --community-repo DIR --community-commit C40 [--acta PATH]
#
# OGL2 release-boundary decision 1, «Candidate facts and main observation»: the exact-candidate
# evaluator reads the six private blobs and the paired Community slug map from immutable objects
# and SHARES ONE evaluator with the live-main reader. That evaluator is
# `scripts/lib/overlay-facts.py`; `check-overlay-live-facts.sh` calls the same file for the sealed
# main. This adapter adds no predicate of its own.
#
# ⛔ WHAT THIS ADAPTER NEVER DOES
#   · resolve a name. E and C are 40-hex COMMIT object ids: a branch, a tag object, a tree or an
#     abbreviation is 2, because a name can move between this reading and the next.
#   · read a working tree or an index. Blobs come from `git --no-replace-objects` objects, so a
#     dirty checkout of either repository cannot move the verdict.
#   · consume the LT1 seal or the measurement Module. The main reader keeps its same-act sealed
#     capture; a seal minted for main is not authority over any candidate.
#   · execute the candidate. Its scripts, Taskfiles, hooks and parsers are bytes inside objects.
#     The evaluator, the AST reader and the default acta come from THIS script's own tree.
#
# ⛔ WHAT THE RESULT IS NOT: a qualification, an admission or a release permit. It is a source
# fact observation about (E, C). E's commit/tree, E's `public` gitlink and C's commit/tree are
# recorded as distinct identities. When the gitlink is not C the pair is a
# `development-source-observation`, and `release_qualified` is false on EVERY output — the equal
# pair included, because qualifying a pair belongs to the authenticated producer, not to this.
#
# Output: one JSON document (schema overlay-candidate-facts/v1) on stdout; diagnostics on stderr.
# ⛔ `go run` COLAPSA EL CODIGO DE SALIDA, as in the live reader: the AST reader is built, then run.

set -euo pipefail
NAME=check-overlay-candidate-facts

# Git selectors first: `GIT_DIR` outranks `-C`, and this adapter reads two object stores.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	printf '%s: COULD NOT LOOK — cannot source %s (git-env isolation)\n' "$NAME" "$_olivares_git_env" >&2
	exit 2
}
unset _olivares_git_env

SELF="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"
FACTS="$SELF/lib/overlay-facts.py"
AST_SRC="$SELF/overlay-ast"
ACTA="$SELF/../design/overlay-live-facts.json"
E_REPO="" E_COMMIT="" C_REPO="" C_COMMIT=""
_bin_dir="" _err="" _ident=""
# shellcheck disable=SC2317 # invoked through the EXIT trap
cleanup() {
	[ -z "$_bin_dir" ] || rm -rf -- "$_bin_dir"
	[ -z "$_err" ] || rm -f -- "$_err"
	return 0
}
trap cleanup EXIT

# One JSON document on every exit, the refusals before the evaluator included: a consumer never
# has to tell "no output" from "no facts". Identities are copied from the evaluator's record,
# never from the request, so an object that was not established stays null.
emit() { # <rc> <reason-if-no-evaluator-detail>
	local rc="$1" reason="${2:-}" erc=0
	if ! command -v python3 >/dev/null 2>&1; then
		printf '%s: COULD NOT LOOK — %s (python3 is not on PATH: no result document)\n' "$NAME" "${reason:-}" >&2
		exit 2
	fi
	python3 - "$rc" "$reason" "${_err:-}" "${_ident:-}" "$FACTS" "$AST_SRC" "$ACTA" \
		"$E_REPO" "$E_COMMIT" "$C_REPO" "$C_COMMIT" <<'PY' || erc=$?
import datetime, hashlib, io, json, os, sys

(rc, reason, err_path, ident_path, facts_path, ast_src, acta,
 e_repo, e_commit, c_repo, c_commit) = sys.argv[1:12]
rc = int(rc)
name = "check-overlay-candidate-facts"


def digest(path):
    try:
        with open(path, "rb") as fh:
            return hashlib.sha256(fh.read()).hexdigest()
    except OSError:
        return None


lines = []
if err_path and os.path.exists(err_path):
    lines = [ln.strip() for ln in io.open(err_path, encoding="utf-8", errors="replace") if ln.strip()]
detail = (lines[-1] if lines else reason)[:400]

ident = None
if ident_path and os.path.exists(ident_path):
    try:
        ident = json.load(io.open(ident_path, encoding="utf-8"))
    except ValueError as exc:
        rc, detail = 2, "the evaluator's identity record is not readable JSON (%s)" % exc
if ident is not None and ident.get("exit") != rc:
    detail = "the evaluator exited %s and its identity record says %r" % (rc, ident.get("exit"))
    rc = 2
if ident is None and rc != 2:
    detail, rc = "the evaluator exited %s without an identity record" % rc, 2
ident = ident or {}

ent = ident.get("enterprise") or {}
com = ident.get("community") or {}
gitlink, community = ent.get("public_gitlink"), com.get("commit")
if gitlink and community:
    relation = "equal" if gitlink == community else "different"
    classification = ("exact-gitlink-source-observation" if relation == "equal"
                      else "development-source-observation")
else:
    relation = classification = "unobserved"

doc = {
    "schema": "overlay-candidate-facts/v1",
    "subject": "candidate-source-facts",
    "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "result": {
        "exit": rc,
        "meaning": {0: "facts-hold", 1: "finding", 2: "could-not-look"}[rc],
        "detail": detail,
    },
    "requested": {
        "enterprise_repository": e_repo, "enterprise_commit": e_commit,
        "community_repository": c_repo, "community_commit": c_commit,
    },
    "enterprise": {
        "commit": ent.get("commit"), "tree": ent.get("tree"), "public_gitlink": gitlink,
    },
    "community": {"commit": community, "tree": com.get("tree")},
    "pair": {"relation": relation, "classification": classification, "release_qualified": False},
    "authority": {
        "qualification": False, "admission": False, "release_permit": False,
        "statement": "source fact observation only; not a qualification, admission or release result",
    },
    "facts": ident.get("facts") if rc == 0 else None,
    "evaluator": {
        "overlay_facts_sha256": digest(facts_path),
        "overlay_ast_schema": "overlay-ast/v1",
        "overlay_ast_main_go_sha256": digest(os.path.join(ast_src, "main.go")),
        "overlay_ast_go_mod_sha256": digest(os.path.join(ast_src, "go.mod")),
        "acta": {"path": acta, "sha256": digest(acta)},
    },
}
print(json.dumps(doc, indent=2, sort_keys=True))

short = lambda v: (v or "unobserved")[:9]
where = "E %s (tree %s, public gitlink %s) · C %s (tree %s) · pair %s" % (
    short(ent.get("commit")), short(ent.get("tree")), short(gitlink),
    short(community), short(com.get("tree")), classification)
verdict = {0: "FACTS HOLD", 1: "FINDING", 2: "COULD NOT LOOK"}[rc]
print("%s: %s — %s%s" % (name, verdict, where, "" if rc == 0 else " — " + detail), file=sys.stderr)
print("%s: source facts only — NOT a qualification, admission or release result" % name, file=sys.stderr)
raise SystemExit(rc)
PY
	case "$erc" in
	0 | 1 | 2) exit "$erc" ;;
	esac
	printf '%s: COULD NOT LOOK — the result document could not be written (exit %s)\n' "$NAME" "$erc" >&2
	exit 2
}

cannot() { emit 2 "$*"; }

while [ $# -gt 0 ]; do
	case "$1" in
	--enterprise-repo | --enterprise-commit | --community-repo | --community-commit | --acta)
		[ $# -ge 2 ] || cannot "$1 needs a value"
		case "$1" in
		--enterprise-repo) E_REPO="$2" ;;
		--enterprise-commit) E_COMMIT="$2" ;;
		--community-repo) C_REPO="$2" ;;
		--community-commit) C_COMMIT="$2" ;;
		--acta) ACTA="$2" ;;
		esac
		shift 2
		;;
	*) cannot "unknown argument '$1' (usage: --enterprise-repo DIR --enterprise-commit E40 --community-repo DIR --community-commit C40 [--acta PATH])" ;;
	esac
done
if [ -z "$E_REPO" ] || [ -z "$E_COMMIT" ] || [ -z "$C_REPO" ] || [ -z "$C_COMMIT" ]; then
	cannot "the four candidate inputs are required: --enterprise-repo --enterprise-commit --community-repo --community-commit"
fi

command -v git >/dev/null 2>&1 || cannot "git is not on PATH"
command -v go >/dev/null 2>&1 || cannot "go is not on PATH: the AST reader was not built"
[ -r "$FACTS" ] || cannot "missing $FACTS: no shared fact evaluator"
if [ ! -f "$AST_SRC/main.go" ] || [ ! -f "$AST_SRC/go.mod" ]; then
	cannot "missing $AST_SRC: no AST reader"
fi

[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
_bin_dir="$(mktemp -d "${TMPDIR:-/tmp}/overlaycand-bin.XXXXXX")" \
	|| cannot "cannot create a scratch dir (TMPDIR=${TMPDIR:-unset})"
_err="$(mktemp "${TMPDIR:-/tmp}/overlaycand-err.XXXXXX")" \
	|| cannot "cannot create a scratch file (TMPDIR=${TMPDIR:-unset})"
_ident="$_bin_dir/identity.json"

AST_BIN="$_bin_dir/overlay-ast"
if ! (cd "$AST_SRC" && GOWORK=off go build -o "$AST_BIN" .) >"$_err" 2>&1; then
	sed 's/^/    /' "$_err" >&2
	: >"$_err"
	cannot "the overlay AST reader did not build"
fi
[ -x "$AST_BIN" ] || cannot "built overlay-ast under TMPDIR=${TMPDIR:-/tmp} but it is not executable (noexec?)"

_rc=0
python3 "$FACTS" candidate \
	--overlay-repo "$E_REPO" --overlay-commit "$E_COMMIT" \
	--community-repo "$C_REPO" --community-commit "$C_COMMIT" \
	--acta "$ACTA" --ast-bin "$AST_BIN" --identity-out "$_ident" 2>"$_err" || _rc=$?
cat "$_err" >&2
case "$_rc" in
0 | 1 | 2) ;;
*) printf '%s: the evaluator exited %s, which is none of its three answers\n' "$NAME" "$_rc" >>"$_err"; _rc=2 ;;
esac
emit "$_rc" ""
