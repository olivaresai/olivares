#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-go-toolchain.sh — battery for scripts/check-go-toolchain.sh.
#
# Both directions are pinned, and the NOT-catching one matters more here than usual: the rule is
# about redundancy INSIDE the workspace, so a module outside the `use` list must keep its toolchain
# line untouched. A gate that forbade it everywhere would delete the only pin a standalone module
# has, turning a cleanup into a regression. That case is the reason the membership test exists.
#
# Hermetic: every fixture is a throwaway directory, nothing is read from the repository under test.
set -uo pipefail
SUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-go-toolchain.sh"
PASS=0; FAIL=0; START=$SECONDS
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

mk() { # mk <name> <go.work body> ; then caller writes modules
	local d="$WORK/$1"; mkdir -p "$d"; printf '%s\n' "$2" > "$d/go.work"; printf '%s' "$d"
}
mod() { # mod <dir> <name> <go version> [toolchain]
	mkdir -p "$1/$2"
	{ printf 'module example.com/%s\n\ngo %s\n' "$2" "$3"
	  [ -n "${4:-}" ] && printf 'toolchain %s\n' "$4"; } > "$1/$2/go.mod"
}
run() { # run <dir> <want-rc> <label> [want-substring]
	local d="$1" want="$2" label="$3" want_txt="${4:-}" out rc
	out=$(OLIVARES_GO_WORK_ROOT="$d" bash "$SUT" 2>&1); rc=$?
	if [ "$rc" -ne "$want" ]; then
		FAIL=$((FAIL+1)); printf 'FAIL %-58s got rc=%d want %d\n     %s\n' "$label" "$rc" "$want" "$(printf '%s' "$out" | head -2 | tr '\n' ' ')"; return
	fi
	if [ -n "$want_txt" ]; then
		case "$out" in *"$want_txt"*) ;; *) FAIL=$((FAIL+1)); printf 'FAIL %-58s rc right, message never said: %s\n' "$label" "$want_txt"; return ;; esac
	fi
	PASS=$((PASS+1)); printf 'ok   %-58s rc=%d\n' "$label" "$rc"
}

WK=$'go 1.26.5\n\ntoolchain go1.26.5\n\nuse (\n\t./a\n\t./b\n)'

