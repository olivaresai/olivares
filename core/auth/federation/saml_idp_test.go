// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // XML Encryption RSA-OAEP-MGF1P is defined over SHA-1; the SP must decrypt what IdPs send.
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// This file is a hand-built SAML identity provider for the login journey. It uses only
// the XML and signature packages that stay in the tree (etree, goxmldsig) and the
// standard library, so the journey exercises the product and never the SAML library
// under it: swapping that library must not require touching the IdP side.

const (
	jnySPEntityID  = "https://sp.olivares.test/saml/metadata"
	jnyACSURL      = "https://sp.olivares.test/saml/acs"
	jnyIdPEntityID = "https://idp.olivares.test/saml/metadata"
	jnyIdPSSOURL   = "https://idp.olivares.test/saml/sso"

	jnyNSProtocol  = "urn:oasis:names:tc:SAML:2.0:protocol"
	jnyNSAssertion = "urn:oasis:names:tc:SAML:2.0:assertion"
	jnyNSXMLEnc    = "http://www.w3.org/2001/04/xmlenc#"
	jnyNSDSig      = "http://www.w3.org/2000/09/xmldsig#"

	jnyStatusSuccess = "urn:oasis:names:tc:SAML:2.0:status:Success"
	jnyCtxPassword   = "urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport"
	jnyCtxMFA        = "https://refeds.org/profile/mfa"
	jnyEncRSAOAEP    = "http://www.w3.org/2001/04/xmlenc#rsa-oaep-mgf1p"
)

// jnyKey is an RSA signing identity: the IdP's, or an attacker's.
type jnyKey struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

// newJnyKey creates a self-signed RSA key and certificate valid around now.
func newJnyKey(t *testing.T, commonName string) *jnyKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &jnyKey{key: key, cert: cert}
}

func (k *jnyKey) b64() string { return base64.StdEncoding.EncodeToString(k.cert.Raw) }

// jnyKeyDescriptor is one <KeyDescriptor> of the IdP metadata.
type jnyKeyDescriptor struct {
	use  string // "signing", "encryption" or "" (both)
	cert *jnyKey
}

// jnyMetadata renders IdP metadata. wrap puts it inside an <EntitiesDescriptor>, the shape
// federations publish.
func jnyMetadata(entityID, ssoURL string, wrap bool, kds ...jnyKeyDescriptor) string {
	var b strings.Builder
	for _, kd := range kds {
		use := ""
		if kd.use != "" {
			use = fmt.Sprintf(` use=%q`, kd.use)
		}
		fmt.Fprintf(&b, `<md:KeyDescriptor%s><ds:KeyInfo><ds:X509Data><ds:X509Certificate>%s</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>`, use, kd.cert.b64())
	}
	entity := fmt.Sprintf(`<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:ds="%s" entityID=%q>`+
		`<md:IDPSSODescriptor protocolSupportEnumeration="%s">%s`+
		`<md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location=%q/>`+
		`</md:IDPSSODescriptor></md:EntityDescriptor>`, jnyNSDSig, entityID, jnyNSProtocol, b.String(), ssoURL)
	if !wrap {
		return xml.Header + entity
	}
	return fmt.Sprintf(`%s<md:EntitiesDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" Name="federation">`+
		`<md:EntityDescriptor entityID="https://other.olivares.test/no-idp"><md:SPSSODescriptor protocolSupportEnumeration="%s"/></md:EntityDescriptor>`+
		`%s</md:EntitiesDescriptor>`, xml.Header, jnyNSProtocol, entity)
}

// jnyAttr is one SAML attribute with its values.
type jnyAttr struct {
	name, friendly string
	values         []string
}

// jnyAuthn is one <AuthnStatement>.
type jnyAuthn struct {
	context             string
	instant             time.Time
	sessionNotOnOrAfter time.Time // zero = absent
}

