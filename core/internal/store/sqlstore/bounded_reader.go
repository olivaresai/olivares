// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// BR1 bounded reads (BOUNDED-READ-DESIGN v0.3, option A; ROOT-RATIFICATION
// R1-R3). Every statement reserves its worst-case envelope before
// QueryContext, variable values reach the driver only through a row-wide
// conditional projection bounded by the admitted per-cell lengths, and the
// ordinary Get/List/Lock materialization paths are never used.
//
// Admission units (v1 private table): result start 8, row header 8, SQL NULL 9,
// fixed INTEGER/REAL 17, string or bytes 9 + representation bound. SQLite
// returns validated booleans and flags as INTEGER, so they are charged 17.
const (
	boundedResultUnits uint64 = 8
	boundedRowUnits    uint64 = 8
	boundedNullUnits   uint64 = 9
	boundedFixedUnits  uint64 = 17
	boundedBoolUnits   uint64 = 10 // native bool returned by PostgreSQL
	boundedVarUnits    uint64 = 9

	// boundedKeyChars is the canonical model.ID text length. Longer or
	// non-canonical keys are explicit metadata errors, never hidden rows.
	boundedKeyChars uint64 = 36

	// Renderer-owned payload aliases. The derived table exposes only these
	// names, so no descriptor field name can collide with them (BR1-SQL-F1).
	boundedOKAlias     = "olivares_br_ok"
	boundedColumnAlias = "olivares_br_c"

	// Qualified result-column ceilings (BR1-W1 revision 1, section 2): the
	// PostgreSQL 16 target-list limit and this SQLite build's column limit.
	// Every inspection and payload group fits its engine's ceiling. A
	// different engine build requires new qualification, not a descriptor cap.
	boundedPostgresResultColumns uint64 = 1664
	boundedSQLiteResultColumns   uint64 = 2000
)

// boundedQuerier is the reader's statement port: the Scope's exact
// transaction. Every statement of every group goes through it.
type boundedQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// SQLite storage-class codes returned by the inspection statement.
const (
	sqliteClassNull    int64 = 0
	sqliteClassInteger int64 = 1
	sqliteClassReal    int64 = 2
	sqliteClassText    int64 = 3
	sqliteClassBlob    int64 = 4
)

var _ store.BoundedReaderFactory = (*tenantScope)(nil)

// NewBoundedReader implements store.BoundedReaderFactory. It validates limits
// and engine eligibility only; it opens no transaction and runs no SQL.
func (sc *tenantScope) NewBoundedReader(opts store.BoundedReadOptions) (store.BoundedReader, error) {
	if err := opts.Limits.Validate(); err != nil {
		return nil, err
	}
	columns := boundedSQLiteResultColumns
	switch sc.s.dia.Name() {
	case store.EngineSQLite:
	case store.EnginePostgres:
		// The transaction's representation settings are checked by every data
		// method; only an unparsed pool mode is refused here, without SQL.
		if sc.s.pgExecMode == pgExecModeUnobserved {
			return nil, fmt.Errorf("%w: PostgreSQL execution mode is not observed", store.ErrBoundedReadUnavailable)
		}
		columns = boundedPostgresResultColumns
	default:
		return nil, fmt.Errorf("%w: engine %q", store.ErrBoundedReadUnavailable, sc.s.dia.Name())
	}
	// MaxQueryBytes is admitted on the exact statement QueryContext receives,
	// so the dialect must expose its statement rebinder.
	if _, ok := dialect.NewRebinder(sc.s.dia); !ok {
		return nil, fmt.Errorf("%w: dialect has no statement rebinder", store.ErrBoundedReadUnavailable)
	}
	return &boundedReader{sc: sc, opts: opts, lim: opts.Limits, columns: columns, q: sc.tx}, nil
}

type sqliteTextEncoding uint8

const (
	sqliteEncodingUnobserved sqliteTextEncoding = iota
	sqliteEncodingUTF8
	sqliteEncodingUTF16LE
	sqliteEncodingUTF16BE
)

// textMultiplier is the ratified returned-representation bound per stored
// TEXT octet: 1 for UTF-8, the conservative 2 for UTF-16 (modernc 1.54).
func (e sqliteTextEncoding) textMultiplier() uint64 {
	if e == sqliteEncodingUTF8 {
		return 1
	}
	return 2
}

// asciiOctets is the stored octet count of one ASCII character.
func (e sqliteTextEncoding) asciiOctets() uint64 {
	if e == sqliteEncodingUTF8 {
		return 1
	}
	return 2
}

type boundedReader struct {
	sc   *tenantScope
	opts store.BoundedReadOptions
	lim  store.BoundedReadLimits
	busy atomic.Bool

	// columns is the engine's result-column ceiling for every group plan; q is
	// the Scope's transaction. Both are fixed at construction.
	columns uint64
	q       boundedQuerier

	mu         sync.Mutex
	reserved   uint64
	observed   uint64
	rowsRes    uint64
	rowsObs    uint64
	lookRes    uint64
	lookObs    uint64
	incomplete bool
	terminal   error

	// encoding is observed once, through the reader's own transaction and
	// under a reservation. Only the active call (busy gate) touches it.
	encoding sqliteTextEncoding
}

func (r *boundedReader) Usage() store.BoundedReadUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return store.BoundedReadUsage{
		ReservedUnits:          r.reserved,
		ObservedUnits:          r.observed,
		PayloadRowsReserved:    r.rowsRes,
		PayloadRowsObserved:    r.rowsObs,
		LookaheadSlotsReserved: r.lookRes,
		LookaheadRowsObserved:  r.lookObs,
		RemainingUnits:         r.lim.MaxUnits - r.reserved,
		RemainingRows:          r.lim.MaxRows - r.rowsRes,
		ObservedComplete:       !r.incomplete,
		Terminal:               r.terminal != nil,
	}
}

// boundedCall is one data method invocation. attempted flips on the first
// reservation attempt: from then on every error except ordinary ErrNotFound
// makes the reader terminal.
type boundedCall struct {
	r         *boundedReader
	pageUnits uint64
	attempted bool
}

func (r *boundedReader) begin() (*boundedCall, error) {
	if !r.busy.CompareAndSwap(false, true) {
		return nil, store.ErrBoundedReadConcurrent
	}
	r.mu.Lock()
	terminal := r.terminal
	r.mu.Unlock()
	if terminal != nil {
		r.busy.Store(false)
		return nil, fmt.Errorf("%w: %w", store.ErrBoundedReadTerminal, terminal)
	}
	return &boundedCall{r: r}, nil
}

func (c *boundedCall) finish(err error) error {
	defer c.r.busy.Store(false)
	if err == nil || !c.attempted || err == store.ErrNotFound {
		return err
	}
	c.r.mu.Lock()
	if c.r.terminal == nil {
		c.r.terminal = err
	}
	c.r.mu.Unlock()
	return err
}

func addBounded(a, b uint64) (uint64, bool) {
	sum := a + b
	return sum, sum >= a
}

func mulBounded(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > math.MaxUint64/b {
		return 0, false
	}
	return a * b, true
}

func boundedLimitError(dimension, what string, bound uint64) error {
	return fmt.Errorf("%w: %s admission bound %d does not fit %s",
		store.ErrBoundedReadLimit, what, bound, dimension)
}

func boundedOverflow(what string) error {
	return fmt.Errorf("%w: %s admission bound overflows", store.ErrBoundedReadLimit, what)
}

