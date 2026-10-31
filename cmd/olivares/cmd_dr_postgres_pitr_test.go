// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// POINT-IN-TIME RECOVERY, measured on a real server of each supported major.
//
// The cell below does what docs/DR-RUNBOOK.md §6-7 and deploy/postgres/backup/
// pitr-setup.md tell an operator to do, with the major's own binaries: a cluster
// that archives its WAL, a pg_basebackup, the `dr backup --pitr-ref` companion
// bundle, more audit events, a recovery to an instant BETWEEN two of them, and
// `dr restore` of the companion against the recovered cluster. The assertion is
// the one the runbook promises: the chain verifies and ends at the event before
// the instant, not at the one after it.
//
// It starts its own servers, so it needs the server binaries of each major
// (initdb, pg_ctl, postgres, pg_basebackup), not a DSN. They are found in
// /usr/lib/postgresql/<major>/bin or /usr/pgsql-<major>/bin, or named in
// OLIVARES_TEST_PITR_BINDIRS ("15=/dir,16=/dir"). A major whose binaries are
// absent SKIPS the cell; OLIVARES_TEST_PITR_REQUIRED=1 makes that a failure, which
// is what the CI job sets, because a skipped leg is not a passing one.

const (
	pitrBinDirsEnv  = "OLIVARES_TEST_PITR_BINDIRS"
	pitrRequiredEnv = "OLIVARES_TEST_PITR_REQUIRED"
)

var pitrMajors = []int{15, 16, 17, 18}

// pitrBinDir finds the server binaries of one major, or skips (fails when the
// run requires every major).
func pitrBinDir(t *testing.T, major int) string {
	t.Helper()
	var candidates []string
	for _, pair := range strings.Split(os.Getenv(pitrBinDirsEnv), ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && k == strconv.Itoa(major) {
			candidates = append(candidates, v)
		}
	}
	candidates = append(candidates,
		filepath.Join("/usr/lib/postgresql", strconv.Itoa(major), "bin"),
		filepath.Join("/usr/pgsql-"+strconv.Itoa(major), "bin"))
	for _, dir := range candidates {
		ok := true
		for _, tool := range []string{"initdb", "pg_ctl", "postgres", "pg_basebackup"} {
			if _, err := os.Stat(filepath.Join(dir, tool)); err != nil {
				ok = false
			}
		}
		if ok {
			return dir
		}
	}
	msg := fmt.Sprintf("no PostgreSQL %d server binaries (initdb, pg_ctl, postgres, pg_basebackup); set %s", major, pitrBinDirsEnv)
	if v := os.Getenv(pitrRequiredEnv); v == "1" || v == "true" {
		t.Fatal(msg)
	}
	t.Skip(msg)
	return ""
}

// pitrCluster is one native PostgreSQL server, driven with its own major's tools.
type pitrCluster struct {
	t     *testing.T
	major int
	bin   string
	data  string
	port  int
	log   string
}

func (c *pitrCluster) cmd(tool string, args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(c.bin, tool), args...) // #nosec G204 -- test-only: a fixed tool name in the major's own bin dir
	// An extracted runtime keeps its libraries beside its bin dir; a system
	// install finds the same paths on its own, so setting them is harmless there.
	prefix := filepath.Clean(filepath.Join(c.bin, "../../../../.."))
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+strings.Join([]string{
		filepath.Join(prefix, "usr/lib/postgresql", strconv.Itoa(c.major), "lib"),
		filepath.Join(prefix, "usr/lib/x86_64-linux-gnu"),
		filepath.Join(prefix, "lib/x86_64-linux-gnu"),
	}, ":"))
	return cmd
}

func (c *pitrCluster) run(tool string, args ...string) {
	c.t.Helper()
	if out, err := c.cmd(tool, args...).CombinedOutput(); err != nil {
		c.t.Fatalf("%s %s (PostgreSQL %d): %v\n%s", tool, strings.Join(args, " "), c.major, err, out)
	}
}

