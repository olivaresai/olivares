// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// The `olivares migrate` command exposes the engine's schema-migration state to an
// operator (read-only). Migrations apply automatically and idempotently at boot in
// the online expand-contract model (core/migrate); an operator still needs to SEE
// what a database carries — which versions, in which phase, any reverted — to verify
// an upgrade and reason about rollback safety (rolling the BINARY back across a
// CONTRACT migration is unsafe). `migrate status` is that read-only window: it opens
// a transient connection and never writes, migrates or reverts.
func newMigrateCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "migrate",
		Short: "Inspect the engine's schema-migration state (read-only)",
		Example: "  olivares migrate status --data-dir /var/lib/olivares\n" +
			"  olivares migrate manifest > schema-manifest.json",
		Long: "Schema migrations apply automatically at boot in the online expand-contract model\n" +
			"(an additive `expand` ships first; a destructive `contract` only a LATER release). This\n" +
			"command shows what a database already carries, so you can verify an upgrade and judge\n" +
			"rollback safety — without a SQL client. It opens a transient connection and applies nothing.\n" +
			"See the upgrade/rollback runbook: docs/UPGRADE-AND-ROLLBACK.md.",
	}
	addTextJSONFormatFlag(root)
	manifest := migrateManifestCmd()
	manifest.Example = "  olivares migrate manifest > schema-manifest.json"
	manifest.RunE = func(cmd *cobra.Command, _ []string) error {
		man, err := collectSchemaManifest()
		if err != nil {
			return err
		}
		// render-exempt: this document IS the open-vs-enterprise parity ORACLE.
		// Both editions run this command and their bytes are compared, so the
		// serialization is a contract between two binaries, not a presentation
		// choice — reformatting it for a human would break the comparison.
		b, err := json.MarshalIndent(man, "", "  ")
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		result := struct {
			Manifest *schemaManifest `json:"manifest"`
			SHA256   string          `json:"sha256"`
		}{Manifest: man, SHA256: hex.EncodeToString(sum[:])}
		return renderOut(cmd, func(out io.Writer) error {
			if _, err := fmt.Fprintln(out, string(b)); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "sha256:%s\n", result.SHA256)
			return err
		}, result)
	}
	root.AddCommand(migrateStatusCmd(), manifest)
	return root
}

func migrateStatusCmd() *cobra.Command {
	var engine, dataDir, dsn string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List applied schema migrations and their expand/contract phase (read-only)",
		Long: "Reads the version-tracking tables (schema_migrations_core + per-module) and prints every\n" +
			"applied migration with its phase and apply time. For sqlite it opens <data-dir>/olivares.db;\n" +
			"for postgres pass --dsn (accepts a file:/env: reference). It never writes, migrates or reverts.",
		Example: `  # Inspect the default SQLite data directory
  olivares migrate status --data-dir /var/lib/olivares

  # Inspect Postgres through an environment-backed DSN
  olivares migrate status --engine postgres --dsn env:DATABASE_URL`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var eng store.Engine
			switch engine {
			case string(store.EngineSQLite):
				eng = store.EngineSQLite
			case string(store.EnginePostgres):
				eng = store.EnginePostgres
			default:
				return fmt.Errorf("--engine %q must be sqlite or postgres", engine)
			}

			cfg := store.Config{Engine: eng}
			switch eng {
			case store.EngineSQLite:
				resolved := dsn
				if resolved == "" {
					dir := dataDir
					if dir == "" {
						d, derr := defaultDataDir()
						if derr != nil {
							return derr
						}
						dir = d
					}
					resolved = filepath.Join(dir, "olivares.db")
				}
				// Read-only intent: don't let the open CREATE an empty database as a side
				// effect of a status query — report a missing store plainly instead. A
				// directory at the path is treated the same (the driver would otherwise
				// fail with a cryptic "unable to open database file").
				if info, serr := os.Stat(resolved); serr != nil || !info.Mode().IsRegular() {
					return fmt.Errorf("no sqlite database at %q — run the engine once to create and migrate it, or pass --dsn/--data-dir", resolved)
				}
				cfg.DSN = resolved
			case store.EnginePostgres:
				if dsn == "" {
					return fmt.Errorf("--dsn is required for --engine postgres (accepts a file:/env: reference)")
				}
				r, err := resolveDSNRef(cmd.Context(), "--dsn", dsn, osGetenv)
				if err != nil {
					return err
				}
				cfg.DSN = r
			}

			recs, err := coreengine.MigrationStatus(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			items := make([]migrationStatusItem, 0, len(recs))
			for _, r := range recs {
				phase := r.Phase
				if phase == "" {
					phase = "expand"
				}
				state := "applied"
				if r.Reverted {
					state = "reverted"
				}
				items = append(items, migrationStatusItem{
					Table: r.Table, Version: r.Version, Name: r.Name, Phase: phase,
					State: state, AppliedAt: r.AppliedAt,
				})
			}
			return renderOut(cmd, func(out io.Writer) error {
				return printMigrationStatus(out, recs)
			}, items)
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "sqlite", "store engine: sqlite or postgres")
	_ = cmd.RegisterFlagCompletionFunc("engine", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"sqlite", "postgres"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory holding olivares.db (sqlite; defaults to $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().StringVar(&dsn, "dsn", "", "connection string to read (postgres; or an explicit sqlite file path). Accepts a file:/env: reference")
	return cmd
}

type migrationStatusItem struct {
	Table     string `json:"table"`
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	State     string `json:"state"`
	AppliedAt string `json:"applied_at"`
}

// printMigrationStatus renders the applied migrations as a table plus an
// expand/contract/reverted summary, and a rollback caution when any contract
// migration is present.
func printMigrationStatus(out io.Writer, recs []store.MigrationRecord) error {
	if len(recs) == 0 {
		_, err := fmt.Fprintln(out, "no schema-migration tracking tables found (the engine has not migrated this database yet)")
		return err
	}
	// render-exempt: this IS the text branch. printMigrationStatus is the
	// textFn renderOut calls; the JSON branch renders the same records.
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TRACKING TABLE\tVERSION\tPHASE\tSTATE\tAPPLIED AT\tNAME")
	var expand, contract, reverted int
	for _, r := range recs {
		phase := r.Phase
		if phase == "" {
			phase = "expand"
		}
		state := "applied"
		if r.Reverted {
			state = "reverted"
			reverted++
		}
		if phase == "contract" {
			contract++
		} else {
			expand++
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", r.Table, r.Version, phase, state, orDash(r.AppliedAt), r.Name)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "\n%d migration(s): %d expand, %d contract, %d reverted.\n", len(recs), expand, contract, reverted); err != nil {
		return err
	}
	if contract > 0 {
		fmt.Fprintln(out, "Note: a CONTRACT migration is a destructive cleanup. Rolling the BINARY back to a")
		fmt.Fprintln(out, "release from before a contract is unsafe — the older code may depend on what the")
		_, err := fmt.Fprintln(out, "contract removed. See docs/UPGRADE-AND-ROLLBACK.md before rolling back across one.")
		return err
	}
	return nil
}
