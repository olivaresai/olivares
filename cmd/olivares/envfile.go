// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/secret"
)

// envfile.go is the shared model behind `olivares setup` (interactive) and
// `olivares config generate` (non-interactive): a validated installPlan that
// renders a STRUCTURED /etc/olivares/olivares.env — not a hand-edited raw
// OLIVARES_EXTRA_ARGS string. The plan composes the production serve flags the
// engine already exposes (the plumbing is good; only the configuration UX was the
// gap), validates every value up front, and externalizes secrets (the database
// password) into 0600 files referenced as file:<path> so no credential is left in
// cleartext in the env file (secret-bootstrap).

// The four install profiles. Each asks only what it needs and seeds safe defaults.
const (
	profileEval        = "eval"             // SQLite, loopback, self-signed — the quickstart shape, but written to a unit
	profileSingleNode  = "single-node-prod" // SQLite on a real host behind your TLS-terminating proxy
	profilePostgresPro = "postgres-prod"    // Postgres with the RLS backstop, residency, BYO TLS
	profileK8s         = "k8s"              // emits container args/env + a Helm values snippet, not a systemd env file
)

var allProfiles = []string{profileEval, profileSingleNode, profilePostgresPro, profileK8s}

// Default install locations (the systemd package layout).
const (
	defaultEnvFilePath = "/etc/olivares/olivares.env"
	defaultSecretsDir  = "/etc/olivares/secrets"
	defaultUnitDataDir = "/var/lib/olivares"
)

// envSecretFile is a 0600 file the plan externalizes a secret into (e.g. the
// password-bearing DSN), referenced from the env file as file:<Path>.
type envSecretFile struct {
	Path    string
	Content string
}

// installPlan is the validated, profile-resolved configuration both entry points
// produce. Empty fields are simply omitted from the output.
type installPlan struct {
	Profile    string
	Listen     string
	GRPCListen string
	// DataDir overrides the unit's hardcoded /var/lib/olivares (emitted as --data-dir
	// only when it differs, so the default stays implicit).
	DataDir string
	Engine  string // sqlite | postgres
	// DSNArg/OwnerDSNArg/AdminDSNArg are the values placed after --dsn / --owner-dsn /
	// --admin-dsn — a literal, or a file:/env: reference. The password-bearing literal
	// (if any) lives in Secrets, not here.
	DSNArg       string
	OwnerDSNArg  string
	AdminDSNArg  string
	TLSCert      string
	TLSKey       string
	GRPCClientCA string
	// AuditSigningKeyFile is an operator-provisioned Ed25519 private-key file.
	// It is rendered as OLIVARES_AUDIT_SIGNING_KEY_FILE with declared BYOK
	// custody, never as a serve flag or inline key material.
	AuditSigningKeyFile string
	License             string
	Region              string
	KnownRegions        []string
	Insecure            bool
	AllowPrivilegedDB   bool
	// CheckpointInterval overrides the unit default (1h); emitted only when set.
	CheckpointInterval string
	// MaxConns sets OLIVARES_DB_MAX_CONNS (env-only knob; 0 = leave the engine default).
	MaxConns int
	// Secrets are the 0600 files to write alongside the env file (DSN refs, etc.).
	Secrets []envSecretFile
}

var (
	regionRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	// hostPortRE accepts an all-interfaces bind (":8443") as well as "host:port"
	// and "[::1]:port"; net.SplitHostPort then confirms the shape. The numeric-port
	// tail is what SplitHostPort itself does NOT enforce.
	hostPortRE = regexp.MustCompile(`^.*:[0-9]+$`)
	// passwordKeywordRE catches a libpq keyword-form DSN that inlines a password
	// (the URL form is caught via net/url userinfo).
	passwordKeywordRE = regexp.MustCompile(`(?i)(^|\s)password\s*=`)
)

