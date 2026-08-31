// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package edugain consumes multilateral Research & Education (R&E) federation
// metadata — eduGAIN interfederation and InCommon aggregates — extending the
// single-IdP SAML of to the multilateral trust a university estate runs on
//. It is the Apache, dep-light half: it VERIFIES the aggregate's
// enveloped XML signature against a pinned trust certificate, parses the entities
// and their REFEDS entity-categories (Sirtfi incident-response trust, Research &
// Scholarship), and maps released eduPerson attributes to a local federated
// identity. The piece that wires this into the login Federation seam lives in
// enterprise/ (the license frontier); this package touches neither /core nor
// the login flow.
//
// Trust before parse (docs/SECURITY-HARDENING.md, §2): the aggregate is a large signed document
// from an interfederation. The connector NEVER trusts its contents until the
// aggregate signature verifies against the operator-pinned metadata-signing
// certificate. A tampered, unsigned, or wrong-cert aggregate is REJECTED and its
// entities are not consumed. XML-DSIG (exclusive C14N + RSA-SHA256, the
// eduGAIN/InCommon profile) is done by the standard pure-Go goxmldsig — hand-rolling
// XML canonicalisation would be the place a subtle bug becomes faked conformance.
//
// Minimal data (docs/SECURITY-HARDENING.md): the connector reads entity METADATA (entity ids,
// roles, registration authority, entity-categories) and, in the eduPerson mapper,
// only the eduPerson / subject-identifier attributes — never a credential. It
// imports only the SDK and the shared Apache helpers — never the engine.
package edugain

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.edugain"

// Entity is one verified federation entity (an IdP or SP) and its trust posture.
type Entity struct {
	EntityID              string
	IsIDP                 bool
	IsSP                  bool
	RegistrationAuthority string
	DisplayName           string
	Categories            EntityCategories
}

// Aggregate is the verified, parsed result of consuming an aggregate.
type Aggregate struct {
	Entities   []Entity
	ValidUntil time.Time
	// Authority is the metadata aggregator (registration authority or a configured
	// label) used to attribute the posture finding.
	Authority string
}

// Counts returns (idps, sps, sirtfi, researchScholarship) over the entities.
func (a Aggregate) Counts() (idps, sps, sirtfi, rs int) {
	for _, e := range a.Entities {
		if e.IsIDP {
			idps++
		}
		if e.IsSP {
			sps++
		}
		if e.Categories.Sirtfi {
			sirtfi++
		}
		if e.Categories.ResearchScholarship {
			rs++
		}
	}
	return
}

// ErrAggregateExpired means the aggregate's signature verified but it is past its
// validUntil — SAML metadata MUST NOT be used past validUntil, so its entities are
// not trusted (a stale aggregate could still list entities long removed).
var ErrAggregateExpired = errors.New("edugain: aggregate expired (past validUntil)")

// Consumer verifies and parses aggregate metadata against pinned trust cert(s).
type Consumer struct {
	trustCerts []*x509.Certificate
	now        func() time.Time // injectable clock (tests); nil => time.Now
}

// NewConsumer builds a Consumer from one or more PEM-encoded metadata-signing
// certificates (the operator pins the federation's signing cert). At least one
// certificate is required — a Consumer with no trust anchor could never verify.
func NewConsumer(trustCertPEM string) (*Consumer, error) {
	certs, err := parseCerts(trustCertPEM)
	if err != nil {
		return nil, err
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("edugain: no trust certificate configured (cannot verify an aggregate without a pinned signing cert)")
	}
	return &Consumer{trustCerts: certs}, nil
}

// Consume verifies the aggregate's enveloped XML signature against the pinned
// trust cert(s) and, ONLY if it verifies AND is not past validUntil, parses and
// returns the entities. A document whose signature does not verify, or that is
// expired, is rejected with an error and no entities are returned.
func (c *Consumer) Consume(xmlBytes []byte) (Aggregate, error) {
	if err := c.verifySignature(xmlBytes); err != nil {
		return Aggregate{}, err
	}
	agg, err := parseAggregate(xmlBytes)
	if err != nil {
		return Aggregate{}, err
	}
	if !agg.ValidUntil.IsZero() && c.clock().After(agg.ValidUntil) {
		return Aggregate{}, ErrAggregateExpired
	}
	return agg, nil
}

func (c *Consumer) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now().UTC()
}

// verifySignature validates the aggregate's enveloped signature against the
// trusted certs. goxmldsig requires the embedded signing cert to equal a pinned
// trust cert and the signature to cover the root by its ID.
func (c *Consumer) verifySignature(xmlBytes []byte) error {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(xmlBytes); err != nil {
		return fmt.Errorf("edugain: parse aggregate XML: %w", err)
	}
	if doc.Root() == nil {
		return fmt.Errorf("edugain: aggregate has no root element")
	}
	ctx := dsig.NewDefaultValidationContext(&dsig.MemoryX509CertificateStore{Roots: c.trustCerts})
	if _, err := ctx.Validate(doc.Root()); err != nil {
		return fmt.Errorf("edugain: aggregate signature did not verify against the pinned trust cert: %w", err)
	}
	return nil
}

