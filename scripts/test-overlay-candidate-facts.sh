#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-overlay-candidate-facts.sh and for the evaluator it SHARES with
# check-overlay-live-facts.sh (scripts/lib/overlay-facts.py). Hermetic.
#
# The fixture is the live-facts battery's own synthetic overlay, sourced as a library: the same
# overlay, Community repo, official sealer and acta judge both adapters, so «main green,
# candidate red» compares two subjects under one evaluator and not two fixtures. The fixtures are
# synthetic for the reason that battery gives: commercial bytes never enter this tree.
#
# Candidate commits are built with plumbing (read-tree/hash-object/commit-tree on a scratch
# index), never by checking out: the working trees stay whatever each case says they are.

set -uo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT (trinquete `lint:git-env`): this battery builds repositories and
# commits in them; an inherited `GIT_DIR` would land those writes in the live repository.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

_self="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
. "$_self/scripts/test-overlay-live-facts.sh" || {
	echo "FATAL: cannot load the live-facts fixture library" >&2
	exit 2
}
for _fn in write_sources stage republish write_acta run expect py_edit ok bad; do
	declare -F "$_fn" >/dev/null || {
		echo "FATAL: the live-facts fixture library does not define $_fn" >&2
		exit 2
	}
done
[ "$ROOT" = "$_self" ] || { echo "FATAL: fixture library resolved ROOT=$ROOT, not $_self" >&2; exit 2; }

ACTA_FIX="$TMP/tree/design/overlay-live-facts-fixture.json"
TRUSTED="$TMP/trusted/scripts"
MARK="$TMP/markers"

# The TRUSTED install: the adapter, the evaluator, the AST reader and the isolator, and nothing
# else. It is not inside either repository the candidate objects live in.
mkdir -p "$TRUSTED/lib" "$TRUSTED/overlay-ast" "$MARK" "$TMP/marker-src"
cp "$ROOT/scripts/check-overlay-candidate-facts.sh" "$TRUSTED/"
cp "$ROOT/scripts/lib/overlay-facts.py" "$ROOT/scripts/lib/git-env.sh" "$TRUSTED/lib/"
cp "$ROOT/scripts/overlay-ast/"*.go "$ROOT/scripts/overlay-ast/go.mod" "$TRUSTED/overlay-ast/"

cand() { # cand <E> <C> [extra args...]
	local rc=0 e="$1" c="$2"
	shift 2
	bash "$TRUSTED/check-overlay-candidate-facts.sh" \
		--enterprise-repo "$TMP/ent" --enterprise-commit "$e" \
		--community-repo "$TMP/tree" --community-commit "$c" \
		--acta "$ACTA_FIX" "$@" >"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
}

# The main adapter, with its own observation directory per invocation. The Module names a
# terminal result by act, seal generation, record, main and observed second, and never
# overwrites one; this battery runs main several times under ONE act and seal, so two runs in
# the same second would collide and read as lost custody (2) — a fixture artefact, not a fact.
mrun() {
	OLIVARES_OVERLAY_OBS_DIR="$(mktemp -d "$TMP/obs.XXXXXX")" || {
		bad "could not reserve an observation directory"
		return 1
	}
	export OLIVARES_OVERLAY_OBS_DIR
	run
	unset OLIVARES_OVERLAY_OBS_DIR
}

jf() { # jf <dotted.key> — one field of the last result document
	python3 - "$TMP/out" "$1" <<'PY' 2>/dev/null || printf '<unreadable>\n'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
for k in sys.argv[2].split("."):
    d = d.get(k) if isinstance(d, dict) else None
print("null" if d is None else json.dumps(d) if isinstance(d, (bool, int, dict, list)) else d)
PY
}

same() { # same <label> <got> <want>
	if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 — got '$2', wanted '$3'"; fi
}

differ() { # differ <label> <a> <b>
	if [ -n "$2" ] && [ "$2" != "$3" ]; then ok "$1"; else bad "$1 — '$2' vs '$3'"; fi
}

never_release() { # never_release <label>
	local pq auth
	pq="$(jf pair.release_qualified)"
	auth="$(jf authority.release_permit)"
	if [ "$pq" = false ] && [ "$auth" = false ] \
		&& ! grep -Eq '"(release_qualified|release_permit|qualification|admission)": true' "$TMP/out"; then
		ok "$1"
	else
		bad "$1 — release_qualified=$pq release_permit=$auth"
	fi
}