// startCluster initialises a cluster in dir with the server's own defaults for
// connections and locks, listening on loopback only. extra is appended to
// postgresql.conf.
func startCluster(t *testing.T, major int, bin, dir, extra string) *pitrCluster {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &pitrCluster{t: t, major: major, bin: bin, data: filepath.Join(dir, "data"), port: freeLoopbackPort(t), log: filepath.Join(dir, "server.log")}
	c.run("initdb", "-D", c.data, "-U", "postgres", "-A", "trust", "--encoding=UTF8", "--locale=C")
	appendFileOrFatal(t, filepath.Join(c.data, "postgresql.conf"), fmt.Sprintf(`
listen_addresses = '127.0.0.1'
port = %d
unix_socket_directories = ''
shared_buffers = 32MB
timezone = 'UTC'
log_timezone = 'UTC'
%s
`, c.port, extra))
	c.start()
	return c
}

// startPITRCluster is a cluster that archives its WAL into archive.
func startPITRCluster(t *testing.T, major int, bin, dir, archive string) *pitrCluster {
	t.Helper()
	return startCluster(t, major, bin, dir, fmt.Sprintf(`wal_level = replica
archive_mode = on
archive_command = 'test ! -f "%s/%%f" && cp "%%p" "%s/%%f"'`, archive, archive))
}

// useMajor points the process at the major's own libraries, so the pg_dump and
// pg_restore the product runs are that major's (an extracted runtime keeps them
// beside its bin dir).
func (c *pitrCluster) useMajor() {
	c.t.Helper()
	for _, kv := range c.cmd("true").Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "LD_LIBRARY_PATH" {
			c.t.Setenv(k, v)
		}
	}
}

func appendFileOrFatal(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func (c *pitrCluster) start() {
	c.t.Helper()
	c.run("pg_ctl", "-D", c.data, "-l", c.log, "-w", "-t", "120", "start")
	c.t.Cleanup(func() { _ = c.cmd("pg_ctl", "-D", c.data, "-m", "immediate", "stop").Run() })
}

func (c *pitrCluster) stop() {
	c.t.Helper()
	c.run("pg_ctl", "-D", c.data, "-m", "fast", "-w", "-t", "120", "stop")
}

func (c *pitrCluster) host() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.port)) }

func (c *pitrCluster) superDSN() string {
	return "postgres://postgres@" + c.host() + "/postgres?sslmode=disable"
}

// tryScalar runs one query as the superuser and returns its single text value.
func (c *pitrCluster) tryScalar(query string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, c.superDSN())
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(ctx) }()
	var v string
	err = conn.QueryRow(ctx, query).Scan(&v)
	return v, err
}

// scalar is tryScalar that fails the test on an error.
func (c *pitrCluster) scalar(query string) string {
	c.t.Helper()
	v, err := c.tryScalar(query)
	if err != nil {
		c.t.Fatalf("query %q on PostgreSQL %d: %v", query, c.major, err)
	}
	return v
}

// onServer is the same estate seen through another server's address: after a
// recovery the roles and the database came back with the data, only the port changed.
func (f *pgSplitFixture) onServer(t *testing.T, host string) *pgSplitFixture {
	t.Helper()
	g := *f
	for _, dsn := range []*string{&g.appDSN, &g.ownerDSN, &g.adminDSN} {
		u, err := url.Parse(*dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.Host = host
		*dsn = u.String()
	}
	return &g
}

// appendEvent adds one audit event to an existing tenant by slug.
func (f *pgSplitFixture) appendEvent(t *testing.T, dataDir, slug, actor string) {
	t.Helper()
	prepareCompositionTestBoot(t)
	defer prepareCompositionTestBoot(t)
	ctx := t.Context()
	eng, err := boot(ctx, bootConfig{
		DataDir: dataDir, Engine: "postgres",
		DSN: f.appDSN, OwnerDSN: f.ownerDSN, AdminDSN: f.adminDSN, Version: "test",
	})
	if err != nil {
		t.Fatalf("boot to append %s: %v", actor, err)
	}
	defer func() { _ = eng.Close() }()
	var tid model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		for _, o := range orgs {
			if o.Slug == slug {
				tid = o.TenantID
			}
		}
		return err
	}); err != nil || tid == "" {
		t.Fatalf("find org %q: %v", slug, err)
	}
	if err := eng.store.Mutate(ctx, tid, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: "user", Action: "agent.create", TargetKind: "core.agent"})
		return err
	}); err != nil {
		t.Fatalf("append %s: %v", actor, err)
	}
}