// dsnInlinesSecret reports whether a DSN argument carries a password BY VALUE — a
// URL userinfo password or a libpq `password=` keyword. A secret reference
// (file:/env:/store:/…) points AT the secret and is fine. Used to refuse a
// cleartext credential in the generated env file (the strict no-inline-secret
// posture of core/secret, applied to the one secret the wizard can't store in the
// sealed store: the database password).
func dsnInlinesSecret(arg string) bool {
	if arg == "" || secret.IsReference(arg) {
		return false
	}
	if u, err := url.Parse(arg); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok {
			return true
		}
	}
	return passwordKeywordRE.MatchString(arg)
}

// validate checks every field so a misconfiguration is caught at generate time, not
// at the first failed boot. It returns the first problem found.
func (p installPlan) validate() error {
	if !contains(allProfiles, p.Profile) {
		return fmt.Errorf("unknown profile %q (one of: %s)", p.Profile, strings.Join(allProfiles, ", "))
	}
	if (p.Profile == profileSingleNode || p.Profile == profilePostgresPro) && p.Insecure {
		return fmt.Errorf("--insecure is forbidden for production profiles (profile %s); configure TLS or terminate it at a trusted proxy without disabling engine TLS", p.Profile)
	}
	if p.Profile == profilePostgresPro {
		if err := p.validatePostgresProduction(); err != nil {
			return err
		}
	}
	for _, hp := range []struct{ name, val string }{{"listen", p.Listen}, {"grpc-listen", p.GRPCListen}} {
		if hp.val == "" {
			return fmt.Errorf("%s address is required", hp.name)
		}
		if !hostPortRE.MatchString(hp.val) {
			return fmt.Errorf("%s %q is not a host:port", hp.name, hp.val)
		}
		if _, _, err := net.SplitHostPort(hp.val); err != nil {
			return fmt.Errorf("%s %q: %w", hp.name, hp.val, err)
		}
	}
	switch p.Engine {
	case string(engineSQLite):
		if p.DSNArg != "" && strings.HasPrefix(p.DSNArg, "postgres") {
			return fmt.Errorf("engine sqlite with a postgres DSN %q — set engine postgres", p.DSNArg)
		}
	case string(enginePostgres):
		if p.DSNArg == "" {
			return fmt.Errorf("engine postgres requires a --dsn (provision roles first with `olivares db init`)")
		}
	default:
		return fmt.Errorf("engine %q must be sqlite or postgres", p.Engine)
	}
	// The whole point of is keeping the database password out of the env file.
	// Refuse a DSN that inlines a credential — store it in a 0600 file and reference
	// it (the interactive wizard does this automatically; `config generate` users
	// must pass file:/env: themselves).
	for _, d := range []struct{ name, val string }{{"--dsn", p.DSNArg}, {"--owner-dsn", p.OwnerDSNArg}, {"--admin-dsn", p.AdminDSNArg}} {
		if dsnInlinesSecret(d.val) {
			return fmt.Errorf("%s carries an inline password; the generated config must not hold a cleartext credential — store it in a 0600 file and reference it as %s=file:/etc/olivares/secrets/db.dsn (env:<VAR> also works), or run `olivares setup` which externalizes it for you", d.name, d.name)
		}
	}
	if (p.TLSCert == "") != (p.TLSKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be set together")
	}
	if p.Region != "" && !regionRE.MatchString(p.Region) {
		return fmt.Errorf("region %q is not a valid code ([a-z][a-z0-9-]*)", p.Region)
	}
	for _, r := range p.KnownRegions {
		if !regionRE.MatchString(r) {
			return fmt.Errorf("known-region %q is not a valid code", r)
		}
	}
	if len(p.KnownRegions) > 0 && p.Region == "" {
		return fmt.Errorf("--known-regions is only meaningful with a home --region set")
	}
	if p.CheckpointInterval != "" {
		if _, err := time.ParseDuration(p.CheckpointInterval); err != nil {
			return fmt.Errorf("checkpoint-interval %q: %w", p.CheckpointInterval, err)
		}
	}
	if p.MaxConns < 0 {
		return fmt.Errorf("max-conns must be ≥ 0")
	}
	if p.Insecure && (!hostIsLoopback(p.Listen) || !hostIsLoopback(p.GRPCListen)) {
		bad := p.Listen
		if hostIsLoopback(p.Listen) {
			bad = p.GRPCListen
		}
		return fmt.Errorf("--insecure on a non-loopback bind (%q) would serve plaintext off-host (gRPC ingest carries the collector bearer token in clear); bind 127.0.0.1 on both listeners, or terminate TLS at your proxy and keep the engine on loopback", bad)
	}
	if p.AllowPrivilegedDB && p.Engine != string(enginePostgres) {
		return fmt.Errorf("--allow-privileged-db-role only applies to engine postgres")
	}
	return nil
}