// reserve charges one statement envelope. An issued reservation is never
// refunded, including after an error or fewer rows.
func (c *boundedCall) reserve(units, payloadRows, lookahead uint64, what string) error {
	c.attempted = true
	r := c.r
	page, ok := addBounded(c.pageUnits, units)
	if !ok {
		return boundedOverflow(what)
	}
	if page > r.lim.MaxPageUnits {
		return boundedLimitError("MaxPageUnits", what, units)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	total, ok := addBounded(r.reserved, units)
	if !ok {
		return boundedOverflow(what)
	}
	if total > r.lim.MaxUnits {
		return boundedLimitError("MaxUnits", what, units)
	}
	rows, ok := addBounded(r.rowsRes, payloadRows)
	if !ok || rows > r.lim.MaxRows {
		return boundedLimitError("MaxRows", what, payloadRows)
	}
	look, ok := addBounded(r.lookRes, lookahead)
	if !ok {
		return boundedOverflow(what)
	}
	c.pageUnits, r.reserved, r.rowsRes, r.lookRes = page, total, rows, look
	return nil
}

func (r *boundedReader) observe(units uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sum, ok := addBounded(r.observed, units); ok {
		r.observed = sum
	} else {
		r.observed, r.incomplete = math.MaxUint64, true
	}
}

func (r *boundedReader) markIncomplete() {
	r.mu.Lock()
	r.incomplete = true
	r.mu.Unlock()
}

// envelope is 8 + maxRows x (8 + sum of per-column maximum charges).
func boundedEnvelope(maxRows uint64, columnMax []uint64) (uint64, bool) {
	row := boundedRowUnits
	for _, charge := range columnMax {
		var ok bool
		if row, ok = addBounded(row, charge); !ok {
			return 0, false
		}
	}
	body, ok := mulBounded(maxRows, row)
	if !ok {
		return 0, false
	}
	return addBounded(boundedResultUnits, body)
}

// query reserves the statement envelope and only then issues QueryContext,
// which may preload the first row.
func (c *boundedCall) query(
	ctx context.Context,
	text string,
	args []any,
	maxRows uint64,
	columnMax []uint64,
	payloadRows, lookahead uint64,
	what string,
) (*sql.Rows, error) {
	r := c.r
	render := func(w *boundedSQL) { w.s(text) }
	size, err := c.statementBytes(what, render)
	if err != nil {
		c.attempted = true
		return nil, err
	}
	units, ok := boundedEnvelope(maxRows, columnMax)
	if !ok {
		c.attempted = true
		return nil, boundedOverflow(what)
	}
	if err := c.reserve(units, payloadRows, lookahead, what); err != nil {
		return nil, err
	}
	statement, err := c.buildStatement(what, render, size)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.QueryContext(ctx, statement, args...)
	if err != nil {
		r.markIncomplete()
		return nil, boundedBackendError(err)
	}
	r.observe(boundedResultUnits)
	return rows, nil
}

// closeRows closes a statement and reports its first deferred failure.
func (c *boundedCall) closeRows(rows *sql.Rows, err error) error {
	if err == nil {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		c.r.markIncomplete()
		return boundedBackendError(err)
	}
	return nil
}

// boundedSQL renders one statement through the same code twice: a counting
// pass with checked arithmetic that allocates nothing, then the building pass
// into a builder grown to the exact counted size. Both passes apply the
// dialect's placeholder rewrite as the text is written (rb), so the count is
// the exact length of the statement QueryContext receives.
type boundedSQL struct {
	b    *strings.Builder
	rb   dialect.Rebinder
	n    uint64
	over bool
}

func (w *boundedSQL) s(parts ...string) {
	for _, p := range parts {
		if w.b != nil {
			w.rb.Write(w.b, p)
			continue
		}
		size, ok := w.rb.Len(p)
		n, sumOK := addBounded(w.n, size)
		w.n, w.over = n, w.over || !ok || !sumOK
	}
}

func (w *boundedSQL) u(v uint64) {
	var buf [20]byte
	w.s(string(strconv.AppendUint(buf[:0], v, 10)))
}

// rebinder returns a fresh statement rebinder of the reader's dialect. The
// factory refuses a dialect without one, so every issued statement has it.
func (r *boundedReader) rebinder() dialect.Rebinder {
	rb, _ := dialect.NewRebinder(r.sc.s.dia)
	return rb
}

// statementBytes counts one statement exactly as QueryContext will receive it,
// after the dialect's placeholder rewrite, and admits that length against
// MaxQueryBytes. It builds no text: MaxQueryBytes excludes only protocol
// framing, parameter values and driver-internal rewrites.
func (c *boundedCall) statementBytes(what string, render func(*boundedSQL)) (uint64, error) {
	count := boundedSQL{rb: c.r.rebinder()}
	render(&count)
	if count.over || count.n > math.MaxInt {
		return 0, boundedOverflow(what + " statement")
	}
	if count.n > c.r.lim.MaxQueryBytes {
		return 0, boundedLimitError("MaxQueryBytes", what+" statement", count.n)
	}
	return count.n, nil
}

// buildStatement writes the admitted statement once, already rewritten, into a
// builder of the counted size, and refuses a renderer that did not reproduce it.
func (c *boundedCall) buildStatement(what string, render func(*boundedSQL), size uint64) (string, error) {
	var text strings.Builder
	text.Grow(int(size))
	render(&boundedSQL{b: &text, rb: c.r.rebinder()})
	if uint64(text.Len()) != size {
		return "", fmt.Errorf("sqlstore: bounded %s renderer is not deterministic", what)
	}
	return text.String(), nil
}

// queryRendered checks the exact emitted statement size and its envelope
// before the SQL text or the argument slice exists, reserves the envelope,
// builds both, and only then issues QueryContext.
func (c *boundedCall) queryRendered(
	ctx context.Context,
	what string,
	render func(*boundedSQL),
	maxRows, columnUnits, payloadRows, lookahead uint64,
	args []any,
	extra ...any,
) (*sql.Rows, error) {
	r := c.r
	c.attempted = true
	size, err := c.statementBytes(what, render)
	if err != nil {
		return nil, err
	}
	row, ok := addBounded(boundedRowUnits, columnUnits)
	if !ok {
		return nil, boundedOverflow(what)
	}
	body, ok := mulBounded(maxRows, row)
	if !ok {
		return nil, boundedOverflow(what)
	}
	units, ok := addBounded(boundedResultUnits, body)
	if !ok {
		return nil, boundedOverflow(what)
	}
	if len(args) > math.MaxInt-len(extra) {
		return nil, boundedOverflow(what + " arguments")
	}
	if err := c.reserve(units, payloadRows, lookahead, what); err != nil {
		return nil, err
	}
	statement, err := c.buildStatement(what, render, size)
	if err != nil {
		return nil, err
	}
	all := make([]any, 0, len(args)+len(extra))
	all = append(append(all, args...), extra...)
	rows, err := r.q.QueryContext(ctx, statement, all...)
	if err != nil {
		r.markIncomplete()
		return nil, boundedBackendError(err)
	}
	r.observe(boundedResultUnits)
	return rows, nil
}

// boundedBaseColumns are the engine-managed columns in AllColumns' order;
// deleted_at exists only on soft-delete descriptors.
var boundedBaseColumns = [...]string{model.ColID, model.ColTenantID, model.ColCreatedAt, model.ColUpdatedAt,
	model.ColVersion, model.ColDeletedAt}

func boundedBaseCount(desc model.EntityDescriptor) int {
	if desc.SoftDelete {
		return len(boundedBaseColumns)
	}
	return len(boundedBaseColumns) - 1
}

// boundedColumnAt returns AllColumns' ordinal i (base columns, then fields)
// without allocating the column list.
func boundedColumnAt(desc model.EntityDescriptor, i int) (col string, kind model.SQLKind, nullable bool) {
	if base := boundedBaseCount(desc); i >= base {
		f := desc.Fields[i-base]
		return f.Name, f.Kind, f.Nullable
	}
	col = boundedBaseColumns[i]
	kind, _ = desc.KindOfColumn(col)
	return col, kind, desc.NullableColumn(col)
}

// boundedInspectionExprs is one column's fixed metadata: its storage or NULL
// class, an octet count for a variable kind and, on SQLite, a boolean range
// flag. A column's metadata never splits across inspection groups.
func boundedInspectionExprs(kind model.SQLKind, pg bool) uint64 {
	n := uint64(1)
	if textLikeKind(kind) || kind == model.KindBytes {
		n++
	}
	if kind == model.KindBool && !pg {
		n++
	}
	return n
}

// boundedPlan is a descriptor's checked group shape. It is computed without
// allocation, before any descriptor-sized slice or SQL text exists. Groups
// are private: callers never select or reassemble them.
type boundedPlan struct {
	arity            int
	inspectionGroups uint64
	inspectionExprs  uint64 // sum of every inspection group's expressions
	payloadGroups    uint64
}

// nextInspectionGroup returns the end ordinal and expression count of the
// largest consecutive inspection group that starts at start and fits the
// result-column ceiling. end == start means one column cannot fit.
func (r *boundedReader) nextInspectionGroup(desc model.EntityDescriptor, arity, start int) (int, uint64) {
	pg := r.postgres()
	end, exprs := start, uint64(0)
	for ; end < arity; end++ {
		_, kind, _ := boundedColumnAt(desc, end)
		n := boundedInspectionExprs(kind, pg)
		if n > r.columns-exprs {
			break
		}
		exprs += n
	}
	return end, exprs
}

// nextPayloadGroup returns the end ordinal and nullable count of the largest
// consecutive payload group of g columns, v of them nullable, whose inner
// projection (g columns and the admission flag, g + 1) and outer projection
// (g values, the reject flag and v SQL NULL flags, g + 1 + v) both fit the
// result-column ceiling. end == start means one column cannot fit.
func (r *boundedReader) nextPayloadGroup(desc model.EntityDescriptor, arity, start int) (int, uint64) {
	end, g, v := start, uint64(0), uint64(0)
	for ; end < arity; end++ {
		_, _, nullable := boundedColumnAt(desc, end)
		ng, nv := g+1, v
		if nullable {
			nv++
		}
		if ng+1 > r.columns || ng+1+nv > r.columns {
			break
		}
		g, v = ng, nv
	}
	return end, v
}

// plan computes the checked descriptor arity and both deterministic group
// plans from descriptor ordinals only, never from names or map iteration.
func (r *boundedReader) plan(desc model.EntityDescriptor) (boundedPlan, error) {
	base := boundedBaseCount(desc)
	if len(desc.Fields) > math.MaxInt-base {
		return boundedPlan{}, boundedOverflow("descriptor arity")
	}
	p := boundedPlan{arity: base + len(desc.Fields)}
	unfit := func(what string) error {
		return fmt.Errorf("%w: one column's %s exceeds the %d result-column ceiling",
			store.ErrBoundedReadUnavailable, what, r.columns)
	}
	for start := 0; start < p.arity; {
		end, exprs := r.nextInspectionGroup(desc, p.arity, start)
		if end == start {
			return boundedPlan{}, unfit("inspection metadata")
		}
		var ok bool
		if p.inspectionExprs, ok = addBounded(p.inspectionExprs, exprs); !ok {
			return boundedPlan{}, boundedOverflow("inspection plan")
		}
		p.inspectionGroups++
		start = end
	}
	for start := 0; start < p.arity; {
		end, _ := r.nextPayloadGroup(desc, p.arity, start)
		if end == start {
			return boundedPlan{}, unfit("payload projection")
		}
		p.payloadGroups++
		start = end
	}
	return p, nil
}

// metadataUnits is the sum of every inspection group's one-row envelope,
// 16 + 17 * e_j, that is 16 * groups + 17 * sum(e_j).
func (p boundedPlan) metadataUnits() (uint64, bool) {
	frames, ok := mulBounded(p.inspectionGroups, boundedResultUnits+boundedRowUnits)
	if !ok {
		return 0, false
	}
	exprs, ok := mulBounded(p.inspectionExprs, boundedFixedUnits)
	if !ok {
		return 0, false
	}
	return addBounded(frames, exprs)
}

func (r *boundedReader) postgres() bool {
	return r.sc != nil && r.sc.s.dia.Name() == store.EnginePostgres
}

// locksRows reports a PostgreSQL Mutate (READ COMMITTED) reader, which must
// lock each selected row before its admitted measurement.
func (r *boundedReader) locksRows() bool { return r.postgres() && !r.sc.readOnly }

// representation observes the current transaction's representation facts for
// one data method. SQLite returns its database text encoding. PostgreSQL runs
// the reserved setting-class statement and applies the accepted matrix; its
// TEXT measure already yields returned client bytes, so it uses multiplier 1
// (sqliteEncodingUTF8) and ASCII keys of 36 client bytes.
func (c *boundedCall) representation(ctx context.Context, target boundedTarget) (sqliteTextEncoding, error) {
	if !c.r.postgres() {
		return c.textEncoding(ctx)
	}
	columnMax := make([]uint64, pgSettingsColumns)
	for i := range columnMax {
		columnMax[i] = boundedFixedUnits
	}
	rows, err := c.query(ctx, pgSettingsStatement, nil, 1, columnMax, 0, 0, "settings")
	if err != nil {
		return 0, err
	}
	values := make([]sql.NullInt64, pgSettingsColumns)
	dests := make([]any, pgSettingsColumns)
	for i := range values {
		dests[i] = &values[i]
	}
	found := false
	var scanErr error
	if rows.Next() {
		found = true
		if scanErr = rows.Scan(dests...); scanErr == nil {
			c.r.observe(boundedRowUnits + boundedFixedUnits*pgSettingsColumns)
		}
	}
	if err := c.closeRows(rows, scanErr); err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: PostgreSQL settings were not observed", store.ErrBoundedReadUnavailable)
	}
	for _, v := range values {
		if !v.Valid {
			return 0, fmt.Errorf("%w: PostgreSQL setting class is NULL", store.ErrBoundedReadUnavailable)
		}
	}
	classes := pgSettingClasses{
		server: values[0].Int64, client: values[1].Int64, byteaOutput: values[2].Int64,
		standardStrings: values[3].Int64, version: values[4].Int64,
	}
	if err := pgBoundedEligibility(c.r.sc.s.pgExecMode, classes, descriptorHasBytes(target.desc)); err != nil {
		return 0, err
	}
	return sqliteEncodingUTF8, nil
}

