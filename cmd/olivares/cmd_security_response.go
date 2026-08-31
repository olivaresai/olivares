// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/threatfeed"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/secadvisory"
)

// cmd_security_response.go completes the `olivares security` group: the PSIRT
// PRODUCER for the advisory feed (`advisories`) and the hot-reload security rule-pack
// channel (`rulepack sign|verify`). The `check` consumer lives in cmd_security.go.
// Docs: docs/PSIRT-RUNBOOK.md.

// registerSecurityResponse adds the producer + rule-pack subcommands to the security
// command group (called from newSecurityCmd).
func registerSecurityResponse(root *cobra.Command) {
	root.AddCommand(newSecurityAdvisoriesCmd(), newSecurityRulePackCmd())
}

// --- producer: security advisories (PSIRT) ----------------------------------

// advisoryDraft is the minimal input a PSIRT author supplies: the author identity
// and the OSV advisories. The feed's schema and modified-time are stamped for them.
type advisoryDraft struct {
	Author     string                 `json:"author"`
	Advisories []secadvisory.Advisory `json:"advisories"`
}

// securityAdvisoriesResult is the -o json pane of `security advisories`.
//
// A producer's report is WHERE IT PUT THE ARTIFACT, and the prose says it in a
// sentence a script has to unpick ("wrote a + a.sig (N advisory(ies); …)"). The
// two paths are separate keys because they are two files: the ceremony copies
// them independently, and `signature` is not always derivable from `feed` by a
// caller that did not choose --out itself.
//
// It reports EXACTLY the facts the sentence reports and no others, which is the
// rule every leaf in this lot follows. The temptation here was `author` — the
// stamped feed author, which --author can silently override — but a pane that
// carries more than its text pane is a second contract, not a second spelling of
// one, and the two then drift on different schedules. Whatever is worth adding is
// worth adding to BOTH panes, in its own change.
type securityAdvisoriesResult struct {
	Feed       string `json:"feed"`
	Signature  string `json:"signature"`
	Advisories int    `json:"advisories"`
}

func newSecurityAdvisoriesCmd() *cobra.Command {
	var in, out, signKey, author, expectPubkey string
	cmd := &cobra.Command{
		Use:   "advisories",
		Short: "Build and sign an OSV advisory feed the product self-checks — PSIRT use",
		Long: "advisories validates a PSIRT draft, stamps its feed metadata, signs it with the\n" +
			"release Ed25519 key, self-verifies the result and writes the feed plus detached signature.",
		Example: "  olivares security advisories --in draft-advisories.json --out advisories.json \\\n" +
			"    --sign-key @release-private.key --expect-pubkey \"$OLIVARES_RELEASE_PUBKEY\"",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(in) == "" || strings.TrimSpace(signKey) == "" {
				return fmt.Errorf("--in and --sign-key are required")
			}
			raw, err := os.ReadFile(in)
			if err != nil {
				return fmt.Errorf("read draft: %w", err)
			}
			var d advisoryDraft
			if err := json.Unmarshal(raw, &d); err != nil {
				return fmt.Errorf("parse draft: %w", err)
			}
			if author != "" {
				d.Author = author
			}
			// NewFeed stamps the schema version + modified time; Sign validates + signs
			// under the security-advisories domain tag (the same reader the product runs).
			feed := secadvisory.NewFeed(d.Author, time.Now().UTC(), d.Advisories)
			priv, err := securityResponseSigningKey(signKey, expectPubkey)
			if err != nil {
				return err
			}
			feedBytes, sig, err := feed.Sign(priv)
			if err != nil {
				return err
			}
			// Prove it verifies against the derived public key before we exit.
			if _, err := secadvisory.VerifyFeed(feedBytes, sig, priv.Public().(ed25519.PublicKey)); err != nil {
				return fmt.Errorf("self-verify of the signed feed failed: %w", err)
			}
			if err := os.WriteFile(out, feedBytes, 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(out+".sig", sig, 0o600); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "wrote %s + %s.sig (%d advisory(ies); verify with the embedded release key)\n",
					out, out, len(feed.Advisories))
				return werr
			}, securityAdvisoriesResult{
				Feed: out, Signature: out + ".sig", Advisories: len(feed.Advisories),
			})
		},
	}
	cmd.Flags().StringVar(&in, "in", "", `draft advisory JSON ({"author":"…","advisories":[…OSV…]}) (required)`)
	cmd.Flags().StringVar(&out, "out", "advisories.json", "output feed path (a .sig is written beside it)")
	cmd.Flags().StringVar(&signKey, "sign-key", "", "base64 (or @file) Ed25519 private key (required)")
	cmd.Flags().StringVar(&author, "author", "", "override the feed author")
	cmd.Flags().StringVar(&expectPubkey, "expect-pubkey", "", "base64 (or @file) Ed25519 PUBLIC key the signature must verify against — the anchor the fleet pins (required)")
	_ = cmd.MarkFlagRequired("expect-pubkey")
	return cmd
}

