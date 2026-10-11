#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-check-cli-render.sh — the battery of check-cli-render.sh.
#
# Every case is HERMETIC: a decoy tree in a temporary directory with its own package and its
# own baseline. Nothing here reads the real `cmd/olivares`, so the battery says the same thing
# on the day somebody fixes the last hand-formatted path as it says today.
#
# Each case is stated in BOTH directions where a direction exists — the pattern present and
# absent, the row at the count and off it. A case that only ever sees the failing side cannot
# tell "the rule fired" from "the script always fires".
#
# 0 every case as expected · 1 a case disagreed · 2 the battery could not run.
set -u -o pipefail
LC_ALL=C
export LC_ALL

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
GATE="$ROOT/scripts/check-cli-render.sh"
[ -x "$GATE" ] || { printf 'test-check-cli-render: COULD NOT CHECK: cannot execute %s\n' "$GATE" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { printf 'test-check-cli-render: COULD NOT CHECK: python3 is unavailable\n' >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/cli-render-battery.XXXXXX")" || {
	printf 'test-check-cli-render: COULD NOT CHECK: could not create the working directory\n' >&2; exit 2; }
trap 'rm -rf "$WORK"' EXIT

FAILS=0
CASES=0

note() { printf '  %s\n' "$*"; }

# decoy <name> builds a tree whose package clears the population floor and whose files are
# all trivially clean. The caller then adds exactly the line the case is about.
decoy() {
	local dir="$WORK/$1"
	rm -rf "$dir"
	mkdir -p "$dir/cmd/olivares/internal/termrender" "$dir/cmd/olivares/internal/toolinstall" "$dir/docs"
	local i
	for i in $(seq 1 205); do
		printf 'package main\n\nfunc filler%d() string { return "nothing to format" }\n' "$i" \
			> "$dir/cmd/olivares/filler_$i.go"
	done
	printf '%s\n' "$dir"
}

# run <dir> <args...> — prints rc on the first line and the combined output after it.
run() {
	local dir="$1"; shift
	local out rc
	out="$(cd "$dir" && OLIVARES_ROOT="$dir" "$GATE" "$@" 2>&1)"
	rc=$?
	printf '%d\n%s\n' "$rc" "$out"
}

expect() {
	local label="$1" want_rc="$2" dir="$3"; shift 3
	local want_text="$1"; shift
	CASES=$((CASES + 1))
	local got rc body
	got="$(run "$dir" "$@")"
	rc="$(printf '%s' "$got" | head -1)"
	body="$(printf '%s' "$got" | tail -n +2)"
	if [ "$rc" != "$want_rc" ]; then
		printf 'FAIL %s: expected rc %s, got %s\n' "$label" "$want_rc" "$rc" >&2
		printf '%s\n' "$body" | sed 's/^/    /' >&2
		FAILS=$((FAILS + 1))
		return
	fi
	if [ -n "$want_text" ] && ! command grep -qF -- "$want_text" <<<"$body"; then
		printf 'FAIL %s: correct rc %s, but output does not name %q\n' "$label" "$rc" "$want_text" >&2
		printf '%s\n' "$body" | sed 's/^/    /' >&2
		FAILS=$((FAILS + 1))
		return
	fi
	note "ok   $label (rc $rc)"
}

baseline_row() { printf '%s\t%s\t%s\n' "$1" "$2" "$3"; }

# --- 1. a clean tree with an empty baseline is clean -------------------------------------
D="$(decoy clean)"
printf '# no file hand-formats\n' > "$D/docs/cli-render-baseline.txt"
expect "clean tree" 0 "$D" "CLEAN" --gate

# --- 2. a NEW hand-formatted table, in a file the baseline does not name ------------------
D="$(decoy newtable)"
printf '# no file hand-formats\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	"text/tabwriter"
)

func printThing(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	return tw.Flush()
}
GO
expect "new table without a row" 1 "$D" "cmd/olivares/cmd_thing.go" --gate
expect "new table: reports NEW" 1 "$D" "NEW" --gate

# --- 2-bis. the SAME command through the renderer is not a finding ------------------------
# The other direction of case 2: it is the hand-formatting that fires, not the file.
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

func printThing(w io.Writer) error {
	termrender.New(w, termrender.Options{}).Table(thingTable())
	return nil
}
GO
expect "the same output through the renderer" 0 "$D" "CLEAN" --gate

# --- 3. a file OVER its row ---------------------------------------------------------------
D="$(decoy over)"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	"text/tabwriter"
)

