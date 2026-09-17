// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

// Core v13 login capability control relation (ROOT-CONSTRUCTION-R5-1 §2–§3).
//
// It is a global control relation, not an entity descriptor: no tenant column, no RLS,
// no seed row. The statements are ROLE-FREE; the split-topology application grants are
// appended by the migration constructor that knows the resolved roles.
const (
	LoginCapabilityObservationTable = "login_capability_observation"
	LoginCapabilityKey              = "global/default/login-enforcement"
	LoginCapabilityLockKey          = "core.login.capability"
)

// LoginCapabilityControlStmts renders the PostgreSQL relation and its PUBLIC revoke.
func (postgresDialect) LoginCapabilityControlStmts() []string {
	rel := EngineSchema + "." + LoginCapabilityObservationTable
	return []string{
		`CREATE TABLE ` + rel + ` ` + LoginCapabilityPostgresTableBody(),
		`REVOKE ALL PRIVILEGES ON TABLE ` + rel + ` FROM PUBLIC`,
	}
}

// LoginCapabilityPostgresTableBody is the ONE PostgreSQL column/constraint body. The
// migration creates the relation from it and the verifier calibrates the server's own
// deparse of the same body on a rolled-back TEMP probe, so constraint verification is
// exact whole-definition equality, never a substring match.
func LoginCapabilityPostgresTableBody() string {
	return `(
  capability_key pg_catalog.text COLLATE pg_catalog."C" NOT NULL,
  first_observed_at pg_catalog.timestamptz NOT NULL,
  last_observed_at pg_catalog.timestamptz NOT NULL,
  last_artifact_version pg_catalog.text COLLATE pg_catalog."C" NOT NULL,
  observation_count pg_catalog.int8 NOT NULL,
  CONSTRAINT login_capability_observation_pkey PRIMARY KEY (capability_key),
  CONSTRAINT login_capability_observation_key_check CHECK (capability_key OPERATOR(pg_catalog.=) '` + LoginCapabilityKey + `'),
  CONSTRAINT login_capability_observation_version_check CHECK (pg_catalog.length(last_artifact_version) OPERATOR(pg_catalog.>) 0),
  CONSTRAINT login_capability_observation_count_check CHECK (observation_count OPERATOR(pg_catalog.>) 0)
)`
}

// sqliteLoginCapabilityTimeGlob is the canonical UTC millisecond text produced by
// strftime('%Y-%m-%dT%H:%M:%fZ','now').
const sqliteLoginCapabilityTimeGlob = `'[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'`

// LoginCapabilityControlStmts renders the SQLite relation. Typeof checks refuse affinity
// drift, and the integer check refuses the REAL an overflowing increment would produce.
func (sqliteDialect) LoginCapabilityControlStmts() []string {
	return []string{
		`CREATE TABLE ` + LoginCapabilityObservationTable + ` (
  capability_key TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
  first_observed_at TEXT COLLATE BINARY NOT NULL,
  last_observed_at TEXT COLLATE BINARY NOT NULL,
  last_artifact_version TEXT COLLATE BINARY NOT NULL,
  observation_count INTEGER NOT NULL,
  CHECK (capability_key = '` + LoginCapabilityKey + `'),
  CHECK (typeof(first_observed_at) = 'text' AND first_observed_at GLOB ` + sqliteLoginCapabilityTimeGlob + `),
  CHECK (typeof(last_observed_at) = 'text' AND last_observed_at GLOB ` + sqliteLoginCapabilityTimeGlob + `),
  CHECK (typeof(last_artifact_version) = 'text' AND length(last_artifact_version) > 0),
  CHECK (typeof(observation_count) = 'integer' AND observation_count > 0)
)`,
	}
}
