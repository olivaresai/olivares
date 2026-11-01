// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	complianceLock = `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`
	governanceLock = `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`
)

func immutableBackupFixture(t *testing.T) (src, pass, dir string) {
	t.Helper()
	src = t.TempDir()
	seedDataDir(t, src)
	pass = filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pass, []byte("a strong DR passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	return src, pass, t.TempDir()
}

// The immutable writer only adds: it locks the new bundle in COMPLIANCE mode for
// the finest GFS span and leaves every older object for the separate pruner, even
// when the GFS plan would expire one and the credential could delete it.
func TestDRBackupImmutableOffsiteOnlyAdds(t *testing.T) {
	src, pass, dir := immutableBackupFixture(t)
	_, mock, flags := newMockOffsite(t)
	mock.lockConfig = complianceLock
	mock.objects["aa-old.drbundle"] = []byte("older bundle")
	before := time.Now()
	out, err := runDR(t, append([]string{"backup", "--data-dir", src, "--out", filepath.Join(dir, "zz-new.drbundle"),
		"--passphrase-file", pass, "--gfs-daily", "7", "--gfs-keep-last", "1", "--offsite-immutable"}, flags...)...)
	if err != nil {
		t.Fatalf("immutable backup: %v\n%s", err, out)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.deletes) != 0 {
		t.Fatalf("the immutable writer deleted %v", mock.deletes)
	}
	if _, ok := mock.objects["aa-old.drbundle"]; !ok {
		t.Fatal("the older offsite bundle is gone")
	}
	lock := mock.lockUntil["zz-new.drbundle"]
	mode, until, _ := strings.Cut(lock, " ")
	at, perr := time.Parse(time.RFC3339, until)
	want := before.Add(7 * 24 * time.Hour)
	if mode != "COMPLIANCE" || perr != nil || at.Before(want.Add(-time.Minute)) || at.After(want.Add(time.Minute)) {
		t.Fatalf("new bundle lock = %q, want COMPLIANCE until about %s", lock, want.UTC().Format(time.RFC3339))
	}
	if !strings.Contains(out, "offsite prune left to `olivares dr prune --offsite`") {
		t.Fatalf("the backup did not say who prunes the immutable copy:\n%s", out)
	}
}

// A bucket without Object Lock cannot hold an immutable copy: the writer refuses
// to push rather than leave an unlocked object the operator believes is locked.
func TestDRBackupImmutableRefusesUnlockedBucket(t *testing.T) {
	src, pass, dir := immutableBackupFixture(t)
	_, mock, flags := newMockOffsite(t)
	out, err := runDR(t, append([]string{"backup", "--data-dir", src, "--out", filepath.Join(dir, "b.drbundle"),
		"--passphrase-file", pass, "--gfs-daily", "7", "--offsite-immutable"}, flags...)...)
	if err == nil || !strings.Contains(err.Error(), "has no Object Lock") {
		t.Fatalf("backup to an unlocked bucket = %v, want the Object Lock refusal\n%s", err, out)
	}
	if keys := offsiteKeys(mock); len(keys) != 0 {
		t.Fatalf("an object reached the unlocked bucket: %v", keys)
	}
}

func TestDRBackupImmutableNeedsGFSPeriod(t *testing.T) {
	src, pass, dir := immutableBackupFixture(t)
	_, mock, flags := newMockOffsite(t)
	mock.lockConfig = complianceLock
	for _, extra := range [][]string{nil, {"--gfs-keep-last", "3"}} {
		args := append([]string{"backup", "--data-dir", src, "--out", filepath.Join(dir, "b.drbundle"),
			"--passphrase-file", pass, "--offsite-immutable"}, extra...)
		out, err := runDR(t, append(args, flags...)...)
		if err == nil || !strings.Contains(err.Error(), "--offsite-immutable needs a GFS period") {
			t.Fatalf("immutable backup with %v = %v, want the GFS period refusal\n%s", extra, err, out)
		}
	}
	if _, err := runDR(t, "backup", "--data-dir", src, "--out", filepath.Join(dir, "c.drbundle"),
		"--passphrase-file", pass, "--offsite-immutable", "--gfs-daily", "7"); err == nil || !strings.Contains(err.Error(), "--offsite-bucket") {
		t.Fatalf("--offsite-immutable without a bucket = %v, want a refusal", err)
	}
}

// Pruning is a separate command with its own credential: it deletes what GFS
// expires and extends the COMPLIANCE lock of what GFS keeps.
func TestDRPruneOffsite(t *testing.T) {
	_, mock, flags := newMockOffsite(t)
	mock.lockConfig = complianceLock
	now := time.Now().UTC().Truncate(time.Second)
	mock.objects["a.drbundle"] = []byte("a")
	mock.objects["b.drbundle"] = []byte("b")
	mock.objects["c.drbundle"] = []byte("c")
	mock.modified = map[string]time.Time{
		"a.drbundle": now.Add(-72 * time.Hour),
		"b.drbundle": now.Add(-time.Second),
		"c.drbundle": now.Add(-2 * time.Second),
	}
	out, err := runDR(t, append([]string{"prune", "--offsite", "--gfs-daily", "1"}, flags...)...)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	deleted := strings.Join(mock.deletes, ",")
	if deleted != "c.drbundle,a.drbundle" && deleted != "a.drbundle,c.drbundle" {
		t.Fatalf("pruned %v, want a.drbundle and c.drbundle (only the newest day is kept)", mock.deletes)
	}
	until := mock.modified["b.drbundle"].Add(24 * time.Hour).Format(time.RFC3339)
	if got := mock.retention["b.drbundle"]; !strings.Contains(got, "<Mode>COMPLIANCE</Mode><RetainUntilDate>"+until+"</RetainUntilDate>") {
		t.Fatalf("kept bundle retention = %q, want COMPLIANCE until %s", got, until)
	}
	if !strings.Contains(out, "GFS pruned offsite bundle: a.drbundle") || !strings.Contains(out, "locked offsite bundle until "+until+": b.drbundle") {
		t.Fatalf("prune output:\n%s", out)
	}
}

// A pruner credential that may not read the lock configuration still prunes; it
// only extends no lock.
func TestDRPruneWithoutLockRead(t *testing.T) {
	_, mock, flags := newMockOffsite(t)
	mock.lockConfig = "denied"
	mock.objects["a.drbundle"] = []byte("a")
	mock.objects["b.drbundle"] = []byte("b")
	mock.modified = map[string]time.Time{"a.drbundle": time.Now().Add(-72 * time.Hour), "b.drbundle": time.Now()}
	out, err := runDR(t, append([]string{"prune", "--offsite", "--gfs-daily", "1"}, flags...)...)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if strings.Join(mock.deletes, ",") != "a.drbundle" || len(mock.retention) != 0 {
		t.Fatalf("deleted %v, locked %v; want a.drbundle deleted and nothing locked", mock.deletes, mock.retention)
	}
	if !strings.Contains(out, "so no lock is extended") {
		t.Fatalf("prune did not say the locks were not extended:\n%s", out)
	}
}

func TestDRPruneRefusals(t *testing.T) {
	_, _, flags := newMockOffsite(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{append([]string{"prune", "--gfs-daily", "2"}, flags...), "--offsite"},
		{append([]string{"prune", "--offsite"}, flags...), "a GFS policy"},
		{[]string{"prune", "--offsite", "--gfs-daily", "2"}, "--offsite-bucket"},
	} {
		if out, err := runDR(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("dr %v = %v, want an error naming %s\n%s", tc.args, err, tc.want, out)
		}
	}
}

// `dr ls --offsite` reads the bucket's lock configuration and warns when an
// attacker holding a credential could delete every bundle.
func TestDRListOffsiteLockWarning(t *testing.T) {
	for _, tc := range []struct {
		name, lock, want string
	}{
		{"no lock", "", "warning: offsite bucket dr-bucket has no Object Lock"},
		{"governance", governanceLock, "warning: offsite bucket dr-bucket defaults to GOVERNANCE retention"},
		{"compliance", complianceLock, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, flags := newMockOffsite(t)
			mock.lockConfig = tc.lock
			out, err := runDR(t, append([]string{"ls", "--offsite"}, flags...)...)
			if err != nil {
				t.Fatalf("ls: %v\n%s", err, out)
			}
			if tc.want == "" && strings.Contains(out, "warning:") || tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("ls --offsite output = %q, want warning %q", out, tc.want)
			}
		})
	}
}

