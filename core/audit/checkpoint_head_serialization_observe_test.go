// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Pure helpers for the CH1 PostgreSQL interleaving tests
// (checkpoint_head_serialization_pg_test.go), and the controls that pin their
// decisions. Nothing here needs a database, so the controls run everywhere.

// advisoryKey is how pg_locks displays an advisory lock taken on ONE bigint key:
// the high 32 bits in classid, the low 32 bits in objid, and objsubid 1. The
// PostgreSQL tests confirm that layout on the server they use (a probe lock whose
// row must match bigintAdvisoryKey) before they rely on it.
type advisoryKey struct {
	ClassID  int64
	ObjID    int64
	ObjSubID int64
}

func bigintAdvisoryKey(key int64) advisoryKey {
	u := uint64(key)
	return advisoryKey{ClassID: int64(u >> 32), ObjID: int64(u & 0xffffffff), ObjSubID: 1}
}

// advisoryLockRow is one advisory-lock row of pg_locks with its backend's
// application_name and the result of pg_blocking_pids.
type advisoryLockRow struct {
	PID       int64
	App       string
	Granted   bool
	Key       advisoryKey
	BlockedBy []int64
}

// parseBlockingPIDs decodes array_to_string(pg_blocking_pids(pid), ',').
func parseBlockingPIDs(s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("blocking pid list %q: element %q is not a positive backend pid", s, p)
		}
		out = append(out, v)
	}
	return out, nil
}

// lockWait is one observed wait: the waiter backend requests the key and is
// blocked by the blocker backend, which holds it.
type lockWait struct {
	WaiterPID  int64
	BlockerPID int64
}

// findLockWait reports whether the rows show a backend of waiterApp requesting
// key while blocked by a backend of blockerApp that holds the same key. When no
// row qualifies it names the missing condition.
func findLockWait(rows []advisoryLockRow, key advisoryKey, waiterApp, blockerApp string) (lockWait, bool, string) {
	holders := map[int64]bool{}
	for _, r := range rows {
		if r.Granted && r.App == blockerApp && r.Key == key {
			holders[r.PID] = true
		}
	}
	waiting := 0
	for _, r := range rows {
		if r.Granted || r.App != waiterApp || r.Key != key {
			continue
		}
		waiting++
		for _, blocker := range r.BlockedBy {
			if holders[blocker] && blocker != r.PID {
				return lockWait{WaiterPID: r.PID, BlockerPID: blocker}, true, ""
			}
		}
	}
	switch {
	case waiting == 0 && len(holders) == 0:
		return lockWait{}, false, fmt.Sprintf("no %s backend waits for the key and no %s backend holds it", waiterApp, blockerApp)
	case waiting == 0:
		return lockWait{}, false, fmt.Sprintf("a %s backend holds the key but no %s backend waits for it", blockerApp, waiterApp)
	case len(holders) == 0:
		return lockWait{}, false, fmt.Sprintf("a %s backend waits for the key but no %s backend holds it", waiterApp, blockerApp)
	default:
		return lockWait{}, false, fmt.Sprintf("a %s backend waits for the key but is not blocked by the %s backend holding it", waiterApp, blockerApp)
	}
}

// interleavedOutcome describes how one interleaved transaction ended. A failure
// stays a failure with its error, a transaction that was never joined never
// reads as finished, and only a nil error with a written event reads as committed.
type interleavedOutcome struct {
	Launched bool
	Joined   bool
	Err      error
	Wrote    bool
	Seq      int64
}

func (o interleavedOutcome) String() string {
	switch {
	case !o.Launched:
		return "not launched"
	case !o.Joined:
		return "launched but not joined"
	case o.Err != nil:
		return "failed: " + o.Err.Error()
	case !o.Wrote:
		return "returned without writing an event"
	default:
		return fmt.Sprintf("committed at seq %d", o.Seq)
	}
}

