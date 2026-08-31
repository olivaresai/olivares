// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/fips140"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/secure"
)

// newKeysCmd is the key-custody ceremony CLI: sealing the engine's signing
// keys (and operator configs) under a customer-managed KMS KEK (CMEK), rotating
// the signing key with a verifiable history, and re-wrapping after a KEK
// rotation. Minting/sealing is DELIBERATELY a CLI ceremony and never a boot
// side effect: under declared custody an absent envelope refuses the boot — the
// operator runs these commands on purpose, with the KEK configured
// (OLIVARES_KEY_WRAP — see custody.go for the full env surface).
func newKeysCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "keys",
		Short: "Key custody (BYOK/HYOK/CMEK): seal, rotate and inspect signing keys",
		Long: "keys is the custody surface for the engine's signing material and for operator\n" +
			"config files that carry secrets: wrap a key into a KEK envelope, rotate to a\n" +
			"new key while keeping the prior public keys as verifiable history, re-seal an\n" +
			"envelope after the KEK itself rotates, and report the posture that results.\n\n" +
			"seal/unseal do the same for a config file. unseal writes to stdout only — it\n" +
			"never puts plaintext back on disk.",
		Example: "  olivares keys status --audit-envelope /var/lib/olivares/audit-signing.key.sealed\n" +
			"  olivares keys wrap --mint --purpose audit --out /var/lib/olivares/audit-signing.key.sealed\n" +
			"  olivares keys seal --in sources.json --out sources.json.sealed",
	}
	root.AddCommand(keysStatusCmd(), keysWrapCmd(), keysRotateCmd(), keysRewrapCmd(), keysSealCmd(), keysUnsealCmd())
	return root
}

// purposeFor maps the CLI --purpose flag to an envelope purpose.
func purposeFor(p string) (string, error) {
	switch p {
	case "audit":
		return secure.PurposeAuditSigningKey, nil
	case "catalog":
		return secure.PurposeCatalogSigningKey, nil
	case "policy":
		return secure.PurposePolicySigningKey, nil
	default:
		return "", fmt.Errorf("--purpose %q unknown (audit|catalog|policy)", p)
	}
}

// requireWrapper builds the configured KEK wrapper or explains exactly what is
// missing — every custody ceremony needs the customer's KEK reachable.
func requireWrapper() (secure.KeyWrapper, *keyWrapConfig, error) {
	cfg, err := loadKeyWrapConfig()
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("no KEK configured: set %s (and its OLIVARES_KEY_WRAP_* backend settings) — custody ceremonies need the customer-managed key", envKeyWrap)
	}
	w, err := cfg.wrapper()
	if err != nil {
		return nil, nil, err
	}
	return w, cfg, nil
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

// The custody ceremonies report their outcome as a TYPED value, not as the
// engine's own bytes. The house form for `-o json` is `json.RawMessage(raw)` —
// the engine's response verbatim, so a field the CLI does not model is still
// carried (observeJSON, cmd_observeplane.go:364-386) — and it does not apply
// here for a reason worth stating: these four leaves have NO engine behind them.
// A key-custody ceremony runs entirely in this process against a KMS, and the
// only value in scope is what this file computed. There are no upstream bytes to
// preserve, so a typed struct IS the whole truth, exactly as `secrets ls` uses
// secretListItem below and as the 40 other engine-less call sites do.
//
// ⛔ THE RULE THIS LOT TURNS ON, and it is not stylistic: EVERY FIELD BELOW IS A
// FIELD THE TEXT FORM ALREADY PRINTS. These commands handle sealed material —
// signing keys, KEK identities, envelope contents — and serializing one extra
// field into a machine-readable object is how a secret reaches a log pipeline
// that a prose line would never have entered. Two consequences that look like
// omissions and are not:
//
//   - keysRotateResult carries the COUNT of prior generations, never the list.
//     The text says "prior generations kept: 3"; the keys themselves are in
//     `keys status`, which is the command that reports them and that says, in its
//     own report, whether they were authenticated. Copying them here would carry
//     UNPROVEN key material into a new place without the provenance field that
//     makes it readable.
//   - keysWrapResult never carries the signing key. On the --mint path the
//     private half is discarded at the call (`_, env, err = Mint…`), but on the
//     --from path it IS a live local variable one line above the report, so the
//     absence is asserted by a witness rather than left to the reader.
type keysWrapResult struct {
	Purpose   string `json:"purpose"`
	Out       string `json:"out"`
	Provider  string `json:"provider"`
	KEK       string `json:"kek"`
	PublicKey string `json:"public_key"`
	// MigratedFrom is the plaintext key file --from read, and it appears only when
	// that flag was used — mirroring the NOTE line the text prints only then. It
	// is the PATH the operator supplied, never the key that was read from it.
	MigratedFrom string `json:"migrated_from,omitempty"`
}

