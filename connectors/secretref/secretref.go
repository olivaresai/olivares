// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package secretref holds the EXTERNAL secret-backend readers behind the engine's
// runtime secret resolver: a `vault:` / `aws-secretsmanager:` /
// `gcp-secretmanager:` / `azure-keyvault:` / `infisical:` / `k8s-secret:`
// reference in a connector config is resolved to the live secret VALUE through
// one of these readers, so the literal secret never has to live by value in the
// operator's config file.
//
// Unlike the secret-store connectors (awskms/azurekeyvault/externalsecrets,
// which OBSERVE topology and NEVER read a secret value), these readers exist to
// read the value — that is the whole point of resolution. They are still
// read-first (GET / GetSecretValue only, never a mutation) and minimal: each is a
// thin authenticated GET reusing the shared httpx client (and awssig for the one
// SigV4 POST), holds the operator credential in memory only, never logs the value
// and never places it in a URL.
//
// Each backend is configured ONCE at the ENGINE level from the environment (so a
// per-connector config carries only the reference, not the backend's own
// credentials) — the same model as the CMEK custody config (custody.go).
// A backend whose minimal config is absent is simply NOT wired: a reference to it
// then fails closed at the resolver ("recognized but not available"), never a
// boot failure and never a silent pass-through of the reference as a value.
//
// It imports only the SDK boundary's connectors/internal helpers and the standard
// library — never /core — so the Apache license boundary stays clean. The
// composition root (cmd/olivares, AGPL) adapts each Reader to the core resolver's
// handler interface (the method shape matches, so satisfaction is structural).
package secretref

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// Reader resolves a backend locator to the live secret value. It matches the
// shape the core secret resolver's Handler expects (structural satisfaction at
// the composition root). Implementations are read-only and fail closed.
type Reader interface {
	Resolve(ctx context.Context, locator string) ([]byte, error)
}

// Scheme names (kept in sync with core/secret's grammar). Defined here too so the
// package is self-contained and the composition root can map scheme→Reader.
const (
	SchemeVault             = "vault"
	SchemeAWSSecretsManager = "aws-secretsmanager"
	SchemeGCPSecretManager  = "gcp-secretmanager"
	SchemeAzureKeyVault     = "azure-keyvault"
	SchemeInfisical         = "infisical"
	SchemeK8sSecret         = "k8s-secret"
)

// defaultTimeout bounds a single backend round-trip.
const defaultTimeout = 30 * time.Second

// Handlers builds every backend whose minimal environment configuration is
// present, keyed by scheme. doer is the shared HTTP transport (nil = a hardened
// default; tests inject a fixture); the k8s reader builds its own CA-pinned
// transport when no doer is injected. A backend with absent config is omitted
// (logged at info) — a reference to it fails closed at the resolver. This never
// returns an error: one backend's misconfiguration must not abort the engine.
func Handlers(getenv func(string) string, doer httpx.Doer, log *slog.Logger) map[string]Reader {
	if getenv == nil {
		getenv = os.Getenv
	}
	if log == nil {
		log = slog.Default()
	}
	out := map[string]Reader{}
	type builder struct {
		scheme string
		build  func(func(string) string, httpx.Doer) (Reader, bool)
	}
	for _, b := range []builder{
		{SchemeVault, newVaultReader},
		{SchemeAWSSecretsManager, newAWSReader},
		{SchemeGCPSecretManager, newGCPReader},
		{SchemeAzureKeyVault, newAzureReader},
		{SchemeInfisical, newInfisicalReader},
		{SchemeK8sSecret, newK8sReader},
	} {
		r, ok := b.build(getenv, doer)
		if !ok {
			log.Info("secretref: backend not configured; references to it fail closed until configured", "scheme", b.scheme)
			continue
		}
		out[b.scheme] = r
		log.Info("secretref: backend wired", "scheme", b.scheme)
	}
	return out
}

// --- shared helpers ----------------------------------------------------------

// envToken is a bearer credential read from the environment: a static value, or a
// file re-read per call so an operator's refresher can rotate it without a
// restart (the custody TokenSource model). It is held only transiently.
type envToken struct {
	static string
	file   string
}

// loadEnvToken reads <prefix> (static) and <prefix>_FILE (path). ok=false when
// neither is set.
func loadEnvToken(getenv func(string) string, prefix string) (envToken, bool) {
	static := strings.TrimSpace(getenv(prefix))
	file := strings.TrimSpace(getenv(prefix + "_FILE"))
	if static == "" && file == "" {
		return envToken{}, false
	}
	return envToken{static: static, file: file}, true
}

// value resolves the current token: the file (re-read, trimmed) wins so a
// refreshed token is picked up; otherwise the static value.
func (t envToken) value() (string, error) {
	if t.file != "" {
		b, err := os.ReadFile(t.file) //nolint:gosec // operator-configured token path
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return t.static, nil
}

// defaultClient returns the doer to use: the injected one (tests), or a hardened
// http.Client with a bounded timeout.
func defaultClient(doer httpx.Doer) httpx.Doer {
	if doer != nil {
		return doer
	}
	return &http.Client{Timeout: defaultTimeout}
}

// firstEnv returns the first non-empty environment value among keys.
func firstEnv(getenv func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
