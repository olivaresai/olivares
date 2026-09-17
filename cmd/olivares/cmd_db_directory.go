// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// directoryActivationResult is the rendered outcome of the ceremony. It is a
// projection of engine.DirectoryWriterActivationResult plus the operator's own
// assertions, so the JSON form is a complete record of what was claimed and
// what the store answered.
type directoryActivationResult struct {
	Engine             string                    `json:"engine"`
	ExpectedGeneration int64                     `json:"expected_generation"`
	Actor              string                    `json:"actor"`
	Reason             string                    `json:"reason"`
	WritersUpgraded    bool                      `json:"writers_upgraded"`
	WritersDrained     bool                      `json:"writers_drained"`
	Before             directoryActivationStatus `json:"before"`
	After              directoryActivationStatus `json:"after"`
	Changed            bool                      `json:"changed"`
	ReopenRequired     bool                      `json:"reopen_required"`
	Error              string                    `json:"error,omitempty"`
}

type directoryActivationStatus struct {
	ControlMode                   string `json:"control_mode"`
	ExpectedGeneration            int64  `json:"expected_generation"`
	WriterPosture                 string `json:"writer_posture"`
	EpochCoverageComplete         bool   `json:"epoch_coverage_complete"`
	CoverageProtocol              string `json:"coverage_protocol"`
	UserAuthorityCoverageComplete bool   `json:"user_authority_coverage_complete"`
	InventoryAuthority            string `json:"inventory_authority"`
	InventoryOrgCount             int    `json:"inventory_org_count"`
	InventoryBusinessOrgCount     int    `json:"inventory_business_org_count"`
	InventoryEpochCount           int    `json:"inventory_epoch_count"`
}

func directoryActivationStatusOf(status store.DirectoryStatus) directoryActivationStatus {
	return directoryActivationStatus{
		ControlMode: string(status.ControlMode), ExpectedGeneration: status.ExpectedGeneration,
		WriterPosture: string(status.WriterPosture), EpochCoverageComplete: status.EpochCoverageComplete,
		CoverageProtocol: status.CoverageProtocol, UserAuthorityCoverageComplete: status.UserAuthorityCoverageComplete,
		InventoryAuthority: status.InventoryAuthority, InventoryOrgCount: status.InventoryOrgCount,
		InventoryBusinessOrgCount: status.InventoryBusinessOrgCount, InventoryEpochCount: status.InventoryEpochCount,
	}
}

