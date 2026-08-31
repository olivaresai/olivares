// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package s3archive is the Olivares AI output connector that writes WORM
// (write-once-read-many) objects to an S3 Object Lock bucket. It is the
// archival sink of the records-management contract: the composition
// root adapts its exported Put face to core/audit's ArchiveSink so audit-ledger
// archive segments land as immutable objects, and its sdk.OutputConnector
// Notify face drops each notification as one locked object — the
// product's first true WORM sink (verified that s3cloudtrail is a
// read-only SOURCE; the filelog "WORM" posture only holds when the underlying
// filesystem is itself append-only).
//
// Compliance posture. Every write carries the S3 Object Lock headers: in
// COMPLIANCE mode (the default) no principal — not even the bucket owner or
// account root — can shorten the retention or delete the version until the
// retain-until date passes; GOVERNANCE mode is the weaker variant a principal
// holding s3:BypassGovernanceRetention can override; a legal hold protects
// indefinitely until explicitly lifted. Because the caller demanded
// immutability, the connector verifies after writing (verify_lock, default
// true): a signed HEAD of the just-written version must echo the
// x-amz-object-lock-* protection back, and a missing or weaker lock is an
// ERROR, never a silent success (fail-closed). Re-PUTting the same content to
// the same key — a crashed run recovering — simply creates another locked
// version under bucket versioning, which is harmless and is the documented
// idempotent recovery.
//
// Two internal layers do the generic work. connectors/internal/delivery does
// the reliable HTTP (backoff, honored Retry-After, retry only transient
// failures, secret-safe diagnostics via safeURL); connectors/internal/siemfmt
// encodes the Notify payloads (this package never re-implements an escaping or
// severity rule). SigV4 signing follows connectors/internal/awssig, extended
// in sign.go to cover the object-lock headers (S3 requires every x-amz-*
// request header in the signed set).
//
// Minimal data and secret discipline (docs/SECURITY-HARDENING.md-4). Archive segments carry
// only ledger events (ids, hashes, canonical meta) and notifications carry
// only non-sensitive displayable fields; this connector adds no enrichment.
// The AWS credentials live only in memory, ride only in the Authorization /
// X-Amz-Security-Token headers, and never appear in an error or a log: every
// HTTP failure is reported through delivery, which reduces URLs to
// scheme://host and never echoes headers. It imports only the SDK and the
// internal helpers, never the engine.
package s3archive