// parseAggregate extracts entities and their categories from the (already
// signature-verified) aggregate XML.
func parseAggregate(xmlBytes []byte) (Aggregate, error) {
	var ed entitiesDescriptor
	if err := xml.Unmarshal(xmlBytes, &ed); err != nil {
		return Aggregate{}, fmt.Errorf("edugain: parse aggregate entities: %w", err)
	}
	agg := Aggregate{Authority: "edugain"}
	if t, err := time.Parse(time.RFC3339, ed.ValidUntil); err == nil {
		agg.ValidUntil = t.UTC()
	}
	for _, e := range ed.Entities {
		ent := Entity{
			EntityID:              strings.TrimSpace(e.EntityID),
			IsIDP:                 e.IDPSSODescriptor != nil,
			IsSP:                  e.SPSSODescriptor != nil,
			RegistrationAuthority: e.Extensions.RegistrationInfo.RegistrationAuthority,
			DisplayName:           firstNonEmptyStr(e.Extensions.UIInfo.DisplayName...),
			Categories:            classifyCategories(e.Extensions.allAttributeValues()),
		}
		if ent.EntityID == "" {
			continue
		}
		if agg.Authority == "edugain" && ent.RegistrationAuthority != "" {
			agg.Authority = ent.RegistrationAuthority
		}
		agg.Entities = append(agg.Entities, ent)
	}
	return agg, nil
}

// ---------------------------------------------------------------------------
// SourceConnector: poll + verify an aggregate, emit a federation-posture finding
// ---------------------------------------------------------------------------

// Source is the runtime half: it fetches the configured aggregate (a local file
// or a read-only URL), verifies it, and emits a federation-posture FindingReport
// so the control plane observes which R&E partners it trusts and their Sirtfi
// coverage. A verification failure emits a High finding and does NOT trust the
// entities. It satisfies sdk.SourceConnector (a re-pollable batch source).
type Source struct {
	metadataFile string
	metadataURL  string
	consumer     *Consumer
	doer         httpx.Doer
	now          func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns an edugain source with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "eduGAIN / InCommon R&E federation",
		Description: "Verifies an eduGAIN/InCommon aggregate's signature against a pinned cert and reports federation posture (entities, Sirtfi, R&S). Maps eduPerson attributes for the login layer.",
		ConfigFields: []sdk.ConfigField{
			{Key: "trust_cert_pem", Type: sdk.FieldString, Description: "PEM of the federation's metadata-signing certificate (pinned). Required to consume an aggregate; empty = source does nothing."},
			{Key: "metadata_file", Type: sdk.FieldString, Description: "Path to a downloaded aggregate metadata file (read-only)."},
			{Key: "metadata_url", Type: sdk.FieldString, Description: "Read-only URL of the aggregate metadata (alternative to metadata_file)."},
		},
	}
}

// Open reads configuration and builds the Consumer. With no trust cert the source
// is a visible no-op (the boot warns), not an error — symmetric with the offline
// modes of the other identity connectors.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.metadataFile = strings.TrimSpace(cfg.Get("metadata_file"))
	s.metadataURL = strings.TrimSpace(cfg.Get("metadata_url"))
	pemStr := strings.TrimSpace(cfg.Get("trust_cert_pem"))
	if pemStr == "" {
		return nil // offline / unconfigured (no-op Gather)
	}
	c, err := NewConsumer(pemStr)
	if err != nil {
		return err
	}
	c.now = s.now
	s.consumer = c
	return nil
}