// dbActivateDirectoryWriterCmd exposes the engine's explicit directory writer
// activation ceremony (core/engine.ActivateDirectoryWriterMaintenance) as an operator
// maintenance step. It is the only production path that moves the durable
// legacy writer control to the User authority protocol; ordinary `serve` never asserts the
// two cluster facts the database cannot inspect (every writer upgraded, old
// writers drained). Those are flags the operator sets on purpose, with an
// actor and a reason that land in the engine log.
func dbActivateDirectoryWriterCmd() *cobra.Command {
	var (
		engineName, dataDir, dsn, ownerDSN, adminDSN string
		expectedGeneration                           int64
		actor, reason                                string
		writersUpgraded, writersDrained              bool
	)
	cmd := &cobra.Command{
		Use:   "activate-directory-writer",
		Short: "Activate the User authority writer protocol on a stopped store (reopen required)",
		Long: "Runs User authority activation on a STOPPED store: staged or enforced legacy generation N\n" +
			"becomes enforced generation N+1 with protocol user-authority-v1 after complete H/org/G proof.\n" +
			"The database cannot inspect two cluster facts, so you assert them explicitly: every writer\n" +
			"process runs a binary carrying the current User authority writer protocol (--writers-upgraded) and no old writer\n" +
			"transaction is in flight (--writers-drained). Stop every engine on this store first; the\n" +
			"ceremony holds the store's migration lock while it runs and its result is a boot witness, so\n" +
			"every engine must be REOPENED afterwards before readiness can observe the enforced control.\n" +
			"An indeterminate commit is reported as such and never as success: reopen and re-run to verify.",
		Example: `  # Fresh single-node SQLite estate: the initial staged generation is 1
  olivares db activate-directory-writer --data-dir /var/lib/olivares \
    --expected-generation 1 --writers-upgraded --writers-drained \
    --actor operator@example.test --reason "single-node estate, serve stopped"

  # Postgres with the least-privilege split
  olivares db activate-directory-writer --engine postgres --dsn env:DATABASE_URL \
    --owner-dsn env:OWNER_DSN --admin-dsn env:ADMIN_DSN --expected-generation 1 \
    --writers-upgraded --writers-drained --actor ops --reason "rollout complete"`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var missing []string
			if !writersUpgraded {
				missing = append(missing, "--writers-upgraded")
			}
			if !writersDrained {
				missing = append(missing, "--writers-drained")
			}
			if strings.TrimSpace(actor) == "" {
				missing = append(missing, "--actor")
			}
			if strings.TrimSpace(reason) == "" {
				missing = append(missing, "--reason")
			}
			if expectedGeneration <= 0 {
				missing = append(missing, "--expected-generation")
			}
			if len(missing) != 0 {
				return fmt.Errorf("directory writer activation needs explicit operator assertions: %s (the database cannot inspect them; nothing was changed)",
					strings.Join(missing, ", "))
			}
			cfg, err := resolveDirectoryActivationStore(cmd, engineName, dataDir, dsn, ownerDSN, adminDSN)
			if err != nil {
				return err
			}
			registrar, err := bootSchemaRegistrar(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				return fmt.Errorf("prepare directory writer maintenance edition: %w", err)
			}
			result, err := coreengine.ActivateDirectoryWriterMaintenance(cmd.Context(), cfg, registrar,
				coreengine.DirectoryWriterActivationRequest{
					ExpectedGeneration: expectedGeneration, WritersUpgraded: writersUpgraded,
					WritersDrained: writersDrained, Actor: actor, Reason: reason,
				})
			rendered := directoryActivationResult{
				Engine: string(cfg.Engine), ExpectedGeneration: expectedGeneration,
				Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
				WritersUpgraded: writersUpgraded, WritersDrained: writersDrained,
				Before: directoryActivationStatusOf(result.Before), After: directoryActivationStatusOf(result.After),
				Changed: result.Changed, ReopenRequired: result.ReopenRequired,
			}
			if err != nil {
				rendered.Error = err.Error()
			}
			if renderErr := renderDirectoryActivationResult(cmd, rendered); renderErr != nil {
				return renderErr
			}
			if err != nil {
				if errors.Is(err, coreengine.ErrDirectoryWriterActivationIndeterminate) {
					return fmt.Errorf("directory writer activation is INDETERMINATE: reopen the store and re-run to verify which state survived: %w", err)
				}
				return err
			}
			return nil
		},
	}
	addStoreFlags(cmd, &dataDir, &engineName, &dsn)
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres only: owner-role DSN used at boot (accepts a file:/env: reference); leave empty for the single-role setup")
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "Postgres only: separate BYPASSRLS read-only admin DSN; without it, the attested closed directory inventory routine must be installed (accepts a file:/env: reference)")
	cmd.Flags().Int64Var(&expectedGeneration, "expected-generation", 0, "the predecessor generation (1 on a fresh estate); activates staged or enforced legacy to target generation+1, or verifies that exact completed retry")
	cmd.Flags().StringVar(&actor, "actor", "", "who performs the ceremony (recorded in the engine log); explicit, never taken from the environment")
	cmd.Flags().StringVar(&reason, "reason", "", "why the rollout is complete (recorded in the engine log)")
	cmd.Flags().BoolVar(&writersUpgraded, "writers-upgraded", false, "assert that EVERY writer process runs a binary carrying the directory writer wrapper")
	cmd.Flags().BoolVar(&writersDrained, "writers-drained", false, "assert that NO old writer transaction is in flight (every engine on this store is stopped)")
	return cmd
}

// bootSchemaRegistrar returns the registration closure boot passes to Open,
// over the same module set boot constructs (wire.go buildModules), plus the two
// engine-owned tables boot registers in every edition.
func bootSchemaRegistrar(log *slog.Logger) (func(store.ExtensionRegistry) error, error) {
	signer, err := audit.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		return nil, fmt.Errorf("directory writer activation: signer: %w", err)
	}
	set, err := buildModules(signer, ed25519.NewKeyFromSeed(fixedSeed(1)), ed25519.NewKeyFromSeed(fixedSeed(2)),
		nil, nil, sourcesConfig{}, EditionConfig{}, log)
	if err != nil {
		return nil, fmt.Errorf("directory writer activation: load module operator config: %w", err)
	}
	rt := runtime.New(runtime.Options{Logger: log})
	for _, m := range set.all {
		sm, ok := m.(sdk.Module)
		if !ok {
			return nil, fmt.Errorf("directory writer activation: module %q does not satisfy sdk.Module", m.APINamespace())
		}
		if err := rt.AddModule(sm, sdk.Config{}); err != nil {
			return nil, fmt.Errorf("directory writer activation: register module %q: %w", m.APINamespace(), err)
		}
	}
	return func(reg store.ExtensionRegistry) error {
		if err := rt.RegisterSchema(reg); err != nil {
			return err
		}
		if err := registerToolPinSchema(reg); err != nil {
			return err
		}
		return registerCircuitBreakerSchema(reg)
	}, nil
}

