// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

// Offsite replication is the "1" in 3-2-1: a DR bundle kept on the same host or
// cluster it protects is not disaster recovery. This is a minimal, dependency-free
// S3-compatible client (AWS S3, Cloudflare R2, MinIO, Wasabi, …) that PUTs a bundle
// off-box, LISTs the offsite copies, GETs one back for a restore, and DELETEs one
// for retention — signed with AWS Signature Version 4.
//
// # Why core carries its own SigV4 rather than importing the connector's
//
// The connector catalog already has a proven SigV4 signer (connectors/internal/
// awssig), but it is Apache-licensed connector code behind an internal/ boundary:
// the AGPL core must not import it (scripts/check-boundary.sh, LICENSING.md). SigV4 is a
// public, stable algorithm, so core keeps a small self-contained implementation
// here — byte-for-byte the same canonical-request/signing-key construction, proven
// against the AWS published key-derivation test vector (offsite_test.go). The
// credentials live only in memory and are never logged.
//
// # Streaming, not buffering
//
// A bundle can be large (the whole store snapshot). PUT signs with the
// UNSIGNED-PAYLOAD content hash — valid over HTTPS — so the body streams straight
// from disk without a second full read to compute a payload digest, and the
// bundle's own manifest SHA-256 (verified on restore) remains the integrity anchor.
// GET/LIST/DELETE have empty bodies and sign the empty-payload hash.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	offsiteAlgorithm     = "AWS4-HMAC-SHA256"
	offsiteAmzDateFmt    = "20060102T150405Z"
	offsiteShortDateFmt  = "20060102"
	offsiteService       = "s3"
	offsiteDefaultRegion = "us-east-1"
	offsiteUnsignedBody  = "UNSIGNED-PAYLOAD"
	offsiteEmptyBodyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	offsiteDefaultTO     = 5 * time.Minute
	// offsiteListMax bounds a single ListObjectsV2 page; pagination follows the
	// continuation token until the bucket prefix is exhausted.
	offsiteListMax = 1000
)

// OffsiteConfig points at an S3-compatible destination for DR bundles. Endpoint is
// empty for AWS S3 (derived from Region); set it for R2/MinIO/Wasabi (then requests
// use path-style addressing, which those endpoints require). Credentials are the
// standard access-key/secret pair, plus an optional STS session token; the caller
// resolves them from a secret reference (env/file/KMS) — this struct only holds the
// resolved values, which stay in memory.
type OffsiteConfig struct {
	Endpoint        string
	Bucket          string
	Region          string
	Prefix          string
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// Timeout bounds each HTTP request (0 → offsiteDefaultTO). A bundle PUT can be
	// large, so this is generous by default.
	Timeout time.Duration
}

// OffsiteObject is one bundle in the offsite store (a ListObjectsV2 entry, scoped to
// the configured prefix). Name is the object key with the prefix stripped, so it
// matches a local bundle filename.
type OffsiteObject struct {
	Key          string
	Name         string
	Size         int64
	LastModified time.Time
}

// OffsiteClient talks to one S3-compatible bucket/prefix. It is safe for concurrent
// use (its http.Client is).
type OffsiteClient struct {
	http     *http.Client
	cfg      OffsiteConfig
	endpoint string
	now      func() time.Time
}

// NewOffsiteClient validates the configuration and builds a client. It requires a
// bucket and both credential halves (a DR bundle carries the ledger signing keys —
// pushing it anonymously would be a silent security downgrade). Region defaults to
// us-east-1 (R2 accepts "auto"); a custom Endpoint forces path-style addressing.
func NewOffsiteClient(cfg OffsiteConfig) (*OffsiteClient, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("dr offsite: bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, fmt.Errorf("dr offsite: access key id and secret access key are required (a DR bundle carries signing keys — it is never pushed anonymously)")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = offsiteDefaultRegion
		cfg.Region = region
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", region)
	} else {
		// A custom endpoint (R2/MinIO/Wasabi) is addressed path-style.
		cfg.PathStyle = true
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = offsiteDefaultTO
	}
	return &OffsiteClient{
		http:     &http.Client{Timeout: timeout},
		cfg:      cfg,
		endpoint: endpoint,
		now:      time.Now,
	}, nil
}

// objectKey joins the configured prefix with a bundle name (e.g. "olivares-….drbundle").
func (c *OffsiteClient) objectKey(name string) string {
	p := strings.Trim(strings.TrimSpace(c.cfg.Prefix), "/")
	name = strings.TrimLeft(name, "/")
	if p == "" {
		return name
	}
	return p + "/" + name
}

