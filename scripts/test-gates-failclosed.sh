#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# One property, checked across every gate that leans on an external tool:
#
#   A GATE THAT COULD NOT RUN MUST REFUSE, NOT APPROVE.
#
# WHY THIS EXISTS. On 2026-08-01 an audit of the 77 gate scripts found this property
# violated in the two gates with the most to lose, and in both cases the gate said the
# reassuring thing. Measured, literally:
#
#   $ GOVULNCHECK=/bin/false bash scripts/govulncheck-gate.sh; echo "exit=$?"
#   vuln:gate: no called vulnerabilities across the workspace.
#   exit=0
#
#   $ PATH=/usr/bin:/bin bash scripts/check-boundary.sh   # no `go` on PATH
#   ==> sdk: must not import github.com/olivaresai/olivares/core
#       (no packages)
#   ...
#   Boundary check OK
#
# The first is the release-blocking dependency-vulnerability gate — the one that on the
# same day correctly caught GO-2026-6061 in grpc. The second is the AGPL/Apache licence
# frontier, where a miss is a licensing defect rather than a style nit. Neither was
# broken in a way anybody would notice: both fail open only when something ELSE is
# already wrong, which is the moment a gate is most load-bearing and least watched.
#
# Each row breaks exactly one tool and asserts a NON-ZERO exit AND a diagnostic that
# names the problem. Exit code alone is not enough: these gates also exit non-zero on a
# genuine finding, and "refused for the wrong reason" is a regression wearing a red coat.
#
# Rows are added here as the remaining gates from that audit are fixed. A gate whose
# tool-failure behaviour is not asserted here has not been shown to fail closed.
#
# Usage: test-gates-failclosed.sh [fast|export|all]      (default: fast)
#
# The rows are grouped by what they COST. `fast` is wired into lint:boundary and runs in
# under a second.
#
# `export` is NOT WIRED ANYWHERE YET, and that is a deliberate, stated gap rather than an
# oversight. Its single row re-runs export-public.sh --check, which copies the curated
# tree and builds a Go helper: measured ~290s here. lint:export already pays that once
# per push, and hanging this off it would double the cost of the pre-push gate — which is
# already the subject of an open decision about how much it should cost. So the row
# exists, passes, and is run on demand (`scripts/test-gates-failclosed.sh export`) until
# that decision lands and it can be placed in CI rather than in every push.
set -uo pipefail

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

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

GROUP="${1:-fast}"
case "$GROUP" in
fast | export | all) ;;
*)
	echo "usage: $0 [fast|export|all]" >&2
	exit 64
	;;
esac
want() { [ "$GROUP" = "all" ] || [ "$GROUP" = "$1" ]; }

pass=0
fail=0
skip=0

# A directory holding a stub that always fails, for gates that resolve a tool by name off
# PATH. It has to satisfy one property that no path can be assumed to have: execve must
# work there. The dev container mounts /tmp noexec, so a stub there dies with EACCES and
# `command -v` skips it — the row would then pass for the wrong reason. But hardcoding
# the container's own scratch path is not the answer either: this file went into CI as
# `/workspace/.olivares-tmptest/...` and the runner does not have a /workspace at all,
# which killed the whole `license boundary` step with
#   mkdir: cannot create directory '/workspace': Permission denied
# — after check-boundary.sh itself had already passed. A developer path baked into a gate
# is a gate that only works on one machine.
#
# So the directory is DETECTED: each candidate is created, given a real script, and the
# script is EXECUTED. Only a location that actually runs it is used.
pick_exec_dir() {
	local base d
	for base in "${RUNNER_TEMP:-}" "${TMPDIR:-}" /workspace/.olivares-tmptest /tmp; do
		[ -n "$base" ] || continue
		mkdir -p "$base" 2>/dev/null || continue
		d="$(mktemp -d "$base/failclosed.XXXXXX" 2>/dev/null)" || continue
		if printf '#!/bin/sh\nexit 0\n' >"$d/probe" 2>/dev/null &&
			chmod +x "$d/probe" 2>/dev/null && "$d/probe" 2>/dev/null; then
			rm -f "$d/probe"
			printf '%s' "$d"
			return 0
		fi
		rm -rf "$d"
	done
	return 1
}

