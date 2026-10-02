// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Retirement is what happens to an account in a tenant after the tenant removes
// it. The offboard excludes the account at once; then each module the
// composition declares removes, or reports, what it stores that names the
// account in that tenant. Only when every declared module is clean does the
// record become retired, and only a retired account can be re-admitted. The
// retirement pump drives the steps; this file is their contract.

// RetirementStep is one declared module's retirement step. It runs in its own
// tenant transaction and writes nothing in the auth partition. A step whose
// barrier finds the pinned authority moved answers an error wrapping
// store.ErrConflict, and the pass is then stale.
type RetirementStep interface {
	// Module names the declared module.
	Module() string
	// RetireUser removes the module's removable references to the account in the
	// tenant and reports every reference it may not remove, and every stored row
	// whose kind no registry knows.
	RetireUser(ctx context.Context, req RetirementRequest) (RetirementOutcome, error)
}

// RetirementRequest is what a step needs to know about the account.
type RetirementRequest struct {
	// Tenant is the tenant the account was removed from.
	Tenant model.TenantID
	// User is the removed account.
	User model.ID
	// Generation is the retirement generation the step runs for.
	Generation int64
	// UserAuthority is the account's authority version the pass read; the step's
	// transaction pins it, so a lift or a new offboard makes the step conflict.
	UserAuthority int64
	// Email and ExternalID are the account's aliases, for untyped text.
	Email      string
	ExternalID string
	// Credentials are the ids of every session and token the account holds or
	// held: the offboard revokes those bound to the tenant, and a permit may still
	// name one.
	Credentials []model.ID
}

// RetirementOutcome is one step's result.
type RetirementOutcome struct {
	// Blocking lists, by kind and id only, the stored rows that name the account
	// and that the tenant must resolve.
	Blocking []string
	// UnknownKinds lists the tables holding a row whose discriminator no registry
	// knows. The raw values go only to the deployment's own audit.
	UnknownKinds []string
	// FactVersion is the tenant fact version the step ran at.
	FactVersion int64
	// Stores names every store inspected, or whose absence could not be proved.
	Stores []RetirementStoreState
	// Limits states what the proof does not cover; limits do not imply erasure
	// and do not block an otherwise clean outcome.
	Limits []string
}

// RetirementStoreState records the custody and absence verdict for one store.
type RetirementStoreState struct {
	Store     string `json:"store"`
	State     string `json:"state"`               // clean, outside_custody, unreadable, unknown, offline, changing or legal_hold
	Retention string `json:"retention,omitempty"` // required for outside_custody
	// Holds names preservation duties over any subject (user, session or agent),
	// without retaining the held content or the hold's reason.
	Holds []RetirementHoldRef `json:"holds,omitempty"`
}

// RetirementHoldRef identifies a hold and its matter, never customer content.
type RetirementHoldRef struct {
	ID        string `json:"id"`
	MatterRef string `json:"matter_ref,omitempty"`
}

func (s RetirementStoreState) clean() bool {
	return strings.TrimSpace(s.Store) != "" && s.State == "clean" && len(s.Holds) == 0
}

// Clean reports whether the step found nothing that blocks the retirement.
func (o RetirementOutcome) Clean() bool {
	if len(o.Blocking) != 0 || len(o.UnknownKinds) != 0 {
		return false
	}
	for _, s := range o.Stores {
		if !s.clean() {
			return false
		}
	}
	return true
}

// SubjectExcluded reports whether user has an unlifted offboard in tenant.
// Ingest writers check once per write transaction, refuse exclusion or errors,
// and renew their fence-time writer lease before an absence read can complete.
// Each call reads the live record; no negative answer is cached.
func (a *Authenticator) SubjectExcluded(ctx context.Context, tenant model.TenantID, user model.ID) (bool, error) {
	var excluded bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		excluded, err = subjectExcluded(ctx, as, tenant, user)
		return err
	})
	return excluded, err
}

// SubjectExcludedIn reads the same live offboard fence as SubjectExcluded,
// within the caller's existing auth transaction. Ingest writers check once per
// write batch and refuse writes when the result is true or the read fails.
// It opens no AuthView and never caches a negative answer.
func SubjectExcludedIn(ctx context.Context, as store.AuthScope, tenant model.TenantID, user model.ID) (bool, error) {
	return subjectExcluded(ctx, as, tenant, user)
}

