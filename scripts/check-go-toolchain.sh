#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-go-toolchain.sh — a module in the workspace must not carry its own `toolchain` line,
# and its `go` line must not ask for more than the workspace pins.
#
# WHY, and it is not tidiness. `go.work` pins `toolchain go1.26.5` for every module in its `use`
# list; a `toolchain` line inside such a module's go.mod is dead in the workspace — the workspace
# wins — and ALIVE outside it, under `GOWORK=off`. So the two can disagree indefinitely and
# nothing says so: the build you run every day reads one value and the build that publishes an
# Apache module alone reads the other. Ten of them were removed on 2026-08-08 for exactly that
# reason, and the adversarial contrast of that change found the real defect: NOTHING would have
# stopped them coming back. A one-line mutant reintroducing `toolchain go1.26.5` passed every gate
# in the repository. This is that missing gate.
#
# WHAT IT DOES NOT DO, said plainly: it does not forbid a `toolchain` line in a module that is NOT
# in the workspace. Outside `use`, that line is the only pin the module has and removing it would
# be a regression, not a cleanup. The rule is about REDUNDANCY inside the workspace, and the
# membership test is what makes it safe — an earlier version of this idea, written from the
# assumption that all ten modules were in the workspace, would have been wrong the moment one was
# moved out. The ten were verified to be in the `use` list before the directives were removed.
#
# THE SECOND RULE, which is the one that catches drift rather than duplication: a module whose `go`
# directive is HIGHER than the workspace's cannot be built by the toolchain the workspace pins, and
# go would resolve that by downloading a newer one — silently changing what compiles the estate.
#
# Exit 0 CLEAN · 1 a violation, each named · 2 COULD NOT LOOK. Never a silent green.
set -u
set -o pipefail
export LC_ALL=C

say() { printf '%s\n' "$*"; }
cannot_look() {
	say "check-go-toolchain: COULD NOT LOOK — $1" >&2
	say "check-go-toolchain: this is not a clean verdict." >&2
	exit 2
}

ROOT="${OLIVARES_GO_WORK_ROOT:-}"
if [ -z "$ROOT" ]; then
	ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || cannot_look "cannot resolve the repository root"
fi
cd "$ROOT" || cannot_look "cannot enter $ROOT"
[ -f go.work ] || cannot_look "no go.work at $ROOT, so workspace membership is UNKNOWN and this gate
  cannot tell a redundant toolchain line from the only pin a module has"

# --- the workspace's own pins ------------------------------------------------------------------
WS_GO=$(grep -m1 -E '^go[[:space:]]+[0-9]' go.work | awk '{print $2}')
[ -n "$WS_GO" ] || cannot_look "go.work has no 'go' directive; the version to compare against is UNKNOWN"