// The warning goes to the commentary stream, so `-o json` keeps a clean document.
func TestDRListOffsiteLockWarningKeepsJSON(t *testing.T) {
	_, mock, flags := newMockOffsite(t)
	mock.objects["a.drbundle"] = []byte("a")
	stdout, stderr, err := execRoot(t, append([]string{"dr", "ls", "--offsite", "-o", "json"}, flags...)...)
	if err != nil {
		t.Fatalf("dr ls --offsite -o json: %v\n%s", err, stderr)
	}
	var items []drBundleListItem
	if err := json.Unmarshal([]byte(stdout), &items); err != nil || len(items) != 1 {
		t.Fatalf("stdout is not the listing: %v\n%s", err, stdout)
	}
	if !strings.Contains(stderr, "has no Object Lock") {
		t.Fatalf("stderr = %q, want the lock warning", stderr)
	}
}

// The missing-backup alert: with --max-age, a mirror whose newest bundle is older
// (or that holds none) exits Degraded, so a scheduler or the nightly drill sees it.
func TestDRListOffsiteMaxAgeAlert(t *testing.T) {
	_, mock, flags := newMockOffsite(t)
	mock.lockConfig = complianceLock
	args := append([]string{"ls", "--offsite", "--max-age", "26h"}, flags...)

	out, err := runDR(t, args...)
	if exitcode.From(err) != exitcode.Degraded || !strings.Contains(errText(err), "no offsite bundle newer than 26h0m0s") {
		t.Fatalf("empty mirror: err=%v (code %d)\n%s", err, exitcode.From(err), out)
	}

	mock.objects["old.drbundle"] = []byte("old")
	out, err = runDR(t, args...)
	if exitcode.From(err) != exitcode.Degraded || !strings.Contains(errText(err), "newest is old.drbundle") {
		t.Fatalf("stale mirror: err=%v (code %d)\n%s", err, exitcode.From(err), out)
	}

	mock.objects["new.drbundle"] = []byte("new")
	mock.modified = map[string]time.Time{"new.drbundle": time.Now().Add(-time.Hour)}
	if out, err = runDR(t, args...); err != nil {
		t.Fatalf("fresh mirror raised the alert: %v\n%s", err, out)
	}
}