// subjectExcluded shares the live offboard predicate with an auth writer that
// already owns its transaction, without opening a nested AuthView.
func subjectExcluded(ctx context.Context, as store.AuthScope, tenant model.TenantID, user model.ID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if tenant == "" || user == "" {
		return false, errors.New("auth: subject exclusion requires a tenant and user")
	}
	rec, found, err := retirementRecord(ctx, as, user, tenant)
	return found && rec.RetirementState != model.RetirementLifted, err
}

// RetirementRecord returns the account's retirement record in tenant, and
// whether one exists.
func (a *Authenticator) RetirementRecord(ctx context.Context, user model.ID, tenant model.TenantID) (model.TenantExclusion, bool, error) {
	var (
		rec   model.TenantExclusion
		found bool
	)
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		rec, found, err = retirementRecord(ctx, as, user, tenant)
		return err
	})
	if err != nil {
		return model.TenantExclusion{}, false, err
	}
	return rec, found, nil
}

// retirementRecord reads the account's offboard record in tenant: the one row
// every retirement of the account from that tenant shares, whose generation
// counts them.
func retirementRecord(ctx context.Context, as store.AuthScope, user model.ID, tenant model.TenantID) (model.TenantExclusion, bool, error) {
	rows, err := drainList(ctx, as.TenantExclusions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return model.TenantExclusion{}, false, err
	}
	var (
		rec   model.TenantExclusion
		found bool
	)
	for _, x := range rows {
		if x.Kind != model.ExclusionOffboard || x.TargetTenantID != tenant {
			continue
		}
		if !found || x.RetirementGeneration > rec.RetirementGeneration {
			rec, found = x, true
		}
	}
	return rec, found, nil
}

