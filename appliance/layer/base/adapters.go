// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

// Paths the Linux adapters read and write, relative to Host.Root.
const (
	productBinary  = "/usr/bin/olivares"
	productEnvFile = "/etc/olivares/olivares.env"
	productUnit    = "olivares.service"
	productAccount = "olivares"
	productDropIn  = "/etc/systemd/system/olivares.service.d/50-olivares-appliance.conf"
	readinessURL   = "https://127.0.0.1:8443/readyz"
	// machineIDKey keys the machine ID digest, as machine-id(5) asks of applications.
	machineIDKey = "olivares-appliance-firstboot/machine-id"
)

// Host is the installation the Linux adapters observe and change.
type Host struct {
	Root string // "/" on an installed appliance
	Run  carriers.Runner
}

func (h Host) path(p string) string { return filepath.Join(h.Root, p) }

// verifyAgain gives an observing stage the same comparison on restart.
func verifyAgain(ctx context.Context, observe func(context.Context, Input) (Effect, error), in Input, recorded Effect, changed string) error {
	got, err := observe(ctx, in)
	if err != nil {
		return err
	}
	if got != recorded {
		return Refuse(changed)
	}
	return nil
}

// OSIdentity observes the identities the operating system's owners created for this
// instance, the machine ID (systemd) and the SSH host keys (cloud-init or OpenSSH), and
// records keyed digests of them. It never generates or rewrites an identity.
type OSIdentity struct{ Host Host }

// Apply records the instance identity.
func (s OSIdentity) Apply(ctx context.Context, in Input) (Effect, error) { return s.observe(ctx, in) }

// Verify refuses an identity that changed after it was recorded.
func (s OSIdentity) Verify(ctx context.Context, in Input, recorded Effect) error {
	return verifyAgain(ctx, s.observe, in, recorded,
		"the instance identity changed after first boot recorded it; it is never regenerated automatically")
}

func (s OSIdentity) observe(context.Context, Input) (Effect, error) {
	raw, err := os.ReadFile(s.Host.path("/etc/machine-id"))
	id := strings.TrimSpace(string(raw))
	if err != nil || len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", Refuse("the machine ID is not initialized; systemd owns it")
	}
	keys, err := filepath.Glob(s.Host.path("/etc/ssh/ssh_host_*_key.pub"))
	if err != nil || len(keys) == 0 {
		return "", Refuse("no SSH host key exists; the host-settings owner creates them")
	}
	sort.Strings(keys)
	fingerprints := sha256.New()
	for _, key := range keys {
		public, err := os.ReadFile(key)
		if err != nil {
			return "", Refuse("an SSH host public key cannot be read")
		}
		fingerprints.Write(bytes.TrimSpace(public))
		fingerprints.Write([]byte{'\n'})
	}
	machine := hmac.New(sha256.New, []byte(machineIDKey))
	machine.Write([]byte(id))
	return Effect(fmt.Sprintf("machine-id-hmac=%x ssh-host-keys=%d/%x",
		machine.Sum(nil)[:8], len(keys), fingerprints.Sum(nil)[:8])), nil
}

// CloudInitHost waits for cloud-init, the single owner of host settings, and verifies what
// it applied. It never applies a host setting.
type CloudInitHost struct{ Host Host }

// Apply verifies the host settings and records what was verified.
func (s CloudInitHost) Apply(ctx context.Context, in Input) (Effect, error) {
	return s.observe(ctx, in)
}

// Verify refuses host settings that changed after they were verified.
func (s CloudInitHost) Verify(ctx context.Context, in Input, recorded Effect) error {
	return verifyAgain(ctx, s.observe, in, recorded, "the host settings changed after first boot verified them; "+
		"run `appliance-firstboot reconcile` to verify them again before the product starts")
}

