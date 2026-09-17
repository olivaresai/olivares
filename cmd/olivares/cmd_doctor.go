// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/localinstall"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
)

const doctorSchema = "olivares.ai/doctor/v1"

type doctorOptions struct {
	mode, init, dataDir, config, unit string
	binary, server, caCert            string
	auditTenant                       string
	checkUpdates                      bool
	timeout                           time.Duration
}

type doctorBuild struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Date       string `json:"date"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	LicenseKey string `json:"license_key"`
	OTAKey     string `json:"ota_key"`
}

type doctorPaths struct {
	Binary   string `json:"binary"`
	DataDir  string `json:"data_dir"`
	Config   string `json:"config"`
	Unit     string `json:"unit"`
	Manifest string `json:"manifest"`
}

type doctorCheck struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Required    bool   `json:"required"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

type doctorReport struct {
	Schema  string        `json:"schema"`
	Overall string        `json:"overall"`
	Mode    string        `json:"mode"`
	Init    string        `json:"init"`
	Build   doctorBuild   `json:"build"`
	Paths   doctorPaths   `json:"paths"`
	Checks  []doctorCheck `json:"checks"`
}

type doctorDeps struct {
	stat      func(string) (fs.FileInfo, error)
	readFile  func(string) ([]byte, error)
	lookPath  func(string) (string, error)
	run       func(context.Context, string, ...string) (int, error)
	runOutput func(context.Context, string, ...string) (int, []byte, error)
	httpGet   func(context.Context, string, string, time.Duration) (int, []byte, error)
	getenv    func(string) string
	euid      func() int
	lookupUID func(string) (int, error)
	homeDir   func() (string, error)
	goos      string
}

// doctorCommands is the CLOSED SET of external programs `doctor` may run by NAME, and it exists
// because gosec G204 asked "can an attacker choose what runs" of the two seams below.
//
// Counted at the call sites rather than assumed: `doctor` reaches exec with a name from exactly
// two places. One is the service-manager switch of doctorInitCheck, whose five branches assign
// three literals (systemctl, rc-service, launchctl); the other is `o.binary`, the operator's own
// `--binary` flag, which resolveDoctorPaths REFUSES unless it is absolute and which defaults to
// this process's own executable. So the answer was already no — but it was a property of today's
// call sites, provable only by reading them. The predicate below turns that reading into a
// refusal: a name is either one of the three managers or an absolute path.
//
// ⚠ WHAT THIS DOES NOT CLAIM. An absolute path is not a safe path: an operator who can pass
// `--binary /anything` can also run `/anything` directly, so this seam is not a privilege
// boundary and pretending otherwise would be worse than saying it. What the predicate stops is a
// RELATIVE name reaching exec, where `$PATH` — not the operator — would choose the program.
var doctorCommands = map[string]struct{}{
	"systemctl": {}, "rc-service": {}, "launchctl": {},
}

func doctorExecutable(name string) error {
	if _, ok := doctorCommands[name]; ok {
		return nil
	}
	if filepath.IsAbs(name) {
		return nil
	}
	return fmt.Errorf("doctor refuses to execute %q: not a known service manager and not an absolute path", name)
}

func defaultDoctorDeps() doctorDeps {
	return doctorDeps{
		stat: os.Stat, readFile: os.ReadFile, lookPath: exec.LookPath,
		run: func(ctx context.Context, name string, args ...string) (int, error) {
			if err := doctorExecutable(name); err != nil {
				return -1, err
			}
			// #nosec G204 -- name is bounded by doctorExecutable above: one of three literal
			// service managers, or an absolute path the operator passed in --binary (validated
			// absolute in resolveDoctorPaths). Args reach exec without a shell.
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin = nil
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			if err := cmd.Run(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					return ee.ExitCode(), nil
				}
				return -1, err
			}
			return 0, nil
		},
		runOutput: func(ctx context.Context, name string, args ...string) (int, []byte, error) {
			if err := doctorExecutable(name); err != nil {
				return -1, nil, err
			}
			// #nosec G204 -- same bound as `run` above, same reason.
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin = nil
			cmd.Stderr = io.Discard
			body, err := cmd.Output()
			if err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					return ee.ExitCode(), body, nil
				}
				return -1, nil, err
			}
			return 0, body, nil
		},
		httpGet: doctorHTTPGet,
		getenv:  os.Getenv,
		euid:    os.Geteuid,
		lookupUID: func(name string) (int, error) {
			u, err := user.Lookup(name)
			if err != nil {
				return 0, err
			}
			return strconv.Atoi(u.Uid)
		},
		homeDir: os.UserHomeDir,
		goos:    runtime.GOOS,
	}
}