// DueRetirements returns up to limit retirement records that are not settled
// (retiring or blocked) and whose next attempt is due at now, the earliest first.
func (a *Authenticator) DueRetirements(ctx context.Context, now model.Timestamp, limit int) ([]model.TenantExclusion, error) {
	var out []model.TenantExclusion
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		for _, state := range []model.RetirementState{model.RetirementRetiring, model.RetirementBlocked} {
			rows, err := drainList(ctx, as.TenantExclusions().List, byEq("retirement_state", string(state), 0))
			if err != nil {
				return err
			}
			for _, x := range rows {
				if x.Kind != model.ExclusionOffboard {
					continue
				}
				if x.NextAttemptAt == nil || !now.Before(*x.NextAttemptAt) {
					out = append(out, x)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return dueAt(out[i]).Before(dueAt(out[j])) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func dueAt(x model.TenantExclusion) model.Timestamp {
	if x.NextAttemptAt == nil {
		return model.Timestamp{}
	}
	return *x.NextAttemptAt
}

// RetirementBackoff is the delay before a pass that failed, or found the record
// blocked, is tried again: one minute, doubling with each attempt, at most one
// hour. What blocks a record is the tenant's to resolve, so a resolution is seen
// at the latest one hour after it is made.
func RetirementBackoff(attempts int64) time.Duration {
	d := time.Minute
	for i := int64(1); i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

// moduleResult is one declared module's entry in a record's module results.
type moduleResult struct {
	State  string                 `json:"state"`
	Fact   int64                  `json:"fact,omitempty"`
	Refs   []string               `json:"refs,omitempty"`
	Cause  string                 `json:"cause,omitempty"`
	Stores []RetirementStoreState `json:"stores,omitempty"`
	Limits []string               `json:"limits,omitempty"`
}

// RetirementProgress is what one retirement pass did to its record.
type RetirementProgress uint8

const (
	// RetirementSettled means the record was not waiting (retired, lifted or
	// absent), so no step ran and nothing was written.
	RetirementSettled RetirementProgress = iota + 1
	// RetirementAdvanced means the pass changed the record: its state, or what
	// blocks it.
	RetirementAdvanced
	// RetirementUnchanged means the pass found exactly what the record already
	// lists as blocking it, and wrote nothing.
	RetirementUnchanged
	// RetirementStale means the record, the account's authority version or the
	// tenant's epoch moved after the pass read them. Nothing was written; the next
	// pass reads afresh.
	RetirementStale
	// RetirementFailed means a step or the composition could not complete: the
	// attempt is counted and the next one scheduled later.
	RetirementFailed
)

// RetirementPass is the result of one retirement pass.
type RetirementPass struct {
	// Record is the record as the pass left it, or as it read it when the pass
	// wrote nothing.
	Record model.TenantExclusion
	// Progress says what the pass did.
	Progress RetirementProgress
}

// AdvanceRetirement runs one retirement pass for the account in tenant: every
// declared step in order, each in its own transaction, then the proof commit.
//
// The pass reads the record and the account's authority version first. Each
// step pins that version with the tenant's fact, and the proof commit pins it
// with the tenant's authorization epoch the steps reached, so a pass that read
// before a lift or a new offboard conflicts and writes nothing (Stale). The
// proof commits retired only when the composition is ready for an absence proof
// and every step found nothing; otherwise the record lists what blocks it and
// is not due again until a backoff has passed. A pass that finds what the record
// already lists writes nothing (Unchanged): the caller keeps the backoff. A step
// that fails leaves the record as it was, with the attempt counted and the next
// one scheduled later, and returns the step's error.
func (a *Authenticator) AdvanceRetirement(ctx context.Context, steps []RetirementStep, user model.ID, tenant model.TenantID) (RetirementPass, error) {
	var (
		rec   model.TenantExclusion
		found bool
		req   RetirementRequest
	)
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		rec, found, err = retirementRecord(ctx, as, user, tenant)
		if err != nil || !found || !retirementActive(rec) {
			return err
		}
		req, err = retirementRequestFor(ctx, as, rec)
		return err
	})
	if err != nil {
		return RetirementPass{Record: rec}, err
	}
	if !found || !retirementActive(rec) {
		return RetirementPass{Record: rec, Progress: RetirementSettled}, nil
	}
	results := make(map[string]moduleResult, len(steps))
	censusLimits := a.retirementCensusLimits()
	cause := a.compositionUnready()
	if cause == "" {
		cause = a.missingSteps(steps)
	}
	if cause != "" {
		results["composition"] = moduleResult{State: "incomplete", Cause: cause, Limits: censusLimits}
		return a.recordRetirementFailure(ctx, rec, req, results, errors.New(cause))
	}
	if len(censusLimits) != 0 {
		results["composition"] = moduleResult{State: "limited", Limits: censusLimits}
	}
	var (
		blocking []string
		epoch    int64
	)
	for _, step := range steps {
		out, err := step.RetireUser(ctx, req)
		if step.Module() == "composition" {
			// A real reader may use this name; keep its outcome and the census limits.
			out.Limits = append(append([]string(nil), censusLimits...), out.Limits...)
		}
		if errors.Is(err, store.ErrConflict) {
			// The step's barrier found the account's authority version or the
			// tenant's fact moved since the pass read them: the pass is stale, and
			// nothing it did committed.
			return RetirementPass{Record: rec, Progress: RetirementStale}, nil
		}
		if err != nil {
			results[step.Module()] = moduleResult{State: "failed", Cause: "the step did not complete", Stores: out.Stores, Limits: out.Limits}
			return a.recordRetirementFailure(ctx, rec, req, results, err)
		}
		refs := append(append([]string(nil), out.Blocking...), unknownKindRefs(out.UnknownKinds)...)
		refs = append(refs, storeBlockingRefs(out.Stores)...)
		state := "clean"
		if !out.Clean() {
			state = "blocked"
		}
		results[step.Module()] = moduleResult{State: state, Fact: out.FactVersion, Refs: refs, Stores: out.Stores, Limits: out.Limits}
		blocking = append(blocking, refs...)
		if out.FactVersion > epoch {
			epoch = out.FactVersion
		}
	}
	if epoch == 0 {
		if epoch, err = a.readAuthorizationEpoch(ctx, tenant); err != nil {
			return RetirementPass{Record: rec}, err
		}
	}
	return a.commitRetirementProof(ctx, rec, req, results, blocking, epoch)
}

// retirementActive reports whether a record still waits for its retirement.
func retirementActive(rec model.TenantExclusion) bool {
	return rec.Kind == model.ExclusionOffboard &&
		(rec.RetirementState == model.RetirementRetiring || rec.RetirementState == model.RetirementBlocked)
}

// unknownKindRefs is how a record lists the tables holding rows whose kind no
// registry knows; the raw values stay in the deployment's own audit.
func unknownKindRefs(tables []string) []string {
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		out = append(out, "unknown_kind:"+t)
	}
	return out
}

// storeBlockingRefs keeps every unproved store visible by name. Unknown or
// malformed verdicts remain blockers, including an unnamed supposedly clean store.
func storeBlockingRefs(stores []RetirementStoreState) []string {
	var refs []string
	for _, s := range stores {
		if s.clean() {
			continue
		}
		name, state := s.Store, s.State
		if strings.TrimSpace(name) == "" {
			name = "unnamed"
		}
		if state == "" {
			state = "unknown"
		}
		if len(s.Holds) != 0 {
			state = "legal_hold"
		}
		refs = append(refs, "store:"+name+":"+state)
		for _, hold := range s.Holds {
			refs = append(refs, "legal_hold:"+hold.ID+":matter:"+hold.MatterRef)
		}
	}
	return refs
}

// retirementRequestFor reads what the steps need about the account: its current
// authority version, its aliases and its credential ids.
func retirementRequestFor(ctx context.Context, as store.AuthScope, rec model.TenantExclusion) (RetirementRequest, error) {
	reader, ok := as.(store.AuthUserAuthorityEvidenceScope)
	if !ok {
		return RetirementRequest{}, errNoAuthorityReader
	}
	ref, err := reader.ReadUserAuthorityFact(ctx, rec.UserID)
	if err != nil {
		return RetirementRequest{}, err
	}
	u, err := as.Users().Get(ctx, rec.UserID)
	if err != nil {
		return RetirementRequest{}, err
	}
	req := RetirementRequest{
		Tenant: rec.TargetTenantID, User: rec.UserID, Generation: rec.RetirementGeneration,
		UserAuthority: ref.Version, Email: u.Email, ExternalID: u.ExternalID,
	}
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", rec.UserID.String(), 0))
	if err != nil {
		return RetirementRequest{}, err
	}
	for _, s := range sessions {
		req.Credentials = append(req.Credentials, s.ID)
	}
	tokens, err := drainList(ctx, as.Tokens().List, byEq("user_id", rec.UserID.String(), 0))
	if err != nil {
		return RetirementRequest{}, err
	}
	for _, t := range tokens {
		req.Credentials = append(req.Credentials, t.ID)
	}
	return req, nil
}

// compositionUnready returns the census cause when the composition cannot
// prove an absence, and "" when it can.
func (a *Authenticator) compositionUnready() string {
	census := a.census
	if census == nil {
		census, _ = a.st.(store.CompositionCensus)
	}
	if census == nil {
		return (store.Readiness{}).Cause()
	}
	if r := census.CompositionReadiness(); !r.Ready() {
		return r.Cause()
	}
	return ""
}

// retirementCensusLimits states the columns the same census explicitly excludes
// from content erasure. These limits are persisted, never counted as erased.
func (a *Authenticator) retirementCensusLimits() []string {
	census := a.census
	if census == nil {
		census, _ = a.st.(store.CompositionCensus)
	}
	if census == nil {
		return nil // compositionUnready already refuses an absent census
	}
	descriptors := append([]model.EntityDescriptor(nil), census.CensusDescriptors()...)
	if contributions, ok := census.(store.CompositionContributions); ok {
		for _, contribution := range contributions.CompositionContributions() {
			descriptors = append(descriptors, contribution.OutsideStores...)
		}
	}
	var limits []string
	for _, d := range descriptors {
		for _, col := range d.ThirdPartyTextColumns() {
			limits = append(limits, "text by others not scanned: "+string(d.Kind)+"."+col)
		}
	}
	return limits
}

// missingSteps returns the census cause when steps lack the step of a module the
// composition declares, as a retirement reader or as a module an edition
// expects, and "" when every such module has its step. The step list is the
// caller's; what must be in it is the store's record of the composition, so a
// declared module that is missing is never taken for one with nothing to find.
func (a *Authenticator) missingSteps(steps []RetirementStep) string {
	census := a.census
	if census == nil {
		census, _ = a.st.(store.CompositionCensus)
	}
	have := make(map[string]bool, len(steps))
	for _, step := range steps {
		if step != nil {
			have[step.Module()] = true
		}
	}
	var want []string
	if readers, ok := census.(store.CompositionReaders); ok {
		for module := range readers.CompositionReaders() {
			want = append(want, module)
		}
	}
	if contributions, ok := census.(store.CompositionContributions); ok {
		for _, c := range contributions.CompositionContributions() {
			want = append(want, c.Modules...)
		}
	}
	sort.Strings(want)
	for _, module := range want {
		if !have[module] {
			return "census_incomplete:" + module
		}
	}
	return ""
}

// readAuthorizationEpoch reads tenant's authorization epoch version.
func (a *Authenticator) readAuthorizationEpoch(ctx context.Context, tenant model.TenantID) (int64, error) {
	var version int64
	err := a.st.View(ctx, tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return errors.New("auth: the scope exposes no authorization epoch reader")
		}
		fact, err := reader.ReadAuthorizationEpoch(ctx)
		version = fact.Version
		return err
	})
	return version, err
}

