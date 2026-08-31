// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// defaultCLIRequestTimeout is the overall deadline a CLI request gets when the
// caller does not state one. It is a var, not a const, purely so a test can
// shorten it: proving that the SSE attach OUTLIVES it otherwise costs ten
// seconds of wall clock, and a ten-second test is a test nobody runs.
var defaultCLIRequestTimeout = 10 * time.Second

const (
	maxCLICABundleSize = 16 << 20
	cliInsecureWarning = "WARNING: TLS verification disabled — never use against production"
	// maxCLIRedirects is the cap http.Client applies by default. Installing a
	// CheckRedirect REPLACES that default, so the cap has to be restated here or a
	// redirect loop would run forever.
	maxCLIRedirects = 10
	// cliCleartextOptInEnv is the escape hatch for the plain-HTTP refusal below. It
	// is an environment variable as well as a flag because cliTransport serves
	// surfaces that do not carry the auth flag set (agent, evals, hook-pep,
	// completion): a refusal must never name a flag the operator cannot pass.
	cliCleartextOptInEnv = "OLIVARES_ALLOW_CLEARTEXT"
)

type cliTransportOptions struct {
	Resolved cliResolvedConfig
	Insecure bool
	Timeout  time.Duration
	Stderr   io.Writer
	// Unbounded asks for a client with NO overall deadline, for a long-lived
	// stream. It exists because Timeout==0 cannot express it: zero means "not
	// specified" here and is replaced by defaultCLIRequestTimeout.
	//
	// Shipped without this and regressed `agent session attach`: that path
	// passed 0 meaning "unlimited" (which is what a bare http.Client does with a
	// zero Timeout, the merge-base behavior) and silently got ten seconds — and
	// http.Client.Timeout covers reading the body, so a live SSE attach died
	// mid-stream. Found by the sol-max contrast.
	Unbounded bool
	// CarriesSecret marks a request whose BODY carries a secret even though no
	// bearer is attached: the two anonymous legs, POST /v1/setup (one-time setup
	// token + the first password) and POST /v1/auth/login (a password).
	//
	// Without it the two rules below would read exactly those requests as harmless,
	// because they judge "does this carry a credential" by the Authorization header
	// and those legs deliberately have none.
	CarriesSecret bool
	// AllowCleartext is the explicit, dangerous opt-in that lets a request carrying
	// a credential travel to a NON-loopback host over plain HTTP. Off by default;
	// OLIVARES_ALLOW_CLEARTEXT=1 is the equivalent for surfaces with no flag for it.
	AllowCleartext bool
}

// cleartextOptIn reports the environment form of --allow-cleartext. Only the
// unambiguous affirmatives count: a variable that happens to be set to "0" or ""
// must not silently disable a credential protection.
func cleartextOptIn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(cliCleartextOptInEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// isLoopbackCLIHost is the ONE exception to the plain-HTTP refusal: a plane on
// this machine. `olivares serve --insecure` on 127.0.0.1 is the documented
// development and first-run path (and the one the C-21 walkthrough drives), and
// there the packets never leave the host.
//
// "localhost" is accepted by name as well as by address because that is how the
// quickstart prints it. That trusts the host's own resolver, which is the same
// trust the operator already extends to it for everything else on the box.
func isLoopbackCLIHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// cliOrigin renders scheme://host:port and NOTHING else. Redirect refusals name
// both ends, and a path or query string can carry an id, a cursor or a filter the
// operator would rather not see copied into a log; the origin is the whole of what
// the decision was made on.
func cliOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + cliURLPort(u)
}

func cliURLPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func sameCLIOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		cliURLPort(a) == cliURLPort(b)
}