# mkcommit <repo> <parent> [put <path> <file> | exe <path> <file> | del <path>]... -> commit id
mkcommit() {
	local repo="$1" parent="$2" idx blob tree
	shift 2
	idx="$(mktemp "$TMP/mk-idx.XXXXXX")" && rm -f "$idx"
	GIT_INDEX_FILE="$idx" $GIT -C "$repo" read-tree "$parent" || return 1
	while [ $# -gt 0 ]; do
		case "$1" in
		put | exe)
			blob="$($GIT -C "$repo" hash-object -w -- "$3")" || return 1
			local mode=100644
			[ "$1" = exe ] && mode=100755
			GIT_INDEX_FILE="$idx" $GIT -C "$repo" update-index --add --cacheinfo "$mode,$blob,$2" || return 1
			shift 3
			;;
		del)
			GIT_INDEX_FILE="$idx" $GIT -C "$repo" update-index --force-remove -- "$2" || return 1
			shift 2
			;;
		*) return 1 ;;
		esac
	done
	tree="$(GIT_INDEX_FILE="$idx" $GIT -C "$repo" write-tree)" || return 1
	rm -f "$idx"
	$GIT -C "$repo" commit-tree "$tree" -p "$parent" -m "candidate $idx"
}

edited() { # edited <repo> <commit> <path> <old> <new> -> a scratch file with the edited blob
	local f
	f="$(mktemp "$TMP/edit.XXXXXX")" || return 1
	$GIT -C "$1" --no-replace-objects show "$2:$3" >"$f" && py_edit "$f" "$4" "$5" >&2 && printf '%s\n' "$f"
}

P_CAT=enterprise/activation/catalog.go
P_DB=cmd-overlay/olivares/durablebus_enterprise.go
P_AP=cmd-overlay/olivares/addonpacks_enterprise.go
P_MAP=commercial/module-slug-package.json

# ══ A · the shared valid facts: both adapters 0, identities recorded apart ══════════════════
stage
MAIN="$($GIT -C "$TMP/ent" rev-parse HEAD)"
MAIN_TREE="$($GIT -C "$TMP/ent" rev-parse "$MAIN^{tree}")"
HUB_TREE="$($GIT -C "$TMP/tree" rev-parse "$HUB_PIN^{tree}")"

mrun
expect 0 "A01 main: the sealed fixture main is CLEAN"
cand "$MAIN" "$HUB_PIN"
expect 0 "A02 candidate: the same facts as immutable objects are 0"
same "A03 E commit recorded" "$(jf enterprise.commit)" "$MAIN"
same "A04 E tree recorded" "$(jf enterprise.tree)" "$MAIN_TREE"
same "A05 E public gitlink recorded" "$(jf enterprise.public_gitlink)" "$HUB_PIN"
same "A06 C commit recorded" "$(jf community.commit)" "$HUB_PIN"
same "A07 C tree recorded" "$(jf community.tree)" "$HUB_TREE"
same "A08 an equal pair is classified as a source observation" "$(jf pair.classification)" exact-gitlink-source-observation
never_release "A09 even the EQUAL pair is not a release result"
same "A10 the result document names its meaning" "$(jf result.meaning)" facts-hold
same "A11 the facts are the evaluator's" "$(jf facts.iso42001_pack)" compliance-packs
differ "A12 the evaluator identity is recorded" "$(jf evaluator.overlay_facts_sha256)" null

# ══ B · main green, candidate mutation red ═══════════════════════════════════════════════════
f="$(edited "$TMP/ent" "$MAIN" "$P_CAT" "$ISO_ROW" "")"
E_NOISO="$(mkcommit "$TMP/ent" "$MAIN" put "$P_CAT" "$f")"
mrun
expect 0 "B01 main stays green: the candidate commit is not the sealed main"
main_out="$(cat "$TMP/out")"
cand "$E_NOISO" "$HUB_PIN"
expect 1 "B02 candidate: the iso42001 catalog row removed is a finding"
same "B03 the finding names the candidate commit, not main" "$(jf enterprise.commit)" "$E_NOISO"
differ "B04 main and candidate commits are distinct identities" "$(jf enterprise.commit)" "$MAIN"
differ "B05 main and candidate trees are distinct identities" "$(jf enterprise.tree)" "$MAIN_TREE"
case "$main_out" in
*"${MAIN:0:9}"*) ok "B06 main names its captured main" ;;
*) bad "B06 main did not name its captured main" ;;
esac
case "$main_out" in
*"${E_NOISO:0:9}"*) bad "B07 main named the candidate commit" ;;
*) ok "B07 main never names the candidate commit" ;;
esac
never_release "B08 a finding is not a release result either"