// withApplicationName returns dsn with application_name set, so the PostgreSQL
// tests can tell the checkpoint, competitor and observer backends apart through
// the existing Store configuration. Errors never echo the DSN, which carries a
// password.
func withApplicationName(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("the DSN is not a URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("the DSN scheme %q is not a PostgreSQL URL", u.Scheme)
	}
	q := u.Query()
	q.Set("application_name", name)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func TestBigintAdvisoryKeyMatchesThePgLocksSplit(t *testing.T) {
	for _, tc := range []struct {
		key  int64
		want advisoryKey
	}{
		{key: 0, want: advisoryKey{ClassID: 0, ObjID: 0, ObjSubID: 1}},
		{key: 0x0000000100000002, want: advisoryKey{ClassID: 1, ObjID: 2, ObjSubID: 1}},
		{key: -1, want: advisoryKey{ClassID: 4294967295, ObjID: 4294967295, ObjSubID: 1}},
		{key: -9223372036854775808, want: advisoryKey{ClassID: 2147483648, ObjID: 0, ObjSubID: 1}},
		{key: 0x7fffffff00000000, want: advisoryKey{ClassID: 2147483647, ObjID: 0, ObjSubID: 1}},
	} {
		if got := bigintAdvisoryKey(tc.key); got != tc.want {
			t.Errorf("bigintAdvisoryKey(%d) = %+v, want %+v", tc.key, got, tc.want)
		}
	}
}

func TestParseBlockingPIDsRefusesWhatIsNotAPidList(t *testing.T) {
	got, err := parseBlockingPIDs("412,99")
	if err != nil || len(got) != 2 || got[0] != 412 || got[1] != 99 {
		t.Fatalf("parseBlockingPIDs(\"412,99\") = %v, %v", got, err)
	}
	if got, err := parseBlockingPIDs(""); err != nil || got != nil {
		t.Fatalf("an empty list must mean no blocker, got %v, %v", got, err)
	}
	for _, bad := range []string{"{412}", "412,", "0", "-3", "412;99"} {
		if _, err := parseBlockingPIDs(bad); err == nil {
			t.Errorf("parseBlockingPIDs(%q) accepted a value that is not a pid list", bad)
		}
	}
}

func TestFindLockWaitRequiresTheKeyTheWaiterAndItsBlocker(t *testing.T) {
	appendKey := bigintAdvisoryKey(-6917529027641081856)
	otherKey := bigintAdvisoryKey(42)
	const checkpoint, competitor, observer = "ch1_checkpoint", "ch1_competitor", "ch1_observer"
	holder := advisoryLockRow{PID: 100, App: checkpoint, Granted: true, Key: appendKey}
	waiter := advisoryLockRow{PID: 200, App: competitor, Granted: false, Key: appendKey, BlockedBy: []int64{100}}

	cases := []struct {
		name       string
		rows       []advisoryLockRow
		wantFound  bool
		wantReason string
	}{
		{name: "competitor waits for the append key held by the checkpoint", rows: []advisoryLockRow{holder, waiter}, wantFound: true},
		{
			name:      "the competitor's other granted advisory lock does not confuse the match",
			rows:      []advisoryLockRow{{PID: 200, App: competitor, Granted: true, Key: otherKey}, holder, waiter},
			wantFound: true,
		},
		{
			name:       "a wait on a different advisory key",
			rows:       []advisoryLockRow{holder, {PID: 200, App: competitor, Key: otherKey, BlockedBy: []int64{100}}},
			wantReason: "no ch1_competitor backend waits",
		},
		{
			name:       "the same key taken as a two-int4 key (objsubid 2)",
			rows:       []advisoryLockRow{holder, {PID: 200, App: competitor, Key: advisoryKey{ClassID: appendKey.ClassID, ObjID: appendKey.ObjID, ObjSubID: 2}, BlockedBy: []int64{100}}},
			wantReason: "no ch1_competitor backend waits",
		},
		{
			name:       "high and low halves swapped",
			rows:       []advisoryLockRow{holder, {PID: 200, App: competitor, Key: advisoryKey{ClassID: appendKey.ObjID, ObjID: appendKey.ClassID, ObjSubID: 1}, BlockedBy: []int64{100}}},
			wantReason: "no ch1_competitor backend waits",
		},
		{
			name:       "the waiting backend belongs to the wrong application",
			rows:       []advisoryLockRow{holder, {PID: 300, App: observer, Key: appendKey, BlockedBy: []int64{100}}},
			wantReason: "no ch1_competitor backend waits",
		},
		{
			name:       "the competitor was granted the key instead of waiting",
			rows:       []advisoryLockRow{{PID: 200, App: competitor, Granted: true, Key: appendKey}},
			wantReason: "no ch1_competitor backend waits for the key and no ch1_checkpoint backend holds it",
		},
		{
			name:       "the competitor waits but no checkpoint backend holds the key",
			rows:       []advisoryLockRow{{PID: 300, App: observer, Granted: true, Key: appendKey}, {PID: 200, App: competitor, Key: appendKey, BlockedBy: []int64{300}}},
			wantReason: "no ch1_checkpoint backend holds it",
		},
		{
			name:       "the competitor waits behind someone other than the holding checkpoint",
			rows:       []advisoryLockRow{holder, {PID: 200, App: competitor, Key: appendKey, BlockedBy: []int64{300}}},
			wantReason: "is not blocked by the ch1_checkpoint backend holding it",
		},
		{
			name:       "the checkpoint holds the key and nobody waits",
			rows:       []advisoryLockRow{holder},
			wantReason: "no ch1_competitor backend waits for it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, reason := findLockWait(tc.rows, appendKey, competitor, checkpoint)
			if found != tc.wantFound {
				t.Fatalf("found = %v (reason %q), want %v", found, reason, tc.wantFound)
			}
			if found {
				if got != (lockWait{WaiterPID: 200, BlockerPID: 100}) || reason != "" {
					t.Fatalf("observation = %+v reason %q, want waiter 200 blocked by 100", got, reason)
				}
				return
			}
			if !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.wantReason)
			}
		})
	}

	// The reverse direction is the same predicate with the roles exchanged: the
	// forward-shaped rows must not satisfy it.
	if _, found, _ := findLockWait([]advisoryLockRow{holder, waiter}, appendKey, checkpoint, competitor); found {
		t.Fatal("rows where the competitor waits were accepted as the checkpoint waiting")
	}
}