// jnyResp describes one SAML Response an IdP posts to the ACS. newJnyResp fills a valid
// default; a test changes exactly the field whose rejection it proves.
type jnyResp struct {
	requestID                   string // InResponseTo on the Response and on SubjectConfirmationData
	respInResponseTo            *string
	scdInResponseTo             *string
	nameID                      string
	attrs                       []jnyAttr
	authn                       []jnyAuthn
	respIssuer, assertionIssuer string
	destination, recipient      string
	audience                    string // "" = the SP entity id
	noAudience                  bool   // omit the AudienceRestriction
	status                      string
	respIssueInstant            time.Time
	assertionIssueInstant       time.Time
	notBefore, notOnOrAfter     time.Time
	scdNotOnOrAfter             time.Time
	sign                        string // "response", "assertion", "both", "none"
	signer                      *jnyKey
	encryptTo                   *x509.Certificate
	encryptAlg                  string                 // data algorithm URI; "" = aes128-cbc
	stamp                       func(time.Time) string // time format; nil = whole seconds in UTC
	noNotBefore                 bool                   // omit Conditions@NotBefore
	assertionID                 string                 // fixed assertion ID; "" = random
	drop                        []string               // assertion child paths to remove before signing
}

func newJnyResp(idp *jnyKey, requestID string) jnyResp {
	now := time.Now().UTC().Truncate(time.Second)
	return jnyResp{
		requestID: requestID,
		nameID:    "alice-nameid",
		attrs: []jnyAttr{
			{name: "email", values: []string{"alice@corp.example"}},
			{name: "displayName", values: []string{"Alice Example"}},
		},
		authn:                 []jnyAuthn{{context: jnyCtxPassword, instant: now}},
		respIssuer:            jnyIdPEntityID,
		assertionIssuer:       jnyIdPEntityID,
		destination:           jnyACSURL,
		recipient:             jnyACSURL,
		status:                jnyStatusSuccess,
		respIssueInstant:      now,
		assertionIssueInstant: now,
		notBefore:             now.Add(-time.Minute),
		notOnOrAfter:          now.Add(5 * time.Minute),
		scdNotOnOrAfter:       now.Add(5 * time.Minute),
		sign:                  "response",
		signer:                idp,
	}
}

func jnyStr(s string) *string { return &s }

func jnyEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func jnyTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// ts renders t the way this IdP writes timestamps.
func (r jnyResp) ts(t time.Time) string {
	if r.stamp != nil {
		return r.stamp(t)
	}
	return jnyTime(t)
}

func (r jnyResp) assertionXML() string {
	scdIn := r.requestID
	if r.scdInResponseTo != nil {
		scdIn = *r.scdInResponseTo
	}
	restriction := ""
	if !r.noAudience {
		audience := r.audience
		if audience == "" {
			audience = jnySPEntityID
		}
		restriction = fmt.Sprintf(`<saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction>`, jnyEsc(audience))
	}
	var authn, attrs strings.Builder
	for _, a := range r.authn {
		session := ""
		if !a.sessionNotOnOrAfter.IsZero() {
			session = fmt.Sprintf(` SessionNotOnOrAfter=%q`, r.ts(a.sessionNotOnOrAfter))
		}
		fmt.Fprintf(&authn, `<saml:AuthnStatement AuthnInstant=%q SessionIndex="_session"%s><saml:AuthnContext><saml:AuthnContextClassRef>%s</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement>`,
			r.ts(a.instant), session, jnyEsc(a.context))
	}
	for _, a := range r.attrs {
		friendly := ""
		if a.friendly != "" {
			friendly = fmt.Sprintf(` FriendlyName=%q`, a.friendly)
		}
		fmt.Fprintf(&attrs, `<saml:Attribute Name=%q%s>`, a.name, friendly)
		for _, v := range a.values {
			fmt.Fprintf(&attrs, `<saml:AttributeValue>%s</saml:AttributeValue>`, jnyEsc(v))
		}
		attrs.WriteString(`</saml:Attribute>`)
	}
	id := r.assertionID
	if id == "" {
		id = jnyRandID()
	}
	notBefore := ""
	if !r.noNotBefore {
		notBefore = fmt.Sprintf(` NotBefore=%q`, r.ts(r.notBefore))
	}
	return fmt.Sprintf(`<saml:Assertion xmlns:saml="%s" xmlns:ds="%s" ID="_assertion-%s" Version="2.0" IssueInstant=%q>`+
		`<saml:Issuer>%s</saml:Issuer>`+
		`<saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:2.0:nameid-format:persistent">%s</saml:NameID>`+
		`<saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData InResponseTo=%q NotOnOrAfter=%q Recipient=%q/></saml:SubjectConfirmation></saml:Subject>`+
		`<saml:Conditions%s NotOnOrAfter=%q>%s</saml:Conditions>`+
		`%s<saml:AttributeStatement>%s</saml:AttributeStatement></saml:Assertion>`,
		jnyNSAssertion, jnyNSDSig, id, r.ts(r.assertionIssueInstant),
		jnyEsc(r.assertionIssuer), jnyEsc(r.nameID), scdIn, r.ts(r.scdNotOnOrAfter), r.recipient,
		notBefore, r.ts(r.notOnOrAfter), restriction, authn.String(), attrs.String())
}

