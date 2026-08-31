// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package edugain

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const unsignedAggregate = `<EntitiesDescriptor ID="agg1" validUntil="2027-01-01T00:00:00Z" xmlns="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:mdattr="urn:oasis:names:tc:SAML:metadata:attribute" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" xmlns:mdrpi="urn:oasis:names:tc:SAML:metadata:rpi" xmlns:mdui="urn:oasis:names:tc:SAML:metadata:ui">
  <EntityDescriptor entityID="https://idp.uni.example/idp">
    <Extensions>
      <mdrpi:RegistrationInfo registrationAuthority="https://incommon.org"/>
      <mdattr:EntityAttributes>
        <saml:Attribute Name="urn:oasis:names:tc:SAML:attribute:assurance-certification">
          <saml:AttributeValue>https://refeds.org/sirtfi</saml:AttributeValue>
        </saml:Attribute>
        <saml:Attribute Name="http://macedir.org/entity-category-support">
          <saml:AttributeValue>http://refeds.org/category/research-and-scholarship</saml:AttributeValue>
        </saml:Attribute>
      </mdattr:EntityAttributes>
      <mdui:UIInfo><mdui:DisplayName>Example University IdP</mdui:DisplayName></mdui:UIInfo>
    </Extensions>
    <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"/>
  </EntityDescriptor>
  <EntityDescriptor entityID="https://sp.service.example/sp">
    <Extensions>
      <mdattr:EntityAttributes>
        <saml:Attribute Name="http://macedir.org/entity-category">
          <saml:AttributeValue>http://refeds.org/category/research-and-scholarship</saml:AttributeValue>
        </saml:Attribute>
      </mdattr:EntityAttributes>
    </Extensions>
    <SPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"/>
  </EntityDescriptor>
</EntitiesDescriptor>`

// unsignedAggregateNoSirtfi is one IdP that asserts NO Sirtfi (nor R&S) — the
// trust-hygiene gap case (an IdP with no incident-response commitment).
const unsignedAggregateNoSirtfi = `<EntitiesDescriptor ID="agg2" validUntil="2027-01-01T00:00:00Z" xmlns="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:mdrpi="urn:oasis:names:tc:SAML:metadata:rpi">
  <EntityDescriptor entityID="https://idp2.uni.example/idp">
    <Extensions>
      <mdrpi:RegistrationInfo registrationAuthority="https://incommon.org"/>
    </Extensions>
    <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"/>
  </EntityDescriptor>
</EntitiesDescriptor>`

func makeKeyCert(t *testing.T) (*rsa.PrivateKey, *x509.Certificate, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-metadata-signer"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return priv, cert, pemStr
}

func signAggregate(t *testing.T, priv *rsa.PrivateKey, cert *x509.Certificate, unsigned string) []byte {
	t.Helper()
	sctx, err := dsig.NewSigningContext(priv, [][]byte{cert.Raw})
	if err != nil {
		t.Fatalf("signing context: %v", err)
	}
	if err := sctx.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatalf("set method: %v", err)
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromString(unsigned); err != nil {
		t.Fatalf("read unsigned: %v", err)
	}
	signed, err := sctx.SignEnveloped(doc.Root())
	if err != nil {
		t.Fatalf("sign enveloped: %v", err)
	}
	out := etree.NewDocument()
	out.SetRoot(signed)
	b, err := out.WriteToBytes()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return b
}

func TestConsumeVerifiesAndParses(t *testing.T) {
	priv, cert, certPEM := makeKeyCert(t)
	signed := signAggregate(t, priv, cert, unsignedAggregate)

	c, err := NewConsumer(certPEM)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	agg, err := c.Consume(signed)
	if err != nil {
		t.Fatalf("Consume (valid) rejected: %v", err)
	}
	if len(agg.Entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(agg.Entities))
	}
	idps, sps, sirtfi, rs := agg.Counts()
	if idps != 1 || sps != 1 || sirtfi != 1 || rs != 2 {
		t.Errorf("counts = (idp=%d sp=%d sirtfi=%d rs=%d), want (1,1,1,2)", idps, sps, sirtfi, rs)
	}
	idp := agg.Entities[0]
	if !idp.IsIDP || !idp.Categories.Sirtfi || idp.Categories.SirtfiVersion != "1" || !idp.Categories.ResearchScholarship {
		t.Errorf("idp categories wrong: %+v", idp.Categories)
	}
	if idp.RegistrationAuthority != "https://incommon.org" {
		t.Errorf("registration authority = %q", idp.RegistrationAuthority)
	}
}

func TestConsumeRejectsTampered(t *testing.T) {
	priv, cert, certPEM := makeKeyCert(t)
	signed := signAggregate(t, priv, cert, unsignedAggregate)
	// Tamper signed content (the IdP display name) after signing.
	tampered := bytes.Replace(signed, []byte("Example University IdP"), []byte("Tampered University"), 1)
	if bytes.Equal(tampered, signed) {
		t.Fatal("tamper did not change the bytes")
	}

	c, _ := NewConsumer(certPEM)
	if _, err := c.Consume(tampered); err == nil {
		t.Fatal("Consume accepted a tampered aggregate — its entities must never be trusted")
	}
}

func TestConsumeRejectsWrongCert(t *testing.T) {
	signPriv, signCert, _ := makeKeyCert(t)
	signed := signAggregate(t, signPriv, signCert, unsignedAggregate)
	// Trust a DIFFERENT cert than the one that signed the aggregate.
	_, _, otherPEM := makeKeyCert(t)

	c, _ := NewConsumer(otherPEM)
	if _, err := c.Consume(signed); err == nil {
		t.Fatal("Consume accepted an aggregate signed by an untrusted cert")
	}
}