func TestInterleavedOutcomeNeverReportsAFailureOrAnUnjoinedTransactionAsCommitted(t *testing.T) {
	cause := fmt.Errorf("append on the system chain: %w", context.Canceled)
	for _, tc := range []struct {
		name    string
		outcome interleavedOutcome
		want    []string
		never   []string
	}{
		{name: "not launched", outcome: interleavedOutcome{}, want: []string{"not launched"}, never: []string{"committed", "failed"}},
		{name: "launched, not joined", outcome: interleavedOutcome{Launched: true}, want: []string{"not joined"}, never: []string{"committed", "failed"}},
		{name: "failed with a written-looking event", outcome: interleavedOutcome{Launched: true, Joined: true, Err: cause, Wrote: true, Seq: 9}, want: []string{"failed", "context canceled"}, never: []string{"committed"}},
		{name: "returned without writing", outcome: interleavedOutcome{Launched: true, Joined: true}, want: []string{"without writing"}, never: []string{"committed", "failed"}},
		{name: "committed", outcome: interleavedOutcome{Launched: true, Joined: true, Wrote: true, Seq: 7}, want: []string{"committed at seq 7"}, never: []string{"failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.outcome.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q does not contain %q", got, w)
				}
			}
			for _, n := range tc.never {
				if strings.Contains(got, n) {
					t.Errorf("%q must not contain %q", got, n)
				}
			}
		})
	}
}

func TestWithApplicationNameKeepsTheDSNAndNeverEchoesIt(t *testing.T) {
	const secret = "p@ss/wo:rd"
	base := "postgres://olivares_app:" + url.QueryEscape(secret) + "@127.0.0.1:5433/olv_db?sslmode=disable&application_name=ambient"
	got, err := withApplicationName(base, "ch1_competitor")
	if err != nil {
		t.Fatalf("withApplicationName: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("the result is not a URL: %v", err)
	}
	pw, _ := u.User.Password()
	if u.User.Username() != "olivares_app" || pw != secret || u.Host != "127.0.0.1:5433" || u.Path != "/olv_db" {
		t.Fatalf("credentials, host or database changed: user %q host %q path %q", u.User.Username(), u.Host, u.Path)
	}
	if q := u.Query(); q.Get("sslmode") != "disable" || q["application_name"] == nil || len(q["application_name"]) != 1 || q.Get("application_name") != "ch1_competitor" {
		t.Fatalf("query = %v, want sslmode kept and one application_name=ch1_competitor", q)
	}
	escaped := url.QueryEscape(secret)
	for i, bad := range []string{
		// keyword/value form, not a URL
		"host=127.0.0.1 user=olivares_app password=" + secret,
		// another scheme
		"mysql://olivares_app:" + escaped + "@127.0.0.1/olv_db",
		// unparseable (missing ']' in host); a wrapped url.Error would quote this input
		"postgres://olivares_app:" + escaped + "@[::1/olv_db",
	} {
		_, err := withApplicationName(bad, "ch1_observer")
		if err == nil {
			t.Fatalf("bad DSN %d was accepted", i)
		}
		if msg := err.Error(); strings.Contains(msg, secret) || strings.Contains(msg, escaped) {
			t.Fatalf("the refusal of bad DSN %d echoed the password: %v", i, err)
		}
	}
}
