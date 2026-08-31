// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/ddil"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// newDDILCmd groups the disconnected-transfer operations.
func newDDILCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ddil",
		Short: "Air-gap DDIL bundles: export, verify and import governance state across a disconnected gap",
		Long: "ddil moves governance state across a gap no network crosses: export signs a\n" +
			"bundle on the connected side, a courier carries it, and import verifies,\n" +
			"reconciles and applies it on the disconnected side.\n\n" +
			"verify is the same check as import's first phase without applying anything, so\n" +
			"a bundle can be inspected before it is trusted. Every stage is fail-closed: an\n" +
			"unverifiable bundle is never partially applied.",
		Example: "  olivares ddil keygen --out ddil-private.b64 > ddil-public.b64\n" +
			"  olivares ddil verify --bundle transfer.ddil --pubkey @ddil-public.b64 -o json\n" +
			"  olivares ddil import --bundle transfer.ddil --pubkey @ddil-public.b64",
	}
	cmd.AddCommand(newDDILExportCmd(), newDDILVerifyCmd(), newDDILImportCmd(), newDDILKeygenCmd())
	return cmd
}

type memArchiveSink struct {
	objects map[string][]byte
}

func newMemArchiveSink() *memArchiveSink {
	return &memArchiveSink{objects: make(map[string][]byte)}
}