f="$(edited "$TMP/ent" "$MAIN" "$P_DB" '	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)' '	return true')"
cand "$(mkcommit "$TMP/ent" "$MAIN" put "$P_DB" "$f")" "$HUB_PIN"
expect 1 "B09 candidate: durableLicensed returning true is a finding (AST predicate)"

f="$(edited "$TMP/ent" "$MAIN" "$P_AP" '		return fromClaims(p)' '		_ = p
		return fromClaims(p)')"
cand "$(mkcommit "$TMP/ent" "$MAIN" put "$P_AP" "$f")" "$HUB_PIN"
expect 1 "B10 candidate: an extra statement is estructura no verificada (closed-shape digest)"

printf '%s\n' '{ "source": "fixture", "entries": [ { "slug": "doraregister", "package": "enterprise/doraregister" } ] }' >"$TMP/map-noiso.json"
C_NOISO="$(mkcommit "$TMP/tree" "$HUB_PIN" put "$P_MAP" "$TMP/map-noiso.json")"
cand "$MAIN" "$C_NOISO"
expect 1 "B11 candidate: a C slug map that stops naming iso42001 is a finding"
same "B12 that C is recorded apart from E's gitlink" "$(jf community.commit)/$(jf enterprise.public_gitlink)" "$C_NOISO/$HUB_PIN"
mrun
expect 0 "B13 main stays green: it reads the map at the gitlink its sealed main declares"

# ══ C · dirty working trees and a dirty index move nothing ═══════════════════════════════════
py_edit "$CAT" "$ISO_ROW" ""
$GIT -C "$TMP/ent" add -- enterprise/activation/catalog.go
py_edit "$DB" '	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)' '	return true'
cp "$TMP/map-noiso.json" "$TMP/tree/$P_MAP"
if [ -n "$($GIT -C "$TMP/ent" status --porcelain -- enterprise cmd-overlay)" ] \
	&& [ -n "$($GIT -C "$TMP/tree" status --porcelain -- commercial)" ]; then
	ok "C01 both repositories really are dirty (staged catalog, unstaged durable, unstaged map)"
else
	bad "C01 the dirty-tree fixture did not apply"
fi
cand "$MAIN" "$HUB_PIN"
expect 0 "C02 candidate: dirty E/C working trees and a staged E change cannot move the verdict"
same "C03 and the identities are the objects', not the checkout's" "$(jf enterprise.tree)" "$MAIN_TREE"
$GIT -C "$TMP/ent" reset -q -- enterprise
$GIT -C "$TMP/ent" checkout -q -- enterprise cmd-overlay
$GIT -C "$TMP/tree" checkout -q -- commercial

# ══ D · a wrong or missing E/C object is 2, never 0 and never 1 ══════════════════════════════
look2() { # look2 <label> [<E-field> <want>]
	expect 2 "$1"
	[ "$(jf result.meaning)" = could-not-look ] || bad "$1 — result document meaning is $(jf result.meaning)"
	never_release "$1 (not a release result)"
}
cand 0123456789abcdef0123456789abcdef01234567 "$HUB_PIN"
look2 "D01 E is not an object in the store"
same "D02 an unestablished E is recorded null, never the request" "$(jf enterprise.commit)" null
cand "$MAIN_TREE" "$HUB_PIN"
look2 "D03 E is a tree object, not a commit"
$GIT -C "$TMP/ent" tag -a -m t cand-tag "$MAIN"
cand "$($GIT -C "$TMP/ent" rev-parse cand-tag)" "$HUB_PIN"
look2 "D04 E is an annotated tag object, not a commit"
cand main "$HUB_PIN"
look2 "D05 E is a branch name: names move, objects do not"
cand "${MAIN:0:12}" "$HUB_PIN"
look2 "D06 E is an abbreviation"
cand "$(mkcommit "$TMP/ent" "$MAIN" del "$P_AP")" "$HUB_PIN"
look2 "D07 E lacks one of the six sources"
cand "$(mkcommit "$TMP/ent" "$MAIN" del public)" "$HUB_PIN"
look2 "D08 E declares no public gitlink"
printf '%s\n' 'package main' 'func (' >"$TMP/bad.go"
cand "$(mkcommit "$TMP/ent" "$MAIN" put "$P_AP" "$TMP/bad.go")" "$HUB_PIN"
look2 "D09 E carries a malformed Go source"
cand "$MAIN" fedcba9876543210fedcba9876543210fedcba98
look2 "D10 C is not an object in the store"
same "D11 E stays recorded while C is null" "$(jf enterprise.commit)/$(jf community.commit)" "$MAIN/null"
cand "$MAIN" "$($GIT -C "$TMP/tree" rev-parse "$HUB_PIN:$P_MAP")"
look2 "D12 C is a blob object, not a commit"
cand "$MAIN" "$(mkcommit "$TMP/tree" "$HUB_PIN" del "$P_MAP")"
look2 "D13 C has no public slug map"
printf 'not json\n' >"$TMP/map-bad.json"
cand "$MAIN" "$(mkcommit "$TMP/tree" "$HUB_PIN" put "$P_MAP" "$TMP/map-bad.json")"
look2 "D14 C's slug map is not readable as the sold map"
rc=0
bash "$TRUSTED/check-overlay-candidate-facts.sh" --enterprise-repo "$TMP/no-such-repo" \
	--enterprise-commit "$MAIN" --community-repo "$TMP/tree" --community-commit "$HUB_PIN" \
	--acta "$ACTA_FIX" >"$TMP/out" 2>"$TMP/err" || rc=$?