STUBS="$(pick_exec_dir)" || {
	echo "test-gates-failclosed: found no directory that permits execve (tried RUNNER_TEMP," >&2
	echo "  TMPDIR, /workspace/.olivares-tmptest, /tmp). The stubs cannot be installed." >&2
	exit 2
}
cleanup() { rm -rf "$STUBS"; }
trap cleanup EXIT HUP INT TERM

row() { # row <name> <expected-substring> -- <command...>
	local name="$1" want="$2"
	shift 3 # name, want, and the literal --
	local out rc=0
	out="$("$@" 2>&1)" || rc=$?
	if [ "$rc" -eq 0 ]; then
		printf 'FAIL  %s: exited 0 — the gate approved without being able to check\n' "$name"
		printf '%s\n' "$out" | sed 's/^/        /' | head -8
		fail=$((fail + 1))
		return
	fi
	case "$out" in
	*"$want"*)
		printf 'ok    %s (exit %s, said %q)\n' "$name" "$rc" "$want"
		pass=$((pass + 1))
		;;
	*)
		printf 'FAIL  %s: exit %s but never said %q — refused for an unnamed reason\n' \
			"$name" "$rc" "$want"
		printf '%s\n' "$out" | sed 's/^/        /' | head -8
		fail=$((fail + 1))
		;;
	esac
}

# Two rows below assert the fail-closed behaviour of the two EXPORT GENERATORS, and those
# generators are the one thing this battery tests that the public export does not contain:
# scripts/export-public.sh curates both of them out (SCRIPTS_BLOCK), while shipping this
# file. Until 2026-08-02 the rows ran unconditionally, so in the exported tree the battery
# invoked a path the export had just removed and the row failed with
#   exit 127 but never said "UNVERIFIED" — refused for an unnamed reason
# taking `task lint:boundary` — the FIRST acceptance command the export prescribes to its
# own reader — down with it (audit F10). That is this file's own rule violated by this
# file: a gate that could not look must SAY SO, not die namelessly.
#
# So the absent generator is answered the way scripts/private-leg.sh answers an absent
# private directory, with three outcomes and no silent one:
#   present                          -> run the row
#   absent + tree classified public  -> skip, NAMING the script and the reason (below)
#   absent + anything else           -> FAIL: this tree is neither a complete hub nor a
#                                       stamped export, and guessing is how a battery
#                                       reports green against nothing.
# scripts/check-export-closure.sh enforces the other half — that a path declared hub-only
# here really is absent from the export and really does exist in the hub.
#
# The classification is scripts/hub-leg.sh's, deliberately NOT a local `[ -f PUBLIC-EXPORT.md ]`.
# That single-file test was a password anybody can type: measured 2026-08-02, an EMPTY file
# of that name — or a marker copied into a hub that had lost the generator — bought a green
# skip here and in the wrapper (adversarial review X-07). hub-leg checks the marker's own
# signature AND the absence of every hub-only sentinel, and both files ship, so the public
# tree keeps one discriminator instead of two that can drift apart.
skip_hub_only() { # skip_hub_only <script-path> <row-name> — answers 2 and 3 above
	local leg="$ROOT/scripts/hub-leg.sh" tree="" rc=0
	if [ ! -f "$leg" ]; then
		printf 'FAIL  %s: %s is MISSING and so is scripts/hub-leg.sh — nothing here can tell a\n' \
			"$2" "$1"
		printf '        curated public tree from a broken one; refusing to guess\n'
		fail=$((fail + 1))
		return
	fi
	tree="$(bash "$leg" --classify 2>/dev/null)" || rc=$?
	if [ "$rc" -eq 0 ] && [ "$tree" = "public" ]; then
		printf 'skip  %s: %s is hub-only tooling, curated out of this tree (PUBLIC-EXPORT.md);\n' \
			"$2" "$1"
		printf '        the gate it asserts does not exist here, so there is nothing to assert\n'
		skip=$((skip + 1))
	else
		printf 'FAIL  %s: %s is MISSING and this tree is not a stamped public export\n' "$2" "$1"
		printf '        (hub-leg --classify said %s, exit %s) — refusing to guess\n' \
			"${tree:-nothing}" "$rc"
		fail=$((fail + 1))
	fi
}

