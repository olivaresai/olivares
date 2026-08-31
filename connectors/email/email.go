// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package email is the Olivares AI output connector that delivers a notification as an
// authenticated email over SMTP. It implements sdk.OutputConnector and is built for the
// deliverability bar a high-volume sender now faces: it DKIM-signs every message
// (RFC 6376, RSA-SHA256, relaxed/relaxed) so the From: domain aligns under DMARC — the
// alignment Gmail/Yahoo/Microsoft require of bulk senders, and whose absence triggers
// Microsoft's 550 5.7.515 rejection — and it carries one-click unsubscribe headers
// (List-Unsubscribe + List-Unsubscribe-Post, RFC 8058) so a recipient can unsubscribe
// without a round trip, another bulk-sender requirement.
//
// SPF is a DNS/envelope concern the operator configures at their domain (this connector
// cannot set DNS); DKIM is what this connector contributes to alignment, and it says so
// honestly. The SMTP credential and the DKIM private key are declared Secret, held in
// memory only, and never logged. It imports only the SDK and the standard library
// (net/smtp), never the engine.
package email

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/smtp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.email"

// Default configuration values.
const (
	defaultSMTPPort = 587
	defaultSubject  = "Olivares notification"
)

// Config field keys.
const (
	cfgSMTPHost      = "smtp_host"
	cfgSMTPPort      = "smtp_port"
	cfgSMTPUsername  = "smtp_username"
	cfgSMTPPassword  = "smtp_password"
	cfgFrom          = "from"
	cfgFromName      = "from_name"
	cfgTo            = "to"
	cfgDKIMDomain    = "dkim_domain"
	cfgDKIMSelector  = "dkim_selector"
	cfgDKIMKey       = "dkim_private_key"
	cfgUnsubURL      = "unsubscribe_url"
	cfgUnsubMailto   = "unsubscribe_mailto"
	cfgInsecureNoTLS = "insecure_no_tls"

	// fieldTo overrides the recipient list per-notification (comma-separated).
	fieldTo = "to"
)

// smtpSendFunc abstracts the SMTP delivery so a test can capture the message without a
// live server. The default implementation (Output.deliver) does real STARTTLS/implicit
// TLS SMTP; a test injects a stub.
type smtpSendFunc func(ctx context.Context, msg []byte, from string, to []string) error

// Output is the SMTP email output connector.
type Output struct {
	host        string
	port        int
	username    string
	password    string
	fromAddr    string
	fromName    string
	defaultTo   []string
	unsubURL    string
	unsubMailto string
	insecureTLS bool

	dkim *dkimSigner

	send smtpSendFunc           // injectable (tests); nil => real SMTP
	now  func() time.Time       // injectable clock (tests); nil => time.Now
	rid  func() (string, error) // injectable message-id randomness (tests); nil => crypto/rand
}

var _ sdk.OutputConnector = (*Output)(nil)

// New returns an email output connector with default configuration.
func New() *Output {
	return &Output{port: defaultSMTPPort}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Email (SMTP + DKIM)",
		Description: "Delivers notifications as DKIM-signed email over SMTP with DMARC-aligning signatures and RFC 8058 one-click unsubscribe headers.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgSMTPHost, Type: sdk.FieldString, Required: true, Description: "SMTP server host."},
			{Key: cfgSMTPPort, Type: sdk.FieldInt, Default: strconv.Itoa(defaultSMTPPort), Description: "SMTP port: 587/25 use STARTTLS, 465 uses implicit TLS."},
			{Key: cfgSMTPUsername, Type: sdk.FieldString, Secret: true, Description: "SMTP auth username. Held in memory only, never logged."},
			{Key: cfgSMTPPassword, Type: sdk.FieldString, Secret: true, Description: "SMTP auth password. Held in memory only, never logged."},
			{Key: cfgFrom, Type: sdk.FieldString, Required: true, Description: "From address (its domain SHOULD equal the DKIM d= domain for DMARC alignment)."},
			{Key: cfgFromName, Type: sdk.FieldString, Description: "Optional From display name."},
			{Key: cfgTo, Type: sdk.FieldString, Description: "Default recipient(s), comma-separated (override per-notification with Fields[\"to\"])."},
			{Key: cfgDKIMDomain, Type: sdk.FieldString, Description: "DKIM signing domain (d=). Empty disables DKIM signing."},
			{Key: cfgDKIMSelector, Type: sdk.FieldString, Description: "DKIM selector (s=); its public key lives at <selector>._domainkey.<domain> in DNS."},
			{Key: cfgDKIMKey, Type: sdk.FieldString, Secret: true, Description: "DKIM RSA private key (PEM, PKCS#1 or PKCS#8). Held in memory only, never logged."},
			{Key: cfgUnsubURL, Type: sdk.FieldString, Description: "HTTPS one-click unsubscribe URL (RFC 8058); enables List-Unsubscribe-Post."},
			{Key: cfgUnsubMailto, Type: sdk.FieldString, Description: "Optional mailto: unsubscribe address (fallback in List-Unsubscribe)."},
			{Key: cfgInsecureNoTLS, Type: sdk.FieldBool, Default: "false", Description: "Send without TLS (DANGEROUS; localhost test relays only)."},
		},
	}
}

