#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-check-test-budget-placement.sh — battery of `scripts/check-test-budget-placement.sh`.
#
# ⛔ WHAT A BATTERY OWES THIS GATE, and it is more than "it goes red once". The gate reads
# TEXT, so the cheap wrong implementation — grep the file for a short budget and grep it for
# `Open(` — passes a naive battery and is useless: almost every store test contains both.
# What makes the gate a gate is the ORDER (fixture between the budget and the wait), the
# THRESHOLD (a 30 s budget is not the class), the COMMENT immunity (this gate's own header
# quotes the defect), and the DISCOVERY GUARD (an empty tree is COULD NOT LOOK, never CLEAN).
# Rows 3, 4, 5 and 10 are the ones that hold those four properties, and each is accredited by
# a MUTANT: the gate is patched to drop the property and the row must change colour. A row
# nothing can break is a row that proves nothing.
#
# ⛔ AND THE THREE REGRESSIONS ARE ROWS, NOT PROSE. The class was measured on three real
# tests; rows 7-9 carry their PRE-FIX bodies verbatim in shape and demand red, rows 10-12
# carry the POST-FIX bodies and demand green. If someone ever rewrites the rule, those six
# rows are what say whether the new rule still catches what the old one was written for.
#
# ⛔ THE REVIEWED EXEMPTION IS EXERCISED AGAINST THE REAL TABLE, with no knob. The key is
# `<path>|<func>`, so a throwaway tree that reproduces that path and that function exercises
# the shipped entry itself. No `OLIVARES_*_EXEMPTIONS` override exists on purpose: an
# override is a way to turn the gate off from the environment, and this repository has
# already paid for one of those.
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="$ROOT/scripts/check-test-budget-placement.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-budget-battery.XXXXXX")" || {
	echo "test-check-test-budget-placement: COULD NOT LOOK — no scratch directory." >&2
	exit 2
}
# ⛔ `chmod -R u+rwX` BEFORE the `rm -rf`, and it is not belt and braces: one row below leaves a
# directory at mode 000 while it runs, and a signal inside that window would leave it there. A
# non-empty unreadable directory cannot be removed by its owner either, so the trap would leak it
# into $TMPDIR — silently, run after run.
trap 'chmod -R u+rwX "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT

PASS=0
FAIL=0
SKIPPED=0
ROW=0
LABEL=""

# ⛔ AS ROOT, A mode-000 PATH DENIES NOTHING. Three rows below build their precondition with
# `chmod 000` and are meaningless for uid 0 — `find` descends the directory and awk opens the
# file anyway, so the row would go RED and its mutant would pass VACUOUSLY. The Hetzner runners
# execute jobs as root (this repository already paid for that assumption once, in
# `license-worker`), so this is not hypothetical. A row whose precondition the running uid
# cannot create is neither a pass nor a failure: it is a row that did not run, and the summary
# says so instead of counting it green.
DENIES_UNREADABLE=1
[ "$(id -u)" = "0" ] && DENIES_UNREADABLE=0


ok() { PASS=$((PASS + 1)); printf '  ok   %2d  %s\n' "$ROW" "$1"; }
# A row that did not run. Not green, not red, and COUNTED, so a summary of "0 failed" cannot be
# read as "everything was exercised".
skipped() { SKIPPED=$((SKIPPED + 1)); printf '  SKIP %2d  %s\n' "$ROW" "$1" >&2; }
ko() {
	FAIL=$((FAIL + 1))
	printf '  FAIL %2d  [%s] %s\n' "$ROW" "$LABEL" "$1" >&2
	printf '           %s\n' "$2" >&2
}

# seed_exempt — every tree carries the file the shipped exemption names, in the same shape,
# so the reviewed entry matches and the STALE detector stays quiet. Row 13 removes it on
# purpose, which is the stale case.
seed_exempt() {
	mkdir -p "$1/core/internal/store/sqlstore"
	cat > "$1/core/internal/store/sqlstore/drcontrolintegrity_test.go" <<'GO'
package sqlstore

func TestDRRecordFIFORefusesThroughActualOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, nil)
		if st != nil {
			_ = st.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		_ = err
	case <-ctx.Done():
		t.Fatal("Open blocked on record FIFO")
	}
}
GO
}

newtree() {
	local d="$WORK/$1"
	mkdir -p "$d/pkg"
	seed_exempt "$d"
	printf '%s' "$d"
}