// lockKey takes the PostgreSQL row lock of one selected key under the complete
// authorized predicate, projecting only the guarded key and its reject flag.
// found=false means the row no longer matches after any lock wait.
func (c *boundedCall) lockKey(ctx context.Context, target boundedTarget, where string, args []any, id string) (bool, error) {
	relation := target.sql.relation()
	render := func(w *boundedSQL) {
		keyOK := func() { w.s(pgTextOctetsPrefix, "id", pgTextOctetsSuffix, " <= "); w.u(boundedKeyChars) }
		w.s("SELECT CASE WHEN ")
		keyOK()
		w.s(" THEN id ELSE NULL END, CASE WHEN ")
		keyOK()
		w.s(" THEN 0 ELSE 1 END FROM ", relation, " WHERE ", where, " AND id = ? FOR UPDATE")
	}
	rows, err := c.queryRendered(ctx, "row lock", render, 1,
		boundedVarUnits+boundedKeyChars+boundedFixedUnits, 0, 0, args, id)
	if err != nil {
		return false, err
	}
	var key sql.NullString
	var rejected sql.NullInt64
	found := false
	var scanErr error
	if rows.Next() {
		found = true
		if scanErr = rows.Scan(&key, &rejected); scanErr == nil {
			units := boundedRowUnits + boundedFixedUnits + boundedNullUnits
			if key.Valid {
				units += uint64(len(key.String))
			}
			c.r.observe(units)
		}
	}
	if err := c.closeRows(rows, scanErr); err != nil {
		return false, err
	}
	if found && (!rejected.Valid || rejected.Int64 != 0 || !key.Valid || key.String != id) {
		return false, fmt.Errorf("%w: locked key differs from its selection", store.ErrBoundedReadConsistency)
	}
	return found, nil
}

func (c *boundedCall) textEncoding(ctx context.Context) (sqliteTextEncoding, error) {
	if c.r.encoding != sqliteEncodingUnobserved {
		return c.r.encoding, nil
	}
	const text = "SELECT CASE encoding WHEN 'UTF-8' THEN 1 WHEN 'UTF-16le' THEN 2 " +
		"WHEN 'UTF-16be' THEN 3 ELSE 0 END FROM pragma_encoding LIMIT 1"
	rows, err := c.query(ctx, text, nil, 1, []uint64{boundedFixedUnits}, 0, 0, "encoding")
	if err != nil {
		return 0, err
	}
	var code sql.NullInt64
	found := false
	var scanErr error
	if rows.Next() {
		found = true
		scanErr = rows.Scan(&code)
		if scanErr == nil {
			c.r.observe(boundedRowUnits + boundedFixedUnits)
		}
	}
	if err := c.closeRows(rows, scanErr); err != nil {
		return 0, err
	}
	if !found || !code.Valid || code.Int64 < 1 || code.Int64 > 3 {
		return 0, fmt.Errorf("%w: SQLite text encoding is not qualified", store.ErrBoundedReadUnavailable)
	}
	c.r.encoding = sqliteTextEncoding(code.Int64)
	return c.r.encoding, nil
}

// boundedTarget is a registry-derived descriptor plus its forced lineage.
type boundedTarget struct {
	desc    model.EntityDescriptor
	sql     *genericRepo // filter rendering and relation only; never materializes
	lineage *model.Filter
}

func (r *boundedReader) targetFor(desc model.EntityDescriptor) *genericRepo {
	return &genericRepo{
		tenant: r.sc.tenant, dia: r.sc.s.dia, desc: desc,
		engineQualified: isDirectoryAuthorityTable(desc.Table),
	}
}

func (r *boundedReader) policyTarget() (boundedTarget, error) {
	if !r.opts.PolicyReadAllowed() {
		return boundedTarget{}, fmt.Errorf("%w: policies carry no workspace lineage",
			store.ErrWorkspaceLineageRequired)
	}
	return boundedTarget{desc: policyDescriptor, sql: r.targetFor(policyDescriptor)}, nil
}

