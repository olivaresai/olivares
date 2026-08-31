// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package schemaregistry resolves the schema a Kafka (or compatible) message
// declares and extracts the message's STRUCTURE — never its content (docs/SECURITY-HARDENING.md). It is the minimal-data (de)serialization seam: a connector reads the
// few framing bytes that name the schema, looks the schema up in the operator's
// Schema Registry (Confluent/Redpanda, read-only), and derives natural references
// (the data-contract subject, the record/message name, its field names) to emit
// topology edges. It deliberately does NOT decode the Avro/Protobuf payload body:
// the SCHEMA is the contract structure we observe; the BODY is the data we must
// never read. So a connector learns "topic X carries records of contract
// orders-value" without ever touching a customer field value.
//
// It handles BOTH wire formats that coexist in 2026:
//
//   - The CLASSIC value-prefix: a magic byte 0x00, a 4-byte big-endian schema id,
//     then the payload. For Protobuf an extra message-index varint array precedes
//     the payload. (HIGH-confidence, docs.confluent.io serdes.)
//   - The newer HEADER-GUID form: the schema reference is a 16-byte GUID carried in
//     a Kafka RECORD HEADER instead of the value prefix. IMPORTANT HONESTY
//: the 16-byte GUID, its placement in record headers, and the
//     header-first read path ARE documented; the exact header KEY NAME and whether
//     a version byte precedes the GUID are NOT specified in primary Confluent docs
//     as of 2026. This package therefore reads the GUID from an operator-configured
//     header key when given, else recognizes a header value that is a 16-byte GUID
//     (optionally preceded by one version/format byte) — and the contract documents
//     this as a heuristic, not a fabricated spec constant.
//
// It is stdlib-only and read-only; the registry credential is held in memory and
// applied per request, never logged or emitted. It imports no engine package.
package schemaregistry

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// magicByte is the first byte of the classic Confluent value-prefix wire format.
const magicByte = 0x00

// guidLen is the length of a Confluent schema GUID (a schema fingerprint).
const guidLen = 16

// ErrNotWireFormatted reports that a value does not begin with the classic
// magic-byte prefix, so it carries no value-embedded schema id (it may still carry
// a header GUID, or be plain bytes).
var ErrNotWireFormatted = errors.New("schemaregistry: not a magic-byte-prefixed value")

// Reference names a schema by the way the message referenced it: either the classic
// 4-byte integer id (value prefix) or the 16-byte GUID (record header). Exactly one
// form is set; HasGUID distinguishes them.
type Reference struct {
	// IntID is the classic 4-byte schema id (valid when HasGUID is false).
	IntID uint32
	// GUID is the 16-byte schema GUID (valid when HasGUID is true).
	GUID [guidLen]byte
	// HasGUID is true when the reference is a GUID rather than an int id.
	HasGUID bool
}

// String renders the reference for an edge ToolRef without leaking anything
// sensitive (a schema id/GUID is not a secret).
func (r Reference) String() string {
	if r.HasGUID {
		return "guid:" + hexLower(r.GUID[:])
	}
	return fmt.Sprintf("id:%d", r.IntID)
}

// ParseConfluentValue reads the classic value-prefix: a 0x00 magic byte, a 4-byte
// big-endian schema id, then the remaining payload. It returns the schema
// reference and the payload that FOLLOWS the prefix (which the caller does NOT
// decode — minimal-data). For a Protobuf payload the returned rest still leads with
// the message-index varint array; ParseProtobufIndexes peels that if a caller wants
// the index path. A value that is not magic-byte-prefixed yields
// ErrNotWireFormatted so the caller can fall back to a header GUID or treat the
// message as unstructured.
func ParseConfluentValue(value []byte) (Reference, []byte, error) {
	if len(value) < 5 || value[0] != magicByte {
		return Reference{}, nil, ErrNotWireFormatted
	}
	id := binary.BigEndian.Uint32(value[1:5])
	return Reference{IntID: id}, value[5:], nil
}

