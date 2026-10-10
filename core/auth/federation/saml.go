// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/beevik/etree"
	saml2 "github.com/russellhaering/gosaml2"
	"github.com/russellhaering/gosaml2/types"
	dsig "github.com/russellhaering/goxmldsig"
	dsigtypes "github.com/russellhaering/goxmldsig/types"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

const (
	// samlMaxIssueDelay is how long after its IssueInstant a response or assertion is
	// still accepted; samlMaxClockSkew is the IdP/SP clock difference tolerated on the
	// Conditions validity window.
	samlMaxIssueDelay = 90 * time.Second
	samlMaxClockSkew  = 180 * time.Second

	// samlMaxXMLTokens bounds a response document; gosaml2 enforces it, and the KeyInfo
	// pre-parse must not read what gosaml2 would refuse by size. 50,000 tokens hold
	// roughly 16,000 attribute values.
	samlMaxXMLTokens = 50000

	// samlMetadataValidity is how long the published SP metadata says it is valid.
	samlMetadataValidity = 48 * time.Hour

	samlNameIDTransient = "urn:oasis:names:tc:SAML:2.0:nameid-format:transient"
	samlNameIDEntity    = "urn:oasis:names:tc:SAML:2.0:nameid-format:entity"
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
// They are the algorithms this SP has always advertised (AES-CBC data encryption and
// the RSA-OAEP key transport), minus aes192-cbc, which gosaml2 cannot decrypt: what we
// advertise we decrypt. There is no EC/ECDH-ES key agreement, so an encryption keypair
// MUST be RSA (enforced in loadEncryptionKeypair).
var samlEncryptionMethods = []types.EncryptionMethod{
	{Algorithm: types.MethodAES256CBC},
	{Algorithm: types.MethodAES128CBC},
	{Algorithm: types.MethodRSAOAEP},
}

// samlProvider implements SAML 2.0 SP-initiated Web Browser SSO via gosaml2. The core
// owns the AuthnRequest id (so InResponseTo is enforced against a value it persisted)
// and CSRF; this provider builds the request, has gosaml2 verify the signature,
// decrypt and check the response, enforces what gosaml2 leaves to the caller
// (InResponseTo, issue delay, validity windows, audience), adds the bearer-assertion
// replay protection gosaml2 omits, and extracts the identity.
//
// regulated SP keys. The SP carries up to TWO independent keypairs in the
// idiomatic SAML split (separate metadata KeyDescriptors): an ENCRYPTION keypair
// (RSA only) that decrypts IdP-encrypted assertions, and a SIGNING keypair (RSA or
// EC) that signs AuthnRequests. gosaml2 holds both on one SAMLServiceProvider. The
// published SP metadata advertises whichever keypairs are configured, each with its
// real certificate and use.
type samlProvider struct {
	assurance *model.FederationAssuranceMapping
	sp        *saml2.SAMLServiceProvider

	signCert *x509.Certificate // SP signing certificate (nil = unsigned requests)
	encCert  *x509.Certificate // SP encryption certificate (nil = no encrypted-assertion support)

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
	assurance                      *model.FederationAssuranceMapping
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

	idpMeta, err := fetchIDPMetadata(context.Background(), metadataURL.String())
	if err != nil {
		return nil, fmt.Errorf("%w: fetch IdP metadata: %w", ErrNotConfigured, err)
	}
	// gosaml2 compares the issuer of a response and of its assertions only when the
	// expected one is set, so an IdP without an entity id would accept any issuer.
	if idpMeta.EntityID == "" {
		return nil, fmt.Errorf("%w: IdP metadata has no entityID", ErrNotConfigured)
	}
	idpCerts, err := idpSigningCerts(idpMeta)
	if err != nil {
		return nil, fmt.Errorf("%w: IdP metadata: %w", ErrNotConfigured, err)
	}

	prov := &samlProvider{
		assurance: p.assurance,
		sp: &saml2.SAMLServiceProvider{
			IdentityProviderSSOURL:      p.idpSSO,
			IdentityProviderIssuer:      idpMeta.EntityID,
			ServiceProviderIssuer:       p.entityID,
			AssertionConsumerServiceURL: acsURL.String(),
			IDPCertificateStore:         &dsig.MemoryX509CertificateStore{Roots: idpCerts},
			NameIdFormat:                samlNameIDTransient,
			Clock:                       dsig.NewRealClock(),
			MaximumXMLTokens:            samlMaxXMLTokens,
		},
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
		// ponytail: gosaml2 v0.12.0 decrypts only with the deprecated SPKeyStore field;
		// SetSPKeyStore is not read by its decryption path. Switch when a release fixes it
		// (the encrypted-assertion journey fails if that changes silently).
		prov.sp.SPKeyStore = dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{encCert.Raw}, PrivateKey: encKey}) //nolint:staticcheck // see above
		prov.encCert = encCert
	}

	// Signing keypair (RSA or EC): signs the AuthnRequest on the start leg.
	if p.signCertPEM != "" || p.signKeyPEM != "" {
		signKey, signCert, method, err := loadSigningKeypair(p.signCertPEM, p.signKeyPEM)
		if err != nil {
			return nil, err
		}
		if err := prov.sp.SetSPSigningKeyStore(&saml2.KeyStore{Signer: signKey, Cert: signCert.Raw}); err != nil {
			return nil, fmt.Errorf("%w: SP signing keypair: %v", ErrNotConfigured, err)
		}
		prov.sp.SignAuthnRequests = true
		prov.sp.SignAuthnRequestsAlgorithm = method
		prov.signCert = signCert
	}

	return &Provider{protocol: auth.ProtocolSAML, saml: prov}, nil
}