// Put streams size bytes from r to the offsite object for name. It signs with
// UNSIGNED-PAYLOAD so the body is never buffered to compute a digest; the bundle's
// manifest SHA-256 is the integrity anchor a restore checks.
func (c *OffsiteClient) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	key := c.objectKey(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.objectURL(key, ""), r)
	if err != nil {
		return fmt.Errorf("dr offsite: build PUT: %w", err)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	c.sign(req, offsiteUnsignedBody)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dr offsite: PUT %s: %w", key, err)
	}
	return closeExpecting(resp, http.StatusOK)
}

// Get opens the offsite object for name for reading. The caller closes the reader.
func (c *OffsiteClient) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	key := c.objectKey(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.objectURL(key, ""), nil)
	if err != nil {
		return nil, fmt.Errorf("dr offsite: build GET: %w", err)
	}
	c.sign(req, offsiteEmptyBodyHash)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dr offsite: GET %s: %w", key, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, key)
	}
	return resp.Body, nil
}

// Delete removes the offsite object for name (retention pruning). A missing object
// is not an error (S3 DELETE is idempotent — 204 whether or not the key existed).
func (c *OffsiteClient) Delete(ctx context.Context, name string) error {
	key := c.objectKey(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.objectURL(key, ""), nil)
	if err != nil {
		return fmt.Errorf("dr offsite: build DELETE: %w", err)
	}
	c.sign(req, offsiteEmptyBodyHash)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dr offsite: DELETE %s: %w", key, err)
	}
	return closeExpecting(resp, http.StatusNoContent, http.StatusOK)
}

// List returns every bundle under the configured prefix, following ListObjectsV2
// continuation tokens so a large offsite store is fully enumerated. Entries are
// sorted newest-first (by LastModified) to match the local listing.
func (c *OffsiteClient) List(ctx context.Context) ([]OffsiteObject, error) {
	prefix := strings.Trim(strings.TrimSpace(c.cfg.Prefix), "/")
	var out []OffsiteObject
	var token string
	for {
		vals := url.Values{}
		vals.Set("list-type", "2")
		vals.Set("max-keys", fmt.Sprintf("%d", offsiteListMax))
		if prefix != "" {
			vals.Set("prefix", prefix+"/")
		}
		if token != "" {
			vals.Set("continuation-token", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.bucketURL(vals.Encode()), nil)
		if err != nil {
			return nil, fmt.Errorf("dr offsite: build LIST: %w", err)
		}
		c.sign(req, offsiteEmptyBodyHash)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("dr offsite: LIST: %w", err)
		}
		body, err := readAllExpecting(resp, "list")
		if err != nil {
			return nil, err
		}
		var parsed offsiteListResult
		if err := xml.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("dr offsite: parse ListObjectsV2: %w", err)
		}
		for _, o := range parsed.Contents {
			key := strings.TrimSpace(o.Key)
			if key == "" || strings.HasSuffix(key, "/") {
				continue // skip the prefix "directory" marker if present
			}
			out = append(out, OffsiteObject{
				Key:          key,
				Name:         keyName(key, prefix),
				Size:         o.Size,
				LastModified: parseAmzTime(o.LastModified),
			})
		}
		if !parsed.IsTruncated || strings.TrimSpace(parsed.NextContinuationToken) == "" {
			break
		}
		token = strings.TrimSpace(parsed.NextContinuationToken)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastModified.After(out[j].LastModified) })
	return out, nil
}

// ---- signing ---------------------------------------------------------------

// sign applies AWS SigV4 to req in place, signing host;x-amz-content-sha256;
// x-amz-date (plus x-amz-security-token when a session token is set). payloadHash is
// the hex SHA-256 of the body, or the sentinel "UNSIGNED-PAYLOAD" for a streamed PUT.
func (c *OffsiteClient) sign(req *http.Request, payloadHash string) {
	t := c.now().UTC()
	amzDate := t.Format(offsiteAmzDateFmt)
	shortDate := t.Format(offsiteShortDateFmt)

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.cfg.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.cfg.SessionToken)
	}

	signedHeaders, canonicalHeaders := c.canonicalHeaders(req)
	canonicalReq := strings.Join([]string{
		req.Method,
		canonicalURIPath(req.URL.EscapedPath()),
		canonicalQuery(req.URL.RawQuery),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{shortDate, c.cfg.Region, offsiteService, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		offsiteAlgorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalReq)),
	}, "\n")

	key := sigV4SigningKey(c.cfg.SecretAccessKey, shortDate, c.cfg.Region, offsiteService)
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", offsiteAlgorithm+
		" Credential="+c.cfg.AccessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+signature)
}

