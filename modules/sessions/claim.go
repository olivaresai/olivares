// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SG-02 (core) — claim, lease and fencing over the canonical identity.
//
// Before this file the module had no notion of a session CLAIMING the right to
// work. The overlay's four routes are all GET (api.go), and what the module
// calls a "heartbeat" is an SSE keepalive on the operate stream
// (stream.go:19-21), not a lease on anything. Coordination therefore happened
// outside the product, in markdown.
//
// A claim is an ADMISSION decision, not an identity one: resolving a sid tells
// you which session this is, never that it may act. The two are deliberately
// separate planes — identity is a fact, admission is a grant that expires.

// Registered kind and table for the admission plane.
const (
	claimKind  model.Kind = "sessions.claim"
	claimTable            = "sessions_claim"
)

// ProviderOperated is the alias namespace for references OLIVARES ITSELF issues —
// today the run_ref of an operated launch. It is deliberately not "claude": a
// run_ref is not a Claude session id, and resolving both through one namespace
// would let two unrelated references collide into one canonical identity.
//
// SG-00 §6 is what this satisfies: an operated run promotes to canonical identity
// (origin = operated) because the plane launched it and knows its identity before
// anybody else, and the provider's own session id — captured later, off the stream
// — is bound as a SECOND alias onto the SAME sid.
const ProviderOperated = "olivares"

// sessions.claim columns.
const (
	colClaimSID     = "sid"
	colHolder       = "holder"
	colFence        = "fence"
	colClaimState   = "claim_state"
	colLeaseExpires = "lease_expires_at"
	colClaimedAt    = "claimed_at"
	colRenewedAt    = "renewed_at"
)

// Claim states. `expired` is DURABLE and ONE-WAY (F5): once anybody observes a
// lease lapsed, the row records it, so a clock that later moves BACKWARDS cannot
// resurrect authority that was already seen to be dead. Only a takeover leaves
// the state, and a takeover mints a new fence.
const (
	claimActive   = "active"
	claimReleased = "released"
	claimExpired  = "expired"
)

// defaultLeaseTTL bounds how long a claim survives without a heartbeat. It is
// deliberately short relative to a working session: the cost of a wrong answer
// here is a session that died holding authority nobody can take back.
const defaultLeaseTTL = 5 * time.Minute

// maxLeaseTTL caps a caller-supplied TTL. A lease long enough to outlive the
// process that holds it is not a lease, it is a lock with extra steps.
const maxLeaseTTL = 1 * time.Hour

// Admission-plane errors.
var (
	// ErrClaimHeld reports that another holder has a LIVE lease on the session.
	ErrClaimHeld = errors.New("sessions: claim held by another holder with a live lease")
	// ErrLeaseLost reports a heartbeat or write from a holder that no longer has
	// authority — its lease expired, it was released, or a newer holder fenced it
	// out. It is returned for a STALE FENCE even when the caller is still alive:
	// being alive is not the same as being in charge.
	ErrLeaseLost = errors.New("sessions: lease lost; authority belongs to a newer fence")
	// ErrNoClaim reports that a session has no claim at all.
	ErrNoClaim = errors.New("sessions: session holds no claim")
	// ErrNoHolder rejects a claim with no holder: an anonymous claim cannot be
	// fenced out, renewed or attributed.
	ErrNoHolder = errors.New("sessions: claim requires a holder")
	// ErrFenceExhausted reports that a takeover would have to wrap the fence, which
	// F9 refuses because a token that can go BACKWARDS is not a fencing token. It is a
	// sentinel rather than a bare message so callers can tell this business verdict
	// from a store that is merely unavailable — flattening the two together is what
	// P1-N1 was.
	ErrFenceExhausted = errors.New("sessions: fence exhausted")
)

// Lease is what a successful claim hands back. The fence is the caller's proof
// of authority for every subsequent write.
type Lease struct {
	// SID is the canonical session the lease is over.
	SID string
	// Holder is who holds it.
	Holder string
	// Fence is a per-session MONOTONIC token. It increases every time authority
	// changes hands and NEVER on a renewal, so a holder that keeps its lease
	// alive keeps one stable token, while a holder that lost it can be detected
	// by comparing numbers rather than by trusting it to notice.
	Fence int64
	// ExpiresAt is when the lease lapses without a heartbeat.
	ExpiresAt time.Time
}

// registerClaimSchema declares the admission entity. One claim row per session,
// enforced by the engine: two rows would mean two holders, which is exactly the
// state a lease exists to make impossible.
func (m *Module) registerClaimSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:                   claimKind,
		Table:                  claimTable,
		AuthorizationFact:      true,
		AuthorizationLockOrder: 40,
		AuthorizationLeaseFence: model.AuthorizationLeaseFenceSpec{
			SubjectColumn:  colClaimSID,
			FenceColumn:    colFence,
			StateColumn:    colClaimState,
			ActiveValue:    claimActive,
			DeadlineColumn: colLeaseExpires,
		},
		Fields: []model.FieldSpec{
			{Name: colClaimSID, Kind: model.KindText},
			{Name: colHolder, Kind: model.KindText},
			{Name: colFence, Kind: model.KindInt},
			{Name: colClaimState, Kind: model.KindText, Indexed: true},
			{Name: colLeaseExpires, Kind: model.KindTimestamp, Indexed: true},
			{Name: colClaimedAt, Kind: model.KindTimestamp},
			{Name: colRenewedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_claim_sid_uniq",
			Columns: []string{model.ColTenantID, colClaimSID},
			Unique:  true,
		}},
	})
}

// clampTTL bounds a caller-supplied lease duration.
func clampTTL(ttl time.Duration) time.Duration {
	return clampFenceTTL(ttl, fenceTTLPolicy{Default: defaultLeaseTTL, Max: maxLeaseTTL})
}