// fetchIDPMetadata downloads and parses the IdP metadata document.
func fetchIDPMetadata(ctx context.Context, metadataURL string) (*types.EntityDescriptor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseIDPMetadata(data)
}

// parseIDPMetadata reads an <EntityDescriptor>, or the first entity with an IdP role in
// an <EntitiesDescriptor> (the shape federations publish).
func parseIDPMetadata(data []byte) (*types.EntityDescriptor, error) {
	var entities []types.EntityDescriptor
	var one types.EntityDescriptor
	if err := xml.Unmarshal(data, &one); err == nil {
		entities = []types.EntityDescriptor{one}
	} else {
		var many struct {
			Entities []types.EntityDescriptor `xml:"urn:oasis:names:tc:SAML:2.0:metadata EntityDescriptor"`
		}
		if manyErr := xml.Unmarshal(data, &many); manyErr != nil {
			return nil, errors.Join(err, manyErr)
		}
		entities = many.Entities
	}
	for i := range entities {
		if entities[i].IDPSSODescriptor != nil {
			return &entities[i], nil
		}
	}
	return nil, errors.New("no entity found with an IDPSSODescriptor")
}

// idpSigningCerts returns the IdP certificates a signature may be verified against: the
// KeyDescriptors whose use is "signing" or unset.
func idpSigningCerts(md *types.EntityDescriptor) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for _, kd := range md.IDPSSODescriptor.KeyDescriptors {
		if kd.Use != "" && kd.Use != "signing" {
			continue
		}
		for _, c := range kd.KeyInfo.X509Data.X509Certificates {
			der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.Data), ""))
			if err != nil {
				return nil, fmt.Errorf("cannot parse certificate: %w", err)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				return nil, fmt.Errorf("cannot parse certificate: %w", err)
			}
			certs = append(certs, cert)
		}
	}
	if len(certs) == 0 {
		return nil, errors.New("no signing certificate in the IdP SSO descriptor")
	}
	return certs, nil
}