// --- hot-reload security rule-packs -----------------------------------------

// newSecurityRulePackCmd authors and verifies signed hot-reload rule-packs (the OSS,
// subscription-free security-rule channel; connectors/threatfeed). The engine applies
// a verified pack at runtime without a restart (anti-rollback + instant rollback);
// these subcommands are the offline producer + a pre-flight verify.
func newSecurityRulePackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rulepack",
		Short: "Author/verify signed hot-reload security rule-packs (deny-lists, MCP blocks, patterns)",
		Long: "rulepack builds and verifies the signed rule bundles the engine hot-reloads:\n" +
			"deny-lists, MCP server blocks and content patterns that must take effect\n" +
			"during an incident without waiting for a release.\n\n" +
			"The signature is what makes that safe — a rule-pack that arrives unsigned, or\n" +
			"signed by a key the engine does not trust, is refused rather than applied.",
		Example: "  olivares security rulepack verify --in rulepack.json --pubkey \"$RELEASE_PUBLIC_KEY\"\n" +
			"  olivares security rulepack sign --in rulepack-draft.json --out rulepack.json \\\n" +
			"    --sign-key @release-private.key",
	}
	cmd.AddCommand(newRulePackSignCmd(), newRulePackVerifyCmd())
	return cmd
}

// rulePackSignResult and rulePackVerifyResult are the -o json panes of `security
// rulepack sign` and `security rulepack verify`.
//
// THEY SHARE THEIR FACT KEYS ON PURPOSE. Both prose lines already report the same
// five things about the same artifact — version, and the three content counts,
// plus the issue time on verify — so a script that reads a verify result must be
// able to read a sign receipt with the same field names. Two spellings of
// `blocked_mcp` across a producer and its verifier would force a consumer to
// write the mapping this unit exists to delete.
//
// sign adds the two PATHS it wrote, because that is what a producer's report is
// and the prose says it in a sentence. verify has no paths to report: it was
// GIVEN them.
//
// The counts are counts, not the lists. That is what the prose reports ("1
// indicators, 1 patterns, 1 blocked MCP") and what `verify`'s own Short calls a
// "contents summary", and a pane that quietly expanded them into the full IOC and
// pattern arrays would be a different, much larger contract wearing this one's
// name — including on `sign`, where the draft the caller just supplied would be
// echoed back at them.
type rulePackSignResult struct {
	Pack       string `json:"pack"`
	Signature  string `json:"signature"`
	Version    uint64 `json:"version"`
	Indicators int    `json:"indicators"`
	Patterns   int    `json:"patterns"`
	BlockedMCP int    `json:"blocked_mcp"`
}

// rulePackVerifyResult is the verify half. `issued_at` is the pack's own RFC3339
// string, carried verbatim rather than re-formatted: it is a signed field of the
// artifact, and a CLI that normalised it would be reporting its own rendering of
// a value the signature covers.
type rulePackVerifyResult struct {
	Version    uint64 `json:"version"`
	IssuedAt   string `json:"issued_at"`
	Indicators int    `json:"indicators"`
	Patterns   int    `json:"patterns"`
	BlockedMCP int    `json:"blocked_mcp"`
}

