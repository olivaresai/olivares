// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	securitymodule "github.com/olivaresai/olivares/modules/security"
)

const (
	maxSupportConfigBytes      = 4 << 20
	maxSupportStatusBytes      = 1 << 20
	maxSupportStatusErrorBytes = 8 << 10
	maxSupportLogBytes         = 32 << 20
	maxSupportVerifyBytes      = 4 << 20
)

var supportSectionNames = []string{"config", "status", "logs", "manifests", "verify", "secrets"}

type supportBundleOptions struct {
	out           string
	dataDir       string
	engine        string
	dsn           string
	server        string
	caCert        string
	pins          []string
	insecure      bool
	timeout       time.Duration
	offline       bool
	configPath    string
	logsPath      string
	journal       bool
	since         string
	include       []string
	exclude       []string
	drBundles     []string
	verifyReports []string
	now           func() time.Time
}

func newSupportCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "support",
		Short:   "Collect redacted diagnostics for support and incident response",
		Example: "  olivares support bundle --server https://127.0.0.1:8443 --journal --since \"24 hours ago\"",
		Long: "Collect diagnostic configuration, status, logs, manifests, verification reports and\n" +
			"non-secret inventory metadata without resolving secret references or including key files.",
	}
	root.AddCommand(supportBundleCmd())
	return root
}

// defaultSupportBundleName is the archive name `support bundle` writes when --out
// is not given.
//
// It is computed WHEN THE BUNDLE IS WRITTEN, not in the flag's default at
// command-construction time, and the difference is not cosmetic. A default
// carrying time.Now() makes the flag's advertised default change every second:
// `olivares support bundle --help` printed a different value on every invocation,
// and the generated CLI reference (scripts/cli-ref-docs) could never be
// byte-stable, so its gate would have flapped for everyone forever. Measured
// 2026-08-16: two walks of the tree nine seconds apart differed in exactly this
// one flag out of 2209. Taking the name from createdAt also makes it agree with
// the manifest's own timestamp, which the construction-time value only
// approximated.
func defaultSupportBundleName(t time.Time) string {
	return fmt.Sprintf("olivares-support-%s.tar.gz", t.UTC().Format("20060102-150405Z"))
}

