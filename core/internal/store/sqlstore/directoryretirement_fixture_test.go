// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The current-schema retirement fixture.
//
// newDirectoryRetirementSQLiteHarness pays a complete real Open — every migration,
// guard rollout and verification — on an empty file for each of its callers. The
// default-config call sites of directoryretirement_test.go never test that transition:
// their subject starts on a current, empty store. This fixture builds that store ONCE
// per test process, keeps only the closed bytes of its main file, and gives every
// caller a private copy that still goes through the complete real Open.
//
// What it deliberately is NOT: not a cache of a Store, pool, connection, attestation
// or runtime witness (the second Open rebuilds all of those); not a persisted or
// prebuilt database (the seed is generated inside the first Test that asks for it, on
// the package clock); not a general fixture (only the retirement default sites opt
// in; explicit-DSN, clock and custom-config callers keep the configurable harness).

// currentSchemaRetirementSeedCache holds the seed bytes of one process. The zero value
// is ready to use; the package-level instance is the only one ordinary callers touch.
// The fixture's own tests construct disposable instances with a seam.
type currentSchemaRetirementSeedCache struct {
	seam currentSchemaSeedSeam
	once sync.Once
	seed []byte
	err  error
}

var currentSchemaRetirementSeed currentSchemaRetirementSeedCache

// currentSchemaSeedSeam is the private injection seam of the fixture's own tests. Every
// field is optional and the zero value is the ordinary path. It is not a store option,
// not a package hook and not reachable from production code.
type currentSchemaSeedSeam struct {
	// tempDir replaces os.MkdirTemp for the seed's scratch directory.
	tempDir func() (string, error)
	// beforeSnapshot runs on the open, validated seed store immediately before the
	// checkpoint. It is how a control commits WAL content or holds a reader.
	beforeSnapshot func(ctx context.Context, st *sqlStore, path string) error
	// closeStore replaces Store.Close for the seed store.
	closeStore func(store.Store) error
	// readFile replaces os.ReadFile for the closed main file.
	readFile func(string) ([]byte, error)
}

// bytes returns the seed, building it on the first call. The build runs inside
// sync.Once and reports through the error: it never calls into testing, never latches
// an empty success, and a failed build fails every later caller the same way. There
// is no fallback.
func (c *currentSchemaRetirementSeedCache) bytes(ctx context.Context) ([]byte, error) {
	c.once.Do(func() {
		seed, err := buildCurrentSchemaRetirementSeed(ctx, c.seam)
		if err != nil {
			c.seed, c.err = nil, err
			return
		}
		c.seed = seed
	})
	if c.err != nil {
		return nil, c.err
	}
	return c.seed, nil
}

const currentSchemaSeedFileName = "retirement-current-schema-seed.db"

// buildCurrentSchemaRetirementSeed runs the complete real Open on an empty file with
// the same configuration policy as the retirement harness (SQLite, Debug, nil
// registration, nothing else), validates the resulting estate, checkpoints the WAL
// completely, closes the store, verifies the teardown and returns the closed main
// file. Nothing is provisioned: no SYSTEM organization, no tenant, no activation.
func buildCurrentSchemaRetirementSeed(ctx context.Context, seam currentSchemaSeedSeam) ([]byte, error) {
	mkdir := seam.tempDir
	if mkdir == nil {
		mkdir = func() (string, error) { return os.MkdirTemp("", "retirement-current-schema-seed-") }
	}
	dir, err := mkdir()
	if err != nil {
		return nil, fmt.Errorf("current-schema seed: scratch directory: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // scratch directory; the bytes are already in memory
	path := filepath.Join(dir, currentSchemaSeedFileName)
	cfg := store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("current-schema seed: Open: %w", err)
	}
	st, ok := raw.(*sqlStore)
	if !ok {
		_ = raw.Close()
		return nil, fmt.Errorf("current-schema seed: Open returned %T, want *sqlStore", raw)
	}
	closed := false
	defer func() {
		if !closed {
			_ = raw.Close()
		}
	}()
	if err := verifyCurrentSchemaRetirementSeedState(ctx, st.db); err != nil {
		return nil, fmt.Errorf("current-schema seed: state: %w", err)
	}
	if seam.beforeSnapshot != nil {
		if err := seam.beforeSnapshot(ctx, st, path); err != nil {
			return nil, fmt.Errorf("current-schema seed: seam: %w", err)
		}
	}
	if err := checkpointCurrentSchemaSeed(ctx, st.db); err != nil {
		return nil, err
	}
	closeStore := seam.closeStore
	if closeStore == nil {
		closeStore = func(s store.Store) error { return s.Close() }
	}
	closed = true
	if err := closeStore(raw); err != nil {
		return nil, fmt.Errorf("current-schema seed: Close: %w", err)
	}
	if err := verifyCurrentSchemaSeedTeardown(path); err != nil {
		return nil, err
	}
	readFile := seam.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("current-schema seed: read closed main file: %w", err)
	}
	if err := verifyCurrentSchemaSeedImage(data); err != nil {
		return nil, err
	}
	return data, nil
}

// checkpointCurrentSchemaSeed runs the complete TRUNCATE checkpoint while the seed
// store still owns its single connection and no statement, rows or transaction is
// open. The journal mode is confirmed BEFORE the checkpoint, so that once TRUNCATE
// has completed nothing reads or writes the database again until Close. All three
// result columns are read: a busy checkpoint, a frame left in the log or an
// unexpected journal mode refuses publication instead of copying a main file whose
// latest pages are still in the WAL.
func checkpointCurrentSchemaSeed(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("current-schema seed: journal mode: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("current-schema seed: journal mode %q before checkpoint, want wal", mode)
	}
	var busy, logFrames, checkpointed int64
	if err := db.QueryRowContext(ctx, "PRAGMA main.wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("current-schema seed: checkpoint: %w", err)
	}
	if busy != 0 || logFrames != 0 || checkpointed != 0 {
		return fmt.Errorf("current-schema seed: checkpoint incomplete: busy=%d log=%d checkpointed=%d, want 0/0/0 after TRUNCATE",
			busy, logFrames, checkpointed)
	}
	return nil
}

// verifyCurrentSchemaSeedTeardown requires the closed main file to be a regular file
// with no nonempty WAL, no hot journal and no SHM sidecar left behind.
func verifyCurrentSchemaSeedTeardown(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("current-schema seed: closed main file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("current-schema seed: closed main file mode %v is not a regular file", info.Mode())
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar, err := os.Lstat(path + suffix)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("current-schema seed: sidecar %s: %w", suffix, err)
		}
		if suffix == "-wal" && sidecar.Size() == 0 {
			continue
		}
		return fmt.Errorf("current-schema seed: teardown left %s (%d bytes)", path+suffix, sidecar.Size())
	}
	return nil
}

