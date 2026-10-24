// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// tailLedger is an audit log that records where each Verify and WalkCanonical starts,
// so a test sees how much of the ledger one session read walks.
type tailLedger struct {
	events    []model.AuditEvent
	metas     []string
	breakAt   int64
	headErr   error
	walkErrAt int64
	// recordedHead is the store's own record of its newest seq (audit_heads): a
	// ledger whose rows end before it was cut short.
	recordedHead int64
	verifyFrom   []int64
	walkFrom     []int64
}

func (l *tailLedger) add(actor string, kind model.Kind, target, meta string) {
	seq := int64(len(l.events) + 1)
	l.events = append(l.events, model.AuditEvent{Seq: seq, Actor: actor, Action: "test.event", TargetKind: kind, TargetID: model.ID(target), Hash: fmt.Appendf(nil, "hash-%d", seq)})
	l.metas = append(l.metas, meta)
	l.recordedHead = seq
}

// truncate removes the newest rows, leaving the recorded head where it was.
func (l *tailLedger) truncate(keep int) {
	l.events, l.metas = l.events[:keep], l.metas[:keep]
}

func (l *tailLedger) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, errors.New("not used")
}

func (l *tailLedger) Walk(context.Context, int64, func(model.AuditEvent) error) error {
	return errors.New("the session read walks canonical meta only")
}

// Verify fails when the break lies in the range asked, as the store's walk does: a
// break before fromSeq is outside what it re-hashes.
func (l *tailLedger) Verify(_ context.Context, fromSeq int64) (store.VerifyReport, error) {
	l.verifyFrom = append(l.verifyFrom, fromSeq)
	if l.breakAt != 0 && l.breakAt >= fromSeq {
		return store.VerifyReport{BreakAt: l.breakAt, Reason: "hash-mismatch"}, nil
	}
	if last := int64(len(l.events)); l.recordedHead > last {
		return store.VerifyReport{BreakAt: last + 1, Reason: "tail-truncated"}, nil
	}
	return store.VerifyReport{OK: true, Checked: int64(len(l.events)) - fromSeq + 1}, nil
}

func (l *tailLedger) Head(context.Context) (store.HeadRef, bool, error) {
	if l.headErr != nil {
		return store.HeadRef{}, false, l.headErr
	}
	if len(l.events) == 0 {
		return store.HeadRef{}, false, nil
	}
	last := l.events[len(l.events)-1]
	return store.HeadRef{Seq: last.Seq, Hash: last.Hash}, true, nil
}

func (l *tailLedger) WalkCanonical(_ context.Context, fromSeq int64, fn func(model.AuditEvent, string, []byte) error) error {
	l.walkFrom = append(l.walkFrom, fromSeq)
	for i, ev := range l.events {
		if ev.Seq < fromSeq {
			continue
		}
		if ev.Seq == l.walkErrAt {
			return errors.New("ledger read failed mid-walk")
		}
		if err := fn(ev, l.metas[i], nil); err != nil {
			return err
		}
	}
	return nil
}

func ledgerRun(id, ref, sid string) model.Record {
	return model.Record{model.ColID: id, colRunRef: ref, colRunClaimSID: sid}
}

// knownRuns resolves run_ref and claim_sid values against a fixed set of runs.
func knownRuns(runs ...model.Record) runResolver {
	return func(_ context.Context, col string, values []string) (map[string][]string, error) {
		out := map[string][]string{}
		for _, run := range runs {
			if v := run.String(col); slices.Contains(values, v) {
				out[v] = append(out[v], run.String(model.ColID))
			}
		}
		return out, nil
	}
}

// readLedger is one session read of the ledger facts: counts, the verdict (nil when
// unknown) and the error.
func readLedger(t *testing.T, m *Module, l *tailLedger, tenant model.TenantID, recs []model.Record) (map[string]int64, *bool, error) {
	t.Helper()
	facts, err := m.factLedger(t.Context(), l, tenant, recs, knownRuns(recs...))
	return facts.counts, facts.verified, err
}

