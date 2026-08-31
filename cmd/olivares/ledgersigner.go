// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/audit/kmssign"
)

// buildCheckpointKey reads the OPTIONAL off-box ledger checkpoint signer from the
// environment (R5). It returns (nil, nil) when none is configured
// — the default on-box Ed25519 signer is preserved unchanged. When configured, the
// ledger's CHECKPOINTS (the cross-time tamper-evidence anchor) are signed by a
// KMS/HSM key that never lives on the host, raising integrity from the DB-only
// attacker to the host-compromised attacker (docs/SECURITY-HARDENING.md). Per-event signing stays
// on-box Ed25519 (the hot path is never routed off-box).
//
//	OLIVARES_LEDGER_SIGNER = "" (on-box, default) | "aws-kms" | "gcp-kms" | "azure-kv"
//
// aws-kms: OLIVARES_LEDGER_KMS_AWS_REGION, _AWS_KEY_ID, optional _AWS_SIGNING_ALG
//
//	(ECDSA_SHA_256 default). Credentials from the standard AWS_ACCESS_KEY_ID /
//	AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN env (IRSA/credential-process populate
//	these; IMDS auto-fetch is a documented seam).
//
// gcp-kms: OLIVARES_LEDGER_KMS_GCP_KEY (full cryptoKeyVersion resource name), and a
//
//	bearer token from OLIVARES_LEDGER_KMS_GCP_TOKEN_FILE (re-read each sign, so the
//	operator's refresher keeps it fresh) or OLIVARES_LEDGER_KMS_GCP_TOKEN (static).
//
// azure-kv: OLIVARES_LEDGER_KMS_AZURE_VAULT_URL, _AZURE_KEY_NAME, optional
//
//	_AZURE_KEY_VERSION, token from _AZURE_TOKEN_FILE or _AZURE_TOKEN.
//
// All backends are pure-Go REST (no cgo): wiring an off-box signer never breaks the
// static engine binary. A native PKCS#11/HSM lives out-of-process behind the same
// seam.
func buildCheckpointKey(log *slog.Logger) (audit.CheckpointKey, error) {
	kind := strings.TrimSpace(os.Getenv("OLIVARES_LEDGER_SIGNER"))
	switch kind {
	case "", "ed25519", "on-box":
		return nil, nil
	case "aws-kms":
		ck, err := kmssign.NewAWS(kmssign.AWSConfig{
			Region:           os.Getenv("OLIVARES_LEDGER_KMS_AWS_REGION"),
			KeyID:            os.Getenv("OLIVARES_LEDGER_KMS_AWS_KEY_ID"),
			SigningAlgorithm: os.Getenv("OLIVARES_LEDGER_KMS_AWS_SIGNING_ALG"),
			Creds: kmssign.AWSCreds{
				AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
				SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
				SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
			},
		})
		if err != nil {
			return nil, err
		}
		log.Info("ledger checkpoints signed OFF-BOX by AWS KMS", "key", ck.KeyID(), "alg", string(ck.Algorithm()))
		return ck, nil
	case "gcp-kms":
		ts, err := tokenSourceFromEnv("OLIVARES_LEDGER_KMS_GCP_TOKEN")
		if err != nil {
			return nil, err
		}
		ck, err := kmssign.NewGCP(kmssign.GCPConfig{
			KeyVersionName: os.Getenv("OLIVARES_LEDGER_KMS_GCP_KEY"),
			Token:          ts,
		})
		if err != nil {
			return nil, err
		}
		log.Info("ledger checkpoints signed OFF-BOX by Google Cloud KMS", "key", ck.KeyID(), "alg", string(ck.Algorithm()))
		return ck, nil
	case "azure-kv":
		ts, err := tokenSourceFromEnv("OLIVARES_LEDGER_KMS_AZURE_TOKEN")
		if err != nil {
			return nil, err
		}
		ck, err := kmssign.NewAzure(kmssign.AzureConfig{
			VaultURL:   os.Getenv("OLIVARES_LEDGER_KMS_AZURE_VAULT_URL"),
			KeyName:    os.Getenv("OLIVARES_LEDGER_KMS_AZURE_KEY_NAME"),
			KeyVersion: os.Getenv("OLIVARES_LEDGER_KMS_AZURE_KEY_VERSION"),
			Token:      ts,
		})
		if err != nil {
			return nil, err
		}
		log.Info("ledger checkpoints signed OFF-BOX by Azure Key Vault", "key", ck.KeyID(), "alg", string(ck.Algorithm()))
		return ck, nil
	default:
		return nil, fmt.Errorf("OLIVARES_LEDGER_SIGNER=%q unknown (use \"\"|aws-kms|gcp-kms|azure-kv)", kind)
	}
}

// tokenSourceFromEnv builds a bearer TokenSource from <prefix>_FILE (re-read each
// call so the operator's refresher keeps it fresh) or <prefix> (static). The file
// form is preferred for a long-lived engine because a static token expires.
func tokenSourceFromEnv(prefix string) (kmssign.TokenSource, error) {
	if path := strings.TrimSpace(os.Getenv(prefix + "_FILE")); path != "" {
		return func(context.Context) (string, error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read ledger token file: %w", err)
			}
			return strings.TrimSpace(string(b)), nil
		}, nil
	}
	if tok := strings.TrimSpace(os.Getenv(prefix)); tok != "" {
		return kmssign.StaticToken(tok), nil
	}
	// Named for the VARIABLES rather than for one of the callers: this helper also
	// serves the KEK custody namespaces, where telling an operator configuring a
	// key wrapper that their "off-box ledger signer" is misconfigured sent them
	// looking at the wrong subsystem entirely.
	return nil, fmt.Errorf("%s or %s_FILE is required: this backend authenticates with a bearer token", prefix, prefix)
}
