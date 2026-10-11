// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
)

func newAuditArchiveLoop(cfg auditArchiveConfig, _ store.Store, _ *audit.Signer, _ []ed25519.PublicKey, _ *slog.Logger) (*auditArchiveLoop, error) {
	if cfg.sink == "" {
		return nil, nil
	}
	_, err := buildAuditArchiveSink(cfg, nil)
	return nil, err
}
func buildAuditArchiveSink(auditArchiveConfig, *slog.Logger) (audit.ArchiveSink, error) {
	return nil, fmt.Errorf("%s requires Business; refusing to start instead of silently disabling archival", auditArchiveSinkEnv)
}
func (*auditArchiveLoop) register(*runtime.Runtime) error { return audit.ErrBusinessAudit }
func (*auditArchiveLoop) runOnce(context.Context) error   { return audit.ErrBusinessAudit }