# --- the use list, which is the whole safety of rule 1 -------------------------------------------
USED=$(awk '
	/^use[[:space:]]*\(/ {inb=1; next}
	inb && /^\)/ {inb=0; next}
	inb {gsub(/^[[:space:]]+|[[:space:]]+$/,""); gsub(/^\.\//,""); if ($0 != "" && $0 !~ /^\/\//) print}
	/^use[[:space:]]+[^(]/ {sub(/^use[[:space:]]+/,""); gsub(/^\.\//,""); print}
' go.work)
[ -n "$USED" ] || cannot_look "go.work declares no modules in its 'use' list; with an empty membership
  set this gate would examine zero modules and report clean, which is the failure it exists to avoid"

N=0
FINDINGS=0
# ver_le A B -> 0 when A <= B, comparing dotted numbers field by field. Plain string compare would
# call 1.26.10 older than 1.26.5, which is the sort of thing that only bites once and expensively.
ver_le() {
	local a="$1" b="$2" i
	local -a A B
	IFS=. read -r -a A <<<"$a"
	IFS=. read -r -a B <<<"$b"
	for i in 0 1 2; do
		local x=${A[i]:-0} y=${B[i]:-0}
		[ "$x" -lt "$y" ] 2>/dev/null && return 0
		[ "$x" -gt "$y" ] 2>/dev/null && return 1
	done
	return 0
}

while IFS= read -r m; do
	[ -n "$m" ] || continue
	f="$m/go.mod"
	if [ ! -f "$f" ]; then
		cannot_look "go.work lists './$m' but $f does not exist; the workspace and the tree disagree
  and every answer below would be about a different set of modules than the one go builds"
	fi
	[ -r "$f" ] || cannot_look "$f exists and is unreadable"
	N=$((N + 1))

	tc=$(grep -m1 -E '^toolchain[[:space:]]' "$f" | awk '{print $2}')
	if [ -n "$tc" ]; then
		FINDINGS=$((FINDINGS + 1))
		say "check-go-toolchain: REDUNDANT TOOLCHAIN — $f pins '$tc' while go.work pins it for the" >&2
		say "  whole workspace. Inside the workspace this line is DEAD (the workspace wins); outside" >&2
		say "  it, under GOWORK=off, it is the one that applies. So the two can drift apart and only" >&2
		say "  the standalone build notices — which is the build that publishes an Apache module." >&2
		say "  repair: delete the line. The module's 'go' directive already requires >= that version." >&2
	fi

	gv=$(grep -m1 -E '^go[[:space:]]+[0-9]' "$f" | awk '{print $2}')
	if [ -z "$gv" ]; then
		FINDINGS=$((FINDINGS + 1))
		say "check-go-toolchain: NO GO DIRECTIVE — $f declares no 'go' version, so nothing states the" >&2
		say "  language level it needs and removing a toolchain line from it would leave no pin at all." >&2
	elif ! ver_le "$gv" "$WS_GO"; then
		FINDINGS=$((FINDINGS + 1))
		say "check-go-toolchain: GO DIRECTIVE ABOVE THE WORKSPACE — $f asks for go $gv, go.work pins $WS_GO." >&2
		say "  The pinned toolchain cannot build it, so go would fetch a newer one and silently change" >&2
		say "  what compiles the estate. Raise go.work in the same commit, or lower the module." >&2
	fi
done <<EOF
$USED
EOF

[ "$N" -gt 0 ] || cannot_look "zero modules examined; go.work's use list resolved to nothing readable"

# --- THE THIRD RULE: the modules go.work does NOT list ------------------------------------------
#
# ⛔ ADDED 2026-08-14, and it is the rule whose absence cost the whole day. Everything above
# enumerates `go.work`'s use list, so this gate answered, forever and identically:
#
#     check-go-toolchain: CLEAN — 11 workspace module(s), …
#
# ELEVEN, always eleven. The tree holds TWENTY-TWO go.mod files. The other eleven — cloud/control-plane,
# both commerce modules, the three protocol examples, the three parquet spikes, and two under
# scripts/ — were never opened, so a confident CLEAN was published about a set half the size of the
# one it named. That is not an omission in a report: rule 2 exists precisely to stop a module from
# compiling under a toolchain the estate did not choose, and it was enforced on half the estate.
#
# What it cost, measured the same day: go.work moved to go1.26.6 and those eleven stayed on 1.26.5
# (three spikes on 1.26.4). SEVEN vulnerabilities that govulncheck reports as CALLED — six in the
# standard library — stayed alive in them, blocking every code PR in the repository. The gate that
# found them was the vulnerability gate, which had ALREADY been fixed for this exact blindness and
# scans fourteen modules. Two gates over the same property, one with the real census and one with
# the census of eleven.
#
# THE RULE IS NOT rule 1. A `toolchain` line outside the workspace is legitimate — it is the only pin
# such a module has, and the header above says so deliberately. The property here is narrower and it
# is the one that actually failed: a module's EFFECTIVE pin (its own `toolchain` when present,
# otherwise its `go` directive) must not sit BELOW what the workspace pins. Below it, a standard
# library advisory that the workspace bump fixes stays open in that module, and nothing says so.
#
# The census comes from `git ls-files`, not `find`: `find` grades whatever is lying in the tree —
# scratch clones, build output, another lane's worktree — which is how a sibling gate once measured
# 163 files where the repository has 55.
#
# THE CENSUS HAS TWO SOURCES AND SAYS WHICH ONE IT USED. `git ls-files` grades the REPOSITORY;
# `find` grades whatever is lying in the tree, which is how a sibling gate once measured 163 files
# where the repository has 55. So git wins wherever there is a work tree — and the fallback is not
# a nicety: this script's own battery builds synthetic trees in temp directories that are not
# repositories at all, and a census that can only speak git would answer COULD NOT LOOK to every
# case in it. (Measured: it did, four cases, rc=2 where 0 or 1 was expected.)
OUTSIDE=0
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	CENSUS_ALL="$(git ls-files)"
	CENSUS_SRC="the git index"
	CENSUS=$(git ls-files '*go.mod' 2>/dev/null | sed 's#/\?go\.mod$##') ||
		cannot_look "this is a git work tree and git ls-files failed, so the set of modules is UNKNOWN;
  a verdict over the workspace alone would repeat the defect this rule exists to close"
else
	CENSUS_ALL="$(find . -type f | sed "s#^\./##")"
	CENSUS_SRC="the filesystem (no git work tree)"
	CENSUS=$(find . -name go.mod -not -path '*/node_modules/*' 2>/dev/null | sed 's#^\./##; s#/\?go\.mod$##')
fi
[ -n "$CENSUS" ] || cannot_look "the census found no go.mod at all via $CENSUS_SRC, and an empty
  census reads as clean — which is the failure mode this rule was added to close"

# The census can be larger than the workspace (that is the whole point) but never SMALLER: if it is,
# it is measuring a different tree than the one go.work describes, and every count below is about
# the wrong set. Reported as COULD NOT LOOK, never as a finding.
CENSUS_N=$(printf '%s\n' "$CENSUS" | grep -c .)
[ "$CENSUS_N" -ge "$N" ] || cannot_look "the census via $CENSUS_SRC found $CENSUS_N module(s) while
  go.work lists $N; the two disagree about which tree this is"

while IFS= read -r m; do
	[ -n "$m" ] || continue
	# Workspace membership decides WHICH rules apply, so it is matched exactly, not by prefix:
	# 'sdk' must not claim 'sdk/plugin', and 'core' must not claim 'cores'.
	inws=0
	while IFS= read -r u; do
		[ "$u" = "$m" ] && { inws=1; break; }
	done <<EOF
$USED
EOF
	[ "$inws" -eq 1 ] && continue

	f="$m/go.mod"
	[ -r "$f" ] || cannot_look "$f is listed in the index and unreadable"
	OUTSIDE=$((OUTSIDE + 1))

	otc=$(grep -m1 -E '^toolchain[[:space:]]' "$f" | awk '{print $2}')
	ogv=$(grep -m1 -E '^go[[:space:]]+[0-9]' "$f" | awk '{print $2}')
	# The effective pin under GOTOOLCHAIN=auto: an explicit toolchain wins, else the go directive.
	eff="${otc#go}"
	[ -n "$eff" ] || eff="$ogv"
	if [ -z "$eff" ]; then
		FINDINGS=$((FINDINGS + 1))
		say "check-go-toolchain: NO PIN OUTSIDE THE WORKSPACE — $f declares neither a 'go' nor a" >&2
		say "  'toolchain' directive, so nothing states which toolchain compiles it." >&2
	elif ! ver_le "$WS_GO" "$eff"; then
		FINDINGS=$((FINDINGS + 1))
		say "check-go-toolchain: PIN BELOW THE WORKSPACE — $f is outside go.work's use list and pins" >&2
		say "  $eff, while the workspace pins $WS_GO. It is built by an OLDER toolchain than the rest of" >&2
		say "  the estate, so a standard-library advisory that the workspace bump closes stays open here" >&2
		say "  and no build of the workspace will ever say so." >&2
		say "  repair: raise this module's 'go' (or 'toolchain') to $WS_GO in the same commit." >&2
	fi
done <<EOF
$CENSUS
EOF

# --- THE FOURTH RULE: the toolchain the BUILDERS use -------------------------------------------
#
# Rules 1-3 are about go.mod files. They were all CLEAN on 2026-08-15 while the release workflow
# built the shipped binary with go1.26.5, because a `go.work` bump moves what `go build` demands and
# moves NOTHING about the image the builder runs in. Measured that day, after the workspace went to
# ⛔ LAS TRES FORMAS DE `FROM golang:`, Y LA QUE FALTABA ERA LA QUE ESCONDÍA EL DEFECTO.
# Hasta el 2026-08-27 este extractor pedía TRES componentes (`X.Y.Z`). `cloud/control-plane/
# Dockerfile` llevaba `FROM golang:1.25-alpine` — dos — sobre un módulo que declara
# `go 1.26.6`, y no casaba: no se contaba, no se comparaba, y el veredicto decía «5 builder
# pin(s) … all equal to 1.26.6» con este fichero fuera de los cinco. La guarda fail-closed
# de abajo tampoco lo veía, porque es de TODO o NADA: con cuatro pines legibles y uno
# invisible, sigue verde. Un gate dice lo que su MECANISMO DE DESCUBRIMIENTO alcanza, y el
# de éste alcanzaba una sola grafía de tres. Ahora ve las tres, y las dos nuevas son
# hallazgo por construcción: una etiqueta de dos componentes FLOTA (sigue el último parche
# de su serie sin que nadie lo decida) y una no numérica (`golang:alpine`, `golang:latest`)
# no es un pin en absoluto. Mutantes: `scripts/test-go-toolchain.sh`.
#
# 1.26.6: four Dockerfiles on `golang:1.26.5-bookworm`, the devcontainer on 1.26.4, and three
# workflows — release.yml among them — carrying `GO_VERSION: "1.26.5"` by hand while SEVENTEEN other
# call sites already derived theirs with `go-version-file: go.work`. Not one gate had anything to
# say, and there was no commit anywhere in the history where every site agreed.
#
# The workflows were repaired by DERIVING rather than by bumping — `go-version-file: go.work` cannot
# drift — which is why this rule finds none of them today and would find the next one immediately.
# A Dockerfile cannot derive: `FROM` takes a literal. So the literal is checked here instead.
#
# WHY IT IS NOT ENOUGH TO BUMP AND MOVE ON: `GOTOOLCHAIN` is unset across this tree, so `auto`
# applies and Go re-executes itself into the version the module demands. That makes a stale builder
# image FAIL rather than silently produce a 1.26.5 binary — good news, and precisely why nobody
# noticed for a week that the pins had diverged.
BUILD_FILES=$(printf '%s\n' "$CENSUS_ALL" | grep -E '(^|/)(Dockerfile[^/]*|devcontainer\.json)$|^\.github/workflows/[^/]+\.ya?ml$' || true)
BUILD_PINS=0
DOCKERFILES=$(printf '%s\n' "$BUILD_FILES" | grep -E '(^|/)Dockerfile[^/]*$' || true)
DOCKER_N=$(printf '%s' "$DOCKERFILES" | grep -c . || true)
while IFS= read -r bf; do
	[ -n "$bf" ] && [ -f "$bf" ] || continue
	while IFS='|' read -r kind pin; do
		[ -n "$pin" ] || continue
		BUILD_PINS=$((BUILD_PINS + 1))
		[ "$pin" = "$WS_GO" ] && continue
		FINDINGS=$((FINDINGS + 1))
		say "check-go-toolchain: BUILDER PINNED OFF THE WORKSPACE — $bf pins $pin via $kind while" >&2
		say "  go.work pins $WS_GO. Rules 1-3 stay green through this: they read go.mod files, and this" >&2
		say "  is the toolchain the artefact is actually BUILT with." >&2
		say "  repair: set it to $WS_GO — or, in a workflow, delete the literal and use" >&2
		say "  'go-version-file: go.work', which cannot drift at all." >&2
	done <<INNER
$(sed -nE 's/^FROM[[:space:]]+golang:([0-9]+\.[0-9]+\.[0-9]+).*/FROM golang|\1/p;
           s/^FROM[[:space:]]+golang:([0-9]+\.[0-9]+)([^0-9.].*)?$/FROM golang (two-part tag, which floats)|\1/p;
           s/^FROM[[:space:]]+golang:([^0-9][^[:space:]]*).*/FROM golang (tag with no version at all)|\1/p;
           s/.*features\/go:1"[^"]*"version"[[:space:]]*:[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/devcontainer feature|\1/p;
           s/^[[:space:]]*GO_VERSION:[[:space:]]*"?([0-9]+\.[0-9]+\.[0-9]+)"?[[:space:]]*$/GO_VERSION|\1/p;
           s/^[[:space:]]*go-version:[[:space:]]*"?([0-9]+\.[0-9]+\.[0-9]+)"?[[:space:]]*$/go-version literal|\1/p' "$bf")
INNER
done <<EOF
$BUILD_FILES
EOF

# Fail-closed on the extraction, not on the tree. A repository with no Dockerfile is legitimate; a
# repository WITH Dockerfiles where the FROM pattern matches nothing means the pattern stopped
# working — a base image renamed, a build arg introduced — and that reads as clean, which is the
# one answer this file never gives.
if [ "$DOCKER_N" -gt 0 ] && [ "$BUILD_PINS" -eq 0 ]; then
	cannot_look "found $DOCKER_N Dockerfile(s) but extracted no Go pin from any of them; the builder
  toolchain is UNKNOWN, not agreed. Check whether the base image moved off 'FROM golang:X.Y.Z'"
fi

# NOTE there is deliberately NO "OUTSIDE must be > 0" refusal here. A repository where every module
# is in the workspace is legitimate, and an absolute demand would be this gate asserting a fact about
# THIS tree as if it were a property of the script — the same overreach the rule above exists to
# correct. What must never happen is a count that goes unsaid, so the number is printed either way,
# zero included, next to where it came from.
if [ "$FINDINGS" -ne 0 ]; then
	say "check-go-toolchain: DIRTY — $FINDINGS finding(s) across $N workspace and $OUTSIDE non-workspace module(s)." >&2
	exit 1
fi
say "check-go-toolchain: CLEAN — $N workspace module(s), none carrying a redundant toolchain line,"
say "  none asking for a go version above the workspace's $WS_GO; and $OUTSIDE module(s) outside the"
say "  workspace (census via $CENSUS_SRC), none pinned below it; and $BUILD_PINS builder pin(s)"
say "  (Dockerfile FROM, devcontainer feature, workflow literal) all equal to $WS_GO."
exit 0