import (
	"context"
	"crypto/md5" // #nosec G501 -- S3 requires Content-MD5 on object-lock PUTs (integrity tag, not a security primitive)
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.s3archive"

// formatSet is this connector's slice of the sdk/siemwire format catalog: the
// notification-connector subset (json-first default, full dialect roster,
// otlp_envelope as the exact alias of otlp). Until the catalog, this connector's
// private const block was the ONE of the four notification connectors that
// lacked asim — not a decision anyone recorded, just drift among hand copies,
// which is exactly what deriving from the catalog ends. Equalized UP: asim is
// accepted and rendered by the same shared encoder the siblings already used.
func formatSet() siemwire.FormatSet { return siemwire.NotificationConnectorFormats() }

const (
	defaultMaxAttempts = 4
	// maxRetentionDays caps retention_days at 100 years, the same ceiling the
	// retention-policy model uses.
	maxRetentionDays = 36500
)

// Object Lock wire vocabulary (uppercase per the S3 API).
const (
	lockModeCompliance = "COMPLIANCE"
	lockModeGovernance = "GOVERNANCE"

	hdrLockMode        = "x-amz-object-lock-mode"
	hdrLockRetainUntil = "x-amz-object-lock-retain-until-date"
	hdrLockLegalHold   = "x-amz-object-lock-legal-hold"
	hdrVersionID       = "x-amz-version-id"
	hdrContentSHA256   = "x-amz-content-sha256"
)

// PutOptions tunes one archival write.
type PutOptions struct {
	// ContentSHA256 is the caller's hex SHA-256 of body. When set it is checked
	// against the body before anything is sent (an integrity mismatch is an
	// error, never silently corrected); when empty the connector computes it.
	ContentSHA256 string
	// RetainUntil is the object-lock retain-until date. Zero means "derive from
	// the configured retention_days", or — when that is 0 too — rely on the
	// bucket's default Object Lock retention.
	RetainUntil time.Time
	// LegalHold additionally places the object under an S3 legal hold
	// (indefinite protection until explicitly lifted). ORed with the connector's
	// legal_hold setting — over-preservation is the safe direction.
	LegalHold bool
}

// Receipt describes the written, lock-protected object. LockMode
// and RetainUntil reflect the verified protection when verify_lock ran, else
// the requested one; LockVerified is true only when a signed HEAD confirmed
// the lock.
type Receipt struct {
	Bucket, Key, ETag, VersionID, LockMode string
	RetainUntil                            time.Time
	LockVerified                           bool
}

// Output is the S3 Object Lock output connector. Open validates the full
// configuration once; Put writes one locked object (the archival face); Notify
// writes one notification as one locked object (the face); Close releases
// nothing (the connector is stateless after Open).
type Output struct {
	endpoint      string // "" => AWS virtual-host addressing; set => path-style (MinIO et al)
	region        string
	bucket        string
	prefix        string // normalized: no leading '/', trailing '/' when non-empty
	creds         awssig.Creds
	lockMode      string // wire form: COMPLIANCE | GOVERNANCE
	retentionDays int
	legalHold     bool
	verifyLock    bool
	format        siemwire.FormatToken // canonical encoder key, resolved at Open
	device        siemfmt.Device
	maxAttempts   int

	doer  delivery.Doer                                    // optional injected transport (tests); nil => http.DefaultClient
	sleep func(ctx context.Context, d time.Duration) error // injectable backoff sleep (tests)
	now   func() time.Time                                 // injectable clock for signing time and retain-until (tests)

	opened bool
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns an S3 archive output connector with default configuration
// (COMPLIANCE mode, verify-after-write on, JSON notifications).
func New() *Output {
	return &Output{
		lockMode:    lockModeCompliance,
		verifyLock:  true,
		format:      siemwire.Canonical(formatSet().Default()),
		maxAttempts: defaultMaxAttempts,
		now:         time.Now,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "S3 archive (Object Lock)",
		Description: "Writes audit-archive segments and notifications as S3 Object Lock (WORM) protected objects, with SigV4 auth and fail-closed verify-after-write.",
		ConfigFields: []sdk.ConfigField{
			{Key: "endpoint", Type: sdk.FieldString, Description: "Custom S3 endpoint base URL (MinIO and friends; path-style addressing). Empty = AWS virtual-host addressing (https://<bucket>.s3.<region>.amazonaws.com)."},
			{Key: "region", Type: sdk.FieldString, Required: true, Description: "Signing region (SigV4 credential scope), e.g. eu-west-1."},
			{Key: "bucket", Type: sdk.FieldString, Required: true, Description: "Target bucket. Object Lock must be enabled on it (which implies versioning)."},
			{Key: "prefix", Type: sdk.FieldString, Description: "Key prefix for every object written (a trailing '/' is added when missing)."},
			{Key: "access_key_id", Type: sdk.FieldString, Required: true, Description: "AWS access key id."},
			{Key: "secret_access_key", Type: sdk.FieldString, Required: true, Secret: true, Description: "AWS secret access key. Held in memory only, never logged."},
			{Key: "session_token", Type: sdk.FieldString, Secret: true, Description: "Optional STS session token (temporary credentials). Held in memory only, never logged."},
			{Key: "lock_mode", Type: sdk.FieldString, Default: "compliance", Description: "Object Lock retention mode: compliance (immutable for everyone) | governance (bypassable with s3:BypassGovernanceRetention)."},
			{Key: "retention_days", Type: sdk.FieldInt, Default: "0", Description: "Default retention period in days when a Put carries no retain-until (and for every notification). 0 = rely on the bucket's default Object Lock retention."},
			{Key: "legal_hold", Type: sdk.FieldBool, Default: "false", Description: "Place every object under an S3 legal hold in addition to retention."},
			{Key: "verify_lock", Type: sdk.FieldBool, Default: "true", Description: "Verify after write: HEAD the written version and fail unless it confirms the requested object-lock protection (fail-closed)."},
			{Key: "format", Type: sdk.FieldString, Default: string(formatSet().Default()), Description: "Notification object format: " + strings.ReplaceAll(formatSet().List(), "|", " | ") + ". otlp_envelope is an exact alias of otlp (identical bytes)."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Maximum HTTP delivery attempts (including the first) per request."},
		},
	}
}

// Open reads and validates the FULL configuration — endpoint shape, region,
// bucket name, credentials, lock_mode enum, retention bounds and the boolean
// flags — so a misconfigured WORM sink fails at provisioning time, never at
// archival time.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	tok, err := siemfmt.ResolveFormat(formatSet(), cfg.Get("format"))
	if err != nil {
		return fmt.Errorf("s3archive: %w", err)
	}
	o.format = tok

	o.endpoint = strings.TrimRight(strings.TrimSpace(cfg.Get("endpoint")), "/")
	if o.endpoint != "" {
		u, err := url.Parse(o.endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("s3archive: endpoint must be an absolute http(s) URL")
		}
	}
	o.region = strings.TrimSpace(cfg.Get("region"))
	if o.region == "" {
		return fmt.Errorf("s3archive: region is required")
	}
	o.bucket = strings.TrimSpace(cfg.Get("bucket"))
	if !validBucketName(o.bucket) {
		return fmt.Errorf("s3archive: bucket is required and must be a valid S3 bucket name")
	}
	o.prefix = strings.TrimLeft(strings.TrimSpace(cfg.Get("prefix")), "/")
	if o.prefix != "" && !strings.HasSuffix(o.prefix, "/") {
		o.prefix += "/"
	}

	o.creds = awssig.Creds{
		AKID:   strings.TrimSpace(cfg.Get("access_key_id")),
		Secret: cfg.Get("secret_access_key"),
		Token:  cfg.Get("session_token"),
	}
	if o.creds.AKID == "" {
		return fmt.Errorf("s3archive: access_key_id is required")
	}
	if o.creds.Secret == "" {
		return fmt.Errorf("s3archive: secret_access_key is required")
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Get("lock_mode"))) {
	case "", "compliance":
		o.lockMode = lockModeCompliance
	case "governance":
		o.lockMode = lockModeGovernance
	default:
		return fmt.Errorf("s3archive: unknown lock_mode %q (want compliance|governance)", cfg.Get("lock_mode"))
	}

	// Retention/lock settings are validated strictly (no silent fallback on a
	// typo): a WORM sink that quietly weakens its protection is worse than one
	// that refuses to open.
	if v, ok := cfg.Lookup("retention_days"); ok && strings.TrimSpace(v) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 || n > maxRetentionDays {
			return fmt.Errorf("s3archive: retention_days must be an integer in [0, %d]", maxRetentionDays)
		}
		o.retentionDays = n
	}
	if o.legalHold, err = boolSetting(cfg, "legal_hold", false); err != nil {
		return err
	}
	if o.verifyLock, err = boolSetting(cfg, "verify_lock", true); err != nil {
		return err
	}

	o.device = siemfmt.DefaultDevice()
	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)
	o.opened = true
	return nil
}