// Claim acquires or renews the working claim on a canonical session.
//
// The rules, in the order they are applied:
//
//	no row            -> mint the claim at fence 1
//	same holder, live -> renew, fence UNCHANGED (a heartbeat is not a new identity)
//	other holder, live-> ErrClaimHeld
//	lease lapsed      -> take over, fence INCREMENTED (the old holder is fenced out)
//	released          -> take over, fence INCREMENTED
//
// A lapsed lease loses authority whether or not its holder is still running.
// That is the whole point of a lease: liveness is asserted by renewal, never
// assumed from the absence of bad news.
func (m *Module) Claim(ctx context.Context, tenant model.TenantID, sid, holder string, ttl time.Duration) (Lease, error) {
	var out Lease
	// refusal is a denial DECIDED inside the transaction and DELIVERED after it
	// commits. See the exhausted-fence branch below for why the two are separated.
	var refusal error
	if holder == "" {
		return out, ErrNoHolder
	}
	if m.data == nil {
		return out, errors.New("sessions: no data handle")
	}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		refusal = nil // a re-entered callback must not inherit the last verdict
		// F6: the clock is read INSIDE the transaction. Reading it before the
		// Mutate meant a wait for the write lock could exceed the TTL, and the
		// transaction would then commit — and return — a lease already expired at
		// the instant it was written.
		now := m.clock.Now().Time()
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		rec, found, err := findClaim(ctx, sc, sid)
		if err != nil {
			return err
		}
		if !found {
			state, aerr := fenceAcquire(fenceState{}, holder, now, ttl,
				fenceTTLPolicy{Default: defaultLeaseTTL, Max: maxLeaseTTL})
			if aerr != nil {
				return aerr
			}
			created, cerr := repo.Create(ctx, model.Record{
				colClaimSID:     sid,
				colHolder:       state.Holder,
				colFence:        state.Fence,
				colClaimState:   claimActive,
				colLeaseExpires: model.NewTimestamp(state.ExpiresAt).String(),
				colClaimedAt:    model.NewTimestamp(state.AcquiredAt).String(),
			})
			if cerr != nil {
				return cerr
			}
			out = Lease{SID: sid, Holder: holder, Fence: created.Int(colFence), ExpiresAt: state.ExpiresAt}
			return nil
		}

		// F9 meets F5, and neither reordering nor a follow-up transaction is the
		// answer. A takeover from an exhausted fence must refuse; a refusal RETURNED
		// FROM HERE rolls the transaction back and takes any retirement with it, and
		// the first cut's fix — flag the lapse and retire in a SECOND transaction —
		// left process state between the two, which a crash or a clock that moves
		// backwards in the gap turns straight back into F5 (R3-01, third contrast).
		//
		// So the transaction is not doomed at all: nothing of the caller's has been
		// written yet, the retirement is the ONLY thing in it, and it COMMITS. The
		// refusal is handed to the caller after the commit. Observation and record are
		// now one transaction, so there is no gap left to lose them in.
		if _, ferr := nextFence(rec.Int(colFence)); errors.Is(ferr, ErrFenceExhausted) && !claimIsLive(rec, now) {
			if _, _, rerr := retireIfLapsed(ctx, sc, rec, now); rerr != nil {
				return rerr
			}
			refusal = fmt.Errorf("%w for %s", ErrFenceExhausted, sid)
			return nil
		}
		// Record the lapse durably BEFORE deciding, so the decision is taken on
		// state rather than on a clock reading nobody can audit later (F5).
		rec, _, err = retireIfLapsed(ctx, sc, rec, now)
		if err != nil {
			return err
		}
		live := claimIsLive(rec, now)
		switch {
		case live && rec.String(colHolder) == holder:
			// Renewal. The fence does NOT move: the holder never lost authority,
			// and bumping it would invalidate the writes it has in flight.
			state, rerr := fenceRenew(claimFenceState(rec), fenceToken{
				Holder: holder, Fence: rec.Int(colFence),
			}, now, ttl, fenceTTLPolicy{Default: defaultLeaseTTL, Max: maxLeaseTTL})
			if rerr != nil {
				return rerr
			}
			applyClaimFenceState(rec, state)
		case live:
			return fmt.Errorf("%w: %s holds it until %s", ErrClaimHeld,
				rec.String(colHolder), rec.String(colLeaseExpires))
		default:
			// Lapsed or released: authority changes hands, so the fence moves and
			// every write still carrying the old token is now rejectable.
			state, aerr := fenceAcquire(claimFenceState(rec), holder, now, ttl,
				fenceTTLPolicy{Default: defaultLeaseTTL, Max: maxLeaseTTL})
			if aerr != nil {
				if errors.Is(aerr, ErrFenceExhausted) {
					return fmt.Errorf("%w for %s", ErrFenceExhausted, sid)
				}
				return aerr
			}
			applyClaimFenceState(rec, state)
		}
		updated, uerr := repo.Update(ctx, rec)
		if uerr != nil {
			return uerr
		}
		out = leaseFrom(updated)
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		// Two first claims raced; the loser's whole transaction rolled back
		// (SG-00 §4 explains why recovery cannot happen inside it). Re-read and
		// report the truth: either we are the holder, or somebody else is.
		return m.leaseOf(ctx, tenant, sid, holder)
	}
	if err != nil {
		// The TRANSACTION's verdict outranks the callback's. A refusal here would be
		// a business answer presented as backed by a retirement that never committed
		// — the callback returning nil is not the same as the commit succeeding
		// (R4-03, fourth contrast).
		return Lease{}, err
	}
	if refusal != nil {
		// The retirement this refusal observed IS committed; only the answer was held
		// back.
		return Lease{}, refusal
	}
	return out, nil
}

