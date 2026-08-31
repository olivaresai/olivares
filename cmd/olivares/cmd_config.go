// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// `olivares config generate` is the non-interactive, scriptable twin of
// `olivares setup`: it builds the SAME validated installPlan from flags and writes
// the structured env file (or a k8s args/env snippet). It is the expert/CI path —
// every knob is a flag, the output is deterministic, and secrets are passed by
// reference (--dsn=file:<path>), never typed.
func newConfigCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "config",
		Short: "Generate validated engine configuration (the non-interactive setup)",
		Long: "config is the non-interactive half of first-run setup: compose a validated\n" +
			"olivares.env (or a Kubernetes snippet) from flags, check that the OLIVARES_*\n" +
			"keys already in the environment are ones the engine accepts, and print what\n" +
			"the engine would actually read, with every secret redacted.\n\n" +
			"Use `olivares setup` instead when a guided, interactive run is wanted.",
		Example: "  olivares config generate --profile eval\n" +
			"  olivares config validate\n" +
			"  olivares config effective",
	}
	root.AddCommand(configGenerateCmd(), configEffectiveCmd(), configValidateCmd())
	return root
}

func configEffectiveCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "effective",
		Short: "Print configured OLIVARES_* values with secrets redacted",
		Long: "Print the configured production OLIVARES_* environment values after applying the\n" +
			"runtime environment/activation-overlay precedence. Secret-bearing values are always\n" +
			"shown as <redacted>. Use --strict as the CI/pre-production unknown-key gate.",
		Example: `  olivares config effective
  olivares config effective -o json
  OLIVARES_CONFIG_STRICT=1 olivares config effective`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			environ := os.Environ()
			unknown := unknownConfigEnvKeys(environ)
			effective := effectiveConfigEnv(environ, osGetenv)
			if err := writeEffectiveConfig(cmd, effective); err != nil {
				return err
			}
			if (strict || osGetenv(envConfigStrict) == "1") && len(unknown) > 0 {
				return unrecognizedConfigEnvError(unknown)
			}
			return nil
		},
	}
	addDeprecatedFormatFlag(cmd, false)
	cmd.Flags().BoolVar(&strict, "strict", false, "fail if any unrecognized OLIVARES_* environment key is present")
	return cmd
}

// configValidateOKLine is the ONLY thing `config validate` prints, and it is a
// constant because it is a contract: the command carries its verdict in the exit
// code, so this sentence is what an operator reads and what a test may pin
// byte-for-byte.
const configValidateOKLine = "configuration valid: all OLIVARES_* environment keys are recognized"

// configValidateResult is what `config validate -o json` reports.
//
// THIS LEAF IS MARGINAL AND THAT IS WORTH SAYING OUT LOUD. It prints one constant
// sentence with no data in it: the verdict already travels in the exit code, and a
// script that wants the answer should test the exit code, not parse a document.
// There is no unrecognized-key list to report either — a non-empty list is the
// FAILURE path, which returns an error and prints nothing on stdout, so a
// `keys: []` field here could never be anything but empty and would invite a
// caller to believe it means "checked and clean" on a run that never got here.
//
// It gets -o json for UNIFORMITY, not for value: an operator scripting the
// bootstrap sequence should not have to remember that eight of the nine commands
// in it answer -o json and this one silently ignores it. Silently is the operative
// word — that was the measured defect class VER-06 exists to close.
type configValidateResult struct {
	Valid bool `json:"valid"`
}

func configValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configured OLIVARES_* environment keys",
		Long: "Validate every configured OLIVARES_* environment key against the production config\n" +
			"contract. Unknown keys are listed together and make the command exit non-zero.",
		Example: "  olivares config validate",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if unknown := unknownConfigEnvKeys(os.Environ()); len(unknown) > 0 {
				return unrecognizedConfigEnvError(unknown)
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintln(out, configValidateOKLine)
				return werr
			}, configValidateResult{Valid: true})
		},
	}
}

func writeEffectiveConfig(cmd *cobra.Command, effective map[string]string) error {
	return renderOut(cmd, func(out io.Writer) error {
		keys := make([]string, 0, len(effective))
		for key := range effective {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, err := fmt.Fprintf(out, "%s=%s\n", key, effective[key]); err != nil {
				return err
			}
		}
		return nil
	}, effective)
}

func unrecognizedConfigEnvError(keys []string) error {
	return fmt.Errorf("unrecognized OLIVARES_* environment keys: %v", keys)
}

