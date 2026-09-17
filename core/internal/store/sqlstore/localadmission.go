// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

// LocalAdmission is ONE boot's local publication ownership, carried from the
// composition root through the engine facade into this constructor.
//
// # Why it is an object and not a boolean
//
// The composition root has to hold the local control across a span this package
// cannot see: the three signing-key loaders MINT on a data directory that has none,
// so the control must be held from before the first key is loaded until after the
// store's publication decision. Before this type, the root took a lease of its own
// and the store took a SECOND, unrelated one — and the two were reconciled by the
// registry rule that granted any shared request whenever the process held anything.
// That rule is F3-IR-1, and removing it removes the reconciliation with it.
//
// So ownership is now HANDED OVER. The admission holds a shared ROOT lease over the
// installation's anchors, and the store's fence is a CHILD derived from it: an exact
// subset of a live parent, with the root's mode preserved. Nothing infers ownership
// from process membership, an operation identifier, a matching path or a boolean.
//
// # What it deliberately is not
//
//   - It is not an extension point. There is no callback, no field a caller can set
//     to suppress a fence, no context value and no bool that turns anything off.
//   - It does not resolve the destination twice. The SQLite target is frozen at
//     construction, and Open takes no destination at all, so there is nothing to
//     re-point: a changed destination is unspellable rather than refused.
//   - It is not a second connection config. The destination it acquired IS the one
//     it opens; Open takes no Config at all, only the audit signer bound after key
//     loading, so there is nothing for a caller to re-point between the two calls.
//
// # It now carries the PostgreSQL session too
//
// The local lease alone was not enough, and F3-IR-5 is why. The three signing-key
// loaders run between acquisition and Open, and a PostgreSQL control lives on the
// server — so a boot that read the local anchors here and only consulted the database
// inside Open would already have loaded (or MINTED) its keys by the time it learned
// the destination was restored. That is true even with NO local sidecar: a remote
// database can carry a completed or pending control on a brand-new node that has
// nothing on disk at all.
//
// So the shared PostgreSQL coordination is taken HERE, before the loaders, and the
// SAME session is carried into Open and through its final decision. Acquiring a probe
// and releasing it before key loading would recreate the exact gap, which is why this
// is a retained admission and not a pre-flight read.
type LocalAdmission struct {
	mu sync.Mutex

	engine  store.Engine
	dataDir string
	cfg     store.Config
	dsn     string
	target  opgate.SQLiteTarget
	anchors []opgate.Anchor
	root    *opgate.Lease
	witness RestoreEnrolmentWitness
	// localComplete is the COMPLETE local record's keyset, where local evidence
	// exists. It is the only source of the expected per-purpose selection: the
	// database column carries a digest and nothing else.
	localComplete opgate.Keyset
	localOpID     string
	localPlan     string
	// pub is the retained PostgreSQL publication admission — the original session,
	// its lock and its judged control — held from before the loaders through the
	// final decision inside Open. Nil for SQLite and for destinations with no
	// PostgreSQL side.
	pub *publicationAdmission
	// req is frozen at acquisition and never recomputed. See CustodyRequirement.
	req CustodyRequirement

	// state is finite and one-way: an admission is acquired, then closed. published
	// records that its ONE publication decision has been taken, so a second Open
	// cannot ride the same admission.
	closed    bool
	published bool
}

// ErrAdmissionDestination reports evidence or a resolved target that is not the one the
// admission acquired: a local control naming another engine or frozen SQLite file, or a
// store resolution that disagrees with the fenced anchor. It is a refusal rather than a
// second acquisition.
//
// It is NOT produced by Open comparing a caller's configuration any more. Open takes no
// store.Config, so a re-pointed publication cannot be expressed; the two config branches
// this error used to guard were removed with the parameter.
var ErrAdmissionDestination = errors.New("sqlstore: this local admission was acquired for a different destination")

