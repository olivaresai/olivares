// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// `olivares setup` is the guided, EXPERT first-run configurator. It picks an install
// profile, asks ONLY what that profile needs, validates every answer, and writes a
// structured /etc/olivares/olivares.env plus 0600 secret files — so a Postgres-prod
// install no longer means hand-editing OLIVARES_EXTRA_ARGS and running SQL with a
// superuser DSN by hand. It is `config generate` with a conversation in front and
// secret externalization built in; the underlying install plumbing is untouched.
func newSetupCmd() *cobra.Command {
	var (
		out, secretsDir string
		force           bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Guided, validated first-run configuration (profiles, Postgres onboarding, no SQL by hand)",
		Long: "Interactively choose a profile (eval, single-node-prod, postgres-prod, k8s), answer a\n" +
			"few validated questions, and have setup write a structured env file and 0600 secret files\n" +
			"— and, for Postgres, optionally provision the least-privilege roles (`db init`) and verify\n" +
			"them (`db check`) before you ever start the engine. The scriptable twin is\n" +
			"`olivares config generate`.",
		Example:      "  olivares setup --out /etc/olivares/olivares.env --secrets-dir /etc/olivares/secrets",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := &prompter{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout()}
			plan, err := buildPlanInteractive(cmd, p, secretsDir)
			if err != nil {
				return err
			}
			if p.err != nil {
				return p.err
			}
			if err := plan.validate(); err != nil {
				return fmt.Errorf("the answers do not form a valid configuration: %w", err)
			}
			return writePlan(cmd.OutOrStdout(), plan, out, force)
		},
	}
	cmd.Flags().StringVar(&out, "out", defaultEnvFilePath, "env file to write")
	cmd.Flags().StringVar(&secretsDir, "secrets-dir", defaultSecretsDir, "directory for 0600 secret files (DSNs)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the env file / secret files if they exist")
	return cmd
}

// prompter is a dependency-free stdin/stdout question helper. On EOF (a closed /
// non-interactive stdin) ask returns the default and records the first missing
// required answer in err, so the flow ends with a clear message instead of looping.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	eof bool
	err error
}

func (p *prompter) printf(format string, a ...any) { fmt.Fprintf(p.out, format, a...) }

