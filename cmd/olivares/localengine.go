// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// localEngine is the engine this host's data directory recorded: the origin
// `quickstart`/`serve` wrote to console.json (the one `first-boot` and `doctor`
// read) and, for TLS, the certificate it generated. It returns "" when this host
// has no recorded engine.
//
// Measured 2026-10-01 (HU J8): signing in on the engine's own host needed
// --server and --ca-cert, and the first try failed with a raw x509 error.
func localEngine() (origin, caCert string) {
	dir, err := defaultDataDir()
	if err != nil {
		return "", ""
	}
	state, err := readConsoleState(dir)
	if err != nil {
		return "", ""
	}
	origin = doctorOriginFromConsoleState(state)
	if strings.HasPrefix(origin, "https://") {
		crt := filepath.Join(dir, "tls.crt")
		if info, err := os.Stat(crt); err == nil && info.Mode().IsRegular() {
			caCert = crt
		}
	}
	return origin, caCert
}

// localContextTenant is the tenant of the saved client context when that context
// points at the engine this host recorded. Commands that read this host's store
// take their tenant from it after the flag and the environment (N1 RU-01: `eventing
// … ls` said "tenant required" right after `auth login`). A context for another
// engine says nothing about this host's store, so it is not used.
func localContextTenant() string {
	cfg, _, err := loadCLIConfig()
	if err != nil {
		return ""
	}
	active, ok := cfg.context(cfg.CurrentContext)
	if !ok || active.Tenant == "" {
		return ""
	}
	origin, _ := localEngine()
	if origin == "" || !sameLocalOrigin(active.Server, origin) {
		return ""
	}
	return active.Tenant
}

// sameLocalOrigin compares two origins on this host, reading localhost, 127.0.0.1
// and ::1 as one host: the engine records 127.0.0.1 and people sign in to either.
func sameLocalOrigin(a, b string) bool {
	ua, errA := url.Parse(strings.TrimRight(a, "/"))
	ub, errB := url.Parse(strings.TrimRight(b, "/"))
	if errA != nil || errB != nil || ua.Scheme != ub.Scheme || ua.Port() != ub.Port() {
		return false
	}
	loopback := func(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }
	return ua.Hostname() == ub.Hostname() || (loopback(ua.Hostname()) && loopback(ub.Hostname()))
}

// promptCredentials asks a person at a terminal for an email and a password; the
// password is not echoed. ok is false when stdin is not a terminal.
func promptCredentials(cmd *cobra.Command) (email, password string, ok bool, err error) {
	in := cmd.InOrStdin()
	if !interactiveStdin(in) {
		return "", "", false, nil
	}
	r := bufio.NewReader(in)
	fmt.Fprint(cmd.ErrOrStderr(), "Email: ")
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", true, err
	}
	email = strings.TrimSpace(line)
	if email == "" {
		return "", "", true, sentence(exitcode.Usage, "Email is required.")
	}
	password, err = askPassword(cmd, in, r)
	if err != nil {
		return "", "", true, err
	}
	return email, password, true, nil
}

// promptPassword asks a person at a terminal for the password of an email given with
// --email; it is not echoed. ok is false when stdin is not a terminal.
func promptPassword(cmd *cobra.Command) (password string, ok bool, err error) {
	in := cmd.InOrStdin()
	if !interactiveStdin(in) {
		return "", false, nil
	}
	password, err = askPassword(cmd, in, bufio.NewReader(in))
	return password, true, err
}

// askPassword prints the prompt and reads one line with echo off.
func askPassword(cmd *cobra.Command, in io.Reader, r *bufio.Reader) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
	password, err := readHiddenInput(in, r)
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", sentence(exitcode.Usage,
			"Could not read the password from this terminal (%v). Use --password-file <file>.", err)
	}
	return password, nil
}

// readHiddenInput reads the password with echo off. It is a variable so a test can
// drive the prompt without a terminal.
var readHiddenInput = func(in io.Reader, r *bufio.Reader) (string, error) {
	f, ok := in.(*os.File)
	if !ok {
		return "", errors.New("standard input is not a terminal")
	}
	return readHiddenLine(f, r)
}

// transportHint turns the two transport failures a new user meets into a sentence
// with the next step: a certificate this computer does not trust, and an engine
// that is not running. The original error stays on the next line (callers redact
// it like any other). Anything else is returned unchanged.
func transportHint(req *url.URL, err error) error {
	var unknown x509.UnknownAuthorityError
	var verify *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	switch {
	case errors.As(err, &hostErr) || (errors.As(err, &verify) && errors.As(verify.Err, &hostErr)):
		// The certificate is trusted but does not name this address (the engine's
		// self-signed one covers loopback only): no trust or pin can fix that.
		return exitcode.New(exitcode.Server, fmt.Errorf(
			"The engine's certificate covers %s, not %s. Use an address it covers, or start the engine with a "+
				"certificate for %s: olivares serve --tls-cert <file> --tls-key <file>\n  %w",
			certificateNames(hostErr.Certificate), hostErr.Host, hostErr.Host, err))
	case errors.As(err, &unknown) || (errors.As(err, &verify) && errors.As(verify.Err, &unknown)):
		return exitcode.New(exitcode.Server, fmt.Errorf(
			"This computer does not trust the certificate of %s. On the engine's host, sign in with: olivares login. "+
				"From another computer, pass the engine's certificate (<data-dir>/tls.crt): --ca-cert <file>\n  %w",
			originOf(req), err))
	case errors.Is(err, syscall.ECONNREFUSED):
		return exitcode.New(exitcode.Server, fmt.Errorf(
			"No engine answers at %s. Start it on that host: olivares quickstart\n  %w", originOf(req), err))
	}
	return err
}

// certificateNames lists the names and addresses a certificate covers.
func certificateNames(c *x509.Certificate) string {
	if c == nil {
		return "other names"
	}
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 {
		return "no address"
	}
	return strings.Join(names, ", ")
}

func originOf(u *url.URL) string {
	if u == nil {
		return "the engine"
	}
	return u.Scheme + "://" + u.Host
}