// ParseProtobufIndexes peels the Confluent Protobuf message-index array that
// follows the 4-byte id (returned as the rest of ParseConfluentValue). The array is
// a zig-zag varint length followed by that many zig-zag varint indexes; the common
// single-[0] case is optimized on the wire to a single 0 byte (length 0 ⇒ the first
// message). It returns the index path and the payload after it. It never decodes the
// payload. On a malformed prefix it returns a nil path and the input unchanged.
func ParseProtobufIndexes(rest []byte) (indexes []int, payload []byte) {
	n, w := readZigZag(rest)
	if w == 0 {
		return nil, rest
	}
	if n == 0 {
		// Optimized single [0]: length encoded as 0 means "the first message type".
		return []int{0}, rest[w:]
	}
	pos := w
	out := make([]int, 0, n)
	for i := int64(0); i < n; i++ {
		v, vw := readZigZag(rest[pos:])
		if vw == 0 {
			return nil, rest // malformed; do not guess
		}
		out = append(out, int(v))
		pos += vw
	}
	return out, rest[pos:]
}

// GUIDFromHeaders extracts a 16-byte schema GUID from a record's headers for the
// header-GUID wire format. Honesty (docs/SECURITY-HARDENING.md): because the exact
// header KEY name is NOT specified in primary Confluent docs as of 2026, this
// resolves the GUID by, in order: (1) the operator-configured key when configuredKey
// is non-empty; (2) otherwise, a header whose value is exactly 16 bytes, or 17 bytes
// where the leading byte is a version/format byte and the trailing 16 are the GUID.
// It returns ok=false when no header carries a GUID-shaped value, so a connector
// falls back to the value prefix. It is a heuristic, documented as such — never a
// fabricated spec constant.
func GUIDFromHeaders(headers map[string][]byte, configuredKey string) (Reference, bool) {
	if configuredKey != "" {
		if v, ok := headers[configuredKey]; ok {
			if g, ok := guidFromValue(v); ok {
				return Reference{GUID: g, HasGUID: true}, true
			}
		}
		return Reference{}, false
	}
	for _, v := range headers {
		if g, ok := guidFromValue(v); ok {
			return Reference{GUID: g, HasGUID: true}, true
		}
	}
	return Reference{}, false
}

// guidFromValue recognizes a 16-byte GUID, optionally preceded by one version byte.
func guidFromValue(v []byte) ([guidLen]byte, bool) {
	var g [guidLen]byte
	switch len(v) {
	case guidLen:
		copy(g[:], v)
		return g, true
	case guidLen + 1:
		// Leading byte treated as a version/format byte (UNCONFIRMED layout
		// §5); the trailing 16 are the GUID.
		copy(g[:], v[1:])
		return g, true
	default:
		return g, false
	}
}

// Schema is a resolved schema: its declared type, its registry subject/version, and
// the schema definition text (Avro JSON, a .proto, or JSON Schema). The definition
// is parsed for STRUCTURE only (StructuralRefs); it is never used to decode a
// payload.
type Schema struct {
	// Type is "AVRO", "PROTOBUF" or "JSON" (empty defaults to AVRO, the registry
	// default).
	Type string
	// Subject and Version are the registry coordinates when known (the by-id lookup
	// may not return them; the by-subject lookup does).
	Subject string
	Version int
	// Definition is the raw schema text.
	Definition string
}

// Doer is the minimal HTTP capability the registry client needs; *http.Client
// satisfies it and a test injects a stub so no live call is made.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a read-only Schema Registry REST client (Confluent/Redpanda compatible).
// It resolves a schema by int id or by GUID and caches results in memory (a schema
// is immutable for a given id/GUID, so caching is safe and avoids re-fetching). The
// operator credential is applied per request via auth and never logged.
type Client struct {
	baseURL string
	http    Doer
	auth    func(*http.Request)

	mu      sync.Mutex
	byID    map[uint32]Schema
	byGUID  map[[guidLen]byte]Schema
	maxBody int64
}

// Options configures a Client. BaseURL is the registry root (e.g.
// "https://schema-registry.corp:8081"). HTTP defaults to a plain *http.Client when
// nil. Auth applies the operator credential (Basic or Bearer) and may be nil for an
// unauthenticated registry.
type Options struct {
	BaseURL string
	HTTP    Doer
	Auth    func(*http.Request)
	// MaxResponseBytes caps a schema document read (defensive; schemas are small).
	MaxResponseBytes int64
}

