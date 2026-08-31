// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package suspension enforces withdrawal of service from a tenant whose org row
// is suspended (orgs.status —), WITHOUT destroying its data.
//
// Retiring ACCESS and destroying DATA are two different decisions. Before
// the engine only had the second: DELETE /v1/system/orgs/{tenant_id} is a
// hard delete documented for use AFTER the cloud grace period, so using it to
// stop serving a non-paying tenant destroys exactly the data the grace period
// exists to protect. This package is the intermediate door.
//
// WHAT "NOT SERVED" MEANS HERE, and why (the decision, not a default):
// mutations and INTERACTIVE SERVICE are denied; taking your own data with you is
// NOT. Every tenant-scoped Mutate is refused, every tenant-scoped View is refused,
// and Store.Export — a distinct door with a distinct type — stays open and is
// recorded on the tenant's chain each time it is used while service is withdrawn.
//
// ⚠ THIS PARAGRAPH HAS BEEN WRONG TWICE, so it is worth reading the history before
// changing it a third time.
//
// It first said the freeze is "deliberately NOT read-only, because in this product
// reading IS the service: a routing policy resolves and a model executes under
// View, so a read-permitting suspension would leave a non-paying tenant spending
// against OUR provider bill" — and that a store-level guard could not "carve out
// export reads from product reads, because the store sees a View, not the HTTP
// route that opened it".
//
// The first half is a true observation and a false NECESSITY: the estate kill
// switch already cuts that provider call without freezing reads. The second half
// was then read by others as if this file already HAD the carve-out. It did not —
// that sentence was the argument AGAINST one.
//
// Both are now settled, and the answer is neither of the two options that were
// posed. Allowing all reads leaves the non-paying tenant OPERATING, which is
// service. Denying all reads withdraws /v1/audit/export and
// GET /v1/m/knowledge/memory/export — the customer's own subject-access and
// anti-lock-in copy — and custody does not lapse for non-payment. The line runs
// between them: deny mutations and interactive service, keep an EXPLICIT and
// AUDITED export path.
//
// And the old objection is genuinely retired rather than waved away. It was right
// that a *context flag* threaded past a deny-closed gate would be an unaudited
// escape hatch stronger than the door beside it. It was wrong that no carve-out
// was possible: the store does not need to know the HTTP route when the CALL SITE
// declares its purpose in the type system. Store.Custody proved the shape and
// Store.Export uses it — a separate method, a separate scope type, greppable,
// unreachable from a module holding only a Scope, and in Export's case recorded on
// the ledger every time it is used on a tenant that is not in service.
//
// The custody obligation is met by what suspension does NOT do — it deletes
// nothing, revokes nothing, and is reversed by a single call that restores the
// estate exactly as it was — AND, since by what it does not gate.
//
// ⚠ THIS PARAGRAPH USED TO SAY THE OPPOSITE, and it was wrong. It read: "A
// CONSEQUENCE, stated rather than hidden: freezing the data plane also freezes
// that tenant's custodial background work — retention sweeps, ledger archival,
// SIEM forwarding. During a grace period that is the safer direction (nothing is
// deleted from a customer who may return)."
//
// Safer is true of retention sweeps, which only ever DELETE. It is false of
// anchoring, which only ever ADDS, and the difference is not academic: the same
// freeze stopped checkpointing the tenant's chain and stopped DR recording a
// verifiable tip for it, while SetOrgStatus went on appending org.suspend_service
// to that very chain. So the evidence kept growing and stopped being provable,
// and `dr verify` could report PASSED without ever having looked at it. Not
// destroying data is not the same as keeping it demonstrable, and during a grace
// period demonstrability is the whole point.
//
// So custody is now a door of its own: store.Custody, which this guard does NOT
// gate (see the Custody method below). Withdrawing service is commercial; keeping
// evidence anchored is custodial, and only the first is a decision about whether
// the customer is paying.
//
// What remains frozen for a withdrawn tenant, and is stated rather than hidden:
// retention sweeps, SIEM forwarding, and the WORM ledger archive. The first two
// are the safe direction. The third is NOT a decision, it is a known gap: the
// archive's resume bookkeeping lives in Org.Settings and must be written in the
// SAME transaction as its anchor event, and CustodyScope deliberately does not
// expose SetOrgSettings — that method can rewrite security-relevant configuration
// (the SCIM receiver's verification key), which has no business on this door. The
// fix is to move that bookkeeping to its own table; until then, say so.
package suspension

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Guard wraps a store.Store with deny-closed service-withdrawal enforcement. It
// is the sibling of residency.Guard and composes with it: residency answers "is
// this tenant's data resident here", suspension answers "is this tenant still
// served at all". A nil inner store returns nil unchanged.
//
// Unlike residency.Guard there is no "does not enforce" fast path to opt out of:
// any deployment can suspend a tenant, so the guard is always armed. The cost is
// one indexed primary-key read of the bound tenant's own org row, folded into the
// SAME transaction as the caller's work — so the check and the work commit or
// roll back together, and there is no second round trip.
func Guard(inner store.Store, log *slog.Logger) store.Store {
	if inner == nil {
		return nil
	}
	g := &guardStore{inner: inner, log: log}
	// Capability-PRESERVING, in both directions. A decorator that always
	// implements an optional interface fabricates a capability the wrapped store
	// may not have — and here that would be worse than a lost feature: the
	// composition root refuses to boot when the store cannot expose durable
	// rollout state, and a guard that answered "yes I can" would make that
	// deny-closed check impossible to trigger. So the rollout methods live on a
	// separate type that is only returned when the inner store really has them.
	//
	// AuditSpoolStatuser needs no such split: its signature carries its own
	// "supported" bool, so forwarding false is a truthful answer.
	if _, ok := inner.(store.RolloutStater); ok {
		return &guardStoreWithRollout{guardStore: g}
	}
	return g
}