// leaseOf re-reads a claim after a lost race.
//
// It is a READ, so it records what it observes the way every other read does: in a
// transaction of its own, before it answers (see recordAndAnswer). Not doing so made
// it a silent observer of F5 — it refused with ErrLeaseLost and left the row `active`
// for a rolled-back clock to revive, which the fourth contrast reproduced (R4-02).
func (m *Module) leaseOf(ctx context.Context, tenant model.TenantID, sid, holder string) (Lease, error) {
	var out Lease
	var obs lapseObservation
	var answer error
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		now := m.clock.Now().Time() // F6
		rec, found, ferr := findClaim(ctx, sc, sid)
		if ferr != nil || !found {
			return ferr
		}
		obs = observedLapse(rec, found, now)
		// F4: comparing the holder ALONE reported success on a row that a racing
		// Release had just retired — the caller got a "lease" it did not hold.
		// State and expiry are part of the answer, not decoration.
		switch {
		case !claimIsLive(rec, now):
			answer = fmt.Errorf("%w: claim is %s", ErrLeaseLost, rec.String(colClaimState))
		case rec.String(colHolder) != holder:
			answer = fmt.Errorf("%w: %s holds it", ErrClaimHeld, rec.String(colHolder))
		default:
			out = leaseFrom(rec)
		}
		return nil
	})
	if err != nil {
		return Lease{}, err
	}
	if rerr := m.recordAndAnswer(ctx, tenant, sid, obs); rerr != nil {
		return Lease{}, rerr
	}
	return out, answer
}

// observedLapse builds the record a FIRST observer of a lapse owes, off the row it
// read. A row that is not there, is not `active`, or is still live has nothing to
// record — only the transition active -> expired is the one-way event F5 is about.
//
// It captures the FENCE and never the time: what the follow-up must persist is that
// this generation was seen dead, not a fresh opinion about whether it is.
func observedLapse(rec model.Record, found bool, now time.Time) lapseObservation {
	if !found || rec.String(colClaimState) != claimActive || claimIsLive(rec, now) {
		return lapseObservation{}
	}
	return lapseObservation{seen: true, fence: rec.Int(colFence)}
}

// recordAndAnswer persists an observation a READ made, and reports whether the caller
// may now deliver the answer that observation belongs to.
//
// A View writes nothing, so the read paths cannot be atomic the way Claim, Heartbeat
// and the early fence check are. What they CAN guarantee — and what this is — is that
// no answer is delivered on the strength of an observation the store does not hold:
// if the record fails, the failure is what the caller returns.
//
// DECLARED WINDOW, the same class as stillLive's and stated here rather than left to
// be found: a crash between the read and this commit loses the marker. Nothing was
// answered when that happens, so nothing was granted, and the row is still `active`
// with an expiry in the past — any observer whose clock has not moved backwards
// denies and retires it.
func (m *Module) recordAndAnswer(ctx context.Context, tenant model.TenantID, sid string, obs lapseObservation) error {
	if !obs.seen {
		return nil
	}
	if err := m.retireObserved(ctx, tenant, sid, obs); err != nil {
		if m.log != nil {
			m.log.Warn("sessions: could not record a lapsed lease", "sid", sid, "err", err)
		}
		return fmt.Errorf("sessions: observed a lapsed lease on %s and could not record it: %w", sid, err)
	}
	return nil
}

// Heartbeat renews a lease the caller still legitimately holds. It renews the
// LEASE and never the IDENTITY: the sid and the fence both survive untouched, so
// a long session keeps one identity and one token from start to finish.
func (m *Module) Heartbeat(ctx context.Context, tenant model.TenantID, sid, holder string, fence int64, ttl time.Duration) (Lease, error) {
	var out Lease
	var refusal error
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		refusal = nil
		now := m.clock.Now().Time() // F6: inside the transaction, never before it
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		rec, found, err := findClaim(ctx, sc, sid)
		if err != nil {
			return err
		}
		if !found {
			return ErrNoClaim
		}
		// A heartbeat that arrives late is the most likely FIRST observer of its own
		// death, and it must not renew its way out of it after a clock rollback (F5).
		// The retirement is written HERE and this transaction commits it; only the
		// refusal waits for the commit. Returning the error from inside would roll the
		// retirement back, and recording it in a follow-up transaction — the first
		// cut — leaves a gap in process state that a crash or a rolled-back clock
		// reopens F5 through (R3-01). A heartbeat that finds itself dead writes
		// nothing else, so the retirement is the whole transaction.
		if !claimIsLive(rec, now) {
			if _, _, rerr := retireIfLapsed(ctx, sc, rec, now); rerr != nil {
				return rerr
			}
			refusal = fmt.Errorf("%w: lease lapsed at %s", ErrLeaseLost, rec.String(colLeaseExpires))
			return nil
		}
		state, rerr := fenceRenew(claimFenceState(rec), fenceToken{
			Holder: holder, Fence: fence,
		}, now, ttl, fenceTTLPolicy{Default: defaultLeaseTTL, Max: maxLeaseTTL})
		if rerr != nil {
			return fmt.Errorf("%w: held by %q at fence %d, presented %q at fence %d",
				ErrLeaseLost, rec.String(colHolder), rec.Int(colFence), holder, fence)
		}
		applyClaimFenceState(rec, state)
		updated, uerr := repo.Update(ctx, rec)
		if uerr != nil {
			return uerr
		}
		out = leaseFrom(updated)
		return nil
	})
	if err != nil {
		return Lease{}, err // the transaction's verdict outranks the callback's (R4-03)
	}
	if refusal != nil {
		return Lease{}, refusal
	}
	return out, nil
}