// loadEncryptionKeypair parses the SP encryption keypair. It MUST be RSA: XML
// Encryption key transport here is RSA-OAEP; there is no EC/ECDH-ES support, so an EC
// key here would advertise an encryption capability the SP cannot honor. An EC key is
// rejected with that explicit reason (use an EC key for the SIGNING role instead).
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
// signing (the regulated-bar case XML Encryption cannot serve for decryption).
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
	md := &types.EntityDescriptor{
		ValidUntil: s.sp.Clock.Now().UTC().Add(samlMetadataValidity),
		EntityID:   s.sp.ServiceProviderIssuer,
		SPSSODescriptor: &types.SPSSODescriptor{
			AuthnRequestsSigned:        s.signCert != nil,
			WantAssertionsSigned:       true,
			ProtocolSupportEnumeration: saml2.SAMLProtocolNamespace,
			KeyDescriptors:             s.keyDescriptors(),
			AssertionConsumerServices: []types.IndexedEndpoint{{
				Binding:  saml2.BindingHttpPost,
				Location: s.sp.AssertionConsumerServiceURL,
				Index:    1,
			}},
		},
	}
	out, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("saml: marshal metadata: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// keyDescriptors builds the metadata KeyDescriptors for the configured certs: a
// signing descriptor for the signing cert and an encryption descriptor (with the
// methods the SP can actually decrypt) for the encryption cert.
func (s *samlProvider) keyDescriptors() []types.KeyDescriptor {
	var kds []types.KeyDescriptor
	if s.signCert != nil {
		kds = append(kds, certKeyDescriptor("signing", s.signCert, nil))
	}
	if s.encCert != nil {
		kds = append(kds, certKeyDescriptor("encryption", s.encCert, samlEncryptionMethods))
	}
	return kds
}

// certKeyDescriptor builds a single KeyDescriptor carrying cert under the given use.
func certKeyDescriptor(use string, cert *x509.Certificate, encMethods []types.EncryptionMethod) types.KeyDescriptor {
	return types.KeyDescriptor{
		Use: use,
		KeyInfo: dsigtypes.KeyInfo{
			X509Data: dsigtypes.X509Data{
				X509Certificates: []dsigtypes.X509Certificate{
					{Data: base64.StdEncoding.EncodeToString(cert.Raw)},
				},
			},
		},
		EncryptionMethods: encMethods,
	}
}

// beginAuth builds the AuthnRequest, stamps it with the core-provided id (so the
// response's InResponseTo can be validated), signs it when a signing key is wired,
// and returns the HTTP-Redirect URL.
func (s *samlProvider) beginAuth(_ context.Context, p auth.AuthParams) (string, error) {
	// The redirect binding signs the query string, not the document, so the document is
	// built unsigned.
	doc, err := s.sp.BuildAuthRequestDocumentNoSig()
	if err != nil {
		return "", fmt.Errorf("saml: make authn request: %w", err)
	}
	root := doc.Root()
	// Override the library-generated id with the core's persisted RequestID so the
	// response InResponseTo is checked against a value the engine holds.
	root.CreateAttr("ID", p.RequestID)
	if issuer := root.SelectElement("Issuer"); issuer != nil {
		issuer.CreateAttr("Format", samlNameIDEntity) // the Issuer format IdPs have always received
	}
	// RelayState carries the core's CSRF state through the IdP round trip. Only
	// BuildAuthURLRedirect signs the query (SigAlg/Signature) when signing is wired; the
	// POST-binding builder would leave the redirect unsigned.
	redirect, err := s.sp.BuildAuthURLRedirect(p.State, doc)
	if err != nil {
		return "", fmt.Errorf("saml: redirect: %w", err)
	}
	return redirect, nil
}

// dropEmptyKeyInfo removes a <KeyInfo> that names no X509Certificate from the signatures
// of a response, so the signature is verified against the IdP certificate from the
// metadata instead of being refused (goxmldsig insists on a certificate in KeyInfo when
// KeyInfo is present; some IdPs send a bare KeyValue). KeyInfo is the sender's own claim
// about its key, never trust: a certificate it does name must still be one the metadata
// lists. A payload that is not plain XML, or is over the size bound, is left for gosaml2
// to judge, and so is a signature inside an encrypted assertion.
func dropEmptyKeyInfo(encoded string) string {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded
	}
	// This reads unverified, unauthenticated input: skip what holds no KeyInfo, and what
	// gosaml2 refuses by size, so the pre-parse costs no more than gosaml2's own.
	if !bytes.Contains(raw, []byte("KeyInfo")) || 3*bytes.Count(raw, []byte("<"))+1 > samlMaxXMLTokens {
		return encoded
	}
	doc := etree.NewDocument()
	if doc.ReadFromBytes(raw) != nil {
		return encoded
	}
	changed := false
	for _, keyInfo := range doc.FindElements("//Signature/KeyInfo") {
		if keyInfo.FindElement(".//X509Certificate") == nil {
			keyInfo.Parent().RemoveChild(keyInfo)
			changed = true
		}
	}
	if !changed {
		return encoded
	}
	out, err := doc.WriteToBytes()
	if err != nil {
		return encoded
	}
	return base64.StdEncoding.EncodeToString(out)
}

