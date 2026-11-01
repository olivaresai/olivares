// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package webpush sends a Web Push message (RFC 8030) the way a browser's push
// service expects it: the body encrypted to the subscription's own key (RFC 8291,
// aes128gcm) and the request signed with the server's VAPID key (RFC 8292). The
// push service, which Google, Mozilla or Apple runs for the browser, relays a body
// it cannot read: no third party holds message text.
//
// It is mechanism only. What to say and to whom is the caller's policy; this package
// knows no session, tenant or user. It uses the standard library, go-jose for the
// ES256 token, and core/egress for the one thing that is not a formality here: the
// endpoint is chosen by whoever registers a subscription, so it is an address the
// engine will connect to on a stranger's word. Send therefore accepts only https on
// port 443, resolves the name once, refuses every reserved address and dials the
// addresses it checked, never a second lookup.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/olivaresai/olivares/core/egress"
)

const (
	// MaxPayload is the largest message Encrypt takes. A push service accepts a 4096
	// byte body (RFC 8030 section 7.2); the header, the delimiter and the tag take the rest.
	MaxPayload = 3000

	// recordSize is the one record a message travels in (RFC 8188). It is the size
	// the encryption header declares, not the size of this body.
	recordSize = 4096

	// Subject is the contact claim of the VAPID token (RFC 8292 section 2.1). The
	// push service uses it to reach the operator of an abusive sender, so it names the
	// product, the one party every deployment shares.
	Subject = "https://olivares.ai"

	pushPort      = 443
	maxEndpoint   = 2048
	tokenLifetime = 12 * time.Hour
	requestBudget = 15 * time.Second
	dialBudget    = 5 * time.Second
)

var (
	// ErrGone reports a subscription the push service no longer knows (404 or 410): the
	// user agent dropped it. The caller deletes it; retrying can never succeed.
	ErrGone = errors.New("webpush: the subscription is gone")
	// ErrPayloadTooLarge reports a message over MaxPayload.
	ErrPayloadTooLarge = errors.New("webpush: the message is too large")
	// ErrEndpoint reports an endpoint this package will not connect to.
	ErrEndpoint = errors.New("webpush: the endpoint is not an acceptable push service address")
)

// Subscription is what a browser's PushManager.subscribe returns: where to post, and
// the two values the body is encrypted with, both base64url as the browser hands them over.
type Subscription struct {
	Endpoint string
	// P256dh is the user agent's P-256 public key (65 bytes, uncompressed).
	P256dh string
	// Auth is the user agent's 16-byte authentication secret.
	Auth string
}

// Urgency is the RFC 8030 section 5.3 hint a push service uses to decide whether to
// wake a sleeping device.
type Urgency string

const (
	UrgencyNormal Urgency = "normal"
	UrgencyHigh   Urgency = "high"
)

// Options are the delivery hints of one message.
type Options struct {
	// TTL is how long the push service keeps the message for an offline device. Zero
	// asks for delivery now or never.
	TTL     time.Duration
	Urgency Urgency
}

// keys decodes the two values a body is encrypted with.
func (s Subscription) keys() (raw []byte, pub *ecdh.PublicKey, auth []byte, err error) {
	raw, err = base64.RawURLEncoding.DecodeString(s.P256dh)
	if err != nil {
		return nil, nil, nil, errors.New("webpush: the subscription key is not base64url")
	}
	pub, err = ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, nil, nil, errors.New("webpush: the subscription key is not a P-256 point")
	}
	auth, err = base64.RawURLEncoding.DecodeString(s.Auth)
	if err != nil || len(auth) != 16 {
		return nil, nil, nil, errors.New("webpush: the subscription auth secret is not 16 bytes of base64url")
	}
	return raw, pub, auth, nil
}

// Validate reports whether Send could deliver to this subscription: an endpoint it
// will connect to and keys a message can be encrypted to.
func (s Subscription) Validate() error {
	if err := ValidateEndpoint(s.Endpoint); err != nil {
		return err
	}
	_, _, _, err := s.keys()
	return err
}

// ValidateEndpoint reports whether Send would connect to this endpoint's host and port.
// It is the registration-time half of the check Send repeats where the bytes leave: it
// cannot see what a name resolves to, so Send alone refuses an internal address.
func ValidateEndpoint(raw string) error {
	if raw == "" || len(raw) > maxEndpoint {
		return fmt.Errorf("%w: length", ErrEndpoint)
	}
	dest, err := egress.ParseDestination(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEndpoint, err)
	}
	if dest.Scheme != "https" || dest.Port != pushPort {
		return fmt.Errorf("%w: only https on port %d", ErrEndpoint, pushPort)
	}
	if dest.IP != nil {
		return fmt.Errorf("%w: a push service is addressed by name", ErrEndpoint)
	}
	return nil
}

// KeyFromSeed derives the server's VAPID key from 32 or more bytes of engine-held key
// material, so there is no second key file to back up and every node holding the same
// seed signs as the same application server. The derivation is domain separated: the
// seed's other uses cannot be turned into this key or the reverse.
func KeyFromSeed(seed []byte) (*ecdsa.PrivateKey, error) {
	if len(seed) < 32 {
		return nil, errors.New("webpush: the seed is shorter than 32 bytes")
	}
	// A 32-byte string is a valid P-256 scalar unless it is zero or at least the group
	// order, about one in 2^32: the next counter value gives another.
	for i := 0; i < 8; i++ {
		raw, err := hkdf.Key(sha256.New, seed, nil, "olivares webpush vapid v1/"+strconv.Itoa(i), 32)
		if err != nil {
			return nil, err
		}
		if key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw); err == nil {
			return key, nil
		}
	}
	return nil, errors.New("webpush: no valid key for this seed")
}