// Release gives up a claim voluntarily. The fence is NOT bumped here — the next
// acquirer bumps it — so a release followed by a re-claim by the same holder is
// distinguishable from never having released.
func (m *Module) Release(ctx context.Context, tenant model.TenantID, sid, holder string, fence int64) error {
	for attempt := 0; ; attempt++ {
		err := m.runtimeData(ctx).Mutate(ctx, tenant, func(sc store.Scope) error {
			now := m.clock.Now().Time() // F6
			repo, err := sc.Ext(claimKind)
			if err != nil {
				return err
			}
			rec, found, err := findClaim(ctx, sc, sid)
			if err != nil {
				return err
			}
			if !found {
				return ErrNoClaim
			}
			state, rerr := fenceRelease(claimFenceState(rec), fenceToken{
				Holder: holder, Fence: fence,
			}, now, "", fenceEndPolicy{Lifecycle: fenceReleased})
			if rerr != nil {
				return fmt.Errorf("%w: held by %q at fence %d", ErrLeaseLost,
					rec.String(colHolder), rec.Int(colFence))
			}
			applyClaimFenceState(rec, state)
			_, err = repo.Update(ctx, rec)
			return err
		})
		if !errors.Is(err, store.ErrConflict) || attempt == 7 {
			return err
		}
		// A WorkLease apply may have touched this exact Claim solely to put
		// session liveness in its OCC write set. Re-open the transaction and
		// release the same holder/fence; a real successor fails fenceRelease.
	}
}

// Authority is the admission check every governed write owes. It answers one
// question — may THIS holder, presenting THIS fence, act on this session RIGHT
// NOW — and it is deny-closed on every path: no claim, lapsed lease, wrong
// holder and stale fence all refuse.
//
// A stale fence is refused EVEN IF the presenter is still running and still
// believes it holds the session. That is the case a lease exists for: the holder
// that stopped renewing does not find out by being told.
func (m *Module) Authority(ctx context.Context, tenant model.TenantID, sid, holder string, fence int64) error {
	// Fast path: a read. The clock is taken inside it (F6).
	var obs lapseObservation
	var answer error
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		now := m.clock.Now().Time()
		rec, found, ferr := findClaim(ctx, sc, sid)
		if ferr != nil {
			return ferr
		}
		answer = authorityOf(rec, found, sid, holder, fence, now)
		// Still marked active but out of time: this read is the FIRST observer of the
		// lapse, and an observation nobody records is exactly what a clock rollback
		// exploits (F5).
		obs = observedLapse(rec, found, now)
		return nil
	})
	if err != nil {
		return err
	}
	// The verdict is the one THIS read computed, and the follow-up only RECORDS it.
	// Re-deciding in the follow-up was worse than not recording at all: with a clock
	// that moved backwards in the gap, the second decision saw an `active` row as live
	// and returned nil — granting the very fence the read had just watched die
	// (R4-01, fourth contrast).
	if rerr := m.recordAndAnswer(ctx, tenant, sid, obs); rerr != nil {
		return rerr
	}
	return answer
}

// authorityOf is the deny-closed answer to "may THIS holder, presenting THIS fence,
// act on this session at now", computed over one claim record.
//
// It serves the READ path only. It was written when the follow-up re-decided the
// question in a transaction of its own, and it kept saying it governed both paths
// after that follow-up was removed; the write path conditions on the fence inside
// the caller's own transaction (fenceWithin), which is a different job.
func authorityOf(rec model.Record, found bool, sid, holder string, fence int64, now time.Time) error {
	if !found {
		return fmt.Errorf("%w: %s", ErrNoClaim, sid)
	}
	state := claimFenceState(rec)
	if err := assertFence(state, fenceToken{Holder: holder, Fence: fence}, now); err == nil {
		return nil
	}
	if !fenceIsLive(state, now) {
		return fmt.Errorf("%w: lease lapsed at %s", ErrLeaseLost, rec.String(colLeaseExpires))
	}
	if cur := rec.String(colHolder); cur != holder {
		return fmt.Errorf("%w: held by %q", ErrLeaseLost, cur)
	}
	if cur := rec.Int(colFence); cur != fence {
		return fmt.Errorf("%w: current fence %d, presented %d", ErrLeaseLost, cur, fence)
	}
	return fmt.Errorf("%w: fenced authority is no longer valid", ErrLeaseLost)
}

// retireObserved records, in its own transaction, a lapse SOMEBODY HAS ALREADY SEEN.
//
// Three shapes exist in this file and it is worth naming which is which, because the
// difference is what F5 actually rests on:
//
//   - the transaction that observes CAN commit the record: Claim's exhausted fence,
//     Heartbeat, and the early fenceWithin. Nothing governed is in the transaction
//     yet, so the retirement is the whole of it and the refusal is delivered after
//     the commit. No window at all.
//   - a READ observes, and a View writes nothing: Authority, leaseOf, ActiveClaim.
//     They record here and answer only if this commits (recordAndAnswer), with the
//     window that leaves declared there.
//   - the LATE re-check observes with the governed effect already in the
//     transaction, so that transaction must roll back and the record comes here.
//     Structural, and stillLive says what it costs.
//
// It does NOT consult the clock, and that is the entire point of the shape. The
// version it replaced re-read `now` and re-decided whether the row had lapsed, so a
// clock that moved backwards between the observation and this call made it a silent
// no-op and left `active` a lease already seen dead — F5 reopened without a crash
// (R3-01, third contrast). What is recorded here is the observation, not a fresh
// judgement of it.
func (m *Module) retireObserved(ctx context.Context, tenant model.TenantID, sid string, obs lapseObservation) error {
	if !obs.seen || sid == "" {
		return nil
	}
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		rec, found, err := findClaim(ctx, sc, sid)
		if err != nil || !found {
			return err
		}
		if rec.String(colClaimState) != claimActive {
			return nil // already retired, or released: both are terminal for this generation
		}
		// The second guard, and the one that carries the design: it is on IDENTITY
		// rather than on time. A takeover mints a new fence (Claim), so a fence that no
		// longer matches means the row the lapse was observed on is gone and this
		// observation must not touch its successor. Neither guard reads a clock.
		if rec.Int(colFence) != obs.fence {
			return nil
		}
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		rec[colClaimState] = claimExpired
		_, err = repo.Update(ctx, rec)
		return err
	})
}