func TestConsumeRejectsExpired(t *testing.T) {
	priv, cert, certPEM := makeKeyCert(t)
	signed := signAggregate(t, priv, cert, unsignedAggregate) // validUntil 2027-01-01
	c, _ := NewConsumer(certPEM)
	// Clock is AFTER the aggregate's validUntil: signature verifies but it is stale.
	c.now = func() time.Time { return time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC) }
	if _, err := c.Consume(signed); err == nil {
		t.Fatal("Consume accepted an aggregate past its validUntil — stale metadata must not be trusted")
	} else if !errors.Is(err, ErrAggregateExpired) {
		t.Fatalf("error = %v, want ErrAggregateExpired", err)
	}
}

func TestNewConsumerRequiresCert(t *testing.T) {
	if _, err := NewConsumer("not a cert"); err == nil {
		t.Fatal("NewConsumer accepted a non-cert trust anchor")
	}
}

func TestMapEduPerson(t *testing.T) {
	attrs := map[string][]string{
		attrEPPN:              {"jdoe@uni.example"},
		attrSubjectID:         {"jdoe-stable@uni.example"},
		attrScopedAffiliation: {"staff@uni.example", "member@uni.example"},
		attrEntitlement:       {"urn:mace:dir:entitlement:common-lib-terms"},
		attrAssurance:         {"https://refeds.org/assurance/IAP/high"},
		attrMail:              {"jdoe@uni.example"},
	}
	s := MapEduPerson(attrs)
	if s.PrincipalName != "jdoe@uni.example" {
		t.Errorf("ePPN = %q", s.PrincipalName)
	}
	if s.Ref() != "jdoe-stable@uni.example" {
		t.Errorf("Ref = %q, want the subject-id (the convergence key)", s.Ref())
	}
	if len(s.ScopedAffiliations) != 2 || len(s.Entitlements) != 1 || len(s.AssuranceValues) != 1 {
		t.Errorf("multi-valued attrs lost: %+v", s)
	}
}

// --- Source (poll) ---

type captureSink struct{ obs []model.Observation }

func (c *captureSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

func gatherFile(t *testing.T, path, certPEM string) []model.FindingReport {
	t.Helper()
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"metadata_file": path, "trust_cert_pem": certPEM,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var out []model.FindingReport
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func TestSourcePostureFinding(t *testing.T) {
	priv, cert, certPEM := makeKeyCert(t)
	signed := signAggregate(t, priv, cert, unsignedAggregate)
	path := filepath.Join(t.TempDir(), "agg.xml")
	if err := os.WriteFile(path, signed, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := gatherFile(t, path, certPEM)
	// The default fixture's single IdP DOES assert Sirtfi, so there is exactly one
	// finding (the Info roll-up) and NO trust-gap — the negative case for the gap.
	if len(f) != 1 || f[0].Kind != "edugain_federation_posture" || f[0].Severity != model.SeverityInfo {
		t.Fatalf("want one Info posture finding, got %+v", f)
	}
}

func TestSourceSirtfiTrustGap(t *testing.T) {
	priv, cert, certPEM := makeKeyCert(t)
	signed := signAggregate(t, priv, cert, unsignedAggregateNoSirtfi)
	path := filepath.Join(t.TempDir(), "agg.xml")
	if err := os.WriteFile(path, signed, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := gatherFile(t, path, certPEM)
	// The IdP asserts no Sirtfi: the posture roll-up PLUS a Low trust-gap finding.
	if len(f) != 2 {
		t.Fatalf("want posture + trust-gap findings, got %+v", f)
	}
	var gap *model.FindingReport
	for i := range f {
		if f[i].Kind == "edugain_sirtfi_trust_gap" {
			gap = &f[i]
		}
	}
	if gap == nil {
		t.Fatalf("no edugain_sirtfi_trust_gap finding; got %+v", f)
	}
	if gap.Severity != model.SeverityLow {
		t.Errorf("trust-gap severity = %v, want Low", gap.Severity)
	}
	if gap.SubjectKind != "federation" {
		t.Errorf("trust-gap subjectKind = %q, want federation", gap.SubjectKind)
	}
}

func TestSourceUnverifiedFinding(t *testing.T) {
	priv, cert, certPEM := makeKeyCert(t)
	signed := signAggregate(t, priv, cert, unsignedAggregate)
	tampered := bytes.Replace(signed, []byte("Example University IdP"), []byte("Evil University"), 1)
	path := filepath.Join(t.TempDir(), "agg.xml")
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := gatherFile(t, path, certPEM)
	if len(f) != 1 || f[0].Kind != "edugain_unverified_aggregate" || f[0].Severity != model.SeverityHigh {
		t.Fatalf("want one High unverified finding, got %+v", f)
	}
}

func TestSourceExpiredFinding(t *testing.T) {
	priv, cert, certPEM := makeKeyCert(t)
	signed := signAggregate(t, priv, cert, unsignedAggregate) // validUntil 2027-01-01
	path := filepath.Join(t.TempDir(), "agg.xml")
	if err := os.WriteFile(path, signed, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := New()
	s.now = func() time.Time { return time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC) } // past validUntil
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"metadata_file": path, "trust_cert_pem": certPEM,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var fr []model.FindingReport
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok {
			fr = append(fr, f)
		}
	}
	if len(fr) != 1 || fr[0].Kind != "edugain_expired_aggregate" || fr[0].Severity != model.SeverityHigh {
		t.Fatalf("want one High edugain_expired_aggregate, got %+v", fr)
	}
}

func TestOfflineNoOp(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline Gather emitted: %+v", sink.obs)
	}
}
