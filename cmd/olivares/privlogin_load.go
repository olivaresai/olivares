// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/x509"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
)

// Privileged-login configuration for the composition root.
//
// WebAuthn relying party — discrete env keys (FromEnv idiom):
//
//	OLIVARES_WEBAUTHN_RPID     e.g. "panel.example.com" (no scheme)
//	OLIVARES_WEBAUTHN_ORIGINS  comma-separated exact origins, e.g. "https://panel.example.com"
//	OLIVARES_WEBAUTHN_RP_NAME  display name (default "Olivares AI")
//
// Unset = the API derives RP id/origin per request from the proxy-aware
// external URL (single-node default); a PARTIAL config (id without origins or
// vice versa) is refused back to derivation, loudly — half a relying party
// pins nothing.
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

// loadWebAuthnRP reads the pinned relying party, or zero for per-request derivation.
func loadWebAuthnRP(getenv func(string) string, log *slog.Logger) auth.WebAuthnRP {
	id := strings.TrimSpace(getenv("OLIVARES_WEBAUTHN_RPID"))
	var origins []string
	for _, o := range strings.Split(getenv("OLIVARES_WEBAUTHN_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	if id == "" && len(origins) == 0 {
		return auth.WebAuthnRP{}
	}
	if id == "" || len(origins) == 0 {
		log.Warn("webauthn: partial relying-party config ignored; deriving per request (set both OLIVARES_WEBAUTHN_RPID and OLIVARES_WEBAUTHN_ORIGINS)")
		return auth.WebAuthnRP{}
	}
	name := strings.TrimSpace(getenv("OLIVARES_WEBAUTHN_RP_NAME"))
	if name == "" {
		name = "Olivares AI"
	}
	return auth.WebAuthnRP{ID: id, DisplayName: name, Origins: origins}
}

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