// Put writes body as one object-lock-protected object at key (under the
// configured prefix) and returns the receipt. It is the archival face the
// composition root adapts to core/audit's ArchiveSink. The write
// is fail-closed end to end: a declared-hash mismatch aborts before any bytes
// leave the process, and — with verify_lock on — a write whose lock the
// destination does not confirm is an error, not a receipt.
func (o *Output) Put(ctx context.Context, key string, body []byte, opts PutOptions) (Receipt, error) {
	return o.put(ctx, key, body, "application/octet-stream", opts)
}

// Notify writes one notification as one locked object under
// <prefix>notifications/<tenant>/<RFC3339-time>-<type>.<ext>, encoded by
// siemfmt in the configured format. The key derives from the notification's
// OWN time (never the wall clock), so a redelivery of the same notification
// lands on the same key and just adds another locked version (the documented
// idempotent recovery).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if !o.opened {
		return fmt.Errorf("s3archive: connector not opened")
	}
	body, ext, contentType, err := o.encodeNotification(n)
	if err != nil {
		return err
	}
	tenant := n.Tenant
	if tenant == "" {
		tenant = "unknown"
	}
	typ := n.Type
	if typ == "" {
		typ = "notification"
	}
	key := "notifications/" + tenant + "/" + n.Time.UTC().Format(time.RFC3339) + "-" + typ + ext
	_, err = o.put(ctx, key, body, contentType, PutOptions{})
	return err
}

// Close releases resources; this connector holds none beyond in-memory
// configuration.
func (o *Output) Close(context.Context) error { return nil }

// lockIntent is the object-lock protection requested for one write — and, on
// the verify side, the protection the destination reported back.
type lockIntent struct {
	mode        string // wire form; set iff retainUntil is set (S3 requires the pair)
	retainUntil time.Time
	legalHold   bool
}

