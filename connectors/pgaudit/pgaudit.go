// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgaudit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/logtail"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.pg-audit"

// Format constants for the `format` setting.
const (
	formatCSVLog  = "csvlog"
	formatJSONLog = "jsonlog"
)

// csvAppNameIndex is the 0-based column of application_name in a PostgreSQL
// csvlog record (stable for PG 12–16); csvMessageIndex is the message column.
const (
	csvMessageIndex = 13
	csvAppNameIndex = 22
)

// Source is the pg-audit source connector. It tails a structured PostgreSQL log
// and emits one EdgeObservation per audited data access. The zero value is not
// usable; call New.
type Source struct {
	path   string
	format string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a pg-audit source with default configuration (csvlog, follow on).
func New() *Source {
	return &Source{format: formatCSVLog, follow: true}
}

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "PostgreSQL pgAudit",
		Description: "Captures R/RW access from the PostgreSQL pgAudit trail (csvlog/jsonlog), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "log_path", Type: sdk.FieldString, Required: true, Description: "path to the PostgreSQL log file to read"},
			{Key: "format", Type: sdk.FieldString, Default: formatCSVLog, Description: "log format: csvlog or jsonlog"},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "tail the log continuously (jsonlog only; csvlog is read as a batch)"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated roles/application_names that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing log_path or an unsupported
// format is a configuration error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("log_path"))
	if s.path == "" {
		return errors.New("pg-audit: log_path is required")
	}
	if f := strings.ToLower(strings.TrimSpace(cfg.Get("format"))); f != "" {
		s.format = f
	}
	if s.format != formatCSVLog && s.format != formatJSONLog {
		return fmt.Errorf("pg-audit: unsupported format %q (want %q or %q)", s.format, formatCSVLog, formatJSONLog)
	}
	s.follow = cfg.GetBool("follow", true)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured log and emits an edge per audited access. jsonlog
// is line-delimited and supports continuous follow; csvlog (whose records can
// span newlines) is read as a batch and returns nil at EOF.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.format == formatJSONLog {
		return s.gatherJSON(ctx, sink)
	}
	return s.gatherCSV(ctx, sink)
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// gatherJSON tails a jsonlog file, emitting an edge per pgAudit line.
func (s *Source) gatherJSON(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		fr, ok := frameFromJSON(line)
		if !ok {
			return nil
		}
		return s.emit(ctx, sink, fr)
	})
}

// gatherCSV reads a csvlog file to EOF, emitting an edge per pgAudit record. A
// malformed record is skipped; a non-pgAudit record is skipped by emit.
func (s *Source) gatherCSV(ctx context.Context, sink sdk.Sink) error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // PostgreSQL csvlog column count varies by version
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				continue // skip a malformed record, keep reading
			}
			return err
		}
		fr, ok := frameFromCSV(rec)
		if !ok {
			continue
		}
		if err := s.emit(ctx, sink, fr); err != nil {
			return err
		}
	}
}

// emit builds an edge from a frame and emits it, skipping frames that are not an
// emittable data access.
func (s *Source) emit(ctx context.Context, sink sdk.Sink, fr frame) error {
	edge, ok := s.buildEdge(fr)
	if !ok {
		return nil
	}
	return sink.Emit(ctx, edge)
}

// buildEdge maps a log frame to an EdgeObservation, or ok=false if the frame is
// not an emittable pgAudit data access (non-pgAudit message, a skipped class, no
// object, an unparseable timestamp, or no identity).
func (s *Source) buildEdge(fr frame) (model.EdgeObservation, bool) {
	ar, ok := parseAuditMessage(fr.message)
	if !ok {
		return model.EdgeObservation{}, false
	}
	mode, emit := classToMode(ar.class)
	if !emit || ar.objName == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTimestamp(fr.timestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}

	// Identity: a distinguishing application_name (not declared shared) is the
	// per-agent bridge and is attributed; otherwise fall back to the session
	// role, and mark approximate if EITHER the application_name or the role is a
	// declared shared account — a shared application_name means the attribution
	// is ambiguous even though we report the role (docs/contracts).
	ref := fr.appName
	conf := model.ConfidenceAttributed
	if fr.appName == "" || s.shared.Has(fr.appName) {
		ref = fr.user
		conf = s.shared.ConfidenceFor(fr.appName, fr.user)
	}
	if ref == "" {
		return model.EdgeObservation{}, false
	}

	resource := ar.objName
	if fr.database != "" {
		resource = fr.database + "." + ar.objName
	}

	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    ref,
		ResourceKind: resourceKindFor(ar.objType),
		ResourceRef:  resource,
		Mode:         mode,
		Source:       model.SignalPGAudit,
		Confidence:   conf,
		ToolRef:      ar.command,
		ObservedAt:   ts,
	}, true
}

// frameFromCSV extracts the frame from a PostgreSQL csvlog record by column
// index. It returns ok=false if the record is too short to hold the message.
func frameFromCSV(rec []string) (frame, bool) {
	if len(rec) <= csvMessageIndex {
		return frame{}, false
	}
	fr := frame{
		timestamp: rec[0],
		user:      rec[1],
		database:  rec[2],
		message:   rec[csvMessageIndex],
	}
	if len(rec) > csvAppNameIndex {
		fr.appName = rec[csvAppNameIndex]
	}
	return fr, true
}

// jsonLogLine is the subset of PostgreSQL jsonlog fields the connector reads.
type jsonLogLine struct {
	Timestamp       string `json:"timestamp"`
	User            string `json:"user"`
	DBName          string `json:"dbname"`
	ApplicationName string `json:"application_name"`
	Message         string `json:"message"`
}

// frameFromJSON extracts the frame from a PostgreSQL jsonlog line. It returns
// ok=false for a line that is not valid JSON.
func frameFromJSON(line []byte) (frame, bool) {
	var j jsonLogLine
	if err := json.Unmarshal(line, &j); err != nil {
		return frame{}, false
	}
	return frame{
		timestamp: j.Timestamp,
		user:      j.User,
		database:  j.DBName,
		appName:   j.ApplicationName,
		message:   j.Message,
	}, true
}
