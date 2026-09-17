// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/x509"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/olivaresai/olivares/core/auth"
)

// Privileged-login configuration for the composition root.
//
// WebAuthn relying party — discrete env keys (FromEnv idiom), resolved in
// publicaddr.go and no longer here:
//
//	OLIVARES_WEBAUTHN_RPID     e.g. "panel.example.com" (no scheme)
//	OLIVARES_WEBAUTHN_ORIGINS  comma-separated exact origins, e.g. "https://panel.example.com"
//	OLIVARES_WEBAUTHN_RP_NAME  display name (default "Olivares AI")
//
// Unset = the relying party comes from the declared console address, or is
// derived per request from the proxy-aware external URL when no address was
// declared. A PARTIAL pair — an ID with no origins, or the reverse — used to be
// dropped back to derivation with a warning; it is now a startup refusal, and so
// is a complete pair that cannot work. See resolveWebAuthnRP: an explicit
// authentication configuration is never replaced silently by a derived one.
//
// PIV/CAC — OLIVARES_PIV_CONFIG points at a JSON file:
//
//	{
//	  "client_ca_file": "/etc/olivares/piv-ca.pem",
//	  "cert_role_map": [{"subject_regexp": "OU=Agency", "role": "admin"}],
//	  "allow_ocsp_unknown": false
//	}
//
// Unlike the rate-limit overlay (which falls back to safe defaults), a broken
// PIV config yields NIL — the route stays 501 and elevation is impossible.
// Fail-closed here means NOT enabling a trust route from a config we could not
// fully validate.

// pivFile is the OLIVARES_PIV_CONFIG JSON shape.
type pivFile struct {
	ClientCAFile string `json:"client_ca_file"`
	CertRoleMap  []struct {
		SubjectRegexp string `json:"subject_regexp"`
		Role          string `json:"role"`
	} `json:"cert_role_map"`
	AllowOCSPUnknown bool `json:"allow_ocsp_unknown"`
}

// loadPIVConfig builds the PIV/CAC route config, or nil when unconfigured. Once the
// operator supplies a path, invalid configuration fails startup closed.
func loadPIVConfig(getenv func(string) string, log *slog.Logger) (*auth.PIVConfig, error) {
	path := getenv("OLIVARES_PIV_CONFIG")
	if path == "" {
		return nil, nil
	}
	var f pivFile
	if err := loadOperatorJSONConfig("OLIVARES_PIV_CONFIG", path, &f); err != nil {
		return nil, err
	}
	if f.ClientCAFile == "" {
		return nil, fmt.Errorf("OLIVARES_PIV_CONFIG=%q is missing client_ca_file", path)
	}
	pem, err := readOperatorConfig(f.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("OLIVARES_PIV_CONFIG=%q references unreadable client_ca_file %q: %w", path, f.ClientCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("OLIVARES_PIV_CONFIG=%q client_ca_file %q contains no usable CA certificate", path, f.ClientCAFile)
	}
	cfg := &auth.PIVConfig{Roots: pool, AllowOCSPUnknown: f.AllowOCSPUnknown}
	for _, m := range f.CertRoleMap {
		re, rerr := regexp.Compile(m.SubjectRegexp)
		if rerr != nil || m.Role == "" {
			return nil, fmt.Errorf("OLIVARES_PIV_CONFIG=%q contains invalid cert_role_map entry for subject_regexp %q", path, m.SubjectRegexp)
		}
		cfg.RoleMap = append(cfg.RoleMap, auth.PIVRoleRule{Subject: re, Role: m.Role})
	}
	if cfg.AllowOCSPUnknown {
		log.Warn("piv: allow_ocsp_unknown is ON — an unreachable OCSP responder no longer blocks elevation; lab use only")
	}
	log.Info("piv: client-certificate route enabled", "role_rules", len(cfg.RoleMap))
	return cfg, nil
}