func a(w io.Writer) { tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0); _ = tw }
func b(w io.Writer) { tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0); _ = tw }
GO
baseline_row 1 cmd/olivares/cmd_thing.go "one block left, the second is new" \
	> "$D/docs/cli-render-baseline.txt"
expect "above its row" 1 "$D" "OVER" --gate

# --- 3-bis. the ratchet HOLDS when the count matches its row -------------------------------
baseline_row 2 cmd/olivares/cmd_thing.go "two column blocks, both awaiting the renderer" \
	> "$D/docs/cli-render-baseline.txt"
expect "the ratchet holds" 0 "$D" "CLEAN" --gate

# --- 4. the ratchet RISES: fewer paths than the row, and the row must come down ------------
baseline_row 5 cmd/olivares/cmd_thing.go "five column blocks awaiting the renderer" \
	> "$D/docs/cli-render-baseline.txt"
expect "the ratchet rises" 1 "$D" "UNDER" --gate
expect "and reports the value to lower to" 1 "$D" "lower the row to 2" --gate

# --- 5. no baseline is NOT a clean tree ----------------------------------------------------
D="$(decoy nobaseline)"
expect "without a baseline" 2 "$D" "COULD NOT CHECK" --gate

# --- 6. a row without a reason is a number, not a decision ---------------------------------
D="$(decoy noreason)"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	"text/tabwriter"
)

func a(w io.Writer) { tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0); _ = tw }
GO
printf '1\tcmd/olivares/cmd_thing.go\n' > "$D/docs/cli-render-baseline.txt"
expect "row without a reason" 2 "$D" "unreadable" --gate
baseline_row 1 cmd/olivares/cmd_thing.go "one block awaiting the renderer" \
	> "$D/docs/cli-render-baseline.txt"
expect "the same row with a reason" 0 "$D" "CLEAN" --gate

# --- 7. a glob that stopped seeing the package cannot declare it clean ----------------------
D="$(decoy floor)"
printf '# empty\n' > "$D/docs/cli-render-baseline.txt"
rm -f "$D"/cmd/olivares/filler_2*.go "$D"/cmd/olivares/filler_1*.go
expect "population floor" 2 "$D" "COULD NOT CHECK" --gate

# --- 8. an unknown mode is not silently the default ----------------------------------------
D="$(decoy unknownflag)"
printf '# empty\n' > "$D/docs/cli-render-baseline.txt"
expect "unknown flag" 2 "$D" "unknown option" --census-please

# --- 9. a comment is not an output path ----------------------------------------------------
D="$(decoy comment)"
printf '# empty\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

// This used to call tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) and print "Next: do the thing"
// with a [ok] marker and a %-12s column. None of that is an output path any more.
func printThing(w io.Writer) { termrender.New(w, termrender.Options{}).Line("done") }
GO
expect "a comment is not an output path" 0 "$D" "CLEAN" --gate

# --- 10. the renderer's own package is the answer, not a finding ----------------------------
D="$(decoy renderer)"
printf '# empty\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/internal/termrender/termrender.go" <<'GO'
package termrender

import "text/tabwriter"

func (r *Renderer) Table(t Table) {
	tw := tabwriter.NewWriter(r.w, 0, 0, 2, ' ', 0)
	_ = tw
}
GO
expect "the renderer itself is not counted" 0 "$D" "CLEAN" --gate

# --- 11. a status in PARENTHESES is the shape this repository already chose -----------------
D="$(decoy parenthetical)"
printf '# empty\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

func describe(status int, detail, code string) string {
	return fmt.Sprintf("the engine rejected this request: %s (HTTP %d %s)", detail, status, code)
}

func remedy() string { return "inspect service logs; this endpoint must return HTTP 200" }
GO
expect "parenthesized status is not an envelope" 0 "$D" "CLEAN" --gate

# --- 11-bis. the same status pasted in front of the body IS ---------------------------------
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

func fail(status int, body []byte) error {
	return fmt.Errorf("request failed: HTTP %d: %s", status, body)
}
GO
expect "a transport envelope is counted" 1 "$D" "envelope=1" --gate

# --- 12. a typed refusal under internal/ is data, not an operator's sentence ------------------
D="$(decoy internalrefusal)"
printf '# empty\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/internal/toolinstall/fetch.go" <<'GO'
package toolinstall

func get(u string, status int) error {
	return refuse(KindTransport, "GET %s: HTTP %d: %s", u, status, "body")
}
GO
expect "typed refusal in internal/" 0 "$D" "CLEAN" --gate

