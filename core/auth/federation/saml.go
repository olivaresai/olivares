// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/olivaresai/olivares/core/auth"
)

// commonEmailAttrs are the SAML Attribute Names IdPs use for email, tried in
// order when no explicit attribute is configured (the Name varies by IdP).
var commonEmailAttrs = []string{
	"email",
	"emailAddress",
	"urn:oid:0.9.2342.19200300.100.1.3",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
}

// samlEncryptionMethods are the algorithms the SP advertises (use="encryption")
// in its published metadata so an IdP knows how to encrypt an assertion to it.
// They are EXACTLY the set crewjam's xmlenc can decrypt (block ciphers + the
// RSA-OAEP key transport): there is no EC/ECDH-ES key agreement, so an encryption
// keypair MUST be RSA (enforced in loadEncryptionKeypair).
var samlEncryptionMethods = []saml.EncryptionMethod{
	{Algorithm: "http://www.w3.org/2001/04/xmlenc#aes256-cbc"},
	{Algorithm: "http://www.w3.org/2001/04/xmlenc#aes192-cbc"},
	{Algorithm: "http://www.w3.org/2001/04/xmlenc#aes128-cbc"},
	{Algorithm: "http://www.w3.org/2001/04/xmlenc#rsa-oaep-mgf1p"},
}

// samlProvider implements SAML 2.0 SP-initiated Web Browser SSO via crewjam/saml.
// The core owns the AuthnRequest id (so InResponseTo is enforced against a value
// it persisted) and CSRF; this provider builds the request, validates the signed
// response (signature/conditions/audience/Recipient/InResponseTo via
// ParseResponse), adds the bearer-assertion replay protection crewjam omits, and
// extracts the identity.
//
// regulated SP keys. The SP carries up to TWO independent keypairs in the
// idiomatic SAML split (separate metadata KeyDescriptors): an ENCRYPTION keypair
// (RSA only) that decrypts IdP-encrypted assertions, and a SIGNING keypair (RSA or
// EC) that signs AuthnRequests. crewjam's ServiceProvider holds a single Key, so the
// two roles live on two ServiceProvider values that share the IdP trust and SP
// endpoints: beginSP (signs the request on the start leg) and validateSP (decrypts
// the assertion on the callback leg). The published SP metadata advertises whichever
// keypairs are configured, each with its real certificate and use.
type samlProvider struct {
	// beginSP makes+signs the AuthnRequest on the start leg. It carries the SIGNING
	// keypair (+SignatureMethod) when one is configured, else no key (unsigned
	// request — the pre behavior).
	beginSP saml.ServiceProvider
	// validateSP parses the SAML Response on the callback leg. It carries the
	// ENCRYPTION (RSA) keypair when one is configured, so crewjam's ParseResponse
	// decrypts an EncryptedAssertion; without it, only a plaintext assertion parses.
	validateSP saml.ServiceProvider
	// metaSP supplies the metadata envelope (EntityID, ACS endpoints); the
	// KeyDescriptors and AuthnRequestsSigned flag are set from the configured certs.
	metaSP saml.ServiceProvider

	signCert *x509.Certificate // SP signing certificate (nil = unsigned requests)
	encCert  *x509.Certificate // SP encryption certificate (nil = no encrypted-assertion support)

	idpSSOURL string
	emailAttr string
	// groupsAttr is the multi-valued attribute carrying the subject's directory
	// groups (U1); "" ⇒ groups are not read.
	groupsAttr string
	replay     *replayStore
}

func samlFromEnv(getenv func(string) string) (*Provider, error) {
	return samlFromParts(samlParts{
		metaURL:     getenv(envSAMLMetadataURL),
		entityID:    getenv(envSAMLEntityID),
		acs:         getenv(envSAMLACSURL),
		idpSSO:      getenv(envSAMLIDPSSOURL),
		encCertPEM:  getenv(envSAMLCertPEM),
		encKeyPEM:   getenv(envSAMLKeyPEM),
		signCertPEM: getenv(envSAMLSignCertPEM),
		signKeyPEM:  getenv(envSAMLSignKeyPEM),
		emailAttr:   getenv(envSAMLEmailAttr),
		groupsAttr:  getenv(envSAMLGroupsAttr),
	})
}