// Open resolves and validates configuration, parses the DKIM key, and prepares the
// connector. It fails fast on a missing host/from, an unparseable port, or an invalid
// DKIM key.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.host = strings.TrimSpace(cfg.Get(cfgSMTPHost))
	if o.host == "" {
		return fmt.Errorf("email: %s is required", cfgSMTPHost)
	}
	o.port = cfg.GetInt(cfgSMTPPort, defaultSMTPPort)
	o.username = cfg.Get(cfgSMTPUsername)
	o.password = cfg.Get(cfgSMTPPassword)
	o.fromAddr = strings.TrimSpace(cfg.Get(cfgFrom))
	if o.fromAddr == "" {
		return fmt.Errorf("email: %s is required", cfgFrom)
	}
	o.fromName = strings.TrimSpace(cfg.Get(cfgFromName))
	o.defaultTo = splitAddrs(cfg.Get(cfgTo))
	o.unsubURL = strings.TrimSpace(cfg.Get(cfgUnsubURL))
	o.unsubMailto = strings.TrimSpace(cfg.Get(cfgUnsubMailto))
	o.insecureTLS = cfg.GetBool(cfgInsecureNoTLS, false)

	signer, err := newDKIMSigner(strings.TrimSpace(cfg.Get(cfgDKIMDomain)), strings.TrimSpace(cfg.Get(cfgDKIMSelector)), cfg.Get(cfgDKIMKey))
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	o.dkim = signer
	return nil
}

// Notify renders n as a DKIM-signed email and delivers it over SMTP. The recipient is
// taken from Fields["to"] or the configured default; a notification with no recipient
// is a configuration error (an email with no destination cannot be delivered).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.host == "" {
		return fmt.Errorf("email: Notify called before Open")
	}
	to := o.defaultTo
	if v := strings.TrimSpace(n.Fields[fieldTo]); v != "" {
		to = splitAddrs(v)
	}
	if len(to) == 0 {
		return fmt.Errorf("email: no recipient (set the to config or Fields[%q])", fieldTo)
	}
	msg, err := o.buildMessage(n, to)
	if err != nil {
		return fmt.Errorf("email: build message: %w", err)
	}
	send := o.send
	if send == nil {
		send = o.deliver
	}
	if err := send(ctx, msg, o.fromAddr, to); err != nil {
		// SMTP errors carry the server's response (which may name the host but never the
		// credential, which is only ever in the AUTH command we never echo). Surface it.
		return fmt.Errorf("email: deliver: %w", err)
	}
	return nil
}

// Close releases resources; this connector holds none.
func (o *Output) Close(context.Context) error { return nil }