func jnyRandID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// jnySign signs el (which must carry an ID attribute) and places the Signature right
// after the Issuer, where the SAML schema puts it.
func jnySign(t *testing.T, el *etree.Element, k *jnyKey) {
	t.Helper()
	ks := dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{k.cert.Raw}, PrivateKey: k.key})
	ctx := dsig.NewDefaultSigningContext(ks)
	ctx.SetSignatureMethod(dsig.RSASHA256SignatureMethod)
	// SAML IdPs sign with exclusive canonicalization, which keeps an assertion's digest
	// stable when it is embedded in a Response; goxmldsig's inclusive default would not.
	ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	sig, err := ctx.ConstructSignature(el, true)
	if err != nil {
		t.Fatalf("sign %s: %v", el.Tag, err)
	}
	el.InsertChildAt(1, sig) // child 0 is the Issuer
}

func jnyParse(t *testing.T, s string) *etree.Element {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(s); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return doc.Root()
}

func jnySerialize(t *testing.T, el *etree.Element) []byte {
	t.Helper()
	doc := etree.NewDocument()
	doc.SetRoot(el.Copy())
	out, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// mintXML builds the Response document. Signing and encryption follow r.
func (r jnyResp) mintXML(t *testing.T) *etree.Element {
	t.Helper()
	assertion := jnyParse(t, r.assertionXML())
	for _, path := range r.drop {
		el := assertion.FindElement("./" + path)
		if el == nil {
			t.Fatalf("the assertion has no %s to drop", path)
		}
		el.Parent().RemoveChild(el)
	}
	if r.sign == "assertion" || r.sign == "both" {
		jnySign(t, assertion, r.signer)
	}
	respIn := r.requestID
	if r.respInResponseTo != nil {
		respIn = *r.respInResponseTo
	}
	resp := jnyParse(t, fmt.Sprintf(`<samlp:Response xmlns:samlp="%s" xmlns:saml="%s" xmlns:ds="%s" ID="_response-%s" Version="2.0" IssueInstant=%q Destination=%q InResponseTo=%q>`+
		`<saml:Issuer>%s</saml:Issuer>`+
		`<samlp:Status><samlp:StatusCode Value=%q/></samlp:Status></samlp:Response>`,
		jnyNSProtocol, jnyNSAssertion, jnyNSDSig, jnyRandID(), r.ts(r.respIssueInstant), r.destination, respIn,
		jnyEsc(r.respIssuer), r.status))
	if r.encryptTo != nil {
		resp.AddChild(jnyEncryptAssertion(t, jnySerialize(t, assertion), r.encryptTo, r.encryptAlg))
	} else {
		resp.AddChild(assertion)
	}
	if r.sign == "response" || r.sign == "both" {
		jnySign(t, resp, r.signer)
	}
	return resp
}

// mint renders the Response as the base64 form field an IdP posts to the ACS.
func (r jnyResp) mint(t *testing.T) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(jnySerialize(t, r.mintXML(t)))
}