func newDoctorCmd() *cobra.Command {
	o := &doctorOptions{mode: "auto", init: "auto", server: "https://127.0.0.1:8443", timeout: 10 * time.Second}
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose this host installation without printing secrets",
		Long: "doctor checks the installed binary and verification anchors, local paths and modes,\n" +
			"service account and init state, TLS live/ready probes, store status, license, and\n" +
			"optional audit/update checks. Values from the env file are never read into output.\n\n" +
			"Exit 0 means healthy, 1 a measured defect, and 2 that a required check could not\n" +
			"be measured. `health` remains the separate remote subject-health namespace.",
		Example: "  olivares doctor --mode system --data-dir /var/lib/olivares\n" +
			"  olivares doctor --mode user -o json\n" +
			"  olivares doctor --audit-tenant t_abc123 --check-updates",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, code, err := runDoctor(cmd.Context(), o, defaultDoctorDeps())
			if err != nil {
				return err
			}
			if err := renderDoctor(cmd, report); err != nil {
				return err
			}
			if code != exitcode.OK {
				return exitcode.New(code, nil)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.mode, "mode", o.mode, "service scope: auto | user | system")
	f.StringVar(&o.init, "init", o.init, "init adapter: auto | systemd | openrc | launchd")
	f.StringVar(&o.dataDir, "data-dir", "", "service data directory (default follows --mode)")
	f.StringVar(&o.config, "config", "", "service env file (default follows --mode)")
	f.StringVar(&o.unit, "unit", "", "service unit/plist path (default follows --mode and --init)")
	f.StringVar(&o.binary, "binary", "", "installed olivares binary (default: this executable)")
	f.StringVar(&o.server, "server", o.server, "local HTTPS engine origin")
	f.StringVar(&o.caCert, "ca-cert", "", "PEM trust anchor (default <data-dir>/tls.crt)")
	f.DurationVar(&o.timeout, "timeout", o.timeout, "deadline for each init/network subprocess")
	f.StringVar(&o.auditTenant, "audit-tenant", "", "also run strict local audit verification for this tenant")
	f.BoolVar(&o.checkUpdates, "check-updates", false, "also run the signed channel check (network; may record fresh CRL observations)")
	return cmd
}

func runDoctor(ctx context.Context, raw *doctorOptions, deps doctorDeps) (doctorReport, int, error) {
	o := *raw
	if o.timeout <= 0 {
		return doctorReport{}, 0, doctorUsage("--timeout must be positive")
	}
	if o.mode == "auto" {
		if deps.euid() == 0 {
			o.mode = "system"
		} else {
			o.mode = "user"
		}
	}
	if o.mode != "user" && o.mode != "system" {
		return doctorReport{}, 0, doctorUsage("--mode must be auto, user or system")
	}
	if o.init == "auto" {
		o.init = detectDoctorInit(deps)
	}
	if o.init != "systemd" && o.init != "openrc" && o.init != "launchd" && o.init != "unknown" {
		return doctorReport{}, 0, doctorUsage("--init must be auto, systemd, openrc or launchd")
	}
	if o.init == "openrc" && o.mode == "user" {
		return doctorReport{}, 0, doctorUsage("OpenRC has no user-service adapter; use --mode system")
	}
	if (o.init == "launchd") != (deps.goos == "darwin") {
		return doctorReport{}, 0, doctorUsage("launchd requires darwin; systemd/OpenRC require linux")
	}
	if o.init != "launchd" && o.init != "unknown" && deps.goos != "linux" {
		return doctorReport{}, 0, doctorUsage("systemd/OpenRC require linux")
	}

	if err := resolveDoctorPaths(&o, deps); err != nil {
		return doctorReport{}, 0, err
	}
	licKey := doctorAnchor(license.KeyOrigin(), license.KeyFingerprint())
	otaKey := doctorAnchor(release.KeyOrigin(), release.KeyFingerprint())
	report := doctorReport{
		Schema: doctorSchema, Overall: "healthy", Mode: o.mode, Init: o.init,
		Build: doctorBuild{Version: version, Commit: commit, Date: date, OS: runtime.GOOS,
			Arch: runtime.GOARCH, LicenseKey: licKey, OTAKey: otaKey},
		Paths: doctorPaths{Binary: o.binary, DataDir: o.dataDir, Config: o.config,
			Unit: o.unit, Manifest: filepath.Join(o.dataDir, "install-manifest.json")},
	}
	add := func(c doctorCheck) { report.Checks = append(report.Checks, c) }

	add(doctorBuildCheck(licKey, otaKey))
	add(doctorFileCheck(deps, "binary", o.binary, true, []fs.FileMode{0o755, 0o750, 0o700}))
	dataModes := []fs.FileMode{0o700}
	configModes := []fs.FileMode{0o400, 0o600}
	if o.mode == "system" {
		dataModes = []fs.FileMode{0o700, 0o750}
		configModes = []fs.FileMode{0o400, 0o440, 0o600, 0o640}
	}
	add(doctorDirCheck(deps, o.dataDir, dataModes))
	add(doctorConfigCheck(deps, o.config, configModes))
	unitModes := []fs.FileMode{0o644}
	if o.init == "openrc" {
		unitModes = []fs.FileMode{0o755}
	}
	if o.init == "unknown" {
		add(doctorCheck{Name: "service-unit", Status: "unknown", Required: true,
			Detail: "no supported init adapter was detected", Remediation: "pass --init after confirming systemd, OpenRC or launchd"})
	} else {
		add(doctorUnitCheck(deps, o, unitModes))
	}
	add(doctorManifestCheck(deps, report.Paths.Manifest, o))
	accountCheck, accountUID := doctorAccountCheck(deps, o)
	add(accountCheck)
	add(doctorOwnershipCheck(deps, o, report.Paths.Manifest, accountUID))
	add(doctorAgentOpsCheck(deps, o, report.Paths.Manifest, configModes))
	initCheck := doctorInitCheck(ctx, deps, o)
	add(initCheck)
	initActive := initCheck.Status == "pass"

	ca := o.caCert
	if ca == "" {
		ca = filepath.Join(o.dataDir, "tls.crt")
	}
	live := doctorProbe(ctx, deps, o, ca, "/livez", initActive)
	ready := doctorProbe(ctx, deps, o, ca, "/readyz", initActive)
	add(live)
	add(ready)
	add(doctorStoreCheck(ctx, deps, o, ca, initActive))
	add(doctorLicenseCheck(o.dataDir))
	add(doctorAuditCheck(ctx, deps, o))
	add(doctorChannelCheck(ctx, deps, o))

	code := exitcode.OK
	for _, c := range report.Checks {
		if c.Status == "fail" {
			report.Overall = "unhealthy"
			code = exitcode.Err
			break
		}
	}
	if code == exitcode.OK {
		for _, c := range report.Checks {
			if c.Required && c.Status == "unknown" {
				report.Overall = "unmeasurable"
				code = exitcode.Usage // DIST-24-04's command-specific rc=2 contract.
				break
			}
		}
	}
	return report, code, nil
}

