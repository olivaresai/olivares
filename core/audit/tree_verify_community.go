// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package audit

import (
	"context"
	"crypto/ed25519"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func VerifyTreeCheckpoint(signed []byte, tenant model.TenantID, pub ed25519.PublicKey) (TreeCheckpoint, error) {
	return TreeCheckpoint{}, ErrBusinessAudit
}
func VerifyTreeAgainstLedger(ctx context.Context, log store.AuditLog, cp TreeCheckpoint) error {
	return ErrBusinessAudit
}
func ProveInclusion(ctx context.Context, log store.AuditLog, seq, size int64) (TreeInclusion, bool, error) {
	return TreeInclusion{}, false, ErrBusinessAudit
}
func VerifyInclusion(p TreeInclusion, cp TreeCheckpoint) error {
	return ErrBusinessAudit
}
