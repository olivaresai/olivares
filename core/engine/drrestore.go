// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// The restore-coordination sentinels, re-exported so a composition root can
// classify a refusal without reaching into an internal package.
var (
	// ErrRestorePublicationBusy means a restore holds publication exclusion on this
	// destination right now. It is produced inside ONE try: nothing waits for a
	// restore to finish.
	ErrRestorePublicationBusy = sqlstore.ErrRestorePublicationBusy
	// ErrRestoreCoordinationUnknown means coordination could not be established.
	// It is never reported as "no restore is running".
	ErrRestoreCoordinationUnknown = sqlstore.ErrRestoreCoordinationUnknown
	// ErrRestorePublicationFenced means the destination's durable control forbids
	// publishing a store for it.
	ErrRestorePublicationFenced = sqlstore.ErrRestorePublicationFenced
	// ErrRestoreControlConflict means the destination carries a control that belongs
	// to another operation, another plan or another destination.
	ErrRestoreControlConflict = sqlstore.ErrRestoreControlConflict
)

// PendingRestoreSpec selects only the initial operation and plan, never a state
// or completed keyset. Neither this value nor a report grants maintenance access.
type PendingRestoreSpec = sqlstore.PendingRestoreSpec

// RestoreControlReport is durable testimony about a destination's control, read
// back inside the transaction that wrote it.
type RestoreControlReport = sqlstore.RestoreControlReport

// RestoreEnrolmentWitness is durable LOCAL evidence that a destination carries a
// restore control. It can only TIGHTEN a publication decision: it turns an absent
// control from the accepted legacy_or_lost_unknown limit into a refusal, and it
// supplies the custody digest a completed control is compared against.
type RestoreEnrolmentWitness = sqlstore.RestoreEnrolmentWitness

// InstallPendingRestoreControl installs or verifies a destination's restore control.
//
// It is the EARLY, MINIMAL installer: it opens no Store, runs no migration and
// creates no SYSTEM tenant, genesis event, directory authority, epoch or relation
// other than the control itself. On an empty destination it leaves a database that
// carries the control and nothing else.
//
// Like Open, this adds no behaviour of its own; it is the visibility seam for the
// internal implementation so a composition root outside /core can install a
// pending control without reaching into an internal package.
func InstallPendingRestoreControl(ctx context.Context, cfg store.Config, spec PendingRestoreSpec) (RestoreControlReport, error) {
	return sqlstore.InstallPendingRestoreControl(ctx, cfg, spec)
}

// ReadRestoreControl reports a destination's control under the publication fence,
// changing nothing.
func ReadRestoreControl(ctx context.Context, cfg store.Config) (RestoreControlReport, error) {
	return sqlstore.ReadPostgresRestoreControl(ctx, cfg)
}

// OpenWithRestoreWitness is Open, plus the local evidence the composition root
// holds about whether this destination was ever enrolled in a restore control.
//
// It exists because the witness is a LOCAL fact — it lives beside the custody in
// the data directory — and the store constructor only ever sees a DSN. Without it,
// a destination whose control was installed and then dropped would be
// indistinguishable from one that never had a control, which is the difference
// between a refusal and an ordinary boot.
//
// The witness is a closed value with no field that can loosen anything: Open's own
// behaviour is exactly OpenWithRestoreWitness with the zero witness, and passing a
// witness can only turn an absent control into a refusal or add a custody
// comparison. It is never a bypass, and there is deliberately no counterpart that
// suppresses the fence.
func OpenWithRestoreWitness(ctx context.Context, cfg store.Config, register func(store.ExtensionRegistry) error, witness RestoreEnrolmentWitness) (store.Store, error) {
	return sqlstore.OpenWithRestoreWitness(ctx, cfg, register, witness)
}

// BootPublication is one boot's LOCAL publication ownership, carried from the
// composition root into the store.
//
// It is the visibility seam and nothing more: cmd/olivares cannot import
// core/internal/store/sqlstore, so the concrete admission has to be reachable
// through this package. It is NOT a plugin extension interface — it has no callback,
// no field a caller can set, no context value and no boolean that suppresses a
// fence, and the schema registrar it forwards still receives only
// store.ExtensionRegistry.
//
// Its lifetime is the span in which a boot can create custody or publish a store for
// its destination: taken before the first signing key is loaded, released
// immediately after the store's publication decision. It is deliberately NOT held
// for the life of the engine — these locks coordinate construction, and a lock held
// by a serving process would advertise a revocation protocol the product does not
// have.
type BootPublication struct {
	adm *sqlstore.LocalAdmission
}

// CustodyRequirement is the closed answer this boot's admission gives about the
// custody its destination demands, read BEFORE any signing key is loaded.
type CustodyRequirement = sqlstore.CustodyRequirement

// CustodyObservation is the typed measurement of the signers a boot ACTUALLY loaded.
// It is produced only by ObserveSelectedCustody, from the real key objects.
type CustodyObservation = sqlstore.CustodyObservation

// SelectedSigner is one purpose's ACTUAL loaded key, as the composition root holds
// it: the same object it is about to construct a signer from, and the loader mode it
// resolved under.
//
// The private key is taken rather than a public one on purpose. The measurement has
// to be of the key this process will SIGN with, and a separately supplied public key
// is exactly the caller-supplied digest the contract forbids. Nothing here is
// retained: ObserveSelectedCustody derives the public half, fingerprints it and keeps
// only that.
type SelectedSigner struct {
	// Purpose is "audit", "catalog" or "policy".
	Purpose string
	// LoaderMode is the custody mode the loader actually resolved under (the
	// custodyMode* vocabulary: minted, byok-env, byok-file, cmek).
	LoaderMode string
	// Key is the loaded private key object itself.
	Key ed25519.PrivateKey
}