func doctorUsage(message string) error {
	return exitcode.New(exitcode.Usage, errors.New(message))
}

func detectDoctorInit(deps doctorDeps) string {
	if deps.goos == "darwin" {
		return "launchd"
	}
	if _, err := deps.lookPath("systemctl"); err == nil {
		if info, serr := deps.stat("/run/systemd/system"); serr == nil && info.IsDir() {
			return "systemd"
		}
	}
	if _, err := deps.lookPath("rc-service"); err == nil {
		return "openrc"
	}
	return "unknown"
}

func resolveDoctorPaths(o *doctorOptions, deps doctorDeps) error {
	if o.binary == "" {
		path, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve current executable: %w", err)
		}
		o.binary = path
	}
	if !filepath.IsAbs(o.binary) {
		return doctorUsage("--binary must be absolute")
	}
	home := ""
	if o.mode == "user" {
		var err error
		home, err = deps.homeDir()
		if err != nil || !filepath.IsAbs(home) {
			return doctorUsage("cannot resolve an absolute home for --mode user")
		}
	}
	if o.dataDir == "" {
		if o.mode == "system" {
			if deps.goos == "darwin" {
				o.dataDir = "/Library/Application Support/Olivares"
			} else {
				o.dataDir = "/var/lib/olivares"
			}
		} else {
			root := deps.getenv("XDG_DATA_HOME")
			if !filepath.IsAbs(root) {
				root = filepath.Join(home, ".local", "share")
			}
			o.dataDir = filepath.Join(root, "olivares")
		}
	}
	if o.config == "" {
		if o.mode == "system" {
			if deps.goos == "darwin" {
				o.config = "/Library/Preferences/dev.olivares.olivares.env"
			} else {
				o.config = "/etc/olivares/olivares.env"
			}
		} else {
			root := deps.getenv("XDG_CONFIG_HOME")
			if !filepath.IsAbs(root) {
				root = filepath.Join(home, ".config")
			}
			o.config = filepath.Join(root, "olivares", "olivares.env")
		}
	}
	if o.unit == "" && o.init != "unknown" {
		switch o.init + ":" + o.mode {
		case "systemd:system":
			o.unit = "/etc/systemd/system/olivares.service"
		case "systemd:user":
			root := deps.getenv("XDG_CONFIG_HOME")
			if !filepath.IsAbs(root) {
				root = filepath.Join(home, ".config")
			}
			o.unit = filepath.Join(root, "systemd", "user", "olivares.service")
		case "openrc:system":
			o.unit = "/etc/init.d/olivares"
		case "launchd:system":
			o.unit = "/Library/LaunchDaemons/dev.olivares.olivares.plist"
		case "launchd:user":
			o.unit = filepath.Join(home, "Library", "LaunchAgents", "dev.olivares.olivares.plist")
		}
	}
	paths := []struct{ name, path string }{
		{"--binary", o.binary}, {"--data-dir", o.dataDir}, {"--config", o.config},
		{"--unit", o.unit}, {"--ca-cert", o.caCert},
	}
	for _, item := range paths {
		name, path := item.name, item.path
		if path != "" && !filepath.IsAbs(path) {
			return doctorUsage(name + " must be absolute")
		}
		if strings.ContainsAny(path, "\r\n\t") {
			return doctorUsage(name + " must not contain control characters")
		}
	}
	if strings.ContainsAny(o.auditTenant, "\r\n\t") {
		return doctorUsage("--audit-tenant must not contain control characters")
	}
	u, err := url.Parse(o.server)
	if err != nil || u.Scheme != "https" || u.Host == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return doctorUsage("--server must be an https origin without credentials, path, query or fragment")
	}
	return nil
}