func (r *boundedReader) extensionTarget(kind model.Kind) (boundedTarget, error) {
	if kind.Namespace() == model.CoreNamespace {
		return boundedTarget{}, store.ErrUnknownEntity
	}
	desc, ok := r.sc.s.reg.lookup(kind)
	if !ok {
		return boundedTarget{}, store.ErrUnknownEntity
	}
	forced, confined, err := r.opts.ExtensionConstraint(desc)
	if err != nil {
		return boundedTarget{}, err
	}
	target := boundedTarget{desc: desc, sql: r.targetFor(desc)}
	if confined {
		target.lineage = &forced
	}
	return target, nil
}

// boundedParams accounts the aggregate request input bytes.
type boundedParams struct {
	max, total uint64
}

func (p *boundedParams) add(n uint64, what string) error {
	sum, ok := addBounded(p.total, n)
	if !ok || sum > p.max {
		return boundedLimitError("MaxParameterBytes", what, n)
	}
	p.total = sum
	return nil
}

func invalidBoundedRead(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{store.ErrInvalidBoundedRead}, args...)...)
}

// boundedValueSize validates one value of the finite model value family and
// returns its parameter size without copying it. It never invokes
// driver.Valuer, Stringer or another custom conversion.
func boundedValueSize(v any) (uint64, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case string:
		return uint64(len(x)), nil
	case []byte:
		return uint64(len(x)), nil
	case bool:
		return 1, nil
	case float32, float64:
		return 8, nil
	}
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.String:
		return uint64(value.Len()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return 8, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value.Uint() > math.MaxInt64 {
			return 0, invalidBoundedRead("unsigned filter value exceeds int64")
		}
		return 8, nil
	default:
		return 0, invalidBoundedRead("unsupported filter value type")
	}
}

// copyBoundedValue normalizes a value already admitted by boundedValueSize.
// A typed nil byte slice stays nil, so it keeps the SQL NULL binding the
// ordinary repository gives it; present bytes, including empty, are copied.
func copyBoundedValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if x == nil {
			return []byte(nil)
		}
		return append(make([]byte, 0, len(x)), x...)
	case string, bool, float64:
		return x
	case float32:
		return float64(x)
	}
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.String:
		return value.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	default:
		return int64(value.Uint())
	}
}

func canonicalBoundedID(raw string) bool {
	id, err := model.ParseID(raw)
	return err == nil && !id.IsZero() && id.String() == raw && uint64(len(raw)) == boundedKeyChars
}

func textLikeKind(kind model.SQLKind) bool {
	switch kind {
	case model.KindText, model.KindJSON, model.KindTimestamp, model.KindUUID:
		return true
	}
	return false
}

// validateFilters counts the caller's filters before confinement replaces the
// lineage column, bounds every parameter and appends the forced lineage.
func (r *boundedReader) validateFilters(
	target boundedTarget,
	filters []model.Filter,
	params *boundedParams,
) ([]model.Filter, error) {
	if uint64(len(filters)) > r.lim.MaxFilters {
		return nil, boundedLimitError("MaxFilters", "filter count", uint64(len(filters)))
	}
	// Pass 1 sizes the complete caller input and the forced predicate. It
	// copies nothing and allocates nothing proportional to the request.
	kept := 0
	for _, f := range filters {
		kind, ok := target.desc.KindOfColumn(f.Column)
		if !ok {
			return nil, fmt.Errorf("%w: unknown filter column", store.ErrUnknownEntity)
		}
		if !f.Op.Valid() {
			return nil, invalidBoundedRead("invalid filter operator")
		}
		if f.Op == model.OpLike && kind != model.KindText {
			return nil, invalidBoundedRead("LIKE requires a text column")
		}
		if err := params.add(uint64(len(f.Column))+uint64(len(f.Op)), "filter"); err != nil {
			return nil, err
		}
		if (f.Op == model.OpIsNull || f.Op == model.OpNotNull) && f.Value != nil {
			return nil, invalidBoundedRead("null operators bind no value")
		}
		size, err := boundedValueSize(f.Value)
		if err != nil {
			return nil, err
		}
		if err := params.add(size, "filter value"); err != nil {
			return nil, err
		}
		if target.lineage == nil || f.Column != target.lineage.Column {
			kept++ // lineage filters are replaced by the forced predicate, as forceQuery does
		}
	}
	if target.lineage != nil {
		forced := *target.lineage
		if err := params.add(uint64(len(forced.Column))+uint64(len(forced.Op)), "lineage filter"); err != nil {
			return nil, err
		}
		size, err := boundedValueSize(forced.Value)
		if err != nil {
			return nil, err
		}
		if err := params.add(size, "lineage filter value"); err != nil {
			return nil, err
		}
		kept++
	}
	// Pass 2 copies the admitted values into an exactly sized slice.
	out := make([]model.Filter, 0, kept)
	for _, f := range filters {
		if target.lineage != nil && f.Column == target.lineage.Column {
			continue
		}
		out = append(out, model.Filter{Column: f.Column, Op: f.Op, Value: copyBoundedValue(f.Value)})
	}
	if target.lineage != nil {
		forced := *target.lineage
		forced.Value = copyBoundedValue(forced.Value)
		out = append(out, forced)
	}
	return out, nil
}

// where renders tenant, soft-delete, filters and the lineage authority
// predicate. The lineage column must hold a readable storage class, so a
// wrong-type lineage value is excluded without disclosing its length.
func (target boundedTarget) where(tenant model.TenantID, filters []model.Filter, includeDeleted bool) (string, []any, error) {
	parts := []string{"tenant_id = ?"}
	args := []any{tenant.String()}
	if target.desc.SoftDelete && !includeDeleted {
		parts = append(parts, "deleted_at IS NULL")
	}
	for _, f := range filters {
		frag, value, err := target.sql.filterFragment(f)
		if err != nil {
			return "", nil, err
		}
		// PostgreSQL lineage columns are typed TEXT; SQLite needs a class guard.
		if target.lineage != nil && f.Column == target.lineage.Column && target.sql.dia.Name() == store.EngineSQLite {
			guard := "typeof(" + f.Column + ") = 'text'"
			if f.Op == model.OpEqOrUnset {
				guard = "typeof(" + f.Column + ") IN ('text', 'null')"
			}
			frag = "(" + guard + " AND " + frag + ")"
		}
		parts = append(parts, frag)
		if f.Op != model.OpIsNull && f.Op != model.OpNotNull {
			args = append(args, value)
		}
	}
	return strings.Join(parts, " AND "), args, nil
}

// boundedAdmission is one column's admitted shape for a selected row.
type boundedAdmission struct {
	column   string
	kind     model.SQLKind
	nullable bool
	class    int64
	octets   uint64
	bound    uint64 // returned representation bound for variable values
	charge   uint64 // admitted cell charge, without its separate SQL NULL flag
}

// boundedRow is one selected row's complete admission.
type boundedRow struct {
	admissions []boundedAdmission
	// rowUnits is the MaxRowUnits charge: the sum of every payload group's row
	// (header, reject flag, SQL NULL flags and admitted cells).
	rowUnits uint64
	// envelopes is the sum of the row's payload statement envelopes: one
	// 8-unit result frame per group plus rowUnits.
	envelopes uint64
}

// admitMetadata checks a complete inspection plan against the remaining page
// and traversal units before any descriptor-sized allocation. It is a
// calculation, not a charge: each issued group still reserves its own envelope.
func (c *boundedCall) admitMetadata(units uint64) error {
	c.attempted = true
	page, ok := addBounded(c.pageUnits, units)
	if !ok {
		return boundedOverflow("inspection plan")
	}
	if page > c.r.lim.MaxPageUnits {
		return boundedLimitError("MaxPageUnits", "inspection plan", units)
	}
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	total, ok := addBounded(c.r.reserved, units)
	if !ok {
		return boundedOverflow("inspection plan")
	}
	if total > c.r.lim.MaxUnits {
		return boundedLimitError("MaxUnits", "inspection plan", units)
	}
	return nil
}

