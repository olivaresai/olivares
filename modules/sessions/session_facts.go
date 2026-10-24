// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The six facts a console shows about a session: who it acts for, what it runs as,
// what it reaches, what it costs, what was approved and what proves it. They are
// ADDED to the run read (list and detail) and nothing else about that read changes.
//
// Every fact is a projection of data the engine already holds. Where a fact cannot be
// read, the fact is present with a `reason` (or a null value beside one) and never a
// default: "no cost reported" is not 0, and an unverified chain is not a verified one.
// The facts ride only the two reads; a control reply (stop, resume) omits them, as it
// omits the caller's pin.

// SessionFactsSource supplies the facts about a session that live outside this module.
// It is optional: unwired, the name of a user and the count of approved actions are
// reported unavailable with a reason, and everything the module itself holds is still
// returned.
type SessionFactsSource interface {
	// UserNames maps user ids to the display name each account carries, empty when it
	// has none. An id with no readable account is absent from the answer.
	UserNames(ctx context.Context, tenant model.TenantID, userIDs []string) (map[string]string, error)
	// ApprovedActions maps canonical session ids to how many of the session's actions a
	// person approved. A session with none is absent from the answer.
	ApprovedActions(ctx context.Context, tenant model.TenantID, sessionIDs []string) (map[string]int64, error)
	// BudgetLimits maps each subject's Key to the limit of the most restrictive budget
	// scoped to that session or to its agent identity. A subject no budget scopes is
	// absent from the answer.
	BudgetLimits(ctx context.Context, tenant model.TenantID, subjects []SessionBudgetSubject) (map[string]SessionBudgetLimit, error)
}

// SessionBudgetSubject names one session whose budget limit is asked: Key is the run's
// ref, SessionRef its canonical session id and AgentRef the agent identity that drove it.
type SessionBudgetSubject struct {
	Key        string
	SessionRef string
	AgentRef   string
}

// SessionBudgetLimit is a budget's limit in micro-USD and the period it is counted over
// (daily, weekly, monthly or total).
type SessionBudgetLimit struct {
	LimitMicroUSD int64
	Period        string
}

// WithSessionFacts wires the source of the facts the run read takes from outside this
// module.
func WithSessionFacts(src SessionFactsSource) Option {
	return func(m *Module) {
		if src != nil {
			m.rt.SessionFacts = src
		}
	}
}

