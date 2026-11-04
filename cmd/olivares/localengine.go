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
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// tlsCertFile is the certificate the engine generated in its data directory.
const tlsCertFile = "tls.crt"

// localEngine is the engine this host's data directory recorded: the origin
// `quickstart`/`serve` wrote to console.json (the one `first-boot` and `doctor`
// read) and, for TLS, the certificate it generated. It returns "" when this host
// has no recorded engine. It reads the files as they are, so it serves only callers
// that send no credential (readyz, the anonymous status, the local context check);
// a caller that sends one uses localEngineFor.
//
// Measured 2026-10-01 (HU J8): signing in on the engine's own host needed
// --server and --ca-cert, and the first try failed with a raw x509 error.
func localEngine() (origin, caCert string) {
	dir, err := defaultDataDir()
	if err != nil {
		return "", ""
	}
	return localEngineAt(dir, nil)
}

// localEngineFor is the engine a caller about to send a credential (a typed password,
// a token from the environment) may reach without --server: the one recorded in the
// account's data directory (accountDataDir: never one under the working directory,
// which a cloned repository can plant), and only while that directory and its console
// state and certificate are safe from other accounts (readDataDirFile). The owner may
// be another account, as when an administrator signs in on the host of an engine that
// runs as its own user.
func localEngineFor() (origin, caCert string) {
	dir, err := accountDataDir()
	if err != nil {
		return "", ""
	}
	return localEngineAt(dir, func(name string) ([]byte, error) {
		return readDataDirFile(dir, name, accountFileNotWritable, anyDirOwner)
	})
}

// localEngineAt is the engine the data directory dir recorded. A nil read takes the
// files as they are; otherwise read is the check the caller trusts, and a certificate
// present but refused by it drops the engine.
func localEngineAt(dir string, read func(name string) ([]byte, error)) (origin, caCert string) {
	checked := read != nil
	if !checked {
		read = func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) }
	}
	body, err := read(consoleStateFile)
	if err != nil {
		return "", ""
	}
	state, err := parseConsoleState(dir, body)
	if err != nil {
		return "", ""
	}
	origin = doctorOriginFromConsoleState(state)
	if strings.HasPrefix(origin, "https://") {
		switch _, err := read(tlsCertFile); {
		case err == nil:
			caCert = filepath.Join(dir, tlsCertFile)
		case checked && !errors.Is(err, fs.ErrNotExist):
			// A certificate the caller cannot vouch for would trust whatever answers.
			return "", ""
		}
	}
	return origin, caCert
}

// localOperatorEngine is the engine this account's own data directory recorded, plus
// the credential the engine left there for the account that owns it
// (localoperator_token.go). It returns empty values unless the data directory, the
// credential, the console state and the certificate are all this account's own
// (readAccountFile), as the engine writes them: another account, a host without the
// data directory, a directory planted under the working directory (a cloned repository)
// and a directory other accounts can write are not the local operator and sign in as before.
func localOperatorEngine() (origin, caCert, token, tenant string) {
	dir, err := accountDataDir()
	if err != nil {
		return "", "", "", ""
	}
	raw, err := readAccountFile(dir, localOperatorTokenFile, accountFilePrivate)
	if err != nil {
		return "", "", "", ""
	}
	token, tenant = splitLocalOperatorFile(string(raw))
	if token == "" {
		return "", "", "", ""
	}
	origin, caCert = localEngineAt(dir, func(name string) ([]byte, error) {
		return readAccountFile(dir, name, accountFileNotWritable)
	})
	if origin == "" {
		return "", "", "", ""
	}
	return origin, caCert, token, tenant
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

// hideTerminalEcho turns echo off on the terminal in until the returned restore
// runs. It is a variable so a test can stand in for a terminal.
var hideTerminalEcho = func(in io.Reader) (func(), error) {
	f, ok := in.(*os.File)
	if !ok {
		return nil, errors.New("standard input is not a terminal")
	}
	return hideEcho(f)
}

// transportHint turns the two transport failures a new user meets into a sentence
// with the next step: a certificate this computer does not trust, and an engine
// that is not running. The original error stays on the next line (callers redact
// it like any other). Anything else is returned unchanged.
func transportHint(req *url.URL, err error) error {
	cause, hostErr := classifyTransport(err)
	switch cause {
	case transportHostname:
		// The certificate is trusted but does not name this address (the engine's
		// self-signed one covers loopback only): no trust or pin can fix that.
		return exitcode.New(exitcode.Server, fmt.Errorf(
			"The engine's certificate covers %s, not %s. Use an address it covers, or start the engine with a "+
				"certificate for %s: olivares serve --tls-cert <file> --tls-key <file>\n  %w",
			certificateNames(hostErr.Certificate), hostErr.Host, hostErr.Host, err))
	case transportUntrusted:
		fix := "On the engine's host, sign in with: olivares login. " +
			"From another computer, pass the engine's certificate (<data-dir>/tls.crt): --ca-cert <file>"
		if loopbackWithoutLocalEngine(originOf(req)) {
			fix = "If the engine runs in Docker, use the CLI inside it: " + dockerEngineNext + ". " + fix
		}
		return exitcode.New(exitcode.Server, fmt.Errorf(
			"This computer does not trust the certificate of %s. %s\n  %w", originOf(req), fix, err))
	case transportRefused:
		return exitcode.New(exitcode.Server, fmt.Errorf(
			"No engine answers at %s. Start it on that host: olivares quickstart\n  %w", originOf(req), err))
	}
	return err
}

// transportCause is which of the failures a new user meets first a transport error is.
type transportCause int

const (
	transportOther transportCause = iota
	transportHostname
	transportUntrusted
	transportRefused
)

// classifyTransport is the one reading of a transport error that transportHint (the
// sentence of a command) and rootStatus (the reason of the menu footer) share. The
// HostnameError is the certificate's, set for transportHostname.
func classifyTransport(err error) (transportCause, x509.HostnameError) {
	var unknown x509.UnknownAuthorityError
	var verify *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	switch {
	case errors.As(err, &hostErr) || (errors.As(err, &verify) && errors.As(verify.Err, &hostErr)):
		return transportHostname, hostErr
	case errors.As(err, &unknown) || (errors.As(err, &verify) && errors.As(verify.Err, &unknown)):
		return transportUntrusted, hostErr
	case errors.Is(err, syscall.ECONNREFUSED):
		return transportRefused, hostErr
	}
	return transportOther, hostErr
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