// Put verifies the declared digest before retaining a private copy. Allowing an
// identical re-put and refusing a different one mirrors the WORM sink contract, so an
// archive-export bug fails before the enclosing DDIL bundle is signed.
func (s *memArchiveSink) Put(ctx context.Context, key string, body []byte, opts audit.ArchivePutOptions) (audit.ArchiveReceipt, error) {
	if err := ctx.Err(); err != nil {
		return audit.ArchiveReceipt{}, err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if opts.ContentSHA256 != "" && !strings.EqualFold(opts.ContentSHA256, digest) {
		return audit.ArchiveReceipt{}, fmt.Errorf("ddil: in-memory archive sink: content sha256 mismatch for %q", key)
	}
	if existing, ok := s.objects[key]; ok {
		existingSum := sha256.Sum256(existing)
		if existingSum != sum {
			return audit.ArchiveReceipt{}, fmt.Errorf("ddil: in-memory archive sink: %q already exists with different content", key)
		}
		return audit.ArchiveReceipt{Location: "memory:" + key}, nil
	}
	s.objects[key] = append([]byte(nil), body...)
	return audit.ArchiveReceipt{Location: "memory:" + key}, nil
}

func (s *memArchiveSink) get(key string) ([]byte, bool) {
	body, ok := s.objects[key]
	return append([]byte(nil), body...), ok
}

type ddilExportPolicyReport struct {
	Included     bool   `json:"included"`
	Revision     string `json:"revision"`
	MaxStaleness string `json:"max_staleness"`
}

type ddilExportReport struct {
	Out       string                 `json:"out"`
	Tenant    string                 `json:"tenant"`
	Segments  int                    `json:"segments"`
	Events    int64                  `json:"events"`
	FromSeq   int64                  `json:"from_seq"`
	ToSeq     int64                  `json:"to_seq"`
	Policy    ddilExportPolicyReport `json:"policy"`
	Evidence  []string               `json:"evidence"`
	CreatedAt time.Time              `json:"created_at"`
	Expires   *time.Time             `json:"expires"`
}

func newDDILExportCmd() *cobra.Command {
	var (
		dataDir, engineName, dsn string
		tenant, out, signKey     string
		fromSeq                  int64
		segmentEvents            int
		evidenceSpecs            []string
		maxStaleness, expires    time.Duration
		notes                    string
		noPolicy                 bool
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Assemble and sign a DDIL bundle from the local governance store",
		Long: "export assembles one tenant's audit segments, active policy snapshot and optional evidence\n" +
			"into a signed courier bundle for transfer across a disconnected or air-gapped boundary.",
		Example: `  olivares ddil export --tenant t_abc123 --out transfer.ddil \
    --sign-key @ddil-private.b64 --evidence assessment=./assessment.pdf --expires 168h`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			if maxStaleness < 0 {
				return fmt.Errorf("--max-staleness must not be negative")
			}
			if expires < 0 {
				return fmt.Errorf("--expires must not be negative")
			}
			priv, err := loadEd25519Private(signKey)
			if err != nil {
				return err
			}
			evidence, evidenceNames, err := loadDDILEvidence(evidenceSpecs)
			if err != nil {
				return err
			}

			eng, err := auditBootRO(cmd, dataDir, engineName, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			sink := newMemArchiveSink()
			segments := make([]ddil.Segment, 0)
			auditReport, err := audit.ExportSegments(cmd.Context(), eng.store, t, sink,
				audit.ExportOptions{FromSeq: fromSeq, SegmentEvents: segmentEvents},
				func(res audit.SegmentResult) error {
					manifest, ok := sink.get(res.ManifestKey)
					if !ok {
						return fmt.Errorf("ddil export: archive sink omitted manifest %q", res.ManifestKey)
					}
					events, ok := sink.get(res.EventsKey)
					if !ok {
						return fmt.Errorf("ddil export: archive sink omitted events %q", res.EventsKey)
					}
					segments = append(segments, ddil.Segment{
						FromSeq:             res.Manifest.FromSeq,
						ToSeq:               res.Manifest.ToSeq,
						FirstHash:           res.Manifest.FirstHash,
						LastHash:            res.Manifest.LastHash,
						PrevSegmentLastHash: res.Manifest.PrevSegmentLastHash,
						ManifestJSON:        manifest,
						EventsJSONL:         events,
					})
					return nil
				})
			if err != nil {
				return err
			}

			var policySnapshot []byte
			var policyRevision string
			policyIncluded := false
			if !noPolicy {
				snapshot, revision, ok, err := governance.ActivePolicySnapshot(cmd.Context(), eng.store, t)
				if err != nil {
					return err
				}
				if ok {
					policySnapshot = []byte(snapshot)
					policyRevision = revision
					policyIncluded = true
				}
			}
			if !policyIncluded && cmd.Flags().Changed("max-staleness") {
				return fmt.Errorf("--max-staleness requires an included active policy snapshot")
			}

			createdAt := time.Now().UTC()
			var expiresAt *time.Time
			if expires > 0 {
				v := createdAt.Add(expires)
				expiresAt = &v
			}
			input := ddil.ExportInput{
				Tenant:             t.String(),
				PolicyRevision:     policyRevision,
				PolicySnapshot:     policySnapshot,
				PolicyMaxStaleness: maxStaleness,
				Segments:           segments,
				Evidence:           evidence,
				CreatedAt:          createdAt,
				Expires:            expiresAt,
				Notes:              notes,
			}
			if err := writeDDILBundleFile(out, input, priv); err != nil {
				return err
			}

			report := ddilExportReport{
				Out: out, Tenant: t.String(), Segments: len(segments), Events: auditReport.Events,
				FromSeq: auditReport.FromSeq, ToSeq: auditReport.ToSeq,
				Policy: ddilExportPolicyReport{
					Included: policyIncluded, Revision: policyRevision,
					MaxStaleness: maxStaleness.String(),
				},
				Evidence: evidenceNames, CreatedAt: createdAt, Expires: expiresAt,
			}
			// E2: honor -o instead of always printing JSON.
			return renderReportOut(cmd, report)
		},
	}
	addStoreFlags(cmd, &dataDir, &engineName, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id to export (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&out, "out", "", "output DDIL bundle file")
	cmd.Flags().StringVar(&signKey, "sign-key", "", "Ed25519 private key (base64 key/seed, or @file)")
	cmd.Flags().Int64Var(&fromSeq, "from-seq", 1, "first ledger sequence number to include")
	cmd.Flags().IntVar(&segmentEvents, "segment-events", audit.DefaultSegmentEvents, "maximum events per audit segment")
	cmd.Flags().StringArrayVar(&evidenceSpecs, "evidence", nil, "evidence file as name=path (repeatable)")
	cmd.Flags().DurationVar(&maxStaleness, "max-staleness", 0, "per-tenant policy freshness bound carried with the snapshot")
	cmd.Flags().DurationVar(&expires, "expires", 0, "bundle lifetime from creation (zero means no expiry)")
	cmd.Flags().StringVar(&notes, "notes", "", "optional bundle notes")
	cmd.Flags().BoolVar(&noPolicy, "no-policy", false, "omit the active policy snapshot plane")
	_ = cmd.MarkFlagRequired("out")
	_ = cmd.MarkFlagRequired("sign-key")
	return cmd
}

func loadDDILEvidence(specs []string) (map[string][]byte, []string, error) {
	evidence := make(map[string][]byte, len(specs))
	for _, spec := range specs {
		name, file, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, nil, fmt.Errorf("--evidence %q must use name=path", spec)
		}
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("--evidence %q has an empty name", spec)
		}
		if strings.Contains(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") {
			return nil, nil, fmt.Errorf("--evidence name %q must not contain '/', '\\', or '..'", name)
		}
		if _, exists := evidence[name]; exists {
			return nil, nil, fmt.Errorf("--evidence name %q is duplicated", name)
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, fmt.Errorf("read --evidence %q from %q: %w", name, file, err)
		}
		evidence[name] = body
	}
	names := make([]string, 0, len(evidence))
	for name := range evidence {
		names = append(names, name)
	}
	sort.Strings(names)
	return evidence, names, nil
}

