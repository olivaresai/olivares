// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secret

import "strings"

// IsCredentialBearingConfigKey reports whether a configuration field conventionally
// carries credential material rather than a public setting or credential handle.
// It is the descriptor-independent backstop for generic connector-config stores;
// descriptor-aware paths still enforce every ConfigField declared Secret.
func IsCredentialBearingConfigKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.NewReplacer("-", "_", ".", "_").Replace(k)

	// Credential identifiers that end in _id are still values on the AWS wire and
	// are declared Secret by the first-party cloud connectors. Explicit *_ref keys
	// are included because their value must satisfy the reference grammar, not carry
	// a pasted credential.
	switch k {
	case "access_key_id", "aws_access_key_id", "api_key", "apikey", "x_api_key",
		"admin_key", "analytics_key", "management_key", "routing_key", "dkim_key",
		"tls_cert", "tls_key", "client_key", "secret_access_key", "aws_secret_access_key",
		"signing_key", "secret_key", "private_key", "encryption_key", "data_encryption_key",
		"content_encryption_key", "encryption_master_key", "kms_master_key", "master_key",
		"root_key", "session_key", "wrap_key", "data_key", "dek", "kek", "key",
		"olivares_audit_signing_key", "olivares_catalog_signing_key", "olivares_policy_signing_key",
		"olivares_eventing_secret_key", "olivares_sso_secret_key", "olivares_threatintel_signing_key",
		"olivares_vector_api_key",
		"olivares_claude_admin_key", "olivares_claude_inference_key", "olivares_secret_store_key",
		"olivares_embeddings_key", "olivares_embeddings_voyage_key", "olivares_embeddings_openai_key",
		"olivares_embeddings_openai_compat_key", "olivares_embeddings_self_hosted_key",
		"credentials", "credential", "credentials_json", "credential_json", "credential_ref",
		"secret_ref", "api_key_ref", "token_ref", "auth_value", "authorization":
		return true
	}

	for _, suffix := range []string{
		"_file", "_path", "_ref", "_url", "_uri", "_endpoint", "_name", "_id",
		"_hint", "_type", "_count", "_limit", "_ttl", "_enabled",
	} {
		if strings.HasSuffix(k, suffix) {
			return false
		}
	}

	if k == "token" || k == "password" || k == "passwd" || k == "passphrase" || k == "secret" {
		return true
	}
	for _, suffix := range []string{
		"_token", "_password", "_passwd", "_passphrase", "_secret", "_private_key",
		"_secret_key", "_signing_key", "_api_key", "_access_key",
	} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// ContainsInlineCredential reports whether a nominally non-secret setting embeds a
// credential in a URL/DSN or common key=value form. It deliberately returns only a
// boolean so callers can reject without copying the value into an error or audit event.
// This is a storage-boundary backstop; descriptor-declared secret fields remain the
// authoritative control for ordinary literal values.
func ContainsInlineCredential(value string) bool {
	low := strings.ToLower(strings.TrimSpace(value))
	if low == "" {
		return false
	}
	// An authority URL with user:password material is never a safe handle, even
	// when its scheme is also a recognized secret-reference alias (for example,
	// db://user:password@host).
	if hasInlineAuthorityCredential(low) {
		return true
	}
	if IsReference(low) {
		return false
	}
	for _, marker := range []string{
		"token=", "secret=", "password=", "passwd=", "api_key=", "apikey=",
		"access_key=", "client_secret=",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// hasInlineAuthorityCredential reports a non-empty password component before
// the @ in a URL authority; the username may be empty. It intentionally performs
// no decoding and returns only a boolean so the credential is never copied into
// diagnostics.
func hasInlineAuthorityCredential(value string) bool {
	i := strings.Index(value, "://")
	if i < 0 {
		return false
	}
	authority := value[i+3:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return false
	}
	userinfo := authority[:at]
	colon := strings.IndexByte(userinfo, ':')
	return colon >= 0 && colon < len(userinfo)-1
}
