// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/spf13/cobra"
)

// Internal bridge for pods whose PostgreSQL client runs in a different image.
// Both images use the same uid and a private directory on their shared emptyDir.
func postgresServiceFileCmd() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:          "pg-service-file <file>",
		Short:        "Write a private PostgreSQL service file for the internal client bridge",
		Long:         "Olivares uses this internal PostgreSQL client bridge itself; people do not need to run it.",
		Example:      "  olivares db pg-service-file --dsn=env:SUPERUSER_DSN /work/postgres/pg_service.conf",
		Hidden:       true,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveDSNRef(cmd.Context(), "--dsn", dsn, osGetenv)
			if err != nil {
				return err
			}
			body, err := postgresServiceEntry(resolved)
			if err != nil {
				return err
			}
			dir := filepath.Dir(args[0])
			if err := secure.EnsureDir(dir); err != nil {
				return err
			}
			info, err := os.Stat(dir)
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o077 != 0 {
				return errors.New("PostgreSQL service file requires a private directory")
			}
			file, err := os.OpenFile(args[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, writeErr := file.Write(body)
			if err := errors.Join(writeErr, file.Close()); err != nil {
				_ = os.Remove(args[0])
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dsn, "dsn", "", "connection reference for the internal PostgreSQL client bridge")
	_ = cmd.MarkFlagRequired("dsn")
	return cmd
}