// guardStore is the deny-closed service-state decorator over store.Store.
type guardStore struct {
	inner store.Store
	log   *slog.Logger
}

// View runs fn read-only, denied closed if the tenant's service is withdrawn.
// Reads are gated as hard as writes: see the package doc — in this product,
// reading is the service.
func (g *guardStore) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if g.passThrough(tenant) {
		return g.inner.View(ctx, tenant, fn)
	}
	return g.inner.View(ctx, tenant, g.wrap(ctx, tenant, fn))
}

// Mutate runs fn read-write, denied closed if the tenant's service is withdrawn.
func (g *guardStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if g.passThrough(tenant) {
		return g.inner.Mutate(ctx, tenant, fn)
	}
	return g.inner.Mutate(ctx, tenant, g.wrap(ctx, tenant, fn))
}

// Custody runs fn over the tenant's evidence ledger and is NOT gated by service
// state. That is the decision this method exists to make explicit.
//
// Withdrawing service is commercial; keeping a customer's evidence anchored and
// provable is custodial, and the second does not stop when the first happens — in
// a grace period it is exactly when it matters. The premise the original design
// rested on, that a withdrawn tenant's chain is FROZEN and so has nothing new to
// anchor, is false in the lines that implement suspension: SetOrgStatus changes
// orgs.status and then appends org.suspend_service to that same tenant's chain. So
// the chain advances at the very moment the checkpoint filter began skipping it,
// and the tail since the previous checkpoint — including the suspension event —
// was left with no anchor, while DR recorded a note instead of a verifiable tip
// and RestoreVerify never looked at it. A backup that cannot be proven continuous
// is worse than a missing one, because it reports success.
//
// What keeps this from being the escape hatch the package doc rejects: it is not a
// flag threaded through a deny-closed gate, it is a distinct method handing out a
// distinct TYPE. A store.CustodyScope carries the ledger and the org row and
// nothing else — no agents, no policies, no models, no Ext — so it cannot serve
// the tenant, which is what "withdraw service" has to mean. A module cannot reach
// it at all: modules receive a Scope, never the Store.
//
// Residency still gates it (residency.Guard.Custody keeps its region check, and
// this guard composes OUTSIDE that one), so custody never becomes a way to read a
// tenant's data from a region that may not serve it.
func (g *guardStore) Custody(ctx context.Context, tenant model.TenantID, fn func(store.CustodyScope) error) error {
	return g.inner.Custody(ctx, tenant, fn)
}

// ActionExportDuringWithdrawal is the audit action recording that a tenant took a
// copy of its own data while its service was withdrawn. It is a NAMED constant
// because a console and an export report both have to match on it exactly.
const ActionExportDuringWithdrawal = "org.export_during_withdrawal"

