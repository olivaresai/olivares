// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package audit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

func TestCommunityTreeEvidenceRefusesBeforeReadingInputs(t *testing.T) {
	_, checkpointErr := audit.VerifyTreeCheckpoint(nil, "", nil)
	_, found, proofErr := audit.ProveInclusion(context.Background(), nil, 1, 1)
	for name, err := range map[string]error{
		"checkpoint": checkpointErr,
		"proof":      proofErr,
		"ledger":     audit.VerifyTreeAgainstLedger(context.Background(), nil, audit.TreeCheckpoint{}),
		"inclusion":  audit.VerifyInclusion(audit.TreeInclusion{}, audit.TreeCheckpoint{}),
	} {
		if !errors.Is(err, audit.ErrBusinessAudit) {
			t.Errorf("%s = %v, want Business refusal", name, err)
		}
	}
	if found {
		t.Fatal("Community returned a proof")
	}
}