echo "$rc" >"$TMP/rc"
look2 "D15 the enterprise repository is not a repository"
rc=0
bash "$TRUSTED/check-overlay-candidate-facts.sh" --enterprise-repo "$TMP/ent" \
	--enterprise-commit "$MAIN" --community-repo "$TMP/tree" --community-commit "$HUB_PIN" \
	>"$TMP/out" 2>"$TMP/err" || rc=$?
echo "$rc" >"$TMP/rc"
look2 "D16 the trusted install has no acta: the default acta is missing"
rc=0
bash "$TRUSTED/check-overlay-candidate-facts.sh" --enterprise-repo "$TMP/ent" \
	--enterprise-commit "$MAIN" >"$TMP/out" 2>"$TMP/err" || rc=$?
echo "$rc" >"$TMP/rc"
look2 "D17 a missing required input"

# ══ E · a C that is not E's gitlink is a development observation, never a release ═══════════
C_SAME="$($GIT -C "$TMP/tree" commit-tree "$HUB_TREE" -p "$HUB_PIN" -m 'same map, another commit')"
cand "$MAIN" "$C_SAME"
expect 0 "E01 candidate: valid facts at a C that is not the gitlink are still 0 as FACTS"
same "E02 the pair relation says different" "$(jf pair.relation)" different
same "E03 and is classified a development source observation" "$(jf pair.classification)" development-source-observation
never_release "E04 a gitlink/C mismatch is never a release result"
same "E05 E gitlink and C are recorded as two identities" "$(jf enterprise.public_gitlink)/$(jf community.commit)" "$HUB_PIN/$C_SAME"
if grep -q 'NOT a qualification, admission or release result' "$TMP/err"; then
	ok "E06 the human line says it is not a release result"
else
	bad "E06 the human line lacks the non-release statement"
fi

# ══ F · marker-bearing candidate scripts never execute ═══════════════════════════════════════
for m in check-overlay-candidate-facts.sh check-overlay-live-facts.sh git-env.sh pre-push hook; do
	printf '#!/bin/sh\n: >"%s/%s"\n' "$MARK" "$m" >"$TMP/marker-src/$m"
	chmod +x "$TMP/marker-src/$m"
done
printf 'open("%s", "w").close()\n' "$MARK/overlay-facts.py" >"$TMP/marker-src/overlay-facts.py"
printf 'package main\n\nimport "os"\n\nfunc init() { _ = os.WriteFile("%s", nil, 0o644) }\n\nfunc main() {}\n' \
	"$MARK/overlay-ast" >"$TMP/marker-src/main.go"
printf 'module marker\n\ngo 1.22\n' >"$TMP/marker-src/go.mod"
printf "version: '3'\ntasks:\n  default:\n    cmds:\n      - touch %s/Taskfile\n" "$MARK" >"$TMP/marker-src/Taskfile.yml"

