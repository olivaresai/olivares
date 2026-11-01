// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/spf13/cobra"
)

func runAuditVerify(cmd *cobra.Command, dataDir, engine, dsn, ownerDSN, tenant, pubAlg string, pubB64s, eventPubB64s []string, fromSeq int64, strict bool) error {
	return exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}

const auditVerificationAvailable = false

func runAuditTreeProve(cmd *cobra.Command, dataDir, engine, dsn, ownerDSN, tenant, checkpointPath, pubkey string, seq int64) error {
	return exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}

func runAuditTreeVerify(cmd *cobra.Command, dataDir, engine, dsn, ownerDSN, tenant, checkpointPath, pubkey, proofPath string) error {
	return exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