# --- 13. --census prints the table, with its header and its rows ------------------------------
D="$(decoy census)"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"fmt"
	"io"
	"text/tabwriter"
)

func a(w io.Writer) { tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0); _ = tw }
func b(w io.Writer) { fmt.Fprintln(w, "Next: olivares thing do") }
GO
expect "census: header" 0 "$D" "# file	paths	kinds	lines" --census
expect "census: the row and its kinds" 0 "$D" "cmd/olivares/cmd_thing.go	2	next=1,table=1" --census

# --- 14. no package is not an empty package ---------------------------------------------------
D="$(decoy nopackage)"
printf '# empty\n' > "$D/docs/cli-render-baseline.txt"
rm -rf "$D/cmd/olivares"
expect "without a package" 2 "$D" "missing cmd/olivares" --gate

# --- 15. a baseline row for a file that no longer hand-formats still has to come down ----------
D="$(decoy resolved)"
baseline_row 3 cmd/olivares/cmd_gone.go "three blocks awaiting the renderer" \
	> "$D/docs/cli-render-baseline.txt"
expect "row for an already clean file" 1 "$D" "lower the row to 0" --gate

# --- 16. A9: a table built through a LOCAL FACTORY is a hand-built table ---------------------
# The case measured on 2026-09-18: this tree builds its tables through
# `newTabWriter(out)`, and a rule that knew only `tabwriter.NewWriter(` left a NEW one green.
D="$(decoy factory)"
printf '# no file hand-formats\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"fmt"
	"io"
	"text/tabwriter"
)

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}

func printThing(w io.Writer) error {
	tw := newTabWriter(w)
	fmt.Fprintln(tw, "NAME\tSTATE")
	return tw.Flush()
}
GO
expect "new table through the local factory" 1 "$D" "NEW" --gate
expect "and counts the call as well as the constructor" 1 "$D" "table=2" --gate

# --- 16-bis. the SAME table through the renderer is not a finding -----------------------------
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

func printThing(w io.Writer) error {
	termrender.New(w, termrender.Options{}).Table(thingTable())
	return nil
}
GO
expect "the same table through the renderer" 0 "$D" "CLEAN" --gate

# --- 16-ter. the factory set is PACKAGE-WIDE: declared in one file, called from another -------
D="$(decoy factoryacross)"
cat > "$D/cmd/olivares/cmd_tw.go" <<'GO'
package main

import (
	"io"
	"text/tabwriter"
)

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}
GO
cat > "$D/cmd/olivares/cmd_other.go" <<'GO'
package main

func printOther(w io.Writer) error {
	tw := newTabWriter(w)
	return tw.Flush()
}
GO
baseline_row 1 cmd/olivares/cmd_tw.go "the factory's own tabwriter construction" \
	> "$D/docs/cli-render-baseline.txt"
expect "the factory applies across the package" 1 "$D" "cmd/olivares/cmd_other.go" --gate

# --- 16-quater. the DECLARATION is not a call ------------------------------------------------
# The factory file is charged for the tabwriter it constructs and NOT a second time for
# declaring itself. A rule that counted its own `func` line would charge every owner twice.
rm -f "$D/cmd/olivares/cmd_other.go"
expect "declaring is not calling" 0 "$D" "CLEAN" --gate

# --- 16-quinquies. the factory is found by SIGNATURE, not by name -----------------------------
# A rule that hard-coded "newTabWriter" would be one rename from silent.
D="$(decoy factoryname)"
printf '# no file hand-formats\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	"text/tabwriter"
)

func columnsFor(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}

func printThing(w io.Writer) error { return columnsFor(w).Flush() }
GO
expect "the factory is found by signature" 1 "$D" "table=2" --gate

# --- 17. DOOR (d): an ALIASED text/tabwriter import ------------------------------------------
# Measured green on 2026-09-19: `import tw "text/tabwriter"` and a
# factory returning *tw.Writer. The identifier `tabwriter` never appears, so a rule that
# matched the NAME saw neither the declaration nor the constructor — renaming the PACKAGE was
# one rename, which is what the old header claimed a signature could not be.
D="$(decoy aliasimport)"
printf '# no file hand-formats\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	tw "text/tabwriter"
)

func columnsFor(w io.Writer) *tw.Writer {
	return tw.NewWriter(w, 0, 2, 2, ' ', 0)
}

func printThing(w io.Writer) error { return columnsFor(w).Flush() }
GO
expect "door (d): aliased import" 1 "$D" "NEW" --gate
expect "door (d): the constructor AND the call" 1 "$D" "table=2" --gate