func newRulePackSignCmd() *cobra.Command {
	var in, out, signKey, expectPubkey string
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Build and sign a rule-pack from a draft (writes <out> + <out>.sig)",
		Long: "sign normalizes a security rule-pack draft, signs it with an Ed25519 release key,\n" +
			"self-verifies the result and writes the pack plus its detached signature.",
		Example: "  olivares security rulepack sign --in rulepack-draft.json --out rulepack.json \\\n" +
			"    --sign-key @release-private.key --expect-pubkey \"$OLIVARES_RELEASE_PUBKEY\"",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in == "" || signKey == "" {
				return fmt.Errorf("--in and --sign-key are required")
			}
			draft, err := os.ReadFile(in)
			if err != nil {
				return fmt.Errorf("read draft: %w", err)
			}
			var pack threatfeed.RulePack
			if err := json.Unmarshal(draft, &pack); err != nil {
				return fmt.Errorf("parse draft: %w", err)
			}
			pack.Schema = threatfeed.RulePackSchema
			if strings.TrimSpace(pack.IssuedAt) == "" {
				pack.IssuedAt = time.Now().UTC().Format(time.RFC3339)
			}
			b, err := threatfeed.MarshalRulePack(pack)
			if err != nil {
				return err
			}
			b = append(b, '\n')
			priv, err := securityResponseSigningKey(signKey, expectPubkey)
			if err != nil {
				return err
			}
			sig := threatfeed.SignRulePack(b, priv)
			if _, err := threatfeed.VerifyRulePack(b, sig, []ed25519.PublicKey{priv.Public().(ed25519.PublicKey)}); err != nil {
				return fmt.Errorf("self-verify of the signed rule-pack failed: %w", err)
			}
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(out+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "wrote %s + %s.sig (v%d: %d indicators, %d patterns, %d blocked MCP)\n",
					out, out, pack.Version, len(pack.Indicators), len(pack.Patterns), len(pack.BlockedMCP))
				return werr
			}, rulePackSignResult{
				Pack: out, Signature: out + ".sig", Version: pack.Version,
				Indicators: len(pack.Indicators), Patterns: len(pack.Patterns),
				BlockedMCP: len(pack.BlockedMCP),
			})
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "draft rule-pack JSON (required)")
	cmd.Flags().StringVar(&out, "out", "rulepack.json", "output rule-pack path")
	cmd.Flags().StringVar(&signKey, "sign-key", "", "base64 (or @file) Ed25519 private key (required)")
	cmd.Flags().StringVar(&expectPubkey, "expect-pubkey", "", "base64 (or @file) Ed25519 PUBLIC key the signature must verify against — the anchor the fleet pins (required)")
	_ = cmd.MarkFlagRequired("expect-pubkey")
	return cmd
}

func newRulePackVerifyCmd() *cobra.Command {
	var in, sig, pubkey string
	cmd := &cobra.Command{
		Use:          "verify",
		Short:        "Verify a signed rule-pack against a trusted key and print its summary",
		Long:         "verify authenticates a signed security rule-pack against a pinned Ed25519 public key and prints its version and contents summary.",
		Example:      "  olivares security rulepack verify --in rulepack.json --pubkey \"$RELEASE_PUBLIC_KEY\"",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in == "" || pubkey == "" {
				return fmt.Errorf("--in and --pubkey are required")
			}
			if sig == "" {
				sig = in + ".sig"
			}
			packBytes, err := os.ReadFile(in)
			if err != nil {
				return err
			}
			sigB, err := os.ReadFile(sig)
			if err != nil {
				return err
			}
			sigRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB)))
			if err != nil {
				return fmt.Errorf("signature is not base64: %w", err)
			}
			pub, err := release.DecodePublicKey(pubkey)
			if err != nil {
				return err
			}
			pack, err := threatfeed.VerifyRulePack(packBytes, sigRaw, []ed25519.PublicKey{pub})
			if err != nil {
				return err
			}
			// The document carries NO "ok" field. Reaching this line IS the verdict —
			// every way this command can fail returns an error above, with its own
			// non-zero code — so a boolean that can only ever be true would be a probe
			// that cannot fail, and a consumer trusting it would be trusting nothing.
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "OK: rule-pack v%d (issued %s) — %d indicators, %d patterns, %d blocked MCP\n",
					pack.Version, pack.IssuedAt, len(pack.Indicators), len(pack.Patterns), len(pack.BlockedMCP))
				return werr
			}, rulePackVerifyResult{
				Version: pack.Version, IssuedAt: pack.IssuedAt,
				Indicators: len(pack.Indicators), Patterns: len(pack.Patterns),
				BlockedMCP: len(pack.BlockedMCP),
			})
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "rule-pack JSON to verify (required)")
	cmd.Flags().StringVar(&sig, "sig", "", "signature path (default: <in>.sig)")
	cmd.Flags().StringVar(&pubkey, "pubkey", "", "base64 Ed25519 trusted key (required)")
	return cmd
}

