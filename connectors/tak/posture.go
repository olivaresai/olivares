// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/sdk/model"
)

// posture.go derives a TAK Server's security posture from its CONFIGURATION FILE
// (grounded, offline, the authoritative signal) and, optionally, confirms the live
// server's reachability/version. Every finding is minimal-data: a title safe to
// display, a hash of any sensitive detail, and NEVER a password, keystore pass, or
// slice of the config file's contents (docs/SECURITY-HARDENING.md).
//
// Posture finding kinds and the Configuration Guide v5.2 sentence each is grounded in:
//
//	tak_input_unencrypted        — protocol udp/mcast/tcp/stcp is "unencrypted"
//	                               (stcp: "Unencrypted, for testing only").
//	tak_input_anonymous          — an input with no auth and no <filtergroup> falls
//	                               into the "__ANON__" anonymous group.
//	tak_default_keystore_password — a keystorePass/truststorePass equal to the
//	                               documented default ("The default password is atakatak").
//	tak_tls_legacy_version       — <tls context> pinned to TLSv1 / TLSv1.1.
//	tak_no_crl                   — <tls> with no <crl> child (no revocation checking).
//	tak_ms_ca_inline_password    — a non-empty <MicrosoftCAConfig password>.
//	tak_sa_announce_enabled      — <announce enable="true">.
//
// The federation element is deliberately NOT modeled or scored: we have no primary
// evidence of its shape and will not invent one (see doc.go, wire.go).

// The documented default password shipped with TAK Server's example keystores/p12s:
// "The default password is atakatak" [Configuration Guide v5.2]. Compared
// case-sensitively; only ever used as the RHS of an equality test, never emitted.
const takDefaultPassword = "atakatak"

// Posture finding kinds.
const (
	findingInputUnencrypted     = "tak_input_unencrypted"
	findingInputAnonymous       = "tak_input_anonymous"
	findingDefaultKeystorePass  = "tak_default_keystore_password"
	findingTLSLegacyVersion     = "tak_tls_legacy_version"
	findingNoCRL                = "tak_no_crl"
	findingMSCAInlinePassword   = "tak_ms_ca_inline_password"
	findingSAAnnounceEnabled    = "tak_sa_announce_enabled"
	findingCoreConfigUnreadable = "tak_core_config_unreadable"
	findingServerVersion        = "tak_server_version"
	findingAPIUnreachable       = "tak_api_unreachable"
)

// maxProbeBodyBytes caps how much of a version-probe response we read. The body is
// server-controlled and only ever hashed or (for a 2xx) parsed for a bounded
// version string, so 64 KiB is generous.
const maxProbeBodyBytes = 64 << 10

// newMTLSClient builds the HTTP client the posture pass uses to reach a TAK Server.
//
// TAK Server authenticates operators with mTLS, so the client certificate is loaded
// from the PEM the operator supplied by reference. InsecureSkipVerify is NEVER set
// and there is no opt-out flag: a posture we could not authenticate is a posture we
// will not report. MinVersion is TLS 1.2.
func newMTLSClient(cfg config) (httpDoer, error) {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}

	if strings.TrimSpace(cfg.clientCertPEM) != "" && strings.TrimSpace(cfg.clientKeyPEM) != "" {
		cert, err := tls.X509KeyPair([]byte(cfg.clientCertPEM), []byte(cfg.clientKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("tak: parse client_cert/client_key: %w", err)
		}
		tlsConf.Certificates = []tls.Certificate{cert}
	}

	if strings.TrimSpace(cfg.caCertPEM) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.caCertPEM)) {
			return nil, fmt.Errorf("tak: ca_cert is not a valid PEM certificate bundle")
		}
		tlsConf.RootCAs = pool
	}

	return &http.Client{
		Timeout:   cfg.requestTimeout,
		Transport: &http.Transport{TLSClientConfig: tlsConf},
		// Refuse ALL redirects. The server being audited is the untrusted party in
		// this connector's threat model; the default Go policy follows up to ten
		// redirects, including to a DIFFERENT host, re-presenting the operator's
		// mTLS client certificate to whatever host the Location points at. A
		// malicious or compromised TAK Server could 302 the probe to any host with a
		// valid publicly-trusted certificate (reachable whenever ca_cert is empty and
		// the host trust store is used) and harvest the client certificate plus a
		// proof-of-possession signature — turning an authenticated control-plane
		// client into an SSRF/credential-exfil primitive. Returning ErrUseLastResponse
		// makes the probe treat a 3xx as a non-2xx result, which versionProbe reports
		// as an honest tak_api_unreachable finding. This is consistent with the
		// connector's deny-closed stance: a posture we cannot authenticate directly is
		// one we will not chase to a third party.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// serverRef is the stable subject reference every posture finding shares: the live
// URL when one is configured, otherwise the CoreConfig path.
func serverRef(cfg config) string {
	if cfg.serverURL != "" {
		return cfg.serverURL
	}
	return cfg.coreConfigPath
}

// gatherPosture runs one posture pass: the grounded CoreConfig inventory plus, when
// a live URL is configured, an honest reachability/version probe. It NEVER fails the
// whole Gather for an unreadable config or an unreachable server — each is itself a
// posture fact and becomes a finding. Findings come back in a deterministic order.
func gatherPosture(ctx context.Context, cfg config, doer httpDoer, at time.Time) ([]model.FindingReport, error) {
	ref := serverRef(cfg)
	var out []model.FindingReport

	if cfg.coreConfigPath != "" {
		data, ioErr := readCoreConfigFile(cfg.coreConfigPath)
		switch {
		case ioErr != nil:
			// An unreadable config is a posture fact, not a Gather failure.
			out = append(out, coreConfigUnreadableFinding(ref, "unreadable", at))
		default:
			c, parseErr := parseCoreConfig(data)
			if parseErr != nil {
				out = append(out, coreConfigUnreadableFinding(ref, "unparseable", at))
			} else {
				out = append(out, postureFindings(c, ref, at)...)
			}
		}
	}

	if cfg.probeEnabled() {
		out = append(out, versionProbe(ctx, cfg, doer, ref, at))
	}

	sortFindings(out)
	return out, nil
}

// readCoreConfigFile reads the CoreConfig, capped with an io.LimitReader at
// maxCoreConfigBytes so a hostile or corrupt file cannot exhaust memory.
func readCoreConfigFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxCoreConfigBytes))
}