// Who the session acts for. Kind is user (a person launched it), automation (a work
// item or an agent dispatched it) or unknown (a session that predates the record).
type runActsForDTO struct {
	Kind   string `json:"kind"`
	UserID string `json:"user_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// What the session runs as: the tool and the provider account it signs in with.
type runRunsAsDTO struct {
	Tool       string `json:"tool"`
	ToolName   string `json:"tool_name,omitempty"`
	ProfileRef string `json:"profile_ref,omitempty"`
	Account    string `json:"account,omitempty"`
	Label      string `json:"label"`
	Reason     string `json:"reason,omitempty"`
}

type runReachFolderDTO struct {
	Path   string `json:"path"`
	Access string `json:"access"`
}

// What the session reaches: its folders, and the hosts it may connect to besides the
// engine itself (the bound provider's endpoint, then the profile's declared hosts).
type runReachesDTO struct {
	Folders []runReachFolderDTO `json:"folders"`
	Hosts   []string            `json:"hosts"`
	Reason  string              `json:"reason,omitempty"`
}

// What the session has cost so far, priced from the tokens its tool reported. MicroUSD
// is null, with a Reason, when the tool reported nothing to price.
type runCostDTO struct {
	MicroUSD *int64 `json:"micro_usd"`
	Reason   string `json:"reason,omitempty"`
	// LimitMicroUSD and LimitPeriod are present only when a budget scoped to this
	// session, or to the agent identity that drove it, applies: its limit and the
	// period it is counted over.
	LimitMicroUSD *int64 `json:"limit_micro_usd,omitempty"`
	LimitPeriod   string `json:"limit_period,omitempty"`
}

// Actions of the session a person approved, and actions waiting for one. Approved is
// null, with a Reason, when no approval source is wired or it could not be read.
type runApprovalsDTO struct {
	Approved *int64 `json:"approved"`
	Waiting  int64  `json:"waiting"`
	Reason   string `json:"reason,omitempty"`
}

// The audit ledger's evidence for the session: how many entries record it (they target
// its row, name its run or session in their meta, or its session identity wrote them),
// and whether the tenant's hash chain verifies. Both are null, with a Reason, when the
// ledger could not be read.
type runProofDTO struct {
	LedgerEntries *int64 `json:"ledger_entries"`
	ChainVerified *bool  `json:"chain_verified"`
	Reason        string `json:"reason,omitempty"`
}

const (
	actsForUser       = "user"
	actsForAutomation = "automation"
	actsForUnknown    = "unknown"

	factReasonNoCost       = "no cost reported"
	factReasonNoPrice      = "tokens reported, but the model has no list price"
	factReasonNoFacts      = "no source is wired for this fact"
	factReasonFolderAbsent = "the session's folder was not recorded"
	factReasonNoProfile    = "the session runs without a provider profile"
	factReasonNoLedger     = "the ledger could not be read"

	factsBatch = 200
)

var errLedgerMetaUnreadable = errors.New("sessions: the audit ledger does not expose its stored meta")

// runResolver maps values of one run column (run_ref or claim_sid) to the row ids of the
// runs that carry them. The ledger tail keeps counts only for runs that exist, so a value
// a caller wrote into an entry's meta never becomes state of its own.
type runResolver func(ctx context.Context, col string, values []string) (map[string][]string, error)

// ledgerFacts is the ledger part of a page's proof: counts by run row id (nil when the
// entries could not be read), whether the chain verifies (nil when unknown) and why.
type ledgerFacts struct {
	counts   map[string]int64
	verified *bool
	reason   string
}

// ledgerTail is what this engine has read of one tenant's audit ledger: the newest
// entry it saw (seq and hash), the first break found up to it, and the entries counted
// per session run (by the run's row id). A read verifies and counts only the entries
// appended since, anchored on that newest entry, so its cost follows the tenant's new
// activity and never the age of its ledger or of the oldest session on the page. The
// first read after the engine starts walks the chain once.
//
// shortcut: an entry is checked when this engine first reads it, and a later in-place
// change below the newest entry is not seen until the engine restarts; `olivares audit
// verify` is what re-walks the whole chain. A break, once found, is kept until restart.
type ledgerTail struct {
	mu      sync.Mutex
	seq     int64
	hash    []byte
	breakAt int64
	counts  map[string]int64
}

// runFactsCache holds one ledgerTail per tenant. Its own lock guards only the map; a
// read walking one tenant's new entries holds that tenant's lock, never another's.
type runFactsCache struct {
	mu     sync.Mutex
	tenant map[model.TenantID]*ledgerTail
}

func (c *runFactsCache) tail(tenant model.TenantID) *ledgerTail {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tenant[tenant]
	if !ok {
		if c.tenant == nil {
			c.tenant = map[model.TenantID]*ledgerTail{}
		}
		t = &ledgerTail{counts: map[string]int64{}}
		c.tenant[tenant] = t
	}
	return t
}

// factLookups is what the page's runs point to, read once for the whole page, with the
// error of each read so a fact says why when its part could not be read.
type factLookups struct {
	profiles      map[string]ProviderProfile
	profilesErr   error
	records       map[string]ProviderRecord
	recordsErr    error
	workspaces    map[string]model.Record
	workspacesErr error
}

// profile is the run's provider profile, or why there is none to show.
func (l factLookups) profile(rec model.Record) (ProviderProfile, string) {
	ref := rec.String(colRunProfileID)
	if ref == "" {
		return ProviderProfile{}, factReasonNoProfile
	}
	if p, ok := l.profiles[ref]; ok {
		return p, ""
	}
	if l.profilesErr != nil {
		return ProviderProfile{}, "the provider profile could not be read"
	}
	return ProviderProfile{}, "the provider profile no longer exists"
}

// attachRunFacts fills the facts the module itself holds (runs_as, reaches, proof) of
// every run in items, inside the read's transaction, reading each kind of row once for
// the whole page. recs[i] is the stored row items[i] was projected from.
func (m *Module) attachRunFacts(ctx context.Context, sc store.Scope, tenant model.TenantID, items []runDTO, recs []model.Record) {
	if len(items) == 0 {
		return
	}
	var look factLookups
	look.profiles, look.profilesErr = m.factProfiles(ctx, sc, recs)
	look.records, look.recordsErr = m.factProviderRecords(ctx, sc, look.profiles)
	look.workspaces, look.workspacesErr = m.factWorkspaces(ctx, sc, recs)
	ledger, ledgerErr := m.factLedger(ctx, sc.Audit(), tenant, recs, runsByColumn(sc))
	if err := errors.Join(look.profilesErr, look.recordsErr, look.workspacesErr, ledgerErr); err != nil {
		m.warnf("sessions: part of the session facts could not be read; each says why", "tenant", tenant.String(), "err", err)
	}
	for i := range items {
		rec, dto := recs[i], &items[i]
		dto.RunsAs = factRunsAs(dto.ProviderDriver, rec, look)
		dto.Reaches = factReaches(rec, look)
		dto.Proof = factProof(rec, ledger)
	}
}

// attachSourcedFacts fills the facts read from outside the module (acts_for, cost,
// approvals) AFTER the read's transaction has ended: each source opens a transaction of
// its own, and inside the read's one it would wait on it (SQLite has one connection).
func (m *Module) attachSourcedFacts(ctx context.Context, tenant model.TenantID, items []runDTO, recs []model.Record) {
	if len(items) == 0 {
		return
	}
	users, usersErr := m.factUserNames(ctx, tenant, recs)
	approved, approvedErr := m.factApproved(ctx, tenant, recs)
	budgets, budgetsErr := m.factBudgets(ctx, tenant, recs)
	if err := errors.Join(usersErr, approvedErr, budgetsErr); err != nil {
		m.warnf("sessions: part of the session facts could not be read; each says why", "tenant", tenant.String(), "err", err)
	}
	for i := range items {
		rec, dto := recs[i], &items[i]
		dto.ActsFor = m.factActsFor(rec, users, usersErr)
		dto.Cost = factCost(dto, budgets[rec.String(colRunRef)], budgetsErr)
		dto.Approvals = m.factApprovals(rec, approved, approvedErr)
	}
}

func (m *Module) factActsFor(rec model.Record, users map[string]string, err error) *runActsForDTO {
	user := rec.String(colRunLauncherUserID)
	switch {
	case user != "":
		out := &runActsForDTO{Kind: actsForUser, UserID: user}
		switch name, ok := users[user]; {
		case ok && name != "":
			out.Name = name
		case ok:
			out.Reason = "the account has no display name"
		case m.rt.SessionFacts == nil:
			out.Reason = factReasonNoFacts
		case err != nil:
			out.Reason = "the account could not be read"
		default:
			out.Reason = "the account no longer exists"
		}
		return out
	case rec.String(colRunWorkItemID) != "" || rec.String(colRunAgentRef) != "":
		return &runActsForDTO{Kind: actsForAutomation}
	}
	return &runActsForDTO{Kind: actsForUnknown, Reason: "the session predates the launcher record"}
}

func factRunsAs(driver string, rec model.Record, look factLookups) *runRunsAsDTO {
	out := &runRunsAsDTO{Tool: driver}
	if facts, ok := driverfacts.Lookup(driver); ok {
		out.ToolName = facts.Name
	}
	name := out.ToolName
	if name == "" {
		name = driver
	}
	out.Label = name
	profile, why := look.profile(rec)
	out.ProfileRef = rec.String(colRunProfileID)
	if why != "" {
		out.Reason = why
	} else {
		out.Account = profile.AccountName
		if out.Account == "" {
			out.Account = profile.DisplayName
		}
		if out.Account != "" {
			out.Label = name + " · " + out.Account
		}
	}
	return out
}

func factReaches(rec model.Record, look factLookups) *runReachesDTO {
	out := &runReachesDTO{Folders: []runReachFolderDTO{}, Hosts: []string{}}
	var why []string
	wsRef := rec.String(colWorkspaceRef)
	ws, registered := look.workspaces[wsRef]
	switch {
	case wsRef == "" || registered:
	case look.workspacesErr != nil:
		why = append(why, "the workspace could not be read")
	default:
		why = append(why, "the workspace no longer exists")
	}
	if root := rec.String(colRunWorkspacePath); root == "" {
		why = append(why, factReasonFolderAbsent)
	} else {
		access := "write"
		switch {
		case wsRef != "" && !registered:
			access = "unknown"
		case registered && ws.String(colWsMountMode) == mountRO:
			access = "read"
		}
		out.Folders = append(out.Folders, runReachFolderDTO{Path: root, Access: access})
	}
	if registered {
		folders, err := decodeWorkspaceReadOnlyFolders(ws)
		if err != nil {
			why = append(why, "the workspace's read-only folders could not be read")
		}
		for _, path := range folders {
			out.Folders = append(out.Folders, runReachFolderDTO{Path: path, Access: "read"})
		}
	}
	profile, noProfile := look.profile(rec)
	if noProfile != "" {
		why = append(why, noProfile)
	} else if ref := profile.ProviderRecordRef; ref != "" {
		record, ok := look.records[ref]
		switch {
		case ok:
			if endpoint, ok := recordEndpoint(record); ok {
				if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
					out.Hosts = append(out.Hosts, u.Host)
				}
			}
		case look.recordsErr != nil:
			why = append(why, "the provider record could not be read")
		default:
			why = append(why, "the provider record no longer exists")
		}
	}
	for _, host := range profile.SessionEgressHosts {
		if !slices.Contains(out.Hosts, host) {
			out.Hosts = append(out.Hosts, host)
		}
	}
	out.Reason = strings.Join(why, "; ")
	return out
}