type keysRotateResult struct {
	Out                  string `json:"out"`
	NewPublicKey         string `json:"new_public_key"`
	PriorGenerationsKept int    `json:"prior_generations_kept"`
}

type keysRewrapResult struct {
	In       string `json:"in"`
	Provider string `json:"provider"`
	KEK      string `json:"kek"`
	Out      string `json:"out"`
}

// keysSealResult reports the two paths and nothing else. The config being sealed
// is a file whose PLAINTEXT holds credentials — that is why the command exists —
// so neither its contents nor any digest of them belongs in the report.
type keysSealResult struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

// loadOldKeyWrapConfig parses the declared MIGRATION SOURCE identity, or
// (nil, nil) when no migration is declared. It exists only here, in the CLI: the
// boot path resolves custody through loadKeyWrapConfig alone, so a running engine
// keeps exactly one custody root no matter what is in the environment.
func loadOldKeyWrapConfig() (*keyWrapConfig, error) {
	return parseKeyWrapConfig(envKeyWrapOld)
}

// requireCeremonyWrappers resolves the TWO sides a custody ceremony needs.
//
// The seal side is always the configured KEK — a migration must never deposit
// future custody under anything but the identity the operator declared as
// current, so this side cannot come from the migration namespace at all. The open
// side is the declared migration source when there is one, and otherwise the same
// configured KEK, which is byte-for-byte the behavior before migration existed.
//
// Resolution is by DECLARATION and never by error: there is deliberately no
// "try the current KEK, and if it fails try the old one" anywhere. Error-driven
// fallback between custody identities is precisely the pattern that let a
// mis-pointed KEK be silently retried against a different key, and it is not
// coming back through the door marked migration. The consequence is accepted on
// purpose: a migration variable left declared after the ceremony makes ordinary
// ceremonies fail loudly until it is unset, which is the direction custody bugs
// should fail in.
func requireCeremonyWrappers() (newW secure.KeyWrapper, sealCfg, openCfg *keyWrapConfig, err error) {
	newW, sealCfg, err = requireWrapper()
	if err != nil {
		return nil, nil, nil, err
	}
	oldCfg, err := loadOldKeyWrapConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	if oldCfg == nil {
		return newW, sealCfg, sealCfg, nil
	}
	return newW, sealCfg, oldCfg, nil
}

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
			cfg, err := loadKeyWrapConfig()
			if err != nil {
				return err
			}
			if cfg == nil {
				out["kek"] = "none"
			} else {
				out["kek"] = cfg.describe()
			}
			// A declared migration source is posture: it changes which KEK every
			// ceremony OPENS with, and a variable left behind after a migration is
			// the thing an operator will be staring at when ordinary ceremonies start
			// refusing. Reported, never used here — status makes no KMS call.
			oldCfg, err := loadOldKeyWrapConfig()
			if err != nil {
				return err
			}
			if oldCfg == nil {
				out["kek_migration_source"] = "none"
			} else {
				out["kek_migration_source"] = oldCfg.describe()
			}
			out["ledger_signer"] = orDash(strings.TrimSpace(os.Getenv("OLIVARES_LEDGER_SIGNER")))
			// FIPS 140-3 mode of THIS binary (honest wording: mode, not validation —
			// docs/SCP-09-FIPS-STIG.md).
			out["fips140"] = map[string]any{"enabled": fips140.Enabled(), "module": fips140.Version()}

			if verifyEnvelopes && cfg == nil {
				return fmt.Errorf("--verify-envelopes needs the KEK that sealed the envelopes: set %s (and its OLIVARES_KEY_WRAP_* backend settings). Without it nothing here can be proven, which is what the unverified report already says", envKeyWrap)
			}

			// A FIXED slot order, each with the purpose it must hold. The policy
			// signing key is a fail-closed CMEK custody source like the other two
			// (auditkey.go), and the command whose job is to report custody posture
			// used to omit it entirely.
			slots := []envelopeSlot{
				{"audit", firstNonEmpty(auditEnv, os.Getenv(envAuditWrapped)), secure.PurposeAuditSigningKey},
				{"catalog", firstNonEmpty(catalogEnv, os.Getenv(envCatalogWrapped)), secure.PurposeCatalogSigningKey},
				{"policy", firstNonEmpty(policyEnv, os.Getenv(envPolicyWrapped)), secure.PurposePolicySigningKey},
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
					if _, oerr := openSealedEnvelope(cmd.Context(), cfg, e, s.purpose); oerr != nil {
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

func keysWrapCmd() *cobra.Command {
	var out, from, purpose string
	var mint bool
	cmd := &cobra.Command{
		Use:   "wrap",
		Short: "Seal a signing key into a CMEK envelope (mint a new key, or migrate an existing plaintext key file)",
		Long: "wrap creates a CMEK custody envelope by minting a fresh signing key or sealing an\n" +
			"existing plaintext key. It refuses to overwrite an existing envelope.",
		Example: "  olivares keys wrap --mint --purpose audit --out /var/lib/olivares/audit-signing.key.sealed",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mint == (from != "") {
				return fmt.Errorf("exactly one of --mint or --from <plain key file> is required")
			}
			// Never overwrite an existing envelope from `wrap`: a custody record
			// holds the ONLY at-rest copy of a signing key and its rotation
			// history — replacing it wholesale is what `keys rotate` (history
			// preserved) and `keys rewrap` (same key, new KEK) are for.
			if _, statErr := os.Stat(out); statErr == nil {
				return fmt.Errorf("%s already exists — refusing to overwrite a custody envelope (use `keys rotate` to rotate the key or `keys rewrap` after a KEK rotation; move the file aside if you really mean to replace it)", out)
			}
			p, err := purposeFor(purpose)
			if err != nil {
				return err
			}
			w, _, err := requireWrapper()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kmsCallTimeout)
			defer cancel()

			var env *secure.SealedEnvelope
			if mint {
				_, env, err = secure.MintSealedSigningKey(ctx, w, p, nil)
				if err != nil {
					return err
				}
			} else {
				b, rerr := os.ReadFile(from) //nolint:gosec // operator-provided key path
				if rerr != nil {
					return rerr
				}
				priv, derr := secure.DecodeSigningKey(string(b))
				if derr != nil {
					return derr
				}
				env, err = secure.SealSigningKey(ctx, w, p, priv, nil)
				if err != nil {
					return err
				}
			}
			if err := secure.WriteSealedFile(out, env); err != nil {
				return err
			}
			res := keysWrapResult{
				Purpose: p, Out: out, Provider: env.Provider, KEK: env.KeyID,
				PublicKey:    base64.StdEncoding.EncodeToString(env.PublicKey),
				MigratedFrom: from,
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, werr := fmt.Fprintf(w, "sealed %s envelope written to %s\n  kek:        %s %s\n  public key: %s\n",
					res.Purpose, res.Out, res.Provider, res.KEK, res.PublicKey); werr != nil {
					return werr
				}
				if res.MigratedFrom == "" {
					return nil
				}
				_, werr := fmt.Fprintf(w, "NOTE: the plaintext key file %s still exists — verify the envelope boots, then shred it; the sealed envelope is now the only at-rest copy you need.\n", res.MigratedFrom)
				return werr
			}, res)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "envelope output path (e.g. audit-signing.key.sealed)")
	cmd.Flags().StringVar(&from, "from", "", "existing plaintext key file to migrate (the base64 form in the data dir)")
	cmd.Flags().BoolVar(&mint, "mint", false, "mint a fresh key inside the ceremony (never persisted in clear)")
	cmd.Flags().StringVar(&purpose, "purpose", "audit", "key purpose: audit|catalog|policy")
	_ = cmd.RegisterFlagCompletionFunc("purpose", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"audit", "catalog", "policy"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func keysRotateCmd() *cobra.Command {
	var in, out string
	var yes bool
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Mint a NEW signing key sealed under the KEK, preserving the prior public keys as verifiable history",
		Long: "rotate mints a new signing key and writes a new envelope carrying the old envelope's\n" +
			"public key (and its priors) as rotation history. Run it with the engine STOPPED: events\n" +
			"after the restart are signed by the new key, and `audit verify` covers the whole chain\n" +
			"by pinning current + prior public keys (the envelope's prior_public_keys).\n\n" +
			"With --out, the superseded envelope at --in stays on disk — shred it once the new one\n" +
			"boots. IMPORTANT: `keys rotate` only mints the new sealed key and preserves the history;\n" +
			"it does NOT by itself FENCE the retired key. Complete the ceremony with `audit\n" +
			"key-transition` (engine STOPPED, off-box signer configured) to record the per-tenant\n" +
			"epoch boundary that revokes the retired key beyond its last-signed sequence — otherwise\n" +
			"a retired key + DB write can still re-sign tail events. Residual risk without\n" +
			"an off-box signer: the boundary cannot be sealed on-box, so fence externally with\n" +
			"`audit verify --event-pubkey <key>@<last_seq>`. See deploy/runbooks/key-rotation.md.\n\n" +
			"MIGRATING CUSTODY AND RETIRING THE KEY AT ONCE: declare the source identity in\n" +
			"OLIVARES_KEY_WRAP_OLD and the destination in OLIVARES_KEY_WRAP, and rotate opens the old\n" +
			"envelope with the source identity while sealing the new key under the destination. This is\n" +
			"the right ceremony when the reason for moving is a compromise. If you only want to change\n" +
			"custodian and keep the signing key — the usual cloud migration — use `keys rewrap`, which\n" +
			"leaves every pinned public key where it is. Fencing above is unchanged either way.",
		Example: "  olivares keys rotate --in /var/lib/olivares/audit-signing.key.sealed --out /var/lib/olivares/audit-signing.next.sealed",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// ⛔ SIN `--out` ESTE VERBO SOBRESCRIBE EL ENVELOPE EN SITIO, Y HASTA HOY NO
			// PREGUNTABA. El default esta en la ayuda del propio flag —«default: overwrite
			// --in atomically»— asi que no era un descuido: era una eleccion sin salvaguarda.
			//
			// Lo que se pierde es la UNICA copia de la clave sellada anterior, y el riesgo no
			// es teorico: si el envelope nuevo queda sellado bajo una KEK que no se puede
			// abrir, el motor no arranca y no hay a que volver. `docs/contracts-
			// custody.md:146` ya lo dice con esas palabras para `rewrap` — «se brickearia».
			//
			// La pregunta va ANTES de la llamada al KMS, no antes de la escritura: declinar
			// despues de acunar deja material de clave huerfano en el KMS por una respuesta
			// que el operador ya iba a dar. Con `--out` no se pregunta nada, porque no se
			// destruye nada — y esa es la mitad que mantiene barata la ruta segura.
			if out == "" {
				if err := confirmDestructive(cmd, yes, fmt.Sprintf(
					"overwrite the sealed envelope at %s in place, discarding the superseded one "+
						"(pass --out to write beside it instead)", in)); err != nil {
					return err
				}
			}
			newW, _, openCfg, err := requireCeremonyWrappers()
			if err != nil {
				return err
			}
			old, err := secure.ReadSealedFile(in)
			if err != nil {
				return err
			}
			// The OPEN side is pinned to what the old envelope recorded (an Azure
			// version, an AWS key ARN behind a repointed alias); the SEAL side is the
			// configured KEK. Rotation authenticates the old envelope before carrying
			// its history forward, so it needs a wrapper that can actually open it.
			// Under a declared migration openCfg is the SOURCE identity — the same
			// pinning rules, applied to the KEK that actually wrapped this envelope.
			oldW, err := openCfg.wrapperFor(old)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kmsCallTimeout)
			defer cancel()
			_, env, err := secure.RotateSealedSigningKey(ctx, oldW, newW, old)
			if err != nil {
				return err
			}
			if out == "" {
				out = in
			}
			if err := secure.WriteSealedFile(out, env); err != nil {
				return err
			}
			res := keysRotateResult{
				Out:          out,
				NewPublicKey: base64.StdEncoding.EncodeToString(env.PublicKey),
				// The count, deliberately, and not env.PriorPublicKeys — see the note
				// on keysRotateResult.
				PriorGenerationsKept: len(env.PriorPublicKeys),
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "rotated: new envelope written to %s\n  new public key: %s\n  prior generations kept: %d\n",
					res.Out, res.NewPublicKey, res.PriorGenerationsKept)
				return werr
			}, res)
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "current envelope path (its public key becomes rotation history)")
	cmd.Flags().BoolVar(&yes, "yes", false, "proceed without the in-place overwrite confirmation")
	cmd.Flags().StringVar(&out, "out", "", "new envelope path (default: overwrite --in atomically)")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