// Export runs fn over the tenant's own exportable data and is NOT denied by
// service state — it is the carve-out in the door, and the reason the door can be
// shut at all without taking the customer's data hostage.
//
// Denying every read and denying every write are not the same decision, and
// treating them as one was the error this replaces. Allowing all reads leaves a
// non-paying tenant OPERATING — a routing policy resolves and a model executes
// under View — and that is service. Denying all reads withdraws
// /v1/audit/export and GET /v1/m/knowledge/memory/export, the customer's own
// subject-access and anti-lock-in copy, and custody does not lapse for
// non-payment. So: mutations and interactive service are denied; taking your data
// with you is not.
//
// AUDITED, and not by convention: when the tenant is not in service this appends
// the record of the export to that tenant's own chain, inside the SAME
// transaction as the export itself. A copy taken during a grace period therefore
// cannot be taken silently, and either side can prove later what left and when.
// The duty lives here rather than in each export handler on purpose — a duty
// spread across callers is inherited by whoever is written next.
func (g *guardStore) Export(ctx context.Context, tenant model.TenantID, fn func(store.ExportScope) error) error {
	if g.passThrough(tenant) {
		return g.inner.Export(ctx, tenant, fn)
	}
	return g.inner.Export(ctx, tenant, func(es store.ExportScope) error {
		org, err := es.Org(ctx)
		if err != nil {
			return err
		}
		if org.Status != model.StatusActive {
			if g.log != nil {
				g.log.Warn("suspension: export ALLOWED for a tenant that is not in service, and recorded on its chain",
					"tenant", tenant.String(), "status", string(org.Status))
			}
			if _, aerr := es.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: ActionExportDuringWithdrawal, TargetKind: "core.org", TargetID: org.ID,
			}); aerr != nil {
				// Fail CLOSED: if the export cannot be recorded it does not happen. An
				// unrecorded copy of a customer's data leaving during a billing dispute
				// is the one outcome neither side can reconstruct afterwards.
				return fmt.Errorf("export during withdrawal could not be recorded, so it was refused: %w", aerr)
			}
		}
		return fn(es)
	})
}

// passThrough reports tenants the guard never gates: the zero tenant (the inner
// store rejects it with ErrNoTenant) and the reserved system tenant, which holds
// the auth/provisioning partition every instance keeps for itself and can never
// be suspended (SetOrgStatus refuses it).
func (g *guardStore) passThrough(tenant model.TenantID) bool {
	return tenant.IsZero() || tenant.IsSystem()
}

// wrap returns fn prefixed with the deny-closed service check, reading the bound
// tenant's org inside the same scope/transaction.
//
// A tenant with NO org row is denied, not passed through. That is deliberate and
// it closes a hole the reproduction found: after DropTenant the rows are gone but
// the request path never checked that the org existed, so a request naming a
// deleted tenant was answered 200 with an empty list — "served nothing" where the
// honest answer is "not served". An absent org can never mean "in service".
func (g *guardStore) wrap(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) func(store.Scope) error {
	return func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Deny-closed, but do NOT call it a suspension. A tenant that never
				// existed — a typo'd id, or one hard-deleted after the grace period —
				// would otherwise be told "your service is suspended", which a console
				// renders as a billing problem for an account that is not there.
				// The work still does not run; only the answer is honest.
				if g.log != nil {
					g.log.Warn("suspension: refused work for a tenant with no organization on this instance",
						"tenant", tenant.String())
				}
				return fmt.Errorf("%w: tenant %s has no organization on this instance", store.ErrNotFound, tenant)
			}
			return err
		}
		switch org.Status {
		case model.StatusActive:
		case model.StatusSuspended:
			return g.deny(tenant, store.ErrTenantSuspended, string(org.Status),
				fmt.Sprintf("tenant %s is not in service (status %q); service was withdrawn without deleting its data and can be restored", tenant, org.Status))
		default:
			// Deny-closed all the same — an unrecognized state is never "in service" —
			// but do NOT call it a suspension. No commercial decision was recorded for
			// this tenant, and saying one was sends the operator to look for a billing
			// problem instead of at the row that is actually wrong.
			return g.deny(tenant, store.ErrTenantNotInService, string(org.Status),
				fmt.Sprintf("tenant %s is not in service: its organization row carries status %q, which is neither %q nor %q. No suspension was recorded for it — this row is inconsistent and needs an operator, not a billing decision", tenant, org.Status, model.StatusActive, model.StatusSuspended))
		}
		return fn(sc)
	}
}

