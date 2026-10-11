// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/fips140"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/secure"
)

func newKeysCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "keys",
		Short: "Inspect signing-key custody",
		Long: "Inspect the local signing-key custody configuration and envelope metadata.\n" +
			"Use status to report declared and configured custody, KEK details and FIPS mode.",
		Example: "  olivares keys status",
	}
	root.AddCommand(keysStatusCmd())
	addCMEKCommands(root)
	return root
}
func configuredKEKDescription(prefix string) string {
	switch kind := strings.TrimSpace(envconfig.Get(prefix)); kind {
	case "aws-kms":
		return kind + " " + envconfig.Get(prefix+"_AWS_KEY_ID")
	case "gcp-kms":
		return kind + " " + envconfig.Get(prefix+"_GCP_KEY")
	case "azure-kv":
		id := strings.TrimSuffix(envconfig.Get(prefix+"_AZURE_VAULT_URL"), "/") + "/keys/" + envconfig.Get(prefix+"_AZURE_KEY_NAME")
		if version := envconfig.Get(prefix + "_AZURE_KEY_VERSION"); version != "" {
			id += "/" + version
		}
		return kind + " " + id
	case "":
		return "none"
	default:
		return kind
	}
}

// envelopeSlot is one custody slot the posture report covers: the report name,
// the path it resolves to, and the purpose an envelope in that slot MUST hold.
// Carrying the expected purpose here is what lets verification catch a custody
// SUBSTITUTION — a catalog-key envelope dropped at the audit path — which a
// report that merely echoes the file's own `purpose` field can never see.
type envelopeSlot struct {
	name, path, purpose string
}

// provenance wording, kept in one place because it is the whole point of the
// report: a reader must be able to tell a field that was PROVEN from a field
// that was merely parsed out of an attacker-writable file.
const (
	provenanceUnverified = "parsed from the file, NOT proven. public_key and prior_public_keys are " +
		"bound into the envelope's AEAD, but this command makes no KMS call by default, so nothing " +
		"here has been authenticated. Re-run with --verify-envelopes to open the envelope under the " +
		"configured KEK before pinning any of these keys with `audit verify --event-pubkey`."
	provenanceVerified = "PROVEN: the envelope opened under the configured KEK, so its purpose, " +
		"public_key and prior_public_keys are authenticated by the AEAD binding and are safe to pin " +
		"with `audit verify --event-pubkey`."
	provenanceFailed = "REFUSED: this envelope did NOT authenticate under the configured KEK. Do not " +
		"pin anything printed here. The fields below are what the FILE claims, which is exactly what " +
		"is in question."
)

