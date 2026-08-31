// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// The `olivares db` command closes the manual-Postgres-onboarding gap: today
// an operator must hand-run deploy/postgres/01-app-role.sql with a superuser DSN and
// only discovers a mis-provisioned role when the engine REFUSES to boot. `db check`
// reports the role posture read-only BEFORE the engine starts (the same
// ConnRolePosture guard, surfaced instead of fatal), and `db init` provisions the
// least-privilege role model idempotently from the binary — no psql, no SQL by hand.
func newDBCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "db",
		Short: "Prepare and verify the database before serving (Postgres roles, RLS posture)",
		Example: "  olivares db check --dsn env:DATABASE_URL --strict\n" +
			"  olivares db init --print-sql --app-password-file /run/secrets/app-pw",
		Long: "Onboard Postgres without editing SQL by hand: `db check` probes a DSN's role posture\n" +
			"and tells you whether the engine will accept it (the RLS-bypass boot guard, surfaced\n" +
			"early); `db init` provisions the least-privilege application / owner / admin roles and\n" +
			"the database idempotently from a superuser DSN.",
	}
	addTextJSONFormatFlag(root)
	root.AddCommand(dbCheckCmd(), dbInitCmd())
	return root
}

func dbCheckCmd() *cobra.Command {
	var engine, dsn, ownerDSN, adminDSN string
	var strict bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Probe a DSN's role posture and report whether the engine will accept it (read-only)",
		Long: "Connects with each DSN you pass and reports its privilege posture WITHOUT running any\n" +
			"migration or schema change. It predicts the boot guard exactly: the --dsn and --owner-dsn\n" +
			"roles must be NOSUPERUSER NOBYPASSRLS (else FORCE row-level security is inert and the\n" +
			"engine refuses to start); the --admin-dsn role must be BYPASSRLS but NOT a superuser.\n" +
			"SQLite has no roles, so it is always reported RLS-safe. With --strict the command exits\n" +
			"non-zero when any DSN would be refused — for a pre-flight CI/cron gate.",
		Example: `  # Check the application role before boot
  olivares db check --dsn env:DATABASE_URL --strict

  # Check all three least-privilege pools
  olivares db check --dsn env:DATABASE_URL --owner-dsn env:OWNER_DSN --admin-dsn env:ADMIN_DSN --strict`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng := store.EngineSQLite
			if engine == string(store.EnginePostgres) {
				eng = store.EnginePostgres
			} else if engine != string(store.EngineSQLite) {
				return fmt.Errorf("--engine %q must be sqlite or postgres", engine)
			}
			if dsn == "" && ownerDSN == "" && adminDSN == "" {
				return fmt.Errorf("nothing to check: pass --dsn (and optionally --owner-dsn / --admin-dsn)")
			}
			ok := true
			results := make([]dbCheckResult, 0, 3)
			for _, c := range []struct {
				label string
				value string
				admin bool
			}{
				{"--dsn", dsn, false},
				{"--owner-dsn", ownerDSN, false},
				{"--admin-dsn", adminDSN, true},
			} {
				if c.value == "" {
					continue
				}
				resolved, err := resolveDSNRef(cmd.Context(), c.label, c.value, osGetenv)
				if err != nil {
					return err
				}
				posture, err := coreengine.ProbeRole(cmd.Context(), store.Config{Engine: eng, DSN: resolved})
				if err != nil {
					return err
				}
				verdict, good := checkVerdict(posture, c.admin)
				ok = ok && good
				results = append(results, dbCheckResult{
					DSN:             c.label,
					Engine:          string(posture.Engine),
					Reachable:       posture.Reachable,
					Role:            posture.Role,
					Superuser:       posture.Superuser,
					BypassRLS:       posture.BypassRLS,
					ReplicationRole: posture.ReplicationRole,
					Verdict:         verdict,
					Accepted:        good,
				})
			}
			if err := renderOut(cmd, func(out io.Writer) error {
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "DSN\tREACHABLE\tROLE\tSUPERUSER\tBYPASSRLS\tVERDICT")
				for _, result := range results {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						result.DSN, yesNo(result.Reachable), orDash(result.Role),
						boolCell(result.Reachable, result.Superuser), boolCell(result.Reachable, result.BypassRLS), result.Verdict)
				}
				return tw.Flush()
			}, results); err != nil {
				return err
			}
			if strict && !ok {
				return fmt.Errorf("db check: at least one DSN would be refused at boot")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "postgres", "store engine the DSNs target: postgres or sqlite")
	_ = cmd.RegisterFlagCompletionFunc("engine", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"sqlite", "postgres"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&dsn, "dsn", "", "application-role DSN to probe (must be NOSUPERUSER NOBYPASSRLS). Accepts a file:/env: reference")
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "owner-role DSN to probe (must be NOSUPERUSER NOBYPASSRLS). Accepts a file:/env: reference")
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "cross-tenant admin-role DSN to probe (must be BYPASSRLS, NOT a superuser). Accepts a file:/env: reference")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero if any DSN would be refused at boot (pre-flight gate)")
	return cmd
}

