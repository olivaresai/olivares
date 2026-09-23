// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestHoldIDRejectsMalformedAndZero pins what a hold identity is. Every ledger row of one
// generation of a key carries it, and a settlement finds that money by it, so a value
// that merely PARSES is not enough: the store compares text, and an uppercase or braced
// spelling of a real identity names no row at all. Only the canonical form of a non-zero
// identity is one; the empty string is "no hold"; everything else is an invalid request.
func TestHoldIDRejectsMalformedAndZero(t *testing.T) {
	none, err := parseHoldID("")
	if err != nil || !none.isZero() {
		t.Fatalf(`"" must decode as no hold: %q err=%v`, none, err)
	}

	minted := newHoldID()
	again, err := parseHoldID(minted.String())
	if err != nil || again != minted || again.isZero() {
		t.Fatalf("a minted identity does not round-trip: minted=%q decoded=%q err=%v", minted, again, err)
	}
	if other := newHoldID(); other == minted {
		t.Fatalf("two generations were minted the same identity %q", minted)
	}

	canonical := minted.String()
	undashed := strings.ReplaceAll(canonical, "-", "")
	// The control that makes the equality leg necessary: the parser the rest of the
	// module uses accepts these spellings, and every one of them names no ledger row.
	for _, spelling := range []string{strings.ToUpper(canonical), "{" + canonical + "}", "urn:uuid:" + canonical, undashed} {
		if _, perr := model.ParseID(spelling); perr != nil {
			t.Fatalf("fixture: model.ParseID no longer accepts %q (%v); the case proves nothing", spelling, perr)
		}
	}
	for _, bad := range []string{
		"x1",
		"not-a-hold",
		"00000000-0000-0000-0000-000000000000",
		strings.ToUpper(canonical),
		"{" + canonical + "}",
		"urn:uuid:" + canonical,
		undashed,
		" " + canonical,
		canonical + " ",
		canonical + "x",
	} {
		got, err := parseHoldID(bad)
		if !errors.Is(err, ErrInvalidAdmission) {
			t.Errorf("parseHoldID(%q) = %q, %v; want ErrInvalidAdmission", bad, got, err)
		}
		if !got.isZero() {
			t.Errorf("parseHoldID(%q) returned identity %q alongside its refusal", bad, got)
		}
	}

	// A stored slot is decoded by the same rule. A row whose slot is not an identity is
	// not a row a writer may act on: it is refused as corrupt, never read as "no hold".
	for _, col := range []string{colAdmHandle, colAdmSpendHandle} {
		rec := admissionRecord("corrupt-"+col, admStateReserved, "", "", baseTime)
		rec[col] = "x1"
		if _, err := admissionRowFrom(rec); !errors.Is(err, errAdmissionRowCorrupt) {
			t.Errorf("a %s slot holding %q decoded with err=%v; want errAdmissionRowCorrupt", col, "x1", err)
		}
	}
}