func supportBundleCmd() *cobra.Command {
	o := supportBundleOptions{now: time.Now}
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Build a redacted diagnostic tarball with an integrity manifest",
		Long: "bundle collects an allowlisted set of diagnostics into a 0600 tar.gz. Free-text\n" +
			"configuration, status, logs and verification reports are always redacted; file:, env:\n" +
			"and store: references remain literal and are never resolved. Private keys, TLS material\n" +
			"and arbitrary data-dir blobs can never enter the archive.",
		Example: `  # Online status plus the configured env file and recent systemd logs
  olivares support bundle --server https://127.0.0.1:8443 --insecure --journal --since "24 hours ago"

  # Offline bundle with an existing audit verification report
  olivares support bundle --offline --logs /var/log/olivares/engine.log --verify-report audit-verify.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return o.run(cmd, cmd.Flags().Changed("config"))
		},
	}
	cmd.Flags().StringVar(&o.out, "out", "", "output tar.gz path (default olivares-support-<UTC timestamp>.tar.gz)")
	addStoreFlags(cmd, &o.dataDir, &o.engine, &o.dsn)
	cmd.Flags().StringVar(&o.server, "server", "", "control-plane base URL (default $OLIVARES_SERVER_URL)")
	// `statusClientConfig` YA lleva caCert y pins —`status` los expone y los usa—, así que aquí
	// faltaban sólo las banderas. Sin ellas, el texto de ayuda de más abajo ofrecía `--insecure`
	// como remedio a un certificado autofirmado «que ya confías», que es justo el caso en el que
	// un pin es la respuesta correcta y estaba a un `StringArrayVar` de distancia.
	cmd.Flags().StringVar(&o.caCert, "ca-cert", "", "PEM CA bundle used to verify the control plane")
	cmd.Flags().StringArrayVar(&o.pins, "pin-sha256", nil, "pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate")
	cmd.Flags().BoolVar(&o.insecure, "insecure", false, "skip TLS certificate verification for the status request")
	cmd.Flags().DurationVar(&o.timeout, "timeout", 10*time.Second, "status request timeout")
	cmd.Flags().BoolVar(&o.offline, "offline", false, "skip the live GET /status request")
	cmd.Flags().StringVar(&o.configPath, "config", defaultEnvFilePath, "effective systemd env file to redact")
	cmd.Flags().StringVar(&o.logsPath, "logs", "", "engine log file to redact line by line")
	cmd.Flags().BoolVar(&o.journal, "journal", false, "collect journalctl output for the olivares unit")
	cmd.Flags().StringVar(&o.since, "since", "24 hours ago", "journalctl --since value (used with --journal)")
	cmd.Flags().StringSliceVar(&o.include, "include", nil, "sections to include: config,status,logs,manifests,verify,secrets (default all)")
	cmd.Flags().StringSliceVar(&o.exclude, "exclude", nil, "sections to exclude after --include selection")
	cmd.Flags().StringArrayVar(&o.drBundles, "dr-bundle", nil, "DR bundle whose non-secret manifest to include (repeatable)")
	cmd.Flags().StringArrayVar(&o.verifyReports, "verify-report", nil, "JSON output from audit verify or dr.RestoreVerify to redact and include (repeatable)")
	return cmd
}

func (o *supportBundleOptions) run(cmd *cobra.Command, configExplicit bool) error {
	if o.timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	selected, err := selectSupportSections(o.include, o.exclude)
	if err != nil {
		return err
	}
	assembler := newSupportBundleAssembler()

	if selected["config"] {
		if err := o.collectConfig(assembler, configExplicit); err != nil {
			return err
		}
	}
	if selected["status"] && !o.offline {
		cfg := statusClientConfig{server: o.server, caCert: o.caCert, pins: o.pins, insecure: o.insecure, timeout: o.timeout}
		_, raw, statusCode, err := cfg.fetch(cmd.Context())
		if err != nil {
			if statusCode != 0 && statusCode != http.StatusOK {
				redacted, _ := redactSupportText(raw)
				if len(redacted) > maxSupportStatusErrorBytes {
					redacted = append(append([]byte(nil), redacted[:maxSupportStatusErrorBytes]...), []byte("... [truncated]")...)
				}
				return fmt.Errorf("collect status: HTTP %d: %s", statusCode, strings.TrimSpace(string(redacted)))
			}
			// A TRANSPORT failure here aborts the WHOLE bundle, and it is the one
			// failure the default install walks straight into: `olivares quickstart`
			// mints a self-signed certificate, so the first support bundle an
			// operator collects on a fresh box gets x509 "signed by unknown
			// authority" and no bundle at all. Measured 2026-08-09 against a
			// real quickstart engine — exit 6, zero sections collected, and the
			// message named neither of the two flags that fix it.
			//
			// Aborting is right: a bundle silently missing a section is worse than
			// no bundle. Not saying how to proceed is not. The incident is already
			// underway when this runs; the operator should not have to re-read
			// --help to get past a certificate.
			//
			// Only the transport branch. An HTTP status above is the server ANSWERING
			// — --insecure and --offline would not change it, and offering them there
			// would send an operator to skip TLS over a 503.
			return fmt.Errorf("collect status: %w\n"+
				"  the rest of the bundle was NOT collected — the status leg is fatal on purpose,\n"+
				"  because a bundle quietly missing a section is worse than no bundle.\n"+
				"  to proceed: --offline (skip the live GET /status and collect everything else),\n"+
				"  or --insecure if this is a self-signed certificate you already trust\n"+
				"  (`olivares quickstart` mints one on a first boot)", err)
		}
		if len(raw) > maxSupportStatusBytes {
			return fmt.Errorf("collect status: response exceeds %d bytes", maxSupportStatusBytes)
		}
		redacted, count := redactSupportText(raw)
		if err := assembler.add("status/status.json", "GET /status", redacted, count); err != nil {
			return err
		}
	}
	if selected["logs"] {
		if err := o.collectLogs(cmd, assembler); err != nil {
			return err
		}
	}
	if selected["manifests"] {
		if err := o.collectManifests(assembler); err != nil {
			return err
		}
	}
	if selected["verify"] {
		if err := o.collectVerifyReports(assembler); err != nil {
			return err
		}
	}
	if selected["secrets"] {
		if err := o.collectSecretInventory(cmd, assembler); err != nil {
			return err
		}
	}

	createdAt := time.Now()
	if o.now != nil {
		createdAt = o.now()
	}
	if o.out == "" {
		o.out = defaultSupportBundleName(createdAt)
	}
	digest, err := writeSupportBundle(o.out, version, createdAt, assembler)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "support bundle written to %s\nmanifest.json sha256:%s\n", o.out, digest)
	return err
}

func (o *supportBundleOptions) collectConfig(assembler *supportBundleAssembler, explicit bool) error {
	if err := refuseSupportKeyMaterial(o.configPath, o.dataDir); err != nil {
		return err
	}
	b, err := readSupportFile(o.configPath, maxSupportConfigBytes)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("collect config: %w", err)
	}
	redacted, count := redactEffectiveConfig(string(b))
	return assembler.add("config/effective.txt", "configured env file", []byte(redacted), count)
}

func (o *supportBundleOptions) collectLogs(cmd *cobra.Command, assembler *supportBundleAssembler) error {
	var raw bytes.Buffer
	var sources []string
	if o.logsPath != "" {
		if err := refuseSupportKeyMaterial(o.logsPath, o.dataDir); err != nil {
			return err
		}
		b, err := readSupportFile(o.logsPath, maxSupportLogBytes)
		if err != nil {
			return fmt.Errorf("collect logs: %w", err)
		}
		raw.Write(b)
		sources = append(sources, "log file")
	}
	if o.journal {
		journalCmd := exec.CommandContext(cmd.Context(), "journalctl", "-u", "olivares", "--since", o.since, "--no-pager", "-o", "short-iso") // #nosec G204 -- fixed journalctl invocation; o.since is passed as a single arg, not shell-interpolated
		b, err := journalCmd.CombinedOutput()
		if err != nil {
			safe, _ := securitymodule.RedactText(string(b))
			return fmt.Errorf("collect journal: %w: %s", err, strings.TrimSpace(safe))
		}
		if len(b) > maxSupportLogBytes {
			return fmt.Errorf("collect journal: output exceeds %d bytes", maxSupportLogBytes)
		}
		if raw.Len() > 0 && !bytes.HasSuffix(raw.Bytes(), []byte("\n")) {
			raw.WriteByte('\n')
		}
		raw.Write(b)
		sources = append(sources, "journalctl")
	}
	if len(sources) == 0 {
		return nil
	}
	if raw.Len() > maxSupportLogBytes {
		return fmt.Errorf("collect logs: combined output exceeds %d bytes", maxSupportLogBytes)
	}
	redacted, count := redactSupportLines(raw.Bytes())
	return assembler.add("logs/engine.log", strings.Join(sources, " + "), redacted, count)
}

func (o *supportBundleOptions) collectManifests(assembler *supportBundleAssembler) error {
	schema, err := collectSchemaManifest()
	if err != nil {
		return err
	}
	// render-exempt: this JSON is written INTO the support tarball as a file,
	// not printed. The bundle is the deliverable; -o governs what a command
	// prints to stdout, and the tarball's internal format is fixed so support
	// tooling can read it.
	b, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Errorf("collect schema manifest: %w", err)
	}
	if err := assembler.add("manifests/schema.json", "compiled schema collector", append(b, '\n'), 0); err != nil {
		return err
	}

	inputs := append([]string(nil), o.drBundles...)
	sort.Strings(inputs)
	for i, input := range inputs {
		work, err := os.MkdirTemp("", "olivares-support-dr-")
		if err != nil {
			return fmt.Errorf("collect DR manifest: %w", err)
		}
		manifest, extractErr := extractDRManifest(input, work)
		removeErr := os.RemoveAll(work)
		if extractErr != nil {
			return fmt.Errorf("collect DR manifest: %w", extractErr)
		}
		if removeErr != nil {
			return fmt.Errorf("collect DR manifest: remove scratch data: %w", removeErr)
		}
		// render-exempt: written INTO the support tarball as a file, not printed.
		// The bundle is the deliverable and its internal format is fixed so the
		// support tooling that opens it can read it.
		b, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("collect DR manifest: %w", err)
		}
		name := fmt.Sprintf("manifests/dr-%03d.json", i+1)
		redacted, count := redactSupportText(append(b, '\n'))
		if err := assembler.add(name, fmt.Sprintf("DR bundle manifest #%d", i+1), redacted, count); err != nil {
			return err
		}
	}
	return nil
}

func extractDRManifest(input, work string) (*dr.Manifest, error) {
	f, err := os.Open(input)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	manifest, _, err := dr.ExtractBundle(f, work)
	return manifest, err
}

func (o *supportBundleOptions) collectVerifyReports(assembler *supportBundleAssembler) error {
	inputs := append([]string(nil), o.verifyReports...)
	sort.Strings(inputs)
	for i, input := range inputs {
		if err := refuseSupportKeyMaterial(input, o.dataDir); err != nil {
			return err
		}
		b, err := readSupportFile(input, maxSupportVerifyBytes)
		if err != nil {
			return fmt.Errorf("collect verify report: %w", err)
		}
		redacted, count := redactSupportText(b)
		name := fmt.Sprintf("verify/report-%03d.json", i+1)
		if err := assembler.add(name, fmt.Sprintf("operator-provided verify report #%d", i+1), redacted, count); err != nil {
			return err
		}
	}
	return nil
}

func (o *supportBundleOptions) collectSecretInventory(cmd *cobra.Command, assembler *supportBundleAssembler) error {
	if secret.IsReference(o.dsn) {
		return fmt.Errorf("collect secret inventory: --dsn references are not resolved by support bundle; pass a literal DSN or exclude the secrets section")
	}
	engineKind := store.Engine(o.engine)
	switch engineKind {
	case store.EngineSQLite, store.EnginePostgres:
	default:
		return fmt.Errorf("collect secret inventory: unknown --engine %q", o.engine)
	}
	// THE READ-ONLY BOOT, not a raw Open (2026-08-05). This collector used to call
	// coreengine.Open with a NIL schema-registration callback, behind a
	// secure.EnsureDir. Both halves were wrong, and both were measured:
	//
	//   - On a HEALTHY install it FAILED and produced no bundle at all. The guard
	//     control plane derives its rollout_id from the SHA of the guard-unit
	//     manifest, which is derived from the REGISTERED schemas; opening with nil
	//     registers only the core schema, so the units differ from the ones the
	//     engine wrote at boot and the append-only receipt reconciliation refuses.
	//     Deterministic, reproduced on three separate installs — never a race. The
	//     incident-response tool did not work on a working product.
	//
	//   - Pointed at a data dir with no store, it CREATED and migrated one itself,
	//     verified its own receipts against itself, exited 0 — and left a store
	//     `serve` could then never open, plus three freshly minted signing keys. A
	//     command that reads like a diagnostic permanently bricked the directory.
	//
	// boot(ReadOnly) is the existing answer to exactly this class (bootConfig
	// ReadOnly): it mints no key, creates no data dir, creates no store file, and
	// returns NotFound when there is nothing to read — while registering the same
	// schemas the engine does, so the guard manifest matches. `secrets ls` and
	// `audit verify` already went through it. This collector was the last raw open
	// in the binary, and simply had not reached it.
	eng, err := auditBootRO(cmd, o.dataDir, o.engine, o.dsn)
	if err != nil {
		return fmt.Errorf("collect secret inventory: %w", err)
	}
	defer func() { _ = eng.Close() }()
	views, err := eng.secretStore.List(cmd.Context(), auth.GlobalSecretScope)
	if err != nil {
		return fmt.Errorf("collect secret inventory: %w", err)
	}
	var b strings.Builder
	if len(views) == 0 {
		b.WriteString("no secrets stored\n")
	} else {
		// render-exempt: rendered into a buffer that becomes a FILE inside the
		// support tarball, not written to stdout.
		tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tHINT\tDESCRIPTION\tUPDATED")
		for _, view := range views {
			updated := ""
			if !view.UpdatedAt.IsZero() {
				updated = view.UpdatedAt.String()
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", view.Name, view.Hint, view.Description, updated)
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("collect secret inventory: %w", err)
		}
	}
	redacted, count := redactSupportText([]byte(b.String()))
	return assembler.add("secrets/inventory.txt", "secret store metadata (List only)", redacted, count)
}

func readSupportFile(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := readSupportInput(f, limit)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", name, err)
	}
	return b, nil
}

func refuseSupportKeyMaterial(name, dataDir string) error {
	if !supportInputLooksLikeKeyMaterial(name, dataDir) {
		return nil
	}
	return fmt.Errorf("support bundle: refusing to read %s: key material or data-dir content is never ingested", name)
}

func supportInputLooksLikeKeyMaterial(name, dataDir string) bool {
	paths := []string{name}
	if resolved, err := filepath.EvalSymlinks(name); err == nil && resolved != name {
		paths = append(paths, resolved)
	}
	for _, candidate := range paths {
		base := strings.ToLower(filepath.Base(candidate))
		if base == "secret-store.key" || strings.HasSuffix(base, "-signing.key") ||
			strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".pem") {
			return true
		}
	}

	if dataDir == "" {
		resolved, err := defaultDataDir()
		if err != nil {
			// This is a REFUSAL predicate: if the default cannot be resolved we must
			// not quietly stop checking. Fall back to the pre relative name so
			// this guard is never weaker than it was before the default moved.
			resolved = legacyDataDirName
		}
		dataDir = resolved
	}
	for _, candidate := range paths {
		// The data dir holds the SQLite DB (opaque secret material — names,
		// ciphertext, payloads — that text redaction cannot scrub), its WAL/SHM and
		// pre-restore copies, and the sealed key/secret files. No diagnostic input
		// legitimately comes from inside it, so refuse the lot: this is what makes
		// "arbitrary data-dir blobs can never enter the archive" actually true.
		if supportPathWithinDir(candidate, dataDir) {
			return true
		}
	}
	return false
}

func supportPathWithinDir(name, dir string) bool {
	absName, err := filepath.Abs(name)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absDir); resolveErr == nil {
		absDir = resolved
	}
	rel, err := filepath.Rel(absDir, absName)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func selectSupportSections(includes, excludes []string) (map[string]bool, error) {
	selected := make(map[string]bool, len(supportSectionNames))
	if len(includes) == 0 {
		for _, name := range supportSectionNames {
			selected[name] = true
		}
	} else {
		for _, selector := range includes {
			if err := applySupportSelector(selected, selector, true); err != nil {
				return nil, fmt.Errorf("--include: %w", err)
			}
		}
	}
	for _, selector := range excludes {
		if err := applySupportSelector(selected, selector, false); err != nil {
			return nil, fmt.Errorf("--exclude: %w", err)
		}
	}
	return selected, nil
}

func applySupportSelector(selected map[string]bool, selector string, value bool) error {
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == "all" {
		for _, name := range supportSectionNames {
			selected[name] = value
		}
		return nil
	}
	for _, name := range supportSectionNames {
		if selector == name {
			selected[name] = value
			return nil
		}
	}
	return fmt.Errorf("unknown section %q (one of: %s)", selector, strings.Join(supportSectionNames, ", "))
}