func doctorAnchor(origin, fingerprint string) string {
	if fingerprint == "" {
		return origin
	}
	return origin + "/" + fingerprint
}

func doctorBuildCheck(licKey, otaKey string) doctorCheck {
	c := doctorCheck{Name: "build-and-anchors", Status: "pass", Required: true,
		Detail: fmt.Sprintf("version=%s commit=%s license-key=%s ota-key=%s", version, commit, licKey, otaKey)}
	if strings.HasPrefix(licKey, "misconfigured") || strings.HasPrefix(otaKey, "misconfigured") {
		c.Status, c.Remediation = "fail", "install a release built with valid, independent verification anchors"
		return c
	}
	if keyDomainCollisionWarning() != "" {
		c.Status, c.Remediation = "fail", "replace the build: license and OTA trust domains must not share a key"
		return c
	}
	if version != "dev" && (commit == "none" || release.KeyOrigin() == "none") {
		c.Status, c.Remediation = "fail", "replace the unstamped or OTA-anchorless release binary"
	}
	return c
}

func doctorFileCheck(deps doctorDeps, name, path string, required bool, modes []fs.FileMode) doctorCheck {
	c := doctorCheck{Name: name, Required: required, Detail: path}
	info, err := deps.stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.Status, c.Remediation = "fail", "restore the installed file from the signed release"
		} else {
			c.Status, c.Remediation = "unknown", "make the path stat-readable and rerun doctor"
		}
		return c
	}
	if !info.Mode().IsRegular() {
		c.Status, c.Remediation = "fail", "replace the path with the expected regular file"
		return c
	}
	mode := info.Mode().Perm()
	c.Detail = fmt.Sprintf("%s mode=%04o", path, mode)
	for _, want := range modes {
		if mode == want {
			c.Status = "pass"
			return c
		}
	}
	c.Status, c.Remediation = "fail", "restore the documented owner and mode with the pinned installer"
	return c
}

func doctorDirCheck(deps doctorDeps, path string, modes []fs.FileMode) doctorCheck {
	c := doctorCheck{Name: "data-directory", Required: true, Detail: path}
	info, err := deps.stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.Status, c.Remediation = "fail", "create it through the pinned service installer"
		} else {
			c.Status, c.Remediation = "unknown", "make the path stat-readable and rerun doctor"
		}
		return c
	}
	if !info.IsDir() {
		c.Status, c.Remediation = "fail", "replace the path with the expected directory"
		return c
	}
	mode := info.Mode().Perm()
	c.Detail = fmt.Sprintf("%s mode=%04o", path, mode)
	for _, want := range modes {
		if mode == want {
			c.Status = "pass"
			return c
		}
	}
	c.Status, c.Remediation = "fail", "remove group/world write access; user mode requires 0700 and system mode 0700 or 0750"
	return c
}

func doctorConfigCheck(deps doctorDeps, path string, modes []fs.FileMode) doctorCheck {
	c := doctorFileCheck(deps, "configuration", path, true, modes)
	if c.Status != "pass" {
		return c
	}
	body, err := deps.readFile(path)
	if err != nil {
		c.Status, c.Remediation = "unknown", "make the env file readable to this diagnostic"
		return c
	}
	keys, err := doctorConfigKeys(body)
	if err != nil {
		c.Status, c.Remediation = "fail", err.Error()
		return c
	}
	unknown := unknownConfigEnvKeys(keys)
	if len(unknown) > 0 {
		c.Status = "fail"
		c.Detail += "; unknown keys=" + strings.Join(unknown, ",")
		c.Remediation = "remove or correct the named keys; values were not emitted"
		return c
	}
	c.Detail += fmt.Sprintf("; recognized_keys=%d (values redacted)", len(keys))
	return c
}

func doctorConfigKeys(body []byte) ([]string, error) {
	var out []string
	for i, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("configuration line %d is not KEY=value (value redacted)", i+1)
		}
		key = strings.TrimSpace(key)
		if key == "OLIVARES_EXTRA_ARGS" {
			continue
		}
		if !strings.HasPrefix(key, "OLIVARES_") || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("configuration line %d has an invalid key name", i+1)
		}
		out = append(out, key+"=")
	}
	return out, nil
}

func doctorManifestCheck(deps doctorDeps, path string, o doctorOptions) doctorCheck {
	c := doctorCheck{Name: "install-manifest", Required: true, Detail: path}
	body, err := deps.readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.Status, c.Remediation = "fail", "rerun the pinned service installer to record ownership"
		} else {
			c.Status, c.Remediation = "unknown", "make the manifest readable and rerun doctor"
		}
		return c
	}
	var value doctorManifest
	if err := json.Unmarshal(body, &value); err != nil {
		c.Status, c.Remediation = "fail", "replace the malformed manifest by rerunning the pinned installer"
		return c
	}
	paths := map[string]bool{}
	for _, f := range value.Files {
		paths[f.Path] = true
	}
	wrapperOK := true
	if o.init == "launchd" {
		wrapperOK = paths[filepath.Join(o.dataDir, "launchd-run.sh")]
	}
	// The manifest names the data directory it lives in; a record that claims
	// another directory is measured as drift, whatever layout it declares.
	if value.Schema != "olivares.ai/local-install/v2" || value.Mode != o.mode || value.Init != o.init ||
		value.DataDir != o.dataDir ||
		!paths[o.binary] || !paths[o.config] || (o.unit != "" && !paths[o.unit]) || !wrapperOK {
		c.Status, c.Remediation = "fail", "rerun the pinned installer; manifest and measured installation disagree"
		return c
	}
	c.Status = "pass"
	layout := value.Layout
	if layout == "" {
		layout = "default"
	}
	c.Detail += fmt.Sprintf(" files=%d layout=%s", len(value.Files), layout)
	return c
}