// ActiveClaim reports the live claim on a session, if any. It is the read the
// admission gate uses when the caller presents no fence of its own (a launch
// asks "is this session claimed at all?", not "is my token current?").
// It is the third READ, and it owes the same record as the other two: answering
// live=false on a row still marked `active` IS an observation of a lapse, and one it
// used to drop on the floor — the fourth contrast rolled the clock back afterwards
// and got authority granted again through it (R4-02).
func (m *Module) ActiveClaim(ctx context.Context, tenant model.TenantID, sid string) (Lease, bool, error) {
	var out Lease
	var obs lapseObservation
	live := false
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		now := m.clock.Now().Time() // F6
		rec, found, ferr := findClaim(ctx, sc, sid)
		if ferr != nil || !found {
			return ferr
		}
		obs = observedLapse(rec, found, now)
		if !claimIsLive(rec, now) {
			return nil
		}
		out, live = leaseFrom(rec), true
		return nil
	})
	if err != nil {
		return Lease{}, false, err
	}
	if rerr := m.recordAndAnswer(ctx, tenant, sid, obs); rerr != nil {
		// Deny-closed: an admission read that could not record what it saw never
		// answers "nobody holds this", which is the answer a launch is waiting for.
		return Lease{}, false, rerr
	}
	return out, live, nil
}

// SignalUnclaimedActivity records that a session acted while holding no live
// claim. Silence is the failure mode this exists to prevent: a deny that leaves
// no trace is indistinguishable from a session that never tried.
//
// It reuses the module's EXISTING finding machinery rather than standing up a
// second one — the same upsertLive fold and the same timeline append that
// onFinding uses (live.go) — so the signal lands where an operator already
// looks. It does NOT reuse the silent_evasion marker: that one means the
// connector caught a discrepancy between what a session did and what it
// reported, and "acted without a claim" is a different accusation. Collapsing
// them would leave nobody able to tell which of the two happened.
//
// ref is the OVERLAY reference (the provider's session id), because that is what
// the live row is keyed on; the canonical sid is reached through the alias.
func (m *Module) SignalUnclaimedActivity(ctx context.Context, tenant model.TenantID, ref string, at time.Time) error {
	if m.data == nil {
		return errors.New("sessions: no data handle")
	}
	at = nonZeroTime(at, m.clock)
	var snap *liveSnapshot
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		rec, err := m.upsertLive(ctx, sc, ref, at, func(rec model.Record, _ bool) {
			// Sticky and FIRST-WINS: the interesting instant is when the session
			// first acted unclaimed, not the most recent time it did.
			if rec.String(colUnclaimedAt) == "" {
				rec[colUnclaimedAt] = model.NewTimestamp(at).String()
			}
			advanceLast(rec, at)
		})
		if err != nil {
			return err
		}
		if err := m.appendTimeline(ctx, sc, ref, at, tlFinding, "", "", "",
			"unclaimed", "activity from a session holding no live claim"); err != nil {
			return err
		}
		s := m.snapshot(rec, tenant)
		snap = &s
		return nil
	})
	if err == nil && snap != nil {
		m.broker.publish(*snap)
	}
	return err
}

// ---------------------------------------------------------------------------
// F3 — conditioning the WRITE on the fence, inside the caller's transaction.
// ---------------------------------------------------------------------------

// errLeaseLapsed marks the refusal fenceWithin returns when it is the FIRST
// observer of a lapse. It is SEPARATE from ErrLeaseLost because it carries an
// instruction to the caller and not just a verdict: the retirement is ALREADY
// WRITTEN in the caller's transaction, so that transaction must be COMMITTED
// (return nil) and this refusal delivered afterwards. Every error fenceWithin wraps
// it in also wraps ErrLeaseLost, so the callers' 403 mapping is unaffected.
var errLeaseLapsed = errors.New("sessions: lease lapsed")

// lapseObservation carries the first observation of a lapsed lease OUT of a callback
// that cannot commit it, to a caller that records it in a transaction of its own.
//
// It carries the FENCE that was observed, not a timestamp, because what the follow-up
// owes is the RECORD of an observation somebody already made — never a re-decision of
// it against a clock that may have moved in the meantime (retireObserved).
type lapseObservation struct {
	// seen is true only for the FIRST observer: a row already `expired` or `released`
	// has nothing left to record.
	seen bool
	// fence is the generation the lapse was observed on. A takeover mints a new one,
	// so this is what tells a stale observation from a live row.
	fence int64
}