func (c *OffsiteClient) canonicalHeaders(req *http.Request) (signed, canonical string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	pairs := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": req.Header.Get("X-Amz-Content-Sha256"),
		"x-amz-date":           req.Header.Get("X-Amz-Date"),
	}
	if c.cfg.SessionToken != "" {
		pairs["x-amz-security-token"] = req.Header.Get("X-Amz-Security-Token")
	}
	names := make([]string, 0, len(pairs))
	for n := range pairs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(strings.Join(strings.Fields(pairs[n]), " "))
		b.WriteByte('\n')
	}
	return strings.Join(names, ";"), b.String()
}

// objectURL builds the request URL for an object key (path- or vhost-style).
func (c *OffsiteClient) objectURL(key, rawQuery string) string {
	base, _ := url.Parse(c.endpoint)
	u := *base
	escKey := uriEncodePath(key)
	if c.cfg.PathStyle {
		u.Path = "/" + c.cfg.Bucket + "/" + key
		u.RawPath = "/" + uriEncodePath(c.cfg.Bucket) + "/" + escKey
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
		u.Path = "/" + key
		u.RawPath = "/" + escKey
	}
	u.RawQuery = rawQuery
	return u.String()
}

// bucketURL builds the request URL for a bucket-level operation (ListObjectsV2).
func (c *OffsiteClient) bucketURL(rawQuery string) string {
	base, _ := url.Parse(c.endpoint)
	u := *base
	if c.cfg.PathStyle {
		u.Path = "/" + c.cfg.Bucket
		u.RawPath = "/" + uriEncodePath(c.cfg.Bucket)
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
		u.Path = "/"
		u.RawPath = "/"
	}
	u.RawQuery = rawQuery
	return u.String()
}

// ---- SigV4 primitives (self-contained; proven against the AWS KAT) ----------

func sigV4SigningKey(secret, shortDate, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(shortDate))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func canonicalURIPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	type kv struct{ k, v string }
	pairs := make([]kv, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		kd, kerr := url.QueryUnescape(k)
		vd, verr := url.QueryUnescape(v)
		if kerr != nil || verr != nil {
			kd, vd = k, v
		}
		pairs = append(pairs, kv{uriEncodeComponent(kd), uriEncodeComponent(vd)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

func uriEncodePath(s string) string      { return uriEncode(s, false) }
func uriEncodeComponent(s string) string { return uriEncode(s, true) }

// uriEncode percent-encodes s per the SigV4 rules (unreserved A-Z a-z 0-9 - _ . ~
// pass through; '/' passes through in path mode).
func uriEncode(s string, encodeSlash bool) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
	return b.String()
}

// ---- response helpers ------------------------------------------------------

func closeExpecting(resp *http.Response, okStatuses ...int) error {
	defer func() { _ = resp.Body.Close() }()
	for _, s := range okStatuses {
		if resp.StatusCode == s {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			return nil
		}
	}
	return statusError(resp, "")
}

func readAllExpecting(resp *http.Response, what string) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, what)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// statusError turns a non-2xx S3 response into an error, folding in the (bounded)
// response body, which for S3 is an <Error><Code>…</Code></Error> document.
func statusError(resp *http.Response, what string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_ = resp.Body.Close()
	msg := strings.TrimSpace(string(body))
	if code := xmlErrorCode(body); code != "" {
		msg = code
	}
	if what != "" {
		return fmt.Errorf("dr offsite: %s returned HTTP %d: %s", what, resp.StatusCode, msg)
	}
	return fmt.Errorf("dr offsite: HTTP %d: %s", resp.StatusCode, msg)
}

func xmlErrorCode(body []byte) string {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &e); err != nil {
		return ""
	}
	if e.Code == "" {
		return ""
	}
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

type offsiteListResult struct {
	Contents              []offsiteListEntry `xml:"Contents"`
	IsTruncated           bool               `xml:"IsTruncated"`
	NextContinuationToken string             `xml:"NextContinuationToken"`
}

type offsiteListEntry struct {
	Key          string `xml:"Key"`
	Size         int64  `xml:"Size"`
	LastModified string `xml:"LastModified"`
}

func keyName(key, prefix string) string {
	if prefix != "" {
		key = strings.TrimPrefix(key, prefix+"/")
	}
	return key
}

func parseAmzTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