// factCost reads the cost the run already carries (priced at list price when the tool
// reported tokens and no money, runtime_usage.go) and says why when there is none.
func factCost(dto *runDTO, budget SessionBudgetLimit, budgetErr error) *runCostDTO {
	out := &runCostDTO{Reason: factReasonNoCost}
	switch {
	case dto.CostMicroUSD != nil:
		cost := *dto.CostMicroUSD
		out.MicroUSD, out.Reason = &cost, ""
	case dto.InputTokens != nil || dto.OutputTokens != nil:
		out.Reason = factReasonNoPrice
	}
	if budget.LimitMicroUSD > 0 {
		limit := budget.LimitMicroUSD
		out.LimitMicroUSD, out.LimitPeriod = &limit, budget.Period
	}
	if budgetErr != nil {
		out.Reason = strings.Join(slices.DeleteFunc([]string{out.Reason, "the budget limit could not be read"}, func(r string) bool { return r == "" }), "; ")
	}
	return out
}

func (m *Module) factApprovals(rec model.Record, approved map[string]int64, err error) *runApprovalsDTO {
	out := &runApprovalsDTO{Waiting: int64(len(m.pendingRunApprovals(rec)))}
	switch sid := rec.String(colRunClaimSID); {
	case m.rt.SessionFacts == nil:
		out.Reason = factReasonNoFacts
	case err != nil:
		out.Reason = "the approvals could not be read"
	case sid == "":
		zero := int64(0)
		out.Approved = &zero
	default:
		count := approved[sid]
		out.Approved = &count
	}
	return out
}