func (s CloudInitHost) observe(ctx context.Context, in Input) (Effect, error) {
	out, err := s.Host.Run(ctx, "cloud-init", "status", "--format", "json")
	if errors.Is(err, exec.ErrNotFound) {
		return "", Refuse("the declared host owner cloud-init is not installed")
	}
	// Exit 2 reports recoverable errors after completion; the status is still printed.
	var status struct {
		Status string `json:"status"`
		Errors []any  `json:"errors"`
	}
	if len(out) == 0 || json.Unmarshal(out, &status) != nil {
		return "", Refuse("the cloud-init status cannot be read")
	}
	switch {
	case status.Status == "running" || status.Status == "not started" || status.Status == "not run":
		return "", Wait("cloud-init has not completed")
	case status.Status == "disabled":
		return "", Refuse("cloud-init, the declared host owner, is disabled")
	case status.Status != "done" || len(status.Errors) > 0:
		return "", Refuse("cloud-init reported errors; its own log names them")
	}
	hostname, err := os.ReadFile(s.Host.path("/proc/sys/kernel/hostname"))
	if err != nil || strings.ToLower(strings.TrimSpace(string(hostname))) != in.Answers.Hostname {
		return "", Refuse("the kernel hostname is not host.hostname; cloud-init owns it")
	}
	if !s.utc() {
		return "", Refuse("the host time zone is not UTC; cloud-init owns it")
	}
	authorized, err := s.authorizedKeys()
	if err != nil {
		return "", Refuse("the authorized SSH keys cannot be read")
	}
	for _, key := range in.Answers.SSHAuthorizedKeys {
		if !authorized[key] {
			return "", Refuse("a declared SSH public key is not authorized for any account; cloud-init owns them")
		}
	}
	return Effect(fmt.Sprintf("cloud-init done; hostname, UTC and %d SSH key(s) verified; "+
		"network mode and time servers delegated to cloud-init", len(in.Answers.SSHAuthorizedKeys))), nil
}

func (s CloudInitHost) utc() bool {
	target, err := os.Readlink(s.Host.path("/etc/localtime"))
	if errors.Is(err, os.ErrNotExist) {
		return true // no zone file: the C library and systemd use UTC
	}
	return err == nil && (strings.HasSuffix(target, "zoneinfo/UTC") || strings.HasSuffix(target, "zoneinfo/Etc/UTC"))
}

// authorizedKeys returns every "type base64" pair authorized for root or a home account.
func (s CloudInitHost) authorizedKeys() (map[string]bool, error) {
	files, err := filepath.Glob(s.Host.path("/home/*/.ssh/authorized_keys"))
	if err != nil {
		return nil, err
	}
	files = append(files, s.Host.path("/root/.ssh/authorized_keys"))
	keys := map[string]bool{}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		lines := bufio.NewScanner(bytes.NewReader(data))
		for lines.Scan() {
			fields := strings.Fields(lines.Text())
			for i := 0; i+1 < len(fields); i++ {
				if fields[i] == "ssh-ed25519" {
					keys[fields[i]+" "+fields[i+1]] = true
				}
			}
		}
	}
	return keys, nil
}

// ProductConfig has the product's own generator write the product configuration file, the
// generator's documented output, and declares the public console address, an environment key
// the product's unit reads, in a drop-in the appliance owns. The appliance itself writes no
// file the product package installed.
type ProductConfig struct{ Host Host }