// deny logs the refusal (an operator-relevant event) and returns the typed,
// wrapped error. The detail is for the operator log and the API body; the
// sentinel store.ErrTenantSuspended is what callers match with errors.Is.
func (g *guardStore) deny(tenant model.TenantID, sentinel error, status, detail string) error {
	if g.log != nil {
		g.log.Warn("suspension: denied work for a tenant that is not in service (deny-closed)",
			"tenant", tenant.String(), "status", status)
	}
	return fmt.Errorf("%w: %s", sentinel, detail)
}

// The remaining methods delegate unchanged. System and Auth pass through BY
// DESIGN, not by omission: the operator must always be able to list, restore and
// (after the grace period) delete a suspended tenant, and its users must still
// authenticate so the console can tell them WHY they are refused rather than
// showing a bad-credential error. Engine/Ping/Close/Leader are pool-level.

func (g *guardStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	return g.inner.System(ctx, fn)
}

func (g *guardStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	return g.inner.AuthView(ctx, fn)
}

func (g *guardStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return g.inner.AuthMutate(ctx, fn)
}

// Leader delegates: service state is a per-tenant commercial fact layered ABOVE
// leadership, which stays a property of the underlying store.
func (g *guardStore) Leader() store.LeaderElector { return g.inner.Leader() }

// A store.Store decorator must forward EVERY optional capability the wrapped
// store exposes, or it silently downgrades the deployment. This guard is ALWAYS
// armed — unlike the residency guard, which only wraps a region-scoped instance —
// so a swallowed capability is not a latent edge here: it is every boot.

// AuditSpoolStatus forwards the optional observability capability when the
// wrapped store provides it. Its own bool reports "not supported" truthfully, so
// it needs no capability split.
func (g *guardStore) AuditSpoolStatus(ctx context.Context) (store.AuditSpoolStatus, bool, error) {
	statuser, ok := g.inner.(store.AuditSpoolStatuser)
	if !ok {
		return store.AuditSpoolStatus{}, false, nil
	}
	return statuser.AuditSpoolStatus(ctx)
}

// DirectoryStatus forwards the optional directory-fence witness. The
// supported result stays false when the wrapped store has no such capability,
// so this always-armed decorator does not manufacture K3 readiness.
func (g *guardStore) DirectoryStatus(ctx context.Context) (store.DirectoryStatus, bool, error) {
	statuser, ok := g.inner.(store.DirectoryStatuser)
	if !ok {
		return store.DirectoryStatus{}, false, nil
	}
	return statuser.DirectoryStatus(ctx)
}

// guardStoreWithRollout is the guard over a store that DOES expose durable
// rollout state (store.RolloutStater). Guard returns it only in that case, so the
// capability is preserved without ever being fabricated.
//
// Rollout controls are deployment-wide state with no tenant bound, so suspension
// never gates them: whether a control is in force for the install does not depend
// on any tenant's commercial status. These three methods therefore delegate
// straight through.
type guardStoreWithRollout struct {
	*guardStore
}

func (g *guardStoreWithRollout) RolloutState(ctx context.Context, key string) (store.RolloutState, error) {
	return g.inner.(store.RolloutStater).RolloutState(ctx, key)
}

func (g *guardStoreWithRollout) RolloutHistory(ctx context.Context, key string) ([]store.RolloutTransitionRecord, error) {
	return g.inner.(store.RolloutStater).RolloutHistory(ctx, key)
}

func (g *guardStoreWithRollout) SetRolloutMode(ctx context.Context, t store.RolloutTransition) (store.RolloutState, error) {
	return g.inner.(store.RolloutStater).SetRolloutMode(ctx, t)
}

func (g *guardStore) Engine() store.Engine { return g.inner.Engine() }

func (g *guardStore) Ping(ctx context.Context) error { return g.inner.Ping(ctx) }

func (g *guardStore) Close() error { return g.inner.Close() }
