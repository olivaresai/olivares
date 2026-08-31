// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

const (
	dpopIATWindow    = 5 * time.Minute
	dpopReplayMaxJTI = 65536
	dpopNonceBucket  = 5 * time.Minute
)

var (
	errDPoPProof    = errors.New("mcp: rs: invalid dpop proof")
	errDPoPUseNonce = errors.New("mcp: rs: use dpop nonce")
)

type dpopProofClaims struct {
	JTI   string           `json:"jti"`
	HTM   string           `json:"htm"`
	HTU   string           `json:"htu"`
	IAT   *jwt.NumericDate `json:"iat"`
	ATH   string           `json:"ath"`
	Nonce string           `json:"nonce"`
}

func (rs *ResourceServer) validateDPoPProof(r *http.Request, accessToken, expectedJKT string) (string, error) {
	proofs := r.Header.Values("DPoP")
	if len(proofs) != 1 {
		return "", errDPoPProof
	}
	proof := strings.TrimSpace(proofs[0])
	if proof == "" || strings.Contains(proof, ",") {
		return "", errDPoPProof
	}
	parsed, err := jwt.ParseSigned(proof, mcpAllowedAlgs)
	if err != nil || len(parsed.Headers) != 1 {
		return "", errDPoPProof
	}
	hdr := parsed.Headers[0]
	if headerType(hdr) != "dpop+jwt" {
		return "", errDPoPProof
	}
	jwk := hdr.JSONWebKey
	if jwk == nil || !jwk.IsPublic() || !jwk.Valid() {
		return "", errDPoPProof
	}
	var claims dpopProofClaims
	if err := parsed.Claims(jwk, &claims); err != nil {
		return "", errDPoPProof
	}
	if strings.TrimSpace(claims.JTI) == "" {
		return "", errDPoPProof
	}
	if claims.HTM != r.Method {
		return "", errDPoPProof
	}
	if claims.HTU != dpopHTU(rs.resource, r.URL.Path) {
		return "", errDPoPProof
	}
	if claims.IAT == nil {
		return "", errDPoPProof
	}
	now := rs.clock()
	iat := claims.IAT.Time()
	if now.Sub(iat) > dpopIATWindow || iat.Sub(now) > dpopIATWindow {
		return "", errDPoPProof
	}
	if claims.ATH == "" || !secureEqualString(claims.ATH, accessTokenHash(accessToken)) {
		return "", errDPoPProof
	}
	if rs.requireDPoPNonce {
		if rs.dpopNonces == nil || !rs.dpopNonces.valid(claims.Nonce, now) {
			return "", errDPoPUseNonce
		}
	}
	jkt, err := jwkThumbprint(jwk)
	if err != nil {
		return "", errDPoPProof
	}
	if expectedJKT != "" && !secureEqualString(jkt, expectedJKT) {
		return "", errDPoPProof
	}
	if rs.dpopReplay == nil || !rs.dpopReplay.claim(claims.JTI, iat, now) {
		return "", errDPoPProof
	}
	return jkt, nil
}

func dpopHTU(resource, requestPath string) string {
	u, err := url.Parse(resource)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return requestPath
	}
	path := requestPath
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path
}

func accessTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func jwkThumbprint(jwk *jose.JSONWebKey) (string, error) {
	sum, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

func mtlsCertificateThumbprint(r *http.Request) (string, bool) {
	if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", false
	}
	sum := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), true
}

func secureEqualString(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type dpopReplayCache struct {
	mu      sync.Mutex
	seen    map[[32]byte]time.Time
	maxSize int
}

func newDPoPReplayCache(maxSize int) *dpopReplayCache {
	if maxSize <= 0 {
		maxSize = dpopReplayMaxJTI
	}
	return &dpopReplayCache{seen: map[[32]byte]time.Time{}, maxSize: maxSize}
}

func (c *dpopReplayCache) claim(jti string, iat, now time.Time) bool {
	if c == nil {
		return false
	}
	key := sha256.Sum256([]byte(jti))
	exp := iat.Add(dpopIATWindow)
	if exp.Before(now) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpired(now)
	if _, ok := c.seen[key]; ok {
		return false
	}
	if len(c.seen) >= c.maxSize {
		return false
	}
	c.seen[key] = exp
	return true
}

func (c *dpopReplayCache) evictExpired(now time.Time) {
	for key, exp := range c.seen {
		if !now.Before(exp) {
			delete(c.seen, key)
		}
	}
}

type dpopNonceManager struct {
	key [32]byte
}

func newDPoPNonceManager() (*dpopNonceManager, error) {
	n := &dpopNonceManager{}
	if _, err := rand.Read(n.key[:]); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *dpopNonceManager) fresh(now time.Time) string {
	if n == nil {
		return ""
	}
	return n.nonceForBucket(now.Unix() / int64(dpopNonceBucket/time.Second))
}

func (n *dpopNonceManager) valid(nonce string, now time.Time) bool {
	if n == nil || strings.TrimSpace(nonce) == "" {
		return false
	}
	bucket := now.Unix() / int64(dpopNonceBucket/time.Second)
	return secureEqualString(nonce, n.nonceForBucket(bucket)) ||
		secureEqualString(nonce, n.nonceForBucket(bucket-1))
}

func (n *dpopNonceManager) nonceAt(t time.Time) string {
	if n == nil {
		return ""
	}
	return n.nonceForBucket(t.Unix() / int64(dpopNonceBucket/time.Second))
}

func (n *dpopNonceManager) nonceForBucket(bucket int64) string {
	mac := hmac.New(sha256.New, n.key[:])
	_, _ = mac.Write([]byte(strconv.FormatInt(bucket, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func mcpAllowedAlgNames() []string {
	out := make([]string, 0, len(mcpAllowedAlgs))
	for _, alg := range mcpAllowedAlgs {
		out = append(out, string(alg))
	}
	sort.Strings(out)
	return out
}

func mcpAllowedAlgList() string {
	return strings.Join(mcpAllowedAlgNames(), " ")
}