// cliRedirectPolicy is why a 307 cannot carry this CLI's secrets to a stranger.
//
// A bare http.Client follows up to ten redirects, and on 307/308 it REPLAYS the
// method and the body — so a POST /v1/setup answered with a redirect re-sends the
// one-time setup token and the first superadmin's password to wherever the
// Location header points. The standard library's own guard is narrower than it
// looks: it drops Authorization only when the host changes (a scheme downgrade to
// http on the SAME host keeps it), it copies every other header including
// X-Olivares-Tenant unconditionally, and it never protects the BODY at all.
//
// So for a request carrying a credential or a secret, the only redirect this
// client follows is one that lands on the SAME origin — same scheme, host and
// port, which is what a trailing-slash or path-canonicalising redirect is. Any
// other is refused by name, on both ends, rather than followed with the secret
// stripped: a login that silently loses its password does not become correct.
// Requests carrying nothing keep the ordinary behavior.
func cliRedirectPolicy(sensitive bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxCLIRedirects {
			return fmt.Errorf("stopped after %d redirects", maxCLIRedirects)
		}
		if !sensitive || len(via) == 0 || req.URL == nil || via[0].URL == nil {
			return nil
		}
		if sameCLIOrigin(via[0].URL, req.URL) {
			return nil
		}
		return fmt.Errorf(
			"refusing to follow a redirect from %s to %s: this request carries a credential or a "+
				"secret, and a 307/308 replays the body — point --server at the plane you mean",
			cliOrigin(via[0].URL), cliOrigin(req.URL))
	}
}

// cliTransport is the shared HTTP substrate for context-aware CLI commands. It
// returns both a hardened client and the authentication/tenant headers callers
// must attach to each request; it never writes or formats credential values.
func cliTransport(opts cliTransportOptions) (*http.Client, http.Header, error) {
	resolved := opts.Resolved
	// Every refusal below is about the CALLER'S ARGUMENTS, so each one exits 2 —
	// "the invocation itself is wrong", the contract the root command's help
	// publishes. They carried no code at all until so exitcode.From read them
	// as the generic 1 and a script could not tell a mistyped pin from a broken
	// control plane. Same defect as the pin decoder below, same function.
	if resolved.Server == "" {
		return nil, nil, exitcode.New(exitcode.Usage,
			errors.New("no server: set --server, OLIVARES_SERVER_URL, or an active client context"))
	}
	u, err := url.Parse(resolved.Server)
	if err != nil || u.Host == "" {
		return nil, nil, exitcode.New(exitcode.Usage, fmt.Errorf("invalid resolved server URL %q", resolved.Server))
	}
	if u.Scheme != "https" && (resolved.CACert != "" || len(resolved.PinSHA256) > 0) {
		return nil, nil, exitcode.New(exitcode.Usage,
			errors.New("--ca-cert and --pin-sha256 require an https server"))
	}
	// A CREDENTIAL DOES NOT TRAVEL IN CLEARTEXT TO ANOTHER HOST.
	//
	// normalizeCLIServer accepts http and https alike (cliconfig.go:302), which is
	// right for the public, unauthenticated GET /status. It is not right for a
	// bearer, a password or a one-time setup token: on plain HTTP every one of them
	// is readable by anything on the path, and re-typing the URL with https is not
	// a decision the operator is asked to make anywhere. Refuse, and say what the
	// two ways forward are — the escape hatch stays, but it has to be asked for.
	//
	// Loopback is the named exception: `serve --insecure` on 127.0.0.1 is the
	// documented first-run path and the packets never leave the machine.
	sensitive := resolved.Token != "" || opts.CarriesSecret
	if sensitive && u.Scheme != "https" && !isLoopbackCLIHost(u.Hostname()) &&
		!opts.AllowCleartext && !cleartextOptIn() {
		return nil, nil, exitcode.New(exitcode.Usage, fmt.Errorf(
			"refusing to send a credential to %s over plain HTTP: it would be readable by every "+
				"host on the path. Use https://, or accept the exposure explicitly with "+
				"--allow-cleartext (or %s=1)", cliOrigin(u), cliCleartextOptInEnv))
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if resolved.CACert != "" {
		roots, err := loadCLIRootCAs(resolved.CACert)
		if err != nil {
			return nil, nil, err
		}
		tlsConfig.RootCAs = roots
	}

	if opts.Insecure {
		stderr := opts.Stderr
		if stderr == nil {
			stderr = os.Stderr
		}
		if _, err := fmt.Fprintln(stderr, cliInsecureWarning); err != nil {
			return nil, nil, fmt.Errorf("write TLS warning: %w", err)
		}
		tlsConfig.InsecureSkipVerify = true // #nosec G402 -- explicit --insecure compatibility path, always accompanied by the warning above
	} else if len(resolved.PinSHA256) > 0 {
		pins := make([][]byte, 0, len(resolved.PinSHA256))
		for _, spec := range resolved.PinSHA256 {
			pin, err := decodeSPKIPin(spec)
			if err != nil {
				return nil, nil, err
			}
			pins = append(pins, pin)
		}
		// A pin is an explicit trust anchor, so it can authenticate a private or
		// self-signed plane. Chain verification is disabled only for this branch;
		// the callback still requires hostname, validity and one exact leaf-SPKI pin.
		tlsConfig.InsecureSkipVerify = true // #nosec G402 -- replaced below by explicit leaf SPKI, hostname and validity verification
		tlsConfig.VerifyPeerCertificate = pinnedPeerVerifier(pins, u.Hostname())
	}

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	} else {
		transport = transport.Clone()
	}
	transport.TLSClientConfig = tlsConfig
	timeout := opts.Timeout
	switch {
	case opts.Unbounded:
		timeout = 0 // http.Client: no overall deadline. The caller's context ends it.
	case timeout <= 0:
		timeout = defaultCLIRequestTimeout
	}
	headers := make(http.Header)
	if resolved.Token != "" {
		headers.Set("Authorization", "Bearer "+resolved.Token)
	}
	if resolved.Tenant != "" {
		headers.Set("X-Olivares-Tenant", resolved.Tenant)
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: cliRedirectPolicy(sensitive),
	}, headers, nil
}