// recordRetirementFailure counts a pass that could not complete and schedules the
// next one later. A record that moved on meanwhile (a new generation, a lift) is
// left alone. read is the record as the pass read it.
func (a *Authenticator) recordRetirementFailure(ctx context.Context, read model.TenantExclusion, req RetirementRequest, results map[string]moduleResult, cause error) (RetirementPass, error) {
	pass := RetirementPass{Record: read, Progress: RetirementFailed}
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(ctx, as); err != nil {
			return err
		}
		rec, found, err := retirementRecord(ctx, as, req.User, req.Tenant)
		if err != nil {
			return err
		}
		if !found || !retirementActive(rec) || rec.RetirementGeneration != req.Generation {
			pass = RetirementPass{Record: rec, Progress: RetirementStale}
			return nil
		}
		rec.Attempts++
		next := model.NewTimestamp(a.clock.Now().Time().Add(RetirementBackoff(rec.Attempts)))
		rec.NextAttemptAt = &next
		rec.ModuleResults = encodeModuleResults(results)
		updated, err := as.TenantExclusions().Update(ctx, rec)
		if err != nil {
			return err
		}
		pass.Record = updated
		return nil
	})
	if err != nil {
		return pass, err
	}
	return pass, cause
}

// commitRetirementProof is the proof commit: under the tenant's authorization
// epoch at epoch and the account's authority version the pass read, a record
// still at the pass's generation becomes retired when nothing blocks it, and
// blocked with the listed references otherwise, not due again until the backoff
// of its attempts has passed. A record already blocked by exactly those
// references and store evidence is not written again: the pass is Unchanged. A moved epoch or
// version, or a record that moved on, writes nothing: the pass is Stale. read is
// the record as the pass read it.
func (a *Authenticator) commitRetirementProof(ctx context.Context, read model.TenantExclusion, req RetirementRequest, results map[string]moduleResult, blocking []string, epoch int64) (RetirementPass, error) {
	var pass RetirementPass
	refs := strings.Join(blocking, ", ")
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		pass = RetirementPass{}
		barrier, ok := as.(store.AuthTenantAuthorityBarrier)
		if !ok {
			return errors.New("auth: the auth scope exposes no tenant authority barrier")
		}
		if err := barrier.LockAuthTenantAuthority(ctx, req.Tenant, store.AuthoritySnapshotBundle{
			Facts: []store.AuthorizationFactRef{{
				Kind: model.AuthorizationEpochKind, ID: model.ID(req.Tenant), Version: epoch,
			}},
			UserAuthorities: []store.UserAuthorityFactRef{{UserID: req.User, Version: req.UserAuthority}},
		}); err != nil {
			return err
		}
		rec, found, err := retirementRecord(ctx, as, req.User, req.Tenant)
		if err != nil {
			return err
		}
		if !found || !retirementActive(rec) || rec.RetirementGeneration != req.Generation {
			pass = RetirementPass{Record: rec, Progress: RetirementStale}
			return nil
		}
		if len(blocking) > 0 && rec.RetirementState == model.RetirementBlocked && rec.BlockingRefs == refs && sameRetirementResults(rec.ModuleResults, results) {
			pass = RetirementPass{Record: rec, Progress: RetirementUnchanged}
			return nil
		}
		rec.Attempts++
		rec.ModuleResults = encodeModuleResults(results)
		if len(blocking) == 0 {
			rec.RetirementState = model.RetirementRetired
			retired := epoch
			rec.RetiredEpoch = &retired
			rec.BlockingRefs = ""
			rec.NextAttemptAt = nil
		} else {
			rec.RetirementState = model.RetirementBlocked
			rec.BlockingRefs = refs
			next := model.NewTimestamp(a.clock.Now().Time().Add(RetirementBackoff(rec.Attempts)))
			rec.NextAttemptAt = &next
		}
		updated, err := as.TenantExclusions().Update(ctx, rec)
		if err != nil {
			return err
		}
		pass = RetirementPass{Record: updated, Progress: RetirementAdvanced}
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		// The pass read before a lift, a new offboard or a new grant, and nothing
		// it wrote committed: it is stale.
		return RetirementPass{Record: read, Progress: RetirementStale}, nil
	}
	if err != nil {
		return RetirementPass{Record: read}, err
	}
	return pass, nil
}