// jnyEncryptAssertion wraps the serialized assertion in an <EncryptedAssertion>: a random
// AES key encrypts it (CBC, XML Encryption padding), and RSA-OAEP-MGF1P(SHA-1) wraps that
// key to the SP's encryption certificate.
func jnyEncryptAssertion(t *testing.T, plain []byte, to *x509.Certificate, dataAlg string) *etree.Element {
	t.Helper()
	if dataAlg == "" {
		dataAlg = "http://www.w3.org/2001/04/xmlenc#aes128-cbc"
	}
	keyLen := map[string]int{
		"http://www.w3.org/2001/04/xmlenc#aes128-cbc": 16,
		"http://www.w3.org/2001/04/xmlenc#aes192-cbc": 24,
		"http://www.w3.org/2001/04/xmlenc#aes256-cbc": 32,
	}[dataAlg]
	if keyLen == 0 {
		t.Fatalf("test IdP cannot encrypt with %q", dataAlg)
	}
	aesKey := make([]byte, keyLen)
	iv := make([]byte, aes.BlockSize)
	for _, b := range [][]byte{aesKey, iv} {
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize // 1..16: zeros then the pad length
	padded := append(append([]byte{}, plain...), make([]byte, pad)...)
	padded[len(padded)-1] = byte(pad)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	wrapped, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, to.PublicKey.(*rsa.PublicKey), aesKey, nil) //nolint:gosec // see import
	if err != nil {
		t.Fatal(err)
	}
	return jnyParse(t, fmt.Sprintf(`<saml:EncryptedAssertion xmlns:saml="%s" xmlns:xenc="%s" xmlns:ds="%s">`+
		`<xenc:EncryptedData Type="%sElement"><xenc:EncryptionMethod Algorithm=%q/>`+
		`<ds:KeyInfo><xenc:EncryptedKey><xenc:EncryptionMethod Algorithm=%q><ds:DigestMethod Algorithm="%ssha1"/></xenc:EncryptionMethod>`+
		`<ds:KeyInfo><ds:X509Data><ds:X509Certificate>%s</ds:X509Certificate></ds:X509Data></ds:KeyInfo>`+
		`<xenc:CipherData><xenc:CipherValue>%s</xenc:CipherValue></xenc:CipherData></xenc:EncryptedKey></ds:KeyInfo>`+
		`<xenc:CipherData><xenc:CipherValue>%s</xenc:CipherValue></xenc:CipherData></xenc:EncryptedData></saml:EncryptedAssertion>`,
		jnyNSAssertion, jnyNSXMLEnc, jnyNSDSig, jnyNSXMLEnc, dataAlg, jnyEncRSAOAEP, jnyNSDSig,
		base64.StdEncoding.EncodeToString(to.Raw),
		base64.StdEncoding.EncodeToString(wrapped), base64.StdEncoding.EncodeToString(append(iv, ct...))))
}

// jnyAuthnRequest is the part of the AuthnRequest an IdP reads.
type jnyAuthnRequest struct {
	ID                          string `xml:"ID,attr"`
	Version                     string `xml:"Version,attr"`
	IssueInstant                string `xml:"IssueInstant,attr"`
	Destination                 string `xml:"Destination,attr"`
	ProtocolBinding             string `xml:"ProtocolBinding,attr"`
	AssertionConsumerServiceURL string `xml:"AssertionConsumerServiceURL,attr"`
	Issuer                      string `xml:"Issuer"`
	HasSignature                bool   `xml:"-"`
}

// jnyDecodeRedirect reads what the IdP receives on the HTTP-Redirect binding: the query
// and the inflated AuthnRequest.
func jnyDecodeRedirect(t *testing.T, redirect string) (jnyAuthnRequest, url.Values) {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("redirect %q: %v", redirect, err)
	}
	q := u.Query()
	deflated, err := base64.StdEncoding.DecodeString(q.Get("SAMLRequest"))
	if err != nil {
		t.Fatalf("SAMLRequest is not base64: %v", err)
	}
	raw, err := io.ReadAll(flate.NewReader(bytes.NewReader(deflated)))
	if err != nil {
		t.Fatalf("SAMLRequest is not deflated XML: %v", err)
	}
	var req jnyAuthnRequest
	if err := xml.Unmarshal(raw, &req); err != nil {
		t.Fatalf("AuthnRequest: %v\n%s", err, raw)
	}
	req.HasSignature = bytes.Contains(raw, []byte("Signature"))
	return req, q
}