// fenceWithin makes a governed write CONDITIONAL on the writer still holding the
// authority the run was launched under. It runs INSIDE the caller's own
// transaction and takes that transaction's Scope, for two reasons:
//
//   - correctness: the point of F3 is that the check and the effect commit or roll
//     back together. A check in its own transaction is the TOCTOU it replaces.
//   - liveness: the SQLite store runs every transaction on ONE connection
//     (sqlstore/store.go:760-761). Opening a nested View/Mutate from inside a
//     Mutate waits for a connection the caller already holds — an unbounded hang.
//     Measured exactly that.
//
// holder and fence are what the RUN ROW says this run is operating under, written
// at launch under that claim's own authority. They are never a value looked up
// moments ago: comparing the live fence against a fresh read of the live fence
// compares a value with itself, and would let a caller whose authority had already
// been superseded be silently upgraded to the current token.
//
// The CAS is the repo.Update on the claim row. The store's generic writer updates
// under a version predicate and reports zero affected rows as store.ErrConflict
// (sqlstore/generic.go:199-232), so putting the claim row in this transaction's
// write set is what makes a concurrent takeover fatal to the governed effect
// rather than invisible to it.
//
// On a LAPSE it RETIRES the claim in this same transaction and returns a refusal
// wrapping errLeaseLapsed, which tells the caller to COMMIT and refuse afterwards.
// That is safe here, and only here, because this check runs BEFORE the governed
// effect: nothing of the caller's is in the transaction yet, so committing commits
// the retirement and nothing else. Retiring and RETURNING an error is the shape that
// reopened F5 once (a write followed by an error return is a write rolled back), and
// recording it in a follow-up transaction is the shape that reopened it again — the
// gap between the two is process state, and a crash or a rolled-back clock inside it
// leaves `active` a lease already seen dead (R3-01, third contrast).
func fenceWithin(ctx context.Context, sc store.Scope, sid, holder string, fence int64, now time.Time) error {
	if sid == "" || holder == "" {
		// Nothing was claimed for this write. The runtime's gates are additive by
		// contract, so an unclaimed caller is refused by a composed admission, not by
		// the store layer inventing a policy of its own.
		return nil
	}
	repo, err := sc.Ext(claimKind)
	if err != nil {
		return err
	}
	rec, found, err := findClaim(ctx, sc, sid)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrNoClaim, sid)
	}
	if !claimIsLive(rec, now) {
		// The FIRST observer of a lapse may be this write, and this is the point at
		// which the transaction can still carry the record of it.
		if _, _, rerr := retireIfLapsed(ctx, sc, rec, now); rerr != nil {
			return rerr
		}
		return fmt.Errorf("%w: %w: lease lapsed at %s", errLeaseLapsed, ErrLeaseLost,
			rec.String(colLeaseExpires))
	}
	state := claimFenceState(rec)
	if ferr := assertFence(state, fenceToken{Holder: holder, Fence: fence}, now); ferr != nil {
		if cur := rec.String(colHolder); cur != holder {
			return fmt.Errorf("%w: the session is held by %q, this write carries %q", ErrLeaseLost, cur, holder)
		}
		if cur := rec.Int(colFence); cur != fence {
			return fmt.Errorf("%w: current fence %d, this write carries %d", ErrLeaseLost, cur, fence)
		}
		return fmt.Errorf("%w: fenced authority is no longer valid", ErrLeaseLost)
	}
	// The CAS. Nothing about the claim changes; what matters is that the row joins
	// this transaction's write set at the version we just read.
	if _, uerr := repo.Update(ctx, rec); uerr != nil {
		return uerr
	}
	return nil
}

// stillLive re-checks, against a FRESH clock, that the claim this transaction is
// writing under has not run out while the governed work was running. The version
// CAS proves that nobody else won the row; it does not prove the lease is still
// live at commit, and a slow effect (or a lock wait) can outlast a short TTL. The
// row is already in our write set by then, so nobody can have moved it underneath.
//
// It is not the only observer that cannot commit its own record — the three read
// paths cannot either, which is what recordAndAnswer exists for. What is different
// here, and what makes the asymmetry structural rather than an omission, is WHY: by
// the time it runs, the transaction already holds the governed effect, and that
// effect MUST NOT land under authority that has expired. A read has nothing to lose
// by opening a second transaction; this one cannot keep its own.
// So the transaction rolls back and the observation travels out in the returned
// lapseObservation, which the caller writes in a transaction of its own BEFORE it
// answers anybody (authorizedMutate, transition). Reporting nothing at all was a
// REOPENING of F5 caught by the second contrast; re-deciding the lapse against a
// fresh clock in the follow-up was a second one caught by the third (R3-01), which
// is why the observation now carries the fence it was made on.
//
// LIMITATION, measured and not smoothed over: a crash strictly between that rollback
// and that follow-up commit loses the marker. Nothing was granted when it happens —
// the effect rolled back and no answer was delivered — and the row is still `active`
// with an expiry in the past, so any observer whose clock has not moved backwards
// still denies and still retires it. Closing even that needs the effect rolled back
// to a SAVEPOINT while the retirement stays, and `core/store` exposes no nested
// transaction on either backend: there is no savepoint API or implementation under
// core/ or modules/, and the only occurrences of the word are these two lines.
// It is written down as deferred work with its design, not counted as closed.
func stillLive(ctx context.Context, sc store.Scope, sid string, now time.Time) (lapseObservation, error) {
	if sid == "" {
		return lapseObservation{}, nil
	}
	rec, found, err := findClaim(ctx, sc, sid)
	if err != nil {
		return lapseObservation{}, err
	}
	if !found {
		return lapseObservation{}, fmt.Errorf("%w: the claim vanished while the governed write was in flight", ErrNoClaim)
	}
	if !fenceIsLive(claimFenceState(rec), now) {
		return lapseObservation{
				seen:  rec.String(colClaimState) == claimActive,
				fence: rec.Int(colFence),
			},
			fmt.Errorf("%w: the lease ran out while the governed write was in flight", ErrLeaseLost)
	}
	return lapseObservation{}, nil
}