func keysRewrapCmd() *cobra.Command {
	var in, out string
	var yes bool
	cmd := &cobra.Command{
		Use:   "rewrap",
		Short: "Re-seal an envelope under the KEK's CURRENT version/primary (KEK rotation; the sealed key does not change)",
		Long: "rewrap opens the envelope (pinning the recorded KEK version where the provider needs it)\n" +
			"and seals it fresh under the configured KEK. Needed after an Azure Key Vault KEK rotation\n" +
			"(unwrap is version-pinned); AWS/GCP KEK rotation is transparent to existing envelopes.\n\n" +
			"MIGRATING CUSTODY TO ANOTHER KEK — including another PROVIDER — is this command too.\n" +
			"Declare the SOURCE identity in OLIVARES_KEY_WRAP_OLD (same shape as OLIVARES_KEY_WRAP:\n" +
			"OLIVARES_KEY_WRAP_OLD_AWS_REGION and so on) and the DESTINATION in OLIVARES_KEY_WRAP.\n" +
			"rewrap then opens with the old identity and seals under the new one, and the signing key,\n" +
			"its public half and its rotation history all survive unchanged — so nothing an auditor has\n" +
			"pinned moves. It covers same-provider moves as well: another AWS account or region,\n" +
			"another Key Vault, another GCP key.\n\n" +
			"This text used to send you to `keys rotate` under the new provider. That worked only\n" +
			"because rotate did not authenticate the old envelope at all: it copied the recorded public\n" +
			"key and history out of the file unopened, which let an edited history be sealed into a\n" +
			"fresh, valid envelope. Authenticating first is what made a second identity necessary, and\n" +
			"it still applies here — an envelope that cannot prove its own history is refused, not\n" +
			"migrated. Use `keys rotate` instead when the move should also RETIRE the signing key.\n\n" +
			"The declaration is deliberately sticky: while OLIVARES_KEY_WRAP_OLD is set, every ceremony\n" +
			"opens with it, so an ordinary rewrap against the current KEK will be refused until you\n" +
			"unset it. Unset it when the migration window closes.",
		Example: "  olivares keys rewrap --in /var/lib/olivares/audit-signing.key.sealed",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// ⛔ SIN `--out` ESTE VERBO SOBRESCRIBE EL ENVELOPE EN SITIO, Y HASTA HOY NO
			// PREGUNTABA. El default esta en la ayuda del propio flag —«default: overwrite
			// --in atomically»— asi que no era un descuido: era una eleccion sin salvaguarda.
			//
			// Lo que se pierde es la UNICA copia de la clave sellada anterior, y el riesgo no
			// es teorico: si el envelope nuevo queda sellado bajo una KEK que no se puede
			// abrir, el motor no arranca y no hay a que volver. `docs/contracts-
			// custody.md:146` ya lo dice con esas palabras para `rewrap` — «se brickearia».
			//
			// La pregunta va ANTES de la llamada al KMS, no antes de la escritura: declinar
			// despues de acunar deja material de clave huerfano en el KMS por una respuesta
			// que el operador ya iba a dar. Con `--out` no se pregunta nada, porque no se
			// destruye nada — y esa es la mitad que mantiene barata la ruta segura.
			if out == "" {
				if err := confirmDestructive(cmd, yes, fmt.Sprintf(
					"overwrite the sealed envelope at %s in place, discarding the superseded one "+
						"(pass --out to write beside it instead)", in)); err != nil {
					return err
				}
			}
			newW, _, openCfg, err := requireCeremonyWrappers()
			if err != nil {
				return err
			}
			e, err := secure.ReadSealedFile(in)
			if err != nil {
				return err
			}
			// Under a declared migration this is the SOURCE identity; otherwise it is
			// the configured KEK, exactly as before. Either way wrapperFor applies the
			// same recorded-over-configured pinning to it.
			oldW, err := openCfg.wrapperFor(e)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kmsCallTimeout)
			defer cancel()
			ne, err := secure.RewrapSealed(ctx, e, oldW, newW)
			if err != nil {
				return err
			}
			if out == "" {
				out = in
			}
			if err := secure.WriteSealedFile(out, ne); err != nil {
				return err
			}
			res := keysRewrapResult{In: in, Provider: ne.Provider, KEK: ne.KeyID, Out: out}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "rewrapped %s under %s %s -> %s\n", res.In, res.Provider, res.KEK, res.Out)
				return werr
			}, res)
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "envelope path to rewrap")
	cmd.Flags().BoolVar(&yes, "yes", false, "proceed without the in-place overwrite confirmation")
	cmd.Flags().StringVar(&out, "out", "", "output path (default: overwrite --in atomically)")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