func factProof(rec model.Record, ledger ledgerFacts) *runProofDTO {
	out := &runProofDTO{ChainVerified: ledger.verified, Reason: ledger.reason}
	if ledger.counts != nil {
		count := ledger.counts[rec.String(model.ColID)]
		out.LedgerEntries = &count
	}
	return out
}

// factProfiles reads the provider profiles of the page's runs in one query.
func (m *Module) factProfiles(ctx context.Context, sc store.Scope, recs []model.Record) (map[string]ProviderProfile, error) {
	refs := distinctColumn(recs, colRunProfileID)
	out := make(map[string]ProviderProfile, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	repo, err := sc.Ext(providerProfileKind)
	if err != nil {
		return out, err
	}
	for _, chunk := range slices.Collect(slices.Chunk(refs, factsBatch)) {
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{Column: colPPRef, Op: model.OpIn, Value: chunk}}, Limit: factsBatch})
		if err != nil {
			return out, err
		}
		for _, row := range rows {
			p := profileFromRecord(row)
			out[p.Ref] = p
		}
	}
	return out, nil
}

// factProviderRecords reads the provider records the page's profiles are bound to.
func (m *Module) factProviderRecords(ctx context.Context, sc store.Scope, profiles map[string]ProviderProfile) (map[string]ProviderRecord, error) {
	var refs []string
	for _, p := range profiles {
		if p.ProviderRecordRef != "" && !slices.Contains(refs, p.ProviderRecordRef) {
			refs = append(refs, p.ProviderRecordRef)
		}
	}
	out := make(map[string]ProviderRecord, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	repo, err := sc.Ext(providerRecordKind)
	if err != nil {
		return out, err
	}
	for _, chunk := range slices.Collect(slices.Chunk(refs, factsBatch)) {
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{Column: colPRRef, Op: model.OpIn, Value: chunk}}, Limit: factsBatch})
		if err != nil {
			return out, err
		}
		for _, row := range rows {
			r := providerRecordFromRecord(row)
			out[r.Ref] = r
		}
	}
	return out, nil
}

