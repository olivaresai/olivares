// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// acquireBootPublication fences this installation's data directory and, for SQLite,
// its store file, for the whole span in which this boot could create custody or
// publish a store.
//
// It is taken BEFORE the signing keys are loaded and released only after the store
// has decided whether to publish. That span is the whole point, and the shorter
// version of it — read the control, release, then load keys — is the defect it exists
// to remove: key loading MINTS on a data directory that has none, so a boot that
// merely checked and let go could create a fresh audit key inside the window a
// restore was replacing that very custody.
//
// It is SHARED, not exclusive. Several ordinary boots of one installation are a
// normal posture; what must not coexist with them is an operation that changes the
// destination, and that one takes the anchors exclusively.
//
// ⛔ THE TWO THINGS THAT USED TO LIVE HERE AND NO LONGER DO:
//
//  1. A SECOND DSN STRIPPER. This file had its own `sqliteDSNPath`, the store had
//     another, and neither implemented the pinned driver's grammar — so a boot could
//     fence one path while the store opened a different file (F3-IR-2). There is now
//     exactly one resolver, in core/dr/opgate, and both the fence and the driver's
//     DSN come out of it.
//  2. A SECOND, UNRELATED LEASE. The boot took its own lease and the store took
//     another, and the two were reconciled by a registry rule that granted any shared
//     request whenever this process held anything at all — which also handed an
//     unrelated engine.Open the console restore's exclusive hold (F3-IR-1). The
//     admission returned here now CARRIES its ownership into the store, which derives
//     a child of it rather than acquiring again.
//  3. A POSTGRESQL PROBE THAT LET GO. The database's own control was read inside the
//     store constructor, which runs AFTER the three loaders — so a node whose remote
//     destination had already been restored minted three fresh keys and only then
//     learned the fact (F3-IR-5). It is read here now, before the loaders, on a
//     session the admission RETAINS through the store's final decision. Taking it
//     here and releasing it before the loaders would leave the identical window.
func acquireBootPublication(ctx context.Context, cfg store.Config, dataDir string) (*coreengine.BootPublication, error) {
	return coreengine.BeginBootPublication(ctx, cfg, dataDir)
}

// observeSelectedCustody measures the custody this boot ACTUALLY loaded, from the
// three key objects it is about to construct its signers from.
func observeSelectedCustody(auditKey, catalogKey, policyKey loadedSigningKey) (coreengine.CustodyObservation, error) {
	return coreengine.ObserveSelectedCustody(
		coreengine.SelectedSigner{Purpose: "audit", LoaderMode: auditKey.mode, Key: auditKey.priv},
		coreengine.SelectedSigner{Purpose: "catalog", LoaderMode: catalogKey.mode, Key: catalogKey.priv},
		coreengine.SelectedSigner{Purpose: "policy", LoaderMode: policyKey.mode, Key: policyKey.priv},
	)
}

// recheckSelectedCustody re-resolves the selection an enrolled boot will serve with,
// immediately before publication, and refuses any difference.
//
// # What it can and cannot promise
//
// The local lease fences THIS installation's files. It does not immobilize a mounted
// Kubernetes Secret, a KMS key or a KEK grant — those live outside the machine and
// can be replaced, unmounted or revoked while this boot is running. So the honest
// property is DETECTION, not prevention: the selection is measured again through the
// same strict, load-only path, and a changed, missing or inaccessible source refuses
// the boot rather than being served.
//
// It never substitutes. The recheck runs in the strict enrolled mode, which has no
// branch that creates a key and no fallback from a configured source to a local one,
// so a failure here produces a refusal and NO new key bytes — never a second, quietly
// different selection. The keys it loads are discarded: the signers already built from
// the first load are the ones that serve, and this is a measurement of them.
// base is the SAME option list the first pass used, with the strict enrolled mode added
// exactly as the first pass added it. Deriving it rather than rebuilding it is the whole
// point: the recheck's only job is to be a faithful second measurement of the SAME
// selection, and a hand-written option list is the one place where a silently divergent
// third option would make a changed selection look unchanged.
func recheckSelectedCustody(dataDir string, base []keyLoadOption, observed coreengine.CustodyObservation) error {
	// The loaders log what they resolved; they already did that on the first pass, and
	// a second copy of those lines would read like two selections were made.
	quiet := slog.New(slog.DiscardHandler)
	opts := append(append([]keyLoadOption(nil), base...), withEnrolledCustody())
	auditKey, err := loadAuditSigningKey(dataDir, quiet, opts...)
	if err != nil {
		return fmt.Errorf("re-resolve the selected audit custody before publication: %w", err)
	}
	catalogKey, err := loadCatalogSigningKey(dataDir, quiet, opts...)
	if err != nil {
		return fmt.Errorf("re-resolve the selected catalog custody before publication: %w", err)
	}
	policyKey, err := loadPolicySigningKey(dataDir, quiet, opts...)
	if err != nil {
		return fmt.Errorf("re-resolve the selected policy custody before publication: %w", err)
	}
	again, err := observeSelectedCustody(auditKey, catalogKey, policyKey)
	if err != nil {
		return fmt.Errorf("re-observe the selected custody before publication: %w", err)
	}
	if again.KeysetSHA256() != observed.KeysetSHA256() {
		return fmt.Errorf(
			"the selected signing custody CHANGED while this boot was preparing: it loaded %s and the sources now resolve to %s — the store is not published under a selection that moved, and no key was created or replaced to reconcile them",
			observed.KeysetSHA256(), again.KeysetSHA256())
	}
	return nil
}
