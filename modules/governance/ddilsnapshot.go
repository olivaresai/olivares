// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// surfaceCedarDDIL is the third Cedar authoring surface: policy ADOPTED from a signed DDIL
// bundle. It is kept separate from the operator's free-form `cedar` surface and the
// structured `cedar-managed` projection so an adoption never clobbers local authoring and the
// revision history shows exactly what arrived by bundle.
const surfaceCedarDDIL = "cedar-ddil"

// ActivePolicySnapshot returns the tenant's ACTIVE Cedar policy as the exact union text the
// scoped-grant engine evaluates, plus its content-addressed revision id ("sha256:<hex>" of the
// snapshot bytes). The revision is content-addressed because per-node (surface, revision)
// counters do not travel across nodes, so bundle policy identity must derive from content.
// ok is false when the tenant has no active policy on any surface.
func ActivePolicySnapshot(ctx context.Context, st store.Store, tenant model.TenantID) (snapshot, revision string, ok bool, err error) {
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		free, freeOK, readErr := latestActiveContent(ctx, sc, surfaceCedar)
		if readErr != nil {
			return readErr
		}
		managed, managedOK, readErr := latestActiveContent(ctx, sc, surfaceCedarManaged)
		if readErr != nil {
			return readErr
		}
		adopted, adoptedOK, readErr := latestActiveContent(ctx, sc, surfaceCedarDDIL)
		if readErr != nil {
			return readErr
		}

		// ok mirrors reloadTenantGrants' activation rule: an active revision whose
		// union is EMPTY text means "no policy" there (the engine clears and abstains),
		// so a bundle must not carry an empty snapshot with a revision id either.
		snapshot = mergeCedarSources(mergeCedarSources(free, managed), adopted)
		ok = (freeOK || managedOK || adoptedOK) && strings.TrimSpace(snapshot) != ""
		if !ok {
			snapshot = ""
			return nil
		}
		sum := sha256.Sum256([]byte(snapshot))
		revision = "sha256:" + hex.EncodeToString(sum[:])
		return nil
	})
	return snapshot, revision, ok, err
}