// verifyCurrentSchemaSeedImage checks the closed bytes against SQLite's own header: the
// magic string, WAL read/write versions, and a database size (pages × page size, valid
// only when the change counter matches its version-valid-for stamp) equal to the file
// length. A truncated or partially written image cannot satisfy the last equality.
func verifyCurrentSchemaSeedImage(data []byte) error {
	const magic = "SQLite format 3\x00"
	if len(data) < 100 || string(data[:16]) != magic {
		return fmt.Errorf("current-schema seed: %d bytes are not a SQLite database image", len(data))
	}
	pageSize := int(binary.BigEndian.Uint16(data[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if data[18] != 2 || data[19] != 2 {
		return fmt.Errorf("current-schema seed: header versions %d/%d, want 2/2 (WAL)", data[18], data[19])
	}
	changeCounter := binary.BigEndian.Uint32(data[24:28])
	pages := int(binary.BigEndian.Uint32(data[28:32]))
	validFor := binary.BigEndian.Uint32(data[92:96])
	if changeCounter != validFor {
		return fmt.Errorf("current-schema seed: header size is stale (change counter %d, valid-for %d)", changeCounter, validFor)
	}
	if pages*pageSize != len(data) {
		return fmt.Errorf("current-schema seed: header declares %d pages × %d bytes = %d, file has %d bytes",
			pages, pageSize, pages*pageSize, len(data))
	}
	return nil
}

// currentSchemaSeedNonemptyTables is the census of every main table an empty real Open
// legitimately leaves with rows, each with the reason it is control or schema
// bookkeeping rather than business, authority or runtime state. The verifier is
// two-sided: every table listed here must be nonempty and every other table must be
// empty, so an addition on either side is a loud failure and not a silent drift.
var currentSchemaSeedNonemptyTables = map[string]string{
	dialect.ScopeTenantTable:            "the reserved SYSTEM scope pin that restoreDirectorySystemBaseline persists after boot; an empty pin is the privileged migration presentation, not the post-Open baseline (directoryepoch.go, dialect/sqlite.go)",
	dialect.DirectoryWriterControlTable: "the directory writer control singleton v7 creates and v10 rewrites: staged, generation 1, membership-union-v1 (userauthority_migration.go)",
	coreTrackingTable:                   "core migration tracking, one row per applied core version (core/migrate)",
	guardInventoryEventsTable:           "guard inventory activations appended by the v6 bootstrap and the later editions (guardledger.go appendInventoryEvent)",
	guardReceiptsTable:                  "bootstrap receipts of the guard control plane and the v7/v9 completion seals (guardplane.go bootstrapReceiptFor)",
	lineageControlTable:                 "the lineage control readiness singleton the v8 lineage migration inserts (lineage_migration.go)",
	auditSpoolUsageTable:                "the single zero-byte spool usage row the audit DDL seeds (dialect/sqlite.go AuditTableStmts)",
	dialect.AuditBlindingStateTable:     "the ledger's metadata-commitment default/actuation record ensured at boot (schema.go ensureAuditBlindingState)",
}

// currentSchemaSeedMustBeEmpty names the durable session-state tables the contract
// calls out by name, on top of the census. They are checked explicitly so a failure
// says which invariant broke.
var currentSchemaSeedMustBeEmpty = []string{
	dialect.DirectoryWriterMarkerTable,
	lineageWriterTable,
	lineageTouchedTable,
	lineageSeededTable,
}

// verifyCurrentSchemaRetirementSeedState is the estate oracle shared by the seed build
// and the fixture's tests: staged/generation 1/legacy coverage control, the reserved
// SYSTEM pin as the only scope row, empty writer and lineage session tables, no TEMP
// object, no attached database, and the two-sided table census above.
func verifyCurrentSchemaRetirementSeedState(ctx context.Context, db *sql.DB) error {
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		return errors.New("no SQLite dialect")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only inspection

	state, err := verifyDirectoryWriterControl(ctx, tx, dia)
	if err != nil {
		return fmt.Errorf("directory writer control: %w", err)
	}
	if state.Mode != directoryWriterStaged || state.ExpectedGeneration != 1 || state.CoverageProtocol != coverageProtocolLegacy {
		return fmt.Errorf("directory control = %+v, want staged/1/%s", state, coverageProtocolLegacy)
	}

	pins, err := currentSchemaColumnValues(ctx, tx, "SELECT tenant_id FROM main."+dialect.ScopeTenantTable)
	if err != nil {
		return err
	}
	if len(pins) != 1 || pins[0] != model.SystemTenantID.String() {
		return fmt.Errorf("scope pin rows = %q, want exactly the reserved SYSTEM pin", pins)
	}
	var spoolBytes, actuated int64
	if err := tx.QueryRowContext(ctx, "SELECT bytes FROM main."+auditSpoolUsageTable+" WHERE id = 1").Scan(&spoolBytes); err != nil {
		return fmt.Errorf("spool usage: %w", err)
	}
	if spoolBytes != 0 {
		return fmt.Errorf("spool usage is %d bytes, want the zero row the DDL seeds", spoolBytes)
	}
	// A ledger born after the blind column exists defaults to the blinded rule
	// (reconcileAuditBlindingState), and the first Open that follows that default
	// records the actuation before serving (resolveBlindingMode). Both facts are
	// written by the empty Open itself; neither is a row of evidence.
	var defaultBlinded int64
	if err := tx.QueryRowContext(ctx, "SELECT default_blinded, actuated FROM main."+dialect.AuditBlindingStateTable+" WHERE id = 1").Scan(&defaultBlinded, &actuated); err != nil {
		return fmt.Errorf("blinding state: %w", err)
	}
	if defaultBlinded != 1 || actuated != 1 {
		return fmt.Errorf("audit metadata blinding default=%d actuated=%d, want the fresh-ledger record 1/1", defaultBlinded, actuated)
	}
	for _, table := range currentSchemaSeedMustBeEmpty {
		var n int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM main."+quoteIdent(table)).Scan(&n); err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		if n != 0 {
			return fmt.Errorf("%s has %d rows, want an empty session table", table, n)
		}
	}

	databases, err := currentSchemaColumnValues(ctx, tx, "SELECT name FROM pragma_database_list ORDER BY seq")
	if err != nil {
		return err
	}
	for _, name := range databases {
		if name != "main" && name != "temp" {
			return fmt.Errorf("attached database %q present", name)
		}
	}
	var tempObjects int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM temp.sqlite_master").Scan(&tempObjects); err != nil {
		return fmt.Errorf("temp schema: %w", err)
	}
	if tempObjects != 0 {
		return fmt.Errorf("%d TEMP objects present", tempObjects)
	}

	tables, err := currentSchemaColumnValues(ctx, tx,
		"SELECT name FROM main.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return err
	}
	var problems []string
	seen := map[string]bool{}
	for _, table := range tables {
		seen[table] = true
		var n int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM main."+quoteIdent(table)).Scan(&n); err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		_, allowed := currentSchemaSeedNonemptyTables[table]
		switch {
		case allowed && n == 0:
			problems = append(problems, fmt.Sprintf("%s is empty but is censused as control bookkeeping", table))
		case !allowed && n != 0:
			problems = append(problems, fmt.Sprintf("%s has %d rows but is not censused as control bookkeeping", table, n))
		}
	}
	for table := range currentSchemaSeedNonemptyTables {
		if !seen[table] {
			problems = append(problems, fmt.Sprintf("censused table %s does not exist", table))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("table census: %s", strings.Join(problems, "; "))
	}
	return nil
}

func currentSchemaColumnValues(ctx context.Context, q dialect.Querier, query string) ([]string, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// writeCurrentSchemaRetirementCopy materializes one private destination: a new regular
// file, mode 0600, created exclusively under dir. It never links, never reuses a path
// and never hands out the seed slice.
func writeCurrentSchemaRetirementCopy(ctx context.Context, cache *currentSchemaRetirementSeedCache, dir string) (string, error) {
	seed, err := cache.bytes(ctx)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "directory-retirement.db")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sqliteFilePerm)
	if err != nil {
		return "", fmt.Errorf("current-schema copy: create %s: %w", path, err)
	}
	if _, err := f.Write(seed); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("current-schema copy: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("current-schema copy: close %s: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("current-schema copy: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != sqliteFilePerm || info.Size() != int64(len(seed)) {
		return "", fmt.Errorf("current-schema copy: %s is mode %v size %d, want a regular %#o file of %d bytes",
			path, info.Mode(), info.Size(), sqliteFilePerm, len(seed))
	}
	return path, nil
}

// openCurrentSchemaRetirementCopy writes a private copy and runs the complete real Open
// on it with the harness configuration policy. The returned config is the copy's real
// config, which is what h.enforce hands to the activation ceremony.
func openCurrentSchemaRetirementCopy(ctx context.Context, cache *currentSchemaRetirementSeedCache, dir string) (store.Store, store.Config, error) {
	path, err := writeCurrentSchemaRetirementCopy(ctx, cache, dir)
	if err != nil {
		return nil, store.Config{}, err
	}
	cfg := store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		return nil, cfg, fmt.Errorf("current-schema copy: Open %s: %w", path, err)
	}
	return raw, cfg, nil
}

// newCurrentSchemaDirectoryRetirementSQLiteHarness is the opt-in replacement for
// newDirectoryRetirementSQLiteHarness(t, store.Config{}). It accepts nothing but t on
// purpose: no config, DSN, clock, registration or seed override can reach it, so a
// caller cannot turn it into a second configurable harness. The sequence after Open
// is the original one — close registered first, then the SYSTEM witness provisioned —
// so activation (h.enforce) still runs the real ceremony on this copy.
func newCurrentSchemaDirectoryRetirementSQLiteHarness(t *testing.T) directoryRetirementSQLiteHarness {
	t.Helper()
	raw, cfg, err := openCurrentSchemaRetirementCopy(context.Background(), &currentSchemaRetirementSeed, t.TempDir())
	if err != nil {
		t.Fatalf("open current-schema retirement SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	retirementEnsureSystemTenant(t, raw)
	return directoryRetirementSQLiteHarness{raw: raw, sql: raw.(*sqlStore), cfg: cfg}
}

// verifyCurrentSchemaRetirementWitnesses requires the runtime state only a complete
// Open produces: registry, elector, dialect, the Debug guard and the immutable
// directory witness of an attested empty first boot awaiting SYSTEM genesis. A Store
// assembled around a copied file without Open has none of it.
func verifyCurrentSchemaRetirementWitnesses(st *sqlStore) error {
	if st == nil {
		return errors.New("nil store")
	}
	if st.engine != store.EngineSQLite || st.dia == nil || st.dia.Name() != store.EngineSQLite {
		return fmt.Errorf("engine %q / dialect %v, want SQLite", st.engine, st.dia)
	}
	if !st.debug {
		return errors.New("debug statement guard is off")
	}
	if st.reg == nil {
		return errors.New("entity registry absent")
	}
	if st.elector == nil {
		return errors.New("elector absent")
	}
	ds := st.directoryStatus
	if ds.Enabled || ds.ControlMode != store.DirectoryControlStaged || ds.ExpectedGeneration != 1 ||
		ds.CoverageProtocol != coverageProtocolLegacy || ds.InventoryUnavailableReason != "system_bootstrap_pending" ||
		ds.InventoryOrgCount != 0 || ds.InventoryBusinessOrgCount != 0 || ds.InventoryEpochCount != 0 {
		return fmt.Errorf("directory witness %+v is not the staged, bootstrap-pending first-boot witness", ds)
	}
	return nil
}

// checkCurrentSchemaCopyIsolation is the assertion the shared-destination oracle must
// trip: two copies are isolated only if they are different files and an organization
// committed in a is invisible to b.
func checkCurrentSchemaCopyIsolation(ctx context.Context, a, b *sqlStore, aPath, bPath string, planted model.TenantID) error {
	ai, err := os.Stat(aPath)
	if err != nil {
		return err
	}
	bi, err := os.Stat(bPath)
	if err != nil {
		return err
	}
	if os.SameFile(ai, bi) {
		return fmt.Errorf("copies share one destination file (%s, %s)", aPath, bPath)
	}
	var inA, inB int64
	if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM main.orgs WHERE id = ?", planted.String()).Scan(&inA); err != nil {
		return err
	}
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM main.orgs WHERE id = ?", planted.String()).Scan(&inB); err != nil {
		return err
	}
	if inA != 1 {
		return fmt.Errorf("planted organization %s is not in its own copy (%d rows)", planted, inA)
	}
	if inB != 0 {
		return fmt.Errorf("planted organization %s of one copy is visible in the other (%d rows)", planted, inB)
	}
	return nil
}

// ---- logical census ------------------------------------------------------------

// currentSchemaCensus is a complete logical picture of one connection's main
// database: every schema object with its stored SQL, every table's row count, the
// rows of every censused nonempty table, settings pragmas, attached databases and
// TEMP objects.
type currentSchemaCensus struct {
	schema      []string
	counts      map[string]int64
	rows        map[string][][2]string // table -> (column, value) pairs flattened per row
	columns     map[string][]string
	pragmas     map[string]string
	databases   []string
	tempObjects int64
}

var currentSchemaCensusPragmas = []string{
	"journal_mode", "foreign_keys", "recursive_triggers", "busy_timeout", "page_size",
	"user_version", "application_id", "auto_vacuum", "encoding", "page_count", "freelist_count",
}

func takeCurrentSchemaCensus(ctx context.Context, db *sql.DB) (currentSchemaCensus, error) {
	c := currentSchemaCensus{
		counts: map[string]int64{}, rows: map[string][][2]string{}, columns: map[string][]string{}, pragmas: map[string]string{},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only inspection
	rows, err := tx.QueryContext(ctx, "SELECT type, name, tbl_name, COALESCE(sql, '') FROM main.sqlite_master ORDER BY type, name")
	if err != nil {
		return c, err
	}
	if c.schema, err = currentSchemaReadSchemaRows(rows); err != nil {
		return c, err
	}
	tables, err := currentSchemaColumnValues(ctx, tx,
		"SELECT name FROM main.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return c, err
	}
	for _, table := range tables {
		var n int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM main."+quoteIdent(table)).Scan(&n); err != nil {
			return c, err
		}
		c.counts[table] = n
		if n == 0 {
			continue
		}
		columns, tableRows, err := currentSchemaTableRows(ctx, tx, table)
		if err != nil {
			return c, err
		}
		c.columns[table] = columns
		c.rows[table] = tableRows
	}
	for _, pragma := range currentSchemaCensusPragmas {
		var v any
		if err := tx.QueryRowContext(ctx, "PRAGMA main."+pragma).Scan(&v); err != nil {
			return c, fmt.Errorf("pragma %s: %w", pragma, err)
		}
		c.pragmas[pragma] = currentSchemaCensusValue(v)
	}
	if c.databases, err = currentSchemaColumnValues(ctx, tx, "SELECT name FROM pragma_database_list ORDER BY seq"); err != nil {
		return c, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM temp.sqlite_master").Scan(&c.tempObjects); err != nil {
		return c, err
	}
	return c, nil
}

// currentSchemaReadSchemaRows drains one sqlite_master result set. The iterator's own
// error is checked after Next and the rows are closed on every exit, with the close
// error reported when nothing else failed: a partial schema is never accepted as a
// census. It returns before the caller issues its next statement on the transaction.
func currentSchemaReadSchemaRows(rows *sql.Rows) (schema []string, err error) {
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			schema, err = nil, cerr
		}
	}()
	for rows.Next() {
		var typ, name, tbl, sqlText string
		if serr := rows.Scan(&typ, &name, &tbl, &sqlText); serr != nil {
			return nil, serr
		}
		schema = append(schema, typ+"|"+name+"|"+tbl+"|"+sqlText)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	return schema, nil
}

