// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/spf13/cobra"
)

func runAuditExport(*cobra.Command, string, string, string, string, string) error {
	return exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
func runAuditArchiveExport(*cobra.Command, string, string, string, string, string, int64, int) error {
	return exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
func runAuditArchiveVerify(*cobra.Command, string, string, []string, []string, bool) error {
	return exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
func archiveVerifyOptions(string, string, []string, []string) (audit.ArchiveVerifyOptions, bool, error) {
	return audit.ArchiveVerifyOptions{}, false, exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