# --- the dependency-vulnerability gate, with govulncheck unable to run --------------
if want fast; then
	row "govulncheck-gate refuses when govulncheck cannot run" \
		"the gate certifies nothing" -- \
		env GOVULNCHECK=/bin/false bash "$ROOT/scripts/govulncheck-gate.sh"
fi

# --- the licence-boundary gate, with the Go toolchain off PATH ----------------------
if want fast; then
	row "check-boundary refuses when the Go toolchain is absent" \
		"UNVERIFIED" -- \
		env PATH=/usr/bin:/bin bash "$ROOT/scripts/check-boundary.sh"
fi

# --- the SAST gate, with gosec absent ------------------------------------------------
# scripts/test-sast-gate.sh owns the full matrix; this row keeps the family together so
# a reader of THIS file sees every tool-dependent gate in one place.
if want fast; then
	row "sast gate refuses when gosec is absent" \
		"gosec not found" -- \
		env GOSEC=/nonexistent/gosec PATH=/usr/bin:/bin bash "$ROOT/scripts/sast.sh"
fi

# --- gates whose tool is grep, broken to exit 2 (a build without PCRE, an unreadable
# --- file mid-walk). grep's 2 is an ERROR; folding it into its 1 (no matches) is what
# --- made these gates answer "nothing found" to the question "could you look?".
printf '#!/bin/sh\necho "grep: -P not supported in this build" >&2\nexit 2\n' >"$STUBS/grep"
chmod +x "$STUBS/grep"

if want fast; then
	row "docs-honesty refuses when grep cannot run" \
		"UNVERIFIED" -- \
		env PATH="$STUBS:$PATH" sh "$ROOT/scripts/check-docs-honesty.sh"

	row "the doc-vocabulary sweep refuses when grep cannot run" \
		"UNVERIFIED" -- \
		env PATH="$STUBS:$PATH" bash "$ROOT/scripts/check-format-docs.sh"

	# The provider export has NO scrubber and no second pass behind its one leak scan,
	# so a tool failure there publishes an unexamined tree.
	#
	# export-closure: hub-only scripts/export-provider.sh — the provider export generator
	# cannot ship AS IT IS. Its own leak gate IS a literal denylist of the private identity
	# and dev-process tokens the export forbids, and its prose names the maintainer the same
	# way; measured 2026-08-02, the export's check #6 matches that script on four lines, and
	# running the real comment scrubber on a copy left two of them: an executable denylist
	# and an executable message. Rewriting the first would mutate a security tool's own
	# pattern — a scrubbed leak regex is a broken leak gate. (This very comment was measured
	# tripping the same check when it quoted one of those tokens: the class is real, not
	# theoretical.) What is NOT claimed here, because it was refuted on 2026-08-02: that the
	# generator would be meaningless outside the hub. The public manifest ships the provider
	# subtree and its inputs, so its `git ls-files` mechanism has the same material there; a
	# deliberately public-safe variant is possible. Curating THIS file out is the scoped fix
	# — "not safely publishable unchanged" is measured, "impossible elsewhere" is not.
	if [ -f "$ROOT/scripts/export-provider.sh" ]; then
		row "the provider-export leak gate refuses when grep cannot run" \
			"UNVERIFIED" -- \
			env PATH="$STUBS:$PATH" TMPDIR="$STUBS" \
			bash "$ROOT/scripts/export-provider.sh" --check
	else
		skip_hub_only scripts/export-provider.sh "the provider-export leak gate"
	fi
fi

