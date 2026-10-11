//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Status is the Community no-op seam. PIV verification belongs to Business.
func (*PIVConfig) Status(context.Context, []*x509.Certificate, time.Time) PIVStatus {
	return PIVStatus{}
}

// ElevatePIVSession cannot grant certificate assurance in Community.
func (*Authenticator) ElevatePIVSession(context.Context, Principal, *PIVConfig, []*x509.Certificate) (model.AuthSession, PIVStatus, error) {
	return model.AuthSession{}, PIVStatus{}, ErrPIVNotConfigured
}