// doctorManifest is the read-only view of the v2 ownership record that doctor
// needs; the strict decoder and layout policy live in internal/localinstall.
type doctorManifest struct {
	Schema       string `json:"schema"`
	Mode         string `json:"mode"`
	Init         string `json:"init"`
	Layout       string `json:"layout"`
	DataDir      string `json:"data_dir"`
	WorkspaceDir string `json:"workspace_dir"`
	Files        []struct {
		Path    string `json:"path"`
		Role    string `json:"role"`
		Mode    string `json:"mode"`
		Managed bool   `json:"managed"`
	} `json:"files"`
}

// doctorAgentOpsCheck measures the native AgentOps layout the manifest records:
// the managed drop-in must exist with its mode and must name the recorded
// claude HOME, token dir and workspace; the runtime env must exist with a
// config mode (its values are never read); the workspace must be a directory.
// A manifest that records none of them makes the check not applicable.
func doctorAgentOpsCheck(deps doctorDeps, o doctorOptions, manifest string, envModes []fs.FileMode) doctorCheck {
	c := doctorCheck{Name: "agentops-layout", Required: false}
	body, err := deps.readFile(manifest)
	if err != nil {
		c.Status, c.Detail = "not_applicable", "no readable manifest (see install-manifest)"
		return c
	}
	var value doctorManifest
	if err := json.Unmarshal(body, &value); err != nil {
		c.Status, c.Detail = "not_applicable", "malformed manifest (see install-manifest)"
		return c
	}
	var dropin, runtimeEnv string
	for _, f := range value.Files {
		switch f.Role {
		case "dropin":
			if f.Managed {
				dropin = f.Path
			}
		case "runtime-env":
			runtimeEnv = f.Path
		}
	}
	if dropin == "" && runtimeEnv == "" && value.WorkspaceDir == "" {
		c.Status, c.Detail = "not_applicable", "manifest records no AgentOps files"
		return c
	}
	c.Required = true
	var parts []string
	if dropin != "" {
		fc := doctorFileCheck(deps, c.Name, dropin, true, []fs.FileMode{0o644})
		if fc.Status != "pass" {
			return fc
		}
		dropinBody, err := deps.readFile(dropin)
		if err != nil {
			c.Status, c.Detail, c.Remediation = "unknown", dropin, "make the managed drop-in readable and rerun doctor"
			return c
		}
		tokens := []string{filepath.Join(o.dataDir, "claude-home"), filepath.Join(o.dataDir, "run")}
		if value.WorkspaceDir != "" {
			tokens = append(tokens, value.WorkspaceDir)
		}
		for _, token := range tokens {
			if !bytesContain(dropinBody, token) {
				c.Status, c.Detail = "fail", fmt.Sprintf("%s does not reference %s", dropin, token)
				c.Remediation = "rerun install-agentops.sh; the managed drop-in and the recorded layout disagree"
				return c
			}
		}
		parts = append(parts, "dropin="+dropin)
	}
	if runtimeEnv != "" {
		fc := doctorFileCheck(deps, c.Name, runtimeEnv, true, envModes)
		if fc.Status != "pass" {
			return fc
		}
		parts = append(parts, "runtime-env="+runtimeEnv+" (values not read)")
	}
	if value.WorkspaceDir != "" {
		info, err := deps.stat(value.WorkspaceDir)
		switch {
		case err != nil && errors.Is(err, fs.ErrNotExist):
			c.Status, c.Detail, c.Remediation = "fail", "workspace "+value.WorkspaceDir+" is absent", "recreate the recorded workspace or rerun install-agentops.sh with OLIVARES_WORKSPACE_DIR"
			return c
		case err != nil:
			c.Status, c.Detail, c.Remediation = "unknown", "workspace "+value.WorkspaceDir, "make the workspace stat-readable and rerun doctor"
			return c
		case !info.IsDir():
			c.Status, c.Detail, c.Remediation = "fail", "workspace "+value.WorkspaceDir+" is not a directory", "replace the path with the recorded workspace directory"
			return c
		}
		parts = append(parts, fmt.Sprintf("workspace=%s mode=%04o", value.WorkspaceDir, info.Mode().Perm()))
	}
	c.Status, c.Detail = "pass", strings.Join(parts, "; ")
	return c
}

