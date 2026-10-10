// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/ddil"
	"github.com/spf13/cobra"
)

func runDDILExport(*cobra.Command, string, string, string, string, string, string, int64, int, []string, time.Duration, time.Duration, string, bool) error {
	return exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
func ddilArchiveVerifyOptions([]string, []string) (audit.ArchiveVerifyOptions, error) {
	return audit.ArchiveVerifyOptions{}, nil
}
func deriveDDILArchiveCursor(context.Context, string, string) (int64, error) {
	return 0, exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
func applyDDILAuditSegments(context.Context, string, string, int64, []ddil.SegmentRef, map[string][]byte, audit.ArchiveVerifyOptions) (int, int64, error) {
	return 0, 0, exitcode.New(exitcode.Edition, audit.ErrBusinessAudit)
}