// lockFor resolves the effective protection for one write: an explicit
// RetainUntil wins, else retention_days from now, else the bucket's default
// retention; legal hold is the OR of the option and the connector setting.
// The retain-until is ceiled to whole seconds (the header is second-precision
// RFC3339, and rounding up over-preserves).
func (o *Output) lockFor(opts PutOptions) lockIntent {
	li := lockIntent{retainUntil: opts.RetainUntil, legalHold: opts.LegalHold || o.legalHold}
	if li.retainUntil.IsZero() && o.retentionDays > 0 {
		li.retainUntil = o.now().Add(time.Duration(o.retentionDays) * 24 * time.Hour)
	}
	if !li.retainUntil.IsZero() {
		li.retainUntil = ceilSecond(li.retainUntil.UTC())
		li.mode = o.lockMode
	}
	return li
}

// put performs one signed, lock-protected PUT (plus the verify-after-write
// HEAD) and assembles the receipt.
func (o *Output) put(ctx context.Context, key string, body []byte, contentType string, opts PutOptions) (Receipt, error) {
	if !o.opened {
		return Receipt{}, fmt.Errorf("s3archive: connector not opened")
	}
	key = strings.TrimPrefix(key, "/")
	if key == "" {
		return Receipt{}, fmt.Errorf("s3archive: object key is required")
	}
	fullKey := o.prefix + key

	sum := awssig.HexSHA256(body)
	if opts.ContentSHA256 != "" && !strings.EqualFold(strings.TrimSpace(opts.ContentSHA256), sum) {
		return Receipt{}, fmt.Errorf("s3archive: content hash mismatch for %s: body is %s, caller declared %s", fullKey, sum, opts.ContentSHA256)
	}

	li := o.lockFor(opts)
	md5sum := md5.Sum(body) // #nosec G401 -- Content-MD5 is mandatory on object-lock PUTs (integrity, not confidentiality)
	hdr := map[string]string{
		"Content-Type":   contentType,
		"Content-MD5":    base64.StdEncoding.EncodeToString(md5sum[:]),
		hdrContentSHA256: sum,
	}
	if li.mode != "" {
		hdr[hdrLockMode] = li.mode
		hdr[hdrLockRetainUntil] = li.retainUntil.Format(time.RFC3339)
	}
	if li.legalHold {
		hdr[hdrLockLegalHold] = "ON"
	}

	req, err := o.signedRequest(http.MethodPut, o.objectURL(fullKey), body, hdr)
	if err != nil {
		return Receipt{}, err
	}
	capture := &captureDoer{doer: o.transport()}
	if _, err := delivery.New(capture, o.deliveryOptions()).Send(ctx, req); err != nil {
		return Receipt{}, fmt.Errorf("s3archive: put %s: %w", fullKey, err)
	}

	rec := Receipt{
		Bucket:      o.bucket,
		Key:         fullKey,
		ETag:        strings.Trim(capture.hdr.Get("ETag"), `"`),
		VersionID:   capture.hdr.Get(hdrVersionID),
		LockMode:    li.mode,
		RetainUntil: li.retainUntil,
	}
	if !o.verifyLock {
		return rec, nil
	}
	got, err := o.verifyObjectLock(ctx, fullKey, rec.VersionID, li)
	if err != nil {
		return Receipt{}, err
	}
	rec.LockVerified = true
	if got.mode != "" {
		rec.LockMode = got.mode
	}
	if !got.retainUntil.IsZero() {
		rec.RetainUntil = got.retainUntil
	}
	return rec, nil
}