func doctorUnitCheck(deps doctorDeps, o doctorOptions, modes []fs.FileMode) doctorCheck {
	c := doctorFileCheck(deps, "service-unit", o.unit, true, modes)
	if c.Status != "pass" {
		return c
	}
	body, err := deps.readFile(o.unit)
	if err != nil {
		c.Status, c.Remediation = "unknown", "make the service definition readable and rerun doctor"
		return c
	}
	// The data directory is read from the execution directive with the same
	// parser the uninstaller trusts (ExecStart= inside [Service], command_args=
	// beside its command=, ProgramArguments), and only when that directive runs
	// the measured program: a mention in a comment, in another section or on
	// another program's command line is drift, not agreement.
	program := o.binary
	if o.init == "launchd" {
		program = filepath.Join(o.dataDir, "launchd-run.sh")
	}
	if dir, perr := localinstall.UnitDataDir(o.init, string(body), program); perr != nil || dir != o.dataDir {
		c.Status, c.Remediation = "fail", "rerun the pinned installer; the service definition does not execute the engine with the measured data directory"
		return c
	}
	var required []string
	if o.init == "launchd" {
		wrapper := filepath.Join(o.dataDir, "launchd-run.sh")
		wc := doctorFileCheck(deps, "launchd-wrapper", wrapper, true, []fs.FileMode{0o755})
		if wc.Status != "pass" {
			wc.Name = "service-unit"
			return wc
		}
		wrapperBody, rerr := deps.readFile(wrapper)
		if rerr != nil {
			c.Status, c.Remediation = "unknown", "make the launchd wrapper readable and rerun doctor"
			return c
		}
		for _, token := range []string{o.binary, o.config, o.dataDir, "127.0.0.1:8443"} {
			if !bytesContain(wrapperBody, token) {
				c.Status, c.Remediation = "fail", "rerun the pinned installer; launchd wrapper paths or listener drifted"
				return c
			}
		}
	} else {
		required = append(required, o.binary, o.config, "127.0.0.1:8443")
	}
	for _, token := range required {
		if !bytesContain(body, token) {
			c.Status, c.Remediation = "fail", "rerun the pinned installer; service definition paths or listener drifted"
			return c
		}
	}
	return c
}

func bytesContain(body []byte, token string) bool {
	return strings.Contains(string(body), token)
}

func doctorAccountCheck(deps doctorDeps, o doctorOptions) (doctorCheck, int) {
	if o.mode == "user" {
		uid := deps.euid()
		return doctorCheck{Name: "service-account", Status: "pass", Required: true,
			Detail: fmt.Sprintf("invoking uid=%d", uid)}, uid
	}
	name := "olivares"
	if deps.goos == "darwin" {
		name = "_olivares"
	}
	c := doctorCheck{Name: "service-account", Required: true, Detail: name}
	uid, err := deps.lookupUID(name)
	if err != nil {
		var unknownUser user.UnknownUserError
		if errors.As(err, &unknownUser) {
			c.Status, c.Remediation = "fail", "create the no-login account through the pinned service installer"
			return c, -1
		}
		c.Status, c.Remediation = "unknown", "make the local account database readable and rerun doctor"
		return c, -1
	}
	c.Status = "pass"
	c.Detail += fmt.Sprintf(" uid=%d", uid)
	return c, uid
}

func doctorOwnershipCheck(deps doctorDeps, o doctorOptions, manifest string, accountUID int) doctorCheck {
	c := doctorCheck{Name: "ownership", Required: true}
	if accountUID < 0 {
		c.Status, c.Detail, c.Remediation = "unknown", "service account uid unavailable", "repair the service account and rerun doctor"
		return c
	}
	type ownerExpectation struct {
		path string
		uid  int
	}
	expected := []ownerExpectation{{path: o.dataDir, uid: accountUID}}
	if o.mode == "system" {
		expected = append(expected,
			ownerExpectation{path: o.config, uid: 0},
			ownerExpectation{path: o.unit, uid: 0},
			ownerExpectation{path: manifest, uid: 0})
		if o.init == "launchd" {
			expected = append(expected, ownerExpectation{path: filepath.Join(o.dataDir, "launchd-run.sh"), uid: 0})
		}
	} else {
		expected = append(expected,
			ownerExpectation{path: o.config, uid: accountUID},
			ownerExpectation{path: o.unit, uid: accountUID},
			ownerExpectation{path: manifest, uid: accountUID})
		if o.init == "launchd" {
			expected = append(expected, ownerExpectation{path: filepath.Join(o.dataDir, "launchd-run.sh"), uid: accountUID})
		}
	}
	parts := make([]string, 0, len(expected))
	for _, item := range expected {
		info, err := deps.stat(item.path)
		if err != nil {
			c.Status, c.Detail, c.Remediation = "unknown", "owner unavailable for "+item.path, "make every installed path stat-readable"
			return c
		}
		got, ok := doctorFileUID(info)
		if !ok {
			c.Status, c.Detail, c.Remediation = "unknown", "platform did not expose uid ownership", "rerun on a supported Linux/macOS host"
			return c
		}
		parts = append(parts, fmt.Sprintf("%s=%d", filepath.Base(item.path), got))
		if int(got) != item.uid {
			c.Status, c.Detail, c.Remediation = "fail", strings.Join(parts, ","), "restore documented ownership with the pinned installer; no recursive repair was attempted"
			return c
		}
	}
	c.Status, c.Detail = "pass", strings.Join(parts, ",")
	return c
}

