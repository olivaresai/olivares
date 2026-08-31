// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "context"

// LeaderElector arbitrates which node, in an active-passive HA cluster sharing
// one Postgres, is the single active writer (OPS-1). The
// control plane governs the actuation of agents, so it must not be a
// single-point-of-failure: several nodes run, exactly one holds leadership and
// serves, the rest are hot standbys that take over automatically when it dies.
//
// The contract is CP-over-AP by design: at most one node is ever Active, even at
// the cost of a few seconds of unavailability on a hard crash. A second
// concurrent writer would fork the signed audit hash-chain (an integrity
// failure), which is strictly worse than a brief gap — so the elector never
// trades safety for availability.
//
// It is engine-agnostic at this seam. The SQLite/single-node store returns an
// always-leader implementation (there is nothing to elect: one process owns the
// file), so callers need no special-casing. The Postgres store returns a
// session-level advisory-lock elector — no new external dependency, no extra
// SQL grant, and it works identically under Kubernetes, bare systemd/Docker and
// air-gapped (unlike a Kubernetes Lease, which would couple HA to the orchestrator
// API). The interface keeps a future Lease-based elector a drop-in.
//
// Lifecycle: the composition root calls Run once before it begins serving (so
// leadership is known up front) and Resign at graceful shutdown (so a standby
// takes over fast). Until Run is called the elector is UNARMED and Active reports
// true — preserving the historical single-writer behavior for the embedded store
// and every test that opens a store directly. Arming is therefore opt-in: only a
// real deployment (boot → Run) turns service gating on.
type LeaderElector interface {
	// IsLeader reports established leadership: whether this node may be exposed as
	// the serving leader. It is false on a standby and on an unarmed elector that
	// has not yet run an election. An elected implementation must keep it false
	// while a pre-establishment OnPromote bootstrap runs; an always-leader
	// implementation is already established. Use it for observability, routing and
	// readiness.
	IsLeader() bool

	// Active reports whether this node may act publicly as the serving writer. It is
	// true when the elector is unarmed (single-node / pre-Run, the historical
	// behavior) or leadership is established; it is false on an armed standby. An
	// elected implementation must not make it true merely because its private
	// OnPromote bootstrap write gate is open; an always-leader implementation is
	// already established. Public background loops and readiness may use it as their
	// service predicate. Store implementations can use a narrower private bootstrap
	// gate to let OnPromote commit its required setup before this method becomes
	// true.
	Active() bool

	// Run arms the elector and performs the initial election, then maintains
	// leadership in the background until ctx is done or Resign is called. It BLOCKS
	// only until the initial state is resolved (this node acquired leadership, or a
	// leader already exists and this node is following) — not until it eventually
	// becomes leader — so the caller knows the cluster role before it serves. After
	// Run returns, Active() reflects established leadership and the service gate is
	// armed. If the initial acquisition runs OnPromote and that fails, Run returns
	// the error.
	Run(ctx context.Context) error

	// Resign relinquishes leadership if held (an explicit unlock, so a standby's
	// blocked acquire returns immediately for a fast handoff) and stops the
	// background loop. It is idempotent and safe to call when not leading. After
	// Resign the elector reports not-Active (it is shutting down).
	Resign(ctx context.Context) error

	// Epoch is a monotonically increasing fencing token, bumped under the election
	// lock each time leadership is acquired cluster-wide. It lets a node that was
	// paused (a stop-the-world GC, a frozen container) and then resumed detect that
	// it is stale — its epoch is below the current one — and self-fence rather than
	// act on a leadership it has silently lost. Zero until the first acquisition.
	Epoch() uint64

	// OnPromote registers a callback invoked each time THIS node acquires
	// leadership — at the initial election and on every failover promotion — and,
	// for the initial election, BEFORE Run returns. It is the seam for write-side
	// bootstrap that only the active writer must perform (provisioning the reserved
	// system tenant). An elected implementation can run the callback with its
	// private bootstrap write gate open while Active and IsLeader remain false to
	// public callers; an always-leader implementation is already established. A
	// non-nil error aborts the
	// acquisition: the node releases the lock, stays a follower and retries — so a
	// node never serves as leader with a half-finished bootstrap. Must be set
	// before Run; passing nil clears it.
	OnPromote(fn func(context.Context) error)
}

// EpochFencer is an OPTIONAL LeaderElector capability (asserted like
// AuditSpoolStatuser / CanonicalWalker): the context-aware DURABLE leadership
// fence for fencing-sensitive effects (the evidence operation journal).
//
// Active()+Epoch() are process-local: on Postgres another node can already hold
// a higher cluster epoch while this process still reports its cached leadership
// until the next poll tick (or forever, while paused). FencedEpoch instead
// verifies against durable state — the Postgres elector proves its held lock
// SESSION is alive and that the persisted leader_epoch row still names this
// node, returning the persisted epoch; the single-node elector's durable truth
// is its own liveness (it refuses after Resign). Both in-tree electors
// implement it; a caller that needs fencing and finds the capability absent
// must FAIL CLOSED, never fall back to the in-memory pair.
type EpochFencer interface {
	// FencedEpoch durably verifies this node may still act as the leader and
	// returns the verified cluster epoch. A non-nil error means the node must
	// not emit a fencing-sensitive effect right now.
	FencedEpoch(ctx context.Context) (uint64, error)
}