// verifyObjectLock HEADs the written version and requires the response's
// x-amz-object-lock-* headers to confirm the requested protection. Every
// branch is deny-shaped: a requested retention must come back in the same mode
// with an equal-or-later retain-until, a requested legal hold must come back
// ON, and when nothing was requested explicitly (the bucket's default
// retention was relied on) the object must still carry SOME lock — the caller
// chose a WORM sink, so "no protection" is a failure, not a shrug.
func (o *Output) verifyObjectLock(ctx context.Context, fullKey, versionID string, want lockIntent) (lockIntent, error) {
	target := o.objectURL(fullKey)
	if versionID != "" {
		target += "?versionId=" + awssig.URIEncode(versionID, true)
	}
	req, err := o.signedRequest(http.MethodHead, target, nil, map[string]string{hdrContentSHA256: awssig.HexSHA256(nil)})
	if err != nil {
		return lockIntent{}, err
	}
	capture := &captureDoer{doer: o.transport()}
	if _, err := delivery.New(capture, o.deliveryOptions()).Send(ctx, req); err != nil {
		return lockIntent{}, fmt.Errorf("s3archive: verify lock for %s: %w", fullKey, err)
	}

	got := lockIntent{
		mode:      capture.hdr.Get(hdrLockMode),
		legalHold: strings.EqualFold(capture.hdr.Get(hdrLockLegalHold), "ON"),
	}
	if v := capture.hdr.Get(hdrLockRetainUntil); v != "" {
		ts, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return lockIntent{}, fmt.Errorf("s3archive: verify lock for %s: unparseable retain-until %q", fullKey, v)
		}
		got.retainUntil = ts.UTC()
	}

	if want.mode != "" {
		if got.mode != want.mode {
			return lockIntent{}, fmt.Errorf("s3archive: object lock not confirmed for %s: mode %q, want %q (immutability was requested)", fullKey, got.mode, want.mode)
		}
		if got.retainUntil.IsZero() || got.retainUntil.Before(want.retainUntil) {
			return lockIntent{}, fmt.Errorf("s3archive: object lock not confirmed for %s: retain-until %q is missing or earlier than requested %s", fullKey, capture.hdr.Get(hdrLockRetainUntil), want.retainUntil.Format(time.RFC3339))
		}
	}
	if want.legalHold && !got.legalHold {
		return lockIntent{}, fmt.Errorf("s3archive: object lock not confirmed for %s: legal hold is not ON", fullKey)
	}
	if want.mode == "" && !want.legalHold && got.mode == "" && !got.legalHold {
		return lockIntent{}, fmt.Errorf("s3archive: object lock not confirmed for %s: no retention and no legal hold (is the bucket's default Object Lock retention configured?)", fullKey)
	}
	return got, nil
}

// signedRequest builds the SigV4-signed delivery request: the headers are set
// on a throwaway http.Request, signV4 covers ALL of them (the object-lock
// headers must be tamper-protected, see sign.go), and the signed header set is
// copied onto the delivery request. The signature is computed once and reused
// across delivery's in-call retries — those span seconds, well inside SigV4's
// 15-minute clock-skew window.
func (o *Output) signedRequest(method, rawURL string, body []byte, hdr map[string]string) (delivery.Request, error) {
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return delivery.Request{}, fmt.Errorf("s3archive: build %s request: %w", method, err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	signV4(req, "s3", o.region, o.creds, o.now())
	out := make(map[string]string, len(req.Header))
	for k := range req.Header {
		out[k] = req.Header.Get(k)
	}
	return delivery.Request{Method: method, URL: rawURL, Header: out, Body: body}, nil
}

// objectURL returns the request URL for fullKey: virtual-host addressing on
// AWS (empty endpoint), path-style on a custom endpoint (MinIO et al). The key
// is URI-encoded once, per the SigV4 canonical-form rules, so the wire path
// and the canonical request path are byte-identical (an RFC3339 ':' in a
// notification key would otherwise sign differently than it travels).
func (o *Output) objectURL(fullKey string) string {
	enc := awssig.URIEncode(fullKey, false)
	if o.endpoint == "" {
		return "https://" + o.bucket + ".s3." + o.region + ".amazonaws.com/" + enc
	}
	return o.endpoint + "/" + o.bucket + "/" + enc
}

// transport resolves the HTTP transport (the injected test doer, or the
// default client).
func (o *Output) transport() delivery.Doer {
	if o.doer != nil {
		return o.doer
	}
	return http.DefaultClient
}

// deliveryOptions is the per-request retry policy. A fresh delivery.Client is
// built per request (it is two words) because the response HEADERS — the
// version id, the lock confirmation — are read via a per-call captureDoer,
// keeping concurrent Put/Notify calls race-free.
func (o *Output) deliveryOptions() delivery.Options {
	return delivery.Options{MaxAttempts: o.maxAttempts, Sleep: o.sleep}
}

// captureDoer records the headers of the most recent response so the caller
// can read what delivery's Result does not expose (ETag, x-amz-version-id,
// x-amz-object-lock-*). On a retried request the final — successful — response
// wins.
type captureDoer struct {
	doer delivery.Doer
	hdr  http.Header
}

func (c *captureDoer) Do(req *http.Request) (*http.Response, error) {
	res, err := c.doer.Do(req)
	if err == nil {
		c.hdr = res.Header.Clone()
	}
	return res, err
}

// encodeNotification renders n in the configured format and returns the body,
// the key extension and the Content-Type. All formatting is siemfmt's; json is
// the canonical one-line notification object every output connector ships.
func (o *Output) encodeNotification(n sdk.Notification) (body []byte, ext, contentType string, err error) {
	switch o.format {
	case siemwire.TokenCEF:
		return []byte(siemfmt.CEF(o.device, n)), ".cef", "text/plain", nil
	case siemwire.TokenLEEF:
		return []byte(siemfmt.LEEF(o.device, n)), ".leef", "text/plain", nil
	case siemwire.TokenSyslog:
		return []byte(siemfmt.Syslog5424(o.device, siemfmt.SyslogOptions{}, n)), ".log", "text/plain", nil
	case siemwire.TokenOTLP:
		b, err := siemfmt.OTLPLogJSON(o.device, n)
		return b, ".json", "application/json", err
	case siemwire.TokenOCSF:
		b, err := siemfmt.OCSF(o.device, n)
		return b, ".json", "application/json", err
	case siemwire.TokenASIM:
		b, err := siemfmt.ASIMAgentEvent(o.device, n)
		return b, ".json", "application/json", err
	case siemwire.TokenJSON:
		b, err := json.Marshal(notificationJSON(n))
		if err != nil {
			return nil, "", "", fmt.Errorf("s3archive: marshal notification json: %w", err)
		}
		return b, ".json", "application/json", nil
	default:
		// Deny-closed: an unrecognized stored value is an error, never a silent
		// JSON relabel (the four notification connectors agree on this now).
		return nil, "", "", fmt.Errorf("s3archive: unrecognized stored format %q", o.format)
	}
}

// boolSetting parses an optional boolean setting strictly: absent/empty yields
// the default, anything else must be a valid bool (a typo in a lock-related
// flag must not silently weaken the protection).
func boolSetting(cfg sdk.Config, key string, def bool) (bool, error) {
	v, ok := cfg.Lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("s3archive: %s must be a boolean, got %q", key, v)
	}
	return b, nil
}