func configGenerateCmd() *cobra.Command {
	var (
		profile, out                    string
		listen, grpcListen, dataDir     string
		engine, dsn, ownerDSN, adminDSN string
		tlsCert, tlsKey, grpcClientCA   string
		auditSigningKeyFile             string
		license, region                 string
		knownRegions                    []string
		insecure, allowPrivilegedDB     bool
		checkpointInterval              string
		maxConns                        int
		force                           bool
	)
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Compose a validated /etc/olivares/olivares.env (or k8s snippet) from flags",
		Long: "Builds and validates the engine configuration for a profile (eval, single-node-prod,\n" +
			"postgres-prod, k8s) and writes it to --out (default stdout). Every value is validated;\n" +
			"pass secrets by reference (--dsn=file:/etc/olivares/secrets/db.dsn) so nothing sensitive\n" +
			"lands in the file. Provision the Postgres roles first with `olivares db init`.",
		Example: `  # Generate an eval config to stdout
  olivares config generate --profile eval

  # Generate a production Postgres config and write to the systemd env file
  olivares config generate --profile postgres-prod \
    --dsn file:/etc/olivares/secrets/app.dsn \
    --owner-dsn file:/etc/olivares/secrets/owner.dsn \
    --admin-dsn file:/etc/olivares/secrets/admin.dsn \
    --tls-cert /etc/olivares/tls.crt --tls-key /etc/olivares/tls.key \
    --grpc-client-ca /etc/olivares/collector-ca.crt \
    --audit-signing-key-file /etc/olivares/audit-signing.key \
    --out /etc/olivares/olivares.env

  # Generate a Kubernetes-oriented config
  olivares config generate --profile k8s --engine postgres --dsn "env:DATABASE_URL"`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Seed the same profile defaults as the interactive wizard. Only explicit
			// engine/max-conns flags override profile-specific defaults, so selecting
			// postgres-prod cannot silently fall back to SQLite or lose its pool cap.
			plan := newPlanForProfile(profile)
			plan.Listen = listen
			plan.GRPCListen = grpcListen
			plan.DataDir = dataDir
			if cmd.Flags().Changed("engine") {
				plan.Engine = engine
			}
			plan.DSNArg = dsn
			plan.OwnerDSNArg = ownerDSN
			plan.AdminDSNArg = adminDSN
			plan.TLSCert = tlsCert
			plan.TLSKey = tlsKey
			plan.GRPCClientCA = grpcClientCA
			plan.AuditSigningKeyFile = auditSigningKeyFile
			plan.License = license
			plan.Region = region
			plan.KnownRegions = splitCSV(knownRegions)
			plan.Insecure = insecure
			plan.AllowPrivilegedDB = allowPrivilegedDB
			plan.CheckpointInterval = checkpointInterval
			if cmd.Flags().Changed("max-conns") {
				plan.MaxConns = maxConns
			}
			if err := plan.validate(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
			}
			content := plan.render()
			if out == "" || out == "-" {
				_, err := fmt.Fprint(cmd.OutOrStdout(), content)
				return err
			}
			if err := writeFileGuarded(out, []byte(content), 0o640, force); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (profile %s). Restart the engine to apply: systemctl restart olivares\n", out, plan.Profile)
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", profileSingleNode, "install profile: eval | single-node-prod | postgres-prod | k8s")
	_ = cmd.RegisterFlagCompletionFunc("profile", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"eval", "single-node-prod", "postgres-prod", "k8s"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&out, "out", "-", "output path (default - = stdout); for systemd use "+defaultEnvFilePath)
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8443", "HTTP (REST + console) listen address")
	cmd.Flags().StringVar(&grpcListen, "grpc-listen", "127.0.0.1:8444", "gRPC listen address")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory override (default the unit's "+defaultUnitDataDir+")")
	cmd.Flags().StringVar(&engine, "engine", "", "store engine override: sqlite or postgres (profile default: postgres for postgres-prod, sqlite otherwise)")
	_ = cmd.RegisterFlagCompletionFunc("engine", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"sqlite", "postgres"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&dsn, "dsn", "", "store DSN or a file:/env: reference (required for postgres)")
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "owner-role DSN or reference (enables the least-privilege owner/app split)")
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "cross-tenant admin-role DSN or reference")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate PEM path (with --tls-key)")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS private key PEM path (with --tls-cert)")
	cmd.Flags().StringVar(&grpcClientCA, "grpc-client-ca", "", "PEM bundle of CAs for collector mTLS")
	cmd.Flags().StringVar(&auditSigningKeyFile, "audit-signing-key-file", "", "operator-provisioned Ed25519 audit signing key file (required external BYOK custody for postgres-prod)")
	cmd.Flags().StringVar(&license, "license", "", "path to a commercial license file")
	cmd.Flags().StringVar(&region, "region", "", "data-residency home region of this instance (e.g. eu)")
	cmd.Flags().StringSliceVar(&knownRegions, "known-regions", nil, "deployment-wide region codes (comma-separated; home region added implicitly)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "serve plaintext (loopback dev only)")
	cmd.Flags().BoolVar(&allowPrivilegedDB, "allow-privileged-db-role", false, "permit a superuser/BYPASSRLS Postgres role (DANGEROUS; disables the RLS backstop)")
	cmd.Flags().StringVar(&checkpointInterval, "checkpoint-interval", "", "audit checkpoint cadence override (e.g. 30m; default 1h)")
	cmd.Flags().IntVar(&maxConns, "max-conns", 0, "OLIVARES_DB_MAX_CONNS — Postgres app-pool cap per node (0 = engine default)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite --out if it already exists")
	return cmd
}

// splitCSV flattens cobra StringSlice entries that themselves contain commas, so
// both --known-regions eu,us and --known-regions eu --known-regions us work.
func splitCSV(in []string) []string {
	var out []string
	for _, v := range in {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// writeFileGuarded writes content to path with mode, refusing to clobber an existing
// file unless force is set. On a force-overwrite it first copies the existing file
// to path+".bak" (so a regeneration never loses the previous configuration). Parent
// directories must already exist (the package postinstall creates /etc/olivares).
func writeFileGuarded(path string, content []byte, mode os.FileMode, force bool) error {
	if _, statErr := os.Stat(path); statErr == nil {
		if !force {
			return fmt.Errorf("%s already exists — pass --force to overwrite (the previous file is backed up to %s.bak)", path, path)
		}
		// Back up the previous file at the SAME tight mode (never the old, possibly
		// looser, perms — the backup of a secret must not be world-readable).
		if old, rerr := os.ReadFile(path); rerr == nil { //nolint:gosec // operator-owned config path
			if werr := os.WriteFile(path+".bak", old, mode); werr == nil {
				_ = os.Chmod(path+".bak", mode)
			}
		}
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// os.WriteFile only applies mode when CREATING the file; on an overwrite of a
	// pre-existing (possibly loosened) file the bits are unchanged, so force the
	// intended mode explicitly — a regenerated 0600 secret must never stay loose.
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}