// sameRetirementResults compares observations without their transaction fact:
// a new epoch alone does not reset backoff, but changed custody, retention or
// stated limits must be recorded even when the same references still block.
func sameRetirementResults(previous string, results map[string]moduleResult) bool {
	var prior map[string]moduleResult
	if err := json.Unmarshal([]byte(previous), &prior); err != nil {
		return false
	}
	for module, result := range prior {
		result.Fact = 0
		prior[module] = result
	}
	current := make(map[string]moduleResult, len(results))
	for module, result := range results {
		result.Fact = 0
		current[module] = result
	}
	return encodeModuleResults(prior) == encodeModuleResults(current)
}

// encodeModuleResults renders a pass's per-module results for the record.
func encodeModuleResults(results map[string]moduleResult) string {
	b, err := json.Marshal(results)
	if err != nil {
		return ""
	}
	return string(b)
}

// UseCompositionCensus hands the authenticator the census of the store as it
// opened, when the store it was given wraps that one in guards that hide it. It
// is set once at boot.
func (a *Authenticator) UseCompositionCensus(c store.CompositionCensus) {
	a.census = c
}

// SetRetirementWaker installs the function every offboard through this
// authenticator calls to wake the retirement pump. It is set once at boot,
// before the authenticator serves; the waker belongs to the pump, lives as long
// as the engine that owns both, and must never block. A wake is only a signal to
// look for due records.
func (a *Authenticator) SetRetirementWaker(wake func()) {
	a.retirementWake = wake
}

