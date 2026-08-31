// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// FileTokenConfig configures a FileTokenSource: a short-lived credential source that
// reads the CURRENT short-lived token from a file at the MOMENT of each call. The
// file is written and rotated by an EXTERNAL attester the operator already runs (a
// Vault Agent, a SPIFFE helper, a cloud workload-identity sidecar). This is the
// usable, deny-closed source and the compatibility path; the LIVE in-process WIF
// exchange (SPIRE JWT-SVID → claude-wif sk-ant-oat, removing the sidecar) is wired behind
// the same CredentialSource seam by the broker in cmd/olivares (credential kind "wif").
//
// It honors the doctrine: short-lived (the read token's lifetime is the refresher's,
// asserted via TTL), environment-scoped (the path template selects per-environment /
// per-mode files), attested (by the external refresher), and NEVER persisted by us
// (read per call, used, discarded — never stored, never logged). Deny-closed: a
// missing/empty token file fails the mint, never a default key.
type FileTokenConfig struct {
	// PathTemplate is the token file path read at mint time. It may contain the
	// placeholders {env}, {mode}, {runtime} and {target} so an external refresher can
	// write narrowly-scoped, per-environment tokens (least privilege).
	PathTemplate string
	// TTL is the asserted lifetime of a freshly-read token (match it to the external
	// refresher's rotation cadence). Default 15m. A token older than this is treated
	// as expired by the executor (fail-closed).
	TTL time.Duration
	// Scheme is a non-sensitive label for audit context (e.g. "vault-agent",
	// "spiffe-helper", "wif").
	Scheme string
}

// fileTokenSource implements CredentialSource over a rotated token file.
type fileTokenSource struct {
	cfg FileTokenConfig
}

// NewFileTokenSource builds a deny-closed file-backed short-lived credential source.
// An empty PathTemplate yields the DenyCredentialSource (no source = fail-closed).
func NewFileTokenSource(cfg FileTokenConfig) CredentialSource {
	if strings.TrimSpace(cfg.PathTemplate) == "" {
		return DenyCredentialSource{}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Minute
	}
	if cfg.Scheme == "" {
		cfg.Scheme = "file-token"
	}
	return &fileTokenSource{cfg: cfg}
}

// Mint reads the current short-lived token from the per-(env,mode,runtime) file. A
// missing or empty file is a deny (fail-closed) — never a default credential.
func (s *fileTokenSource) Mint(_ context.Context, req MintRequest) (Credential, error) {
	path := expandTokenPath(s.cfg.PathTemplate, req)
	raw, err := os.ReadFile(path) //nolint:gosec // operator-provisioned, rotated token path
	if err != nil {
		return Credential{}, fmt.Errorf("%w: no short-lived token at the configured path for env=%q mode=%s", ErrNoCredentialSource, req.Environment, req.Mode)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return Credential{}, fmt.Errorf("%w: short-lived token file is empty", ErrNoCredentialSource)
	}
	return Credential{
		ID:       s.cfg.Scheme + ":" + req.Environment + ":" + req.Mode.String() + ":" + fingerprint(tok),
		Token:    tok,
		NotAfter: nowFunc().Add(s.cfg.TTL),
		Scheme:   s.cfg.Scheme,
	}, nil
}

// expandTokenPath substitutes the scoping placeholders in a token path template.
func expandTokenPath(tmpl string, req MintRequest) string {
	r := strings.NewReplacer(
		"{env}", safePathPart(req.Environment),
		"{mode}", req.Mode.String(),
		"{runtime}", safePathPart(req.Runtime),
		"{target}", safePathPart(req.Target),
	)
	return r.Replace(tmpl)
}

// safePathPart strips path separators / traversal from a placeholder value so a
// crafted environment/target cannot escape the configured token directory.
func safePathPart(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	return s
}

// fingerprint is a short, non-reversible SHA-256 prefix of a token, used ONLY as a
// non-sensitive audit correlation id (never the token itself).
func fingerprint(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])[:12]
}