// samlParts is the plaintext SAML config shared by the env and managed-config
// builders. The two keypairs are independent: either, both, or neither may be set.
type samlParts struct {
	metaURL, entityID, acs, idpSSO string
	encCertPEM, encKeyPEM          string // encryption keypair (RSA only)
	signCertPEM, signKeyPEM        string // signing keypair (RSA or EC)
	emailAttr                      string
	groupsAttr                     string // multi-valued groups attribute
}

// samlFromParts builds the SAML provider from explicit parts (shared by the env
// and the managed-config paths). It fetches the IdP metadata, so a
// transient outage surfaces here as ErrNotConfigured (fail-closed).
func samlFromParts(p samlParts) (*Provider, error) {
	if p.metaURL == "" || p.entityID == "" || p.acs == "" || p.idpSSO == "" {
		return nil, fmt.Errorf("%w: saml metadata_url, entity_id, acs_url and idp_sso_url are required", ErrNotConfigured)
	}
	acsURL, err := mustAbsURL(p.acs)
	if err != nil {
		return nil, err
	}
	metadataURL, err := mustAbsURL(p.metaURL)
	if err != nil {
		return nil, err
	}
	if _, err := mustAbsURL(p.idpSSO); err != nil {
		return nil, err
	}

	idpMeta, err := samlsp.FetchMetadata(context.Background(), httpClient(), *metadataURL)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch IdP metadata: %v", ErrNotConfigured, err)
	}

	// The shared base: IdP trust + SP endpoints, no key material. Both legs and the
	// metadata envelope are cloned from it so they never drift.
	base := saml.ServiceProvider{
		EntityID:    p.entityID,
		AcsURL:      *acsURL,
		IDPMetadata: idpMeta,
		// IdP-initiated responses have no InResponseTo and lose the CSRF/replay
		// binding; require SP-initiated only.
		AllowIDPInitiated: false,
	}

	sp := &samlProvider{
		beginSP:    base,
		validateSP: base,
		metaSP:     base,
		idpSSOURL:  p.idpSSO,
		emailAttr:  p.emailAttr,
		groupsAttr: p.groupsAttr,
		replay:     newReplayStore(),
	}

	// Encryption keypair (RSA only): decrypts EncryptedAssertions on the callback leg.
	if p.encCertPEM != "" || p.encKeyPEM != "" {
		encKey, encCert, err := loadEncryptionKeypair(p.encCertPEM, p.encKeyPEM)
		if err != nil {
			return nil, err
		}
		sp.validateSP.Key = encKey
		sp.validateSP.Certificate = encCert
		sp.encCert = encCert
	}

	// Signing keypair (RSA or EC): signs the AuthnRequest on the start leg.
	if p.signCertPEM != "" || p.signKeyPEM != "" {
		signKey, signCert, method, err := loadSigningKeypair(p.signCertPEM, p.signKeyPEM)
		if err != nil {
			return nil, err
		}
		sp.beginSP.Key = signKey
		sp.beginSP.Certificate = signCert
		sp.beginSP.SignatureMethod = method
		sp.signCert = signCert
	}

	return &Provider{protocol: auth.ProtocolSAML, saml: sp}, nil
}

// loadEncryptionKeypair parses the SP encryption keypair. It MUST be RSA: XML
// Encryption key transport (RSA-OAEP) is the only scheme crewjam's xmlenc decrypts;
// there is no EC/ECDH-ES support, so an EC key here would advertise an encryption
// capability the SP cannot honor. An EC key is rejected with that explicit reason
// (use an EC key for the SIGNING role instead).
func loadEncryptionKeypair(certPEM, keyPEM string) (*rsa.PrivateKey, *x509.Certificate, error) {
	if certPEM == "" || keyPEM == "" {
		return nil, nil, fmt.Errorf("%w: SP encryption keypair needs both cert and key", ErrNotConfigured)
	}
	kp, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: SP encryption keypair: %v", ErrNotConfigured, err)
	}
	key, ok := kp.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("%w: SP encryption key must be RSA — SAML assertion encryption uses RSA-OAEP key transport (no EC/ECDH-ES); use an EC key only for the signing keypair", ErrNotConfigured)
	}
	cert, err := x509.ParseCertificate(kp.Certificate[0])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: SP encryption cert: %v", ErrNotConfigured, err)
	}
	return key, cert, nil
}