// Gather fetches the configured aggregate, verifies it, and emits the posture
// finding. With no consumer/source configured it is a no-op.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.consumer == nil || (s.metadataFile == "" && s.metadataURL == "") {
		return nil
	}
	raw, err := s.fetch(ctx)
	if err != nil {
		return err
	}
	agg, verr := s.consumer.Consume(raw)
	if verr != nil {
		// Signature did not verify, or the aggregate is expired: emit a High finding
		// and trust nothing.
		kind, title := "edugain_unverified_aggregate", "eduGAIN/InCommon aggregate signature did NOT verify against the pinned trust cert — its entities are not trusted"
		if errors.Is(verr, ErrAggregateExpired) {
			kind, title = "edugain_expired_aggregate", "eduGAIN/InCommon aggregate is expired (past validUntil) — its entities are not trusted"
		}
		return sink.Emit(ctx, model.FindingReport{
			Kind:        kind,
			Severity:    model.SeverityHigh,
			SubjectKind: "federation",
			SubjectRef:  firstNonEmptyStr(s.metadataURL, s.metadataFile, "edugain"),
			Title:       title,
			DetailHash:  redact.Hash("edugain-rejected|" + verr.Error()),
			OccurredAt:  s.clock(),
		})
	}
	idps, sps, sirtfi, rs := agg.Counts()
	if err := sink.Emit(ctx, model.FindingReport{
		Kind:        "edugain_federation_posture",
		Severity:    model.SeverityInfo,
		SubjectKind: "federation",
		SubjectRef:  agg.Authority,
		Title: fmt.Sprintf("Consumed %s aggregate (signature verified): %d entities — %d IdP, %d SP; Sirtfi=%d, R&S=%d",
			agg.Authority, len(agg.Entities), idps, sps, sirtfi, rs),
		DetailHash: redact.Hash(fmt.Sprintf("edugain|%s|%d|%d|%d|%d|%d", agg.Authority, len(agg.Entities), idps, sps, sirtfi, rs)),
		OccurredAt: s.clock(),
	}); err != nil {
		return err
	}
	// Trust-hygiene: an R&E IdP that asserts no Sirtfi has made no incident-response
	// commitment the federation can rely on. The posture roll-up only COUNTS Sirtfi;
	// surface the shortfall as a Low finding so the operator can decide which
	// federated IdPs to trust. Count IdPs SPECIFICALLY (an SP asserting Sirtfi must
	// not mask an IdP that lacks it — Counts()'s sirtfi is over all entity roles). One
	// aggregate finding, so no per-entity cardinality fan-out.
	idpNoSirtfi := 0
	for _, e := range agg.Entities {
		if e.IsIDP && !e.Categories.Sirtfi {
			idpNoSirtfi++
		}
	}
	if idpNoSirtfi > 0 {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        "edugain_sirtfi_trust_gap",
			Severity:    model.SeverityLow,
			SubjectKind: "federation",
			SubjectRef:  agg.Authority,
			Title: fmt.Sprintf("%d of %d federated IdP(s) in %s assert no Sirtfi incident-response commitment",
				idpNoSirtfi, idps, agg.Authority),
			DetailHash: redact.Hash(fmt.Sprintf("edugain-sirtfi-gap|%s|%d|%d", agg.Authority, idpNoSirtfi, idps)),
			OccurredAt: s.clock(),
		})
	}
	return nil
}

// Close releases resources; the connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// fetch reads the aggregate from the configured file or URL (read-only).
func (s *Source) fetch(ctx context.Context) ([]byte, error) {
	if s.metadataFile != "" {
		return os.ReadFile(s.metadataFile)
	}
	client := httpx.New(s.metadataURL, s.doer, nil, nil)
	resp, err := client.GetRaw(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return readAllLimited(resp)
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// ---------------------------------------------------------------------------
// SAML metadata wire shapes (local-name matched; namespaces vary by aggregate)
// ---------------------------------------------------------------------------

type entitiesDescriptor struct {
	XMLName    xml.Name           `xml:"EntitiesDescriptor"`
	ValidUntil string             `xml:"validUntil,attr"`
	Entities   []entityDescriptor `xml:"EntityDescriptor"`
}

type entityDescriptor struct {
	EntityID         string      `xml:"entityID,attr"`
	IDPSSODescriptor *roleMarker `xml:"IDPSSODescriptor"`
	SPSSODescriptor  *roleMarker `xml:"SPSSODescriptor"`
	Extensions       extensions  `xml:"Extensions"`
}

type roleMarker struct{}

type extensions struct {
	EntityAttributes entityAttributes `xml:"EntityAttributes"`
	RegistrationInfo registrationInfo `xml:"RegistrationInfo"`
	UIInfo           uiInfo           `xml:"UIInfo"`
}

// allAttributeValues flattens every AttributeValue across the entity-attributes so
// a category is recognized by its value regardless of which Attribute Name carried
// it (entity-category, entity-category-support or assurance-certification).
func (e extensions) allAttributeValues() []string {
	var out []string
	for _, a := range e.EntityAttributes.Attributes {
		out = append(out, a.Values...)
	}
	return out
}

type entityAttributes struct {
	Attributes []samlAttribute `xml:"Attribute"`
}

type samlAttribute struct {
	Name   string   `xml:"Name,attr"`
	Values []string `xml:"AttributeValue"`
}

type registrationInfo struct {
	RegistrationAuthority string `xml:"registrationAuthority,attr"`
}

type uiInfo struct {
	DisplayName []string `xml:"DisplayName"`
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseCerts parses one or more PEM CERTIFICATE blocks.
func parseCerts(pemStr string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(pemStr)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("edugain: parse trust cert: %w", err)
		}
		certs = append(certs, cert)
	}
	return certs, nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// maxAggregateBytes bounds an aggregate read. A full interfederation aggregate is
// large (tens of MiB), so the bound is generous but finite (memory protection).
const maxAggregateBytes = 256 << 20

// readAllLimited reads a bounded response body.
func readAllLimited(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxAggregateBytes))
}