func writeDDILBundleFile(path string, input ddil.ExportInput, priv ed25519.PrivateKey) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create DDIL bundle %q: %w", path, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("secure DDIL bundle %q: %w", path, err)
	}
	if err = ddil.Export(f, input, priv); err != nil {
		return fmt.Errorf("write DDIL bundle %q: %w", path, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("fsync DDIL bundle %q: %w", path, err)
	}
	err = f.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("close DDIL bundle %q: %w", path, err)
	}
	return nil
}

// ddilKeygenResult reports a generated transport keypair BY SINK, which is the
// whole safety property of this leaf expressed as a type.
//
// `ddil keygen` is the one command in the product whose text can put a private key
// on stdout, and it has two modes that differ in exactly that:
//
//   - WITH --out the seed goes to a 0600 file and stdout carries ONLY the public
//     half — a bare base64 line, because the documented idiom is
//     `keygen --out ddil-private.b64 > ddil-public.b64`. The report therefore names
//     PrivateKeyFile, the path the operator supplied, and PrivateKey stays empty.
//     Serializing the seed here would hand a structured log pipeline the one byte
//     string the --out flag exists to keep out of stdout, so a witness asserts its
//     absence rather than trusting this comment.
//   - WITHOUT --out the operator has chosen stdout as the sink for BOTH halves and
//     the text already prints `private: <seed>`. PrivateKey is populated, with the
//     same value the text shows. Dropping it would make the two forms of one
//     command disagree about which fields exist — the defect render.go:271-281
//     already refuses in writing for the empty-map case — and would silently break
//     the only mode where a script has any private half to capture. The exposure is
//     identical either way: same process, same file descriptor.
//
// Exactly one of the two private fields is ever set.
type ddilKeygenResult struct {
	PrivateKey     string `json:"private_key,omitempty"`
	PrivateKeyFile string `json:"private_key_file,omitempty"`
	PublicKey      string `json:"public_key"`
}

func newDDILKeygenCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate an Ed25519 DDIL transport keypair",
		Long: strings.TrimSpace(`
Generate a dedicated Ed25519 transport keypair for DDIL bundles. The private key signs
bundles through --sign-key on export; the receiving node pins its PUBLIC half through
--pubkey on import or verify. This is deliberately not the ledger or release key.`),
		Example: "  olivares ddil keygen --out ddil-private.b64 > ddil-public.b64",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return fmt.Errorf("generate DDIL transport key: %w", err)
			}
			privateB64 := base64.StdEncoding.EncodeToString(priv.Seed())
			publicB64 := base64.StdEncoding.EncodeToString(pub)
			if out != "" {
				if err := writeDDILPrivateSeed(out, []byte(privateB64+"\n")); err != nil {
					return err
				}
				// The seed is NOT carried here: it went to the file, so the report names
				// the file. See ddilKeygenResult.
				res := ddilKeygenResult{PrivateKeyFile: out, PublicKey: publicB64}
				return renderOut(cmd, func(w io.Writer) error {
					_, werr := fmt.Fprintln(w, res.PublicKey)
					return werr
				}, res)
			}
			// Stays on STDERR in both formats: a warning about custody is not part of the
			// data contract, and moving it into the object would let `2>/dev/null` — a
			// perfectly ordinary thing to write around a JSON pipe — swallow it.
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: the private DDIL key must be kept off the importing node")
			res := ddilKeygenResult{PrivateKey: privateB64, PublicKey: publicB64}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "private: %s\npublic: %s\n", res.PrivateKey, res.PublicKey)
				return werr
			}, res)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write the base64 private seed to this 0600 file")
	return cmd
}

func writeDDILPrivateSeed(path string, body []byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create DDIL private key %q: %w", path, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("secure DDIL private key %q: %w", path, err)
	}
	if _, err = f.Write(body); err != nil {
		return fmt.Errorf("write DDIL private key %q: %w", path, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("fsync DDIL private key %q: %w", path, err)
	}
	err = f.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("close DDIL private key %q: %w", path, err)
	}
	return nil
}
