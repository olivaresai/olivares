// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// pgExecModeFact is the private, non-secret execution-mode fact of the
// application pool (BR1 ratification R2). It is derived from the parsed
// connection config with the same pgx v5.10.0 parser the pool uses; neither
// the DSN nor any connection setting is retained or changed.
type pgExecModeFact uint8

const (
	pgExecModeUnobserved pgExecModeFact = iota
	pgExecModeCacheStatement
	pgExecModeCacheDescribe
	pgExecModeDescribeExec
	pgExecModeExec
	pgExecModeSimpleProtocol
)

func (f pgExecModeFact) String() string {
	switch f {
	case pgExecModeCacheStatement:
		return "cache_statement"
	case pgExecModeCacheDescribe:
		return "cache_describe"
	case pgExecModeDescribeExec:
		return "describe_exec"
	case pgExecModeExec:
		return "exec"
	case pgExecModeSimpleProtocol:
		return "simple_protocol"
	default:
		return "unobserved"
	}
}

// textResults reports the modes whose stdlib results use the text format,
// where BYTEA decoding requires bytea_output=hex.
func (f pgExecModeFact) textResults() bool {
	return f == pgExecModeExec || f == pgExecModeSimpleProtocol
}

func pgExecModeFactFor(mode pgx.QueryExecMode) pgExecModeFact {
	switch mode {
	case pgx.QueryExecModeCacheStatement:
		return pgExecModeCacheStatement
	case pgx.QueryExecModeCacheDescribe:
		return pgExecModeCacheDescribe
	case pgx.QueryExecModeDescribeExec:
		return pgExecModeDescribeExec
	case pgx.QueryExecModeExec:
		return pgExecModeExec
	case pgx.QueryExecModeSimpleProtocol:
		return pgExecModeSimpleProtocol
	default:
		return pgExecModeUnobserved
	}
}

// observePGExecMode records the application pool's execution mode. A parse
// failure yields unobserved (and therefore unavailable); the error text is
// discarded because it can contain DSN material.
func observePGExecMode(cfg store.Config) pgExecModeFact {
	if cfg.Engine != store.EnginePostgres {
		return pgExecModeUnobserved
	}
	parsed, err := pgEngineConnConfig(cfg.DSN)
	if err != nil {
		return pgExecModeUnobserved
	}
	return pgExecModeFactFor(parsed.DefaultQueryExecMode)
}

// Bounded integer setting classes (ROOT-DECISION for r88-bounded-reader-pg).
// No variable-length setting value is returned to the caller.
const (
	pgClassUnknown int64 = 0
	pgClassUTF8    int64 = 1
	pgClassLATIN1  int64 = 2

	pgClassHex int64 = 1
	pgClassOn  int64 = 1
)

// pgSettingsStatement observes the current transaction's representation
// settings as five fixed INTEGER classes. It is reserved like every other
// statement and runs through the reader's own transaction on every method.
const pgSettingsStatement = "SELECT " +
	"CASE pg_catalog.current_setting('server_encoding') WHEN 'UTF8' THEN 1 WHEN 'LATIN1' THEN 2 ELSE 0 END, " +
	"CASE pg_catalog.current_setting('client_encoding') WHEN 'UTF8' THEN 1 WHEN 'LATIN1' THEN 2 ELSE 0 END, " +
	"CASE pg_catalog.current_setting('bytea_output') WHEN 'hex' THEN 1 WHEN 'escape' THEN 2 ELSE 0 END, " +
	"CASE pg_catalog.current_setting('standard_conforming_strings') WHEN 'on' THEN 1 WHEN 'off' THEN 2 ELSE 0 END, " +
	"CASE WHEN pg_catalog.current_setting('server_version_num')::pg_catalog.int4 BETWEEN 160000 AND 169999 " +
	"THEN 1 ELSE 0 END"

const pgSettingsColumns = 5

type pgSettingClasses struct {
	server, client, byteaOutput, standardStrings, version int64
}

// pgBoundedEligibility applies the accepted finite matrix. SQL_ASCII and every
// unknown class are refused for this optional capability; ordinary Store
// behavior is unaffected.
func pgBoundedEligibility(mode pgExecModeFact, s pgSettingClasses, hasBytes bool) error {
	unavailable := func(reason string) error {
		return fmt.Errorf("%w: PostgreSQL %s (execution mode %s)", store.ErrBoundedReadUnavailable, reason, mode)
	}
	switch mode {
	case pgExecModeCacheStatement, pgExecModeCacheDescribe, pgExecModeDescribeExec,
		pgExecModeExec, pgExecModeSimpleProtocol:
	default:
		return unavailable("execution mode is not qualified")
	}
	if s.version != 1 {
		return unavailable("server version is not qualified")
	}
	if s.server != pgClassUTF8 && s.server != pgClassLATIN1 {
		return unavailable("server encoding class is not qualified")
	}
	if s.client != pgClassUTF8 && s.client != pgClassLATIN1 {
		return unavailable("client encoding class is not qualified")
	}
	if mode == pgExecModeSimpleProtocol && (s.client != pgClassUTF8 || s.standardStrings != pgClassOn) {
		return unavailable("simple protocol requires UTF8 client encoding and standard_conforming_strings=on")
	}
	if hasBytes && mode.textResults() && s.byteaOutput != pgClassHex {
		return unavailable("text-result BYTEA requires bytea_output=hex")
	}
	return nil
}

// descriptorHasBytes reports a BYTEA field without allocating the column
// list; base columns are never BYTEA.
func descriptorHasBytes(desc model.EntityDescriptor) bool {
	for _, f := range desc.Fields {
		if f.Kind == model.KindBytes {
			return true
		}
	}
	return false
}

// pgTextOctetsPrefix + column + pgTextOctetsSuffix is the accepted measure of
// the returned driver TEXT bytes in the current client encoding. The column is
// always a registry-derived identifier.
const (
	pgTextOctetsPrefix = "pg_catalog.octet_length(pg_catalog.convert_to("
	pgTextOctetsSuffix = ", pg_catalog.current_setting('client_encoding')::pg_catalog.name))"
)

// boundedBackendError classifies a driver or backend failure. Conversion
// failures (22P05 untranslatable character, 22021 invalid byte sequence) and
// the driver's own simple-protocol configuration refusal make the whole method
// unavailable, with the backend cause retained. Nothing is retried.
func boundedBackendError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "22P05" || pgErr.Code == "22021") {
		return fmt.Errorf("%w: PostgreSQL representation conversion failed: %w",
			store.ErrBoundedReadUnavailable, err)
	}
	// pgx v5.10.0 Conn.sanitizeForSimpleQuery reports these as plain errors.
	if strings.HasPrefix(err.Error(), "simple protocol queries must be run with ") {
		return fmt.Errorf("%w: PostgreSQL driver refused the simple protocol configuration: %w",
			store.ErrBoundedReadUnavailable, err)
	}
	return wrapUnavailableErr(err)
}
