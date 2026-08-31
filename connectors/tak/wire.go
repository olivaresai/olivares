// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// wire.go declares the on-disk shape of a TAK Server CoreConfig.xml and the
// (best-effort) shape of the optional live version probe. Every element and
// attribute modeled here is traceable to the TAK Server Configuration Guide v5.2
// (July 2024); nothing is derived from the GPLv3 TAK Server source tree (see
// doc.go for the clean-room provenance record). Elements the guide names but does
// not give a citable schema for (the <auth> backend, the federation element) are
// either modeled as opaque presence or deliberately not modeled at all — this
// connector does not invent a shape it cannot cite.

// CoreConfig parse errors. Sentinel-wrapped so posture.go can classify a rejected
// configuration without matching free text.
var (
	// ErrConfigEmpty is returned for an empty or whitespace-only document.
	ErrConfigEmpty = errors.New("tak: empty CoreConfig")
	// ErrConfigDoctype refuses any XML directive (DOCTYPE/ENTITY) outright, exactly
	// as cot.go does for a CoT event: a document that declares one is not a TAK
	// CoreConfig and we will not spend cycles proving an external-entity vector harmless.
	ErrConfigDoctype = errors.New("tak: CoreConfig carries an XML directive (DOCTYPE/ENTITY) — refused")
	// ErrConfigRoot is returned when the root element is not <Configuration>.
	ErrConfigRoot = errors.New("tak: CoreConfig root element is not <Configuration>")
)

// Configuration is the CoreConfig.xml root element [Configuration Guide v5.2].
// The real file carries a default XML namespace; because the field tags below name
// only local element names (no namespace), Go's decoder matches them regardless of
// the document's namespace, which is what a clean-room reader wants.
type Configuration struct {
	XMLName            xml.Name            `xml:"Configuration"`
	Network            *Network            `xml:"network"`
	Auth               *Auth               `xml:"auth"`
	Security           *Security           `xml:"security"`
	CertificateSigning *CertificateSigning `xml:"certificateSigning"`
}

// Network holds the series of <input> listeners and the optional <announce>
// self-announcement element [Configuration Guide v5.2, <network>].
type Network struct {
	Inputs   []Input   `xml:"input"`
	Announce *Announce `xml:"announce"`
}

// Input is one CoT listener. Protocol values documented by the guide:
//
//	udp   — "standard CoT udp protocol; unencrypted"
//	mcast — "like udp, but has additional configuration option for multicast group"
//	tcp   — "publish-only port; standard CoT tcp protocol; unencrypted"
//	stcp  — "streaming/bi-directional; this is for ATAK to connect to. Unencrypted,
//	         for testing only"
//	tls   — "TCP+TLS streaming/bi-directional for encrypted communication with TAK clients"
//
// Guide examples:
//
//	<input _name="stdssl"   protocol="tls"  port="8089" auth="x509"/>
//	<input _name="streamtcp" protocol="stcp" port="8088"/>
//	<input _name="stdudp"   protocol="udp"  port="8087"><filtergroup>TEST1</filtergroup></input>
//	<input _name="SAproxy"  protocol="mcast" group="239.2.3.1" port="6969" proxy="true"/>
type Input struct {
	Name     string `xml:"_name,attr"`
	Protocol string `xml:"protocol,attr"`
	Port     string `xml:"port,attr"`
	// Auth is the per-input auth ATTRIBUTE (e.g. "x509"). The guide notes a
	// <filtergroup> "cannot be used in conjunction with the auth attribute on the
	// same input"; an input with neither falls into the __ANON__ anonymous group.
	Auth  string `xml:"auth,attr"`
	Group string `xml:"group,attr"`
	Proxy string `xml:"proxy,attr"`
	// Archive is carried because it appears on inputs in the guide; no posture
	// finding currently reads it.
	Archive string `xml:"archive,attr"`
	// FilterGroups are the <filtergroup> child elements. "If there is no filtergroup
	// specified, the default is ... a special anonymous group ... named \"__ANON__\"."
	FilterGroups []string `xml:"filtergroup"`
}

// Announce is the SA self-announcement element. Guide example:
//
//	<announce enable="true" uid="Marti1" group="239.2.3.1" port="6969" interval="1" ip="..."/>
type Announce struct {
	Enable   string `xml:"enable,attr"`
	UID      string `xml:"uid,attr"`
	Group    string `xml:"group,attr"`
	Port     string `xml:"port,attr"`
	Interval string `xml:"interval,attr"`
	IP       string `xml:"ip,attr"`
}

// Auth is the top-level <auth> backend element. The Configuration Guide v5.2 names
// the two backend kinds ("you can use either a flat file or an LDAP backend for
// group filtering support") but does not publish a citable attribute/child schema,
// so this connector does NOT model its internals: presence is captured (a backend
// is configured) and nothing more is asserted. No posture finding reads it today.
type Auth struct{}

// Security wraps the <security> element's <tls> child [Configuration Guide v5.2].
type Security struct {
	TLS *TLSConfig `xml:"tls"`
}