// ObserveSelectedCustody builds the ONE immutable observation of this boot's actual
// selected custody, from the actual loaded key objects.
//
// It is the measurement side of F3-IR-5. The defect it replaces read the digest a
// completed control recorded, copied it into the value it called "observed", and
// compared the two — a comparison that passes for every input, including three keys
// minted seconds earlier. So this derives purpose, custody source and the SHA-256 of
// the RAW PUBLIC KEY from the objects themselves, and the canonical keyset digest is
// computed from those, through opgate's single encoder.
//
// No key bytes are retained and none can be printed: the result carries fingerprints.
func ObserveSelectedCustody(signers ...SelectedSigner) (CustodyObservation, error) {
	if len(signers) != 3 {
		return CustodyObservation{}, fmt.Errorf("engine: the selected custody observation is exactly the audit, catalog and policy signers, and %d were supplied", len(signers))
	}
	keys := make([]opgate.SelectedKey, 0, len(signers))
	for _, s := range signers {
		source, err := opgate.CustodySourceForLoader(s.LoaderMode)
		if err != nil {
			return CustodyObservation{}, fmt.Errorf("engine: %s signing key: %w", s.Purpose, err)
		}
		if len(s.Key) != ed25519.PrivateKeySize {
			return CustodyObservation{}, fmt.Errorf("engine: the %s signing key object is not a loaded Ed25519 private key, so there is nothing actual to observe", s.Purpose)
		}
		fingerprint, err := opgate.FingerprintPublicKey(s.Key.Public().(ed25519.PublicKey))
		if err != nil {
			return CustodyObservation{}, fmt.Errorf("engine: %s signing key: %w", s.Purpose, err)
		}
		keys = append(keys, opgate.SelectedKey{
			Purpose: opgate.KeyPurpose(s.Purpose), Source: source, PublicSHA256: fingerprint,
		})
	}
	return sqlstore.NewCustodyObservation(keys)
}

// BeginBootPublication takes the publication admission of one installation.
//
// It resolves the SQLite destination ONCE through the authoritative resolver,
// acquires every local anchor the installation has, reads their durable controls,
// and — on PostgreSQL — takes the SHARED coordination on the destination's own
// server and reads the control there too. A blocking, pending, malformed or
// unreadable control refuses HERE, before the caller has loaded or minted a key.
//
// ⛔ THE POSTGRESQL READ IS PART OF THIS CALL, and its position is the property
// (F3-IR-5). A remote database can carry a completed or pending control on a node
// that has nothing on disk, so a boot that consulted the server only inside Open
// would already have loaded — or MINTED — three signing keys before learning the
// destination had been restored. Taking a probe here and releasing it before the
// loaders would leave the same window open, so the session is RETAINED and carried
// into Open, where it makes the final decision.
//
// cfg is the destination configuration this admission binds to. Open takes no Config
// at all: it takes the audit signer, so there is no second connection configuration
// for anything to re-point between the two calls.
func BeginBootPublication(ctx context.Context, cfg store.Config, dataDir string) (*BootPublication, error) {
	adm, err := sqlstore.BeginLocalAdmission(ctx, cfg, dataDir)
	if err != nil {
		return nil, err
	}
	return &BootPublication{adm: adm}, nil
}

// CustodyRequirement reports what this destination demands of the caller's key
// loading. It is read BEFORE the loaders run, which is the only point at which it can
// change what they do: a completed control means the boot must LOAD the exact custody
// that operation published and never mint, and an unenrolled destination is the
// ordinary first boot that mints its keys.
func (b *BootPublication) CustodyRequirement() CustodyRequirement {
	if b == nil || b.adm == nil {
		return CustodyRequirement{}
	}
	return b.adm.CustodyRequirement()
}

// Open publishes a store under this admission's ownership.
//
// The store's own fence is a CHILD derived from the live shared root this admission
// holds — not a second, unrelated acquisition, and on PostgreSQL the decision is made
// on the very session the control was read on. The destination is the one the
// admission froze: this call cannot name another, because it is handed no
// configuration to name one with.
//
// observed is the measurement of the custody this boot actually loaded. When the
// admission reported a completed control, an absent observation is refused here
// rather than at the end — the comparison that control exists for cannot be made
// without it.
func (b *BootPublication) Open(ctx context.Context, signEvent store.AuditEventSigner, observed CustodyObservation, register func(store.ExtensionRegistry) error) (store.Store, error) {
	if b == nil || b.adm == nil {
		return nil, errors.New("engine: this boot has no local publication admission, and an ordinary Open must not manufacture one for a destination it never fenced")
	}
	return b.adm.Open(ctx, signEvent, observed, register)
}

// Close gives the admission back. It is idempotent, so a caller can defer it against
// an early failure and also release it explicitly once the publication decision has
// been made.
func (b *BootPublication) Close() {
	if b == nil || b.adm == nil {
		return
	}
	b.adm.Close()
}

// IsRestoreRefusal reports whether err is any of the coordination refusals, so a
// caller can render one operator sentence for the family without matching four
// sentinels.
func IsRestoreRefusal(err error) bool {
	return errors.Is(err, ErrRestorePublicationBusy) ||
		errors.Is(err, ErrRestoreCoordinationUnknown) ||
		errors.Is(err, ErrRestorePublicationFenced)
}