// NewClient builds a registry client. A nil HTTP uses http.DefaultClient.
func NewClient(o Options) *Client {
	h := o.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	mb := o.MaxResponseBytes
	if mb <= 0 {
		mb = 4 << 20 // 4 MiB is generous for a schema document
	}
	return &Client{
		baseURL: strings.TrimRight(o.BaseURL, "/"),
		http:    h,
		auth:    o.Auth,
		byID:    map[uint32]Schema{},
		byGUID:  map[[guidLen]byte]Schema{},
		maxBody: mb,
	}
}

// schemaResponse is the registry's by-id / by-guid JSON shape.
type schemaResponse struct {
	Schema     string `json:"schema"`
	SchemaType string `json:"schemaType"`
	Subject    string `json:"subject"`
	Version    int    `json:"version"`
}

// Resolve fetches the schema for a Reference (by GUID or by int id), caching the
// result. It returns an error when the registry is unreachable or the schema is
// unknown — the connector then emits the topology edge it can (topic/group) without
// a schema ref, never a fabricated contract.
func (c *Client) Resolve(ctx context.Context, ref Reference) (Schema, error) {
	if ref.HasGUID {
		return c.resolveGUID(ctx, ref.GUID)
	}
	return c.resolveID(ctx, ref.IntID)
}

func (c *Client) resolveID(ctx context.Context, id uint32) (Schema, error) {
	c.mu.Lock()
	if s, ok := c.byID[id]; ok {
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()
	s, err := c.fetch(ctx, fmt.Sprintf("/schemas/ids/%d", id))
	if err != nil {
		return Schema{}, err
	}
	c.mu.Lock()
	c.byID[id] = s
	c.mu.Unlock()
	return s, nil
}

func (c *Client) resolveGUID(ctx context.Context, guid [guidLen]byte) (Schema, error) {
	c.mu.Lock()
	if s, ok := c.byGUID[guid]; ok {
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()
	// The by-GUID endpoint exists on Confluent Schema Registry 8.x+ (its
	// availability depends on the registry version; an older registry returns 404 and
	// the connector degrades to topology-only).
	s, err := c.fetch(ctx, "/schemas/guids/"+hexLower(guid[:]))
	if err != nil {
		return Schema{}, err
	}
	c.mu.Lock()
	c.byGUID[guid] = s
	c.mu.Unlock()
	return s, nil
}

func (c *Client) fetch(ctx context.Context, path string) (Schema, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return Schema{}, err
	}
	req.Header.Set("Accept", "application/vnd.schemaregistry.v1+json, application/json")
	if c.auth != nil {
		c.auth(req)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Schema{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, c.maxBody))
	if resp.StatusCode != http.StatusOK {
		// A bounded excerpt for diagnostics; the registry error never carries our
		// credential. Keep it short.
		excerpt := string(body)
		if len(excerpt) > 200 {
			excerpt = excerpt[:200]
		}
		return Schema{}, fmt.Errorf("schemaregistry: GET %s: %s: %s", path, resp.Status, excerpt)
	}
	var sr schemaResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return Schema{}, fmt.Errorf("schemaregistry: decode %s: %w", path, err)
	}
	t := sr.SchemaType
	if t == "" {
		t = "AVRO" // the registry default when schemaType is omitted
	}
	return Schema{Type: t, Subject: sr.Subject, Version: sr.Version, Definition: sr.Schema}, nil
}

// hexLower renders bytes as lowercase hex without importing encoding/hex twice.
func hexLower(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}

// readZigZag decodes one zig-zag varint (Avro/Confluent index encoding) from b and
// returns the value and the number of bytes consumed (0 on a malformed/empty input).
func readZigZag(b []byte) (int64, int) {
	var ux uint64
	var shift uint
	for i := 0; i < len(b); i++ {
		c := b[i]
		ux |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			// zig-zag decode
			return int64(ux>>1) ^ -int64(ux&1), i + 1
		}
		shift += 7
		if shift >= 64 {
			return 0, 0
		}
	}
	return 0, 0
}
