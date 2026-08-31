// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kongaudit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.kong-audit"

// SignalKongAudit is the SignalSource for this connector. SignalSource is an OPEN
// string (sdk/model/enums.go), so a connector declares its own provenance value
// PACKAGE-LOCALLY rather than editing the sealed enum — the operator then never
// silently collapses a Kong control-plane edge with a mesh L7 or kernel L4 edge.
const SignalKongAudit model.SignalSource = "kong_audit"

// Source is the Kong Gateway audit connector. It satisfies sdk.SourceConnector: it
// reads an EXPORTED Kong audit log and emits one EdgeObservation per Admin API
// access (audit_requests) and one FindingReport per config change (audit_objects).
// The zero value is not usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the SourceConnector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a kong-audit source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Kong Gateway Audit Log",
		Description: "Observes who changed the Kong Gateway config via the Admin API from Kong's exported audit log (audit_requests + audit_objects), read-only. Never reads a request body or entity snapshot.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Kong audit log file, or a directory of *.json / *.jsonl / *.log files (JSON lines)."},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("kong-audit: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured Kong audit export and emits an edge per Admin API
// request and a finding per entity change. It is a BATCH source: it lists the
// files, parses every record, emits, and returns nil when the files are exhausted
// (the engine decides when to re-run it). It honors ctx in the loop.
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
			obs, ok := s.observation(rec)
			if !ok {
				continue
			}
			if err := sink.Emit(ctx, obs); err != nil {
				return err
			}
		}
	}
	return nil
}

// observation maps one Kong audit record to its observation: an EdgeObservation
// for an audit_requests record (an Admin API access) or a FindingReport for an
// audit_objects record (a config change). ok=false for an unrecognized record or
// one missing the minimum it needs to be attributed.
func (s *Source) observation(rec record) (model.Observation, bool) {
	switch rec.kind() {
	case streamRequests:
		return s.requestEdge(rec)
	case streamObjects:
		return s.changeFinding(rec)
	default:
		return nil, false
	}
}

// requestEdge builds the EdgeObservation for an Admin API access. OriginRef is the
// RBAC user (id, then name) and Confidence is Attributed; with RBAC off there is
// no user, so it falls back to the client IP at Approximate confidence. A record
// with neither an identity nor an IP, or no parseable timestamp, is skipped (never
// emitted with a fabricated origin or zero time).
func (s *Source) requestEdge(rec record) (model.EdgeObservation, bool) {
	ts, ok := rec.requestTime()
	if !ok {
		return model.EdgeObservation{}, false
	}
	origin, conf, ok := requestOrigin(rec)
	if !ok {
		return model.EdgeObservation{}, false
	}
	path := strings.TrimSpace(rec.Path)
	if path == "" {
		return model.EdgeObservation{}, false
	}
	method := strings.ToUpper(strings.TrimSpace(rec.Method))
	return model.EdgeObservation{
		OriginKind:   meshobs.OriginKindIdentity,
		OriginRef:    origin,
		ResourceKind: resourceAdminAPI,
		ResourceRef:  path,
		// Mode is taken VERBATIM from the HTTP method via the shared L7 mapping —
		// GET/HEAD -> read, POST/PUT/PATCH -> readwrite, DELETE -> write, else unknown.
		Mode:       meshobs.MethodToMode(method),
		Source:     SignalKongAudit,
		Confidence: conf,
		ToolRef:    method,
		ObservedAt: ts,
	}, true
}

// requestOrigin resolves who performed an Admin API request and the attribution
// confidence: an RBAC user is firmly attributed; a bare client IP (RBAC disabled)
// is approximate. ok=false when the record names neither.
func requestOrigin(rec record) (ref string, conf model.Confidence, ok bool) {
	if u := rec.rbacUser(); u != "" {
		return u, model.ConfidenceAttributed, true
	}
	if ip := strings.TrimSpace(rec.ClientIP); ip != "" {
		return ip, model.ConfidenceApproximate, true
	}
	return "", "", false
}

// changeFinding builds the FindingReport for an entity change. Severity is Info: a
// config change is normal operations, not by itself an incident (the access map or
// a downstream rule decides if a SPECIFIC change is suspicious). The detail is
// reduced to a SHA-256 of a stable, non-sensitive key — the entity snapshot never
// leaves. A record with no entity key is skipped.
func (s *Source) changeFinding(rec record) (model.FindingReport, bool) {
	dao := strings.TrimSpace(rec.DAOName)
	key := strings.TrimSpace(rec.EntityKey)
	if key == "" {
		key = dao // some operations name only the DAO; never invent a key
	}
	if key == "" {
		return model.FindingReport{}, false
	}
	op := strings.ToLower(strings.TrimSpace(rec.Operation))
	subject := dao + ":" + key

	who := rec.rbacUser()
	if who == "" {
		who = "unknown"
	}

	ts, ok := rec.objectTime()
	if !ok {
		ts = s.clock()
	}

	// Stable de-dup key for the hashed detail; non-sensitive identifiers only.
	detail := strings.Join([]string{rec.RequestID, op, dao, key}, "|")

	return model.FindingReport{
		Kind:        findingKind,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectEntity,
		SubjectRef:  subject,
		Title:       "kong " + op + " " + dao + " by " + redact.Clean(who),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  ts,
	}, true
}

// listFiles resolves the configured path to a sorted list of files (a directory
// contributes its *.json / *.jsonl / *.log entries; a file contributes itself).
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
		n := e.Name()
		if strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".log") {
			files = append(files, filepath.Join(s.path, n))
		}
	}
	sort.Strings(files)
	return files, nil
}

// readRecords reads one Kong audit file into its records.
func readRecords(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return recordsFromBytes(data), nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}