// BeginLocalAdmission takes the local publication ownership of one installation and
// reads its durable controls, before the caller loads or mints any key.
//
// It acquires EVERY local anchor the installation has — the data directory that
// holds the custody and, for SQLite, the resolved store file — in one sorted
// acquisition, so a partial take is unwound rather than left half-held and two
// callers naming the pair in different orders cannot interleave.
//
// A destination with no control is the ordinary case for every installation that
// exists today and proceeds untouched.
func BeginLocalAdmission(ctx context.Context, cfg store.Config, dataDir string) (*LocalAdmission, error) {
	engine, dsn := cfg.Engine, cfg.DSN
	adm := &LocalAdmission{engine: engine, dataDir: dataDir, cfg: cfg, dsn: dsn}

	dirAnchor, present, err := opgate.AnchorForDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	if present {
		adm.anchors = append(adm.anchors, dirAnchor)
	}
	if engine == store.EngineSQLite {
		// ONE resolver, the same one the driver's DSN comes from. The old code had a
		// private string-stripper here and another in the store, and their two answers
		// were the alias gap (F3-IR-2).
		target, terr := opgate.ResolveSQLiteTarget(dsn)
		if terr != nil {
			return nil, fmt.Errorf("%w: %v", ErrRestoreCoordinationUnknown, terr)
		}
		adm.target = target
		if a := target.Anchor(); !a.Zero() {
			adm.anchors = append(adm.anchors, a)
		}
	}
	if len(adm.anchors) > 0 {
		lease, ok, err := opgate.TryAcquire(opgate.ModeShared, adm.anchors...)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf(
				"a disaster-recovery operation holds this installation (%s): its restore control is exclusive, and this command will not load signing keys or open the store while it runs. Wait for that operation to finish, or ask it for its status",
				dataDir)
		}
		adm.root = lease
		if jerr := adm.judge(); jerr != nil {
			adm.Close()
			return nil, jerr
		}
	}
	// Nothing local to fence is still a real admission: an in-memory destination, or
	// a remote store whose data directory does not exist. The caller's Close and Open
	// stay unconditional either way — and a remote PostgreSQL destination with no
	// local record at all is precisely the case the read below exists for.
	if err := adm.admitPostgres(ctx); err != nil {
		adm.Close()
		return nil, err
	}
	if adm.engine == store.EngineSQLite {
		adm.freezeLocalRequirement()
	}
	return adm, nil
}

// admitPostgres takes the SHARED coordination on the destination's own server and
// reads its control — BEFORE the caller loads or mints a key, and on a session this
// admission keeps.
//
// ⛔ THE POSITION IS THE WHOLE POINT. Reading the control after the loaders would
// let a boot mint three fresh keys onto a node whose destination was already
// restored, and then discover the fact. Reading it here and RELEASING before the
// loaders would be the same gap with an extra round trip: the window is the span in
// which keys are created, so the fence has to cover it.
func (a *LocalAdmission) admitPostgres(ctx context.Context) error {
	if a.engine != store.EnginePostgres {
		return nil
	}
	pub, req, err := beginPostgresPublicationAdmission(ctx, a.cfg, a.witness, a.localComplete)
	if err != nil {
		return err
	}
	a.pub, a.req = pub, req
	return nil
}

// freezeLocalRequirement is the SQLite half, and there the local record is the whole
// story: a SQLite estate's control cannot live inside the database, because the
// database is the file a restore replaces.
func (a *LocalAdmission) freezeLocalRequirement() {
	if a.localComplete.SHA256 == "" {
		return
	}
	a.req = completedCustodyRequirement(a.localDestinationLabel(), a.localOpID, a.localPlan, a.localComplete.SHA256, a.localComplete)
}

func (a *LocalAdmission) localDestinationLabel() string {
	if !a.target.Anchor().Zero() {
		return a.target.Anchor().Canonical()
	}
	return a.dataDir
}

// CustodyRequirement is the closed answer this admission gives about the custody the
// destination demands. It is read BEFORE the signing keys are loaded, which is the
// only point at which it can change what they do.
func (a *LocalAdmission) CustodyRequirement() CustodyRequirement {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.req
}

// judge reads every held anchor's control and refuses on the first one that forbids
// publication.
//
// A record that exists and cannot be read is a REFUSAL, not an absence. That
// distinction is the same one the store applies, and it is what keeps a corrupt or
// truncated control from being treated as permission.
func (a *LocalAdmission) judge() error {
	for _, anchor := range a.anchors {
		rec, present, err := a.root.Read(anchor)
		if err != nil {
			return fmt.Errorf(
				"the restore control at %s exists and could not be read, and a read that failed is not an absence: %w", anchor.RecordPath(), err)
		}
		if !present {
			continue
		}
		if rec.Destination.Engine != string(a.engine) || (a.engine == store.EngineSQLite && rec.Destination.SQLiteFile != a.target.CanonicalPath()) {
			return fmt.Errorf("%w: local control names another engine or frozen SQLite target", ErrAdmissionDestination)
		}
		if rec.Blocks() {
			return fmt.Errorf(
				"a disaster-recovery operation has left %s in state %q (operation %s): this command will not load signing keys or open the store until that operation is resolved",
				anchor.Canonical(), rec.State, rec.OpID)
		}
		// A COMPLETE control is local evidence that this destination is enrolled. It
		// travels to the store so an absent PostgreSQL control is read as a LOSS
		// rather than as a legacy estate — the one thing the store cannot know from a
		// DSN alone.
		//
		// It is metadata copied from the record and NOT a measurement of the custody
		// this boot will actually select. That distinction is F3-IR-5, and it is
		// CLOSED: the measurement is CustodyObservation, built from the actual loaded
		// key objects, and this value is never used as one.
		if a.engine == store.EnginePostgres &&
			rec.Destination.Database != "" && rec.Destination.Schema != "" {
			a.witness = RestoreEnrolmentWitness{
				Enrolled:     rec.Enrolled,
				Destination:  rec.Destination.Postgres(),
				KeysetSHA256: rec.Keyset.SHA256,
			}
		}
		// The COMPLETE record's own keyset is the ONE place the expected per-purpose
		// selection exists — purpose, custody source and public fingerprint. The
		// database column carries the digest alone, so without this a PostgreSQL
		// refusal could say "the digests differ" and never "your catalog key came
		// from the wrong source".
		//
		// Two local anchors can both carry a record (the data directory and the
		// SQLite file). They are evidence about ONE installation, so a disagreement
		// between them is a binding error rather than a choice: assembling a
		// requirement out of halves of two controls is exactly the laundering this
		// sublot forbids.
		if rec.State == opgate.StateComplete {
			if a.localComplete.SHA256 != "" &&
				(a.localComplete.SHA256 != rec.Keyset.SHA256 || a.localOpID != rec.OpID || a.localPlan != rec.PlanSHA256) {
				return fmt.Errorf("%w: this installation carries two completed restore controls that do not agree on operation, plan or custody, so neither is the expectation this boot is bound to",
					ErrRestorePublicationFenced)
			}
			a.localComplete = rec.Keyset
			a.localOpID = rec.OpID
			a.localPlan = rec.PlanSHA256
		}
	}
	return nil
}

