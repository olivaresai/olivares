// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// batchScanCases are the bodies of the pre-decode count's table, with the largest top-level
// array count the scan must report when it stops at 3 (a limit of 2), and whether it can read
// the body at all. kills names the mutant each case catches:
//   - M1: the scan stops counting after the first top-level member (the skip of another key
//     ends the walk);
//   - M2: a non-array value under the counted key ends the scan;
//   - fail-open: a value the scan cannot read, or a top level that is not an object, lets the
//     body through to the full decode uncounted;
//   - key: only the key "requests" is counted, by raw bytes or by a fold that differs from
//     encoding/json's;
//   - first, last: only the first or only the last occurrence of a repeated key is counted;
//   - nested: arrays nested inside a value or an entry are counted;
//   - string, escape: the string or escape state is not tracked;
//   - boundary: an off-by-one count (an empty array counted as one entry, or an array of
//     exactly the limit counted past it).
var batchScanCases = []struct {
	name, body, kills string
	largest           int
	ok                bool
}{
	{"another key first: a scalar", `{"x":0,"requests":[{},{},{}]}`, "M1", 3, true},
	{"another key first: an array of 3", `{"x":[1,2,3],"requests":[{}]}`, "key", 3, true},
	{"another key first: an object", `{"x":{"a":{},"b":1},"requests":[{},{},{}]}`, "M1", 3, true},
	{"nested arrays do not count", `{"x":{"a":[1,2,3]},"requests":[[1,2,3],{"b":[1,2,3]}]}`, "nested", 2, true},
	{"a requests object, then an array of 3", `{"requests":{"a":1},"requests":[{},{},{}]}`, "M2", 3, true},
	{"small then large repeated key", `{"requests":[{}],"requests":[{},{},{}]}`, "first", 3, true},
	{"large then small repeated key", `{"requests":[{},{},{}],"requests":[{}]}`, "last", 3, true},
	{"an escaped key", `{"\u0072equests":[{},{},{}]}`, "key", 3, true},
	{"a folded key (U+017F)", "{\"reque\u017fts\":[{},{},{}]}", "key", 3, true},
	{"a number out of float range first", `{"requests":1e999,"REQUESTS":[{},{},{}]}`, "fail-open", 3, true},
	{"a top level that is an array", `[{},{},{}]`, "fail-open", 0, false},
	{"a top level that is null", `null`, "fail-open", 0, false},
	{"exactly the limit", `{"requests":[{},{}]}`, "boundary", 2, true},
	{"an empty array, with spaces", `{"requests":[ ]}`, "boundary", 0, true},
	{"commas inside a string", `{"requests":["a,b,c"]}`, "string", 1, true},
	{"escaped quotes inside a string", `{"requests":["a\",\"b",{}]}`, "escape", 2, true},
	{"an escaped backslash before a closing quote", `{"requests":["a\\",{},{}]}`, "escape", 3, true},
	{"spaces around every token", `{ "requests" : [ {} , {} , {} ] }`, "boundary", 3, true},
}

// TestScanBatchEntries pins the pre-decode count on small bodies with a limit of 2, so each
// shape is covered without a large body.
func TestScanBatchEntries(t *testing.T) {
	for _, tc := range batchScanCases {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.body)) {
				t.Fatalf("case body is not valid JSON: %s", tc.body)
			}
			largest, ok := scanBatchEntries([]byte(tc.body), 3)
			if largest != tc.largest || ok != tc.ok {
				t.Errorf("scanBatchEntries(%s, 3) = (%d, %v), want (%d, %v); a %s mutant survives", tc.body, largest, ok, tc.largest, tc.ok, tc.kills)
			}
		})
	}
}

// FuzzScanBatchEntriesBoundsTheDecode is the differential oracle between the pre-decode count
// and encoding/json. For every body that passes json.Valid and the scan, the entries the full
// decode binds to Requests are at most the scan's largest top-level array count, and a scan
// that stops at a limit of 2 refuses every body whose decode binds more than 2. The seed corpus
// holds the table's bodies, so the oracle also runs as a normal test.
func FuzzScanBatchEntriesBoundsTheDecode(f *testing.F) {
	for _, tc := range batchScanCases {
		f.Add([]byte(tc.body))
	}
	for _, body := range []string{
		`{}`,
		`{"requests":null}`,
		`{"requests":[{}],"requests":1}`,
		`{"x":[1,2,3,4],"y":{"requests":[1,2,3,4,5]}}`,
		`{"requests":[{"custom_id":"a","params":{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"],[\""}]}]}}]}`,
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if !json.Valid(body) {
			return
		}
		largest, ok := scanBatchEntries(body, len(body)+1)
		if !ok {
			return
		}
		var env batchEnvelope
		// A decode that returns an error has still bound, and allocated, these entries.
		_ = json.Unmarshal(body, &env)
		n := len(env.Requests)
		if n > largest {
			t.Fatalf("the decode bound %d entries, but the scan's largest top-level array holds %d: %s", n, largest, body)
		}
		const limit = 2
		if stopped, _ := scanBatchEntries(body, limit+1); n > limit && stopped <= limit {
			t.Fatalf("the decode bound %d entries, over the limit of %d, but the scan stopping at %d reported %d: %s", n, limit, limit+1, stopped, body)
		}
	})
}

// BenchmarkBatchOverEntryLimit measures the check that runs before the decode (json.Valid and
// the scan) over a batch of 100,001 minimal entries, and over bodies whose first top-level
// value is a string of 1, 16 or 64 MiB. The scan holds no value, so B/op must not grow with
// the value's size; a count that buffered a skipped value would grow with it.
func BenchmarkBatchOverEntryLimit(b *testing.B) {
	b.Run("entries=100001", func(b *testing.B) {
		benchmarkPreDecodeCheck(b, []byte(entryBatch(100001)))
	})
	for _, mib := range []int{1, 16, 64} {
		b.Run(fmt.Sprintf("value=%dMiB", mib), func(b *testing.B) {
			benchmarkPreDecodeCheck(b, valueBatch(mib<<20))
		})
	}
}

func benchmarkPreDecodeCheck(b *testing.B, body []byte) {
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if json.Valid(body) {
			scanBatchEntries(body, maxBatchEntries+1)
		}
	}
}

// valueBatch builds a batch body whose first top-level value is a string of n bytes, followed
// by a one-entry "requests" array.
func valueBatch(n int) []byte {
	body := make([]byte, 0, n+32)
	body = append(body, `{"x":"`...)
	body = append(body, bytes.Repeat([]byte{'a'}, n)...)
	return append(body, `","requests":[{}]}`...)
}