// validatePostgresProduction makes the named profile a contract rather than a
// cosmetic label. These are the locally provable inputs; the runtime still probes
// the role attributes/RLS posture, while replica count and load-balancer health are
// deployment-owned and must be proved by the HA/DR drill.
func (p installPlan) validatePostgresProduction() error {
	if p.Engine != string(enginePostgres) {
		return fmt.Errorf("profile %s requires engine postgres (got %q)", profilePostgresPro, p.Engine)
	}
	if p.DSNArg == "" {
		return fmt.Errorf("profile %s requires an application-role --dsn", profilePostgresPro)
	}
	if p.OwnerDSNArg == "" {
		return fmt.Errorf("profile %s requires a separate least-privilege --owner-dsn", profilePostgresPro)
	}
	if strings.TrimSpace(p.OwnerDSNArg) == strings.TrimSpace(p.DSNArg) {
		return fmt.Errorf("profile %s requires distinct application and owner DSNs", profilePostgresPro)
	}
	if p.AdminDSNArg == "" {
		return fmt.Errorf("profile %s requires a dedicated cross-tenant --admin-dsn (NOSUPERUSER BYPASSRLS)", profilePostgresPro)
	}
	if strings.TrimSpace(p.AdminDSNArg) == strings.TrimSpace(p.DSNArg) ||
		strings.TrimSpace(p.AdminDSNArg) == strings.TrimSpace(p.OwnerDSNArg) {
		return fmt.Errorf("profile %s requires distinct application, owner, and admin DSNs", profilePostgresPro)
	}
	if p.TLSCert == "" || p.TLSKey == "" {
		return fmt.Errorf("profile %s requires operator-provided --tls-cert and --tls-key", profilePostgresPro)
	}
	if p.GRPCClientCA == "" {
		return fmt.Errorf("profile %s requires --grpc-client-ca so collector ingest uses mutual TLS", profilePostgresPro)
	}
	if p.AuditSigningKeyFile == "" {
		return fmt.Errorf("profile %s requires --audit-signing-key-file for external BYOK custody", profilePostgresPro)
	}
	if p.AllowPrivilegedDB {
		return fmt.Errorf("profile %s forbids --allow-privileged-db-role because it disables the FORCE-RLS backstop", profilePostgresPro)
	}
	return nil
}

// engine aliases so the plan can reference the store engines without importing the
// store package into this file's signature.
type engineName string

const (
	engineSQLite   engineName = "sqlite"
	enginePostgres engineName = "postgres"
)

// serveFlags composes the complete, ordered `serve` flag list that realizes the
// plan. It is what goes into OLIVARES_EXTRA_ARGS (systemd) and into a container's
// args (k8s) — one source of truth for both renderings.
func (p installPlan) serveFlags() []string {
	var f []string
	add := func(args ...string) { f = append(f, args...) }
	if p.Engine == string(enginePostgres) {
		add("--engine=postgres")
	}
	if p.DSNArg != "" {
		add("--dsn=" + p.DSNArg)
	}
	if p.OwnerDSNArg != "" {
		add("--owner-dsn=" + p.OwnerDSNArg)
	}
	if p.AdminDSNArg != "" {
		add("--admin-dsn=" + p.AdminDSNArg)
	}
	if p.DataDir != "" && p.DataDir != defaultUnitDataDir {
		add("--data-dir=" + p.DataDir)
	}
	add("--listen=" + p.Listen)
	add("--grpc-listen=" + p.GRPCListen)
	if p.TLSCert != "" {
		add("--tls-cert="+p.TLSCert, "--tls-key="+p.TLSKey)
	}
	if p.GRPCClientCA != "" {
		add("--grpc-client-ca=" + p.GRPCClientCA)
	}
	if p.Region != "" {
		add("--region=" + p.Region)
		if len(p.KnownRegions) > 0 {
			add("--known-regions=" + strings.Join(p.sortedKnownRegions(), ","))
		}
	}
	if p.License != "" {
		add("--license=" + p.License)
	}
	if p.AllowPrivilegedDB {
		add("--allow-privileged-db-role")
	}
	if p.Insecure {
		add("--insecure")
	}
	if p.CheckpointInterval != "" && p.CheckpointInterval != "1h" {
		add("--checkpoint-interval=" + p.CheckpointInterval)
	}
	return f
}