// TLSConfig is the <security><tls> element. Guide example:
//
//	<tls context="TLSv1" keymanager="SunX509" keystore="JKS"
//	     keystoreFile="certs/files/takserver.jks" keystorePass="atakatak"
//	     truststore="JKS" truststoreFile="certs/files/truststore-root.jks"
//	     truststorePass="atakatak">
//	  <!-- <crl _name="Marti CA" crlFile="certs/ca.crl"/> -->
//	</tls>
//
// keystorePass/truststorePass are carried so posture.go can compare them against
// the documented default ("atakatak") WITHOUT ever emitting the value.
type TLSConfig struct {
	Context        string `xml:"context,attr"`
	KeyManager     string `xml:"keymanager,attr"`
	Keystore       string `xml:"keystore,attr"`
	KeystoreFile   string `xml:"keystoreFile,attr"`
	KeystorePass   string `xml:"keystorePass,attr"`
	Truststore     string `xml:"truststore,attr"`
	TruststoreFile string `xml:"truststoreFile,attr"`
	TruststorePass string `xml:"truststorePass,attr"`
	// CRL is the optional <crl> child. In the guide's shipped example it is
	// COMMENTED OUT, so a stock install parses to CRL == nil — precisely the
	// "no revocation checking" posture posture.go flags.
	CRL *CRL `xml:"crl"`
}

// CRL is the certificate-revocation-list child of <tls>. Guide example:
//
//	<crl _name="Marti CA" crlFile="certs/ca.crl"/>
type CRL struct {
	Name    string `xml:"_name,attr"`
	CRLFile string `xml:"crlFile,attr"`
}

// CertificateSigning is <certificateSigning CA="{TAKServer | MicrosoftCA}"> with
// one of two backend configs [Configuration Guide v5.2].
type CertificateSigning struct {
	CA                string             `xml:"CA,attr"`
	TAKServerCAConfig *TAKServerCAConfig `xml:"TAKServerCAConfig"`
	MicrosoftCAConfig *MicrosoftCAConfig `xml:"MicrosoftCAConfig"`
}

// TAKServerCAConfig is the built-in signer config. Guide attributes:
//
//	<TAKServerCAConfig keystorePass="..." validityDays="30" signatureAlg="SHA256WithRSA"/>
type TAKServerCAConfig struct {
	KeystorePass string `xml:"keystorePass,attr"`
	ValidityDays string `xml:"validityDays,attr"`
	SignatureAlg string `xml:"signatureAlg,attr"`
}

// MicrosoftCAConfig is the external Microsoft CA signer config. Guide attributes:
//
//	<MicrosoftCAConfig username="..." password="{MS CA Password}"
//	                   truststorePass="atakatak" svcUrl="https://..."/>
//
// A non-empty password attribute is a credential living in a config file; posture.go
// flags its PRESENCE and never emits the value.
type MicrosoftCAConfig struct {
	Username       string `xml:"username,attr"`
	Password       string `xml:"password,attr"`
	TruststorePass string `xml:"truststorePass,attr"`
	SvcURL         string `xml:"svcUrl,attr"`
}

// versionResponse is a BEST-EFFORT shape for the optional live version probe.
//
// There is NO citable public schema for TAK Server's version endpoint (tak.gov's
// API reference is account-gated — see config.go), so this connector does not
// assert one. It attempts a permissive JSON decode of the common Marti envelope
// shapes and, on ANY mismatch, degrades to reporting the server as merely
// "reachable" rather than fabricate a version string. A value that IS extracted
// came verbatim from the server's own response (and is length-bounded and
// control-stripped before it is ever placed in a finding title).
type versionResponse struct {
	Version string `json:"version"`
	Data    struct {
		Version string `json:"version"`
	} `json:"data"`
}

// version returns the first non-empty version field found, or "".
func (v versionResponse) version() string {
	if v.Version != "" {
		return v.Version
	}
	return v.Data.Version
}

// parseCoreConfig strictly parses a CoreConfig.xml document. It mirrors cot.go's
// hardening: it refuses an XML directive (DOCTYPE/ENTITY), runs the decoder in
// strict mode with entity expansion disabled (only the five predefined entities),
// and rejects any root element other than <Configuration>. The caller is expected
// to have already capped the input at maxCoreConfigBytes with an io.LimitReader.
func parseCoreConfig(data []byte) (Configuration, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Configuration{}, ErrConfigEmpty
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil // only the five predefined XML entities; anything else is an error

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			// End of prolog with no element: there was no <Configuration> root.
			return Configuration{}, ErrConfigRoot
		}
		if err != nil {
			return Configuration{}, fmt.Errorf("tak: CoreConfig is not well-formed XML: %w", err)
		}

		switch t := tok.(type) {
		case xml.Directive:
			// DOCTYPE/ENTITY declarations live in the prolog, before the root; refusing
			// here shuts the external-entity door before any element is decoded.
			return Configuration{}, ErrConfigDoctype
		case xml.StartElement:
			if t.Name.Local != "Configuration" {
				return Configuration{}, fmt.Errorf("%w: <%s>", ErrConfigRoot, t.Name.Local)
			}
			var cfg Configuration
			if err := dec.DecodeElement(&cfg, &t); err != nil {
				return Configuration{}, fmt.Errorf("tak: CoreConfig is not well-formed XML: %w", err)
			}
			return cfg, nil
		}
	}
}