// versionProbe performs the optional live GET against server_url + version_path.
// A 2xx yields an info finding (with the version string when the body parses,
// otherwise "reachable"); anything else — non-2xx or a transport error — yields a
// medium "unreachable" finding whose title carries NO server/attacker-controlled
// text (only a hash of the body/error goes into DetailHash).
func versionProbe(ctx context.Context, cfg config, doer httpDoer, ref string, at time.Time) model.FindingReport {
	url := cfg.serverURL + cfg.versionPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return unreachableFinding(ref, "request build error: "+err.Error(), at)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := doer.Do(req)
	if err != nil {
		return unreachableFinding(ref, "transport error: "+err.Error(), at)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A numeric status code is safe to display; the body is not.
		title := "TAK Server API returned HTTP " + strconv.Itoa(resp.StatusCode)
		detail := "status=" + strconv.Itoa(resp.StatusCode) + "|body=" + string(body)
		return model.FindingReport{
			Kind:        findingAPIUnreachable,
			Severity:    model.SeverityMedium,
			SubjectKind: subjectKindServer,
			SubjectRef:  ref,
			Title:       title,
			DetailHash:  hashString(findingAPIUnreachable + "|" + ref + "|" + detail),
			OccurredAt:  at,
		}
	}

	ver := extractVersion(body)
	title := "TAK Server reachable"
	if ver != "" {
		title = "TAK Server reachable, version " + ver
	}
	return model.FindingReport{
		Kind:        findingServerVersion,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectKindServer,
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  hashString(findingServerVersion + "|" + ref + "|" + ver),
		OccurredAt:  at,
	}
}

// unreachableFinding builds the medium "API unreachable" finding. reason is hashed
// into DetailHash only — it may echo a server- or error-controlled string, so it
// never reaches the Title.
func unreachableFinding(ref, reason string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingAPIUnreachable,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKindServer,
		SubjectRef:  ref,
		Title:       "TAK Server API unreachable",
		DetailHash:  hashString(findingAPIUnreachable + "|" + ref + "|" + reason),
		OccurredAt:  at,
	}
}

// coreConfigUnreadableFinding reports that the CoreConfig could not be read
// (reason "unreadable") or parsed (reason "unparseable"). Neither the file's
// contents nor the raw error reaches any field; the reason class is a stable token.
func coreConfigUnreadableFinding(ref, reason string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingCoreConfigUnreadable,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKindServer,
		SubjectRef:  ref,
		Title:       "TAK Server CoreConfig is " + reason,
		DetailHash:  hashString(findingCoreConfigUnreadable + "|" + ref + "|" + reason),
		OccurredAt:  at,
	}
}

// extractVersion best-effort-decodes a version string from a probe body. On any
// failure it returns "" so the caller reports the server as merely reachable. Any
// value returned is bounded and control-stripped, so a hostile server cannot inject
// into a title/log line.
func extractVersion(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v versionResponse
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	return sanitizeVersion(v.version())
}

// sanitizeVersion trims, length-bounds (64 bytes) and rejects control characters,
// returning "" for anything that is not a plain short token.
func sanitizeVersion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