func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }

// A session read verifies and counts only the entries appended since the last read,
// and counts every entry that records the session: those that target its row, those
// whose meta names its run or its session, and those its session identity wrote.
func TestRunLedgerTailVerifiesAndCountsOnlyNewEntries(t *testing.T) {
	m := New()
	tenant := model.TenantID("t_tail")
	recs := []model.Record{ledgerRun("row-a", "run-a", "sid-a"), ledgerRun("row-b", "run-b", "sid-b")}
	l := &tailLedger{}
	l.add("user:u1", runKind, "row-a", `{}`)
	l.add("session:sid-a", "claude.tool.use", "toolu_1", `{"decision":"allow","run_ref":"run-a","session_ref":"sid-a"}`)
	l.add("user:u1", liveKind, "", `{"session_ref":"sid-a"}`)
	l.add("session:sid-a", "governance.approval", "appr-1", `{"action":"publish"}`)
	l.add("user:u1", runKind, "row-b", `{}`)
	l.add("user:u1", "evals.run", "e-1", `{"run_ref":"another-run"}`)

	read := func() (map[string]int64, bool) {
		t.Helper()
		counts, verified, err := readLedger(t, m, l, tenant, recs)
		if err != nil {
			t.Fatal(err)
		}
		return counts, isTrue(verified)
	}

	counts, verified := read()
	if counts["row-a"] != 4 || counts["row-b"] != 1 || !verified {
		t.Fatalf("first read = %v verified=%v, want row-a 4, row-b 1, verified", counts, verified)
	}
	if !slices.Equal(l.verifyFrom, []int64{1}) || !slices.Equal(l.walkFrom, []int64{1}) {
		t.Fatalf("first read verified from %v and walked from %v, want the whole chain once", l.verifyFrom, l.walkFrom)
	}
	// A run_ref no session run carries (here an eval run's) is not kept: the tail holds
	// counts for the two runs only.
	if kept := m.rt.factsCache.tail(tenant).counts; len(kept) != 2 {
		t.Fatalf("the tail keeps %v, want counts for the two runs only", kept)
	}

	// No new entry: the read walks nothing.
	if counts, verified = read(); counts["row-a"] != 4 || !verified || len(l.verifyFrom) != 1 || len(l.walkFrom) != 1 {
		t.Fatalf("an unchanged ledger was walked again: verify %v walk %v counts %v", l.verifyFrom, l.walkFrom, counts)
	}

	// One new entry: the read re-hashes from the last head it saw, not from 1.
	l.add("user:u1", "sessions.stop", "", `{"run_ref":"run-b"}`)
	counts, verified = read()
	if counts["row-a"] != 4 || counts["row-b"] != 2 || !verified {
		t.Fatalf("after one append = %v verified=%v, want row-b 2", counts, verified)
	}
	if !slices.Equal(l.verifyFrom, []int64{1, 6}) || !slices.Equal(l.walkFrom, []int64{1, 6}) {
		t.Fatalf("an append re-verified from %v and walked from %v, want only from the prior head 6", l.verifyFrom, l.walkFrom)
	}

	// A break in the new range reads unverified, and stays so without a new walk of it.
	l.breakAt = 8
	l.add("session:sid-b", "claude.tool.use", "toolu_2", `{}`)
	l.add("session:sid-b", "claude.tool.use", "toolu_3", `{}`)
	if counts, verified = read(); verified || counts["row-b"] != 4 {
		t.Fatalf("a broken chain = %v verified=%v, want unverified with row-b 4", counts, verified)
	}
	l.add("user:u1", runKind, "row-a", `{}`)
	if counts, verified = read(); verified || counts["row-a"] != 5 {
		t.Fatalf("after a break = %v verified=%v, want still unverified with row-a 5", counts, verified)
	}
	if got := l.verifyFrom[len(l.verifyFrom)-1]; len(l.verifyFrom) != 3 || got != 7 {
		t.Fatalf("verify calls = %v, want none after the break was found", l.verifyFrom)
	}
}