// factWorkspaces reads the registered workspaces the page's runs named.
func (m *Module) factWorkspaces(ctx context.Context, sc store.Scope, recs []model.Record) (map[string]model.Record, error) {
	refs := distinctColumn(recs, colWorkspaceRef)
	out := make(map[string]model.Record, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	repo, err := sc.Ext(workspaceKind)
	if err != nil {
		return out, err
	}
	for _, chunk := range slices.Collect(slices.Chunk(refs, factsBatch)) {
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{Column: colWsRef, Op: model.OpIn, Value: chunk}}, Limit: factsBatch})
		if err != nil {
			return out, err
		}
		for _, row := range rows {
			out[row.String(colWsRef)] = row
		}
	}
	return out, nil
}

func (m *Module) factUserNames(ctx context.Context, tenant model.TenantID, recs []model.Record) (map[string]string, error) {
	if m.rt.SessionFacts == nil {
		return nil, nil
	}
	ids := distinctColumn(recs, colRunLauncherUserID)
	if len(ids) == 0 {
		return nil, nil
	}
	return m.rt.SessionFacts.UserNames(ctx, tenant, ids)
}

func (m *Module) factApproved(ctx context.Context, tenant model.TenantID, recs []model.Record) (map[string]int64, error) {
	if m.rt.SessionFacts == nil {
		return nil, nil
	}
	sids := distinctColumn(recs, colRunClaimSID)
	if len(sids) == 0 {
		return nil, nil
	}
	return m.rt.SessionFacts.ApprovedActions(ctx, tenant, sids)
}

// factBudgets reads, in one call for the page, the budget limit that applies to each
// run. An unwired source shows no limit; a source that fails shows none either, and the
// cost says why, so a missing limit is never read as "unlimited".
func (m *Module) factBudgets(ctx context.Context, tenant model.TenantID, recs []model.Record) (map[string]SessionBudgetLimit, error) {
	if m.rt.SessionFacts == nil {
		return nil, nil
	}
	subjects := make([]SessionBudgetSubject, 0, len(recs))
	for _, rec := range recs {
		subjects = append(subjects, SessionBudgetSubject{Key: rec.String(colRunRef), SessionRef: rec.String(colRunClaimSID), AgentRef: rec.String(colRunAgentRef)})
	}
	return m.rt.SessionFacts.BudgetLimits(ctx, tenant, subjects)
}