// validate parses and verifies the SAML Response, decrypts an EncryptedAssertion
// when the SP has an encryption key, enforces replay protection, and extracts the
// NameID + email.
func (s *samlProvider) validate(_ context.Context, a auth.Assertion) (auth.FederatedIdentity, error) {
	if a.RequestID == "" {
		return auth.FederatedIdentity{}, errors.New("saml: no request id to bind the response to")
	}
	// ValidateEncodedResponse verifies the signature (on the response, else on every
	// assertion), transparently decrypts an EncryptedAssertion with the SPKeyStore key,
	// and checks the response Destination and Status, the Issuer of the response and of
	// each assertion against the IdP entity id, and each bearer confirmation's Recipient
	// and NotOnOrAfter.
	response, err := s.sp.ValidateEncodedResponse(dropEmptyKeyInfo(a.Raw))
	if err != nil {
		return auth.FederatedIdentity{}, fmt.Errorf("saml: parse response: %w", err)
	}
	assertion, replayUntil, err := s.checkResponse(response, a.RequestID)
	if err != nil {
		return auth.FederatedIdentity{}, fmt.Errorf("saml: %w", err)
	}
	if assertion.ID == "" {
		return auth.FederatedIdentity{}, errors.New("saml: assertion has no ID")
	}
	// Replay protection (gosaml2 does not do this): reject a re-used assertion id
	// until its bearer SubjectConfirmationData NotOnOrAfter passes.
	if !s.replay.admit(assertion.ID, replayUntil) {
		return auth.FederatedIdentity{}, errors.New("saml: assertion replay rejected")
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
		return auth.FederatedIdentity{}, errors.New("saml: no email attribute or email NameID")
	}
	// assertion.Issuer.Value is the IdP entityID: ValidateEncodedResponse already
	// rejected any assertion whose Issuer != the trusted IdP entity id, so by here it is
	// the verified issuing IdP identity, safe to qualify the subject with (U3).
	// checkAssertion has guaranteed the pointer is set.
	id := auth.FederatedIdentity{Protocol: auth.ProtocolSAML, Subject: nameID, Issuer: assertion.Issuer.Value, Email: email, DisplayName: name}
	id.AAL, id.AuthenticatedAt = s.assertionAssurance(assertion)
	if s.groupsAttr != "" {
		id.Groups = s.extractGroups(assertion)
	}
	return id, nil
}

// checkResponse applies what gosaml2 leaves to the caller to a response it has already
// verified: the response answers OUR request (SP-initiated only, so an unsolicited or
// stolen response cannot start a session), it is fresh, and every assertion passes
// checkAssertion. It returns the assertion to use and until when its id must be
// remembered.
func (s *samlProvider) checkResponse(r *types.Response, requestID string) (*types.Assertion, time.Time, error) {
	if r.InResponseTo != requestID {
		return nil, time.Time{}, errors.New("response InResponseTo does not match the request")
	}
	if r.Issuer == nil || r.Issuer.Value != s.sp.IdentityProviderIssuer {
		return nil, time.Time{}, errors.New("response issuer is not the IdP")
	}
	now := s.sp.Clock.Now()
	if r.IssueInstant.Add(samlMaxIssueDelay).Before(now) {
		return nil, time.Time{}, fmt.Errorf("response issued at %s is too old", r.IssueInstant.Format(time.RFC3339))
	}
	if len(r.Assertions) == 0 {
		return nil, time.Time{}, errors.New("response carries no assertion")
	}
	var replayUntil time.Time
	for i := range r.Assertions {
		until, err := s.checkAssertion(&r.Assertions[i], requestID, now)
		if err != nil {
			return nil, time.Time{}, err
		}
		if i == 0 {
			replayUntil = until
		}
	}
	return &r.Assertions[0], replayUntil, nil
}