// inspect reads only fixed storage-class codes, octet lengths and boolean
// range flags for one exact ID, one consecutive column group per statement on
// the same transaction and predicate. found=false is ordinary absence at the
// first group only; a row that a later group no longer finds is a consistency
// failure.
func (c *boundedCall) inspect(
	ctx context.Context,
	target boundedTarget,
	where string,
	args []any,
	id string,
	enc sqliteTextEncoding,
) (boundedRow, bool, error) {
	pg := c.r.postgres()
	desc := target.desc
	// The checked plan and its complete metadata total precede every
	// descriptor-sized allocation and every SQL text (R4.2).
	plan, err := c.r.plan(desc)
	if err != nil {
		c.attempted = true
		return boundedRow{}, false, err
	}
	metadata, ok := plan.metadataUnits()
	if !ok {
		c.attempted = true
		return boundedRow{}, false, boundedOverflow("inspection plan")
	}
	if err := c.admitMetadata(metadata); err != nil {
		return boundedRow{}, false, err
	}
	// Every payload group adds its own row header and reject flag.
	rowUnits, ok := mulBounded(plan.payloadGroups, boundedRowUnits+boundedFixedUnits)
	if !ok {
		return boundedRow{}, false, boundedOverflow("payload row")
	}
	relation := target.sql.relation()
	admissions := make([]boundedAdmission, plan.arity)
	for start, group := 0, 0; start < plan.arity; group++ {
		end, exprs := c.r.nextInspectionGroup(desc, plan.arity, start)
		// Layout: every class, then the group's lengths, then its SQLite
		// boolean flags; the decoder below walks the same plan.
		render := func(w *boundedSQL) {
			w.s("SELECT ")
			for i := start; i < end; i++ {
				col, _, _ := boundedColumnAt(desc, i)
				if i > start {
					w.s(", ")
				}
				if pg {
					// PostgreSQL column types are fixed by the descriptor DDL.
					w.s("CASE WHEN ", col, " IS NULL THEN 0 ELSE 1 END")
					continue
				}
				w.s("CASE typeof(", col, ") WHEN 'null' THEN 0 WHEN 'integer' THEN 1 ",
					"WHEN 'real' THEN 2 WHEN 'text' THEN 3 WHEN 'blob' THEN 4 ELSE 5 END")
			}
			for i := start; i < end; i++ {
				col, kind, _ := boundedColumnAt(desc, i)
				switch {
				case pg && textLikeKind(kind):
					w.s(", CASE WHEN ", col, " IS NULL THEN 0 ELSE ", pgTextOctetsPrefix, col, pgTextOctetsSuffix, " END")
				case pg && kind == model.KindBytes:
					w.s(", CASE WHEN ", col, " IS NULL THEN 0 ELSE pg_catalog.octet_length(", col, ") END")
				case textLikeKind(kind) || kind == model.KindBytes:
					w.s(", CASE WHEN typeof(", col, ") IN ('text', 'blob') THEN octet_length(", col, ") ELSE 0 END")
				}
			}
			if !pg {
				for i := start; i < end; i++ {
					if col, kind, _ := boundedColumnAt(desc, i); kind == model.KindBool {
						w.s(", CASE WHEN typeof(", col, ") = 'integer' AND ", col, " IN (0, 1) THEN 1 ELSE 0 END")
					}
				}
			}
			w.s(" FROM ", relation, " WHERE ", where, " AND id = ? LIMIT 1")
		}
		// exprs never exceeds the ceiling, and the plan total was checked.
		columnUnits := exprs * boundedFixedUnits
		rows, err := c.queryRendered(ctx, "inspection", render, 1, columnUnits, 0, 0, args, id)
		if err != nil {
			return boundedRow{}, false, err
		}
		values := make([]sql.NullInt64, exprs)
		dests := make([]any, exprs)
		for i := range values {
			dests[i] = &values[i]
		}
		found := false
		var scanErr error
		if rows.Next() {
			found = true
			if scanErr = rows.Scan(dests...); scanErr == nil {
				c.r.observe(boundedRowUnits + columnUnits)
			}
		}
		if err := c.closeRows(rows, scanErr); err != nil {
			return boundedRow{}, false, err
		}
		if !found {
			if group == 0 {
				return boundedRow{}, false, nil
			}
			return boundedRow{}, false, fmt.Errorf("%w: row disappeared between inspection groups",
				store.ErrBoundedReadConsistency)
		}
		for i, v := range values {
			if !v.Valid {
				return boundedRow{}, false, fmt.Errorf("%w: inspection flag %d of group %d is NULL",
					store.ErrBoundedReadMetadata, i, group)
			}
		}
		next := end - start
		for i := start; i < end; i++ {
			col, kind, nullable := boundedColumnAt(desc, i)
			a := boundedAdmission{column: col, kind: kind, nullable: nullable, class: values[i-start].Int64}
			if pg && a.class != sqliteClassNull {
				a.class = pgStorageClass(kind)
			}
			if textLikeKind(kind) || kind == model.KindBytes {
				n := values[next].Int64
				next++
				if n < 0 {
					return boundedRow{}, false, fmt.Errorf("%w: negative length", store.ErrBoundedReadMetadata)
				}
				a.octets = uint64(n)
			}
			admissions[i] = a
		}
		for i := start; i < end; i++ {
			a := &admissions[i]
			boolOK := pg && a.kind == model.KindBool // native BOOLEAN
			if !pg && a.kind == model.KindBool {
				boolOK = values[next].Int64 == 1
				next++
			}
			charge, err := c.admit(a, boolOK, enc)
			if err != nil {
				return boundedRow{}, false, err
			}
			a.charge = charge
			if a.nullable {
				if charge, ok = addBounded(charge, boundedFixedUnits); !ok { // separate null flag
					return boundedRow{}, false, boundedOverflow("payload row")
				}
			}
			if rowUnits, ok = addBounded(rowUnits, charge); !ok {
				return boundedRow{}, false, boundedOverflow("payload row")
			}
		}
		// The charge only grows, so a known refusal stops before the next group.
		if rowUnits > c.r.lim.MaxRowUnits {
			return boundedRow{}, false, boundedLimitError("MaxRowUnits", "payload row", rowUnits)
		}
		start = end
	}
	frames, ok := mulBounded(plan.payloadGroups, boundedResultUnits)
	if !ok {
		return boundedRow{}, false, boundedOverflow("payload row")
	}
	envelopes, ok := addBounded(frames, rowUnits)
	if !ok {
		return boundedRow{}, false, boundedOverflow("payload row")
	}
	return boundedRow{admissions: admissions, rowUnits: rowUnits, envelopes: envelopes}, true, nil
}

// admit validates one column's storage class and returns its payload charge.
func (c *boundedCall) admit(a *boundedAdmission, boolOK bool, enc sqliteTextEncoding) (uint64, error) {
	metadata := func() error {
		return fmt.Errorf("%w: column %s has an inadmissible storage class", store.ErrBoundedReadMetadata, a.column)
	}
	if a.class == sqliteClassNull {
		if !a.nullable {
			return 0, metadata()
		}
		return boundedNullUnits, nil
	}
	switch {
	case textLikeKind(a.kind):
		switch a.class {
		case sqliteClassText:
			bound, ok := mulBounded(a.octets, enc.textMultiplier())
			if !ok {
				return 0, boundedOverflow("cell")
			}
			a.bound = bound
		case sqliteClassBlob:
			a.bound = a.octets
		default:
			return 0, metadata()
		}
	case a.kind == model.KindBytes:
		if a.class != sqliteClassBlob {
			return 0, metadata()
		}
		a.bound = a.octets
	case a.kind == model.KindInt:
		if a.class != sqliteClassInteger {
			return 0, metadata()
		}
		return boundedFixedUnits, nil
	case a.kind == model.KindFloat:
		if a.class != sqliteClassInteger && a.class != sqliteClassReal {
			return 0, metadata()
		}
		return boundedFixedUnits, nil
	case a.kind == model.KindBool:
		if a.class != sqliteClassInteger || !boolOK {
			return 0, metadata()
		}
		if c.r.postgres() {
			return boundedBoolUnits, nil // PostgreSQL returns a validated native bool
		}
		return boundedFixedUnits, nil
	default:
		return 0, metadata()
	}
	if a.bound > c.r.lim.MaxCellBytes {
		return 0, boundedLimitError("MaxCellBytes", "cell "+a.column, a.bound)
	}
	charge, ok := addBounded(boundedVarUnits, a.bound)
	if !ok {
		return 0, boundedOverflow("cell")
	}
	return charge, nil
}

// writeRowOK renders the renderer-owned admission condition for one complete
// row as a balanced binary conjunction: one column predicate is a leaf, and a
// larger ordinal interval is (left AND right) split at its midpoint, so the
// expression depth grows with log2 of the arity. Every column must still have
// its admitted class, and every variable value must fit that row's admitted
// per-cell octet count. Counting and building share this renderer.
func writeRowOK(w *boundedSQL, admissions []boundedAdmission, pg bool) {
	if len(admissions) <= 1 {
		for _, a := range admissions {
			writeAdmissionOK(w, a, pg)
		}
		return
	}
	mid := len(admissions) / 2
	w.s("(")
	writeRowOK(w, admissions[:mid], pg)
	w.s(" AND ")
	writeRowOK(w, admissions[mid:], pg)
	w.s(")")
}