// buildMessage assembles the RFC 5322 message: the ordered headers (DKIM-Signature
// prepended), a blank line, and the text/plain body. The body uses CRLF line endings.
func (o *Output) buildMessage(n sdk.Notification, to []string) ([]byte, error) {
	now := o.clock()
	headers := []header{
		{"From", o.fromHeader()},
		{"To", strings.Join(to, ", ")},
		{"Subject", subject(n)},
		{"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", o.messageID()},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=UTF-8"},
	}
	if lu := o.listUnsubscribe(); lu != "" {
		headers = append(headers, header{"List-Unsubscribe", lu})
		// One-click POST only makes sense with an HTTPS URI (RFC 8058 §3.1).
		if o.unsubURL != "" {
			headers = append(headers, header{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"})
		}
	}
	body := renderBody(n)

	// DKIM signature is computed over the canonicalized headers + body and prepended.
	var out strings.Builder
	if o.dkim != nil {
		sig, err := o.dkim.sign(headers, body, now)
		if err != nil {
			return nil, err
		}
		out.WriteString("DKIM-Signature: ")
		out.WriteString(sig)
		out.WriteString("\r\n")
	}
	for _, h := range headers {
		out.WriteString(h.name)
		out.WriteString(": ")
		out.WriteString(h.value)
		out.WriteString("\r\n")
	}
	out.WriteString("\r\n")
	out.Write(body)
	return []byte(out.String()), nil
}

// deliver performs the real SMTP delivery: implicit TLS on port 465, STARTTLS on
// 587/25 (unless insecure_no_tls). It authenticates with PLAIN when a username is set.
func (o *Output) deliver(_ context.Context, msg []byte, from string, to []string) error {
	addr := net.JoinHostPort(o.host, strconv.Itoa(o.port))
	var auth smtp.Auth
	if o.username != "" {
		auth = smtp.PlainAuth("", o.username, o.password, o.host)
	}
	if o.insecureTLS {
		return smtp.SendMail(addr, auth, from, to, msg)
	}
	if o.port == 465 {
		return o.deliverImplicitTLS(addr, auth, from, to, msg)
	}
	// STARTTLS path: smtp.SendMail issues STARTTLS automatically when the server
	// advertises it, and PLAIN auth requires the connection to be TLS-protected first.
	return smtp.SendMail(addr, auth, from, to, msg)
}

// deliverImplicitTLS dials a TLS connection (port 465 implicit TLS) and runs the SMTP
// conversation over it.
func (o *Output) deliverImplicitTLS(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: o.host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, o.host)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// fromHeader returns the From header value with an optional display name.
func (o *Output) fromHeader() string {
	if o.fromName != "" {
		return fmt.Sprintf("%s <%s>", o.fromName, o.fromAddr)
	}
	return o.fromAddr
}

// listUnsubscribe builds the List-Unsubscribe header value: the HTTPS URI first (for
// one-click), then the mailto: fallback. Empty when neither is configured.
func (o *Output) listUnsubscribe() string {
	var parts []string
	if o.unsubURL != "" {
		parts = append(parts, "<"+o.unsubURL+">")
	}
	if o.unsubMailto != "" {
		m := o.unsubMailto
		if !strings.HasPrefix(m, "mailto:") {
			m = "mailto:" + m
		}
		parts = append(parts, "<"+m+">")
	}
	return strings.Join(parts, ", ")
}

// messageID returns a unique Message-ID using the From address domain.
func (o *Output) messageID() string {
	rid := o.rid
	if rid == nil {
		rid = randomID
	}
	id, err := rid()
	if err != nil {
		id = "0000000000000000"
	}
	return "<" + id + "@" + addrDomain(o.fromAddr) + ">"
}

// clock returns the connector's time source (injectable for tests).
func (o *Output) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// randomID returns 16 hex chars of cryptographic randomness for a Message-ID.
func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// subject returns the Title (falling back to the Body's first line), or a stable
// default so the message is never sent with an empty Subject.
func subject(n sdk.Notification) string {
	s := strings.TrimSpace(n.Title)
	if s == "" {
		s = firstLine(n.Body)
	}
	if s == "" {
		s = defaultSubject
	}
	return s
}

// renderBody renders the text/plain body: the Body text followed by a deterministic
// block of the non-sensitive structured fields (excluding the recipient field).
func renderBody(n sdk.Notification) []byte {
	var sb strings.Builder
	if n.Body != "" {
		sb.WriteString(n.Body)
		sb.WriteString("\r\n")
	}
	keys := make([]string, 0, len(n.Fields))
	for k := range n.Fields {
		if k == fieldTo {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 || n.Severity != "" || n.Tenant != "" {
		sb.WriteString("\r\n")
	}
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(n.Fields[k])
		sb.WriteString("\r\n")
	}
	if n.Severity != "" {
		sb.WriteString("severity: ")
		sb.WriteString(string(n.Severity))
		sb.WriteString("\r\n")
	}
	if n.Tenant != "" {
		sb.WriteString("tenant: ")
		sb.WriteString(n.Tenant)
		sb.WriteString("\r\n")
	}
	return []byte(sb.String())
}

// splitAddrs splits a comma-separated address list, trimming and dropping empties.
func splitAddrs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// addrDomain returns the domain part of an email address, or "localhost" if absent.
func addrDomain(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "localhost"
}

// firstLine returns the first line of s, trimmed.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