// Apply generates the configuration. The generator is deterministic, so a run interrupted
// before its record was persisted writes the same file again.
func (s ProductConfig) Apply(ctx context.Context, in Input) (Effect, error) {
	if _, err := os.Stat(s.Host.path(productBinary)); err != nil {
		return "", Refuse("the product is not installed")
	}
	if in.Answers.StorageProfile != "single-node-prod" {
		return "", Refuse("the storage profile has no product configuration adapter")
	}
	if _, err := s.Host.Run(ctx, s.Host.path(productBinary), "config", "generate", "--profile", "single-node-prod",
		"--data-dir", ProductDataDir, "--out", productEnvFile, "--force"); err != nil {
		return "", Refuse("the product configuration generator failed")
	}
	// The package ships the directory 0755; creating it here would inherit the unit's UMask=0077.
	dropIn := s.Host.path(productDropIn)
	if info, err := os.Stat(filepath.Dir(dropIn)); err != nil || !info.IsDir() {
		return "", Refuse("the product drop-in directory is missing; olivares-appliance-base ships it")
	}
	content := "# Written by the appliance first boot from product.public_console_url.\n" +
		"[Service]\nEnvironment=OLIVARES_PUBLIC_URL=" + in.Answers.PublicConsoleURL + "\n"
	if err := writeAtomic(filepath.Dir(dropIn), filepath.Base(dropIn), []byte(content), 0o644); err != nil {
		return "", Refuse("the product drop-in cannot be written")
	}
	if _, err := s.Host.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return "", Refuse("systemd could not reload its unit files")
	}
	return s.observe(ctx, in)
}

// Verify refuses a configuration that changed after first boot generated it.
func (s ProductConfig) Verify(ctx context.Context, in Input, recorded Effect) error {
	return verifyAgain(ctx, s.observe, in, recorded,
		"the product configuration changed after first boot generated it; "+
			"run `appliance-firstboot reconcile` to generate it again before the product starts")
}

func (s ProductConfig) observe(context.Context, Input) (Effect, error) {
	env, err := os.ReadFile(s.Host.path(productEnvFile))
	if err != nil {
		return "", Refuse("the product configuration cannot be read")
	}
	dropIn, err := os.ReadFile(s.Host.path(productDropIn))
	if err != nil {
		return "", Refuse("the product drop-in cannot be read")
	}
	envSum, dropInSum := sha256.Sum256(env), sha256.Sum256(dropIn)
	return Effect(fmt.Sprintf("olivares.env=%x drop-in=%x", envSum[:8], dropInSum[:8])), nil
}

// Storage verifies the storage of the single-node profile. Its initialization mechanism is
// the product's own first start, which creates the SQLite store in the data directory the
// product package created. This stage changes nothing and records only what it measured:
// the directory. Readiness measures the store once the product has started.
type Storage struct{ Host Host }

// Apply verifies the data directory the product will initialize.
func (s Storage) Apply(ctx context.Context, in Input) (Effect, error) { return s.observe(ctx, in) }

// Verify refuses a data directory that changed owner or disappeared.
func (s Storage) Verify(ctx context.Context, in Input, recorded Effect) error {
	return verifyAgain(ctx, s.observe, in, recorded, "the product data directory changed after first boot verified it; "+
		"run `appliance-firstboot reconcile` to verify it again before the product starts")
}

func (s Storage) observe(ctx context.Context, _ Input) (Effect, error) {
	info, err := os.Lstat(s.Host.path(ProductDataDir))
	if err != nil || !info.IsDir() {
		return "", Refuse("the product data directory is missing")
	}
	uid, err := s.Host.Run(ctx, "id", "-u", productAccount)
	if err != nil {
		return "", Refuse("the product service account is missing")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(st.Uid), 10) != strings.TrimSpace(string(uid)) || info.Mode().Perm()&0o007 != 0 {
		return "", Refuse("the product data directory is not private to the product service account")
	}
	return Effect(fmt.Sprintf("single-node-prod: %s owned by the service account (uid %s), mode %04o; "+
		"the product's first start creates the store and readiness measures it",
		ProductDataDir, strings.TrimSpace(string(uid)), uint32(info.Mode().Perm()))), nil
}

// ProductService enables and starts the product unit without waiting for it: the first-boot
// unit queues the start, and the readiness unit, ordered after the product, measures it.
type ProductService struct{ Host Host }