// The newest entry a read saw anchors the next one: rewritten, the chain no longer
// reads verified, whether or not entries were appended after it.
func TestRunLedgerTailCatchesARewrittenAnchor(t *testing.T) {
	for _, appendAfter := range []bool{false, true} {
		t.Run(fmt.Sprintf("append=%v", appendAfter), func(t *testing.T) {
			m := New()
			recs := []model.Record{ledgerRun("row-a", "run-a", "sid-a")}
			l := &tailLedger{}
			l.add("user:u1", runKind, "row-a", `{}`)
			l.add("user:u1", runKind, "row-a", `{}`)
			if _, verified, err := readLedger(t, m, l, "t_anchor", recs); err != nil || !isTrue(verified) {
				t.Fatalf("first read verified=%v err=%v", verified, err)
			}
			l.events[1].Hash = []byte("rewritten")
			if appendAfter {
				l.add("user:u1", runKind, "row-a", `{}`)
			}
			if _, verified, err := readLedger(t, m, l, "t_anchor", recs); err != nil || !isFalse(verified) {
				t.Fatalf("a rewritten anchor read verified=%v err=%v, want unverified", verified, err)
			}
		})
	}
}

// A ledger that cannot be read is an error to report, and it moves no state, even when
// the walk failed after counting some entries: the next read that can see the ledger
// starts where the last good one ended and counts each entry once.
func TestRunLedgerTailKeepsItsStateOnAReadFailure(t *testing.T) {
	m := New()
	recs := []model.Record{ledgerRun("row-a", "run-a", "sid-a")}
	l := &tailLedger{}
	l.add("user:u1", runKind, "row-a", `{}`)
	if _, _, err := readLedger(t, m, l, "t_fail", recs); err != nil {
		t.Fatal(err)
	}
	l.add("user:u1", runKind, "row-a", `{}`)
	l.add("user:u1", runKind, "row-a", `{}`)
	l.headErr = errors.New("ledger unavailable")
	if _, _, err := readLedger(t, m, l, "t_fail", recs); err == nil {
		t.Fatal("an unreadable head read as a count")
	}
	l.headErr, l.walkErrAt = nil, 3
	if _, _, err := readLedger(t, m, l, "t_fail", recs); err == nil {
		t.Fatal("a walk that failed half-way read as a count")
	}
	l.walkErrAt = 0
	counts, verified, err := readLedger(t, m, l, "t_fail", recs)
	if err != nil || !isTrue(verified) || counts["row-a"] != 3 || l.walkFrom[len(l.walkFrom)-1] != 1 {
		t.Fatalf("after the failures = %v verified=%v err=%v walks %v, want row-a 3 walked from the last good head", counts, verified, err, l.walkFrom)
	}
}

// A walk that fails before it reads the newest entry the last read saw (a cancelled
// request, a store error) found no break: the next read that can see the ledger reads
// it verified, not broken until the engine restarts.
func TestRunLedgerTailFailingBeforeItsAnchorIsNoBreak(t *testing.T) {
	m := New()
	recs := []model.Record{ledgerRun("row-a", "run-a", "sid-a")}
	l := &tailLedger{}
	l.add("user:u1", runKind, "row-a", `{}`)
	l.add("user:u1", runKind, "row-a", `{}`)
	if _, verified, err := readLedger(t, m, l, "t_anchor_fail", recs); err != nil || !isTrue(verified) {
		t.Fatalf("first read verified=%v err=%v", verified, err)
	}
	l.add("user:u1", runKind, "row-a", `{}`)
	l.walkErrAt = 2
	if _, _, err := readLedger(t, m, l, "t_anchor_fail", recs); err == nil {
		t.Fatal("a walk that failed on its anchor read as a count")
	}
	l.walkErrAt = 0
	counts, verified, err := readLedger(t, m, l, "t_anchor_fail", recs)
	if err != nil || !isTrue(verified) || counts["row-a"] != 3 {
		t.Fatalf("after the failed walk = %v verified=%v err=%v, want row-a 3 and verified", counts, verified, err)
	}
}