type dbCheckResult struct {
	DSN       string `json:"dsn"`
	Engine    string `json:"engine"`
	Reachable bool   `json:"reachable"`
	Role      string `json:"role,omitempty"`
	Superuser bool   `json:"superuser"`
	BypassRLS bool   `json:"bypass_rls"`
	// ReplicationRole is reported so an operator can see WHY a verdict refused a
	// connection whose role attributes look fine.
	ReplicationRole string `json:"replication_role,omitempty"`
	Verdict         string `json:"verdict"`
	Accepted        bool   `json:"accepted"`
}

// checkVerdict renders the human verdict for a probed posture and whether it passes.
// An application/owner DSN must be RLS-safe; an admin DSN must be BYPASSRLS but not a
// superuser — mirroring the store's two boot guards exactly.
func checkVerdict(p store.RolePosture, admin bool) (string, bool) {
	// Reachability first, for every engine: a SQLite DSN that could not be opened is
	// not "RLS-safe by construction", it is unknown, and answering OK for it made
	// --strict pass on a database nobody could connect to.
	if !p.Reachable {
		return "UNREACHABLE — " + p.Err, false
	}
	if p.Engine == store.EngineSQLite {
		return "OK — sqlite has no roles; RLS-safe by construction", true
	}
	// Mirror the boot guards EXACTLY — being stricter than boot is as wrong as being
	// laxer, because this command is believed. Under session_replication_role='replica'
	// PostgreSQL skips every ORDINARY trigger, so the append-only and cutover guards go
	// inert while still listed in the catalog, and Open refuses the app and owner pools
	// for it. The ADMIN pool is deliberately exempt: it is read-only and cross-tenant,
	// no trigger-based guard applies to it, and openAdminPool does not check this.
	if !admin && p.TriggersDisabled() {
		return "REFUSED — session_replication_role=" + p.ReplicationRole +
			"; ordinary triggers would not fire, so the append-only and cutover guards are inert" +
			" (ALTER ROLE " + p.Role + " RESET session_replication_role, and check the database default)", false
	}
	if admin {
		switch {
		case p.Superuser:
			return "REFUSED — a SUPERUSER (more privilege than the admin pool needs; use NOSUPERUSER BYPASSRLS)", false
		case !p.BypassRLS:
			return "REFUSED — NOT BYPASSRLS; cross-tenant reads would return empty (grant BYPASSRLS)", false
		default:
			return "OK — BYPASSRLS, NOSUPERUSER (cross-tenant admin pool)", true
		}
	}
	if p.RLSUnsafe() {
		return "REFUSED — " + p.Why() + "; FORCE RLS would be inert (provision NOSUPERUSER NOBYPASSRLS, or pass --allow-privileged-db-role)", false
	}
	return "OK — NOSUPERUSER NOBYPASSRLS (RLS-safe)", true
}

// dbInitStep is one provisioning statement as `db init -o json` reports it.
//
// It is a CLI-side copy of store.PgProvisionStep on purpose: that type is an inert
// DTO in core with NO json tags, so marshaling it directly would publish Go field
// names (Label/SQL/Secret) as the wire contract of this CLI, and the wire contract
// of a CLI is not core's to set. Secret is carried because it is the only thing
// that explains the '********' in the SQL: the step's EXECUTED form binds a
// password, and what is printed here is the redacted display form.
type dbInitStep struct {
	Label  string `json:"label"`
	SQL    string `json:"sql"`
	Secret bool   `json:"secret"`
}