markers() { # the marker files, as one repository's candidate content
	printf '%s\n' \
		exe scripts/check-overlay-candidate-facts.sh "$TMP/marker-src/check-overlay-candidate-facts.sh" \
		exe scripts/check-overlay-live-facts.sh "$TMP/marker-src/check-overlay-live-facts.sh" \
		put scripts/lib/overlay-facts.py "$TMP/marker-src/overlay-facts.py" \
		exe scripts/lib/git-env.sh "$TMP/marker-src/git-env.sh" \
		put scripts/overlay-ast/main.go "$TMP/marker-src/main.go" \
		put scripts/overlay-ast/go.mod "$TMP/marker-src/go.mod" \
		put Taskfile.yml "$TMP/marker-src/Taskfile.yml" \
		exe .githooks/pre-push "$TMP/marker-src/pre-push"
}
mapfile -t MK < <(markers)
E_MARK="$(mkcommit "$TMP/ent" "$MAIN" "${MK[@]}")"
C_MARK="$(mkcommit "$TMP/tree" "$HUB_PIN" "${MK[@]}")"
f="$(edited "$TMP/ent" "$MAIN" "$P_CAT" "$ISO_ROW" "")"
E_MARK_FINDING="$(mkcommit "$TMP/ent" "$MAIN" "${MK[@]}" put "$P_CAT" "$f")"
E_MARK_BAD="$(mkcommit "$TMP/ent" "$MAIN" "${MK[@]}" put "$P_AP" "$TMP/bad.go")"
C_MARK_NOMAP="$(mkcommit "$TMP/tree" "$HUB_PIN" "${MK[@]}" del "$P_MAP")"
# The same markers in the overlay's working tree, and as live hooks and fsmonitor in both
# repositories' own configuration: a checkout, an index refresh or a ref update would fire one.
mkdir -p "$TMP/ent/scripts/lib" "$TMP/ent/scripts/overlay-ast"
cp "$TMP/marker-src/check-overlay-candidate-facts.sh" "$TMP/marker-src/check-overlay-live-facts.sh" "$TMP/ent/scripts/"
cp "$TMP/marker-src/overlay-facts.py" "$TMP/marker-src/git-env.sh" "$TMP/ent/scripts/lib/"
cp "$TMP/marker-src/main.go" "$TMP/marker-src/go.mod" "$TMP/ent/scripts/overlay-ast/"
cp "$TMP/marker-src/Taskfile.yml" "$TMP/ent/Taskfile.yml"
for r in "$TMP/ent" "$TMP/tree"; do
	for h in post-checkout post-index-change reference-transaction pre-auto-gc; do
		cp "$TMP/marker-src/hook" "$r/.git/hooks/$h"
	done
	$GIT -C "$r" config core.fsmonitor "$TMP/marker-src/hook"
done

# Positive controls: every marker kind really fires when something DOES run it.
sh "$TMP/ent/scripts/check-overlay-candidate-facts.sh"
python3 "$TMP/ent/scripts/lib/overlay-facts.py"
$GIT -C "$TMP/ent" update-ref refs/heads/marker-probe "$MAIN"
if [ -e "$MARK/check-overlay-candidate-facts.sh" ] && [ -e "$MARK/overlay-facts.py" ] && [ -e "$MARK/hook" ]; then
	ok "F01 positive control: the shell, python and hook markers fire when executed"
else
	bad "F01 positive control: a marker did not fire when executed [$(find "$MARK" -mindepth 1 -printf '%f ')]"
