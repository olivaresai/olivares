// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestDROffsiteImmutableLiveBucket runs against the Object Lock bucket and the
// write-only credential scripts/dr-offsite-targets.sh provisions on MinIO: the
// writer credential may put and read but not delete, and the pruner credential is
// a different one. The ordinary suite never needs a bucket.
func TestDROffsiteImmutableLiveBucket(t *testing.T) {
	endpoint := os.Getenv("OLIVARES_TEST_OFFSITE_ENDPOINT")
	lockBucket := os.Getenv("OLIVARES_TEST_OFFSITE_LOCK_BUCKET")
	if endpoint == "" || lockBucket == "" {
		t.Skip("run scripts/dr-offsite-targets.sh for the Object Lock qualification")
	}
	target := func(bucket, access, secret, token string) offsiteFlags {
		return offsiteFlags{endpoint: endpoint, bucket: bucket, region: "us-east-1", prefix: "immutable",
			akidFile: os.Getenv(access), secretFile: os.Getenv(secret), tokenFile: os.Getenv(token)}
	}
	args := func(f offsiteFlags) []string {
		out := []string{"--offsite-endpoint", f.endpoint, "--offsite-bucket", f.bucket, "--offsite-region", f.region,
			"--offsite-prefix", f.prefix, "--offsite-access-key-id-file", f.akidFile, "--offsite-secret-access-key-file", f.secretFile}
		if f.tokenFile != "" {
			out = append(out, "--offsite-session-token-file", f.tokenFile)
		}
		return out
	}
	writerTarget := target(lockBucket, "OLIVARES_TEST_OFFSITE_WRITER_ACCESS_FILE", "OLIVARES_TEST_OFFSITE_WRITER_SECRET_FILE", "OLIVARES_TEST_OFFSITE_WRITER_TOKEN_FILE")
	writer := args(writerTarget)
	pruner := args(target(lockBucket, "OLIVARES_TEST_OFFSITE_ACCESS_FILE", "OLIVARES_TEST_OFFSITE_SECRET_FILE", ""))
	unlocked := args(target("dr-qualification", "OLIVARES_TEST_OFFSITE_ACCESS_FILE", "OLIVARES_TEST_OFFSITE_SECRET_FILE", ""))

	prevVersion := version
	version = "1.900"
	t.Cleanup(func() { version = prevVersion })
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	seedDataDir(t, src)
	pass := filepath.Join(dir, "pass")
	if err := os.WriteFile(pass, []byte("disposable DR qualification passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(want int, args ...string) string {
		t.Helper()
		out, err := runDR(t, args...)
		if got := exitcode.From(err); err == nil && want != exitcode.OK || err != nil && got != want {
			t.Fatalf("dr %s: %v (exit %d), want exit %d\n%s", args[0], err, got, want, out)
		}
		if err != nil {
			out += err.Error()
		}
		return out
	}
	backup := func(name string, flags []string) []string {
		return append([]string{"backup", "--data-dir", src, "--engine", "sqlite", "--out", filepath.Join(dir, name),
			"--passphrase-file", pass, "--gfs-daily", "1", "--offsite-immutable"}, flags...)
	}

	// 1. The write-only credential backs up twice. GFS would expire the first copy;
	// the writer leaves it in place.
	run(exitcode.OK, backup("a-old.drbundle", writer)...)
	out := run(exitcode.OK, backup("z-current.drbundle", writer)...)
	if !strings.Contains(out, "locked offsite in COMPLIANCE mode") || strings.Contains(out, "GFS pruned offsite") {
		t.Fatalf("immutable backup:\n%s", out)
	}
	out = run(exitcode.OK, append([]string{"ls", "--offsite"}, writer...)...)
	if !strings.Contains(out, "a-old.drbundle") || !strings.Contains(out, "z-current.drbundle") || strings.Contains(out, "warning:") {
		t.Fatalf("writer listing of the locked bucket:\n%s", out)
	}

	// 2. A Delete with the write credential fails at the bucket itself (AccessDenied),
	// not only in our client, which refuses it before sending.
	raw, err := writerTarget.client()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Delete(context.Background(), "a-old.drbundle"); err == nil || !strings.Contains(err.Error(), "HTTP 403: AccessDenied") {
		t.Fatalf("delete with the write credential = %v, want HTTP 403 AccessDenied", err)
	}

	// 3. The separate pruner deletes what GFS expires and keeps the rest locked.
	out = run(exitcode.OK, append([]string{"prune", "--offsite", "--gfs-daily", "1"}, pruner...)...)
	if !strings.Contains(out, "GFS pruned offsite bundle: a-old.drbundle") || !strings.Contains(out, ": z-current.drbundle") {
		t.Fatalf("pruner:\n%s", out)
	}
	pulled := filepath.Join(dir, "pulled.drbundle")
	run(exitcode.OK, append([]string{"pull", "--name", "z-current.drbundle", "--out", pulled}, writer...)...)
	if info, err := os.Stat(pulled); err != nil || info.Size() == 0 {
		t.Fatalf("kept bundle not readable after the prune: %v", err)
	}

	// 4. The missing-backup alert fires once the newest copy is older than allowed.
	time.Sleep(2 * time.Second)
	out = run(exitcode.Degraded, append([]string{"ls", "--offsite", "--max-age", "1s"}, writer...)...)
	if !strings.Contains(out, "offsite alert: no offsite bundle newer than 1s") {
		t.Fatalf("missing-backup alert did not fire:\n%s", out)
	}

	// 5. A bucket without Object Lock is named, and the immutable writer refuses it.
	out = run(exitcode.OK, append([]string{"ls", "--offsite"}, unlocked...)...)
	if !strings.Contains(out, "warning: offsite bucket dr-qualification has no Object Lock") {
		t.Fatalf("unlocked bucket listing:\n%s", out)
	}
	out = run(exitcode.Err, backup("refused.drbundle", unlocked)...)
	if !strings.Contains(out, "has no Object Lock") {
		t.Fatalf("immutable backup to an unlocked bucket:\n%s", out)
	}
	t.Log("Object Lock bucket: write-only backups, server-side delete denial, separate GFS pruner, missing-backup alert and unlocked-bucket refusal passed")
}