// dbInitVerification is one post-provision verification probe: db init reconnects
// as each role it provisioned and confirms the engine will accept it.
type dbInitVerification struct {
	// Pool is app | owner | admin — which of the three least-privilege pools this
	// row is about.
	Pool string `json:"pool"`
	// Verified is false for the one case the text form spells "(password kept; not
	// re-verified)": no password was supplied, the role kept its stored one, and
	// db init did not reconnect as it. Posture is then null. A caller that treats
	// a missing posture as a failure would be wrong, and a caller that treats it
	// as a pass would be wrong too — hence an explicit field for it.
	Verified bool `json:"verified"`
	// Posture is the SAME row shape `db check -o json` emits for the same fact, so
	// a script that already parses db check needs no second parser here. Its dsn
	// field names the FLAG this role is provisioned for (--dsn / --owner-dsn /
	// --admin-dsn), which is what db check puts there too.
	Posture *dbCheckResult `json:"posture"`
}

// dbInitResult is what `db init -o json` reports, for BOTH of the command's paths.
// One shape covers both because they answer the same question at different
// commitment levels, and Preview is which: true is --print-sql (nothing was
// connected to, nothing ran, Verification is empty and the DSN hints are unknown),
// false is a real provisioning run.
//
// Collapsing them into one type is deliberate. Two types would mean a caller has
// to sniff which document it got before reading it, and the failure mode of that
// is a script that silently treats a dry run as a completed provisioning.
type dbInitResult struct {
	Preview  bool         `json:"preview"`
	Database string       `json:"database"`
	Steps    []dbInitStep `json:"steps"`
	// Executed is what core reported about the steps, not what the CLI intended:
	// on the --print-sql path it is false because no statement ran.
	Executed     bool                 `json:"executed"`
	Verification []dbInitVerification `json:"verification"`
	// The DSN hints are PASSWORD-FREE by construction (core builds them that way):
	// they are the ready-to-use --dsn / --owner-dsn / --admin-dsn values, and the
	// password belongs in a 0600 file referenced as file:<path>. Empty means that
	// role was not provisioned.
	AppDSNHint   string `json:"app_dsn_hint"`
	OwnerDSNHint string `json:"owner_dsn_hint"`
	AdminDSNHint string `json:"admin_dsn_hint"`
}

// dbInitSteps converts core's steps to the CLI's wire shape.
func dbInitSteps(steps []store.PgProvisionStep) []dbInitStep {
	out := make([]dbInitStep, 0, len(steps))
	for _, s := range steps {
		out = append(out, dbInitStep{Label: s.Label, SQL: s.SQL, Secret: s.Secret})
	}
	return out
}

// newDBInitResult builds the JSON document for a REAL provisioning run.
//
// The verification rows follow the text form exactly, including which rows exist:
// the app pool is always reported (printPosture is called for it unconditionally,
// even when its posture is nil), and owner/admin appear only when they were
// provisioned. A document listing pools the text form does not mention would say
// this command did more than it did.
func newDBInitResult(spec store.PgProvisionSpec, res store.PgProvisionResult) dbInitResult {
	out := dbInitResult{
		Preview:      false,
		Database:     spec.Database,
		Steps:        dbInitSteps(res.Steps),
		Executed:     res.Executed,
		Verification: []dbInitVerification{dbInitPosture("app", "--dsn", res.AppPosture, false)},
		AppDSNHint:   res.AppDSNHint,
		OwnerDSNHint: res.OwnerDSNHint,
		AdminDSNHint: res.AdminDSNHint,
	}
	if res.OwnerPosture != nil {
		out.Verification = append(out.Verification, dbInitPosture("owner", "--owner-dsn", res.OwnerPosture, false))
	}
	if res.AdminPosture != nil {
		out.Verification = append(out.Verification, dbInitPosture("admin", "--admin-dsn", res.AdminPosture, true))
	}
	return out
}

// dbInitPosture renders one verification row. The verdict comes from the SAME
// checkVerdict the text form and `db check` use — a second copy of the boot-guard
// wording is how the two would come to disagree about whether a role is accepted.
func dbInitPosture(pool, flag string, p *store.RolePosture, admin bool) dbInitVerification {
	if p == nil {
		return dbInitVerification{Pool: pool, Verified: false}
	}
	verdict, accepted := checkVerdict(*p, admin)
	return dbInitVerification{Pool: pool, Verified: true, Posture: &dbCheckResult{
		DSN:             flag,
		Engine:          string(p.Engine),
		Reachable:       p.Reachable,
		Role:            p.Role,
		Superuser:       p.Superuser,
		BypassRLS:       p.BypassRLS,
		ReplicationRole: p.ReplicationRole,
		Verdict:         verdict,
		Accepted:        accepted,
	}}
}