// factLedger brings the tenant's ledger tail up to the head this read sees, then returns
// each run's entry count, keyed by the run's row id, and whether the chain verifies.
func (m *Module) factLedger(ctx context.Context, audit store.AuditLog, tenant model.TenantID, recs []model.Record, resolve runResolver) (ledgerFacts, error) {
	walker, ok := audit.(store.CanonicalWalker)
	if !ok {
		return ledgerFacts{reason: factReasonNoLedger}, errLedgerMetaUnreadable
	}
	head, hasHead, err := audit.Head(ctx)
	if err != nil {
		return ledgerFacts{reason: factReasonNoLedger}, err
	}
	tail := m.rt.factsCache.tail(tenant)
	tail.mu.Lock()
	defer tail.mu.Unlock()
	switch {
	case hasHead && head.Seq > tail.seq:
		err = tail.advance(ctx, audit, walker, resolve)
	case tail.breakAt != 0 || tail.seq == 0:
	case hasHead && head.Seq == tail.seq:
		if !bytes.Equal(head.Hash, tail.hash) {
			tail.breakAt = head.Seq
		}
	default:
		// The head is behind the newest entry this engine read: a read on an older
		// snapshot, or rows removed from the end. The store's recorded head tells them
		// apart (Verify's tail-truncation check), at the cost of the rows after head.
		err = tail.checkShorter(ctx, audit, head.Seq)
	}
	verified := tail.seq > 0 && tail.breakAt == 0
	switch {
	case err != nil && tail.breakAt != 0:
		// The walk could not read past a break the verification found: the chain is
		// known broken, the count is not.
		return ledgerFacts{verified: &verified, reason: "the ledger could not be read past a break in the chain"}, err
	case err != nil:
		return ledgerFacts{reason: factReasonNoLedger}, err
	case tail.seq == 0:
		return ledgerFacts{counts: map[string]int64{}, reason: "the ledger holds no entry to verify"}, nil
	}
	counts := make(map[string]int64, len(recs))
	for _, rec := range recs {
		id := rec.String(model.ColID)
		counts[id] = tail.counts[id]
	}
	return ledgerFacts{counts: counts, verified: &verified}, nil
}

// checkShorter verifies from a head behind the tail's newest entry, so a ledger whose
// newest rows were removed reads broken rather than verified.
func (t *ledgerTail) checkShorter(ctx context.Context, audit store.AuditLog, headSeq int64) error {
	report, err := audit.Verify(ctx, max(headSeq, 1))
	if err != nil {
		return err
	}
	if !report.OK {
		t.breakAt = max(report.BreakAt, 1)
	}
	return nil
}

// advance verifies and counts the entries after the tail's newest one. The verification
// re-hashes from that entry, anchored on the one before it, and the walk checks that
// entry's hash is still the one the last read saw: rewritten, it reads as a break. The
// counts and the new head are committed only when the whole walk and the run lookup
// succeed; a break the verification found is kept even when the walk then fails on it.
func (t *ledgerTail) advance(ctx context.Context, audit store.AuditLog, walker store.CanonicalWalker, resolve runResolver) error {
	from := max(t.seq, 1)
	breakAt := t.breakAt
	if breakAt == 0 {
		report, err := audit.Verify(ctx, from)
		if err != nil {
			return err
		}
		if !report.OK {
			breakAt = max(report.BreakAt, from)
		}
	}
	seq, hash, anchored := t.seq, t.hash, t.seq == 0
	byKey := map[ledgerKey]int64{}
	err := walker.WalkCanonical(ctx, from, func(ev model.AuditEvent, meta string, _ []byte) error {
		if !anchored {
			anchored = true
			if ev.Seq != t.seq || !bytes.Equal(ev.Hash, t.hash) {
				breakAt = cmp.Or(breakAt, t.seq)
			}
			if ev.Seq <= t.seq {
				return nil
			}
		}
		if key := ledgerEventKey(ev, meta); key != (ledgerKey{}) {
			byKey[key]++
		}
		seq, hash = ev.Seq, ev.Hash
		return nil
	})
	if err == nil && !anchored {
		// The whole walk ran and never reached the entry the last read saw: it is gone.
		// A walk that failed first found nothing.
		breakAt = cmp.Or(breakAt, t.seq)
	}
	var byRun map[string]int64
	if err == nil {
		byRun, err = resolveLedgerKeys(ctx, resolve, byKey)
	}
	if err != nil {
		t.breakAt = breakAt
		return err
	}
	for id, n := range byRun {
		t.counts[id] += n
	}
	t.seq, t.hash, t.breakAt = seq, hash, breakAt
	return nil
}