// postureFindings is a PURE function (no I/O): it maps a parsed CoreConfig to the
// findings it warrants, in emission (document) order. gatherPosture sorts the
// combined set deterministically. Every title is safe to display; no secret,
// keystore pass or config content is ever placed in any field.
func postureFindings(c Configuration, ref string, at time.Time) []model.FindingReport {
	out := make([]model.FindingReport, 0, 8)
	add := func(kind string, sev model.Severity, title, detail string) {
		out = append(out, model.FindingReport{
			Kind:        kind,
			Severity:    sev,
			SubjectKind: subjectKindServer,
			SubjectRef:  ref,
			Title:       title,
			DetailHash:  hashString(kind + "|" + ref + "|" + detail),
			OccurredAt:  at,
		})
	}

	if c.Network != nil {
		for _, in := range c.Network.Inputs {
			name := boundName(in.Name)
			proto := strings.ToLower(strings.TrimSpace(in.Protocol))

			// Unencrypted transport. The guide labels udp/mcast/tcp/stcp unencrypted;
			// stcp is additionally "for testing only".
			switch proto {
			case "udp", "mcast", "tcp", "stcp":
				title := "TAK input " + name + " uses unencrypted protocol " + proto
				if proto == "stcp" {
					title += " (streaming/bi-directional; unencrypted, for testing only)"
				}
				add(findingInputUnencrypted, model.SeverityHigh, title,
					"input="+name+"|proto="+proto+"|port="+strings.TrimSpace(in.Port))
			}

			// Anonymous access. An input with neither an auth attribute nor a
			// <filtergroup> falls into the "__ANON__" anonymous group per the guide.
			if strings.TrimSpace(in.Auth) == "" && len(in.FilterGroups) == 0 {
				add(findingInputAnonymous, model.SeverityMedium,
					"TAK input "+name+" has no auth and no filtergroup: traffic falls into the __ANON__ anonymous group",
					"input="+name)
			}
		}

		// SA announce (multicast self-discovery) enabled.
		if a := c.Network.Announce; a != nil && strings.EqualFold(strings.TrimSpace(a.Enable), "true") {
			add(findingSAAnnounceEnabled, model.SeverityInfo,
				"TAK Server SA announce (multicast discovery) is enabled", "sa_announce")
		}
	}

	if c.Security != nil && c.Security.TLS != nil {
		tlsCfg := c.Security.TLS

		if tlsCfg.KeystorePass == takDefaultPassword {
			add(findingDefaultKeystorePass, model.SeverityCritical,
				"TAK Server keystore uses the documented default password (security.tls.keystorePass)",
				"element=security.tls.keystorePass")
		}
		if tlsCfg.TruststorePass == takDefaultPassword {
			add(findingDefaultKeystorePass, model.SeverityCritical,
				"TAK Server truststore uses the documented default password (security.tls.truststorePass)",
				"element=security.tls.truststorePass")
		}

		if ctxVer := strings.TrimSpace(tlsCfg.Context); ctxVer == "TLSv1" || ctxVer == "TLSv1.1" {
			add(findingTLSLegacyVersion, model.SeverityHigh,
				"TAK Server TLS is pinned to legacy protocol "+ctxVer+" (security.tls @context)",
				"context="+ctxVer)
		}

		if tlsCfg.CRL == nil {
			add(findingNoCRL, model.SeverityLow,
				"TAK Server TLS has no certificate revocation list (CRL) configured", "no_crl")
		}
	}

	if cs := c.CertificateSigning; cs != nil {
		if ca := cs.TAKServerCAConfig; ca != nil && ca.KeystorePass == takDefaultPassword {
			add(findingDefaultKeystorePass, model.SeverityCritical,
				"TAK Server CA keystore uses the documented default password (certificateSigning.TAKServerCAConfig.keystorePass)",
				"element=certificateSigning.TAKServerCAConfig.keystorePass")
		}
		if ms := cs.MicrosoftCAConfig; ms != nil {
			if ms.TruststorePass == takDefaultPassword {
				add(findingDefaultKeystorePass, model.SeverityCritical,
					"Microsoft CA truststore uses the documented default password (certificateSigning.MicrosoftCAConfig.truststorePass)",
					"element=certificateSigning.MicrosoftCAConfig.truststorePass")
			}
			if strings.TrimSpace(ms.Password) != "" {
				add(findingMSCAInlinePassword, model.SeverityHigh,
					"Microsoft CA password is stored inline in the TAK Server config (certificateSigning.MicrosoftCAConfig.password)",
					"element=certificateSigning.MicrosoftCAConfig.password")
			}
		}
	}

	return out
}

// sortFindings imposes a deterministic total order (Kind, then SubjectRef, then
// Title, then DetailHash) so tests and the append-only ledger are stable regardless
// of emission order.
func sortFindings(f []model.FindingReport) {
	sort.Slice(f, func(i, j int) bool {
		a, b := f[i], f[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.SubjectRef != b.SubjectRef {
			return a.SubjectRef < b.SubjectRef
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.DetailHash < b.DetailHash
	})
}

// boundName renders an input's operator-authored _name for a title/hash: control
// characters are stripped and the result is bounded to 64 bytes on a rune boundary.
// An empty name becomes "(unnamed)".
func boundName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > 64 {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "(unnamed)"
	}
	return b.String()
}