// writeAdmissionOK renders one column's leaf predicate.
func writeAdmissionOK(w *boundedSQL, a boundedAdmission, pg bool) {
	if pg {
		switch {
		case a.class == sqliteClassNull:
			w.s(a.column, " IS NULL")
		case textLikeKind(a.kind):
			w.s("(", a.column, " IS NOT NULL AND ", pgTextOctetsPrefix, a.column, pgTextOctetsSuffix, " <= ")
			w.u(a.octets)
			w.s(")")
		case a.kind == model.KindBytes:
			w.s("(", a.column, " IS NOT NULL AND pg_catalog.octet_length(", a.column, ") <= ")
			w.u(a.octets)
			w.s(")")
		default:
			w.s(a.column, " IS NOT NULL")
		}
		return
	}
	switch {
	case a.class == sqliteClassNull:
		w.s(a.column, " IS NULL")
	case a.kind == model.KindInt:
		w.s("typeof(", a.column, ") = 'integer'")
	case a.kind == model.KindFloat:
		w.s("typeof(", a.column, ") IN ('integer', 'real')")
	case a.kind == model.KindBool:
		w.s("(typeof(", a.column, ") = 'integer' AND ", a.column, " IN (0, 1))")
	default:
		class := "text"
		if a.class == sqliteClassBlob {
			class = "blob"
		}
		w.s("(typeof(", a.column, ") = '", class, "' AND octet_length(", a.column, ") <= ")
		w.u(a.octets)
		w.s(")")
	}
}

// boundedPayload is the fixed statement context of one admitted row.
type boundedPayload struct {
	relation, where, id string
	args                []any
	row                 boundedRow
	pg                  bool
}

// render renders payload group [start, end). The derived table projects only
// that group's columns under their absolute ordinal aliases, and its guard is
// the complete row admission, including columns projected by other groups.
// Descriptor names appear only as source columns and at the Record mapping.
func (p boundedPayload) render(start, end int) func(*boundedSQL) {
	admissions := p.row.admissions
	return func(w *boundedSQL) {
		w.s("SELECT ")
		for i := start; i < end; i++ {
			if i > start {
				w.s(", ")
			}
			w.s("CASE WHEN b.", boundedOKAlias, " = 1 THEN b.", boundedColumnAlias)
			w.u(uint64(i))
			w.s(" ELSE NULL END")
		}
		w.s(", CASE WHEN b.", boundedOKAlias, " = 1 THEN 0 ELSE 1 END")
		for i := start; i < end; i++ {
			if admissions[i].nullable {
				w.s(", CASE WHEN b.", boundedColumnAlias)
				w.u(uint64(i))
				w.s(" IS NULL THEN 1 ELSE 0 END")
			}
		}
		w.s(" FROM (SELECT CASE WHEN ")
		writeRowOK(w, admissions, p.pg)
		w.s(" THEN 1 ELSE 0 END AS ", boundedOKAlias)
		for i := start; i < end; i++ {
			w.s(", ", admissions[i].column, " AS ", boundedColumnAlias)
			w.u(uint64(i))
		}
		// Both levels carry LIMIT, so SQLite never flattens the derived table
		// into the outer projection and copies the complete guard into every
		// CASE (flattening restriction 13). PostgreSQL does not pull up a
		// subquery with LIMIT either. The result is still at most one row.
		w.s(" FROM ", p.relation, " WHERE ", p.where, " AND id = ? LIMIT 1) AS b LIMIT 1")
	}
}

// groupUnits is payload group [start, end)'s row charge: its 8-unit header,
// the 17-unit reject flag, 17 per SQL NULL flag and the admitted cell charges.
func (p boundedPayload) groupUnits(start, end int) (uint64, bool) {
	units := boundedRowUnits + boundedFixedUnits
	for _, a := range p.row.admissions[start:end] {
		charge, ok := a.charge, true
		if a.nullable {
			if charge, ok = addBounded(charge, boundedFixedUnits); !ok {
				return 0, false
			}
		}
		if units, ok = addBounded(units, charge); !ok {
			return 0, false
		}
	}
	return units, true
}

// admitPayloadStatements counts every planned payload statement of a page
// against MaxQueryBytes, with its argument count, before the first payload
// statement. Octet counts are known, so the exact guard text is counted. It
// renders into no buffer and reserves nothing; each statement still reserves
// its own envelope when it is issued.
func (c *boundedCall) admitPayloadStatements(target boundedTarget, page []boundedPayload) error {
	c.attempted = true
	for _, p := range page {
		if len(p.args) > math.MaxInt-1 {
			return boundedOverflow("payload arguments")
		}
		arity := len(p.row.admissions)
		for start := 0; start < arity; {
			end, _ := c.r.nextPayloadGroup(target.desc, arity, start)
			if end == start {
				return boundedOverflow("payload plan")
			}
			if _, err := c.statementBytes("payload", p.render(start, end)); err != nil {
				return err
			}
			start = end
		}
	}
	return nil
}

// load issues the payload groups of one admitted row in ordinal order on the
// same transaction and predicate. Decoded values stay private in rec until
// every group has succeeded; no partial Record is ever returned.
func (c *boundedCall) load(ctx context.Context, target boundedTarget, p boundedPayload) (model.Record, error) {
	arity := len(p.row.admissions)
	rec := make(model.Record, arity)
	for start := 0; start < arity; {
		end, _ := c.r.nextPayloadGroup(target.desc, arity, start)
		if end == start {
			c.attempted = true
			return nil, boundedOverflow("payload plan")
		}
		if err := c.loadGroup(ctx, p, start, end, rec); err != nil {
			return nil, err
		}
		start = end
	}
	if rec.String(model.ColID) != p.id || rec.String(model.ColTenantID) != c.r.sc.tenant.String() {
		return nil, fmt.Errorf("%w: row identity differs", store.ErrBoundedReadConsistency)
	}
	return rec, nil
}