// Apply enables the product unit and queues its start.
func (s ProductService) Apply(ctx context.Context, _ Input) (Effect, error) {
	if _, err := s.Host.Run(ctx, "systemctl", "enable", productUnit); err != nil {
		return "", Refuse("the product service could not be enabled")
	}
	if _, err := s.Host.Run(ctx, "systemctl", "start", "--no-block", productUnit); err != nil {
		return "", Refuse("the product service start could not be queued")
	}
	return "olivares.service enabled; start queued", nil
}

// Verify refuses a product unit that is no longer enabled.
func (s ProductService) Verify(ctx context.Context, _ Input, _ Effect) error {
	out, _ := s.Host.Run(ctx, "systemctl", "is-enabled", productUnit)
	if strings.TrimSpace(string(out)) != "enabled" {
		return Refuse("the product service is no longer enabled")
	}
	return nil
}

// ProductReadiness measures the product through its own readiness endpoint, over TLS
// pinned to this instance's certificate, and checks the instance's identity files.
type ProductReadiness struct {
	Host Host
	Poll time.Duration
}

// Measure reports health and identity. While the product service is not active it returns
// at once: the first-boot unit queues the product's start, and the readiness unit, ordered
// after the product, measures it. The product unit is Type=simple, so systemd reports it
// active at fork, before the product has created its certificate, keys and store; Measure
// therefore polls the product's own /readyz, pinned to the certificate the product creates,
// and judges those files only once it has answered ok, all within ctx's deadline.
func (r ProductReadiness) Measure(ctx context.Context, _ Input) (Measurement, error) {
	out, _ := r.Host.Run(ctx, "systemctl", "is-active", productUnit)
	if strings.TrimSpace(string(out)) != "active" {
		return Measurement{Detail: "the product service is not active yet"}, nil
	}
	for {
		got, final := r.attempt(ctx)
		if final {
			return got, nil
		}
		select {
		case <-ctx.Done():
			return Measurement{Detail: "the product's readiness endpoint did not answer ok: " + got.Detail}, nil
		case <-time.After(r.Poll):
		}
	}
}

// attempt measures once. It is final once /readyz has answered, over a certificate that is not
// this instance's, or ok; only then are the identity files and the store judged.
func (r ProductReadiness) attempt(ctx context.Context) (Measurement, bool) {
	certificate, err := os.ReadFile(r.Host.path(filepath.Join(ProductDataDir, "tls.crt")))
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(certificate) {
		return Measurement{Detail: "the instance certificate cannot be read yet"}, false
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	defer client.CloseIdleConnections()
	healthy, foreign := r.probe(ctx, client)
	switch {
	case foreign:
		return Measurement{Health: true, Detail: "the product serves a certificate that is not this instance's"}, true
	case !healthy:
		return Measurement{Detail: "readyz has not answered ok yet"}, false
	}
	for _, name := range []string{"tls.key", "audit-signing.key", "catalog-signing.key", "policy-signing.key"} {
		info, err := os.Lstat(r.Host.path(filepath.Join(ProductDataDir, name)))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return Measurement{Health: true, Detail: "the instance identity file " + name + " is missing or exposed"}, true
		}
	}
	// The store the product's first start creates, which initialize-storage could not measure.
	store, err := os.Lstat(r.Host.path(filepath.Join(ProductDataDir, "olivares.db")))
	if err != nil || !store.Mode().IsRegular() || store.Mode().Perm()&0o007 != 0 {
		return Measurement{Health: true, Detail: "the product store olivares.db is missing or open to others"}, true
	}
	return Measurement{Health: true, Identity: true, Detail: "readyz ok over the instance certificate; store and identity files present"}, true
}

func (r ProductReadiness) probe(ctx context.Context, client *http.Client) (healthy, foreign bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readinessURL, nil)
	if err != nil {
		return false, false
	}
	resp, err := client.Do(req)
	if err != nil {
		var verification *tls.CertificateVerificationError
		return false, errors.As(err, &verification)
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
	}
	ok := resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) == nil
	return ok && body.Status == "ok", false
}