func keysSealCmd() *cobra.Command {
	var in, out string
	cmd := &cobra.Command{
		Use:     "seal",
		Short:   "Seal an operator config file (its secrets at rest only exist KEK-wrapped)",
		Long:    "seal encrypts an operator configuration file into a KEK-wrapped envelope that the engine can open transparently at boot.",
		Example: "  olivares keys seal --in sources.json --out sources.json.sealed",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w, _, err := requireWrapper()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(in) //nolint:gosec // operator-provided config path
			if err != nil {
				return err
			}
			if secure.IsSealedEnvelope(b) {
				return fmt.Errorf("%s is already a sealed envelope", in)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kmsCallTimeout)
			defer cancel()
			env, err := secure.Seal(ctx, w, secure.PurposeOperatorConfig, b)
			if err != nil {
				return err
			}
			if err := secure.WriteSealedFile(out, env); err != nil {
				return err
			}
			res := keysSealResult{In: in, Out: out}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "sealed %s -> %s (point the OLIVARES_*_CONFIG env at the sealed file; the engine opens it transparently at boot)\n", res.In, res.Out)
				return werr
			}, res)
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "plaintext config file")
	cmd.Flags().StringVar(&out, "out", "", "sealed output path")
	_ = cmd.MarkFlagRequired("in")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func keysUnsealCmd() *cobra.Command {
	var in string
	cmd := &cobra.Command{
		Use:   "unseal",
		Short: "Open a sealed operator config to STDOUT (debugging; never writes plaintext to disk)",
		Long: "unseal decrypts an operator-config envelope and writes the plaintext only to stdout for\n" +
			"controlled debugging or piping. It honors OLIVARES_KEY_WRAP_OLD like the other ceremonies:\n" +
			"during a migration window, inspecting a config still sealed under the OLD identity is\n" +
			"exactly when you need this, and one rule — the migration identity opens, the configured KEK\n" +
			"seals — is easier to audit than a debug-shaped exception to it.\n\n" +
			"unseal has NO -o json form, and that is a decision rather than an omission: its\n" +
			"stdout IS the plaintext, byte for byte, so the caller can pipe it to jq or to the\n" +
			"tool that consumes that config. Wrapping those bytes in a JSON field would give a\n" +
			"machine-readable envelope to the one command in this group whose payload is already\n" +
			"the operator's own file — and would base64 or escape a config that the documented\n" +
			"`| jq .` idiom expects verbatim. Ask this command for JSON and it gives you the\n" +
			"config's own JSON, which is the right answer.",
		Example: "  olivares keys unseal --in sources.json.sealed | jq .",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _, openCfg, err := requireCeremonyWrappers()
			if err != nil {
				return err
			}
			e, err := secure.ReadSealedFile(in)
			if err != nil {
				return err
			}
			pt, err := openSealedEnvelope(cmd.Context(), openCfg, e, secure.PurposeOperatorConfig)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: plaintext on stdout — pipe it, don't redirect it to a world-readable file")
			_, werr := cmd.OutOrStdout().Write(pt)
			return werr
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "sealed config file")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}