# run_gate <tree> [gate] — sets LAST (output) and RC (exit code).
#
# ⛔ NOT a command substitution, and that is a measured lesson from this very battery: the
# first draft captured the exit code with `rc="$(run_gate …)"`, which runs the function in a
# SUBSHELL, so every `LAST=` assignment died with it. Eight rows then graded an EMPTY string
# and reported "not named" about output that was correct — a battery lying about its subject.
LAST=""
RC=0
run_gate() {
	local gate="${2:-$GATE}"
	LAST="$(bash "$gate" "$1" 2>&1)"
	RC=$?
}

# The label is carried so a reader can find a row by name in the SOURCE from a failure line.
# It is printed by `ko` and not by `ok`: a passing row needs no pointer back to its code, and
# a failing one is exactly where the reader has to land. (Until 2026-09-16 the label was
# assigned and read by nobody while this comment said it was printed — a dead variable with a
# false comment, which is worse than either alone.)
row() { ROW=$((ROW + 1)); LABEL="$1"; }

echo "test-check-test-budget-placement: battery over throwaway trees in $WORK"

# ── 1 · POSITIVE CONTROL: the class itself must be RED and must be NAMED ────────────────
row "positive control"
T="$(newtree c1)"
cat > "$T/pkg/bad_test.go" <<'GO'
package pkg

func TestClassItself(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("budget was spent by the fixture")
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'pkg/bad_test.go:4' <<<"$LAST" &&
	grep -q 'TestClassItself' <<<"$LAST"; then
	ok "the class is refused and named with its budget line"
else
	ko "the class was not refused or not named (rc=$rc)" "$LAST"
fi

# ── 2 · THE REMEDY: budget after the fixture must be GREEN ──────────────────────────────
row "remedy"
T="$(newtree c2)"
cat > "$T/pkg/good_test.go" <<'GO'
package pkg

func TestRemedyApplied(t *testing.T) {
	setupCtx := context.Background()
	st, err := Open(setupCtx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	held := make(chan struct{})
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("the race did not hand off")
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ]; then ok "the remedy of 01f81b8e81/4859cc43f3/346bce0c8a passes"
else ko "the remedy was refused (rc=$rc)" "$LAST"; fi

# ── 3 · THRESHOLD: a budget above the cap is not this class ─────────────────────────────
row "threshold"
T="$(newtree c3)"
cat > "$T/pkg/long_test.go" <<'GO'
package pkg

func TestLongBudgetIsNotTheClass(t *testing.T) {
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()
	st, err := Open(setupCtx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-setupCtx.Done():
		t.Fatal("setup")
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ]; then ok "a 30 s budget is out of scope (the class is <=20 s)"
else ko "the threshold was ignored (rc=$rc)" "$LAST"; fi

# ── 4 · ORDER: a fixture AFTER the wait is not this class ───────────────────────────────
row "order"
T="$(newtree c4)"
cat > "$T/pkg/order_test.go" <<'GO'
package pkg

func TestFixtureAfterTheWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("handoff")
	}
	st, err := Open(ctx, cfg, nil)
	_ = st
	_ = err
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ]; then ok "a fixture after the wait does not trip the rule (ORDER, not presence)"
else ko "the rule fired on presence instead of order (rc=$rc)" "$LAST"; fi

# ── 5 · COMMENT IMMUNITY: the defect quoted in prose is not the defect ──────────────────
row "comment immunity"
T="$(newtree c5)"
cat > "$T/pkg/comment_test.go" <<'GO'
package pkg

// The class looks like this, and this comment must not trip the gate:
//	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
//	st, err := Open(ctx, cfg, nil)
//	select {
//	case <-ctx.Done():
//	}
func TestOnlyProse(t *testing.T) {
	_ = 1
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ]; then ok "the class written in a comment is prose, not a deadline"
else ko "a comment tripped the gate (rc=$rc)" "$LAST"; fi

# ── 6 · REVIEWED EXEMPTION: the shipped entry matches and does not fail the tree ────────
row "exemption"
T="$(newtree c6)"
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ] && grep -q '1 reviewed exemption(s) matched' <<<"$LAST"; then
	ok "the reviewed exemption matches the shape it waives and the tree stays green"
else
	ko "the reviewed exemption did not match (rc=$rc)" "$LAST"
fi

# ── 7-9 · THE THREE HISTORICAL REGRESSIONS, PRE-FIX: each must be RED ───────────────────
row "regression 01f81b8e81 (pre)"
T="$(newtree c7)"
cat > "$T/pkg/userauthority_bundle_test.go" <<'GO'
package pkg

