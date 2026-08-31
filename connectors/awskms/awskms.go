// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package awskms

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.aws-kms"

// Source is the AWS KMS / Secrets Manager audit connector. It satisfies
// sdk.SourceConnector (the OBSERVED key/secret-access edges) and
// identitysource.GraphProvider (the secret_store inventory). The zero
// value is not usable; call New.
type Source struct {
	path   string
	shared identity.SharedSet
	now    func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an aws-kms source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AWS KMS & Secrets Manager (CloudTrail)",
		Description: "Observes AWS KMS and Secrets Manager use from CloudTrail (who used which key/secret), read-only. Never reads a secret value or key material.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "CloudTrail export file, or a directory of *.json / *.json.gz files."},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated IAM role ARNs that are shared (attribution marked approximate)."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("aws-kms: path is required")
	}
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured CloudTrail export and emits one edge per KMS /
// Secrets Manager event that names a key or secret. It is a batch source: it
// returns nil when the files are exhausted.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		recs, err := readRecords(f)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, edge := range s.buildEdges(rec) {
				if err := sink.Emit(ctx, edge); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Snapshot exposes the AWS secret-manager custodians seen in the export as
// secret_store NHIs. It re-reads the export (cheap) and collects the
// distinct (service, account, region) scopes from the key/secret ARNs. With no
// configured path it returns an empty graph (offline). It never returns a secret
// value or key material — only the store's existence.
func (s *Source) Snapshot(_ context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceAWSKMS, CapturedAt: s.clock().UTC()}
	if s.path == "" {
		return g, nil
	}
	files, err := s.listFiles()
	if err != nil {
		return identitysource.Graph{}, err
	}
	stores := map[string]store{}
	for _, f := range files {
		recs, err := readRecords(f)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, rec := range recs {
			for _, res := range resolveResources(rec) {
				if st, ok := storeFromARN(res.ref); ok {
					stores[st.ref()] = st
				}
			}
		}
	}
	refs := make([]string, 0, len(stores))
	for ref := range stores {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		st := stores[ref]
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         ref,
			Type:        identitysource.PrincipalNHI,
			Kind:        identitysource.KindSecretStore,
			DisplayName: st.displayName(),
			Source:      identitysource.SourceAWSKMS,
			Attributes:  map[string]string{"provider": "aws", "service": st.service, "account": st.account, "region": st.region},
		})
	}
	return g, nil
}

// buildEdges maps one CloudTrail record to its access edges (zero, one, or — for
// ReEncrypt — two). It returns nothing for a non-KMS/non-Secrets event, an event
// that names no resolvable key/secret, an unparseable timestamp, or an
// unattributable identity.
func (s *Source) buildEdges(rec record) []model.EdgeObservation {
	if rec.EventSource != kmsEventSource && rec.EventSource != secretsEventSource {
		return nil
	}
	ts, ok := parseTime(rec.EventTime)
	if !ok {
		return nil
	}
	origin, conf, ok := s.resolveIdentity(rec.UserIdentity)
	if !ok {
		return nil
	}
	mode := classifyMode(rec)
	resources := resolveResources(rec)
	out := make([]model.EdgeObservation, 0, len(resources))
	for _, res := range resources {
		out = append(out, model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    origin,
			ResourceKind: res.kind,
			ResourceRef:  res.ref,
			Mode:         mode,
			Source:       model.SignalCloudTrail,
			Confidence:   conf,
			ToolRef:      rec.EventName,
			ObservedAt:   ts,
		})
	}
	return out
}

// resolveIdentity returns the raw IAM identity a call is attributed to and the
// attribution confidence (docs/contracts), mirroring s3-cloudtrail: the raw
// principal is always emitted; confidence drops to approximate for a shared
// assumed-role, an AWS service principal, or an account/anonymous identity.
func (s *Source) resolveIdentity(ui userIdentity) (ref string, conf model.Confidence, ok bool) {
	switch ui.Type {
	case "AWSService":
		if ui.InvokedBy == "" {
			return "", "", false
		}
		return ui.InvokedBy, model.ConfidenceApproximate, true
	case "AssumedRole":
		ref = firstNonEmpty(ui.ARN, ui.PrincipalID)
		if ref == "" {
			return "", "", false
		}
		return ref, s.shared.ConfidenceFor(ui.SessionContext.SessionIssuer.ARN), true
	case "IAMUser", "Root", "FederatedUser", "Directory", "IdentityCenterUser":
		ref = firstNonEmpty(ui.ARN, ui.PrincipalID)
		if ref == "" {
			return "", "", false
		}
		return ref, s.shared.ConfidenceFor(ref), true
	default:
		ref = firstNonEmpty(ui.ARN, ui.PrincipalID, ui.AccountID)
		if ref == "" {
			return "", "", false
		}
		return ref, model.ConfidenceApproximate, true
	}
}

// listFiles resolves the configured path to a sorted list of files (a directory
// contributes its *.json and *.json.gz entries; a file contributes itself).
func (s *Source) listFiles() ([]string, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{s.path}, nil
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".json.gz") {
			files = append(files, filepath.Join(s.path, n))
		}
	}
	sort.Strings(files)
	return files, nil
}

// readRecords reads one CloudTrail file (gunzipping a .gz) into its records.
func readRecords(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return recordsFromBytes(data), nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