# --- 17-bis. the same command through the renderer, with the alias still imported -------------
# The other direction, and it keeps the aliased import so the case cannot pass by the file
# simply not mentioning tabwriter any more.
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	tw "text/tabwriter"
)

var _ = tw.AlignRight

func printThing(w io.Writer) error {
	termrender.New(w, termrender.Options{}).Table(thingTable())
	return nil
}
GO
expect "door (d): the same output through the renderer" 0 "$D" "CLEAN" --gate

# --- 18. DOOR (c): the factory held in a VARIABLE and called through it -----------------------
# No line reads `newTabWriter(`, so the call is invisible to any rule that looks for one. The
# name is still written down at the ASSIGNMENT, which is where this gate counts it: that is the
# last line at which a reader can see a column writer being handed around.
D="$(decoy factoryvalue)"
printf '# no file hand-formats\n' > "$D/docs/cli-render-baseline.txt"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	"text/tabwriter"
)

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}

var columnsVar = newTabWriter

func printThing(w io.Writer) error {
	tw := columnsVar(w)
	return tw.Flush()
}
GO
expect "door (c): the factory as a value" 1 "$D" "NEW" --gate
expect "door (c): the constructor AND the assignment" 1 "$D" "table=2" --gate

# --- 18-bis. the same command through the renderer, the factory still declared ----------------
# The factory file keeps its own construction — that is its baseline row — and nothing is
# handed around any more, so the gate is clean at that row.
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"
	"text/tabwriter"
)

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}

func printThing(w io.Writer) error {
	termrender.New(w, termrender.Options{}).Table(thingTable())
	return nil
}
GO
baseline_row 1 cmd/olivares/cmd_thing.go "the factory's own tabwriter construction" \
	> "$D/docs/cli-render-baseline.txt"
expect "door (c): the same output through the renderer" 0 "$D" "CLEAN" --gate

# --- 19. DOOR (b): the factory in a SIBLING MODULE of this workspace ---------------------------
# `core.Columns(w)` is a selector whose meaning lives in another directory, and the command-layer
# file never writes the word tabwriter. The gate resolves the import through go.work and the
# sibling's go.mod, and reads the factory there by the same signature rule as a local one.
sibling_workspace() {
	local dir="$1"
	printf 'go 1.26.6\n\nuse (\n\t./cmd/olivares\n\t./core\n)\n' > "$dir/go.work"
	printf 'module example.test/cmd/olivares\n\ngo 1.26.6\n' > "$dir/cmd/olivares/go.mod"
	mkdir -p "$dir/core"
	printf 'module example.test/core\n\ngo 1.26.6\n' > "$dir/core/go.mod"
	cat > "$dir/core/columns.go" <<'GO'
package core

import (
	"io"
	"text/tabwriter"
)

// Columns hands out a column writer, which makes every caller of it a hand-built table.
func Columns(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}
GO
}

D="$(decoy sibling)"
printf '# no file hand-formats\n' > "$D/docs/cli-render-baseline.txt"
sibling_workspace "$D"
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"

	"example.test/core"
)

func printThing(w io.Writer) error {
	tw := core.Columns(w)
	return tw.Flush()
}
GO
expect "door (b): factory in a sibling module" 1 "$D" "cmd/olivares/cmd_thing.go" --gate
expect "door (b): counts the cross-module call" 1 "$D" "table=1" --gate

# --- 19-bis. the same command through the renderer, the sibling module still there -------------
cat > "$D/cmd/olivares/cmd_thing.go" <<'GO'
package main

import (
	"io"

	"example.test/core"
)

var _ = core.Name

func printThing(w io.Writer) error {
	termrender.New(w, termrender.Options{}).Table(thingTable())
	return nil
}
GO
expect "door (b): the same output through the renderer" 0 "$D" "CLEAN" --gate

# --- 19-ter. a `use` entry the gate cannot read is NOT a clean tree ----------------------------
# Door (b) reopens in silence if a sibling module the workspace names is quietly skipped, so the
# reader refuses instead. This is the fail-closed half of the case above.
rm -f "$D/core/go.mod"
expect "door (b): an unreadable sibling module reports COULD NOT CHECK" 2 "$D" "COULD NOT CHECK" --gate

printf '\n'
if [ "$FAILS" -ne 0 ]; then
	printf 'test-check-cli-render: %d of %d cases disagreed.\n' "$FAILS" "$CASES" >&2
	exit 1
fi
printf 'test-check-cli-render: CLEAN — %d cases, each tested in both directions where applicable.\n' "$CASES"
exit 0