// Open publishes a store for the destination this admission owns.
//
// The configuration is the caller's to assemble — a boot builds it out of dozens of
// settings — but its DESTINATION is not. Engine, DSN and data directory must be the
// ones this admission acquired; anything else is refused rather than admitted,
// because acquiring a second admission for the new destination is precisely the
// re-pointing this object exists to make impossible.
func (a *LocalAdmission) Open(ctx context.Context, signEvent store.AuditEventSigner, observed CustodyObservation, register func(store.ExtensionRegistry) error) (store.Store, error) {
	in, err := a.claimPublication(signEvent, observed)
	if err != nil {
		return nil, err
	}
	return openPrepared(ctx, in.cfg, register, prepareThroughReadiness, nil, in)
}

// claimPublication takes the admission's ONE publication decision, under its own
// mutex and released before the constructor runs — the constructor calls back in
// for the derived lease, and holding the mutex across it would deadlock.
func (a *LocalAdmission) claimPublication(signEvent store.AuditEventSigner, observed CustodyObservation) (publicationInputs, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.closed:
		return publicationInputs{}, fmt.Errorf("%w: this local admission has been closed", opgate.ErrLeaseClosed)
	case a.published:
		return publicationInputs{}, errors.New("sqlstore: this local admission has already made its publication decision; a second Open would be a second decision under one acquisition")
	case a.req.required && !observed.Present():
		// The requirement was handed to the caller before it loaded a key. Arriving
		// here without the measurement means the strict enrolled load did not happen,
		// and the comparison the completed control exists for cannot be made at all.
		return publicationInputs{}, fmt.Errorf(
			"%w: %s carries a COMPLETED restore control (operation %s) and this Open supplied no observation of the custody it loaded",
			ErrRestorePublicationFenced, a.req.destination, a.req.opID)
	}
	a.published = true
	cfg := a.cfg
	cfg.SignEvent = signEvent
	return publicationInputs{
		cfg: cfg, witness: a.witness, admission: a, pub: a.pub, req: a.req, observed: observed,
	}, nil
}

// deriveStoreLease hands the store's fence a CHILD of this admission's live shared
// root, over exactly the one anchor the store needs.
//
// DeriveShared and not Derive: an admission is always rooted at a SHARED
// acquisition, and requiring that here means a lease that somehow descended from an
// exclusive restore could never be presented as an ordinary publication token.
func (a *LocalAdmission) deriveStoreLease(anchor opgate.Anchor) (*opgate.Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.root == nil {
		return nil, fmt.Errorf("%w: %v", ErrRestoreCoordinationUnknown,
			errors.New("the boot's local admission is not held, so the store has no ownership of this destination to publish under"))
	}
	if a.target.Anchor().LockPath() != anchor.LockPath() {
		return nil, fmt.Errorf("%w: it fenced %s and the store resolved %s",
			ErrAdmissionDestination, a.target.Anchor().Canonical(), anchor.Canonical())
	}
	lease, err := a.root.DeriveShared(anchor)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRestoreCoordinationUnknown, err)
	}
	return lease, nil
}

// Close gives the admission back. It is idempotent, so a caller can both defer it
// against an early failure and release it explicitly at the point the publication
// decision has been made.
//
// It is released at that point and not later. The engine a boot returns keeps
// running, and a construction lock held by a serving process would claim an
// exclusion over already-published stores that the ratified contract explicitly does
// not promise; client drain stays the operator's prerequisite.
func (a *LocalAdmission) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	// The retained PostgreSQL session is retired HERE, on every path. It was taken
	// before the loaders and it is given back once the publication decision has been
	// made (or the boot has failed), never held into the life of the served engine.
	if a.pub != nil {
		if err := a.pub.close(); err != nil {
			slog.Warn("the retained restore publication admission could not be confirmed released", "err", err)
		}
		a.pub = nil
	}
	if a.root == nil {
		return
	}
	if err := a.root.Release(); err != nil {
		slog.Warn("the local restore control lease could not be confirmed released", "err", err)
	}
	a.root = nil
}
