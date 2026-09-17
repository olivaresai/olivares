// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRebindPositional(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"SELECT 1", "SELECT 1"},
		{"a = ?", "a = $1"},
		{"a = ? AND b = ?", "a = $1 AND b = $2"},
		{"INSERT INTO t VALUES (?, ?, ?)", "INSERT INTO t VALUES ($1, $2, $3)"},
		// A '?' inside a string literal must NOT be rewritten.
		{"x = '?' AND y = ?", "x = '?' AND y = $1"},
		// Escaped quote ('') inside a literal keeps the literal open.
		{"x = 'a''?''b' AND y = ?", "x = 'a''?''b' AND y = $1"},
	}
	for _, tc := range cases {
		if got := rebindPositional(tc.in); got != tc.want {
			t.Errorf("rebind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSQLiteRebindIdentity confirms SQLite leaves '?' untouched.
func TestSQLiteRebindIdentity(t *testing.T) {
	q := "a = ? AND b = ?"
	if got := (sqliteDialect{}).Rebind(q); got != q {
		t.Errorf("sqlite rebind = %q, want unchanged", got)
	}
}

// TestColumnTypeParity checks that every portable kind maps to a concrete,
// NOT NULL-aware column type on both engines (no kind is left unmapped).
func TestColumnTypeParity(t *testing.T) {
	sq := sqliteDialect{}
	pg := postgresDialect{appRole: "olivares_app"}
	for k := model.KindText; k <= model.KindBytes; k++ {
		if !k.Valid() {
			continue
		}
		if got := sq.ColumnType(k, false); got == "" {
			t.Errorf("sqlite ColumnType(%v) empty", k)
		}
		if got := pg.ColumnType(k, true); got == "" {
			t.Errorf("postgres ColumnType(%v) empty", k)
		}
	}
}

// TestRebinderIsRebindAcrossFragments pins the statement rebinder to each
// dialect's own Rebind: for every split of a query into fragments, the summed
// Len equals the length Rebind returns and Write builds the same bytes. The
// queries cross quoted and escaped-quote literals holding '?', fragment splits
// inside a pending quote, and the placeholder digit transitions at 10 and 100.
func TestRebinderIsRebindAcrossFragments(t *testing.T) {
	queries := []string{
		"", "SELECT 1", "a = ?", "x = '?' AND y = ?", "x = 'a''?''b' AND y = ?",
		"''?", "'''?'", "''''?", "a = 'unterminated ? ?", "?'?'?''?'''?",
		strings.Repeat("?, ", 9) + "?", strings.Repeat("c = ? AND ", 99) + "d = ?",
	}
	rng := rand.New(rand.NewPCG(97, 2))
	for i := 0; i < 400; i++ {
		b := make([]byte, rng.IntN(260))
		for j := range b {
			b[j] = "?' a"[rng.IntN(4)]
		}
		queries = append(queries, string(b))
	}
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		d, ok := New(engine)
		if !ok {
			t.Fatalf("dialect %s", engine)
		}
		template, ok := NewRebinder(d)
		if !ok {
			t.Fatalf("%s has no statement rebinder", engine)
		}
		for _, q := range queries {
			want := d.Rebind(q)
			var cuts [][]int
			if len(q) <= 40 {
				for k := 0; k <= len(q); k++ {
					cuts = append(cuts, []int{k})
				}
			}
			for k := 0; k < 8; k++ {
				cut := []int{}
				for at := 0; at < len(q); at += 1 + rng.IntN(7) {
					cut = append(cut, at)
				}
				cuts = append(cuts, cut)
			}
			for _, cut := range cuts {
				counter, writer := template, template
				var total uint64
				var built strings.Builder
				prev := 0
				for _, at := range append(cut, len(q)) {
					n, ok := counter.Len(q[prev:at])
					if !ok {
						t.Fatalf("%s Len overflow on %q", engine, q)
					}
					total += n
					writer.Write(&built, q[prev:at])
					prev = at
				}
				if total != uint64(len(want)) || built.String() != want {
					t.Fatalf("%s cut %v of %q: Len %d built %q, Rebind %q (%d)", engine, cut, q, total, built.String(), want, len(want))
				}
			}
		}
	}
	// Explicit digit widths: 9 x "$d" then "$10" is 21 bytes; 100 placeholders are 9x2+90x3+4 = 292.
	for _, c := range []struct {
		n    int
		want uint64
	}{{9, 18}, {10, 21}, {99, 288}, {100, 292}} {
		r := Rebinder{positional: true}
		if got, ok := r.Len(strings.Repeat("?", c.n)); !ok || got != c.want {
			t.Errorf("%d placeholders: Len %d, want %d", c.n, got, c.want)
		}
	}
	if _, ok := NewRebinder(struct{ Dialect }{sqliteDialect{}}); ok {
		t.Error("a Dialect wrapper claimed a statement rebinder")
	}
	long := strings.Repeat("a = ? AND b = 'x''?' AND ", 200)
	if allocs := testing.AllocsPerRun(20, func() {
		r := Rebinder{positional: true}
		_, _ = r.Len(long)
	}); allocs != 0 {
		t.Errorf("Len allocated %.0f times", allocs)
	}
}

// rebindPositionalDelivered is rebindPositional exactly as delivered at
// 7430616a1a (blob be7bd1e3), kept as the differential oracle: the shared
// statement rebinder must not change a single byte of what PostgreSQL's Rebind
// already returned to every ordinary repository.
func rebindPositionalDelivered(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inLiteral := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			if inLiteral && i+1 < len(query) && query[i+1] == '\'' {
				b.WriteByte(c)
				b.WriteByte(query[i+1])
				i++
				continue
			}
			inLiteral = !inLiteral
			b.WriteByte(c)
		case c == '?' && !inLiteral:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func TestRebindPositionalIsUnchanged(t *testing.T) {
	rng := rand.New(rand.NewPCG(97, 3))
	queries := []string{"", "?", "'", "''", "'?'", "'''", "?''?", "x = 'a''?''b' AND y = ?", strings.Repeat("?", 1000)}
	for i := 0; i < 5000; i++ {
		b := make([]byte, rng.IntN(120))
		for j := range b {
			b[j] = "?'a $1"[rng.IntN(6)]
		}
		queries = append(queries, string(b))
	}
	for _, q := range queries {
		if got, want := rebindPositional(q), rebindPositionalDelivered(q); got != want {
			t.Fatalf("rebind(%q) = %q, delivered %q", q, got, want)
		}
	}
}