// checkAssertion enforces on one verified assertion what gosaml2's Validate does not:
// the issuer, freshness, the bearer confirmation answering our request, the Conditions
// window (with the clock-skew allowance) and the audience (every restriction must name
// us; none at all is accepted, as it always was, since InResponseTo and Recipient bind
// the assertion to this flow). It returns the confirmation's NotOnOrAfter, which bounds
// the replay window. gosaml2 already rejects a missing Subject or confirmation; they are
// checked again so a change in the library cannot become a nil dereference on login.
func (s *samlProvider) checkAssertion(a *types.Assertion, requestID string, now time.Time) (time.Time, error) {
	if a.Issuer == nil || a.Subject == nil || a.Subject.SubjectConfirmation == nil ||
		a.Subject.SubjectConfirmation.SubjectConfirmationData == nil || a.Conditions == nil {
		return time.Time{}, errors.New("assertion lacks its issuer, subject confirmation or conditions")
	}
	if a.Issuer.Value != s.sp.IdentityProviderIssuer {
		return time.Time{}, errors.New("assertion issuer is not the IdP")
	}
	if a.IssueInstant.Add(samlMaxIssueDelay).Before(now) {
		return time.Time{}, fmt.Errorf("assertion issued at %s is too old", a.IssueInstant.Format(time.RFC3339))
	}
	confirmation := a.Subject.SubjectConfirmation.SubjectConfirmationData
	if confirmation.InResponseTo != requestID {
		return time.Time{}, errors.New("assertion SubjectConfirmationData InResponseTo does not match the request")
	}
	confirmedUntil, err := time.Parse(time.RFC3339, confirmation.NotOnOrAfter)
	if err != nil {
		return time.Time{}, fmt.Errorf("SubjectConfirmationData NotOnOrAfter: %w", err)
	}
	c := a.Conditions
	notOnOrAfter, err := time.Parse(time.RFC3339, c.NotOnOrAfter)
	if err != nil {
		return time.Time{}, fmt.Errorf("conditions NotOnOrAfter: %w", err)
	}
	if notOnOrAfter.Add(samlMaxClockSkew).Before(now) {
		return time.Time{}, errors.New("assertion conditions are expired")
	}
	if c.NotBefore != "" {
		notBefore, err := time.Parse(time.RFC3339, c.NotBefore)
		if err != nil {
			return time.Time{}, fmt.Errorf("conditions NotBefore: %w", err)
		}
		if notBefore.Add(-samlMaxClockSkew).After(now) {
			return time.Time{}, errors.New("assertion conditions are not yet valid")
		}
	}
	for _, restriction := range c.AudienceRestrictions {
		if !slices.ContainsFunc(restriction.Audiences, func(au types.Audience) bool { return au.Value == s.sp.ServiceProviderIssuer }) {
			return time.Time{}, fmt.Errorf("assertion audience restriction does not name %q", s.sp.ServiceProviderIssuer)
		}
	}
	return confirmedUntil, nil
}

// attributes lists the assertion's attributes (an assertion has at most one
// AttributeStatement in gosaml2's types).
func attributes(a *types.Assertion) []types.Attribute {
	if a.AttributeStatement == nil {
		return nil
	}
	return a.AttributeStatement.Attributes
}

// extractGroups reads EVERY value of the configured multi-valued groups attribute
// (Name or FriendlyName) — SAML groups arrive as repeated AttributeValues, unlike email
// which is single-valued. Blank values are dropped; "" ⇒ no groups (fail-inert).
func (s *samlProvider) extractGroups(a *types.Assertion) []string {
	var out []string
	for _, attr := range attributes(a) {
		if strings.EqualFold(attr.Name, s.groupsAttr) || strings.EqualFold(attr.FriendlyName, s.groupsAttr) {
			for _, v := range attr.Values {
				if g := strings.TrimSpace(v.Value); g != "" {
					out = append(out, g)
				}
			}
		}
	}
	return out
}

// extractAttrs reads the email (configured attribute or a common name) and a
// display name from the assertion's attributes.
func (s *samlProvider) extractAttrs(a *types.Assertion) (email, name string) {
	get := func(want string) string {
		for _, attr := range attributes(a) {
			if strings.EqualFold(attr.Name, want) || strings.EqualFold(attr.FriendlyName, want) {
				if len(attr.Values) > 0 {
					return attr.Values[0].Value
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