// cliDo performs req and classifies a TRANSPORT failure as exit 6 (E3).
//
// httpErr already maps HTTP statuses onto the exit contract, but it only ever
// sees a response. A control plane that is down, unreachable, or presenting a
// certificate the caller refuses never produces one: client.Do returns a Go
// error, which exitcode.From could only read as generic. Measured before this:
// `dial tcp: connection refused` exited 1, so a script could not tell a dead
// engine from a bad request — the contract in `olivares --help` says 6.
//
// Every CLI network path goes through here, so the classification is stated once
// rather than at each of the dozens of call sites that could forget it.
func cliDo(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, exitcode.New(exitcode.Server, err)
	}
	return resp, nil
}

// missingCLIValueError explains an unresolved server/token/tenant in terms of
// EVERY place it could have come from, including the client contexts (E7).
// The old message named only the flag and the environment variable, so after a
// successful `olivares auth login` a command could still say "no server: set
// --server or OLIVARES_SERVER_URL" without ever mentioning that contexts exist,
// that one was active, or which one.
func missingCLIValueError(what, flag, env string, resolved cliResolvedConfig) error {
	msg := fmt.Sprintf("no %s: pass %s, set %s, or select a client context with `olivares auth use-context`",
		what, flag, env)
	switch {
	case resolved.ContextName != "":
		msg += fmt.Sprintf(" (the active context %q does not supply one; config: %s)",
			resolved.ContextName, resolved.ConfigPath)
	case resolved.ConfigPath != "":
		msg += fmt.Sprintf(" (no context is active; config: %s)", resolved.ConfigPath)
	}
	return exitcode.New(exitcode.Usage, errors.New(msg))
}