// findClaim reads the single claim row for a session.
func findClaim(ctx context.Context, sc store.Scope, sid string) (model.Record, bool, error) {
	repo, err := sc.Ext(claimKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colClaimSID, sid)},
		Limit:   1,
	})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	return recs[0], true, nil
}

// claimFenceState adapts the admission row to the shared pure primitive. It is
// intentionally one-way entity knowledge: fence.go never knows claim columns or
// states, and WorkLease has its own adapter.
func claimFenceState(rec model.Record) fenceState {
	state := fenceState{
		Holder: rec.String(colHolder),
		Fence:  rec.Int(colFence),
	}
	switch rec.String(colClaimState) {
	case claimActive:
		state.Lifecycle = fenceActive
	case claimReleased:
		state.Lifecycle = fenceReleased
	case claimExpired:
		state.Lifecycle = fenceExpired
	default:
		state.Lifecycle = fenceVacant
	}
	if ts, err := model.ParseTimestamp(rec.String(colClaimedAt)); err == nil {
		state.AcquiredAt = ts.Time()
	}
	if ts, err := model.ParseTimestamp(rec.String(colRenewedAt)); err == nil {
		state.RenewedAt = ts.Time()
	}
	if ts, err := model.ParseTimestamp(rec.String(colLeaseExpires)); err == nil {
		state.ExpiresAt = ts.Time()
	}
	return state
}

// applyClaimFenceState writes only fields the Claim entity owns. Metadata used by
// WorkLease (ended_at, reason and renewal_count) remains outside this row.
func applyClaimFenceState(rec model.Record, state fenceState) {
	rec[colHolder] = state.Holder
	rec[colFence] = state.Fence
	switch state.Lifecycle {
	case fenceActive:
		rec[colClaimState] = claimActive
	case fenceReleased:
		rec[colClaimState] = claimReleased
	case fenceExpired:
		rec[colClaimState] = claimExpired
	}
	if !state.ExpiresAt.IsZero() {
		rec[colLeaseExpires] = model.NewTimestamp(state.ExpiresAt).String()
	}
	if !state.AcquiredAt.IsZero() {
		rec[colClaimedAt] = model.NewTimestamp(state.AcquiredAt).String()
	}
	if state.RenewedAt.IsZero() {
		delete(rec, colRenewedAt)
	} else {
		rec[colRenewedAt] = model.NewTimestamp(state.RenewedAt).String()
	}
}

// claimIsLive reports whether a claim still carries authority.
//
// The STATE is consulted first and that ordering is the fix for F5. Expiry used
// to be a pure computation over the clock, so a rollback below lease_expires_at
// made a lease that had already been seen dead read as live again, and let its
// old holder heartbeat with the old fence. A durable `expired` outranks any
// clock: a claim that has been observed lapsed stays lapsed until somebody takes
// it over, and a takeover mints a new fence.
func claimIsLive(rec model.Record, now time.Time) bool {
	return fenceIsLive(claimFenceState(rec), now)
}

// retireIfLapsed records the one-way transition to `expired` when a still-active
// row's lease has run out. It is the write that makes F5's guarantee durable,
// and it is idempotent: a row already retired is left alone.
func retireIfLapsed(ctx context.Context, sc store.Scope, rec model.Record, now time.Time) (model.Record, bool, error) {
	state, changed, err := materializeExpiry(claimFenceState(rec), now, "", false)
	if err != nil || !changed {
		return rec, changed, err
	}
	repo, rerr := sc.Ext(claimKind)
	if rerr != nil {
		return rec, false, rerr
	}
	applyClaimFenceState(rec, state)
	updated, uerr := repo.Update(ctx, rec)
	if uerr != nil {
		return rec, false, uerr
	}
	return updated, true, nil
}

// leaseFrom projects a claim record.
func leaseFrom(rec model.Record) Lease {
	l := Lease{
		SID:    rec.String(colClaimSID),
		Holder: rec.String(colHolder),
		Fence:  rec.Int(colFence),
	}
	if ts, err := model.ParseTimestamp(rec.String(colLeaseExpires)); err == nil {
		l.ExpiresAt = ts.Time()
	}
	return l
}

// ---------------------------------------------------------------------------
// Admission — the LaunchGate decorator that makes a claim a precondition.
// ---------------------------------------------------------------------------

// ClaimAdmission wraps any LaunchGate so a launch is refused unless the LAUNCHER
// ITSELF holds the live claim on the session it is about to drive.
//
// It shipped UNWIRABLE and said so: a create consulted the gate with an EMPTY run
// reference, because launchIntentFor was called with "" and the reference was
// minted five lines later. SG-02-b closed that (F1) by minting the reference and
// ACQUIRING the claim before the gate is consulted, and by widening LaunchIntent to
// carry Holder, Fence and ClaimSID. This decorator is now composed for real, at the
// single composition point (cmd/olivares/sessiongov.go).
//
// What it can and cannot assert, stated plainly because the difference matters:
//
//   - On a CREATE the reference is fresh, so no other holder can possibly have a
//     live claim on it. Admission here refuses an UNIDENTIFIED launcher and nothing
//     else. It does not "check" — it ARMS: the acquired claim is stamped on the run
//     row, and that stamp is what later writes are conditioned against.
//   - On a RESUME the session already exists, so the acquisition can genuinely fail:
//     another holder with a live lease refuses it (ErrClaimHeld) before this gate is
//     ever reached, and this gate then confirms the launch carries the claim the
//     runtime actually holds.
//   - F2, the refuted first cut: a live claim EXISTING is not the question. Holder
//     and fence are both compared, so a launcher cannot ride somebody else's claim.
//
// Ordering note: admission runs BEFORE the inner gate, so an unclaimed launch never
// opens a HITL approval nor spends budget quota on its way to being denied.
//
// F3 is not this decorator's job and never could be: a gate is a read, and a read
// cannot bind an effect. The write is conditioned on the fence separately, inside
// the transaction that performs it (fenceWithin).
type ClaimAdmission struct {
	inner    LaunchGate
	admitter interface {
		ActiveClaim(ctx context.Context, tenant model.TenantID, sid string) (Lease, bool, error)
		Authority(ctx context.Context, tenant model.TenantID, sid, holder string, fence int64) error
		ResolveSession(ctx context.Context, tenant model.TenantID, b SessionBinding) (string, error)
	}
	// provider is the engine key launches are attributed to when resolving the
	// run's external reference to a canonical session.
	provider string
	// holderOf identifies the LAUNCHER from its intent. Returning ok=false means
	// the launcher cannot name itself, and an unidentifiable launcher is denied:
	// admission that accepts "somebody holds a claim" authorizes a process to ride
	// a claim it never took.
	holderOf func(LaunchIntent) (holder string, fence int64, ok bool)
	// onRefusal signals a refusal so it is VISIBLE and not merely returned. A deny
	// nobody can see is the failure mode this whole story exists to prevent.
	onRefusal func(ctx context.Context, tenant model.TenantID, ref string)
}