func currentSchemaTableRows(ctx context.Context, tx *sql.Tx, table string) ([]string, [][2]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT * FROM main."+quoteIdent(table))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][2]string
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		for i, col := range columns {
			out = append(out, [2]string{col, currentSchemaCensusValue(values[i])})
		}
		out = append(out, [2]string{"", "\x00row-end"})
	}
	return columns, out, rows.Err()
}

func currentSchemaCensusValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return "x'" + hex.EncodeToString(x) + "'"
	case string:
		return fmt.Sprintf("%q", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// currentSchemaRowKeys renders each row of one censused table as an order-independent
// key, substituting a placeholder for every column named in variable. It reports the
// names of variable columns it never saw, so a stale allowance is a failure too.
func currentSchemaRowKeys(rows [][2]string, variable map[string]bool) ([]string, map[string]bool) {
	unseen := map[string]bool{}
	for col := range variable {
		unseen[col] = true
	}
	var keys []string
	var current []string
	for _, pair := range rows {
		if pair[0] == "" && pair[1] == "\x00row-end" {
			keys = append(keys, strings.Join(current, "\x1f"))
			current = nil
			continue
		}
		value := pair[1]
		if variable[pair[0]] {
			value = "<variable>"
			delete(unseen, pair[0])
		}
		current = append(current, pair[0]+"="+value)
	}
	sort.Strings(keys)
	return keys, unseen
}

// compareCurrentSchemaCensus lists every logical difference between two censuses,
// tolerating only the named variable columns of named tables. It returns nil when the
// two are the same estate.
func compareCurrentSchemaCensus(label string, want, got currentSchemaCensus, variable map[string]map[string]bool) []string {
	var diffs []string
	if strings.Join(want.schema, "\n") != strings.Join(got.schema, "\n") {
		diffs = append(diffs, label+": schema objects differ:\n"+currentSchemaLineDiff(want.schema, got.schema))
	}
	tables := map[string]bool{}
	for t := range want.counts {
		tables[t] = true
	}
	for t := range got.counts {
		tables[t] = true
	}
	var names []string
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, table := range names {
		if want.counts[table] != got.counts[table] {
			diffs = append(diffs, fmt.Sprintf("%s: %s has %d rows, want %d", label, table, got.counts[table], want.counts[table]))
			continue
		}
		if want.counts[table] == 0 {
			continue
		}
		if strings.Join(want.columns[table], ",") != strings.Join(got.columns[table], ",") {
			diffs = append(diffs, fmt.Sprintf("%s: %s columns %v, want %v", label, table, got.columns[table], want.columns[table]))
			continue
		}
		wantKeys, unseenWant := currentSchemaRowKeys(want.rows[table], variable[table])
		gotKeys, _ := currentSchemaRowKeys(got.rows[table], variable[table])
		for col := range unseenWant {
			diffs = append(diffs, fmt.Sprintf("%s: %s allows variable column %q that does not exist", label, table, col))
		}
		if strings.Join(wantKeys, "\n") != strings.Join(gotKeys, "\n") {
			diffs = append(diffs, fmt.Sprintf("%s: %s rows differ:\n%s", label, table, currentSchemaLineDiff(wantKeys, gotKeys)))
		}
	}
	var pragmas []string
	for p := range want.pragmas {
		pragmas = append(pragmas, p)
	}
	sort.Strings(pragmas)
	for _, p := range pragmas {
		if want.pragmas[p] != got.pragmas[p] {
			diffs = append(diffs, fmt.Sprintf("%s: pragma %s = %s, want %s", label, p, got.pragmas[p], want.pragmas[p]))
		}
	}
	// pragma_database_list reports "temp" only once a connection has used its TEMP
	// schema; that is per-connection presentation, not file content. Attached
	// databases are the invariant.
	if strings.Join(currentSchemaAttached(want.databases), ",") != strings.Join(currentSchemaAttached(got.databases), ",") {
		diffs = append(diffs, fmt.Sprintf("%s: databases %v, want %v", label, got.databases, want.databases))
	}
	if want.tempObjects != got.tempObjects {
		diffs = append(diffs, fmt.Sprintf("%s: %d TEMP objects, want %d", label, got.tempObjects, want.tempObjects))
	}
	return diffs
}

func currentSchemaLineDiff(want, got []string) string {
	wantSet := map[string]bool{}
	for _, l := range want {
		wantSet[l] = true
	}
	gotSet := map[string]bool{}
	for _, l := range got {
		gotSet[l] = true
	}
	var out []string
	for _, l := range want {
		if !gotSet[l] {
			out = append(out, "  - "+l)
		}
	}
	for _, l := range got {
		if !wantSet[l] {
			out = append(out, "  + "+l)
		}
	}
	const limit = 40
	if len(out) > limit {
		out = append(out[:limit], fmt.Sprintf("  … %d more", len(out)-limit))
	}
	return strings.Join(out, "\n")
}

// currentSchemaVariableColumns names, per censused table, the only columns allowed to
// differ between two independent empty real Opens. Each entry cites why the contract
// of that column permits it. Nothing else is normalized.
var currentSchemaVariableColumns = map[string]map[string]bool{
	// migrate stamps the wall clock when it applies a version.
	coreTrackingTable: {"applied_at": true},
	// guardReceipt.bodyDigest and chainDigest exclude applied_at by design: the same
	// attribution must produce the same receipt id on a retry (guardledger.go).
	guardReceiptsTable: {"applied_at": true},
	// inventoryEvent.chainDigest DOES cover recorded_at (guardledger.go), so the event
	// digest and every successor's predecessor link are functions of the wall clock.
	// They are not compared by equality; verifyCurrentSchemaInventoryChain recomputes
	// the whole chain with the production digest instead.
	guardInventoryEventsTable: {"recorded_at": true, "event_sha256": true, "prev_event_sha256": true},
}

// ---- helpers for the controls -------------------------------------------------

func currentSchemaTestOpenRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openSQLite(path)
	if err != nil {
		t.Fatalf("open raw SQLite handle on %s: %v", path, err)
	}
	return db
}