func resolveDirectoryActivationStore(
	cmd *cobra.Command, engineName, dataDir, dsn, ownerDSN, adminDSN string,
) (store.Config, error) {
	var cfg store.Config
	switch engineName {
	case string(store.EngineSQLite):
		cfg.Engine = store.EngineSQLite
		resolved := dsn
		if resolved == "" {
			dir := dataDir
			if dir == "" {
				d, err := defaultDataDir()
				if err != nil {
					return cfg, err
				}
				dir = d
			}
			resolved = filepath.Join(dir, "olivares.db")
		}
		if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
			return cfg, fmt.Errorf("no sqlite database at %q — the activation ceremony never creates a store; run the engine once (or `olivares quickstart`) first, or pass --dsn/--data-dir", resolved)
		}
		cfg.DSN = resolved
		if ownerDSN != "" || adminDSN != "" {
			return cfg, fmt.Errorf("--owner-dsn and --admin-dsn apply to --engine postgres only")
		}
	case string(store.EnginePostgres):
		cfg.Engine = store.EnginePostgres
		if dsn == "" {
			return cfg, fmt.Errorf("--dsn is required for --engine postgres (accepts a file:/env: reference)")
		}
		resolved, err := resolveDSNRef(cmd.Context(), "--dsn", dsn, osGetenv)
		if err != nil {
			return cfg, err
		}
		cfg.DSN = resolved
		if ownerDSN != "" {
			owner, err := resolveDSNRef(cmd.Context(), "--owner-dsn", ownerDSN, osGetenv)
			if err != nil {
				return cfg, err
			}
			cfg.OwnerDSN = owner
		}
		if adminDSN == "" {
			return cfg, fmt.Errorf("--admin-dsn is required for --engine postgres: the ceremony pins the boot-attested separate BYPASSRLS admin role as its witness")
		}
		admin, err := resolveDSNRef(cmd.Context(), "--admin-dsn", adminDSN, osGetenv)
		if err != nil {
			return cfg, err
		}
		cfg.AdminDSN = admin
	default:
		return cfg, fmt.Errorf("--engine %q must be sqlite or postgres", engineName)
	}
	return cfg, nil
}

func renderDirectoryActivationResult(cmd *cobra.Command, result directoryActivationResult) error {
	return renderOut(cmd, func(out io.Writer) error {
		status := func(s directoryActivationStatus) string {
			if s.ControlMode == "" {
				return "(not observed)"
			}
			return fmt.Sprintf("%s generation %d (%s, epoch coverage complete=%t)",
				s.ControlMode, s.ExpectedGeneration, s.WriterPosture, s.EpochCoverageComplete) +
				fmt.Sprintf(" protocol=%s H_coverage=%t inventory=%s orgs=%d business_orgs=%d epochs=%d", s.CoverageProtocol, s.UserAuthorityCoverageComplete, s.InventoryAuthority, s.InventoryOrgCount, s.InventoryBusinessOrgCount, s.InventoryEpochCount)
		}
		lines := []string{
			"directory writer activation",
			fmt.Sprintf("  engine:             %s", result.Engine),
			fmt.Sprintf("  expected generation: %d", result.ExpectedGeneration),
			fmt.Sprintf("  actor / reason:     %s / %s", result.Actor, result.Reason),
			fmt.Sprintf("  before:             %s", status(result.Before)),
			fmt.Sprintf("  after:              %s", status(result.After)),
			fmt.Sprintf("  changed:            %t", result.Changed),
			fmt.Sprintf("  reopen required:    %t", result.ReopenRequired),
		}
		if result.ReopenRequired {
			lines = append(lines, "  next: restart every engine on this store; DirectoryStatus is an immutable boot witness and K3 readiness observes the enforced control only after reopen")
		}
		if result.Error != "" {
			lines = append(lines, "  error:              "+result.Error)
		}
		_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))
		return err
	}, result)
}