func doctorInitCheck(ctx context.Context, deps doctorDeps, o doctorOptions) doctorCheck {
	c := doctorCheck{Name: "init-state", Required: true, Detail: o.init + "/" + o.mode}
	if o.init == "unknown" {
		c.Status, c.Remediation = "unknown", "pass --init after confirming a supported service manager"
		return c
	}
	var name string
	var args []string
	switch o.init + ":" + o.mode {
	case "systemd:system":
		name, args = "systemctl", []string{"is-active", "olivares"}
	case "systemd:user":
		name, args = "systemctl", []string{"--user", "is-active", "olivares"}
	case "openrc:system":
		name, args = "rc-service", []string{"olivares", "status"}
	case "launchd:system":
		name, args = "launchctl", []string{"print", "system/dev.olivares.olivares"}
	case "launchd:user":
		name, args = "launchctl", []string{"print", fmt.Sprintf("gui/%d/dev.olivares.olivares", deps.euid())}
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	rc, err := deps.run(ctx, name, args...)
	switch {
	case err != nil:
		c.Status, c.Remediation = "unknown", "make the selected service manager executable and rerun doctor"
	case rc != 0:
		c.Status, c.Remediation = "fail", "review configuration, then start the service explicitly"
	default:
		c.Status = "pass"
	}
	return c
}

func doctorHTTPGet(ctx context.Context, rawURL, caCert string, timeout time.Duration) (int, []byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if u.Scheme == "https" {
		pem, rerr := os.ReadFile(caCert)
		if rerr != nil {
			return 0, nil, fmt.Errorf("read CA certificate: %w", rerr)
		}
		pool, perr := x509.SystemCertPool()
		if perr != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return 0, nil, errors.New("CA certificate contains no PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	// cli-transport-exempt: `olivares doctor` probes the LOCAL first-boot listener (/livez, /readyz,
	// /status over loopback TLS) with the CA file the operator names; it carries no operator
	// token, resolves no endpoint and must not inherit the control-plane transport, its proxies
	// or its headers — the same class as the public release-channel reads above.
	client := &http.Client{Transport: transport, Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

func doctorProbe(ctx context.Context, deps doctorDeps, o doctorOptions, caCert, path string, initActive bool) doctorCheck {
	c := doctorCheck{Name: strings.TrimPrefix(path, "/"), Required: true, Detail: o.server + path}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	rc, _, err := deps.httpGet(ctx, strings.TrimRight(o.server, "/")+path, caCert, o.timeout)
	if err != nil {
		if initActive {
			c.Status, c.Remediation = "fail", "the init manager says active but the TLS endpoint is unreachable; inspect service logs and certificate paths"
		} else {
			c.Status, c.Remediation = "unknown", "start the service and make its local TLS certificate readable"
		}
		return c
	}
	c.Detail += " status=" + strconv.Itoa(rc)
	if rc != http.StatusOK {
		c.Status, c.Remediation = "fail", "inspect service logs; this endpoint must return HTTP 200"
	} else {
		c.Status = "pass"
	}
	return c
}

func doctorStoreCheck(ctx context.Context, deps doctorDeps, o doctorOptions, caCert string, initActive bool) doctorCheck {
	c := doctorCheck{Name: "store", Required: true, Detail: o.server + "/status"}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	rc, body, err := deps.httpGet(ctx, strings.TrimRight(o.server, "/")+"/status", caCert, o.timeout)
	if err != nil {
		if initActive {
			c.Status, c.Remediation = "fail", "the active service did not expose a readable public status"
		} else {
			c.Status, c.Remediation = "unknown", "start the service, then rerun doctor"
		}
		return c
	}
	if rc != http.StatusOK {
		c.Status, c.Remediation = "fail", "GET /status must return HTTP 200"
		return c
	}
	var status statusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		c.Status, c.Remediation = "fail", "the public status response is not valid JSON"
		return c
	}
	for _, component := range status.Components {
		if component.Name != "store" {
			continue
		}
		c.Detail += " status=" + component.Status
		if component.Status == statusOperational {
			c.Status = "pass"
		} else {
			c.Status, c.Remediation = "fail", "repair the store named by the engine status before admitting traffic"
		}
		return c
	}
	c.Status, c.Remediation = "fail", "the public status omitted its required store component"
	return c
}

func doctorLicenseCheck(dataDir string) doctorCheck {
	c := doctorCheck{Name: "license", Required: false}
	src, err := resolveLicense("", dataDir, osGetenv)
	if err != nil {
		c.Status, c.Required, c.Detail, c.Remediation = "fail", true, "configured license source is unreadable", "repair or remove the named license source"
		return c
	}
	c.Detail = "source=" + src.Kind
	if src.Blob == "" {
		c.Status = "not_applicable"
		return c
	}
	c.Required = true
	kr, err := licenseKeyringForDataDir(dataDir)
	if err != nil {
		c.Status, c.Remediation = "fail", "repair or remove "+licenseTrustFileName+" in the data directory; its error details are suppressed"
		return c
	}
	if kr.Len() == 0 {
		c.Status, c.Remediation = "unknown", "use a release binary carrying the expected license verification anchor, or configure it with `olivares license trust set`"
		return c
	}
	if _, err := kr.Verify(src.Blob, time.Now().UTC()); err != nil {
		c.Status, c.Remediation = "fail", "replace the installed license with a credential this deployment's license trust accepts; blob and error details are suppressed"
		return c
	}
	c.Status = "pass"
	return c
}

func doctorAuditCheck(ctx context.Context, deps doctorDeps, o doctorOptions) doctorCheck {
	c := doctorCheck{Name: "audit-chain", Required: false}
	if strings.TrimSpace(o.auditTenant) == "" {
		c.Status, c.Detail = "not_applicable", "not requested; pass --audit-tenant for a strict local verification"
		return c
	}
	c.Required, c.Detail = true, "tenant="+o.auditTenant+" (report suppressed)"
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	rc, err := deps.run(ctx, o.binary, "audit", "verify", "--data-dir", o.dataDir,
		"--tenant", o.auditTenant, "--strict", "-o", "json")
	switch {
	case err != nil:
		c.Status, c.Remediation = "unknown", "make the installed binary executable and rerun the audit check"
	case rc != 0:
		c.Status, c.Remediation = "fail", "run the same audit verify command interactively to inspect its redacted report"
	default:
		c.Status = "pass"
	}
	return c
}

func doctorChannelCheck(ctx context.Context, deps doctorDeps, o doctorOptions) doctorCheck {
	c := doctorCheck{Name: "update-channel", Required: false}
	if !o.checkUpdates {
		c.Status, c.Detail = "not_applicable", "not requested; pass --check-updates to verify the signed channel"
		return c
	}
	c.Required = true
	c.Detail = "signed stable channel; subprocess errors suppressed"
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	rc, body, err := deps.runOutput(ctx, o.binary, "upgrade", "--check", "--target", o.binary,
		"--data-dir", o.dataDir, "--timeout", o.timeout.String(), "-o", "json")
	switch {
	case err != nil:
		c.Status, c.Remediation = "unknown", "restore network/DNS access or run without --check-updates in an air gap"
	case rc != 0:
		c.Status, c.Remediation = "fail", "run olivares upgrade --check interactively to inspect the signed-channel refusal"
	default:
		var result struct {
			Status    string `json:"status"`
			Current   string `json:"current"`
			Available string `json:"available"`
		}
		if json.Unmarshal(body, &result) != nil || result.Current == "" || result.Available == "" {
			c.Status, c.Remediation = "fail", "the signed-channel check did not return its version comparison as JSON"
			break
		}
		if _, err := release.ParseVersion(result.Current); err != nil {
			c.Status, c.Remediation = "fail", "the signed-channel check returned an invalid installed version"
			break
		}
		if _, err := release.ParseVersion(result.Available); err != nil {
			c.Status, c.Remediation = "fail", "the signed-channel check returned an invalid available version"
			break
		}
		switch result.Status {
		case upgradeStatusUpToDate:
			if result.Current != result.Available {
				c.Status, c.Remediation = "fail", "the channel called unequal versions up-to-date"
				break
			}
			c.Status = "pass"
		case upgradeStatusAvailable:
			if result.Current == result.Available {
				c.Status, c.Remediation = "fail", "the channel called equal versions an upgrade"
				break
			}
			c.Status = "pass"
		default:
			c.Status, c.Remediation = "fail", "the signed-channel check returned an unsupported ordering verdict"
		}
		if c.Status == "pass" {
			c.Detail = fmt.Sprintf("current=%s available=%s status=%s", result.Current, result.Available, result.Status)
		}
	}
	return c
}

func renderDoctor(cmd *cobra.Command, report doctorReport) error {
	return renderOut(cmd, func(out io.Writer) error {
		tw := newTabWriter(out)
		fmt.Fprintf(tw, "OVERALL\t%s\nMODE\t%s\nINIT\t%s\n", report.Overall, report.Mode, report.Init)
		fmt.Fprintf(tw, "VERSION\t%s\nCOMMIT\t%s\n", report.Build.Version, report.Build.Commit)
		fmt.Fprintln(tw, "\nCHECK\tSTATUS\tREQUIRED\tDETAIL\tREMEDIATION")
		for _, check := range report.Checks {
			fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\n", check.Name, check.Status, check.Required,
				check.Detail, check.Remediation)
		}
		return tw.Flush()
	}, report)
}

// doctorFileUID is deliberately small and Linux/macOS-only, matching the service
// adapters. Kept for ownership-aware extensions without ever printing user data.
func doctorFileUID(info fs.FileInfo) (uint32, bool) {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return 0, false
	}
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	uid := value.FieldByName("Uid")
	if !uid.IsValid() || !uid.CanUint() {
		return 0, false
	}
	return uint32(uid.Uint()), true
}