// loadSigningKeypair parses the SP signing keypair (RSA or EC) and picks the
// matching XML-DSig signature method, so an EC SP key is honored for request
// signing (the regulated-bar case xmlenc cannot serve for decryption).
func loadSigningKeypair(certPEM, keyPEM string) (crypto.Signer, *x509.Certificate, string, error) {
	if certPEM == "" || keyPEM == "" {
		return nil, nil, "", fmt.Errorf("%w: SP signing keypair needs both cert and key", ErrNotConfigured)
	}
	kp, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: SP signing keypair: %v", ErrNotConfigured, err)
	}
	var method string
	switch kp.PrivateKey.(type) {
	case *rsa.PrivateKey:
		method = dsig.RSASHA256SignatureMethod
	case *ecdsa.PrivateKey:
		method = dsig.ECDSASHA256SignatureMethod
	default:
		return nil, nil, "", fmt.Errorf("%w: SP signing key must be RSA or EC", ErrNotConfigured)
	}
	signer, ok := kp.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, nil, "", fmt.Errorf("%w: SP signing key is not a usable signer", ErrNotConfigured)
	}
	cert, err := x509.ParseCertificate(kp.Certificate[0])
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: SP signing cert: %v", ErrNotConfigured, err)
	}
	return signer, cert, method, nil
}