d=$(mk clean "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
run "$d" 0 "no toolchain lines in the workspace is CLEAN"

d=$(mk dup "$WK"); mod "$d" a 1.26.5 go1.26.5; mod "$d" b 1.26.5
run "$d" 1 "a workspace module carrying toolchain is caught" "REDUNDANT TOOLCHAIN"

d=$(mk both "$WK"); mod "$d" a 1.26.5 go1.26.5; mod "$d" b 1.26.5 go1.26.5
run "$d" 1 "two of them are two findings" "2 finding(s)"

# THE NOT-CATCHING DIRECTION, and the safety of the whole rule.
d=$(mk outside "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5; mod "$d" c 1.26.5 go1.26.5
run "$d" 0 "a module OUTSIDE the use list keeps its toolchain, untouched"

d=$(mk higher "$WK"); mod "$d" a 1.27.0; mod "$d" b 1.26.5
run "$d" 1 "a go directive above the workspace is caught" "ABOVE THE WORKSPACE"

# numeric, not lexical: 1.26.10 > 1.26.5 and must NOT be read as older.
WK10=$'go 1.26.10\n\nuse (\n\t./a\n)'
d=$(mk numeric "$WK10"); mod "$d" a 1.26.5
run "$d" 0 "1.26.5 under a 1.26.10 workspace is fine (numeric compare)"
d=$(mk numeric2 $'go 1.26.5\n\nuse (\n\t./a\n)'); mod "$d" a 1.26.10
run "$d" 1 "1.26.10 under a 1.26.5 workspace is caught (not string order)" "ABOVE THE WORKSPACE"

d=$(mk nogo "$WK"); mkdir -p "$d/a" "$d/b"; printf 'module example.com/a\n' > "$d/a/go.mod"; mod "$d" b 1.26.5
run "$d" 1 "a module with NO go directive is a finding" "NO GO DIRECTIVE"

# --- could not look ------------------------------------------------------------------------
d="$WORK/nowork"; mkdir -p "$d"
# The reason is asserted, not just the code: a later guard (go.work with no go directive) also
# exits 2, so "COULD NOT LOOK" alone cannot tell which one fired — and a mutant that removes THIS
# guard survived the loose version of this case.
run "$d" 2 "no go.work is COULD NOT LOOK, and says WHY" "no go.work at"

d=$(mk emptyuse $'go 1.26.5\n\nuse (\n)')
run "$d" 2 "an EMPTY use list is COULD NOT LOOK, not zero findings" "zero modules"

d=$(mk missing "$WK"); mod "$d" a 1.26.5
run "$d" 2 "a use-listed module absent from disk is COULD NOT LOOK" "does not exist"

d=$(mk nogoline $'use (\n\t./a\n)'); mod "$d" a 1.26.5
run "$d" 2 "go.work with no go directive is COULD NOT LOOK" "UNKNOWN"

# --- THE THIRD RULE: the modules go.work does NOT list -------------------------------------------
#
# Added with the rule itself, 2026-08-14. Before it, the gate answered "CLEAN — 11 workspace
# module(s)" over a tree of twenty-two, and the eleven it never opened were the ones that stayed on
# an older toolchain while seven CALLED standard-library vulnerabilities blocked every code PR.
#
# The first case below is the one that reproduces that morning exactly.

d=$(mk below "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5; mod "$d" c 1.26.4
run "$d" 1 "an OUTSIDE module pinned below the workspace is caught" "PIN BELOW THE WORKSPACE"

d=$(mk belowname "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5; mod "$d" c 1.26.4
run "$d" 1 "and the refusal NAMES the module, not just the count" "c/go.mod"

# The EFFECTIVE pin, not the go line: outside the workspace a toolchain directive is the one that
# applies, so a low go line under a sufficient toolchain is NOT a finding. Getting this backwards
# would red every standalone module that pins conservatively and builds new.
d=$(mk efftc "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5; mod "$d" c 1.26.0 go1.26.5
run "$d" 0 "an OUTSIDE module low on 'go' but pinned by 'toolchain' is clean"

# And the counterfactual for that same case, so the pass above is not just a permissive path: the
# SAME low go line with the toolchain line REMOVED must be caught.
d=$(mk efftc2 "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5; mod "$d" c 1.26.0
run "$d" 1 "the same module WITHOUT the toolchain line is caught" "PIN BELOW"

d=$(mk nopin "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
mkdir -p "$d/c"; printf 'module example.com/c\n' > "$d/c/go.mod"
run "$d" 1 "an OUTSIDE module with NO pin at all is a finding" "NO PIN OUTSIDE THE WORKSPACE"

# The count has to be SAID. A gate that examines a set and does not name its size is how "eleven,
# always eleven" survived: the number was right about what it measured and silent about what it did not.
d=$(mk counted "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5; mod "$d" c 1.26.5
run "$d" 0 "the clean verdict names how many modules were outside" "1 module(s) outside"

# A repository where everything is in the workspace is legitimate and must not be a refusal.
d=$(mk allinside "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
run "$d" 0 "zero modules outside the workspace is CLEAN, not COULD NOT LOOK" "0 module(s) outside"

# ── RULE 4: the toolchain the BUILDERS use ─────────────────────────────────────────────────────
# Every case above passes with rule 4 present or absent, which is exactly how the real defect got
# in: rules 1-3 read go.mod files and were CLEAN on 2026-08-15 while four Dockerfiles, the
# devcontainer and three workflows — release.yml included — built with go1.26.5 under a 1.26.6
# workspace. A module gate cannot see a base image.

# The clean half FIRST, because a rule that hates every builder pin would pass the red cases below
# and be useless.
d=$(mk builder_ok "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
printf 'FROM golang:1.26.5-bookworm AS build\n' > "$d/Dockerfile"
run "$d" 0 "a builder image equal to the workspace is CLEAN" "1 builder pin(s)"

d=$(mk builder_stale "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
printf 'FROM golang:1.26.4-bookworm AS build\n' > "$d/Dockerfile"
run "$d" 1 "a builder image BELOW the workspace is caught and named" "Dockerfile pins 1.26.4 via FROM golang"

d=$(mk builder_devcontainer "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
mkdir -p "$d/.devcontainer"
printf '{ "features": { "ghcr.io/devcontainers/features/go:1": { "version": "1.26.4" } } }\n' > "$d/.devcontainer/devcontainer.json"
run "$d" 1 "a devcontainer feature off the workspace is caught" "via devcontainer feature"

# The workflow half. The repair for these was to DERIVE (go-version-file: go.work), so the rule must
# still catch the literal that someone writes back by hand.
d=$(mk builder_workflow "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
mkdir -p "$d/.github/workflows"
printf 'jobs:\n  x:\n    steps:\n      - uses: actions/setup-go@v5\n        with:\n          go-version: "1.26.4"\n' > "$d/.github/workflows/release.yml"
run "$d" 1 "a hand-written go-version literal in a workflow is caught" "via go-version literal"

d=$(mk builder_derives "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
mkdir -p "$d/.github/workflows"
printf 'jobs:\n  x:\n    steps:\n      - uses: actions/setup-go@v5\n        with:\n          go-version-file: go.work\n' > "$d/.github/workflows/release.yml"
run "$d" 0 "go-version-file: go.work is not a pin and must not be graded" "0 builder pin(s)"

# ⛔ LAS DOS GRAFÍAS QUE EL EXTRACTOR NO VEÍA, y una de ellas escondía un defecto real:
# `cloud/control-plane/Dockerfile` llevaba `FROM golang:1.25-alpine` sobre un módulo que
# declara `go 1.26.6`, y el gate contaba «5 builder pin(s) … all equal» sin ese fichero
# dentro. No es un caso teórico: es el defecto con su fecha (2026-08-27).
d=$(mk builder_two_part "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
printf 'FROM golang:1.25-alpine AS build\n' > "$d/Dockerfile"
run "$d" 1 "a TWO-part golang tag is a finding, not an invisible pin" "two-part tag, which floats"

# Y la peor de todas, porque no se puede comparar con nada: una etiqueta sin versión. El
# fallo anterior no era «la comparaba mal» — es que no la contaba, así que el recuento de
# pines mentía por omisión.
d=$(mk builder_no_version "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
printf 'FROM golang:alpine AS build\n' > "$d/Dockerfile"
run "$d" 1 "a golang tag with no version at all is a finding" "tag with no version at all"

# NO DISPARO: la grafía de tres componentes correcta no puede caer en las reglas nuevas ni
# contarse dos veces. Si contara doble, el recuento de pines dejaría de ser un recuento.
d=$(mk builder_three_part_once "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
printf 'FROM golang:1.26.5-bookworm AS build\n' > "$d/Dockerfile"
run "$d" 0 "a correct three-part tag matches ONE rule, not two" "1 builder pin(s)"

# THE THIRD ANSWER, and the one that keeps this rule honest as the tree changes: a Dockerfile whose
# base image moved off `FROM golang:` yields no pin, and a rule that reported that as agreement
# would be certifying a builder toolchain it never read.
d=$(mk builder_unreadable "$WK"); mod "$d" a 1.26.5; mod "$d" b 1.26.5
printf 'ARG GO_IMAGE\nFROM ${GO_IMAGE} AS build\n' > "$d/Dockerfile"
run "$d" 2 "a Dockerfile with no readable Go pin is COULD NOT LOOK" "toolchain is UNKNOWN, not agreed"

printf '\ngo-toolchain: %d passed, %d failed, %ds\n' "$PASS" "$FAIL" "$((SECONDS-START))"
[ "$FAIL" -eq 0 ] || exit 1