# --- the fuzz inventory, with one of its source trees gone. Not a broken tool: a broken
# --- PREMISE. grep still succeeds and still returns the survivors, so the count shrinks
# --- with nothing to notice it.
if want fast; then
	FZCASE="$STUBS/fuzzcase"
	mkdir -p "$FZCASE/scripts"
	cp "$ROOT/scripts/fuzz-smoke.sh" "$FZCASE/scripts/"
	for d in cmd core modules connectors operator clients terraform-provider-olivares cloud; do
		mkdir -p "$FZCASE/$d"
	done # `sdk` deliberately absent — `cloud` is a SANCTIONED absence in the exported
	# tree (the gate skips it loudly), so it can no longer carry this fixture
	row "the fuzz inventory refuses when a source tree has vanished" \
		"is missing" -- \
		sh -c "cd '$FZCASE' && bash scripts/fuzz-smoke.sh"

	# The public-export MARKER family (S-O audit B2): the SAME absence must be fatal in
	# the hub and a loud skip in a marked export — both directions proven, every run.
	FZHUB="$STUBS/fzhub"
	mkdir -p "$FZHUB/scripts"
	cp "$ROOT/scripts/fuzz-smoke.sh" "$FZHUB/scripts/"
	for d in cmd core modules connectors sdk operator clients terraform-provider-olivares; do
		mkdir -p "$FZHUB/$d"
	done # cloud absent, NO marker -> must refuse
	row "a curated-out tree missing WITHOUT the export marker refuses" \
		"no public-export marker" -- \
		sh -c "cd '$FZHUB' && bash scripts/fuzz-smoke.sh"
	FZPUB="$STUBS/fzpub"
	cp -r "$FZHUB" "$FZPUB"
	printf 'olivares-public-export-marker v1\n' >"$FZPUB/.olivares-public-export"
	if out="$(cd "$FZPUB" && bash scripts/fuzz-smoke.sh 2>&1)"; then
		: # fuzz-smoke may exit 0 or nonzero here (no fuzz targets); only the skip wording matters
	fi
	case "$out" in
	*"curated public export"*)
		printf 'ok    the same absence WITH the export marker skips loudly\n'
		pass=$((pass + 1))
		;;
	*)
		printf 'FAIL  the same absence WITH the export marker did not skip loudly\n'
		printf '%s\n' "$out" | sed 's/^/        /' | head -6
		fail=$((fail + 1))
		;;
	esac

	# Same shape, different gate: the verifier-truth sweep used to iterate
	# `for f in $(find core modules … cloud …)`, and set -e does not reach into a
	# for-list command substitution, so a renamed root emptied the list and the gate
	# vouched for every assurance verifier having read none.
	VTCASE="$STUBS/vtcase"
	mkdir -p "$VTCASE/scripts"
	cp "$ROOT/scripts/check-verifier-truth.sh" "$VTCASE/scripts/"
	for d in core modules connectors cmd cloud; do mkdir -p "$VTCASE/$d"; done
	# `sdk` deliberately absent — `cloud` is the one sanctioned absence (exported tree)
	# and no longer refuses, so it cannot carry this fixture
	row "the verifier-truth sweep refuses when a source root has vanished" \
		"is missing" -- \
		sh -c "cd '$VTCASE' && bash scripts/check-verifier-truth.sh"
fi

# export-public builds a Go helper into TMPDIR, so TMPDIR must be an exec mount. This
# row copies the entire curated tree and compiles that helper: ~4.5 minutes, which is
# why it lives in the `export` group next to lint:export rather than in the pre-push gate.
#
# export-closure: hub-only scripts/export-public.sh — the export generator itself. Same
# reason as export-provider.sh above and one stronger: this file IS the curation (the
# allow/block lists name every private directory) and it carries the leak-gate patterns.
# Nothing in the public tree can produce a public tree.
if want export; then
	if [ -f "$ROOT/scripts/export-public.sh" ]; then
		row "the public-export leak gate refuses when grep cannot run" \
			"UNVERIFIED" -- \
			env PATH="$STUBS:$PATH" TMPDIR="$STUBS" \
			bash "$ROOT/scripts/export-public.sh" --check
	else
		skip_hub_only scripts/export-public.sh "the public-export leak gate"
	fi
fi