func keysStatusCmd() *cobra.Command {
	var auditEnv, catalogEnv, policyEnv string
	var verifyEnvelopes bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the key-custody posture (declared vs configured, envelopes, FIPS mode)",
		Long: "status reports declared and configured key custody, KEK details, signing-envelope metadata,\n" +
			"prior verification keys and this binary's FIPS mode.\n\n" +
			"By default it reads envelope files and makes NO KMS call, which is deliberate: this is the\n" +
			"command an operator runs to diagnose a REVOKED KEK, so it has to keep working when the KEK\n" +
			"refuses every call. The cost is that everything it prints about an envelope is unproven —\n" +
			"including prior_public_keys, the rotation history an external auditor pins per generation.\n" +
			"An attacker who can write the envelope file but does not hold the KEK cannot make such an\n" +
			"edit survive an open, but they CAN make it appear here. Every envelope therefore reports an\n" +
			"`authenticated` field, and --verify-envelopes opens each one under the configured KEK so the\n" +
			"answer is yes. Pin from a verified report, never from an unverified one.",
		Example: "  olivares keys status --audit-envelope /var/lib/olivares/audit-signing.key.sealed\n" +
			"  olivares keys status --verify-envelopes",
		Args: cobra.NoArgs,
		// A failed envelope authentication is not a usage mistake, and printing the
		// flag list under it would bury the one line that matters.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := map[string]any{}

			assertions, err := loadCustodyAssertions()
			if err != nil {
				return err
			}
			out["declared"] = map[string]string{
				"key_custody":    orDash(assertions.auditKey),
				"ledger_custody": orDash(assertions.ledger),
			}
			description, err := describeConfiguredKEK(envKeyWrap)
			if err != nil {
				return err
			}
			out["kek"] = description
			// A declared migration source is posture: it changes which KEK every
			// ceremony OPENS with, and a variable left behind after a migration is
			// the thing an operator will be staring at when ordinary ceremonies start
			// refusing. Reported, never used here — status makes no KMS call.
			oldDescription, err := describeConfiguredKEK(envKeyWrapOld)
			if err != nil {
				return err
			}
			out["kek_migration_source"] = oldDescription

			out["ledger_signer"] = orDash(strings.TrimSpace(envconfig.Get("OLIVARES_LEDGER_SIGNER")))
			// FIPS 140-3 mode of THIS binary (honest wording: mode, not validation —
			// docs/SCP-09-FIPS-STIG.md).
			out["fips140"] = map[string]any{"enabled": fips140.Enabled(), "module": fips140.Version()}

			if verifyEnvelopes && strings.TrimSpace(envconfig.Get(envKeyWrap)) == "" {
				return fmt.Errorf("--verify-envelopes needs the KEK that sealed the envelopes: set %s (and its OLIVARES_KEY_WRAP_* backend settings). Without it nothing here can be proven, which is what the unverified report already says", envKeyWrap)
			}

			// A FIXED slot order, each with the purpose it must hold. The policy
			// signing key is a fail-closed CMEK custody source like the other two
			// (auditkey.go), and the command whose job is to report custody posture
			// used to omit it entirely.
			slots := []envelopeSlot{
				{"audit", firstNonEmpty(auditEnv, envconfig.Get(envAuditWrapped)), secure.PurposeAuditSigningKey},
				{"catalog", firstNonEmpty(catalogEnv, envconfig.Get(envCatalogWrapped)), secure.PurposeCatalogSigningKey},
				{"policy", firstNonEmpty(policyEnv, envconfig.Get(envPolicyWrapped)), secure.PurposePolicySigningKey},
			}
			envelopes := map[string]any{}
			// The FIRST authentication failure, returned after the report is
			// rendered: an operator needs to SEE what was planted before the process
			// exits, and an auditor who asked for proof must not get exit 0 without it.
			var verifyErr error
			for _, s := range slots {
				if strings.TrimSpace(s.path) == "" {
					continue
				}
				e, rerr := secure.ReadSealedFile(s.path)
				if rerr != nil {
					envelopes[s.name] = map[string]string{"path": s.path, "error": rerr.Error()}
					if verifyEnvelopes && verifyErr == nil {
						verifyErr = fmt.Errorf("%s envelope %s could not be read: %w", s.name, s.path, rerr)
					}
					continue
				}
				priors := make([]string, 0, len(e.PriorPublicKeys))
				for _, p := range e.PriorPublicKeys {
					priors = append(priors, base64.StdEncoding.EncodeToString(p))
				}
				rec := map[string]any{
					"path": s.path, "purpose": e.Purpose, "provider": e.Provider, "kek": e.KeyID,
					"created_at": e.CreatedAt, "public_key": base64.StdEncoding.EncodeToString(e.PublicKey),
					// The prior generations' verification keys, oldest first — what an
					// external auditor pins per generation (audit verify --event-pubkey).
					// UNPROVEN unless `authenticated` below says otherwise: these bytes
					// come out of the decoded JSON, and the AEAD binding that makes them
					// tamper-evident only pays out on a path that OPENS the envelope.
					"prior_public_keys": priors,
					"authenticated":     false,
					"provenance":        provenanceUnverified,
				}
				if verifyEnvelopes {
					// The purpose passed here is the SLOT's, never the file's own claim:
					// that is what makes a substituted envelope fail instead of being
					// echoed back as whatever it says it is.
					if oerr := verifyCMEKEnvelope(cmd.Context(), e, s.purpose); oerr != nil {
						rec["provenance"] = provenanceFailed
						rec["error"] = oerr.Error()
						if verifyErr == nil {
							verifyErr = fmt.Errorf("%s envelope %s did not authenticate: %w", s.name, s.path, oerr)
						}
					} else {
						rec["authenticated"] = true
						rec["provenance"] = provenanceVerified
					}
				}
				envelopes[s.name] = rec
			}
			out["envelopes"] = envelopes
			out["envelopes_verified"] = verifyEnvelopes

			// E2: honor -o. This printed JSON whatever the operator asked for.
			if rerr := renderReportOut(cmd, out); rerr != nil {
				return rerr
			}
			if verifyErr != nil {
				return exitcode.New(exitcode.Err, verifyErr)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&auditEnv, "audit-envelope", "", "audit key envelope path (default $"+envAuditWrapped+")")
	cmd.Flags().StringVar(&catalogEnv, "catalog-envelope", "", "catalog key envelope path (default $"+envCatalogWrapped+")")
	cmd.Flags().StringVar(&policyEnv, "policy-envelope", "", "policy key envelope path (default $"+envPolicyWrapped+")")
	cmd.Flags().BoolVar(&verifyEnvelopes, "verify-envelopes", false,
		"open each envelope under the configured KEK to PROVE its purpose, public key and rotation history are unedited (one KMS call per envelope; without it the report is parsed, not proven)")
	return cmd
}