// PublicKey is the application server key the browser subscribes with, in the base64url
// form PushManager.subscribe and the VAPID header both take.
func PublicKey(key *ecdsa.PrivateKey) string {
	b, err := key.PublicKey.Bytes()
	if err != nil {
		return "" // unreachable for a key this package made; an empty key fails every subscribe loudly
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Encrypt returns the request body for one message: the aes128gcm content coding of
// RFC 8188 with the key agreement of RFC 8291.
func Encrypt(sub Subscription, payload []byte) ([]byte, error) {
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: key agreement: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("webpush: salt: %w", err)
	}
	return encrypt(sub, payload, asPriv, salt)
}

// encrypt is Encrypt with the application server key and salt supplied, so the RFC's
// worked example can pin the output byte for byte.
func encrypt(sub Subscription, payload []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, ErrPayloadTooLarge
	}
	uaRaw, uaPub, auth, err := sub.keys()
	if err != nil {
		return nil, err
	}
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: key agreement: %w", err)
	}
	asPub := asPriv.PublicKey().Bytes()

	prkKey, err := hkdf.Extract(sha256.New, secret, auth)
	if err != nil {
		return nil, err
	}
	keyInfo := bytes.Join([][]byte{[]byte("WebPush: info\x00"), uaRaw, asPub}, nil)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// salt, record size, key id length, key id (the application server's public key),
	// then the one and last record: the message and its 0x02 delimiter.
	body := make([]byte, 0, 21+len(asPub)+len(payload)+1+gcm.Overhead())
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, recordSize)
	body = append(body, byte(len(asPub)))
	body = append(body, asPub...)
	return gcm.Seal(body, nonce, append(append([]byte(nil), payload...), 0x02), nil), nil
}

// vapidHeader is the Authorization value of RFC 8292: an ES256 token over the push
// service's origin, and the key that verifies it.
func vapidHeader(key *ecdsa.PrivateKey, subject, endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Audience: jwt.Audience{u.Scheme + "://" + u.Host},
		Subject:  subject,
		Expiry:   jwt.NewNumericDate(now.Add(tokenLifetime)),
	}).Serialize()
	if err != nil {
		return "", err
	}
	return "vapid t=" + token + ", k=" + PublicKey(key), nil
}

// Sender posts messages to push services as one application server.
type Sender struct {
	key *ecdsa.PrivateKey

	// The fields below are the production defaults; a test in this package replaces them
	// to reach a loopback server, and nothing else does.
	tls      *tls.Config
	reserved func(net.IP) bool
	anyPort  bool
	resolver egress.Resolver
	now      func() time.Time
}

// NewSender returns a Sender that signs with key.
func NewSender(key *ecdsa.PrivateKey) *Sender {
	return &Sender{key: key, reserved: egress.ReservedAddress, resolver: egress.NetResolver{}, now: time.Now}
}

// PublicKey is the application server key a browser must subscribe with.
func (s *Sender) PublicKey() string { return PublicKey(s.key) }

// Send encrypts payload to sub and posts it once. A nil error means the push service
// accepted the message for delivery (RFC 8030 section 5: 201), not that a device showed
// it. Errors never carry the endpoint: it is a capability, whoever holds it can push.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opts Options) error {
	if !s.anyPort {
		if err := ValidateEndpoint(sub.Endpoint); err != nil {
			return err
		}
	}
	dest, err := egress.ParseDestination(sub.Endpoint)
	if err != nil || dest.Scheme != "https" {
		return ErrEndpoint
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	ips, err := egress.Resolve(ctx, s.resolver, dest)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("webpush: %s did not resolve", dest)
	}
	for _, ip := range ips {
		if s.reserved(ip) {
			return fmt.Errorf("%w: %s resolves to a reserved address", ErrEndpoint, dest)
		}
	}
	token, err := vapidHeader(s.key, Subject, sub.Endpoint, s.now())
	if err != nil {
		return fmt.Errorf("webpush: sign: %w", err)
	}

	ctx, cancel := context.WithTimeout(egress.WithPin(ctx, ips), requestBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrEndpoint
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.FormatInt(int64(opts.TTL/time.Second), 10))
	if opts.Urgency != "" {
		req.Header.Set("Urgency", string(opts.Urgency))
	}

	// One request, no proxy and no redirect: a proxy would dial for us and a redirect
	// would leave the addresses that were checked.
	dialer := &net.Dialer{Timeout: dialBudget}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return egress.DialPinned(ctx, dialer, network, addr)
			},
			TLSClientConfig:   s.tls,
			ForceAttemptHTTP2: true,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webpush: %s unreachable: %w", dest, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return fmt.Errorf("%w: %s answered %d", ErrGone, dest, resp.StatusCode)
	default:
		return fmt.Errorf("webpush: %s answered %d", dest, resp.StatusCode)
	}
}
