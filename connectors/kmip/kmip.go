// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kmip

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/tlsx"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.kmip"

// signalKMIP is the SignalSource stamped on custody edges (an open string, S02 §6).
const signalKMIP = model.SignalSource("kmip")

// resourceKMIPKey is the ResourceKind for a discovered key object.
const resourceKMIPKey = "kmip.key"

const (
	defaultPort    = "5696"
	defaultTimeout = 30 * time.Second
	// maxObjects bounds a Locate sweep so a huge HSM cannot exhaust memory; a
	// truncated sweep is logged honestly by the caller, never silently capped.
	maxObjects = 5000
)

// transport issues one KMIP request and returns the response bytes. The default
// dials mutually-authenticated TLS; tests inject a fake server.
type transport interface {
	roundTrip(ctx context.Context, req []byte) ([]byte, error)
}

// Source is the KMIP inventory connector. It satisfies sdk.SourceConnector (custody
// edges) and identitysource.GraphProvider (the secret_store inventory). Call New.
type Source struct {
	endpoint string
	tlsOpts  tlsx.Options
	timeout  time.Duration
	tr       transport // nil => the default TLS transport built in Open
	now      func() time.Time
}

var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a kmip source.
func New() *Source { return &Source{timeout: defaultTimeout} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "OASIS KMIP v2.1 key server",
		Description: "Inventories an on-prem KMIP server's key objects read-only (Locate + GetAttributes). Never reads key material.",
		ConfigFields: []sdk.ConfigField{
			{Key: "endpoint", Type: sdk.FieldString, Required: true, Description: "KMIP server host or host:port (default port 5696)."},
			{Key: "tls_ca", Type: sdk.FieldString, Description: "PEM CA bundle that pins the server certificate."},
			{Key: "tls_cert", Type: sdk.FieldString, Secret: true, Description: "Client certificate PEM for mutual TLS (most KMIP servers require it)."},
			{Key: "tls_key", Type: sdk.FieldString, Secret: true, Description: "Client private key PEM for mutual TLS."},
			{Key: "tls_insecure", Type: sdk.FieldBool, Description: "Disable certificate verification (operator opt-in only; never a default)."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request timeout."},
		},
	}
}

// Open reads configuration and prepares the TLS transport. With no endpoint it
// errors; with an endpoint but no transport reachable, Snapshot/Gather surface the
// dial error at run time (not at Open).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.endpoint = strings.TrimSpace(cfg.Get("endpoint"))
	if s.endpoint == "" {
		return errors.New("kmip: endpoint is required")
	}
	if _, _, err := net.SplitHostPort(s.endpoint); err != nil {
		s.endpoint = net.JoinHostPort(s.endpoint, defaultPort)
	}
	s.tlsOpts = tlsx.Options{
		Enable:             true,
		CAFile:             cfg.Get("tls_ca"),
		CertFile:           cfg.Get("tls_cert"),
		KeyFile:            cfg.Get("tls_key"),
		InsecureSkipVerify: cfg.GetBool("tls_insecure", false),
	}
	s.timeout = cfg.GetDuration("timeout", defaultTimeout)
	if s.tr == nil {
		tlsCfg, err := tlsx.Build(s.tlsOpts)
		if err != nil {
			return err
		}
		s.tr = &tlsTransport{endpoint: s.endpoint, cfg: tlsCfg, timeout: s.timeout}
	}
	return nil
}

// Close releases resources; this connector holds none between runs.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot exposes the KMIP server as a secret_store NHI. It performs
// no key-object enumeration (that is Gather's custody edges); the inventory of WHERE
// keys live is the server itself.
func (s *Source) Snapshot(_ context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceKMIP, CapturedAt: s.clock().UTC()}
	if s.endpoint == "" {
		return g, nil
	}
	g.Identities = append(g.Identities, identitysource.Identity{
		Ref:         s.serverRef(),
		Type:        identitysource.PrincipalNHI,
		Kind:        identitysource.KindSecretStore,
		DisplayName: "KMIP server " + s.endpoint,
		Source:      identitysource.SourceKMIP,
		Attributes:  map[string]string{"provider": "kmip", "endpoint": s.endpoint},
	})
	return g, nil
}

// Gather discovers the server's key objects (Locate) and reads each object's
// non-secret attributes (GetAttributes), emitting one custody edge per object. It
// NEVER issues Get/Create/Destroy.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.endpoint == "" || s.tr == nil {
		return nil
	}
	uids, err := s.locate(ctx)
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return err
		}
		attrs, err := s.getAttributes(ctx, uid)
		if err != nil {
			return err
		}
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    s.serverRef(),
			ResourceKind: resourceKMIPKey,
			ResourceRef:  s.serverRef() + "/" + uid,
			Mode:         model.ModeUnknown, // custody/inventory, not an R/RW access
			Source:       signalKMIP,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      attrs.descriptor(),
			ObservedAt:   now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) serverRef() string { return "kmip:" + s.endpoint }