func (p *prompter) readLine() (string, bool) {
	if p.eof {
		return "", false
	}
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		p.eof = true
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// ask shows "label [def]: " and returns the trimmed answer, or def when the answer
// is empty or stdin is exhausted.
func (p *prompter) ask(label, def string) string {
	if def != "" {
		p.printf("%s [%s]: ", label, def)
	} else {
		p.printf("%s: ", label)
	}
	line, ok := p.readLine()
	if !ok {
		p.printf("%s\n", def)
		return def
	}
	if v := strings.TrimSpace(line); v != "" {
		return v
	}
	return def
}

// askRequired re-prompts until a non-empty answer, or records an error on EOF.
func (p *prompter) askRequired(label string) string {
	for {
		if p.eof {
			if p.err == nil {
				p.err = fmt.Errorf("setup: %q is required but no input was provided (run setup on a terminal, or use `olivares config generate`)", label)
			}
			return ""
		}
		if v := p.ask(label, ""); v != "" {
			return v
		}
		p.printf("  (required)\n")
	}
}

// askSecret reads a value that will be sealed into a 0600 file. The input is
// line-echoed (no TTY raw-mode dependency); the note keeps that honest.
func (p *prompter) askSecret(label string) string {
	p.printf("  (the value is visible as you type; it is written to a 0600 file, not the env file)\n")
	return p.askRequired(label)
}

func (p *prompter) askBool(label string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	p.printf("%s [%s]: ", label, d)
	line, ok := p.readLine()
	if !ok {
		p.printf("%v\n", def)
		return def
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

func (p *prompter) askChoice(label string, opts []string, def int) string {
	p.printf("%s:\n", label)
	for i, o := range opts {
		marker := " "
		if i == def {
			marker = "*"
		}
		p.printf("  %s %d) %s\n", marker, i+1, o)
	}
	line, ok := p.readLine()
	if !ok {
		p.printf("%s\n", opts[def])
		return opts[def]
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return opts[def]
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(opts) {
		return opts[n-1]
	}
	// Accept the profile name typed verbatim too.
	if contains(opts, s) {
		return s
	}
	p.printf("  (unrecognized; using %s)\n", opts[def])
	return opts[def]
}

// newPlanForProfile seeds the profile's safe defaults; the wizard overlays answers.
func newPlanForProfile(profile string) installPlan {
	p := installPlan{
		Profile:    profile,
		Listen:     "127.0.0.1:8443",
		GRPCListen: "127.0.0.1:8444",
		Engine:     string(engineSQLite),
	}
	if profile == profilePostgresPro {
		p.Engine = string(enginePostgres)
		p.MaxConns = defaultPostgresMaxConns
	}
	return p
}

// buildPlanInteractive runs the profile-specific question flow and returns the plan.
func buildPlanInteractive(cmd *cobra.Command, p *prompter, secretsDir string) (installPlan, error) {
	p.printf("\n=== OLIVARES AI — GUIDED SETUP ===\n")
	p.printf("Answer a few questions; setup writes a validated env file (no hand-edited flag strings)\n")
	p.printf("and 0600 secret files. Press Enter to accept the [default].\n\n")

	profile := p.askChoice("Install profile", allProfiles, 1) // default single-node-prod
	plan := newPlanForProfile(profile)

	switch profile {
	case profileEval:
		// The quickstart shape, but written to a unit: loopback SQLite, self-signed.
		plan.DataDir = p.ask("Data directory", defaultUnitDataDir)
	case profileSingleNode:
		askListeners(p, &plan, false)
		askTLS(p, &plan)
		askResidency(p, &plan)
	case profilePostgresPro:
		if err := askPostgres(cmd, p, &plan, secretsDir); err != nil {
			return plan, err
		}
		askListeners(p, &plan, false)
		askTLS(p, &plan)
		plan.AuditSigningKeyFile = p.askRequired("External audit signing key file (operator-provisioned Ed25519 key)")
		askResidency(p, &plan)
	case profileK8s:
		// Container-oriented: engine choice, a DSN the operator will mount as a
		// Secret (referenced as file:<mountPath>), residency. No on-disk secret files.
		if p.askBool("Use Postgres (vs the embedded SQLite)?", true) {
			plan.Engine = string(enginePostgres)
			plan.DSNArg = p.askRequired("Mounted app DSN reference (e.g. file:/secrets/db.dsn)")
			if owner := p.ask("Mounted owner DSN reference (blank = single-role)", ""); owner != "" {
				plan.OwnerDSNArg = owner
			}
			if admin := p.ask("Mounted admin DSN reference (blank = none)", ""); admin != "" {
				plan.AdminDSNArg = admin
			}
			plan.MaxConns = defaultPostgresMaxConns
		}
		// In a Pod the engine binds inside the container; default to Go's
		// dual-stack wildcard fronted by the Service/Ingress.
		plan.Listen = p.ask("Container HTTP bind", ":8443")
		plan.GRPCListen = p.ask("Container gRPC bind", ":8444")
		askResidency(p, &plan)
	}
	return plan, nil
}

func askListeners(p *prompter, plan *installPlan, _ bool) {
	plan.DataDir = p.ask("Data directory", defaultUnitDataDir)
	plan.Listen = p.ask("HTTP listen (the console + REST). Widen only behind a TLS-terminating proxy", plan.Listen)
	plan.GRPCListen = p.ask("gRPC listen (collectors)", plan.GRPCListen)
	if !hostIsLoopback(plan.Listen) {
		p.printf("  note: a non-loopback bind must sit behind your reverse proxy / ingress (docs/08).\n")
	}
}

func askTLS(p *prompter, plan *installPlan) {
	if plan.Profile == profilePostgresPro {
		p.printf("  postgres-prod requires operator TLS and collector mTLS; self-signed/unauthenticated ingest is not a production posture.\n")
		plan.TLSCert = p.askRequired("TLS certificate PEM path")
		plan.TLSKey = p.askRequired("TLS private key PEM path")
		plan.GRPCClientCA = p.askRequired("Collector client-CA PEM bundle path")
		return
	}
	if p.askBool("Bring your own TLS certificate (else a self-signed cert is generated on first boot)?", false) {
		plan.TLSCert = p.askRequired("TLS certificate PEM path")
		plan.TLSKey = p.askRequired("TLS private key PEM path")
	}
	if p.askBool("Require collector mutual TLS (a client-cert CA for gRPC ingest)?", false) {
		plan.GRPCClientCA = p.askRequired("Collector client-CA PEM bundle path")
	}
}

func askResidency(p *prompter, plan *installPlan) {
	if !p.askBool("Enforce data-residency (region-scope this instance)?", false) {
		return
	}
	plan.Region = p.askRequired("Home region code (e.g. eu, us)")
	if more := p.ask("Other deployment-wide region codes (comma-separated, blank = none)", ""); more != "" {
		plan.KnownRegions = splitCSV([]string{more})
	}
}

// askPostgres collects the Postgres connection, builds password-bearing DSNs,
// externalizes them to 0600 files (referenced as file:<path>), and optionally runs
// `db init` to provision the roles with the SAME credentials right away.
func askPostgres(cmd *cobra.Command, p *prompter, plan *installPlan, secretsDir string) error {
	host := p.ask("Postgres host", "localhost")
	port := p.ask("Postgres port", "5432")
	dbName := p.ask("Database name", "olivares")
	sslmode := p.ask("TLS sslmode", "verify-full")

	appRole := p.ask("Application role (runtime traffic; NOSUPERUSER NOBYPASSRLS)", "olivares_app")
	appPw := p.askSecret("Application role password")

	split := plan.Profile == profilePostgresPro
	if split {
		p.printf("  postgres-prod requires a separate NOSUPERUSER NOBYPASSRLS owner role for DDL.\n")
	} else {
		split = p.askBool("Provision a SEPARATE least-privilege owner role for DDL (recommended for prod)?", true)
	}
	var ownerRole, ownerPw string
	if split {
		ownerRole = p.ask("Owner role", "olivares_owner")
		ownerPw = p.askSecret("Owner role password")
	}
	wantAdmin := plan.Profile == profilePostgresPro
	if wantAdmin {
		p.printf("  postgres-prod requires a dedicated NOSUPERUSER BYPASSRLS admin role for complete cross-tenant operations.\n")
	} else {
		wantAdmin = p.askBool("Provision a cross-tenant admin role (full org-list / checkpoint coverage)?", false)
	}
	var adminRole, adminPw string
	if wantAdmin {
		adminRole = p.ask("Admin role (NOSUPERUSER BYPASSRLS)", "olivares_admin")
		adminPw = p.askSecret("Admin role password")
	}
	if p.err != nil {
		return p.err
	}

	mkDSN := func(role, pw string) string {
		u := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(role, pw),
			Host:     host + ":" + port,
			Path:     "/" + dbName,
			RawQuery: "sslmode=" + url.QueryEscape(sslmode),
		}
		return u.String()
	}
	addSecretRef := func(file, role, pw string) string {
		path := filepath.Join(secretsDir, file)
		plan.Secrets = append(plan.Secrets, envSecretFile{Path: path, Content: mkDSN(role, pw)})
		return "file:" + path
	}

	plan.Engine = string(enginePostgres)
	plan.DSNArg = addSecretRef("app.dsn", appRole, appPw)
	if split {
		plan.OwnerDSNArg = addSecretRef("owner.dsn", ownerRole, ownerPw)
	}
	if wantAdmin {
		plan.AdminDSNArg = addSecretRef("admin.dsn", adminRole, adminPw)
	}

	// Optionally provision the roles right now with a superuser DSN — the guided
	// alternative to running deploy/postgres/01-app-role.sql by hand. The passwords
	// are reused, so the provisioned roles match the DSNs setup just wrote.
	if p.askBool("Provision these roles now with a superuser DSN (runs `db init`)?", false) {
		superDSN := p.askRequired("Superuser / maintenance DSN (e.g. postgres://postgres@" + host + ":" + port + "/postgres)")
		if p.err != nil {
			return p.err
		}
		spec := store.PgProvisionSpec{
			Database: dbName,
			SSLMode:  sslmode,
			App:      store.PgRole{Name: appRole, Password: appPw},
		}
		if split {
			spec.Owner = store.PgRole{Name: ownerRole, Password: ownerPw}
		}
		if wantAdmin {
			spec.Admin = &store.PgRole{Name: adminRole, Password: adminPw}
		}
		res, err := coreengine.ProvisionPostgres(cmd.Context(), superDSN, spec, true)
		if err != nil {
			return fmt.Errorf("db init during setup: %w", err)
		}
		p.printf("  provisioned database %q (%d step(s)); roles verified.\n", dbName, len(res.Steps))
		warnPosture(p, "app", res.AppPosture, false)
		warnPosture(p, "owner", res.OwnerPosture, false)
		warnPosture(p, "admin", res.AdminPosture, true)
	} else {
		p.printf("  skipping provisioning — run it later: olivares db init --superuser-dsn … --app-role %s%s\n",
			appRole, splitInitHint(split, ownerRole, wantAdmin, adminRole))
	}
	return nil
}

func warnPosture(p *prompter, label string, posture *store.RolePosture, admin bool) {
	if posture == nil {
		return
	}
	if _, ok := checkVerdict(*posture, admin); !ok {
		p.printf("  WARNING: %s role posture: %s\n", label, posture.Why())
	}
}

func splitInitHint(split bool, ownerRole string, admin bool, adminRole string) string {
	var b strings.Builder
	if split {
		fmt.Fprintf(&b, " --owner-role %s", ownerRole)
	}
	if admin {
		fmt.Fprintf(&b, " --admin-role %s", adminRole)
	}
	return b.String()
}

// writePlan writes the secret files (0600) and the env file, then prints next steps.
func writePlan(out io.Writer, plan installPlan, envPath string, force bool) error {
	// Secret files first, in their own 0700 directory.
	for _, s := range plan.Secrets {
		if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
			return fmt.Errorf("create secrets dir: %w", err)
		}
		if err := writeFileGuarded(s.Path, []byte(s.Content+"\n"), 0o600, force); err != nil {
			return err
		}
		chownToServiceUser(out, s.Path)
	}
	if plan.Profile == profileK8s {
		// k8s output is an artifact to drop into Helm values, not an on-disk env file.
		fmt.Fprint(out, "\n")
		fmt.Fprint(out, plan.render())
		fmt.Fprintln(out, "\nApply it via the Helm chart (deploy/helm) and mount the DSN(s) from Secrets.")
		return nil
	}
	if err := writeFileGuarded(envPath, []byte(plan.render()), 0o640, force); err != nil {
		return err
	}
	chownToServiceUser(out, envPath)

	fmt.Fprintf(out, "\nWrote %s (profile %s).\n", envPath, plan.Profile)
	if len(plan.Secrets) > 0 {
		fmt.Fprintf(out, "Wrote %d secret file(s) under %s (0600).\n", len(plan.Secrets), filepath.Dir(plan.Secrets[0].Path))
	}
	fmt.Fprintf(out, "serve will run: olivares serve %s\n", strings.Join(plan.serveFlags(), " "))
	fmt.Fprintln(out, "Apply it: sudo systemctl restart olivares   (then `journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'` for the one-time token)")
	return nil
}

// chownToServiceUser best-effort gives a file to the packaged `olivares` service
// user/group so the engine (which drops to that user) can read it. It is a no-op
// with a note when the user does not exist (a non-packaged host) or we lack
// permission — never fatal.
func chownToServiceUser(out io.Writer, path string) {
	u, uerr := user.Lookup("olivares")
	if uerr != nil {
		// Not a packaged install (no service user). Don't fail, but don't stay silent:
		// a 0600 secret owned by the current user may be unreadable by whatever account
		// actually runs the engine.
		fmt.Fprintf(out, "note: no `olivares` service user on this host; ensure the account that runs the engine can read %s (it is mode 0600/0640, owned by the current user).\n", path)
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.Chown(path, uid, gid); err != nil {
		fmt.Fprintf(out, "note: could not chown %s to the olivares user (%v); ensure the service user can read it.\n", path, err)
	}
}