// loadGroup issues payload group [start, end). The logical row slot is
// reserved on the first group only. After a successful scan and close the
// group's delivered values are observed; the final group then counts the
// logical row as observed before semantic validation (R4.1). Any known error
// returns before a later group is issued.
func (c *boundedCall) loadGroup(ctx context.Context, p boundedPayload, start, end int, rec model.Record) error {
	units, ok := p.groupUnits(start, end)
	if !ok {
		c.attempted = true
		return boundedOverflow("payload row")
	}
	slots := uint64(0)
	if start == 0 {
		slots = 1
	}
	// The envelope's single row carries units, which include its 8-unit header.
	rows, err := c.queryRendered(ctx, "payload", p.render(start, end), 1, units-boundedRowUnits, slots, 0, p.args, p.id)
	if err != nil {
		return err
	}
	admissions := p.row.admissions[start:end]
	nullable := 0
	for _, a := range admissions {
		if a.nullable {
			nullable++
		}
	}
	dests := make([]any, 0, len(admissions)+1+nullable)
	for _, a := range admissions {
		switch {
		case a.kind == model.KindBytes:
			dests = append(dests, new([]byte))
		case textLikeKind(a.kind):
			dests = append(dests, new(sql.NullString))
		case a.kind == model.KindFloat:
			dests = append(dests, new(sql.NullFloat64))
		case a.kind == model.KindBool && p.pg:
			dests = append(dests, new(sql.NullBool))
		default:
			dests = append(dests, new(sql.NullInt64))
		}
	}
	rejected := new(sql.NullInt64)
	dests = append(dests, rejected)
	flags := make([]*sql.NullInt64, len(admissions))
	for j, a := range admissions {
		if a.nullable {
			flags[j] = new(sql.NullInt64)
			dests = append(dests, flags[j])
		}
	}
	found := false
	var scanErr error
	if rows.Next() {
		found = true
		scanErr = rows.Scan(dests...)
	}
	if err := c.closeRows(rows, scanErr); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: admitted row disappeared", store.ErrBoundedReadConsistency)
	}
	// Observed units come from the values the driver actually delivered.
	observed := boundedRowUnits + boundedFixedUnits + boundedFixedUnits*uint64(nullable)
	delivered := 0
	for j := range admissions {
		switch d := dests[j].(type) {
		case *[]byte:
			if *d == nil {
				observed += boundedNullUnits
			} else {
				observed += boundedVarUnits + uint64(len(*d))
				delivered++
			}
		case *sql.NullString:
			if !d.Valid {
				observed += boundedNullUnits
			} else {
				observed += boundedVarUnits + uint64(len(d.String))
				delivered++
			}
		case *sql.NullFloat64:
			observed, delivered = observeFixed(observed, delivered, d.Valid, boundedFixedUnits)
		case *sql.NullInt64:
			observed, delivered = observeFixed(observed, delivered, d.Valid, boundedFixedUnits)
		case *sql.NullBool:
			observed, delivered = observeFixed(observed, delivered, d.Valid, boundedBoolUnits)
		}
	}
	c.r.observe(observed)
	if end == len(p.row.admissions) {
		c.r.mu.Lock()
		c.r.rowsObs++
		c.r.mu.Unlock()
	}
	if !rejected.Valid || rejected.Int64 != 0 {
		if delivered != 0 {
			return fmt.Errorf("%w: rejected row delivered %d values", store.ErrBoundedReadConsistency, delivered)
		}
		return fmt.Errorf("%w: row no longer matches its admission", store.ErrBoundedReadConsistency)
	}
	consistency := func(column string) error {
		return fmt.Errorf("%w: column %s differs from its admission", store.ErrBoundedReadConsistency, column)
	}
	for j, a := range admissions {
		if flag := flags[j]; flag != nil {
			if !flag.Valid || flag.Int64 != boolToInt64(a.class == sqliteClassNull) {
				return consistency(a.column)
			}
		}
		switch d := dests[j].(type) {
		case *[]byte:
			if a.class == sqliteClassNull {
				rec[a.column] = nil
				continue
			}
			if uint64(len(*d)) > a.bound {
				return consistency(a.column)
			}
			rec[a.column] = append([]byte{}, *d...) // present empty stays non-nil
		case *sql.NullString:
			if a.class == sqliteClassNull {
				if d.Valid {
					return consistency(a.column)
				}
				rec[a.column] = nil
				continue
			}
			if !d.Valid || uint64(len(d.String)) > a.bound {
				return consistency(a.column)
			}
			rec[a.column] = d.String
		case *sql.NullFloat64:
			if a.class == sqliteClassNull {
				rec[a.column] = nil
				continue
			}
			if !d.Valid {
				return consistency(a.column)
			}
			rec[a.column] = d.Float64
		case *sql.NullInt64:
			if a.class == sqliteClassNull {
				rec[a.column] = nil
				continue
			}
			if !d.Valid {
				return consistency(a.column)
			}
			if a.kind == model.KindBool {
				if d.Int64 != 0 && d.Int64 != 1 {
					return consistency(a.column)
				}
				rec[a.column] = d.Int64 == 1
			} else {
				rec[a.column] = d.Int64
			}
		case *sql.NullBool:
			if a.class == sqliteClassNull {
				rec[a.column] = nil
				continue
			}
			if !d.Valid {
				return consistency(a.column)
			}
			rec[a.column] = d.Bool
		}
	}
	return nil
}

func observeFixed(observed uint64, delivered int, valid bool, units uint64) (uint64, int) {
	if !valid {
		return observed + boundedNullUnits, delivered
	}
	return observed + units, delivered + 1
}