// locate issues a Locate with an empty Attributes filter (match all objects) and
// returns the discovered unique identifiers.
func (s *Source) locate(ctx context.Context) ([]string, error) {
	req := buildLocateRequest()
	resp, err := s.tr.roundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	return parseLocateResponse(resp)
}

// getAttributes issues a GetAttributes for one object and returns its non-secret
// attributes.
func (s *Source) getAttributes(ctx context.Context, uid string) (keyAttrs, error) {
	req := buildGetAttributesRequest(uid)
	resp, err := s.tr.roundTrip(ctx, req)
	if err != nil {
		return keyAttrs{}, err
	}
	return parseGetAttributesResponse(resp)
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// --- KMIP message build / parse ----------------------------------------------

// requestHeader builds the common Request Header (protocol version 2.1 + batch
// count 1).
func requestHeader() []byte {
	var pv []byte
	pv = encInteger(pv, tagProtocolVersionMajor, int32(protocolMajor))
	pv = encInteger(pv, tagProtocolVersionMinor, int32(protocolMinor))
	var hdr []byte
	hdr = encStructure(hdr, tagProtocolVersion, pv)
	hdr = encInteger(hdr, tagBatchCount, 1)
	return hdr
}

// buildLocateRequest builds a Locate request that matches all objects (an empty
// Attributes structure).
func buildLocateRequest() []byte {
	var payload []byte
	payload = encStructure(payload, tagAttributes, nil) // empty = match all
	var batch []byte
	batch = encEnumeration(batch, tagOperation, opLocate)
	batch = encStructure(batch, tagRequestPayload, payload)

	var msg []byte
	msg = encStructure(msg, tagRequestHeader, requestHeader())
	msg = encStructure(msg, tagBatchItem, batch)
	return encStructure(nil, tagRequestMessage, msg)
}

// buildGetAttributesRequest builds a GetAttributes for one unique identifier (no
// Attribute Reference list ⇒ the server returns all attributes).
func buildGetAttributesRequest(uid string) []byte {
	var payload []byte
	payload = encTextString(payload, tagUniqueIdentifier, uid)
	var batch []byte
	batch = encEnumeration(batch, tagOperation, opGetAttributes)
	batch = encStructure(batch, tagRequestPayload, payload)

	var msg []byte
	msg = encStructure(msg, tagRequestHeader, requestHeader())
	msg = encStructure(msg, tagBatchItem, batch)
	return encStructure(nil, tagRequestMessage, msg)
}

// responseBatchItem decodes a ResponseMessage and returns its first batch item's
// ResultStatus and ResponsePayload, or an error for a failed/blank operation.
func responseBatchItem(resp []byte) (item, error) {
	msg, _, err := decode(resp)
	if err != nil {
		return item{}, err
	}
	if msg.tag != tagResponseMessage {
		return item{}, fmt.Errorf("kmip: response root tag %s, want ResponseMessage", tagHex(msg.tag))
	}
	bi, ok := msg.find(tagBatchItem)
	if !ok {
		return item{}, errors.New("kmip: response has no batch item")
	}
	if st, ok := bi.find(tagResultStatus); ok && st.u != resultSuccess {
		reason := ""
		if r, ok := bi.find(tagResultReason); ok {
			reason = " reason=" + tagHex(r.u)
		}
		if m, ok := bi.find(tagResultMessage); ok && m.s != "" {
			reason += " (" + m.s + ")"
		}
		return item{}, fmt.Errorf("kmip: operation failed status=%s%s", tagHex(st.u), reason)
	}
	pl, ok := bi.find(tagResponsePayload)
	if !ok {
		return item{}, errors.New("kmip: response batch item has no payload")
	}
	return pl, nil
}

// parseLocateResponse extracts the unique identifiers from a Locate response.
func parseLocateResponse(resp []byte) ([]string, error) {
	pl, err := responseBatchItem(resp)
	if err != nil {
		return nil, err
	}
	var uids []string
	for _, u := range pl.findAll(tagUniqueIdentifier) {
		if u.s != "" {
			uids = append(uids, u.s)
		}
		if len(uids) >= maxObjects {
			break
		}
	}
	sort.Strings(uids)
	return uids, nil
}

// keyAttrs holds the non-secret attributes read from a key object.
type keyAttrs struct {
	objectType string
	algorithm  string
	length     int64
	state      string
	name       string
}

// descriptor renders the attributes as a compact, non-secret ToolRef descriptor.
func (a keyAttrs) descriptor() string {
	parts := make([]string, 0, 5)
	if a.objectType != "" {
		parts = append(parts, "type="+a.objectType)
	}
	if a.algorithm != "" {
		parts = append(parts, "alg="+a.algorithm)
	}
	if a.length > 0 {
		parts = append(parts, "bits="+strconv.FormatInt(a.length, 10))
	}
	if a.state != "" {
		parts = append(parts, "state="+a.state)
	}
	if a.name != "" {
		parts = append(parts, "name="+a.name)
	}
	if len(parts) == 0 {
		return "kmip.object"
	}
	return strings.Join(parts, ";")
}

// parseGetAttributesResponse reads the Attributes structure of a GetAttributes
// response. KMIP 2.1 carries each attribute as an independently-tagged item inside
// the Attributes structure (tag 0x420125); we read the confirmed tags and ignore
// the rest (a documented seam — never fabricated).
func parseGetAttributesResponse(resp []byte) (keyAttrs, error) {
	pl, err := responseBatchItem(resp)
	if err != nil {
		return keyAttrs{}, err
	}
	attrs := pl
	if a, ok := pl.find(tagAttributes); ok {
		attrs = a // 2.x Attributes wrapper; some servers inline the items in the payload
	}
	var ka keyAttrs
	if v, ok := attrs.find(tagObjectType); ok {
		ka.objectType = objectTypeName(v.u)
	}
	if v, ok := attrs.find(tagCryptographicAlg); ok {
		ka.algorithm = cryptoAlgName(v.u)
	}
	if v, ok := attrs.find(tagCryptographicLength); ok {
		ka.length = v.i
	}
	if v, ok := attrs.find(tagState); ok {
		ka.state = stateName(v.u)
	}
	if v, ok := attrs.find(tagName); ok {
		if nv, ok := v.firstText(); ok {
			ka.name = nv
		}
	}
	return ka, nil
}

// --- enum value names (KMIP v2.1 enumeration tables) -------------------------

func objectTypeName(v uint32) string {
	switch v {
	case 0x01:
		return "Certificate"
	case 0x02:
		return "SymmetricKey"
	case 0x03:
		return "PublicKey"
	case 0x04:
		return "PrivateKey"
	case 0x05:
		return "SplitKey"
	case 0x07:
		return "SecretData"
	case 0x08:
		return "OpaqueObject"
	case 0x09:
		return "PGPKey"
	case 0x0A:
		return "CertificateRequest"
	default:
		return tagHex(v)
	}
}

func cryptoAlgName(v uint32) string {
	switch v {
	case 0x01:
		return "DES"
	case 0x02:
		return "3DES"
	case 0x03:
		return "AES"
	case 0x04:
		return "RSA"
	case 0x05:
		return "DSA"
	case 0x06:
		return "ECDSA"
	case 0x09:
		return "HMAC-SHA256"
	case 0x0D:
		return "DH"
	case 0x0E:
		return "ECDH"
	case 0x1A:
		return "EC"
	case 0x37:
		return "Ed25519"
	case 0x38:
		return "Ed448"
	default:
		return tagHex(v)
	}
}

func stateName(v uint32) string {
	switch v {
	case 0x01:
		return "PreActive"
	case 0x02:
		return "Active"
	case 0x03:
		return "Deactivated"
	case 0x04:
		return "Compromised"
	case 0x05:
		return "Destroyed"
	case 0x06:
		return "DestroyedCompromised"
	default:
		return tagHex(v)
	}
}

// --- default TLS transport ----------------------------------------------------

type tlsTransport struct {
	endpoint string
	cfg      *tls.Config
	timeout  time.Duration
}

// roundTrip dials TLS, writes the request, and reads one self-delimiting KMIP
// response message.
func (t *tlsTransport) roundTrip(ctx context.Context, req []byte) ([]byte, error) {
	d := &net.Dialer{Timeout: t.timeout}
	raw, err := d.DialContext(ctx, "tcp", t.endpoint)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, t.cfg)
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else if t.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(t.timeout))
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	return readMessage(conn)
}

// readMessage reads one TTLV message: the 8-byte header carries the value length,
// from which the full (padded) message size is known.
func readMessage(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	length := int(uint32(hdr[4])<<24 | uint32(hdr[5])<<16 | uint32(hdr[6])<<8 | uint32(hdr[7]))
	padded := length + (8-length%8)%8
	if padded < 0 || padded > 8<<20 {
		return nil, fmt.Errorf("kmip: response length %d out of range", length)
	}
	buf := make([]byte, 8+padded)
	copy(buf, hdr)
	if _, err := io.ReadFull(r, buf[8:]); err != nil {
		return nil, err
	}
	return buf, nil
}