func TestUserAuthorityBundleWriterRaces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, _, users, tenants := f2aFreshTarget(t, engine)
	bundle := f2aBundle(tenants[0], users[0].ID)
	held := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatalf("writer never took the slot: %v", ctx.Err())
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'TestUserAuthorityBundleWriterRaces' <<<"$LAST"; then
	ok "the 01f81b8e81 defect (f2aFreshTarget inside a 15 s budget) is caught"
else ko "the 01f81b8e81 defect was not caught (rc=$rc)" "$LAST"; fi

row "regression 4859cc43f3 (pre)"
T="$(newtree c8)"
cat > "$T/pkg/directoryepoch_test.go" <<'GO'
package pkg

func TestDirectoryEpochSQLiteLifecyclePathsShareGlobalWriterReservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dsn := filepath.Join(t.TempDir(), "directory-lifecycle-lock.db")
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open SQLite lifecycle-lock store: %v", err)
	}
	missing := provisionTenant(t, st, "directory-sqlite-lock-backfill")
	backfillPaused := make(chan struct{})
	select {
	case <-backfillPaused:
	case <-ctx.Done():
		t.Fatalf("backfill never paused: %v", ctx.Err())
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'LifecyclePathsShareGlobalWriterReservation' <<<"$LAST"; then
	ok "the 4859cc43f3 defect (Open + provisionTenant inside a 15 s budget) is caught"
else ko "the 4859cc43f3 defect was not caught (rc=$rc)" "$LAST"; fi

row "regression 346bce0c8a (pre)"
T="$(newtree c9)"
cat > "$T/pkg/transaction_lock_test.go" <<'GO'
package pkg

func TestSQLiteRowLockerFencesAnotherStoreInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	open := func() store.Store {
		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("open SQLite row-lock store: %v", err)
		}
		return st
	}
	first := open()
	second := open()
	holderReady := make(chan struct{})
	select {
	case <-holderReady:
	case <-ctx.Done():
		t.Fatal("holder never took the writer slot")
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'TestSQLiteRowLockerFencesAnotherStoreInstance' <<<"$LAST"; then
	ok "the 346bce0c8a defect (two Opens inside a 10 s budget) is caught"
else ko "the 346bce0c8a defect was not caught (rc=$rc)" "$LAST"; fi

# ── 10 · DISCOVERY GUARD: a tree with no test file is COULD NOT LOOK, never CLEAN ───────
row "discovery guard"
T="$WORK/c10"
mkdir -p "$T"
run_gate "$T"; rc="$RC"
if [ "$rc" = 2 ]; then ok "an empty tree answers COULD NOT LOOK (2), not CLEAN"
else ko "an empty tree did not answer 2 (rc=$rc)" "$LAST"; fi

# ── 11 · THE POST-FIX BODIES OF THE THREE REGRESSIONS MUST BE GREEN ─────────────────────
row "the three fixes stay green"
T="$(newtree c11)"
cat > "$T/pkg/fixed_test.go" <<'GO'
package pkg

func TestUserAuthorityBundleWriterRaces(t *testing.T) {
	s, _, users, tenants := f2aFreshTarget(t, engine)
	bundle := f2aBundle(tenants[0], users[0].ID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	held := make(chan struct{})
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatalf("writer never took the slot: %v", ctx.Err())
	}
}

func TestDirectoryEpochSQLiteLifecyclePathsShareGlobalWriterReservation(t *testing.T) {
	setupCtx := context.Background()
	st, err := Open(setupCtx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	missing := provisionTenant(t, st, "directory-sqlite-lock-backfill")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	backfillPaused := make(chan struct{})
	select {
	case <-backfillPaused:
	case <-ctx.Done():
		t.Fatalf("backfill never paused: %v", ctx.Err())
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ]; then ok "the shipped remedies of the three fixes are accepted"
else ko "a shipped remedy was refused (rc=$rc)" "$LAST"; fi

# ── 12 · REPORT SHAPE: a finding names the fixture line and the wait line ───────────────
row "report shape"
T="$(newtree c12)"
cp "$WORK/c1/pkg/bad_test.go" "$T/pkg/bad_test.go"
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'fixture inside the budget' <<<"$LAST" &&
	grep -q 'wait that pays for it' <<<"$LAST"; then
	ok "the refusal names the budget, the fixture and the wait"
else ko "the refusal did not name all three sites (rc=$rc)" "$LAST"; fi

# ── 13 · STALE EXEMPTION: a waiver that matches nothing is a FINDING ────────────────────
row "stale exemption"
T="$WORK/c13"
mkdir -p "$T/pkg"
cat > "$T/pkg/unrelated_test.go" <<'GO'
package pkg

func TestNothingToSeeHere(t *testing.T) {
	_ = 1
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'STALE EXEMPTION' <<<"$LAST"; then
	ok "an exemption that matches nothing is refused, not silently carried"
else ko "a stale exemption did not fail (rc=$rc)" "$LAST"; fi

# ── 14 · A PATH WITH WHITESPACE IS READ, not silently globbed away ─────────────────────
# The first shipped version passed the file list through an unquoted `$(cat …)`, which both
# split on spaces and GLOBBED: `a[a]_test.go` silently became `aa_test.go`, read twice, and
# the real file never read at all — under a verdict line that said CLEAN. The list is an
# array now, so the right answer is not "refuse the path", it is "read it".
row "whitespace path"
T="$(newtree c14)"
cp "$WORK/c1/pkg/bad_test.go" "$T/pkg/a file_test.go"
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'a file_test.go:4' <<<"$LAST"; then
	ok "a discovered path with a space is READ and its finding is named"
else ko "a whitespace path was not read (rc=$rc)" "$LAST"; fi

# ── 14b · A GLOB METACHARACTER IN A PATH IS READ ONCE, not expanded ────────────────────
row "glob metacharacter in a path"
T="$(newtree c14b)"
cp "$WORK/c1/pkg/bad_test.go" "$T/pkg/aa_test.go"
cp "$WORK/c1/pkg/bad_test.go" "$T/pkg/a[a]_test.go"
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'a\[a\]_test.go:4' <<<"$LAST" &&
	[ "$(printf '%s' "$LAST" | grep -c 'aa_test.go:4')" = 1 ]; then
	ok "a path with a glob metacharacter is read once, and the real file is read"
else ko "the glob path was expanded away (rc=$rc)" "$LAST"; fi

# ── 14c · THE READER'S OWN FAILURE IS COULD NOT LOOK, never CLEAN ──────────────────────
# Discovery found the file; awk could not open it. A green over a file nobody read is the
# exact trade §0-COBERTURA refuses, and it is what this gate did before the status of awk
# was checked.
row "unreadable file"
T="$(newtree c14c)"
cp "$WORK/c1/pkg/bad_test.go" "$T/pkg/locked_test.go"
chmod 000 "$T/pkg/locked_test.go"
if [ "$DENIES_UNREADABLE" = 1 ]; then
	run_gate "$T"; rc="$RC"
	chmod 644 "$T/pkg/locked_test.go"
	if [ "$rc" = 2 ]; then ok "a file discovery found but awk cannot open answers COULD NOT LOOK (2)"
	else ko "an unreadable file did not answer 2 (rc=$rc)" "$LAST"; fi
else
	chmod 644 "$T/pkg/locked_test.go"
	skipped "uid 0 opens a mode-000 file, so this row cannot build its precondition"
fi

# ── 15 · A METHOD's name is its method name, not its receiver plus its body ────────────
row "method receiver"
T="$(newtree c15)"
cat > "$T/pkg/method_test.go" <<'GO'
package pkg

func (h *harness) waitAndRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Open(ctx, cfg, nil)
	_ = st
	_ = err
	select {
	case <-done:
	case <-ctx.Done():
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'in waitAndRace$' <<<"$LAST"; then
	ok "a finding inside a METHOD is reported under the method name"
else ko "the method name was not extracted (rc=$rc)" "$LAST"; fi

# ── 16 · A STRING LITERAL IS NOT CODE — no FALSE RED in the fast path ──────────────────
# A gate wired into the pre-push hook that can go red on a `t.Fatalf("Open(ctx) …")` poisons
# the push of every branch on every box. Measured on the first shipped version: it did.
row "string literal immunity"
T="$(newtree c16)"
cat > "$T/pkg/str_test.go" <<'GO'
package pkg

func TestStringLiteralIsNotAFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if st == nil {
		t.Fatalf("Open(ctx) returned nil store")
	}
	select {
	case <-held:
	case <-ctx.Done():
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ]; then ok "a fixture head inside a STRING does not trip the gate"
else ko "a string literal produced a false red (rc=$rc)" "$LAST"; fi

# ── 17 · A SEND IS NOT A WAIT, and a channel TYPE is not a wait ────────────────────────
row "send is not a wait"
T="$(newtree c17)"
cat > "$T/pkg/send_test.go" <<'GO'
package pkg

func TestSendDoesNotDisarm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- warmUp() }()
	run := func(held chan<- struct{}) {}
	_ = run
	st, err := Open(ctx, cfg, nil)
	_ = st
	_ = err
	select {
	case <-held:
	case <-ctx.Done():
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q 'TestSendDoesNotDisarm' <<<"$LAST"; then
	ok "a send and a chan<- type do not close the window before the fixture"
else ko "a send disarmed the rule (rc=$rc)" "$LAST"; fi

# ── 18 · A MILLISECOND BUDGET IS IN SCOPE, and its unit is divided ────────────────────
row "millisecond budget"
T="$(newtree c18)"
cat > "$T/pkg/ms_test.go" <<'GO'
package pkg

func TestMillisecondBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	tr := newChatFixture(t)
	_ = tr
	select {
	case <-done:
	case <-ctx.Done():
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q '0.25s budget' <<<"$LAST"; then
	ok "250ms is read as 0.25 s and is in scope (the class at millisecond scale)"
else ko "a millisecond budget was mis-read (rc=$rc)" "$LAST"; fi

# ── 19 · A BARE time.Second IS ONE SECOND, not unreadable ─────────────────────────────
row "bare time.Second"
T="$(newtree c19)"
cat > "$T/pkg/bare_test.go" <<'GO'
package pkg

func TestBareSecond(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	st, err := Open(ctx, cfg, nil)
	_ = st
	_ = err
	select {
	case <-done:
	case <-ctx.Done():
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q '1s budget' <<<"$LAST"; then
	ok "a bare time.Second is read as 1 s"
else ko "a bare time.Second was not read (rc=$rc)" "$LAST"; fi

# ── 20 · A PACKAGE CONSTANT IS RESOLVED — the reintroduction path the reviewer found ───
# `core/audit` writes its budgets as `const serializationTestTimeout = 2*time.Minute`. A
# rule that cannot read a constant calls that package clean WITHOUT LOOKING, and a one-line
# edit of the constant brings the class back under a green gate. Both directions are rows.
row "package constant, short"
T="$(newtree c20)"
cat > "$T/pkg/konst_test.go" <<'GO'
package pkg

const raceBudget = 15 * time.Second

func TestConstantBudgetIsRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), raceBudget)
	defer cancel()
	st, err := Open(ctx, cfg, nil)
	_ = st
	_ = err
	select {
	case <-done:
	case <-ctx.Done():
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q '15s budget' <<<"$LAST"; then
	ok "a package constant of 15 s is resolved and refused"
else ko "a package constant was not resolved (rc=$rc)" "$LAST"; fi

row "package constant, long"
T="$(newtree c20b)"
sed 's/15 \* time.Second/2 * time.Minute/' "$WORK/c20/pkg/konst_test.go" > "$T/pkg/konst_test.go"
run_gate "$T"; rc="$RC"
if [ "$rc" = 0 ]; then ok "the same constant at 2 min is out of scope, not unreadable"
else ko "a 2 min constant was refused (rc=$rc)" "$LAST"; fi

# ── 21 · A BUDGET THIS GATE CANNOT READ IS COUNTED, not skipped ───────────────────────
row "unreadable budget is counted"
T="$(newtree c21)"
cat > "$T/pkg/unreadable_test.go" <<'GO'
package pkg

func TestBudgetFromAnIdentifierElsewhere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), budgetFromSomewhereElse)
	defer cancel()
	_ = ctx
	_ = cancel
}
GO
run_gate "$T"; rc="$RC"
if grep -q '1 budget(s) this gate cannot read' <<<"$LAST"; then
	ok "a budget whose duration cannot be read is COUNTED on the verdict line"
else ko "an unreadable budget was silently skipped (rc=$rc)" "$LAST"; fi

# ── 22 · WithDeadline IS THE SAME CLASS ────────────────────────────────────────────────
row "WithDeadline"
T="$(newtree c22)"
cat > "$T/pkg/deadline_test.go" <<'GO'
package pkg

func TestDeadlineIsTheSameClass(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancel()
	st, err := Open(ctx, cfg, nil)
	_ = st
	_ = err
	select {
	case <-done:
	case <-ctx.Done():
	}
}
GO
run_gate "$T"; rc="$RC"
if [ "$rc" = 1 ] && grep -q '10s budget' <<<"$LAST"; then
	ok "context.WithDeadline(…, time.Now().Add(D)) is the same class and is caught"
else ko "WithDeadline was not caught (rc=$rc)" "$LAST"; fi

# ── 23 · A ROOT THAT IS NOT A DIRECTORY IS COULD NOT LOOK ─────────────────────────────
row "root is not a directory"
run_gate "$WORK/c1/pkg/bad_test.go"; rc="$RC"
if [ "$rc" = 2 ]; then ok "a root that is not a directory answers COULD NOT LOOK (2)"
else ko "a non-directory root did not answer 2 (rc=$rc)" "$LAST"; fi

# ═══════════════════════════════════════════════════════════════════════════════════════
# MUTANTS — each removes ONE property and demands that the row holding it changes colour.
# A mutation that does not apply is itself a failure: the battery would then be grading a
# gate it did not modify.
# ═══════════════════════════════════════════════════════════════════════════════════════
mutate() { # <name> <sed-expr> <tree> <expected-rc-after>
	local name="$1" expr="$2" tree="$3" want="$4"
	local m="$WORK/mutant-$name.sh"
	sed "$expr" "$GATE" > "$m"
	if cmp -s "$m" "$GATE"; then
		FAIL=$((FAIL + 1))
		printf '  FAIL %2d  MUTATION DID NOT APPLY: %s\n' "$ROW" "$name" >&2
		return
	fi
	local got
	run_gate "$tree" "$m"
	got="$RC"
	if [ "$got" = "$want" ]; then ok "mutant '$name' changes the verdict (rc=$got) — the row is not vacuous"
	else ko "mutant '$name' left the verdict at rc=$got, want $want" "$LAST"; fi
}

# The ORDER property expressed positionally: a finding REQUIRES a fixture line between the
# budget and the wait. Drop that requirement and tree c4 — budget, wait, and only then an
# `Open(` — goes red, which is exactly what the naive "grep both in the same file"
# implementation does to every store test in the tree.
row "mutant: drop the order"
mutate order 's/if (fline > 0) {/if (1) {/' "$WORK/c4" 1

row "mutant: drop the threshold"
mutate threshold 's/} else if (s <= maxs + 0) {/} else if (1) {/' "$WORK/c3" 1

row "mutant: drop the comment stripping"
mutate comments 's|sub(/\\/\\/.\*\$/, "", code)|code = code|' "$WORK/c5" 1

row "mutant: an empty tree may report CLEAN"
mutate discovery 's/blind "discovery found no _test.go under \$ROOT — nothing was examined"/say CLEAN-BY-BLINDNESS \&\& exit 0/' "$WORK/c10" 0

# The reader's own failure must reach the verdict. Drop the status check on the second awk
# pass and tree c14c — a file discovery found and awk cannot open — reports CLEAN. Row 14c
# restores the mode, so the mutant re-imposes it: a battery that grades a mutant against a
# tree it has already repaired is grading nothing.
row "mutant: awk status not checked"
mutant_awk="$WORK/mutant-awkstatus.sh"
sed 's/ || blind "awk could not read the discovered files (pass [12])"//' "$GATE" > "$mutant_awk"
if cmp -s "$mutant_awk" "$GATE"; then
	FAIL=$((FAIL + 1))
	printf '  FAIL %2d  MUTATION DID NOT APPLY: awkstatus\n' "$ROW" >&2
else
	if [ "$DENIES_UNREADABLE" = 1 ]; then
		chmod 000 "$WORK/c14c/pkg/locked_test.go"
		run_gate "$WORK/c14c" "$mutant_awk"
		chmod 644 "$WORK/c14c/pkg/locked_test.go"
		if [ "$RC" = 2 ]; then
			ko "mutant 'awkstatus' still answered 2, so row 14c proves nothing" "$LAST"
		else
			ok "mutant 'awkstatus' reports rc=$RC over a file it never read — row 14c is not vacuous"
		fi
	else
		skipped "uid 0: the mutant's premise (an unopenable file) cannot be built"
	fi
fi

# This mutant does NOT move the exit code — both versions refuse tree c15 — so it is graded
# on the OUTPUT, which is the property row 15 actually holds. A mutant compared on the wrong
# observable is a mutant that accredits nothing.
row "mutant: millisecond unit not divided"
mutate ms 's/else if (u == "Millisecond") mul = 1e-3/else if (u == "Millisecond") mul = 1/' "$WORK/c18" 0

row "mutant: bare time.Unit unreadable"
mutate bare 's/{ n = 1; u = e; sub(\/\^time\\.\/, "", u) }/{ return -1 }/' "$WORK/c19" 0

row "mutant: package constants not resolved"
mutate konst 's/if (id in konstlocal) s = seconds(konstlocal\[id\])/s = -1/' "$WORK/c20" 0

row "mutant: strings not stripped"
mutate strings 's|gsub(/"(\[^"\\\\\]\|\\\\.)\*"/, "\\"\\"", code)|code = code|' "$WORK/c16" 1

row "mutant: any arrow closes the window"
mutate arrow 's/isrecv = (probe ~ .*)$/isrecv = (probe ~ \/<-\/)/' "$WORK/c17" 0

row "mutant: greedy receiver match"
mutant_receiver="$WORK/mutant-receiver.sh"
sed 's/\[\^)\]\*/.*/' "$GATE" > "$mutant_receiver"
if cmp -s "$mutant_receiver" "$GATE"; then
	FAIL=$((FAIL + 1))
	printf '  FAIL %2d  MUTATION DID NOT APPLY: receiver\n' "$ROW" >&2
else
	run_gate "$WORK/c15" "$mutant_receiver"
	if grep -q 'in waitAndRace$' <<<"$LAST"; then
		ko "mutant 'receiver' still names the method, so row 15 proves nothing" "$LAST"
	else
		ok "mutant 'receiver' loses the method name — row 15 is not vacuous"
	fi
fi

# ── 23 · THE TABLE NAMES THE FIXTURE HEADS THIS SERIES ITSELF MOVED A BUDGET BELOW ─────
# `connectors/modelprovider/chat_transport_test.go` is the same class at millisecond scale:
# a 250 ms CALLER deadline created above `chatTransport(t, cfg)` and `chatPrepared(t)`, i.e.
# above the HTTP client wrapper, the round tripper and the prepared request. Commit
# f5cc92063e moves it below them — and until this row the gate could not see that site at
# all, so reverting that whole file left the verdict CLEAN. A gate blind to a remedy its own
# series shipped is a gate whose green does not cover its own work; the header's rule is
# "widen the table when a new fixture costs seconds", and this one is read at 250 ms because
# the budget it spends is 250 ms.
#
# Found by the independent standards review of 2026-09-16.
row "modelprovider fixture head"
T="$(newtree c23)"
mkdir -p "$T/connectors/modelprovider"
cat > "$T/connectors/modelprovider/chat_transport_test.go" <<'GO'
package modelprovider

func TestChatTransportRealHTTPCancellationAndTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	transport := chatTransport(t, cfg)
	p := chatPrepared(t)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("deadline")
	}
}
GO
run_gate "$T"
rc="$RC"
if [ "$rc" = 1 ] && grep -q 'chat_transport_test.go:4' <<<"$LAST"; then
	ok "a 250 ms budget above chatTransport( is named, not invisible"
else
	ko "the modelprovider fixture head is not in the table (rc=$rc)" "$LAST"
fi

row "mutant: modelprovider heads dropped from the table"
mutate chatheads 's/|chatTransport|chatPrepared//' "$WORK/c23" 0

# ── 14d · DISCOVERY's OWN FAILURE IS COULD NOT LOOK, never CLEAN ───────────────────────
# Row 14c holds the READER's failure: discovery found a file and awk could not open it.
# This row holds the half before it — discovery itself could not descend. `find` prints the
# subtree it could read, exits NONZERO, and says nothing further about the directory it was
# refused. The class lives in that directory, so the gate that ignores the status answers
# CLEAN over a tree it never finished looking at, which is the trade §0-COBERTURA refuses
# and the exact shape of the two failures already fixed in this gate (the unquoted
# expansion, and awk's unchecked status).
#
# Found by the independent standards review of 2026-09-16, not by a failing run: the gate
# runs under `set -uo pipefail` WITHOUT `-e`, so a nonzero pipeline is a value nobody read.
row "unreadable directory"
T="$(newtree c14d)"
mkdir -p "$T/hidden"
cp "$WORK/c1/pkg/bad_test.go" "$T/hidden/locked_test.go"
if [ "$DENIES_UNREADABLE" = 1 ]; then
	chmod 000 "$T/hidden"
	run_gate "$T"
	rc="$RC"
	chmod 755 "$T/hidden"
	if [ "$rc" = 2 ]; then
		ok "a directory discovery cannot descend answers COULD NOT LOOK (2)"
	else
		ko "an unreadable directory did not answer 2 (rc=$rc)" "$LAST"
	fi
else
	skipped "uid 0 descends a mode-000 directory, so this row cannot build its precondition"
fi

# ── 14e · THE SECOND HALF OF DISCOVERY: a `sort` that fails is COULD NOT LOOK, never CLEAN ──
# Row 14d holds `find`'s status. This holds `sort`'s, and it exists because the review that
# earned 14d said the quiet part: the `|| blind` on the sort had NO ROW, so deleting it left the
# battery at 41/41 while a `sort` that died (ENOSPC in $TMPDIR is the realistic way) truncated
# the file list and the verdict went CLEAN over a tree half read. Same class as 14c and 14d, one
# statement further along, and now it is a row instead of a promise.
#
# The precondition is built without root and without filling a disk: `sort` is unqualified in
# the gate, so a stub earlier on PATH is the whole mechanism.
#
# ⛔ AND THE STUB TRUNCATES, IT DOES NOT EMPTY, which is the difference between a row and a
# decoration. A sort that writes NOTHING is already caught by the `n_files -gt 0` guard, so a
# mutant against it answers 2 either way and proves nothing — measured, on the first draft of
# this row. The dangerous shape is the PARTIAL write: some lines land, the process dies, and the
# list is a SUBSET that no longer contains the offending file. The stub emits the first line and
# exits 1, so discovery is non-empty and the class file is exactly the part that is missing.
row "sort that fails after a partial write"
T="$(newtree c14e)"
cp "$WORK/c1/pkg/bad_test.go" "$T/pkg/bad_test.go"
mkdir -p "$WORK/binstub"
cat > "$WORK/binstub/sort" <<'STUB'
#!/bin/sh
# A sort that dies mid-write. It emits every discovered path EXCEPT the one holding the class,
# then exits nonzero. Which line a real dying sort drops depends on its buffer; that is not the
# property under test. The property is: if the list that reaches the reader is a SUBSET and
# nobody checks the status, the verdict is CLEAN over a file that was never opened.
for a in "$@"; do
	case "$a" in
	-*) ;;
	*) [ -f "$a" ] && grep -v '/bad_test\.go$' "$a" ;;
	esac
