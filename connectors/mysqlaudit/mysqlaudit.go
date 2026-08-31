// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mysqlaudit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/logtail"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.mysql-audit"

// Format constants for the `format` setting.
const (
	formatMariaDBAudit = "mariadb_audit"
	formatGeneralLog   = "general_log"
)

// Source is the mysql-audit source connector. The zero value is not usable; call New.
type Source struct {
	path   string
	format string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a mysql-audit source with default configuration (MariaDB Audit
// Plugin format, follow on).
func New() *Source {
	return &Source{format: formatMariaDBAudit, follow: true}
}

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "MySQL/MariaDB audit",
		Description: "Captures R/RW access from the MariaDB Audit Plugin or the MySQL general query log, read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "log_path", Type: sdk.FieldString, Required: true, Description: "path to the audit log file to read"},
			{Key: "format", Type: sdk.FieldString, Default: formatMariaDBAudit, Description: "log format: mariadb_audit or general_log"},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "tail the log continuously"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated users/user@host that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("log_path"))
	if s.path == "" {
		return errors.New("mysql-audit: log_path is required")
	}
	if f := strings.ToLower(strings.TrimSpace(cfg.Get("format"))); f != "" {
		s.format = f
	}
	if s.format != formatMariaDBAudit && s.format != formatGeneralLog {
		return fmt.Errorf("mysql-audit: unsupported format %q (want %q or %q)", s.format, formatMariaDBAudit, formatGeneralLog)
	}
	s.follow = cfg.GetBool("follow", true)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather tails the configured log and emits an edge per audited data access.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.format == formatGeneralLog {
		return s.gatherGeneralLog(ctx, sink)
	}
	return s.gatherMariaDBAudit(ctx, sink)
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// gatherMariaDBAudit tails a MariaDB Audit Plugin log, emitting an edge per
// TABLE/QUERY event.
func (s *Source) gatherMariaDBAudit(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		ev, ok := parseAuditLine(string(line))
		if !ok {
			return nil
		}
		edge, ok := s.buildAuditEdge(ev)
		if !ok {
			return nil
		}
		return sink.Emit(ctx, edge)
	})
}

// buildAuditEdge maps a MariaDB audit event to an edge, or ok=false if it is not
// an emittable data access (a CONNECT event, an empty identity/resource, or an
// unparseable timestamp).
func (s *Source) buildAuditEdge(ev mariaEvent) (model.EdgeObservation, bool) {
	if ev.user == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(ev.timestamp, mariaTimeLayouts)
	if !ok {
		return model.EdgeObservation{}, false
	}

	originRef := ev.user
	if ev.host != "" {
		originRef = ev.user + "@" + ev.host
	}

	var mode model.AccessMode
	var resourceKind, resourceRef, toolRef string

	switch {
	case isTableOp(ev.operation):
		m, _ := tableOpToMode(ev.operation)
		table := strings.Trim(ev.object, "`")
		if table == "" {
			return model.EdgeObservation{}, false
		}
		mode, resourceKind, toolRef = m, "mysql.table", ev.operation
		resourceRef = table
		if ev.database != "" {
			resourceRef = ev.database + "." + table
		}
	case strings.HasPrefix(ev.operation, "QUERY"):
		if ev.database == "" {
			return model.EdgeObservation{}, false // no resolvable resource
		}
		m, verb := classifyVerb(ev.object)
		mode, resourceKind, resourceRef, toolRef = m, "mysql.database", ev.database, verb
	default:
		return model.EdgeObservation{}, false // CONNECT/DISCONNECT/…
	}

	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    originRef,
		ResourceKind: resourceKind,
		ResourceRef:  resourceRef,
		Mode:         mode,
		Source:       SignalMySQLAudit,
		Confidence:   s.shared.ConfidenceFor(ev.user, originRef),
		ToolRef:      toolRef,
		ObservedAt:   ts,
	}, true
}

// isTableOp reports whether op is a MariaDB Audit Plugin TABLE-event operation.
func isTableOp(op string) bool {
	_, ok := tableOpToMode(op)
	return ok
}

// genConn is the tracked state of one connection in the general query log.
type genConn struct {
	userHost string
	db       string
}

// gatherGeneralLog tails the MySQL general query log, running a connection state
// machine (Connect/Change user → identity+db, Init DB / USE → db, Quit → drop)
// so each Query is attributed to its user@host and current database.
func (s *Source) gatherGeneralLog(ctx context.Context, sink sdk.Sink) error {
	state := make(map[string]genConn)
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		e, ok := parseGeneralLine(string(line))
		if !ok {
			return nil // header or statement-continuation line
		}
		switch e.command {
		case "Connect", "Change user":
			// A Connect (or a COM_CHANGE_USER re-auth) starts a fresh session:
			// reset the state so a reused connection id never inherits the prior
			// session's user or database.
			uh, db := parseConnectArg(e.argument)
			state[e.connID] = genConn{userHost: uh, db: db}
		case "Init DB":
			c := state[e.connID]
			c.db = strings.TrimSpace(e.argument)
			state[e.connID] = c
		case "Quit":
			delete(state, e.connID)
		case "Query":
			c := state[e.connID]
			if db, ok := dbFromUse(e.argument); ok {
				c.db = db
				state[e.connID] = c
				return nil // a USE statement is not a data access
			}
			edge, ok := s.buildGeneralEdge(e, c)
			if !ok {
				return nil
			}
			return sink.Emit(ctx, edge)
		}
		return nil
	})
}

// buildGeneralEdge maps a general-log Query to an edge, or ok=false when the
// connection is unattributed (its Connect predates the log) or has no current
// database, or the timestamp is unparseable.
func (s *Source) buildGeneralEdge(e genEntry, c genConn) (model.EdgeObservation, bool) {
	if c.userHost == "" || c.db == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(e.timestamp, generalTimeLayouts)
	if !ok {
		return model.EdgeObservation{}, false
	}
	mode, verb := classifyVerb(e.argument)
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    c.userHost,
		ResourceKind: "mysql.database",
		ResourceRef:  c.db,
		Mode:         mode,
		Source:       SignalMySQLAudit,
		Confidence:   s.shared.ConfidenceFor(userOf(c.userHost), c.userHost),
		ToolRef:      verb,
		ObservedAt:   ts,
	}, true
}