// Rows removed from the end of the ledger, or all of them, read as a break; a read on an
// older snapshot, whose recorded head matches its rows, still reads verified.
func TestRunLedgerTailCatchesAShorterLedger(t *testing.T) {
	recs := []model.Record{ledgerRun("row-a", "run-a", "sid-a")}
	for _, tc := range []struct {
		name     string
		keep     int
		recorded int64 // 0: the store's recorded head moves with the rows (an older snapshot)
		verified bool
	}{
		{"older snapshot", 2, 2, true},
		{"newest rows removed", 2, 0, false},
		{"every row removed", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			l := &tailLedger{}
			for range 4 {
				l.add("user:u1", runKind, "row-a", `{}`)
			}
			if _, verified, err := readLedger(t, m, l, "t_short", recs); err != nil || !isTrue(verified) {
				t.Fatalf("first read verified=%v err=%v", verified, err)
			}
			l.truncate(tc.keep)
			if tc.recorded != 0 {
				l.recordedHead = tc.recorded
			}
			_, verified, err := readLedger(t, m, l, "t_short", recs)
			if err != nil || verified == nil || *verified != tc.verified {
				t.Fatalf("after the ledger shrank to %d rows verified=%v err=%v, want %v", tc.keep, verified, err, tc.verified)
			}
		})
	}
}

// A row the verification reports broken and the walk cannot read (a malformed record)
// reads as a broken chain with no count, never as an outage that hides the break.
func TestRunLedgerTailReportsABreakTheWalkCannotReadPast(t *testing.T) {
	m := New()
	recs := []model.Record{ledgerRun("row-a", "run-a", "sid-a")}
	l := &tailLedger{breakAt: 2, walkErrAt: 2}
	l.add("user:u1", runKind, "row-a", `{}`)
	l.add("user:u1", runKind, "row-a", `{}`)
	for read := range 2 {
		facts, err := m.factLedger(t.Context(), l, "t_malformed", recs, knownRuns(recs...))
		if err == nil || !isFalse(facts.verified) || facts.counts != nil || facts.reason == "" {
			t.Fatalf("read %d = %+v err=%v, want the chain unverified, no count and a reason", read, facts, err)
		}
	}
	proof := factProof(recs[0], ledgerFacts{verified: new(bool), reason: "r"})
	if proof.LedgerEntries != nil || proof.ChainVerified == nil || *proof.ChainVerified || proof.Reason != "r" {
		t.Fatalf("proof = %+v, want ledger_entries null and chain_verified false with the reason", proof)
	}
}

// An empty ledger has nothing to verify: the verdict is null with a reason, not false.
func TestRunLedgerTailOnAnEmptyLedgerSaysWhy(t *testing.T) {
	facts, err := New().factLedger(t.Context(), &tailLedger{}, "t_empty", nil, knownRuns())
	if err != nil || facts.verified != nil || facts.reason == "" || facts.counts == nil {
		t.Fatalf("empty ledger = %+v err=%v, want a zero count, a null verdict and a reason", facts, err)
	}
}

// A budget source that fails says so beside the cost; it never reads as "no budget".
func TestRunFactsCostSaysWhyWhenTheBudgetCannotBeRead(t *testing.T) {
	got := factCost(&runDTO{}, SessionBudgetLimit{}, errors.New("finops down"))
	if got.LimitMicroUSD != nil || got.Reason != factReasonNoCost+"; the budget limit could not be read" {
		t.Fatalf("cost = %+v", got)
	}
	cost := int64(1_420_000)
	if got := factCost(&runDTO{CostMicroUSD: &cost}, SessionBudgetLimit{}, errors.New("finops down")); got.Reason != "the budget limit could not be read" {
		t.Fatalf("priced cost with a failed budget read = %+v", got)
	}
}