// renderEnvFile renders the structured systemd env file. It is deterministic (no
// clock) so it diffs cleanly across regenerations and is easy to test.
func (p installPlan) renderEnvFile() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# SPDX-FileCopyrightText: 2026 Olivares.AI\n")
	w("# SPDX-License-Identifier: AGPL-3.0-only\n")
	w("#\n")
	w("# Generated by `olivares setup` (profile: %s). The systemd unit appends\n", p.Profile)
	w("# OLIVARES_EXTRA_ARGS to `olivares serve`. Re-run `olivares setup` or\n")
	w("# `olivares config generate` to change it — do NOT hand-edit the args string.\n")
	w("# Secrets are referenced as file:<path>, never inlined (see %s).\n", defaultSecretsDir)
	if p.Profile == profilePostgresPro {
		w("# Resolved posture: postgres-prod = Postgres + distinct app/owner/admin DSN refs;\n")
		w("# TLS + collector mTLS; external BYOK audit key. Runtime proves role/RLS posture;\n")
		w("# the deployment's replica/LB posture is proved separately by the HA/DR drill.\n")
	}
	w("#\n")
	if p.AuditSigningKeyFile != "" {
		w("# External audit-signing key (declared BYOK; boot fails if the file is absent):\n")
		w("%s=byok\n", envKeyCustody)
		w("%s=%s\n#\n", envAuditKeyFile, p.AuditSigningKeyFile)
	}
	if p.MaxConns > 0 {
		w("# Postgres application-pool cap per node:\n")
		w("OLIVARES_DB_MAX_CONNS=%d\n#\n", p.MaxConns)
	}
	w("# Validated serve flags (engine knobs, listeners, TLS, residency):\n")
	w("OLIVARES_EXTRA_ARGS=%s\n", strings.Join(p.serveFlags(), " "))
	return b.String()
}

// renderK8s renders the container-oriented artifact for the k8s profile: the serve
// args as a Helm `extraArgs` list plus the env knobs, with the secret-as-Secret
// guidance (mount the DSN as a file and reference it as file:<path>).
func (p installPlan) renderK8s() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Generated by `olivares config generate` (profile: k8s).\n")
	w("# Drop `extraArgs` into your Helm values (deploy/helm) or the container args.\n")
	w("# Mount each secret-bearing DSN from a Kubernetes Secret and reference it as\n")
	w("# file:<mountPath> — the engine resolves it at boot, so no password is in the\n")
	w("# manifest. The Helm chart wires the RLS-safe roles `olivares db init` provisions.\n")
	w("extraArgs:\n")
	for _, a := range p.serveFlags() {
		w("  - %q\n", a)
	}
	if p.MaxConns > 0 {
		w("env:\n  - name: OLIVARES_DB_MAX_CONNS\n    value: %q\n", fmt.Sprintf("%d", p.MaxConns))
	}
	return b.String()
}

// render returns the right artifact for the plan's profile.
func (p installPlan) render() string {
	if p.Profile == profileK8s {
		return p.renderK8s()
	}
	return p.renderEnvFile()
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// sortedKnownRegions returns KnownRegions with the home region folded in and sorted,
// so the generated list is stable and always contains the home region (the engine
// adds it implicitly; making it explicit keeps the env file self-describing).
func (p installPlan) sortedKnownRegions() []string {
	set := map[string]bool{}
	for _, r := range p.KnownRegions {
		set[r] = true
	}
	if p.Region != "" {
		set[p.Region] = true
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