// IntentHolder is the canonical holderOf: the launch's admission references, put
// there by the runtime's own preamble (runtime.go, admit) from the authenticated
// principal and from the store-minted fence. A launch with no holder never
// identified itself and is refused.
//
// It is exported so the composition root wires the SAME identity seam the runtime
// fills, instead of each composition inventing its own answer to "who is calling".
func IntentHolder(i LaunchIntent) (string, int64, bool) {
	return i.Holder, i.Fence, i.Holder != ""
}

// NewClaimAdmission composes admission over an inner gate. holderOf is how a
// launcher names itself; passing nil denies every launch, which is the correct
// default for a control nobody has taught to identify its callers yet.
func NewClaimAdmission(inner LaunchGate, m *Module, provider string,
	holderOf func(LaunchIntent) (string, int64, bool)) *ClaimAdmission {
	return &ClaimAdmission{
		inner:    inner,
		admitter: m,
		provider: provider,
		holderOf: holderOf,
		onRefusal: func(ctx context.Context, tenant model.TenantID, ref string) {
			// The error is deliberately observed, not discarded: a signal that
			// failed to record is itself the silence this guards against.
			if err := m.SignalUnclaimedActivity(ctx, tenant, ref, time.Time{}); err != nil && m.log != nil {
				m.log.Warn("sessions: could not record an unclaimed-launch refusal",
					"ref", ref, "err", err)
			}
		},
	}
}

// Authorize refuses a launch whose session holds no live claim, and only then
// defers to the inner gate.
func (a *ClaimAdmission) Authorize(ctx context.Context, tenant model.TenantID, intent LaunchIntent) (LaunchDecision, error) {
	ref := intent.RunRef
	if ref == "" {
		// No reference to admit on. Deny-closed: an anonymous launch is exactly
		// what admission exists to stop, so it is not the case to be lenient in.
		return LaunchDecision{Allowed: false, Reason: "admission: launch carries no session reference"}, nil
	}
	// The runtime already resolved the canonical session when it acquired the claim,
	// so take its answer. Resolving again here would mint identity on the DENY path
	// (ResolveSession creates on first sight, identity.go:236-240) — a refused launch
	// leaving a canonical session behind as a side effect of being refused.
	sid := intent.ClaimSID
	if sid == "" {
		var err error
		sid, err = a.admitter.ResolveSession(ctx, tenant, SessionBinding{
			Provider: a.provider, ExternalID: ref, Origin: OriginOperated,
		})
		if err != nil {
			// An unreadable identity plane never means "go".
			return LaunchDecision{Allowed: false, Reason: "admission: identity unavailable"}, err
		}
	}
	_, live, err := a.admitter.ActiveClaim(ctx, tenant, sid)
	if err != nil {
		return LaunchDecision{Allowed: false, Reason: "admission: claim unreadable"}, err
	}
	if !live {
		if a.onRefusal != nil {
			a.onRefusal(ctx, tenant, ref)
		}
		return LaunchDecision{
			Allowed: false,
			Reason:  fmt.Sprintf("admission: session %s holds no live claim", sid),
		}, nil
	}
	// F2. A live claim is NOT the question; whether THIS launcher holds it is.
	if a.holderOf == nil {
		return LaunchDecision{Allowed: false, Reason: "admission: no launcher identity seam wired"}, nil
	}
	holder, fence, ok := a.holderOf(intent)
	if !ok || holder == "" {
		if a.onRefusal != nil {
			a.onRefusal(ctx, tenant, ref)
		}
		return LaunchDecision{Allowed: false, Reason: "admission: launcher did not identify itself"}, nil
	}
	if aerr := a.admitter.Authority(ctx, tenant, sid, holder, fence); aerr != nil {
		if !errors.Is(aerr, ErrNoClaim) && !errors.Is(aerr, ErrLeaseLost) {
			return LaunchDecision{Allowed: false, Reason: "admission: claim unreadable"}, aerr
		}
		if a.onRefusal != nil {
			a.onRefusal(ctx, tenant, ref)
		}
		return LaunchDecision{
			Allowed: false,
			Reason:  fmt.Sprintf("admission: %s presented fence %d against a claim held by another", holder, fence),
		}, nil
	}
	if a.inner == nil {
		return LaunchDecision{Allowed: true}, nil
	}
	return a.inner.Authorize(ctx, tenant, intent)
}