// What a session reaches, and why when part of it could not be read: a session never
// reads "reaches no host" because its profile could not be read.
func TestRunFactsReaches(t *testing.T) {
	roWS := model.Record{colWsRef: "ws-ro", colWsMountMode: mountRO}
	foldersWS := model.Record{colWsRef: "ws-folders", colWsMountMode: "rw", colWsReadOnlyFolders: `["/srv/shared","/srv/docs"]`}
	badWS := model.Record{colWsRef: "ws-bad", colWsReadOnlyFolders: `["relative"]`}
	anthropic := ProviderRecord{Ref: "rec-a", Kind: ProviderKindAnthropic}
	custom := ProviderRecord{Ref: "rec-c", Kind: ProviderKindOpenAI, BaseURL: "https://llm.example.com:8443/v1"}
	profiles := map[string]ProviderProfile{
		"pp-a":    {Ref: "pp-a", ProviderRecordRef: "rec-a", SessionEgressHosts: []string{"api.anthropic.com", "mcp.example.com"}},
		"pp-c":    {Ref: "pp-c", ProviderRecordRef: "rec-c"},
		"pp-gone": {Ref: "pp-gone", ProviderRecordRef: "rec-gone"},
	}
	records := map[string]ProviderRecord{"rec-a": anthropic, "rec-c": custom}
	workspaces := map[string]model.Record{"ws-ro": roWS, "ws-folders": foldersWS, "ws-bad": badWS}
	run := func(path, ws, profile string) model.Record {
		return model.Record{colRunWorkspacePath: path, colWorkspaceRef: ws, colRunProfileID: profile}
	}
	readErr := errors.New("store unavailable")
	cases := []struct {
		name    string
		rec     model.Record
		look    factLookups
		folders []runReachFolderDTO
		hosts   []string
		reason  string
	}{
		{"read-only mount", run("/w/a", "ws-ro", ""), factLookups{workspaces: workspaces},
			[]runReachFolderDTO{{"/w/a", "read"}}, []string{}, factReasonNoProfile},
		{"read-only folders of the workspace", run("/w/a", "ws-folders", ""), factLookups{workspaces: workspaces},
			[]runReachFolderDTO{{"/w/a", "write"}, {"/srv/shared", "read"}, {"/srv/docs", "read"}}, []string{}, factReasonNoProfile},
		{"bound record's vendor endpoint, then the profile's hosts once each", run("/w/a", "", "pp-a"), factLookups{profiles: profiles, records: records},
			[]runReachFolderDTO{{"/w/a", "write"}}, []string{"api.anthropic.com", "mcp.example.com"}, ""},
		{"bound record's own base URL", run("/w/a", "", "pp-c"), factLookups{profiles: profiles, records: records},
			[]runReachFolderDTO{{"/w/a", "write"}}, []string{"llm.example.com:8443"}, ""},
		{"profile unreadable", run("/w/a", "", "pp-a"), factLookups{profilesErr: readErr},
			[]runReachFolderDTO{{"/w/a", "write"}}, []string{}, "the provider profile could not be read"},
		{"profile deleted", run("/w/a", "", "pp-deleted"), factLookups{profiles: profiles},
			[]runReachFolderDTO{{"/w/a", "write"}}, []string{}, "the provider profile no longer exists"},
		{"provider record unreadable", run("/w/a", "", "pp-c"), factLookups{profiles: profiles, recordsErr: readErr},
			[]runReachFolderDTO{{"/w/a", "write"}}, []string{}, "the provider record could not be read"},
		{"provider record deleted", run("/w/a", "", "pp-gone"), factLookups{profiles: profiles, records: records},
			[]runReachFolderDTO{{"/w/a", "write"}}, []string{}, "the provider record no longer exists"},
		{"workspace unreadable", run("/w/a", "ws-ro", "pp-c"), factLookups{profiles: profiles, records: records, workspacesErr: readErr},
			[]runReachFolderDTO{{"/w/a", "unknown"}}, []string{"llm.example.com:8443"}, "the workspace could not be read"},
		{"workspace deleted", run("/w/a", "ws-deleted", "pp-c"), factLookups{profiles: profiles, records: records, workspaces: workspaces},
			[]runReachFolderDTO{{"/w/a", "unknown"}}, []string{"llm.example.com:8443"}, "the workspace no longer exists"},
		{"workspace folders invalid", run("/w/a", "ws-bad", "pp-c"), factLookups{profiles: profiles, records: records, workspaces: workspaces},
			[]runReachFolderDTO{{"/w/a", "write"}}, []string{"llm.example.com:8443"}, "the workspace's read-only folders could not be read"},
		{"folder not recorded", run("", "", "pp-c"), factLookups{profiles: profiles, records: records},
			[]runReachFolderDTO{}, []string{"llm.example.com:8443"}, factReasonFolderAbsent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := factReaches(tc.rec, tc.look)
			if !slices.Equal(got.Folders, tc.folders) || !slices.Equal(got.Hosts, tc.hosts) || got.Reason != tc.reason {
				t.Fatalf("reaches = %+v, want folders %v hosts %v reason %q", got, tc.folders, tc.hosts, tc.reason)
			}
		})
	}
}