done
exit 1
STUB
chmod 755 "$WORK/binstub/sort"
LAST="$(PATH="$WORK/binstub:$PATH" bash "$GATE" "$T" 2>&1)"
rc=$?
if [ "$rc" = 2 ]; then
	ok "a sort that fails answers COULD NOT LOOK (2), not CLEAN over a truncated list"
else
	ko "a failed sort did not answer 2 (rc=$rc)" "$LAST"
fi

row "mutant: sort status not checked"
mutant_sort="$WORK/mutant-sortstatus.sh"
sed 's/ || blind "cannot sort the discovered file list"//' "$GATE" > "$mutant_sort"
if cmp -s "$mutant_sort" "$GATE"; then
	FAIL=$((FAIL + 1))
	printf '  FAIL %2d  MUTATION DID NOT APPLY: sortstatus\n' "$ROW" >&2
else
	LAST="$(PATH="$WORK/binstub:$PATH" bash "$mutant_sort" "$T" 2>&1)"
	RC=$?
	if [ "$RC" = 2 ]; then
		ko "mutant 'sortstatus' still answered 2, so the row above proves nothing" "$LAST"
	else
		ok "mutant 'sortstatus' reports rc=$RC over a TRUNCATED file list — the row is not vacuous"
	fi
fi

# Graded with the directory LOCKED, like the awkstatus mutant above: a mutant judged over a
# tree the row has already repaired is judging nothing.
row "mutant: discovery status not checked"
mutant_discovery="$WORK/mutant-discoverystatus.sh"
sed 's/ || blind "discovery could not read the whole tree under \$ROOT"//' "$GATE" > "$mutant_discovery"
if cmp -s "$mutant_discovery" "$GATE"; then
	FAIL=$((FAIL + 1))
	printf '  FAIL %2d  MUTATION DID NOT APPLY: discoverystatus\n' "$ROW" >&2
else
	if [ "$DENIES_UNREADABLE" = 1 ]; then
		chmod 000 "$WORK/c14d/hidden"
		run_gate "$WORK/c14d" "$mutant_discovery"
		chmod 755 "$WORK/c14d/hidden"
		if [ "$RC" = 2 ]; then
			ko "mutant 'discoverystatus' still answered 2, so the row above proves nothing" "$LAST"
		else
			ok "mutant 'discoverystatus' reports rc=$RC over a subtree it never listed — the row is not vacuous"
		fi
	else
		skipped "uid 0: the mutant's premise (a subtree find cannot list) cannot be built"
	fi
fi

echo
printf 'test-check-test-budget-placement: %d passed, %d failed, %d skipped, %d row(s)\n' \
	"$PASS" "$FAIL" "$SKIPPED" "$ROW"
[ "$SKIPPED" -eq 0 ] || printf 'test-check-test-budget-placement: %d row(s) did not run under uid %s; see DENIES_UNREADABLE\n' "$SKIPPED" "$(id -u)" >&2
[ "$FAIL" -eq 0 ] || exit 1
exit 0