// TestOwedHoldsDecodeIsStrict pins the owed list. It is the only record of money a
// superseded generation still holds, so a list this module did not write is not
// guessed at: it is undecodable, the row keeps its text, and a writer that rewrites the
// row writes that text back unchanged. No list longer than sixteen can be written.
func TestOwedHoldsDecodeIsStrict(t *testing.T) {
	encode := func(t *testing.T, ids []string) string {
		t.Helper()
		b, err := json.Marshal(ids)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	fresh := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = newHoldID().String()
		}
		return out
	}

	if owed, err := decodeOwedHolds(nil); err != nil || len(owed) != 0 {
		t.Fatalf("NULL must decode as nothing owed: %v err=%v", owed, err)
	}
	for _, n := range []int{1, maxOwedHolds} {
		ids := fresh(n)
		text := encode(t, ids)
		owed, err := decodeOwedHolds(text)
		if err != nil || len(owed) != n {
			t.Fatalf("a canonical list of %d did not decode: %v err=%v", n, owed, err)
		}
		for i, h := range owed {
			if h.String() != ids[i] {
				t.Fatalf("entry %d decoded as %q, want %q", i, h, ids[i])
			}
		}
		cell, err := owed.encode()
		if err != nil || cell != text {
			t.Fatalf("a list of %d does not re-encode to its own bytes: %v err=%v", n, cell, err)
		}
	}

	one := fresh(1)[0]
	seventeen := fresh(maxOwedHolds + 1)
	for name, cell := range map[string]any{
		"empty text":         "",
		"empty list":         "[]",
		"JSON null":          "null",
		"seventeen entries":  encode(t, seventeen),
		"a duplicate":        encode(t, []string{one, one}),
		"a malformed entry":  encode(t, []string{one, "x1"}),
		"the zero identity":  encode(t, []string{"00000000-0000-0000-0000-000000000000"}),
		"an empty entry":     encode(t, []string{""}),
		"an uppercase entry": encode(t, []string{strings.ToUpper(one)}),
		"a number":           "[1]",
		"an object":          `{"h":"` + one + `"}`,
		"trailing bytes":     encode(t, []string{one}) + "x",
		"foreign spacing":    `[ "` + one + `" ]`,
		"not text":           int64(5),
	} {
		if owed, err := decodeOwedHolds(cell); !errors.Is(err, errOwedUndecodable) || len(owed) != 0 {
			t.Errorf("%s (%v) decoded as %v err=%v; want errOwedUndecodable", name, cell, owed, err)
		}
	}

	// Sixteen is the bound, and it holds for every writer: a list of seventeen cannot be
	// encoded, and a union that would pass it is refused and leaves the list as it was.
	full := make(owedHolds, 0, maxOwedHolds)
	for _, s := range fresh(maxOwedHolds) {
		full = append(full, holdID(s))
	}
	if _, err := append(full[:len(full):len(full)], newHoldID()).encode(); !errors.Is(err, errOwedFull) {
		t.Fatalf("a list of seventeen encoded: err=%v", err)
	}
	if grown, err := full.with(newHoldID()); !errors.Is(err, errOwedFull) || grown != nil {
		t.Fatalf("a seventeenth owed identity was accepted: %v err=%v", grown, err)
	}
	if len(full) != maxOwedHolds {
		t.Fatalf("a refused union changed the list it was refused on: %d entries", len(full))
	}
	same, err := full.with(full[0], "")
	if err != nil || len(same) != maxOwedHolds {
		t.Fatalf("a known identity and no hold must not grow a full list: %d entries err=%v", len(same), err)
	}
	var none owedHolds
	grown, err := none.with("", holdID(one), "", holdID(one))
	if err != nil || len(grown) != 1 || grown[0] != holdID(one) {
		t.Fatalf("a union of one identity twice and no hold: %v err=%v", grown, err)
	}

	// A list that names no hold, or one hold twice, is a writer's fault, not stored text:
	// it cannot be encoded, and it is told apart from an undecodable stored list.
	for name, list := range map[string]owedHolds{
		"no hold":        {""},
		"one hold twice": {holdID(one), holdID(one)},
	} {
		if cell, err := list.encode(); !errors.Is(err, errOwedEntryInvalid) || errors.Is(err, errOwedUndecodable) {
			t.Errorf("a list naming %s encoded as %v err=%v; want errOwedEntryInvalid", name, cell, err)
		}
	}

	// An undecodable list on a row: the row still decodes, it says why its list is not
	// used, and rewriting the row carries the stored text back byte for byte.
	raw := `["` + one + `",`
	rec := admissionRecord("undecodable-owed", admStatePending, newHoldID(), "", baseTime)
	rec[colAdmOwedHandles] = raw
	rec[model.ColID] = model.NewID().String()
	rec[model.ColVersion] = int64(3)
	row, err := admissionRowFrom(rec)
	if err != nil {
		t.Fatalf("a row whose owed list is undecodable must still decode: %v", err)
	}
	if !errors.Is(row.owedErr, errOwedUndecodable) || len(row.owed) != 0 {
		t.Fatalf("the row does not say its owed list is undecodable: owed=%v owedErr=%v", row.owed, row.owedErr)
	}
	back, err := row.record()
	if err != nil {
		t.Fatalf("encode the row back: %v", err)
	}
	if back[colAdmOwedHandles] != raw {
		t.Fatalf("the undecodable text was not written back unchanged: %v, want %q", back[colAdmOwedHandles], raw)
	}
	if back[model.ColVersion] != int64(3) || back[model.ColID] != rec[model.ColID] {
		t.Fatalf("the rewrite does not present the row it read: id=%v version=%v", back[model.ColID], back[model.ColVersion])
	}
}