func currentSchemaTestCloseRaw(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close raw SQLite handle: %v", err)
	}
}

// currentSchemaTestCensusOfBytes census the seed's closed bytes through a raw handle on
// a private copy that is never opened by the store.
func currentSchemaTestCensusOfBytes(t *testing.T, cache *currentSchemaRetirementSeedCache) currentSchemaCensus {
	t.Helper()
	path, err := writeCurrentSchemaRetirementCopy(context.Background(), cache, t.TempDir())
	if err != nil {
		t.Fatalf("write seed copy for raw census: %v", err)
	}
	db := currentSchemaTestOpenRaw(t, path)
	defer currentSchemaTestCloseRaw(t, db)
	if err := verifyCurrentSchemaRetirementSeedState(context.Background(), db); err != nil {
		t.Fatalf("seed bytes state: %v", err)
	}
	c, err := takeCurrentSchemaCensus(context.Background(), db)
	if err != nil {
		t.Fatalf("census of seed bytes: %v", err)
	}
	return c
}

func currentSchemaAttached(databases []string) []string {
	var out []string
	for _, name := range databases {
		if name != "temp" {
			out = append(out, name)
		}
	}
	return out
}

// verifyCurrentSchemaInventoryChain recomputes the guard inventory stream with the
// production chain digest: contiguous ordinals from 1, each predecessor link equal to
// the previous digest, each stored digest equal to its recomputation. This is the
// oracle for the three inventory columns the parity comparison cannot equate.
func verifyCurrentSchemaInventoryChain(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT event_sha256, event_ordinal, prev_event_sha256, kind,
  relation_schema, relation_name, trigger_name, producer,
  manifest_format, code_epoch, definition_sha256, spec_sha256,
  desired_enable_state, legacy_allowed_states,
  retained_revision, retained_sha256, recorded_at
FROM main.`+guardInventoryEventsTable+` ORDER BY event_ordinal`)
	if err != nil {
		return fmt.Errorf("inventory chain: %w", err)
	}
	defer rows.Close()
	var prev optDigest
	var expected int64 = 1
	seen := 0
	for rows.Next() {
		var ev inventoryEvent
		var eventRaw, prevRaw, definitionRaw, specRaw, retainedRaw []byte
		var kind, producer, states string
		if err := rows.Scan(&eventRaw, &ev.EventOrdinal, &prevRaw, &kind,
			&ev.Key.Schema, &ev.Key.Relation, &ev.Key.Trigger, &producer,
			&ev.Format, &ev.CodeEpoch, &definitionRaw, &specRaw,
			&ev.DesiredEnableState, &states,
			&ev.RetainedRevision, &retainedRaw, &ev.RecordedAt); err != nil {
			return fmt.Errorf("inventory chain: %w", err)
		}
		ev.Kind, ev.Producer = inventoryEventKind(kind), guardProducer(producer)
		ev.LegacyAllowedStates = decodeUnitList(states)
		if ev.EventSHA256, err = scanDigest(eventRaw, "an inventory event digest"); err != nil {
			return err
		}
		if prevRaw != nil {
			d, err := scanDigest(prevRaw, "an inventory predecessor digest")
			if err != nil {
				return err
			}
			ev.PrevEventSHA256 = someDigest(d)
		}
		if ev.DefinitionSHA256, err = scanDigest(definitionRaw, "a guard definition digest"); err != nil {
			return err
		}
		if ev.SpecSHA256, err = scanDigest(specRaw, "a guard spec digest"); err != nil {
			return err
		}
		if ev.RetainedSHA256, err = scanDigest(retainedRaw, "a guard retained digest"); err != nil {
			return err
		}
		if ev.EventOrdinal != expected {
			return fmt.Errorf("inventory chain jumps from ordinal %d to %d", expected-1, ev.EventOrdinal)
		}
		if ev.PrevEventSHA256 != prev {
			return fmt.Errorf("inventory event %d records predecessor %s, but its predecessor hashes to %s", ev.EventOrdinal, ev.PrevEventSHA256, prev)
		}
		sum, err := ev.chainDigest()
		if err != nil {
			return err
		}
		if sum != ev.EventSHA256 {
			return fmt.Errorf("inventory event %d stores digest %s but hashes to %s", ev.EventOrdinal, hexDigest(ev.EventSHA256), hexDigest(sum))
		}
		prev = someDigest(ev.EventSHA256)
		expected++
		seen++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen == 0 {
		return errors.New("inventory chain is empty")
	}
	return nil
}

// verifyCurrentSchemaReceiptHistory runs the production receipt-stream verifier over
// every rollout present, so a copy's history is valid on the same terms Open uses.
func verifyCurrentSchemaReceiptHistory(ctx context.Context, db *sql.DB) error {
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		return errors.New("no SQLite dialect")
	}
	rollouts, err := currentSchemaColumnValues(ctx, db, "SELECT DISTINCT rollout_id FROM main."+guardReceiptsTable+" ORDER BY rollout_id")
	if err != nil {
		return err
	}
	if len(rollouts) == 0 {
		return errors.New("no guard receipts")
	}
	for _, rollout := range rollouts {
		if _, err := guardRolloutReceipts(ctx, db, dia, rollout); err != nil {
			return err
		}
	}
	return nil
}

// ---- tests of the fixture ------------------------------------------------------

func currentSchemaTestCopyConfig(path string) store.Config {
	return store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true}
}

// currentSchemaTestOpenCopy opens a private copy WITHOUT provisioning SYSTEM, so a test
// can observe the exact seed estate before its own setup. The returned close is
// idempotent and also registered as cleanup.
func currentSchemaTestOpenCopy(t *testing.T, cache *currentSchemaRetirementSeedCache) (directoryRetirementSQLiteHarness, func()) {
	t.Helper()
	raw, cfg, err := openCurrentSchemaRetirementCopy(context.Background(), cache, t.TempDir())
	if err != nil {
		t.Fatalf("open current-schema copy: %v", err)
	}
	var once sync.Once
	closeFn := func() { once.Do(func() { _ = raw.Close() }) }
	t.Cleanup(closeFn)
	return directoryRetirementSQLiteHarness{raw: raw, sql: raw.(*sqlStore), cfg: cfg}, closeFn
}

func currentSchemaTestMustExec(t *testing.T, exec directoryWriterTestExecer, query string, args ...any) {
	t.Helper()
	directoryWriterTestMustExec(t, exec, query, args...)
}

func TestCurrentSchemaRetirementFixtureParityWithFreshOpen(t *testing.T) {
	ctx := context.Background()

	// a: a fresh real Open on an empty file, censused through its own pool.
	freshPath := filepath.Join(t.TempDir(), "fresh.db")
	freshRaw, err := Open(ctx, currentSchemaTestCopyConfig(freshPath), nil)
	if err != nil {
		t.Fatalf("fresh Open: %v", err)
	}
	t.Cleanup(func() { _ = freshRaw.Close() })
	fresh := freshRaw.(*sqlStore)
	if err := verifyCurrentSchemaRetirementSeedState(ctx, fresh.db); err != nil {
		t.Fatalf("fresh state: %v", err)
	}
	if err := verifyCurrentSchemaRetirementWitnesses(fresh); err != nil {
		t.Fatalf("fresh witnesses: %v", err)
	}
	freshCensus, err := takeCurrentSchemaCensus(ctx, fresh.db)
	if err != nil {
		t.Fatalf("fresh census: %v", err)
	}

	// b: the cached seed bytes, read through a raw handle on a copy the store never opens.
	bytesCensus := currentSchemaTestCensusOfBytes(t, &currentSchemaRetirementSeed)

	// c: a private copy after the complete Open, before any SYSTEM provisioning.
	copyH, _ := currentSchemaTestOpenCopy(t, &currentSchemaRetirementSeed)
	if err := verifyCurrentSchemaRetirementSeedState(ctx, copyH.sql.db); err != nil {
		t.Fatalf("copy state after Open: %v", err)
	}
	if err := verifyCurrentSchemaRetirementWitnesses(copyH.sql); err != nil {
		t.Fatalf("copy witnesses: %v", err)
	}
	if err := verifyCurrentSchemaInventoryChain(ctx, copyH.sql.db); err != nil {
		t.Fatalf("copy inventory chain: %v", err)
	}
	if err := verifyCurrentSchemaReceiptHistory(ctx, copyH.sql.db); err != nil {
		t.Fatalf("copy receipt history: %v", err)
	}
	copyCensus, err := takeCurrentSchemaCensus(ctx, copyH.sql.db)
	if err != nil {
		t.Fatalf("copy census: %v", err)
	}

	var diffs []string
	diffs = append(diffs, compareCurrentSchemaCensus("seed bytes vs fresh Open", freshCensus, bytesCensus, currentSchemaVariableColumns)...)
	diffs = append(diffs, compareCurrentSchemaCensus("opened copy vs fresh Open", freshCensus, copyCensus, currentSchemaVariableColumns)...)
	// The copy is the seed's bytes: after its Open nothing may differ at all.
	diffs = append(diffs, compareCurrentSchemaCensus("opened copy vs seed bytes", bytesCensus, copyCensus, nil)...)
	if len(diffs) > 0 {
		t.Fatalf("parity:\n%s", strings.Join(diffs, "\n"))
	}
	for table := range currentSchemaSeedNonemptyTables {
		if copyCensus.counts[table] == 0 {
			t.Fatalf("censused table %s is empty in the copy", table)
		}
	}
	t.Logf("seed image: %s pages of %s bytes, %d schema objects, %d tables, nonempty %d",
		bytesCensus.pragmas["page_count"], bytesCensus.pragmas["page_size"], len(bytesCensus.schema), len(bytesCensus.counts), len(currentSchemaSeedNonemptyTables))

	// The destination is a private regular 0600 file, distinct from every other copy.
	info, err := os.Lstat(copyH.cfg.DSN)
	if err != nil {
		t.Fatalf("copy destination %s: %v", copyH.cfg.DSN, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != sqliteFilePerm {
		t.Fatalf("copy destination %s: mode %v", copyH.cfg.DSN, info.Mode())
	}
	if strings.ContainsAny(copyH.cfg.DSN, "?&") || copyH.cfg.Engine != store.EngineSQLite || !copyH.cfg.Debug {
		t.Fatalf("copy config %+v is not a plain-file Debug SQLite config", copyH.cfg)
	}
	second, _ := currentSchemaTestOpenCopy(t, &currentSchemaRetirementSeed)
	secondInfo, err := os.Stat(second.cfg.DSN)
	if err != nil || os.SameFile(info, secondInfo) {
		t.Fatalf("two copies share a destination: %s %s (%v)", copyH.cfg.DSN, second.cfg.DSN, err)
	}

	// Activation on a copy is still the real ceremony: SYSTEM provisioned locally,
	// then changed=true, enforced, generation 2. Nothing of that is cached: the second
	// copy is still exactly the staged seed afterwards.
	retirementEnsureSystemTenant(t, copyH.raw)
	copyH.enforce(t)
	if err := verifyCurrentSchemaRetirementSeedState(ctx, second.sql.db); err != nil {
		t.Fatalf("second copy after the first was activated: %v", err)
	}
}

func TestCurrentSchemaRetirementFixtureSnapshotControls(t *testing.T) {
	ctx := context.Background()

	t.Run("committed WAL content is preserved", func(t *testing.T) {
		var walBefore int64
		cache := &currentSchemaRetirementSeedCache{seam: currentSchemaSeedSeam{
			beforeSnapshot: func(ctx context.Context, st *sqlStore, path string) error {
				if _, err := st.db.ExecContext(ctx, "CREATE TABLE seed_wal_witness (marker TEXT NOT NULL)"); err != nil {
					return err
				}
				if _, err := st.db.ExecContext(ctx, "INSERT INTO seed_wal_witness (marker) VALUES ('committed-before-checkpoint')"); err != nil {
					return err
				}
				info, err := os.Stat(path + "-wal")
				if err != nil {
					return err
				}
				walBefore = info.Size()
				return nil
			},
		}}
		path, err := writeCurrentSchemaRetirementCopy(ctx, cache, t.TempDir())
		if err != nil {
			t.Fatalf("seed with committed WAL content: %v", err)
		}
		if walBefore == 0 {
			t.Fatal("control did not observe a nonempty WAL before the checkpoint")
		}
		db := currentSchemaTestOpenRaw(t, path)
		defer currentSchemaTestCloseRaw(t, db)
		var marker string
		if err := db.QueryRowContext(ctx, "SELECT marker FROM main.seed_wal_witness").Scan(&marker); err != nil || marker != "committed-before-checkpoint" {
			t.Fatalf("WAL content after checkpoint: marker=%q err=%v (WAL was %d bytes)", marker, err, walBefore)
		}
	})

	t.Run("held reader refuses publication", func(t *testing.T) {
		var reader *sql.DB
		var readerTx *sql.Tx
		release := func() {
			if readerTx != nil {
				_ = readerTx.Rollback()
				readerTx = nil
			}
			if reader != nil {
				_ = reader.Close()
				reader = nil
			}
		}
		t.Cleanup(release)
		cache := &currentSchemaRetirementSeedCache{seam: currentSchemaSeedSeam{
			beforeSnapshot: func(ctx context.Context, st *sqlStore, path string) error {
				var err error
				if reader, err = openSQLite(path); err != nil {
					return err
				}
				if readerTx, err = reader.BeginTx(ctx, nil); err != nil {
					return err
				}
				var n int
				if err := readerTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM main.sqlite_master").Scan(&n); err != nil {
					return err
				}
				// Frames committed past the reader's snapshot cannot be backfilled
				// while it lives; a complete TRUNCATE is impossible.
				_, err = st.db.ExecContext(ctx, "CREATE TABLE seed_reader_witness (marker TEXT NOT NULL)")
				return err
			},
		}}
		_, err := cache.bytes(ctx)
		if err == nil || !strings.Contains(err.Error(), "checkpoint incomplete") {
			t.Fatalf("held reader: err = %v, want the checkpoint refusal", err)
		}
		release()
		if _, again := cache.bytes(ctx); again == nil || again.Error() != err.Error() {
			t.Fatalf("after releasing the reader the cache answered %v, want the same latched refusal", again)
		}
	})

	t.Run("open failure publishes nothing and latches", func(t *testing.T) {
		var attempts atomic.Int64
		missing := filepath.Join(t.TempDir(), "missing", "nested")
		cache := &currentSchemaRetirementSeedCache{seam: currentSchemaSeedSeam{
			tempDir: func() (string, error) { attempts.Add(1); return missing, nil },
		}}
		_, err := cache.bytes(ctx)
		if err == nil || !strings.Contains(err.Error(), "Open") {
			t.Fatalf("open failure: err = %v", err)
		}
		if _, again := cache.bytes(ctx); again == nil || again.Error() != err.Error() {
			t.Fatalf("second call: %v, want the same error", again)
		}
		if got := attempts.Load(); got != 1 {
			t.Fatalf("initializer ran %d times, want exactly once", got)
		}
		if _, err := writeCurrentSchemaRetirementCopy(ctx, cache, t.TempDir()); err == nil {
			t.Fatal("a copy was written from a failed seed")
		}
	})

	t.Run("read failure publishes nothing", func(t *testing.T) {
		cache := &currentSchemaRetirementSeedCache{seam: currentSchemaSeedSeam{
			readFile: func(string) ([]byte, error) { return nil, errors.New("injected read failure") },
		}}
		if _, err := cache.bytes(ctx); err == nil || !strings.Contains(err.Error(), "injected read failure") {
			t.Fatalf("read failure: err = %v", err)
		}
	})

	t.Run("close failure publishes nothing", func(t *testing.T) {
		cache := &currentSchemaRetirementSeedCache{seam: currentSchemaSeedSeam{
			closeStore: func(s store.Store) error {
				_ = s.Close()
				return errors.New("injected close failure")
			},
		}}
		if _, err := cache.bytes(ctx); err == nil || !strings.Contains(err.Error(), "injected close failure") {
			t.Fatalf("close failure: err = %v", err)
		}
	})

	t.Run("incomplete image is refused", func(t *testing.T) {
		cache := &currentSchemaRetirementSeedCache{seam: currentSchemaSeedSeam{
			readFile: func(path string) ([]byte, error) {
				data, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				return data[:len(data)-4096], nil
			},
		}}
		if _, err := cache.bytes(ctx); err == nil || !strings.Contains(err.Error(), "header declares") {
			t.Fatalf("truncated image: err = %v", err)
		}
	})

	t.Run("existing destination is refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "directory-retirement.db"), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := writeCurrentSchemaRetirementCopy(ctx, &currentSchemaRetirementSeed, dir); err == nil || !errors.Is(err, fs.ErrExist) {
			t.Fatalf("existing destination: err = %v, want ErrExist", err)
		}
	})
}

func TestCurrentSchemaRetirementFixtureInitializesOnceConcurrently(t *testing.T) {
	ctx := context.Background()
	var builds atomic.Int64
	cache := &currentSchemaRetirementSeedCache{seam: currentSchemaSeedSeam{
		beforeSnapshot: func(context.Context, *sqlStore, string) error { builds.Add(1); return nil },
	}}
	const callers = 4
	dirs := make([]string, callers)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	type outcome struct {
		raw store.Store
		cfg store.Config
		err error
	}
	outcomes := make([]outcome, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			raw, cfg, err := openCurrentSchemaRetirementCopy(ctx, cache, dirs[i])
			outcomes[i] = outcome{raw: raw, cfg: cfg, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	// Register every opened copy before an error from any caller can end the test.
	for _, o := range outcomes {
		if o.raw != nil {
			t.Cleanup(func() { _ = o.raw.Close() })
		}
	}
	for i, o := range outcomes {
		if o.err != nil {
			t.Fatalf("caller %d: %v", i, o.err)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("seed built %d times under %d concurrent callers, want 1", got, callers)
	}
	for i := range outcomes {
		st := outcomes[i].raw.(*sqlStore)
		if err := verifyCurrentSchemaRetirementWitnesses(st); err != nil {
			t.Fatalf("caller %d witnesses: %v", i, err)
		}
		if err := verifyCurrentSchemaRetirementSeedState(ctx, st.db); err != nil {
			t.Fatalf("caller %d state: %v", i, err)
		}
		for j := range outcomes[:i] {
			a, err := os.Stat(outcomes[i].cfg.DSN)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.Stat(outcomes[j].cfg.DSN)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(a, b) {
				t.Fatalf("callers %d and %d share %s", i, j, outcomes[i].cfg.DSN)
			}
		}
	}
}

func TestCurrentSchemaRetirementFixtureIsolation(t *testing.T) {
	t.Run("first copy holds while the second operates", func(t *testing.T) {
		exerciseCurrentSchemaIsolation(t, true)
	})
	t.Run("second copy holds while the first operates", func(t *testing.T) {
		exerciseCurrentSchemaIsolation(t, false)
	})
	t.Run("shared destination is detected", func(t *testing.T) {
		ctx := context.Background()
		a := newCurrentSchemaDirectoryRetirementSQLiteHarness(t)
		planted := provisionTenant(t, a.raw, "shared-destination")
		// The reuse the helper refuses, done without it: a second complete Open on
		// the same file.
		bRaw, err := Open(ctx, currentSchemaTestCopyConfig(a.cfg.DSN), nil)
		if err != nil {
			t.Fatalf("open shared destination: %v", err)
		}
		t.Cleanup(func() { _ = bRaw.Close() })
		err = checkCurrentSchemaCopyIsolation(ctx, a.sql, bRaw.(*sqlStore), a.cfg.DSN, a.cfg.DSN, planted)
		if err == nil || !strings.Contains(err.Error(), "share one destination") {
			t.Fatalf("shared destination passed the isolation assertion: %v", err)
		}
		// The data leg alone, with the file identity taken out of the picture,
		// still trips on the visible organization.
		var visible int64
		if err := bRaw.(*sqlStore).db.QueryRowContext(ctx, "SELECT COUNT(*) FROM main.orgs WHERE id = ?", planted.String()).Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if visible != 1 {
			t.Fatalf("shared file hides the planted organization (%d rows); the oracle is not discriminating", visible)
		}
		// Positive control: two private copies pass.
		c := newCurrentSchemaDirectoryRetirementSQLiteHarness(t)
		if err := checkCurrentSchemaCopyIsolation(ctx, a.sql, c.sql, a.cfg.DSN, c.cfg.DSN, planted); err != nil {
			t.Fatalf("private copies failed the isolation assertion: %v", err)
		}
	})
}

// exerciseCurrentSchemaIsolation opens two copies and lets one build an estate and
// hold a pinned, armed, TEMP-bearing transaction while the other proves it is still
// exactly the seed, sets itself up, refuses unarmed raw writes and then activates and
// retires on its own after the holder rolled back and closed. holderFirst selects
// which of the two opened copies plays the holder.
func exerciseCurrentSchemaIsolation(t *testing.T, holderFirst bool) {
	t.Helper()
	ctx := context.Background()
	first, closeFirst := currentSchemaTestOpenCopy(t, &currentSchemaRetirementSeed)
	second, closeSecond := currentSchemaTestOpenCopy(t, &currentSchemaRetirementSeed)
	holder, other, closeHolder := first, second, closeFirst
	if !holderFirst {
		holder, other, closeHolder = second, first, closeSecond
	}
	hi, err := os.Stat(holder.cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	oi, err := os.Stat(other.cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(hi, oi) {
		t.Fatalf("copies share a file: %s %s", holder.cfg.DSN, other.cfg.DSN)
	}

	// The holder builds an estate: SYSTEM, a tenant, a User, an Identity, activation and
	// a definitive retirement with its tombstone and audit rows.
	retirementEnsureSystemTenant(t, holder.raw)
	tenant := provisionTenant(t, holder.raw, "isolation-holder")
	user := retirementCreateUser(t, holder.raw, "isolation-holder-user")
	identity := retirementCreateIdentity(t, holder.raw, tenant, "isolation-holder-identity")
	holder.enforce(t)
	result, err := RetireDirectoryPrincipal(ctx, holder.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if err != nil || !result.Definitive {
		t.Fatalf("holder retirement = %+v err=%v", result, err)
	}
	if err := verifyCurrentSchemaRetirementSeedState(ctx, other.sql.db); err != nil {
		t.Fatalf("other copy is no longer the seed after the holder's estate: %v", err)
	}

	// The holder now holds: tenant pin, writer marker, lineage writer presentation and
	// a TEMP object, all on its single connection, uncommitted.
	held, err := holder.sql.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Safety net for a run that fails while the transaction is held: it releases the
	// holder's single connection so the cleanups can close the store. The normal path
	// below rolls back explicitly and checks it, so ErrTxDone here is the expected
	// state and is not reported; any other error is added without replacing the
	// failure that ended the run.
	defer func() {
		if err := held.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("rollback of the held transaction: %v", err)
		}
	}()
	currentSchemaTestMustExec(t, held, "DELETE FROM main."+dialect.ScopeTenantTable)
	currentSchemaTestMustExec(t, held, "INSERT INTO main."+dialect.ScopeTenantTable+"(tenant_id) VALUES (?)", tenant.String())
	currentSchemaTestMustExec(t, held, "INSERT INTO main."+dialect.DirectoryWriterMarkerTable+"(control_key, generation, coverage_protocol) VALUES (?, ?, ?)",
		directoryWriterLockKey, int64(2), coverageProtocolLegacy)
	currentSchemaTestMustExec(t, held, "INSERT INTO main."+lineageWriterTable+"(writer_id, tenant_id) VALUES ('isolation-hold', ?)", tenant.String())
	currentSchemaTestMustExec(t, held, "CREATE TEMP TABLE isolation_hold (marker TEXT NOT NULL)")

	// While held: the other copy is still the seed, sets itself up, and its own
	// lineage guard refuses an unarmed raw write.
	if err := verifyCurrentSchemaRetirementSeedState(ctx, other.sql.db); err != nil {
		t.Fatalf("other copy baseline while the holder holds: %v", err)
	}
	retirementEnsureSystemTenant(t, other.raw)
	otherTenant := provisionTenant(t, other.raw, "isolation-other")
	otherIdentity := retirementCreateIdentity(t, other.raw, otherTenant, "isolation-other-identity")
	otherSibling := retirementCreateAgent(t, other.raw, otherTenant, otherIdentity.ID, "", "isolation-other-sibling", model.StatusActive)
	_, err = other.sql.db.ExecContext(ctx,
		"UPDATE main.agents SET workspace_id = 'not-a-canonical-workspace' WHERE id = ?", otherSibling.ID.String())
	if err == nil || !strings.Contains(err.Error(), "lineage writer protocol required") {
		t.Fatalf("unarmed raw write on the other copy = %v, want the lineage writer refusal", err)
	}
	for _, probe := range []struct {
		table, predicate string
		args             []any
	}{
		{"orgs", "id = ?", []any{tenant.String()}},
		{"users", "id = ?", []any{user.ID.String()}},
		{identityDescriptor.Table, "id = ?", []any{identity.ID.String()}},
		{directoryTombstoneDescriptor.Table, "", nil},
		{auditTable, "tenant_id = ?", []any{tenant.String()}},
		{dialect.DirectoryWriterMarkerTable, "", nil},
		{lineageWriterTable, "", nil},
	} {
		if got := retirementRowCount(t, other.sql, probe.table, probe.predicate, probe.args...); got != 0 {
			t.Fatalf("holder state leaked into the other copy: %s %q = %d rows", probe.table, probe.predicate, got)
		}
	}
	var temp int64
	if err := other.sql.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM temp.sqlite_master").Scan(&temp); err != nil || temp != 0 {
		t.Fatalf("other copy sees %d TEMP objects (%v)", temp, err)
	}

	// The holder's rollback and close change nothing for the other copy.
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	closeHolder()
	other.enforce(t)
	otherRetiree := retirementCreateIdentity(t, other.raw, otherTenant, "isolation-other-retiree")
	otherResult, err := RetireDirectoryPrincipal(ctx, other.raw, DirectoryPrincipalRetirementRequest{
		TenantID: otherTenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: otherRetiree.ID, ExpectedVersion: otherRetiree.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if err != nil || !otherResult.Definitive {
		t.Fatalf("other retirement after the holder closed = %+v err=%v", otherResult, err)
	}
	if got := retirementRowCount(t, other.sql, directoryTombstoneDescriptor.Table, "principal_ref = ?", otherRetiree.ID.String()); got != 1 {
		t.Fatalf("other tombstones for its own retiree = %d, want 1", got)
	}
	if got := retirementRowCount(t, other.sql, directoryTombstoneDescriptor.Table, "", nil); got != 1 {
		t.Fatalf("other copy has %d tombstones, want only its own", got)
	}

	// A later copy from the cached bytes is still exactly the seed.
	later, _ := currentSchemaTestOpenCopy(t, &currentSchemaRetirementSeed)
	if err := verifyCurrentSchemaRetirementSeedState(ctx, later.sql.db); err != nil {
		t.Fatalf("later copy: %v", err)
	}
	if err := verifyCurrentSchemaRetirementWitnesses(later.sql); err != nil {
		t.Fatalf("later copy witnesses: %v", err)
	}
}

const currentSchemaTombstoneGuard = "core_directory_tombstone_no_update"

func TestCurrentSchemaRetirementFixtureRefusesMissingTombstoneGuard(t *testing.T) {
	ctx := context.Background()
	path, err := writeCurrentSchemaRetirementCopy(ctx, &currentSchemaRetirementSeed, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const present = "SELECT COUNT(*) FROM main.sqlite_master WHERE type = 'trigger' AND name = '" + currentSchemaTombstoneGuard + "'"
	raw := currentSchemaTestOpenRaw(t, path)
	directoryWriterTestWantSQLiteScalar(t, raw, present, 1)
	currentSchemaTestMustExec(t, raw, "DROP TRIGGER main."+currentSchemaTombstoneGuard)
	directoryWriterTestWantSQLiteScalar(t, raw, present, 0)
	currentSchemaTestCloseRaw(t, raw)

	opened, err := Open(ctx, currentSchemaTestCopyConfig(path), nil)
	if err == nil {
		_ = opened.Close()
		t.Fatal("Open accepted a copy without its non-healing tombstone guard")
	}
	if opened != nil {
		t.Fatalf("refused Open returned a store: %v", opened)
	}
	want := `missing declared SQLite object "` + currentSchemaTombstoneGuard + `"`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Open error = %v, want it to name %s", err, want)
	}
	// Not healed: the guard is still absent after the refusal.
	post := currentSchemaTestOpenRaw(t, path)
	directoryWriterTestWantSQLiteScalar(t, post, present, 0)
	currentSchemaTestCloseRaw(t, post)

	// An untouched sibling copy still opens.
	sibling, _ := currentSchemaTestOpenCopy(t, &currentSchemaRetirementSeed)
	if err := verifyCurrentSchemaRetirementWitnesses(sibling.sql); err != nil {
		t.Fatalf("sibling copy: %v", err)
	}
}

func TestCurrentSchemaRetirementFixtureRefusesAlteredReceipt(t *testing.T) {
	ctx := context.Background()
	path, err := writeCurrentSchemaRetirementCopy(ctx, &currentSchemaRetirementSeed, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := currentSchemaTestOpenRaw(t, path)
	guardsOf := func(db *sql.DB) map[string]string {
		rows, err := db.QueryContext(ctx, "SELECT name, sql FROM main.sqlite_master WHERE type = 'trigger' AND tbl_name = ? ORDER BY name", guardReceiptsTable)
		if err != nil {
			t.Fatal(err)
		}
		// Closed on every exit, including a Fatal from Scan (Goexit runs defers);
		// the normal path closes explicitly so a close error is a failure too.
		closed := false
		defer func() {
			if !closed {
				_ = rows.Close()
			}
		}()
		out := map[string]string{}
		for rows.Next() {
			var name, text string
			if err := rows.Scan(&name, &text); err != nil {
				t.Fatal(err)
			}
			out[name] = text
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("receipt guard inventory: %v", err)
		}
		closed = true
		if err := rows.Close(); err != nil {
			t.Fatalf("receipt guard inventory close: %v", err)
		}
		return out
	}
	guards := guardsOf(raw)
	if _, ok := guards[guardReceiptsTable+"_no_update"]; !ok || len(guards) < 2 {
		t.Fatalf("receipt guards = %v", guards)
	}
	const alter = "UPDATE main." + guardReceiptsTable + " SET attempt_id = 'altered-by-control' WHERE event_ordinal = 1"
	if _, err := raw.ExecContext(ctx, alter); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("append-only guard did not refuse the alteration: %v", err)
	}
	for name := range guards {
		currentSchemaTestMustExec(t, raw, "DROP TRIGGER main."+quoteIdent(name))
	}
	res, err := raw.ExecContext(ctx, alter)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("alteration touched %d rows, want 1", n)
	}
	var names []string
	for name := range guards {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		currentSchemaTestMustExec(t, raw, guards[name])
	}
	var attempt string
	if err := raw.QueryRowContext(ctx, "SELECT attempt_id FROM main."+guardReceiptsTable+" WHERE event_ordinal = 1").Scan(&attempt); err != nil || attempt != "altered-by-control" {
		t.Fatalf("alteration did not land: attempt_id=%q err=%v", attempt, err)
	}
	if after := guardsOf(raw); fmt.Sprint(after) != fmt.Sprint(guards) {
		t.Fatalf("guards after reinstatement differ:\n%v\n%v", after, guards)
	}
	if err := verifyCurrentSchemaReceiptHistory(ctx, raw); err == nil || !errors.Is(err, ErrGuardGateChainBroken) {
		t.Fatalf("receipt history on the altered copy = %v, want %v", err, ErrGuardGateChainBroken)
	}
	var storedID []byte
	if err := raw.QueryRowContext(ctx, "SELECT receipt_id FROM main."+guardReceiptsTable+" WHERE event_ordinal = 1").Scan(&storedID); err != nil {
		t.Fatal(err)
	}
	currentSchemaTestCloseRaw(t, raw)

	// Open refuses by HISTORY: the core v7 guard witness preflight walks the compiled
	// edition lineage, and every candidate epoch reports the bootstrap receipt whose
	// stored id its altered contents no longer produce. errors.Join keeps that cause
	// reachable behind ErrGuardManifestNoEdge (guardplane.go).
	opened, err := Open(ctx, currentSchemaTestCopyConfig(path), nil)
	if err == nil {
		_ = opened.Close()
		t.Fatal("Open accepted a copy with an altered bootstrap receipt")
	}
	if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) ||
		!strings.Contains(err.Error(), "stores an id its own contents do not produce") ||
		!strings.Contains(err.Error(), hex.EncodeToString(storedID)) {
		t.Fatalf("Open error = %v, want %v naming receipt %x", err, ErrGuardBootstrapReceiptsInvalid, storedID)
	}
	post := currentSchemaTestOpenRaw(t, path)
	if err := post.QueryRowContext(ctx, "SELECT attempt_id FROM main."+guardReceiptsTable+" WHERE event_ordinal = 1").Scan(&attempt); err != nil || attempt != "altered-by-control" {
		t.Fatalf("refused Open changed the receipt: attempt_id=%q err=%v", attempt, err)
	}
	currentSchemaTestCloseRaw(t, post)

	sibling, _ := currentSchemaTestOpenCopy(t, &currentSchemaRetirementSeed)
	if err := verifyCurrentSchemaReceiptHistory(ctx, sibling.sql.db); err != nil {
		t.Fatalf("sibling copy history: %v", err)
	}
}

func TestCurrentSchemaRetirementFixtureWitnessGuardsRejectFakeStore(t *testing.T) {
	ctx := context.Background()
	path, err := writeCurrentSchemaRetirementCopy(ctx, &currentSchemaRetirementSeed, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := currentSchemaTestOpenRaw(t, path)
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	// A Store assembled around the copied file without Open: same engine, same
	// pragmas, same Debug flag, no registry, no elector, no witness.
	fake := &sqlStore{engine: store.EngineSQLite, db: db, adminDB: db, dia: dia, clock: model.SystemClock{}, debug: true}
	if err := verifyCurrentSchemaRetirementWitnesses(fake); err == nil {
		t.Fatal("witness guards accepted a store that never went through Open")
	}
	// Copying the directory witness by hand is not enough either.
	fake.directoryStatus = store.DirectoryStatus{
		ControlMode: store.DirectoryControlStaged, ExpectedGeneration: 1,
		CoverageProtocol: coverageProtocolLegacy, InventoryUnavailableReason: "system_bootstrap_pending",
	}
	if err := verifyCurrentSchemaRetirementWitnesses(fake); err == nil {
		t.Fatal("witness guards accepted a hand-copied directory witness")
	}
	h := newCurrentSchemaDirectoryRetirementSQLiteHarness(t)
	if err := verifyCurrentSchemaRetirementWitnesses(h.sql); err != nil {
		t.Fatalf("real harness: %v", err)
	}
}