func dbInitCmd() *cobra.Command {
	var (
		superuserDSN                          string
		database, sslmode                     string
		appRole, appPassword, appPasswordFile string
		ownerRole, ownerPassword, ownerPwFile string
		adminRole, adminPassword, adminPwFile string
		printSQL                              bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Provision the least-privilege Postgres roles + database idempotently (no psql by hand)",
		Long: "Connects with --superuser-dsn (a superuser / maintenance DSN) and provisions, idempotently:\n" +
			"  • the application role (--app-role, NOSUPERUSER NOBYPASSRLS) for runtime traffic;\n" +
			"  • optionally a SEPARATE owner role (--owner-role) that owns the schema and runs DDL, so\n" +
			"    the app role becomes a non-owner with only DML grants (ALTER DEFAULT PRIVILEGES) — the\n" +
			"    least-privilege split that makes --owner-dsn worthwhile;\n" +
			"  • optionally the cross-tenant admin role (--admin-role, NOSUPERUSER BYPASSRLS) for --admin-dsn;\n" +
			"  • the application database, owned by the owner role.\n\n" +
			"It then reconnects as each provisioned role to verify the engine will accept it. Use\n" +
			"--print-sql to preview the exact statements (passwords redacted) without connecting.",
		Example: `  # Preview the SQL without connecting
  olivares db init --print-sql --app-password-file /run/secrets/app-pw

  # Provision with the least-privilege owner/app split
  olivares db init --superuser-dsn "postgres://postgres@db:5432/postgres" \
    --app-role olivares_app --app-password-file /run/secrets/app-pw \
    --owner-role olivares_owner --owner-password-file /run/secrets/owner-pw`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			appPw, err := readSecretValue(cmd, appPassword, appPasswordFile)
			if err != nil {
				return fmt.Errorf("--app-password: %w", err)
			}
			ownerPw, err := readSecretValue(cmd, ownerPassword, ownerPwFile)
			if err != nil {
				return fmt.Errorf("--owner-password: %w", err)
			}
			adminPw, err := readSecretValue(cmd, adminPassword, adminPwFile)
			if err != nil {
				return fmt.Errorf("--admin-password: %w", err)
			}
			spec := store.PgProvisionSpec{
				Database: database,
				SSLMode:  sslmode,
				App:      store.PgRole{Name: appRole, Password: appPw},
			}
			if ownerRole != "" && ownerRole != appRole {
				spec.Owner = store.PgRole{Name: ownerRole, Password: ownerPw}
			}
			if adminRole != "" {
				spec.Admin = &store.PgRole{Name: adminRole, Password: adminPw}
			}

			if printSQL {
				steps, err := coreengine.RenderProvisionSQL(spec)
				if err != nil {
					return err
				}
				return renderDBInitPreview(cmd, spec, steps)
			}

			if superuserDSN == "" {
				return fmt.Errorf("--superuser-dsn is required (a superuser / maintenance DSN, e.g. postgres://postgres@host:5432/postgres); or use --print-sql to preview without connecting")
			}
			resolved, err := resolveDSNRef(cmd.Context(), "--superuser-dsn", superuserDSN, osGetenv)
			if err != nil {
				return err
			}
			res, err := coreengine.ProvisionPostgres(cmd.Context(), resolved, spec, true)
			if err != nil {
				return err
			}
			return renderDBInitResult(cmd, spec, res)
		},
	}
	cmd.Flags().StringVar(&superuserDSN, "superuser-dsn", "", "superuser / maintenance DSN used ONLY to provision (e.g. postgres://postgres@host:5432/postgres). Accepts a file:/env: reference")
	cmd.Flags().StringVar(&database, "database", "olivares", "application database name to create/own")
	cmd.Flags().StringVar(&sslmode, "sslmode", "verify-full", "libpq sslmode for the printed DSN hints")
	_ = cmd.RegisterFlagCompletionFunc("sslmode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&appRole, "app-role", "olivares_app", "application role (runtime traffic; NOSUPERUSER NOBYPASSRLS)")
	cmd.Flags().StringVar(&appPassword, "app-password", "", "application role password (prefer --app-password-file)")
	cmd.Flags().StringVar(&appPasswordFile, "app-password-file", "", "read the application role password from a file, or - for stdin")
	cmd.Flags().StringVar(&ownerRole, "owner-role", "", "SEPARATE owner role that owns the schema and runs DDL (enables the least-privilege split). Empty = the app role owns the schema. Use on a FRESH database; adopting the split on an existing single-role db needs a manual REASSIGN OWNED first (see deploy/postgres/README.md)")
	cmd.Flags().StringVar(&ownerPassword, "owner-password", "", "owner role password (prefer --owner-password-file)")
	cmd.Flags().StringVar(&ownerPwFile, "owner-password-file", "", "read the owner role password from a file, or - for stdin")
	cmd.Flags().StringVar(&adminRole, "admin-role", "", "cross-tenant admin role for --admin-dsn (NOSUPERUSER BYPASSRLS). Empty = not provisioned")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "", "admin role password (prefer --admin-password-file)")
	cmd.Flags().StringVar(&adminPwFile, "admin-password-file", "", "read the admin role password from a file, or - for stdin")
	cmd.Flags().BoolVar(&printSQL, "print-sql", false, "print the provisioning SQL (passwords redacted) and exit, without connecting")
	return cmd
}