// validBucketName checks the S3 bucket-name grammar this connector relies on
// for addressing: 3-63 chars of lowercase letters, digits, dots and hyphens,
// starting and ending alphanumeric.
func validBucketName(b string) bool {
	if len(b) < 3 || len(b) > 63 {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '.' || c == '-') && i > 0 && i < len(b)-1:
		default:
			return false
		}
	}
	return true
}

// ceilSecond rounds t UP to a whole second (truncating would shorten the
// retention by up to a second; over-preserving is the safe direction).
func ceilSecond(t time.Time) time.Time {
	tt := t.Truncate(time.Second)
	if tt.Equal(t) {
		return tt
	}
	return tt.Add(time.Second)
}

// notificationView is the canonical one-line JSON shape (the same flat,
// non-sensitive projection the webhook/SIEM/filelog connectors ship).
type notificationView struct {
	Type     string            `json:"type,omitempty"`
	Title    string            `json:"title,omitempty"`
	Body     string            `json:"body,omitempty"`
	Severity string            `json:"severity,omitempty"`
	Tenant   string            `json:"tenant,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	Time     string            `json:"time,omitempty"`
}

// notificationJSON projects an sdk.Notification onto the wire view. The time
// is rendered RFC3339 (UTC) and dropped when zero.
func notificationJSON(n sdk.Notification) notificationView {
	v := notificationView{
		Type:     n.Type,
		Title:    n.Title,
		Body:     n.Body,
		Severity: severityString(n.Severity),
		Tenant:   n.Tenant,
		Fields:   n.Fields,
	}
	if !n.Time.IsZero() {
		v.Time = n.Time.UTC().Format(time.RFC3339)
	}
	return v
}

// severityString renders the model severity as its lowercase label, or "" for
// an empty/unknown severity so it is omitted from the JSON.
func severityString(s model.Severity) string {
	switch s {
	case model.SeverityInfo:
		return "info"
	case model.SeverityLow:
		return "low"
	case model.SeverityMedium:
		return "medium"
	case model.SeverityHigh:
		return "high"
	case model.SeverityCritical:
		return "critical"
	default:
		return ""
	}
}