// ledgerKey is what one entry names its session by. Each entry has one key, so no entry
// counts twice for a run: the run row it targets, else the run named in its meta, else
// the session named in its meta, else the session identity that wrote it (a hook
// decision's actor "session:<sid>").
type ledgerKey struct {
	col   string // model.ColID, colRunRef or colRunClaimSID
	value string
}

func ledgerEventKey(ev model.AuditEvent, meta string) ledgerKey {
	if ev.TargetKind == runKind && !ev.TargetID.IsZero() {
		return ledgerKey{model.ColID, ev.TargetID.String()}
	}
	var refs struct {
		RunRef     string `json:"run_ref"`
		SessionRef string `json:"session_ref"`
	}
	if strings.Contains(meta, `_ref"`) {
		_ = json.Unmarshal([]byte(meta), &refs) // a meta not of this shape names no session
	}
	switch {
	case refs.RunRef != "":
		return ledgerKey{colRunRef, refs.RunRef}
	case refs.SessionRef != "":
		return ledgerKey{colRunClaimSID, refs.SessionRef}
	}
	if sid, ok := strings.CutPrefix(ev.Actor, "session:"); ok && sid != "" {
		return ledgerKey{colRunClaimSID, sid}
	}
	return ledgerKey{}
}

// resolveLedgerKeys turns counts by key into counts by run row id. A run_ref or session
// id that names no run of this tenant is dropped: it is not a session's entry.
func resolveLedgerKeys(ctx context.Context, resolve runResolver, byKey map[ledgerKey]int64) (map[string]int64, error) {
	out := map[string]int64{}
	values := map[string][]string{}
	for k, n := range byKey {
		if k.col == model.ColID {
			out[k.value] += n
			continue
		}
		values[k.col] = append(values[k.col], k.value)
	}
	for col, vals := range values {
		ids, err := resolve(ctx, col, vals)
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			for _, id := range ids[v] {
				out[id] += byKey[ledgerKey{col, v}]
			}
		}
	}
	return out, nil
}

// runsByColumn resolves run_ref and claim_sid values to run row ids in this scope.
func runsByColumn(sc store.Scope) runResolver {
	return func(ctx context.Context, col string, values []string) (map[string][]string, error) {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return nil, err
		}
		out := make(map[string][]string, len(values))
		for chunk := range slices.Chunk(values, factsBatch) {
			q := model.Query{Filters: []model.Filter{{Column: col, Op: model.OpIn, Value: chunk}}, Limit: factsBatch}
			for {
				rows, page, err := repo.List(ctx, q)
				if err != nil {
					return nil, err
				}
				for _, row := range rows {
					out[row.String(col)] = append(out[row.String(col)], row.String(model.ColID))
				}
				if !page.HasMore || page.Cursor == "" {
					break
				}
				q.Cursor = page.Cursor
			}
		}
		return out, nil
	}
}

// distinctColumn is the non-empty distinct values of one column across rows.
func distinctColumn(recs []model.Record, col string) []string {
	var out []string
	for _, rec := range recs {
		if v := strings.TrimSpace(rec.String(col)); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