// wakeRetirement wakes the installed pump, if any.
func (a *Authenticator) wakeRetirement() {
	if a.retirementWake != nil {
		a.retirementWake()
	}
}

// PinRetirement is a retirement step's first call in its tenant transaction. It
// pins the tenant's authorization epoch and the account's authority version the
// pass read, through the directory authority barrier, and returns the epoch
// version the step reports as its fact: a pass that read before a lift or a new
// offboard conflicts here and changes nothing.
func PinRetirement(ctx context.Context, sc store.Scope, req RetirementRequest) (int64, error) {
	fact, err := PinFence(ctx, sc, FenceAuthorization, []store.UserAuthorityFactRef{
		{UserID: req.User, Version: req.UserAuthority},
	})
	if err != nil {
		return 0, err
	}
	return fact.Version, nil
}

// RowsNaming reads every row of repo that matches filters and returns those
// whose column, read through the counted view of decl, names user. unknown
// reports a row whose discriminator no registry knows: such a row is never
// matched, and the step lists its table instead.
func RowsNaming(ctx context.Context, repo store.GenericRepo, decl *model.ColumnDecl, column string, user model.ID, filters ...model.Filter) (named []model.Record, unknown bool, err error) {
	return rowsNaming(ctx, repo, decl, column, map[model.ID]bool{user: true}, nil, filters...)
}

// RowsNamingAccount is RowsNaming for the account a retirement step runs for: a
// row names it when its column, read through the counted view of decl, names the
// account, or names one of the account's credentials, a token that owns the row
// in the account's stead.
func RowsNamingAccount(ctx context.Context, repo store.GenericRepo, decl *model.ColumnDecl, column string, req RetirementRequest, filters ...model.Filter) (named []model.Record, unknown bool, err error) {
	creds := make(map[model.ID]bool, len(req.Credentials))
	for _, c := range req.Credentials {
		creds[c] = true
	}
	return rowsNaming(ctx, repo, decl, column, map[model.ID]bool{req.User: true}, creds, filters...)
}

func rowsNaming(ctx context.Context, repo store.GenericRepo, decl *model.ColumnDecl, column string, users, creds map[model.ID]bool, filters ...model.Filter) (named []model.Record, unknown bool, err error) {
	q := model.Query{Filters: filters, Limit: 500}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, false, err
		}
		for _, rec := range recs {
			ids, err := decl.CountedUserIDs(rec, column)
			if errors.Is(err, model.ErrUnknownKind) {
				unknown = true
				continue
			}
			if err != nil {
				return nil, false, err
			}
			hit := false
			for _, id := range ids {
				hit = hit || users[id]
			}
			for _, id := range decl.CountedCredentialIDs(rec, column) {
				hit = hit || creds[id]
			}
			if hit {
				named = append(named, rec)
			}
		}
		if !page.HasMore || page.Cursor == "" {
			return named, unknown, nil
		}
		q.Cursor = page.Cursor
	}
}