// ── El ancla de firma de los dos productores de respuesta a incidentes ────────────────────
//
// ⛔ POR QUE HACE FALTA, Y POR QUE NO BASTA CON EL AUTO-VERIFY QUE YA HABIA. Una clave publica
// Ed25519 y un seed miden LOS DOS 32 bytes, asi que `loadEd25519Private` acepta la mitad PUBLICA
// como seed y deriva OTRO par, en silencio y con rc=0. El feed sale firmado por una clave que no
// tiene nadie, y el receptor —que fija la publica original— lo rechaza. Es la misma confusion que
// el contraste `sol max` reprodujo para `ddil export` (F-02).
//
// El auto-verify del rule-pack NO la caza y conviene decir por que: comprueba la firma contra
// `priv.Public()`, o sea la publica DERIVADA de lo que se haya cargado. Un verificador construido
// sobre la misma suposicion que el productor confirma la creencia en vez de probarla: pasa igual
// de verde con el par correcto que con el derivado por error. La separacion tiene que ser
// EXTRINSECA — de ahi `--expect-pubkey`, que el operador toma del ancla que fija la flota.
//
// ⛔ Y NI UN BYTE DE CLAVE EN EL DIAGNOSTICO: solo huellas de 8 hex. Imprimir el ancla o la
// derivada en base64 publica 44 caracteres en el stderr de la ceremonia, y si el error fue pasar
// la privada por --expect-pubkey, eso manda el SEED a los logs. La huella identifica sin revelar.
func securityResponseSigningKey(signKeyFlag, expectPubFlag string) (ed25519.PrivateKey, error) {
	expect, err := securityResponseLoadPublic(expectPubFlag)
	if err != nil {
		return nil, err
	}
	priv, err := loadEd25519Private(signKeyFlag)
	if err != nil {
		return nil, err
	}
	got := priv.Public().(ed25519.PublicKey)
	if !got.Equal(expect) {
		// ⛔ AQUI NO SE PUEDE DECIR CUAL DE LAS DOS BANDERAS ESTA MAL. Esta condicion se
		// cumple tanto si se paso la publica a --sign-key como si las dos apuntan al mismo
		// fichero; sin una procedencia autenticada del ancla no hay forma de distinguirlas,
		// y afirmar una manda a buscar al sitio equivocado.
		return nil, fmt.Errorf(
			"REFUSING to sign: --sign-key derives public key %s, but --expect-pubkey anchors %s.\n\n"+
				"Una clave publica Ed25519 y un seed miden los dos 32 bytes, asi que una publica pasada "+
				"como clave de firma se acepta y deriva OTRO par: la firma verificaria contra una clave "+
				"que no tiene nadie. Tres causas ordinarias, y este mandato NO puede distinguirlas "+
				"sin una procedencia autenticada del ancla: (1) la clave de firma y el ancla son de "+
				"PARES DISTINTOS —lo mas comun: se roto una y no la otra—; (2) se paso la PUBLICA "+
				"en --sign-key; (3) las dos banderas apuntan al MISMO material. Comprueba cual de "+
				"las tres es. (Huellas de 8 hex: no se imprime material de clave.)",
			securityResponseFingerprint(got), securityResponseFingerprint(expect))
	}
	return priv, nil
}

// securityResponseLoadPublic acepta las MISMAS tres codificaciones que el cargador privado
// —base64 estandar, base64 raw-url y `@fichero` con cualquiera de las dos— porque un ancla que
// solo entienda una de ellas convierte un formato valido en «clave equivocada», que es el error
// mas caro de diagnosticar de los tres.
func securityResponseLoadPublic(flag string) (ed25519.PublicKey, error) {
	raw := strings.TrimSpace(flag)
	if raw == "" {
		return nil, fmt.Errorf("--expect-pubkey is required: it is the Ed25519 PUBLIC key the fleet pins, and without it a public key passed as --sign-key cannot be told from a seed")
	}
	if strings.HasPrefix(raw, "@") {
		b, err := os.ReadFile(raw[1:])
		if err != nil {
			// ⛔ LA RUTA NO SE ECO-A, y no es paranoia: `--expect-pubkey @<algo>` con la
			// CLAVE pegada donde iba la ruta publica ese material en el error. `%w` sobre
			// el error de `os` arrastra el argumento entero. Se dice QUE fallo y de que
			// clase, que es lo que un operador necesita, sin repetir lo que escribio.
			switch {
			case errors.Is(err, os.ErrNotExist):
				return nil, fmt.Errorf("--expect-pubkey names a file that does not exist (the path is not echoed: a mistyped flag can put key material where a path belongs)")
			case errors.Is(err, os.ErrPermission):
				return nil, fmt.Errorf("--expect-pubkey names a file that cannot be read: permission denied (path not echoed)")
			default:
				return nil, fmt.Errorf("--expect-pubkey names a file that cannot be read (path not echoed)")
			}
		}
		raw = strings.TrimSpace(string(b))
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		if dec, err = base64.RawURLEncoding.DecodeString(raw); err != nil {
			return nil, fmt.Errorf("--expect-pubkey is not valid base64")
		}
	}
	if len(dec) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("--expect-pubkey is %d bytes, want %d", len(dec), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(dec), nil
}

// securityResponseFingerprint identifica una clave publica sin revelarla: los 8 primeros hex del
// SHA-256 de sus bytes. Suficiente para casar dos anclas en un mensaje de error, inutil para
// reconstruir la clave.
func securityResponseFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:8]
}