// runs_as says why when it carries no account.
func TestRunFactsRunsAsSaysWhyWithoutAnAccount(t *testing.T) {
	profiles := map[string]ProviderProfile{"pp": {Ref: "pp", AccountName: "Lena's Claude account"}}
	cases := []struct {
		name, profile string
		look          factLookups
		account       string
		reason        string
	}{
		{"bound", "pp", factLookups{profiles: profiles}, "Lena's Claude account", ""},
		{"no profile", "", factLookups{}, "", factReasonNoProfile},
		{"unreadable", "pp", factLookups{profilesErr: errors.New("down")}, "", "the provider profile could not be read"},
		{"deleted", "pp-gone", factLookups{profiles: profiles}, "", "the provider profile no longer exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := factRunsAs("claude", model.Record{colRunProfileID: tc.profile}, tc.look)
			if got.Account != tc.account || got.Reason != tc.reason {
				t.Fatalf("runs_as = %+v, want account %q reason %q", got, tc.account, tc.reason)
			}
		})
	}
}

// acts_for names the person a session acts for, and says why when it cannot.
func TestRunFactsActsForSaysWhyWithoutAName(t *testing.T) {
	launched := model.Record{colRunLauncherUserID: "u"}
	cases := []struct {
		name   string
		m      *Module
		rec    model.Record
		users  map[string]string
		err    error
		kind   string
		person string
		reason string
	}{
		{"named", New(WithSessionFacts(factsSource{})), launched, map[string]string{"u": "Lena Fischer"}, nil, actsForUser, "Lena Fischer", ""},
		{"no display name", New(WithSessionFacts(factsSource{})), launched, map[string]string{"u": ""}, nil, actsForUser, "", "the account has no display name"},
		{"not a member any more", New(WithSessionFacts(factsSource{})), launched, map[string]string{}, nil, actsForUser, "", "the account no longer exists"},
		{"unreadable", New(WithSessionFacts(factsSource{})), launched, nil, errors.New("down"), actsForUser, "", "the account could not be read"},
		{"no source", New(), launched, nil, nil, actsForUser, "", factReasonNoFacts},
		{"an agent", New(), model.Record{colRunAgentRef: "agent-1"}, nil, nil, actsForAutomation, "", ""},
		{"a work item", New(), model.Record{colRunWorkItemID: "wi-1"}, nil, nil, actsForAutomation, "", ""},
		{"no launcher record", New(), model.Record{}, nil, nil, actsForUnknown, "", "the session predates the launcher record"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.m.factActsFor(tc.rec, tc.users, tc.err)
			if got.Kind != tc.kind || got.Name != tc.person || got.Reason != tc.reason {
				t.Fatalf("acts_for = %+v, want %s %q reason %q", got, tc.kind, tc.person, tc.reason)
			}
		})
	}
}