// pgStorageClass maps a present PostgreSQL value to the class its descriptor
// DDL fixes (dialect ColumnType), so admission shares one rule set.
func pgStorageClass(kind model.SQLKind) int64 {
	switch {
	case textLikeKind(kind):
		return sqliteClassText
	case kind == model.KindBytes:
		return sqliteClassBlob
	case kind == model.KindFloat:
		return sqliteClassReal
	default:
		return sqliteClassInteger
	}
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// getRecord is the Get journey: inspection groups, complete admission,
// payload groups.
func (c *boundedCall) getRecord(ctx context.Context, target boundedTarget, filters []model.Filter, id string) (model.Record, error) {
	enc, err := c.representation(ctx, target)
	if err != nil {
		return nil, err
	}
	where, args, err := target.where(c.r.sc.tenant, filters, false)
	if err != nil {
		return nil, err
	}
	locked := c.r.locksRows()
	if locked {
		found, err := c.lockKey(ctx, target, where, args, id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, store.ErrNotFound
		}
	}
	row, found, err := c.inspect(ctx, target, where, args, id, enc)
	if err != nil {
		return nil, err
	}
	if !found {
		if locked {
			return nil, fmt.Errorf("%w: locked row disappeared", store.ErrBoundedReadConsistency)
		}
		return nil, store.ErrNotFound
	}
	p := boundedPayload{relation: target.sql.relation(), where: where, id: id, args: args, row: row, pg: c.r.postgres()}
	if err := c.admitPage(row.envelopes, 1); err != nil {
		return nil, err
	}
	if err := c.admitPayloadStatements(target, []boundedPayload{p}); err != nil {
		return nil, err
	}
	return c.load(ctx, target, p)
}

// admitPage checks the complete sum of a page's physical payload statement
// envelopes against the page and traversal limits, and the count of logical
// selected rows against MaxRows, before the first payload statement. A group
// is never a row, and no group's result frame is omitted from the envelopes.
func (c *boundedCall) admitPage(envelopes, logicalRows uint64) error {
	c.attempted = true
	page, ok := addBounded(c.pageUnits, envelopes)
	if !ok {
		return boundedOverflow("payload page")
	}
	if page > c.r.lim.MaxPageUnits {
		return boundedLimitError("MaxPageUnits", "payload page", envelopes)
	}
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	total, ok := addBounded(c.r.reserved, envelopes)
	if !ok {
		return boundedOverflow("payload page")
	}
	if total > c.r.lim.MaxUnits {
		return boundedLimitError("MaxUnits", "payload page", envelopes)
	}
	if rows, ok := addBounded(c.r.rowsRes, logicalRows); !ok || rows > c.r.lim.MaxRows {
		return boundedLimitError("MaxRows", "payload page", logicalRows)
	}
	return nil
}

// listRecords is the List journey: guarded key selection with one lookahead
// key, per-row inspection groups, complete page admission, per-row payload groups.
func (c *boundedCall) listRecords(
	ctx context.Context,
	target boundedTarget,
	filters []model.Filter,
	limit int,
	cursor string,
	includeDeleted bool,
) ([]model.Record, model.Page, error) {
	enc, err := c.representation(ctx, target)
	if err != nil {
		return nil, model.Page{}, err
	}
	where, args, err := target.where(c.r.sc.tenant, filters, includeDeleted)
	if err != nil {
		return nil, model.Page{}, err
	}
	keyOctets := boundedKeyChars * enc.asciiOctets()
	keyBound := keyOctets * enc.textMultiplier()
	if keyBound > c.r.lim.MaxCellBytes {
		c.attempted = true
		return nil, model.Page{}, boundedLimitError("MaxCellBytes", "key", keyBound)
	}
	pg := c.r.postgres()
	relation := target.sql.relation()
	render := func(w *boundedSQL) {
		keyOK := func() {
			if pg {
				w.s(pgTextOctetsPrefix, "id", pgTextOctetsSuffix, " <= ")
			} else {
				w.s("typeof(id) = 'text' AND octet_length(id) <= ")
			}
			w.u(keyOctets)
		}
		w.s("SELECT CASE WHEN ")
		keyOK()
		w.s(" THEN id ELSE NULL END, CASE WHEN ")
		keyOK()
		w.s(" THEN 0 ELSE 1 END FROM ", relation, " WHERE ", where)
		if cursor != "" {
			w.s(" AND id > ?")
		}
		w.s(" ORDER BY id ASC LIMIT ")
		w.u(uint64(limit) + 1)
	}
	var cursorArg []any
	if cursor != "" {
		cursorArg = []any{cursor}
	}
	rows, err := c.queryRendered(ctx, "key selection", render, uint64(limit)+1,
		boundedVarUnits+keyBound+boundedFixedUnits, 0, 1, args, cursorArg...)
	if err != nil {
		return nil, model.Page{}, err
	}
	keys := make([]string, 0, limit+1)
	previous := cursor
	var loopErr error
	for rows.Next() {
		var key sql.NullString
		var rejected sql.NullInt64
		if loopErr = rows.Scan(&key, &rejected); loopErr != nil {
			break
		}
		units := boundedRowUnits + boundedFixedUnits + boundedNullUnits
		if key.Valid {
			units += uint64(len(key.String))
		}
		c.r.observe(units)
		if !rejected.Valid || rejected.Int64 != 0 || !key.Valid || !canonicalBoundedID(key.String) {
			loopErr = fmt.Errorf("%w: selected key is not admissible", store.ErrBoundedReadMetadata)
			break
		}
		if previous != "" && key.String <= previous {
			loopErr = fmt.Errorf("%w: selected keys are not strictly ascending", store.ErrBoundedReadConsistency)
			break
		}
		previous = key.String
		keys = append(keys, key.String)
	}
	if err := c.closeRows(rows, loopErr); err != nil {
		return nil, model.Page{}, err
	}
	if loopErr != nil {
		return nil, model.Page{}, loopErr
	}
	page := model.Page{}
	if len(keys) > limit {
		keys = keys[:limit]
		page.HasMore = true
		c.r.mu.Lock()
		c.r.lookObs++
		c.r.mu.Unlock()
	}
	// PostgreSQL Mutate: lock every selected payload row in ascending ID order
	// before measuring it. A row whose filters or authority no longer match
	// after the wait is a consistency failure, never a reordered page. The
	// lookahead key is not locked; it only reports HasMore.
	if c.r.locksRows() {
		for _, key := range keys {
			found, err := c.lockKey(ctx, target, where, args, key)
			if err != nil {
				return nil, model.Page{}, err
			}
			if !found {
				return nil, model.Page{}, fmt.Errorf("%w: selected row changed while waiting for its lock",
					store.ErrBoundedReadConsistency)
			}
		}
	}
	selected := make([]boundedPayload, len(keys))
	envelopes := uint64(0)
	for i, key := range keys {
		row, found, err := c.inspect(ctx, target, where, args, key, enc)
		if err != nil {
			return nil, model.Page{}, err
		}
		if !found {
			return nil, model.Page{}, fmt.Errorf("%w: selected row disappeared", store.ErrBoundedReadConsistency)
		}
		selected[i] = boundedPayload{relation: relation, where: where, id: key, args: args, row: row, pg: pg}
		var ok bool
		if envelopes, ok = addBounded(envelopes, row.envelopes); !ok {
			return nil, model.Page{}, boundedOverflow("payload page")
		}
	}
	if err := c.admitPage(envelopes, uint64(len(keys))); err != nil {
		return nil, model.Page{}, err
	}
	if err := c.admitPayloadStatements(target, selected); err != nil {
		return nil, model.Page{}, err
	}
	out := make([]model.Record, 0, len(keys))
	for _, p := range selected {
		rec, err := c.load(ctx, target, p)
		if err != nil {
			return nil, model.Page{}, err
		}
		out = append(out, rec)
	}
	if page.HasMore && len(out) > 0 {
		page.Cursor = out[len(out)-1].String(model.ColID)
	}
	return out, page, nil
}

// prepareGet validates a Get request before any I/O.
func (r *boundedReader) prepareGet(target boundedTarget, kind model.Kind, id model.ID) ([]model.Filter, error) {
	params := &boundedParams{max: r.lim.MaxParameterBytes}
	if err := params.add(uint64(len(kind)), "kind"); err != nil {
		return nil, err
	}
	if err := params.add(uint64(len(id.String())), "id"); err != nil {
		return nil, err
	}
	if !canonicalBoundedID(id.String()) {
		return nil, invalidBoundedRead("id is not a canonical identifier")
	}
	return r.validateFilters(target, nil, params)
}

// prepareList validates a List request before any I/O.
func (r *boundedReader) prepareList(target boundedTarget, kind model.Kind, q model.Query) ([]model.Filter, int, error) {
	params := &boundedParams{max: r.lim.MaxParameterBytes}
	if err := params.add(uint64(len(kind)), "kind"); err != nil {
		return nil, 0, err
	}
	if err := params.add(uint64(len(q.Cursor)), "cursor"); err != nil {
		return nil, 0, err
	}
	if len(q.Sort) != 0 {
		return nil, 0, invalidBoundedRead("custom sort is not supported")
	}
	if q.Limit <= 0 || q.Limit > maxLimit || uint64(q.Limit) > r.lim.MaxRowsPerPage {
		return nil, 0, invalidBoundedRead("limit must be positive and within the page limits")
	}
	if q.Cursor != "" && !canonicalBoundedID(q.Cursor) {
		return nil, 0, invalidBoundedRead("cursor is not a canonical identifier")
	}
	filters, err := r.validateFilters(target, q.Filters, params)
	return filters, q.Limit, err
}

func (r *boundedReader) GetPolicySnapshot(ctx context.Context, id model.ID) (store.PolicySnapshot, error) {
	call, err := r.begin()
	if err != nil {
		return store.PolicySnapshot{}, err
	}
	snapshot, err := r.getPolicySnapshot(ctx, call, id)
	return snapshot, call.finish(err)
}

func (r *boundedReader) getPolicySnapshot(ctx context.Context, call *boundedCall, id model.ID) (store.PolicySnapshot, error) {
	target, err := r.policyTarget()
	if err != nil {
		return store.PolicySnapshot{}, err
	}
	filters, err := r.prepareGet(target, policyDescriptor.Kind, id)
	if err != nil {
		return store.PolicySnapshot{}, err
	}
	rec, err := call.getRecord(ctx, target, filters, id.String())
	if err != nil {
		return store.PolicySnapshot{}, err
	}
	return r.policySnapshot(rec)
}

func (r *boundedReader) policySnapshot(rec model.Record) (store.PolicySnapshot, error) {
	snapshot, err := policySnapshotFromRecord(rec)
	if err == nil && snapshot.TenantID != r.sc.tenant {
		err = errPolicySnapshotTenant
	}
	if err != nil {
		return store.PolicySnapshot{}, fmt.Errorf("%w: %w", store.ErrBoundedReadMetadata, err)
	}
	return snapshot, nil
}

func (r *boundedReader) ListPolicySnapshots(ctx context.Context, q model.Query) ([]store.PolicySnapshot, model.Page, error) {
	call, err := r.begin()
	if err != nil {
		return nil, model.Page{}, err
	}
	out, page, err := r.listPolicySnapshots(ctx, call, q)
	if err != nil {
		return nil, model.Page{}, call.finish(err)
	}
	return out, page, call.finish(nil)
}

func (r *boundedReader) listPolicySnapshots(ctx context.Context, call *boundedCall, q model.Query) ([]store.PolicySnapshot, model.Page, error) {
	target, err := r.policyTarget()
	if err != nil {
		return nil, model.Page{}, err
	}
	filters, limit, err := r.prepareList(target, policyDescriptor.Kind, q)
	if err != nil {
		return nil, model.Page{}, err
	}
	recs, page, err := call.listRecords(ctx, target, filters, limit, q.Cursor, q.IncludeDeleted)
	if err != nil {
		return nil, model.Page{}, err
	}
	out := make([]store.PolicySnapshot, 0, len(recs))
	for _, rec := range recs {
		snapshot, err := r.policySnapshot(rec)
		if err != nil {
			return nil, model.Page{}, err
		}
		out = append(out, snapshot)
	}
	return out, page, nil
}

func (r *boundedReader) GetExtension(ctx context.Context, kind model.Kind, id model.ID) (model.Record, error) {
	call, err := r.begin()
	if err != nil {
		return nil, err
	}
	rec, err := r.getExtension(ctx, call, kind, id)
	if err != nil {
		return nil, call.finish(err)
	}
	return rec, call.finish(nil)
}

func (r *boundedReader) getExtension(ctx context.Context, call *boundedCall, kind model.Kind, id model.ID) (model.Record, error) {
	target, err := r.extensionTarget(kind)
	if err != nil {
		return nil, err
	}
	filters, err := r.prepareGet(target, kind, id)
	if err != nil {
		return nil, err
	}
	return call.getRecord(ctx, target, filters, id.String())
}

func (r *boundedReader) ListExtensions(ctx context.Context, kind model.Kind, q model.Query) ([]model.Record, model.Page, error) {
	call, err := r.begin()
	if err != nil {
		return nil, model.Page{}, err
	}
	out, page, err := r.listExtensions(ctx, call, kind, q)
	if err != nil {
		return nil, model.Page{}, call.finish(err)
	}
	return out, page, call.finish(nil)
}

func (r *boundedReader) listExtensions(ctx context.Context, call *boundedCall, kind model.Kind, q model.Query) ([]model.Record, model.Page, error) {
	target, err := r.extensionTarget(kind)
	if err != nil {
		return nil, model.Page{}, err
	}
	filters, limit, err := r.prepareList(target, kind, q)
	if err != nil {
		return nil, model.Page{}, err
	}
	return call.listRecords(ctx, target, filters, limit, q.Cursor, q.IncludeDeleted)
}