fi
$GIT -c core.hooksPath=/dev/null -C "$TMP/ent" update-ref -d refs/heads/marker-probe
rm -f "$MARK"/*

cand "$E_MARK" "$C_MARK"
expect 0 "F02 candidate with marker-bearing E and C: the facts hold"
cand "$E_MARK_FINDING" "$C_MARK"
expect 1 "F03 candidate with markers and a fact violation: finding"
cand "$E_MARK_BAD" "$C_MARK"
expect 2 "F04 bad source with markers: could not look"
cand "$E_MARK" "$C_MARK_NOMAP"
expect 2 "F05 C without its map, with markers: could not look"
left="$(ls -A "$MARK")"
if [ -z "$left" ]; then
	ok "F06 no candidate script, Taskfile, hook, parser or fsmonitor ran in any of the four"
else
	bad "F06 candidate content EXECUTED: $left"
fi
for r in "$TMP/ent" "$TMP/tree"; do
	rm -f "$r/.git/hooks/post-checkout" "$r/.git/hooks/post-index-change" \
		"$r/.git/hooks/reference-transaction" "$r/.git/hooks/pre-auto-gc"
	$GIT -C "$r" config --unset core.fsmonitor
done
rm -rf "$TMP/ent/scripts" "$TMP/ent/Taskfile.yml"

# ══ G · the LT1 seal is main's alone, and main takes no candidate SHA ════════════════════════
rm -f "$TMP/seal"
mrun
expect 2 "G01 main without its seal is could-not-look (LT1 unchanged)"
cand "$MAIN" "$HUB_PIN"
expect 0 "G02 candidate neither needs nor consumes a seal"
republish
export OLIVARES_OVERLAY_CURRENT_SHA="$E_NOISO"
mrun
unset OLIVARES_OVERLAY_CURRENT_SHA
expect 0 "G03 main ignores an inherited OLIVARES_OVERLAY_CURRENT_SHA naming a red candidate"
case "$(cat "$TMP/out")" in
*"${MAIN:0:9}"*) ok "G04 and it still names its own captured main" ;;
*) bad "G04 main did not name its captured main under the inherited SHA" ;;
esac
if ! grep -Eq -- '--(enterprise|overlay|community)-commit' "$ROOT/scripts/check-overlay-live-facts.sh"; then
	ok "G05 the main caller has no candidate commit option"
else
	bad "G05 the main caller grew a candidate commit option"
fi

# ══ H · ONE evaluator: both adapters use it, and it decides for both ═════════════════════════
for s in check-overlay-live-facts.sh check-overlay-candidate-facts.sh; do
	if grep -q 'lib/overlay-facts.py' "$ROOT/scripts/$s" && ! grep -q 'REQUIRED_CHECKS' "$ROOT/scripts/$s"; then
		ok "H01 $s calls the shared evaluator and carries no predicate table of its own"
	else
		bad "H01 $s does not delegate to scripts/lib/overlay-facts.py"
	fi
done

# The overlay main declares a Community commit whose map stops naming iso42001: both adapters
# find it. One mutation of the evaluator's slug-map comparison then silences BOTH.
$GIT -C "$TMP/ent" update-index --add --cacheinfo "160000,$C_NOISO,public"
$GIT -C "$TMP/ent" commit -q -m "gitlink to a map without iso42001"
republish
MAIN2="$($GIT -C "$TMP/ent" rev-parse HEAD)"
write_acta "$MAIN2" "$C_NOISO"
mrun
expect 1 "H02 main: its declared map no longer names iso42001"
cand "$MAIN2" "$C_NOISO"
expect 1 "H03 candidate: the same map at the explicit C is the same finding"
for copy in "$TMP/tree/scripts/lib/overlay-facts.py" "$TRUSTED/lib/overlay-facts.py"; do
	cp "$copy" "$copy.orig"
	sed -i 's/^    if present is not acta.get("iso42001_in_public_slug_map"):$/    if False:/' "$copy"
done
if grep -q '^    if False:$' "$TMP/tree/scripts/lib/overlay-facts.py" && grep -q '^    if False:$' "$TRUSTED/lib/overlay-facts.py"; then
	ok "H04 the evaluator mutant applied to both installs"
else
	bad "H04 the evaluator mutant did not apply — the causal control is dead"
fi
mrun
expect 0 "H05 mutant evaluator: main no longer finds it"
cand "$MAIN2" "$C_NOISO"
expect 0 "H06 mutant evaluator: candidate no longer finds it either"
for copy in "$TMP/tree/scripts/lib/overlay-facts.py" "$TRUSTED/lib/overlay-facts.py"; do
	mv "$copy.orig" "$copy"
done
mrun
expect 1 "H07 restored evaluator: main finds it again"

# ══ J · observed-crl-keyring-v1: one whole profile, exact support, closed inputs ═════════════
jexpr() { # jexpr <python expression over d, the last result document>
	python3 - "$TMP/out" "$1" <<'PY' 2>/dev/null || printf '<unreadable>\n'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
print(eval(sys.argv[2], {"d": d}))
PY
}

acta_mut() { # acta_mut <mode> -> $TMP/acta-mut.json, a mutated copy of the fixture acta
	python3 - "$ACTA_FIX" "$TMP/acta-mut.json" "$1" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
mode = sys.argv[3]
L = d["reviewed_constructions"]["legacy-direct-v1"]
C = d["reviewed_constructions"]["observed-crl-keyring-v1"]
W = "cmd-overlay/olivares/durablebus_enterprise.go#durableLicensed"
H = W + "At"
P = "cmd-overlay/olivares/addonpacks_enterprise.go#packProductID"
if mode == "legacy-digest-in-current":
    C["reviewed_node_digests"][W] = L["reviewed_node_digests"][W]
elif mode == "legacy-imports-in-current":
    C["reviewed_imports"] = L["reviewed_imports"]
elif mode == "helper-omitted":
    del C["reviewed_node_digests"][H]
elif mode == "node-omitted":
    del L["reviewed_node_digests"][P]
    del C["reviewed_node_digests"][P]
elif mode == "support-omitted":
    C["support_blobs"].pop()
elif mode == "profile-renamed":
    d["reviewed_constructions"]["observed-crl-keyring-v2"] = d["reviewed_constructions"].pop("observed-crl-keyring-v1")
elif mode == "legacy-stops-requiring-absence":
    L["required_absent_nodes"] = []
elif mode == "v1-flat":
    d["schema"] = "overlay-live-facts/v1"
    d["reviewed_surface_schema"] = "overlay-ast/v1"
    d["reviewed_node_digests"] = L["reviewed_node_digests"]
    d["reviewed_imports"] = L["reviewed_imports"]
    for k in ("reviewed_digest_schema", "reviewed_constructions", "reviewed_construction_sources"):
        del d[k]
else:
    raise SystemExit("unknown acta mutation %s" % mode)
json.dump(d, open(sys.argv[2], "w", encoding="utf-8"), indent=2)
PY
}

cand_acta() { # cand_acta <acta> <E> <C>
	local rc=0
	bash "$TRUSTED/check-overlay-candidate-facts.sh" \
		--enterprise-repo "$TMP/ent" --enterprise-commit "$2" \
		--community-repo "$TMP/tree" --community-commit "$3" \
		--acta "$1" >"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
}

stage
MAIN="$($GIT -C "$TMP/ent" rev-parse HEAD)"
write_current_durable "$TMP/current-durable.go"
E_CUR="$(mkcommit "$TMP/ent" "$MAIN" put "$P_DB" "$TMP/current-durable.go")"
FSHAPE='(d["facts"]["construction"], len(d["facts"]["inputs"]), len(d["facts"]["support"]), sum(n["present"] for n in d["facts"]["nodes"].values()), len(d["facts"]["checks"]), d["facts"]["community"]["source"])'

cand "$E_CUR" "$HUB_PIN"
expect 0 "J01 candidate: the current construction with its four exact support files is 0"
same "J02 current: 11 inputs, 4 support identities, 11 present nodes, 40 checks, explicit C" \
	"$(jexpr "$FSHAPE")" "('observed-crl-keyring-v1', 11, 4, 11, 40, 'explicit-candidate-commit')"
never_release "J03 a current-construction pass is still not a release result"

cand "$MAIN" "$HUB_PIN"
same "J04 legacy: 7 inputs, no support, 10 present nodes, the helper explicitly absent" \
	"$(jexpr "$FSHAPE + (d['facts']['nodes']['cmd-overlay/olivares/durablebus_enterprise.go#durableLicensedAt']['present'],)")" \
	"('legacy-direct-v1', 7, 0, 10, 40, 'explicit-candidate-commit', False)"

f="$(edited "$TMP/ent" "$E_CUR" "$P_DB" 'getenv, time.Now)' 'getenv, nil)')"
E_CUR_NIL="$(mkcommit "$TMP/ent" "$E_CUR" put "$P_DB" "$f")"
mrun
expect 0 "J05 main stays green on its sealed legacy construction"
cand "$E_CUR_NIL" "$HUB_PIN"
expect 1 "J06 main green never rescues a current candidate whose wrapper forwards a nil clock"

C_NOTRUST="$(mkcommit "$TMP/tree" "$HUB_PIN" del cmd/olivares/license_trust.go)"
cand "$E_CUR" "$C_NOTRUST"
look2 "J07 a Community support file missing at the explicit C"
cand "$MAIN" "$C_NOTRUST"
expect 0 "J08 the legacy construction reads no support file at the same C"

$GIT -C "$TMP/tree" show "$HUB_PIN:cmd/olivares/license_trust.go" >"$TMP/trust-changed.go"
printf '\n// an unreviewed byte\n' >>"$TMP/trust-changed.go"
cand "$E_CUR" "$(mkcommit "$TMP/tree" "$HUB_PIN" put cmd/olivares/license_trust.go "$TMP/trust-changed.go")"
expect 1 "J09 a byte-changed Community support binding is 1"

$GIT -C "$TMP/ent" show "$E_CUR:enterprise/addongate/addongate.go" >"$TMP/gate-changed.go"
printf '\n// an unreviewed byte\n' >>"$TMP/gate-changed.go"
cand "$(mkcommit "$TMP/ent" "$E_CUR" put enterprise/addongate/addongate.go "$TMP/gate-changed.go")" "$HUB_PIN"
expect 1 "J10 a byte-changed private support binding is 1"

cand "$(mkcommit "$TMP/ent" "$E_CUR" del enterprise/addongate/addongate.go)" "$HUB_PIN"
look2 "J11 the private support file missing from E"

$GIT -C "$TMP/ent" show "$MAIN:$P_DB" >"$TMP/mixed.go"
python3 - "$TMP/mixed.go" "$TMP/current-durable.go" <<'PY'
import sys
helper = open(sys.argv[2], encoding="utf-8").read()
open(sys.argv[1], "a", encoding="utf-8").write("\n" + helper[helper.index("func durableLicensedAt("):])
PY
cand "$(mkcommit "$TMP/ent" "$MAIN" put "$P_DB" "$TMP/mixed.go")" "$HUB_PIN"
expect 1 "J12 the legacy body beside the current helper is neither construction"

acta_mut legacy-digest-in-current
cand_acta "$TMP/acta-mut.json" "$E_CUR" "$HUB_PIN"
expect 1 "J13 a legacy wrapper digest inside the current profile cannot complete it"

acta_mut legacy-imports-in-current
cand_acta "$TMP/acta-mut.json" "$E_CUR" "$HUB_PIN"
expect 1 "J14 legacy import lists inside the current profile cannot complete it"

python3 - "$TMP/current-durable.go" "$TMP/wrapper-only.go" <<'PY'
import sys
src = open(sys.argv[1], encoding="utf-8").read()
open(sys.argv[2], "w", encoding="utf-8").write(src[:src.index("func durableLicensedAt(")])
PY
acta_mut helper-omitted
cand_acta "$TMP/acta-mut.json" "$(mkcommit "$TMP/ent" "$MAIN" put "$P_DB" "$TMP/wrapper-only.go")" "$HUB_PIN"
expect 1 "J15 the helper omitted from both source and acta cannot shrink the union"

$GIT -C "$TMP/ent" show "$MAIN:$P_AP" >"$TMP/no-productid.go"
python3 - "$TMP/no-productid.go" <<'PY'
import sys
src = open(sys.argv[1], encoding="utf-8").read()
start = src.index("func packProductID(")
end = src.index("\n}\n", start) + 3
open(sys.argv[1], "w", encoding="utf-8").write(src[:start] + src[end:])
PY
acta_mut node-omitted
cand_acta "$TMP/acta-mut.json" "$(mkcommit "$TMP/ent" "$MAIN" put "$P_AP" "$TMP/no-productid.go")" "$HUB_PIN"
expect 1 "J16 an original node omitted from both source and acta cannot shrink the union"

acta_mut support-omitted
cand_acta "$TMP/acta-mut.json" "$E_CUR" "$HUB_PIN"
expect 1 "J17 an acta with a support descriptor omitted is invalid"

acta_mut profile-renamed
cand_acta "$TMP/acta-mut.json" "$E_CUR" "$HUB_PIN"
expect 1 "J18 an acta naming another profile ID is invalid"

acta_mut legacy-stops-requiring-absence
cand_acta "$TMP/acta-mut.json" "$MAIN" "$HUB_PIN"
expect 1 "J19 a legacy profile that stops requiring the helper absent is invalid"

acta_mut v1-flat
cand_acta "$TMP/acta-mut.json" "$MAIN" "$HUB_PIN"
expect 1 "J20 a v1 flat acta is refused by the v2 evaluator"

# ══ I · inherited Git selectors cannot redirect the candidate reads ═══════════════════════════
stage
MAIN="$($GIT -C "$TMP/ent" rev-parse HEAD)"
$GIT init -q -b poison "$TMP/cdecoy"
printf 'decoy\n' >"$TMP/cdecoy/README"
$GIT -C "$TMP/cdecoy" add README
$GIT -C "$TMP/cdecoy" commit -q -m decoy
decoy_before="$($GIT -C "$TMP/cdecoy" rev-parse HEAD; $GIT -C "$TMP/cdecoy" for-each-ref)"
rc=0
GIT_DIR="$TMP/cdecoy/.git" GIT_WORK_TREE="$TMP/cdecoy" GIT_INDEX_FILE="$TMP/cdecoy/.git/index" \
	bash "$TRUSTED/check-overlay-candidate-facts.sh" --enterprise-repo "$TMP/ent" \
	--enterprise-commit "$MAIN" --community-repo "$TMP/tree" --community-commit "$HUB_PIN" \
	--acta "$ACTA_FIX" >"$TMP/out" 2>"$TMP/err" || rc=$?
echo "$rc" >"$TMP/rc"
expect 0 "I01 poisoned GIT_DIR/WORK_TREE/INDEX: the candidate still reads its own objects"
same "I02 and records the fixture commit, not the decoy" "$(jf enterprise.commit)" "$MAIN"
same "I03 the decoy is untouched" "$($GIT -C "$TMP/cdecoy" rev-parse HEAD; $GIT -C "$TMP/cdecoy" for-each-ref)" "$decoy_before"

# shellcheck disable=SC2154 # pass/fail are the fixture library's counters
echo "check-overlay-candidate-facts selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
exit 0