// TestRowLookupsReadBothLegacySlots pins how money is found by its identity. A legacy
// pair names its budget hold in `handle` and its seat hold in `spend_handle`, and a
// spend-only admission names its only hold in the spend slot, so a settlement that reads
// one slot loses the other half of the money. The spend slot names a row for settlement
// only while that row publishes the hold; a row that owes money back is settled by
// recovery. Every lookup is the tenant's own.
func TestRowLookupsReadBothLegacySlots(t *testing.T) {
	forEachAdmissionEngine(t, runRowLookupsReadBothLegacySlots)
}

func runRowLookupsReadBothLegacySlots(t *testing.T, cfg store.Config) {
	_, st, tenant, _ := openFinCfg(t, cfg)
	ctx := context.Background()
	now := baseTime
	live := now.Add(reservationTTL)
	budget, limit := model.NewID(), model.NewID()
	h1, h2, h3, h4 := newHoldID(), newHoldID(), newHoldID(), newHoldID()
	h5, h6, h8, h9 := newHoldID(), newHoldID(), newHoldID(), newHoldID()
	intent := newHoldID()

	// (c) a published legacy pair, withholding on both components.
	c := seedAdmission(t, st, tenant, admissionRecord("legacy-c", admStateReserved, h1, h2, now.Add(-time.Minute)))
	seedReservation(t, st, tenant, ledgerRow(budget, "b", h1, 1, 2*oneUSD, now, live, resvStateActive))
	seedReservation(t, st, tenant, ledgerRow(limit, "s", h2, 1, 2*oneUSD, now, live, resvStateActive))
	// (j) spend-only: an earlier build issued no budget handle when no budget applied.
	seedAdmission(t, st, tenant, admissionRecord("legacy-j", admStateReserved, "", h3, now.Add(-30*time.Second)))
	seedReservation(t, st, tenant, ledgerRow(limit, "s", h3, 2, 2*oneUSD, now, live, resvStateActive))
	// (j2) the same shape after its release.
	seedAdmission(t, st, tenant, admissionRecord("legacy-j2", admStateReleased, "", h4, now.Add(-30*time.Second)))
	// (e) a committed pair.
	seedAdmission(t, st, tenant, admissionRecord("legacy-e", admStateCommitted, h8, h9, now.Add(-1000*time.Second)))
	// (g) a row that owes both halves back.
	seedAdmission(t, st, tenant, admissionRecord("legacy-g", admStateOwesRelease, h5, h6, now.Add(-20*time.Second)))
	// (i) an undated legacy claim.
	seedAdmission(t, st, tenant, admissionRecord("legacy-i", admStatePending, "", "", time.Time{}))
	// A current claim: its intent is named before any ledger row exists.
	claim := admissionRecord("intent-k", admStatePending, intent, "", now)
	claim[colAdmSpendHandle] = nil
	seedAdmission(t, st, tenant, claim)

	// The same identity in another tenant is another tenant's row.
	other := provisionSecondTenant(t, st)
	seedAdmission(t, st, other, admissionRecord("legacy-c", admStateReserved, h1, "", now))

	naming := func(t *testing.T, tenant model.TenantID, h holdID) (admissionRow, bool) {
		t.Helper()
		var (
			row   admissionRow
			found bool
		)
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			row, found, err = rowNamingHold(ctx, sc, h)
			return err
		}); err != nil {
			t.Fatalf("the row naming %q: %v", h, err)
		}
		return row, found
	}
	for _, tc := range []struct {
		name string
		h    holdID
		key  string
	}{
		{"(c) by its budget slot", h1, "legacy-c"},
		{"(c) by its spend slot", h2, "legacy-c"},
		{"(j) spend-only and reserved", h3, "legacy-j"},
		{"(j2) spend-only and released", h4, "legacy-j2"},
		{"(e) committed, by its spend slot", h9, "legacy-e"},
		{"(g) owes_release, by its handle slot", h5, "legacy-g"},
		{"a pending intent", intent, "intent-k"},
	} {
		row, found := naming(t, tenant, tc.h)
		if !found || row.key != tc.key {
			t.Errorf("%s: found=%v key=%q, want %q", tc.name, found, row.key, tc.key)
		}
	}
	for _, tc := range []struct {
		name string
		h    holdID
	}{
		{"(g) owes_release, by its spend slot: recovery settles it", h6},
		{"an identity no row names", newHoldID()},
		{"no hold", ""},
	} {
		if row, found := naming(t, tenant, tc.h); found {
			t.Errorf("%s: named by row %q (%s)", tc.name, row.key, row.state)
		}
	}
	if row, found := naming(t, other, h1); !found || row.key != "legacy-c" || !row.spendHandle.isZero() {
		t.Errorf("the other tenant's own row was not found by its own slot: found=%v row=%+v", found, row)
	}
	if row, found := naming(t, other, h2); found {
		t.Errorf("a lookup crossed tenants: %q named by the other tenant's row %q", h2, row.key)
	}

	ofKey := func(t *testing.T, key string) (admissionRow, bool) {
		t.Helper()
		var (
			row   admissionRow
			found bool
		)
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			row, found, err = rowOfKey(ctx, sc, key)
			return err
		}); err != nil {
			t.Fatalf("the row of %q: %v", key, err)
		}
		return row, found
	}
	row, found := ofKey(t, "legacy-c")
	if !found || row.state != admStateReserved || row.handle != h1 || row.spendHandle != h2 {
		t.Fatalf("the row of legacy-c decoded as found=%v %+v", found, row)
	}
	if want := model.NewTimestamp(now.Add(-time.Minute)).String(); row.stateAt.String() != want {
		t.Errorf("state_at decoded as %s, want %s", row.stateAt, want)
	}
	if len(row.owed) != 0 || row.owedErr != nil {
		t.Errorf("a legacy row with no owed list decoded owed=%v owedErr=%v", row.owed, row.owedErr)
	}
	if want := (admissionToken{key: "legacy-c", version: c.Int(model.ColVersion)}); row.token() != want || want.version == 0 {
		t.Errorf("the row's token is %+v, want the version it was read at: %+v", row.token(), want)
	}
	if undated, _ := ofKey(t, "legacy-i"); !undated.stateAt.IsZero() {
		t.Errorf("an undated row decoded a stamp: %v", undated.stateAt)
	}
	if _, found := ofKey(t, "never-claimed"); found {
		t.Error("a key nobody claimed has a row")
	}

	under := func(t *testing.T, tenant model.TenantID, h holdID) []model.Record {
		t.Helper()
		var rows []model.Record
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			rows, err = rowsUnderHold(ctx, sc, h)
			return err
		}); err != nil {
			t.Fatalf("rows under %q: %v", h, err)
		}
		return rows
	}
	if rows := under(t, tenant, h1); len(rows) != 1 || rows[0].String(colResvPolicyKind) != policyKindBudget {
		t.Errorf("rows under h1: %d, want the one budget row", len(rows))
	}
	if rows := under(t, tenant, h2); len(rows) != 1 || rows[0].String(colResvPolicyKind) != policyKindSpendLimit {
		t.Errorf("rows under h2: %d, want the one spend-limit row", len(rows))
	}
	if rows := under(t, other, h1); len(rows) != 0 {
		t.Errorf("the other tenant reads %d of this tenant's rows under h1", len(rows))
	}
}