func loadCLIRootCAs(path string) (*x509.CertPool, error) {
	f, err := os.Open(path) //nolint:gosec // operator-selected CA bundle
	if err != nil {
		return nil, fmt.Errorf("read CA certificate %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxCLICABundleSize+1))
	if err != nil {
		return nil, fmt.Errorf("read CA certificate %s: %w", path, err)
	}
	if len(raw) > maxCLICABundleSize {
		return nil, fmt.Errorf("CA certificate %s exceeds %d bytes", path, maxCLICABundleSize)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if ok := roots.AppendCertsFromPEM(raw); !ok {
		return nil, fmt.Errorf("CA certificate %s contains no valid PEM certificates", path)
	}
	return roots, nil
}

// decodeSPKIPin parses one --pin-sha256 value into the 32 raw bytes of a leaf
// SubjectPublicKeyInfo SHA-256 digest.
//
// Widened it and sharpened its refusal, for one reason: the digest is 32 bytes
// and HOW THEY WERE WRITTEN DOWN IS NOT A SECURITY PROPERTY. This accepted base64
// only, while `openssl x509 -fingerprint -sha256` — and, until our own
// first-boot log line — hand the operator hex. A correct pin was being refused for
// its punctuation, with a message that never said a digest of WHAT, so there was no
// way to act on it. Hex and base64 cannot be confused: 32 bytes is 64 hex characters
// or 43/44 base64 ones.
//
// What did NOT widen, and must not: this is the SPKI digest, and only an SPKI digest
// ever authenticates. Be precise about where a certificate fingerprint dies, because
// the first version of this comment was not: a certificate fingerprint is 64 hex
// characters, so since the hex branch it PARSES here into its 32 bytes — and then
// fails at the handshake, on the pin comparison, as a mismatch (exit 6). It is never
// accepted as a pin. What must never happen is treating it as one: that would
// silently turn an SPKI pin — which survives renewing the certificate on the same
// key — into a certificate pin that breaks on every renewal.
func decodeSPKIPin(spec string) ([]byte, error) {
	original := strings.TrimSpace(spec)
	// Strip surrounding quotes. Operators copy this value out of a log line, and a
	// structured logger quotes what it must: a JSON handler quotes every value, and
	// slog's text handler quotes anything containing '='. SPKIPin now emits unpadded
	// base64 so the text handler renders it bare, but the JSON case remains and a
	// pasted "…" is unambiguous, so accept it rather than refuse a correct digest
	// over its punctuation.
	encoded := trimMatchingQuotes(original)
	for _, prefix := range []string{"SHA256:", "sha256:", "sha256/", "SHA256/"} {
		if strings.HasPrefix(encoded, prefix) {
			encoded = strings.TrimPrefix(encoded, prefix)
			break
		}
	}
	// Hex first: `openssl` emits it colon- or space-grouped, in either case.
	if h := strings.Map(dropPinHexSeparator, encoded); len(h) == hex.EncodedLen(sha256.Size) {
		if pin, err := hex.DecodeString(h); err == nil {
			return pin, nil
		}
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if pin, err := encoding.DecodeString(encoded); err == nil && len(pin) == sha256.Size {
			return pin, nil
		}
	}
	return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
		"invalid --pin-sha256 %q: expected the leaf certificate's SPKI SHA-256 digest — 32 bytes, "+
			"written as base64 or hex. This is NOT the certificate fingerprint: the engine prints "+
			"the value to use as pin_sha256 on the line where it reports the certificate "+
			"(`generated a self-signed TLS certificate…` / `serving HTTPS…`). From a PEM: "+
			"openssl x509 -in cert.pem -pubkey -noout | openssl pkey -pubin -outform der | "+
			"openssl dgst -sha256 -binary | openssl base64", original))
}

// trimMatchingQuotes removes ONE matching pair of surrounding quotes.
//
// strings.Trim was wrong here and the sol-max contrast said why: it strips any
// number and any mix of quote characters from both ends, so `'digest"` and `""digest`
// were accepted as if they were quoted values. That is not "the operator pasted a
// quoted log field", it is a typo being silently normalised.
func trimMatchingQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' || first == '\'') && first == last {
		return s[1 : len(s)-1]
	}
	return s
}

// dropPinHexSeparator strips the grouping punctuation openssl puts between hex
// octets, so AA:BB:CC… and "AA BB CC…" parse as the digest they are.
func dropPinHexSeparator(r rune) rune {
	switch r {
	case ':', ' ', '-':
		return -1
	}
	return r
}

func pinnedPeerVerifier(pins [][]byte, serverName string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("TLS peer sent no certificate for SPKI pin verification")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parse TLS leaf certificate for SPKI pin verification: %w", err)
		}
		now := time.Now()
		if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
			return fmt.Errorf("TLS leaf certificate is not valid at %s", now.UTC().Format(time.RFC3339))
		}
		if err := leaf.VerifyHostname(serverName); err != nil {
			return fmt.Errorf("TLS leaf certificate hostname verification failed: %w", err)
		}
		actual := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
		for _, pin := range pins {
			if subtle.ConstantTimeCompare(actual[:], pin) == 1 {
				return nil
			}
		}
		return fmt.Errorf("TLS SPKI pin mismatch: leaf SHA256:%s did not match any configured --pin-sha256", base64.RawStdEncoding.EncodeToString(actual[:]))
	}
}
