// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secret_test

import (
	"testing"

	"github.com/olivaresai/olivares/core/secret"
)

func TestIsCredentialBearingConfigKey(t *testing.T) {
	cases := map[string]bool{
		"api_key":                               true,
		"sasl-password":                         true,
		"client.secret":                         true,
		"secret_access_key":                     true,
		"credentials_json":                      true,
		"credential_ref":                        true,
		"authorization":                         true,
		"SIGNING_KEY":                           true,
		"SECRET_KEY":                            true,
		"PRIVATE_KEY":                           true,
		"ENCRYPTION_KEY":                        true,
		"DATA_ENCRYPTION_KEY":                   true,
		"CONTENT_ENCRYPTION_KEY":                true,
		"ENCRYPTION_MASTER_KEY":                 true,
		"KMS_MASTER_KEY":                        true,
		"MASTER_KEY":                            true,
		"ROOT_KEY":                              true,
		"SESSION_KEY":                           true,
		"WRAP_KEY":                              true,
		"DATA_KEY":                              true,
		"DEK":                                   true,
		"KEK":                                   true,
		"key":                                   true,
		"OLIVARES_AUDIT_SIGNING_KEY":            true,
		"OLIVARES_CATALOG_SIGNING_KEY":          true,
		"OLIVARES_POLICY_SIGNING_KEY":           true,
		"OLIVARES_EVENTING_SECRET_KEY":          true,
		"OLIVARES_SSO_SECRET_KEY":               true,
		"OLIVARES_THREATINTEL_SIGNING_KEY":      true,
		"OLIVARES_VECTOR_API_KEY":               true,
		"OLIVARES_CLAUDE_ADMIN_KEY":             true,
		"OLIVARES_CLAUDE_INFERENCE_KEY":         true,
		"OLIVARES_SECRET_STORE_KEY":             true,
		"OLIVARES_EMBEDDINGS_KEY":               true,
		"OLIVARES_EMBEDDINGS_VOYAGE_KEY":        true,
		"OLIVARES_EMBEDDINGS_OPENAI_KEY":        true,
		"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_KEY": true,
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY":   true,
		"token_url":                             false,
		"api_key_id":                            false,
		"key_id":                                false,
		"key_name":                              false,
		"partition_key":                         false,
		"sort_key":                              false,
		"hash_key":                              false,
		"range_key":                             false,
		"cache_key":                             false,
		"OLIVARES_KEY_WRAP_GCP_KEY":             false,
		"OLIVARES_LEDGER_KMS_GCP_KEY":           false,
		"OLIVARES_LICENSE_PUBKEY":               false,
		"OLIVARES_OTA_PUBKEY":                   false,
		"credentials_file":                      false,
		"secret_name":                           false,
		"max_tokens":                            false,
		"force_password_enabled":                false,
	}
	for key, want := range cases {
		if got := secret.IsCredentialBearingConfigKey(key); got != want {
			t.Errorf("IsCredentialBearingConfigKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestContainsInlineCredential(t *testing.T) {
	for _, value := range []string{
		"postgres://alice:AUDIT-password@db.internal/app",
		"db://svc:AUDIT-password@db.internal/app",
		"db://:AUDIT-password@db.internal/app",
		"db://:AKIA0000000000000000@db.internal/app",
		"https://api.internal/hook?token=AUDIT-token",
		"host=db.internal password=AUDIT-password",
	} {
		if !secret.ContainsInlineCredential(value) {
			t.Errorf("ContainsInlineCredential(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"https://api.internal/v1", "db://alice@db.internal/app", "store:source/prod/password",
		"token_url=https://idp.internal/token",
	} {
		if secret.ContainsInlineCredential(value) {
			t.Errorf("ContainsInlineCredential(%q) = true, want false", value)
		}
	}
}