// renderDBInitPreview and renderDBInitResult are the two render switches of
// `db init`, named so they can be exercised directly.
//
// renderDBInitResult exists as a function for a reason worth stating: its input is
// a store.PgProvisionResult that only a REAL Postgres superuser connection can
// produce, so the only way to prove its two forms agree — and that the text one is
// byte-identical to what this command printed before -o json existed — is to hand
// it a fabricated result. Left inline in RunE it would have been unreachable
// without a database, which is how a renderer ends up with no witness at all.
// Called from exactly one place each (dbInitCmd's RunE, above).
func renderDBInitPreview(cmd *cobra.Command, spec store.PgProvisionSpec, steps []store.PgProvisionStep) error {
	// printSteps stays the text formatter, untouched: the text form is then
	// byte-identical BY CONSTRUCTION, not by a second rendering that happens to
	// agree with it today.
	return renderOut(cmd, func(out io.Writer) error {
		printSteps(out, steps)
		return nil
	}, dbInitResult{
		Preview:      true,
		Database:     spec.Database,
		Steps:        dbInitSteps(steps),
		Executed:     false,
		Verification: []dbInitVerification{},
	})
}

func renderDBInitResult(cmd *cobra.Command, spec store.PgProvisionSpec, res store.PgProvisionResult) error {
	return renderOut(cmd, func(out io.Writer) error {
		printInitResult(out, spec, res)
		return nil
	}, newDBInitResult(spec, res))
}

func printSteps(out io.Writer, steps []store.PgProvisionStep) {
	fmt.Fprintln(out, "-- olivares db init (preview; passwords are redacted, statements are applied idempotently)")
	for _, s := range steps {
		fmt.Fprintf(out, "\n-- %s\n%s\n", s.Label, s.SQL)
	}
}

func printInitResult(out io.Writer, spec store.PgProvisionSpec, res store.PgProvisionResult) {
	fmt.Fprintf(out, "provisioned database %q with %d step(s):\n", spec.Database, len(res.Steps))
	for _, s := range res.Steps {
		fmt.Fprintf(out, "  • %s\n", s.Label)
	}
	fmt.Fprintln(out, "\nverification (reconnected as each provisioned role):")
	printPosture(out, "  app  ", res.AppPosture, false)
	if res.OwnerPosture != nil {
		printPosture(out, "  owner", res.OwnerPosture, false)
	}
	if res.AdminPosture != nil {
		printPosture(out, "  admin", res.AdminPosture, true)
	}
	fmt.Fprintln(out, "\nNext: store each password in a 0600 file and point serve at it (the password stays out of the env file):")
	fmt.Fprintf(out, "  --dsn=file:/etc/olivares/secrets/app.dsn        # %s\n", res.AppDSNHint)
	if res.OwnerDSNHint != "" {
		fmt.Fprintf(out, "  --owner-dsn=file:/etc/olivares/secrets/owner.dsn  # %s\n", res.OwnerDSNHint)
	}
	if res.AdminDSNHint != "" {
		fmt.Fprintf(out, "  --admin-dsn=file:/etc/olivares/secrets/admin.dsn  # %s\n", res.AdminDSNHint)
	}
	fmt.Fprintln(out, "`olivares setup` writes these files and the env file for you.")
}

func printPosture(out io.Writer, label string, p *store.RolePosture, admin bool) {
	if p == nil {
		fmt.Fprintf(out, "%s: (password kept; not re-verified)\n", label)
		return
	}
	verdict, _ := checkVerdict(*p, admin)
	fmt.Fprintf(out, "%s: %s — %s\n", label, orDash(p.Role), verdict)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// boolCell renders a posture boolean only when the probe was reachable (otherwise the
// value is meaningless).
func boolCell(reachable, v bool) string {
	if !reachable {
		return "-"
	}
	return yesNo(v)
}