// SPMetadata returns the SP's SAML metadata document (XML), so an IdP can be
// onboarded by URL instead of by hand. It advertises the configured ACS endpoint
// and whichever SP keypairs are wired — a signing KeyDescriptor (with
// AuthnRequestsSigned) when a signing key is set, and an encryption KeyDescriptor
// when an encryption key is set — each with its real certificate.
func (s *samlProvider) SPMetadata() ([]byte, error) {
	md := s.metaSP.Metadata()
	if len(md.SPSSODescriptors) == 0 {
		return nil, fmt.Errorf("saml: metadata has no SP descriptor")
	}
	d := &md.SPSSODescriptors[0]
	d.KeyDescriptors = s.keyDescriptors()
	signed := s.signCert != nil
	d.AuthnRequestsSigned = &signed
	d.NameIDFormats = nonEmptyNameIDFormats(d.NameIDFormats)

	out, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("saml: marshal metadata: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// keyDescriptors builds the metadata KeyDescriptors for the configured certs: a
// signing descriptor for the signing cert and an encryption descriptor (with the
// RSA-OAEP methods the SP can actually decrypt) for the encryption cert.
func (s *samlProvider) keyDescriptors() []saml.KeyDescriptor {
	var kds []saml.KeyDescriptor
	if s.signCert != nil {
		kds = append(kds, certKeyDescriptor("signing", s.signCert, nil))
	}
	if s.encCert != nil {
		kds = append(kds, certKeyDescriptor("encryption", s.encCert, samlEncryptionMethods))
	}
	return kds
}

// certKeyDescriptor builds a single KeyDescriptor carrying cert under the given use.
func certKeyDescriptor(use string, cert *x509.Certificate, encMethods []saml.EncryptionMethod) saml.KeyDescriptor {
	return saml.KeyDescriptor{
		Use: use,
		KeyInfo: saml.KeyInfo{
			X509Data: saml.X509Data{
				X509Certificates: []saml.X509Certificate{
					{Data: base64.StdEncoding.EncodeToString(cert.Raw)},
				},
			},
		},
		EncryptionMethods: encMethods,
	}
}

// nonEmptyNameIDFormats drops empty NameIDFormat entries so the published metadata
// never carries a bare <NameIDFormat></NameIDFormat> (some IdP validators reject it).
func nonEmptyNameIDFormats(in []saml.NameIDFormat) []saml.NameIDFormat {
	out := in[:0]
	for _, f := range in {
		if strings.TrimSpace(string(f)) != "" {
			out = append(out, f)
		}
	}
	return out
}

// beginAuth builds the AuthnRequest, stamps it with the core-provided id (so the
// response's InResponseTo can be validated), signs it when a signing key is wired,
// and returns the HTTP-Redirect URL.
func (s *samlProvider) beginAuth(_ context.Context, p auth.AuthParams) (string, error) {
	req, err := s.beginSP.MakeAuthenticationRequest(s.idpSSOURL, saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", fmt.Errorf("saml: make authn request: %w", err)
	}
	// Override the library-generated id with the core's persisted RequestID so the
	// response InResponseTo is checked against a value the engine holds.
	req.ID = p.RequestID
	// RelayState carries the core's CSRF state through the IdP round trip. Redirect
	// signs the query (SigAlg/Signature) when beginSP.SignatureMethod is set.
	redirect, err := req.Redirect(p.State, &s.beginSP)
	if err != nil {
		return "", fmt.Errorf("saml: redirect: %w", err)
	}
	return redirect.String(), nil
}

// validate parses and verifies the SAML Response, decrypts an EncryptedAssertion
// when the SP has an encryption key, enforces replay protection, and extracts the
// NameID + email.
func (s *samlProvider) validate(_ context.Context, a auth.Assertion) (auth.FederatedIdentity, error) {
	// Reconstruct the ACS POST request crewjam's ParseResponse expects.
	form := url.Values{"SAMLResponse": {a.Raw}}
	req := &http.Request{Method: http.MethodPost, URL: &s.validateSP.AcsURL, PostForm: form, Form: form}

	// ParseResponse transparently decrypts an EncryptedAssertion using validateSP.Key
	// (the RSA encryption key) before validating the signature/conditions.
	assertion, err := s.validateSP.ParseResponse(req, []string{a.RequestID})
	if err != nil {
		return auth.FederatedIdentity{}, fmt.Errorf("saml: parse response: %w", err)
	}
	// Replay protection (crewjam does not do this): reject a re-used assertion id
	// until its bearer SubjectConfirmationData NotOnOrAfter passes.
	if !s.replay.admit(assertion) {
		return auth.FederatedIdentity{}, fmt.Errorf("saml: assertion replay rejected")
	}

	var nameID string
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		nameID = assertion.Subject.NameID.Value
	}
	email, name := s.extractAttrs(assertion)
	if email == "" {
		// Fall back to the NameID when it looks like an email.
		if strings.Contains(nameID, "@") {
			email = nameID
		}
	}
	if email == "" {
		return auth.FederatedIdentity{}, fmt.Errorf("saml: no email attribute or email NameID")
	}
	// assertion.Issuer.Value is the IdP entityID: crewjam's ParseResponse already
	// rejected any assertion whose Issuer != the trusted IDPMetadata.EntityID, so by
	// here it is the verified issuing IdP identity, safe to qualify the subject with
	// (U3). It is a value (not a pointer), so no nil-guard is needed.
	id := auth.FederatedIdentity{Subject: nameID, Issuer: assertion.Issuer.Value, Email: email, DisplayName: name}
	if s.groupsAttr != "" {
		id.Groups = s.extractGroups(assertion)
	}
	return id, nil
}

// extractGroups reads EVERY value of the configured multi-valued groups attribute
// (Name or FriendlyName), across all attribute statements — SAML groups arrive as
// repeated AttributeValues, unlike email which is single-valued. Blank values are
// dropped; "" ⇒ no groups (fail-inert).
func (s *samlProvider) extractGroups(a *saml.Assertion) []string {
	var out []string
	for _, st := range a.AttributeStatements {
		for _, attr := range st.Attributes {
			if strings.EqualFold(attr.Name, s.groupsAttr) || strings.EqualFold(attr.FriendlyName, s.groupsAttr) {
				for _, v := range attr.Values {
					if g := strings.TrimSpace(v.Value); g != "" {
						out = append(out, g)
					}
				}
			}
		}
	}
	return out
}

// extractAttrs reads the email (configured attribute or a common name) and a
// display name from the assertion's attribute statements.
func (s *samlProvider) extractAttrs(a *saml.Assertion) (email, name string) {
	get := func(want string) string {
		for _, st := range a.AttributeStatements {
			for _, attr := range st.Attributes {
				if strings.EqualFold(attr.Name, want) || strings.EqualFold(attr.FriendlyName, want) {
					if len(attr.Values) > 0 {
						return attr.Values[0].Value
					}
				}
			}
		}
		return ""
	}
	if s.emailAttr != "" {
		email = get(s.emailAttr)
	}
	for i := 0; email == "" && i < len(commonEmailAttrs); i++ {
		email = get(commonEmailAttrs[i])
	}
	for _, n := range []string{"displayName", "name", "http://schemas.microsoft.com/identity/claims/displayname"} {
		if name = get(n); name != "" {
			break
		}
	}
	return email, name
}