# --- the migration linter, whose tool is `cat` and whose input is a file it may not be
# --- able to read. A miniature repo, because the gate walks its own root.
if want fast; then
	MIGCASE="$STUBS/migcase"
	mkdir -p "$MIGCASE/scripts" "$MIGCASE/modules/x"
	cp "$ROOT/scripts/check-migrations.sh" "$MIGCASE/scripts/"
	mkdir -p "$MIGCASE/modules/x/migrations"
	printf 'ALTER TABLE users DROP COLUMN email;\n' >"$MIGCASE/modules/x/migrations/0001_expand_thing.sql"
	# ⛔ THE SANDBOX IS A GIT REPO NOW, BECAUSE THE LINTER'S SUBJECT IS THE INDEX.
	# It used to build its census with a raw `find .`, so a planted file in a bare directory was
	# enough. That census graded untracked scratch trees — 96 files from .claude/worktrees and 12
	# from a directory another gate creates mid-run — and the fix was to read `git ls-files`. A
	# double that plants an UNTRACKED file therefore stopped modelling the thing it tests: the
	# linter cannot see it, and the case would pass or fail for the wrong reason. Planted AND
	# added, so the sandbox is the shape the gate actually reads.
	( cd "$MIGCASE" && git init -q . && git add modules/x/migrations/0001_expand_thing.sql ) >/dev/null 2>&1
	# Control: readable, the destructive statement is caught and named.
	control_out="$(cd "$MIGCASE" && bash scripts/check-migrations.sh 2>&1)" || true
	case "$control_out" in
	*"DESTRUCTIVE statement"*)
		printf 'ok    migration linter catches a destructive expand migration (control)\n'
		pass=$((pass + 1))
		;;
	*)
		printf 'FAIL  migration linter control did not catch the destructive statement\n'
		printf '%s\n' "$control_out" | sed 's/^/        /' | head -6
		fail=$((fail + 1))
		;;
	esac
	# ⛔ ESTA CASILLA SE APOYA EN QUE `chmod 000` HAGA ILEGIBLE EL FICHERO, y eso es FALSO para
	# root: el kernel le salta la comprobacion de permisos. Medido el 2026-08-19 en el job
	# `control-plane`, cuyo servicio de runner corre con HOME=/root — el mismo log lo dice. Ahi el
	# linter LEE el fichero, encuentra la sentencia destructiva que la casilla de control acaba de
	# plantar, y sale 1 con un hallazgo legitimo. La casilla lo leia como
	#
	#   «exit 1 pero nunca dijo UNVERIFIED — refused for an unnamed reason»
	#
	# es decir, acusaba al gate de refusar mal cuando el gate hizo exactamente lo correcto: la
	# premisa del TEST era la que no se cumplia.
	#
	# No se veia antes porque este guion moria unas lineas mas arriba con «HOME: unbound
	# variable»; al arreglar aquello, esto quedo al descubierto. Subir un limite o quitar un
	# aborto no oculta el siguiente problema: lo ENSEÑA.
	if [ "$(id -u)" = "0" ]; then
		printf 'skip  %s: corriendo como root, y root ignora chmod 000 —\n' \
			"migration linter refuses when a migration cannot be read"
		printf '        la premisa de esta casilla no se cumple aqui, asi que no mide nada.\n'
		printf '        Ejecutala sin privilegios para ejercitar el camino UNVERIFIED.\n'
		skip=$((skip + 1))
	else
		chmod 000 "$MIGCASE/modules/x/migrations/0001_expand_thing.sql"
		row "migration linter refuses when a migration cannot be read" \
			"UNVERIFIED" -- \
			env HOME="${HOME:-$MIGCASE}" sh -c "cd '$MIGCASE' && bash scripts/check-migrations.sh"
		chmod 644 "$MIGCASE/modules/x/migrations/0001_expand_thing.sql"
	fi
fi

printf '\ntest-gates-failclosed[%s]: %d passed, %d failed, %d skipped\n' \
	"$GROUP" "$pass" "$fail" "$skip"
[ "$fail" -eq 0 ]