func TestDRPostgresPITRRestoresToAnInstantBetweenTwoAuditEvents(t *testing.T) {
	for _, major := range pitrMajors {
		t.Run(fmt.Sprintf("PostgreSQL %d", major), func(t *testing.T) {
			bin := pitrBinDir(t, major)
			stampVersion(t)
			root := t.TempDir()
			archive := filepath.Join(root, "wal-archive")
			if err := os.Mkdir(archive, 0o700); err != nil {
				t.Fatal(err)
			}
			primary := startPITRCluster(t, major, bin, filepath.Join(root, "primary"), archive)
			t.Setenv(pgProbeDSN, primary.superDSN())
			if got := primary.scalar(`SELECT (current_setting('server_version_num')::int / 10000)::text`); got != strconv.Itoa(major) {
				t.Fatalf("started the wrong major: asked for %d, the server is %s", major, got)
			}

			// The estate: three tenants, checkpointed, on the archiving primary.
			est := provisionPGSplitFixture(t, primary.superDSN(), "", "pitr", true)
			dataDir := filepath.Join(root, "node")
			if err := os.Mkdir(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			est.seed(t, dataDir)

			// Base backup, then the companion bundle taken right after it.
			base := filepath.Join(root, "base")
			primary.run("pg_basebackup", "-h", "127.0.0.1", "-p", strconv.Itoa(primary.port), "-U", "postgres",
				"-D", base, "-F", "p", "-X", "stream", "-c", "fast", "-w")
			pf := passphraseFile(t)
			companion := filepath.Join(root, "companion.drbundle")
			args := append([]string{"backup", "--data-dir", dataDir}, est.drArgs()...)
			args = append(args, "--pitr-ref", "wal-archive://"+filepath.Base(archive), "--out", companion, "--passphrase-file", pf)
			if out, err := runDR(t, args...); err != nil {
				t.Fatalf("PITR companion backup: %v\n%s", err, out)
			}

			// Two events on one tenant with the instant between them.
			const chainEnd = `SELECT coalesce(max(seq), 0) FROM audit_events
			  WHERE tenant_id = (SELECT tenant_id FROM orgs WHERE slug = 'acme-0')`
			seqBefore := est.superScalar(t, chainEnd)
			est.appendEvent(t, dataDir, "acme-0", "pitr:x1")
			instant := primary.scalar(`SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '+00'`)
			time.Sleep(300 * time.Millisecond)
			est.appendEvent(t, dataDir, "acme-0", "pitr:x2")
			if got := est.superScalar(t, chainEnd); got != seqBefore+2 {
				t.Fatalf("fixture: the live chain ends at seq %d, want %d", got, seqBefore+2)
			}

			// Ship the last segment, then lose the primary.
			lastWAL := primary.scalar(`SELECT pg_walfile_name(pg_switch_wal())`)
			deadline := time.Now().Add(60 * time.Second)
			for primary.scalar(`SELECT coalesce(last_archived_wal, '') FROM pg_stat_archiver`) < lastWAL {
				if time.Now().After(deadline) {
					t.Fatalf("the archiver did not ship %s within a minute", lastWAL)
				}
				time.Sleep(200 * time.Millisecond)
			}
			if failed := primary.scalar(`SELECT failed_count::text FROM pg_stat_archiver`); failed != "0" {
				t.Fatalf("the archiver reported %s failures", failed)
			}
			primary.stop()

			// Recover the base backup to the instant.
			recovered := &pitrCluster{t: t, major: major, bin: bin, data: filepath.Join(root, "recovered"), port: freeLoopbackPort(t), log: filepath.Join(root, "recovered.log")}
			if out, err := exec.Command("cp", "-a", base, recovered.data).CombinedOutput(); err != nil {
				t.Fatalf("copy the base backup: %v\n%s", err, out)
			}
			appendFileOrFatal(t, filepath.Join(recovered.data, "postgresql.auto.conf"), fmt.Sprintf(`
port = %d
archive_mode = off
restore_command = 'cp "%s/%%f" "%%p"'
recovery_target_time = '%s'
recovery_target_action = 'promote'
`, recovered.port, archive, instant))
			if err := os.WriteFile(filepath.Join(recovered.data, "recovery.signal"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			replayStart := time.Now()
			recovered.start()
			t.Setenv(pgProbeDSN, recovered.superDSN())
			// A server still starting up refuses connections: keep asking until it
			// answers that recovery is over.
			for {
				if v, err := recovered.tryScalar(`SELECT pg_is_in_recovery()::text`); err == nil && v == "false" {
					break
				}
				if time.Since(replayStart) > 120*time.Second {
					t.Fatal("the recovered cluster did not finish recovery within two minutes")
				}
				time.Sleep(200 * time.Millisecond)
			}
			replay := time.Since(replayStart)
			logText, _ := os.ReadFile(recovered.log)
			// The server says where it stopped: before the first transaction past the
			// instant, whether that one would have committed or aborted.
			if !strings.Contains(string(logText), "recovery stopping before ") {
				t.Fatalf("recovery did not stop at the target instant %s:\n%s", instant, logText)
			}

			// The recovered estate, seen through its new address.
			rec := est.onServer(t, recovered.host())
			if got := rec.superScalar(t, chainEnd); got != seqBefore+1 {
				t.Fatalf("after recovery the chain ends at seq %d, want %d (the event before the instant)", got, seqBefore+1)
			}
			if rec.superScalar(t, `SELECT count(*) FROM audit_events WHERE actor = 'pitr:x1'`) != 1 ||
				rec.superScalar(t, `SELECT count(*) FROM audit_events WHERE actor = 'pitr:x2'`) != 0 {
				t.Fatal("recovery kept the event after the instant or lost the one before it")
			}

			// The companion proves it: keys installed, chain, per-event signatures
			// and checkpoints verified against the recovered cluster.
			restoreDir := t.TempDir()
			restoreArgs := append([]string{"restore", "--data-dir", restoreDir}, rec.drArgs()...)
			restoreArgs = append(restoreArgs, "--in", companion, "--passphrase-file", pf,
				"--operator", "oncall@example.test", "--reason", "PITR drill")
			restoreStart := time.Now()
			out, err := runDR(t, restoreArgs...)
			if err != nil {
				t.Fatalf("dr restore of the companion against the recovered cluster: %v\n%s", err, out)
			}
			t.Logf("PostgreSQL %d: WAL replay to the instant and promote %s; dr restore of the companion %s",
				major, replay.Round(time.Millisecond), time.Since(restoreStart).Round(time.Millisecond))
			if !strings.Contains(out, "restore verified") {
				t.Fatalf("the restore did not report the ledger as verified:\n%s", out)
			}
			// The chain ENDS at the first event itself, not merely at a count.
			if rec.superScalar(t, `SELECT count(*) FROM audit_events WHERE actor = 'pitr:x1' AND seq = $1
			  AND tenant_id = (SELECT tenant_id FROM orgs WHERE slug = 'acme-0')`, seqBefore+1) != 1 {
				t.Fatal("the last event of the recovered chain is not the first of the two")
			}
			// The act is recorded in the recovered ledger, as for any declared restore.
			if rec.superScalar(t, `SELECT count(*) FROM audit_events WHERE action = $1 AND actor = $2`,
				drRestoreDeclarationAction, "operator:oncall@example.test") != 1 {
				t.Fatal("the companion restore was not recorded in the recovered ledger")
			}

			// A tenant created after the companion was taken is not in its manifest,
			// and the verification says so instead of passing it: take a new
			// companion after creating a tenant (docs/DR-RUNBOOK.md §9).
			rec.moveOnAfterBackup(t, restoreDir)
			_, err = runDR(t, append(restoreArgs, "--force")...)
			if err == nil || !strings.Contains(err.Error(), "not in the manifest") {
				t.Fatalf("a tenant created after the companion must be reported, got: %v", err)
			}
			// A companion carries no store bytes: nothing is replaced, so no safety copy of
			// the (occupied) recovered database may be taken for it.
			if copies, _ := filepath.Glob(filepath.Join(restoreDir, "database.pgcustom.*")); len(copies) != 0 {
				t.Fatalf("a PITR companion replaces no database, yet a safety copy was taken: %v", copies)
			}
		})
	}
}
