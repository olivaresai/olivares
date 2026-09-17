// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRLF1DatabaseCustody(t *testing.T) {
	observed := roleLabDatabase{OID: 41, OwnerOID: 10, Name: "ra2p1_lab_fixture"}
	for _, tc := range []struct {
		name    string
		row     roleLabDatabase
		owner   int64
		err     error
		confirm bool
	}{
		{"confirmed", observed, 10, nil, true},
		{"catalog unavailable after CREATE", observed, 10, context.DeadlineExceeded, false},
		{"catalog absent after CREATE", observed, 10, sql.ErrNoRows, false},
		{"different owner", observed, 11, nil, false},
		{"missing session owner", observed, 0, nil, false},
		{"different name", roleLabDatabase{OID: 41, OwnerOID: 10, Name: "other"}, 10, nil, false},
		{"missing OID", roleLabDatabase{OwnerOID: 10, Name: observed.Name}, 10, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned, err := confirmRoleLabDatabase(observed.Name, tc.row, tc.owner, tc.err)
			if (owned != nil) != tc.confirm || (err == nil) != tc.confirm {
				t.Fatalf("confirmation custody=%v error=%v, want confirmed=%v", owned, err, tc.confirm)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		owned   *roleLabDatabase
		row     roleLabDatabase
		err     error
		drop    bool
		wantErr bool
	}{
		{"unconfirmed CREATE cannot adopt visible candidate", nil, observed, nil, false, false},
		{"unconfirmed CREATE cannot claim absence", nil, roleLabDatabase{}, sql.ErrNoRows, false, false},
		{"confirmed identity including later setup failure", &observed, observed, nil, true, false},
		{"replacement OID", &observed, roleLabDatabase{OID: 42, OwnerOID: 10, Name: observed.Name}, nil, false, true},
		{"changed owner", &observed, roleLabDatabase{OID: 41, OwnerOID: 11, Name: observed.Name}, nil, false, true},
		{"changed name", &observed, roleLabDatabase{OID: 41, OwnerOID: 10, Name: "other"}, nil, false, true},
		{"absent recorded database", &observed, roleLabDatabase{}, sql.ErrNoRows, false, true},
		{"unavailable recheck", &observed, observed, context.DeadlineExceeded, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drop, err := roleLabDropAllowed(tc.owned, tc.row, tc.err)
			if drop != tc.drop || (err != nil) != tc.wantErr {
				t.Fatalf("drop=%v error=%v, want drop=%v error=%v", drop, err, tc.drop, tc.wantErr)
			}
		})
	}
}

func TestRLF1AuthenticationClassification(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		err        error
		login      bool
		wantErr    bool
	}{
		{"accepted", "no SQLSTATE", nil, true, false},
		{"authorization refused", "28000", &pgconn.PgError{Code: "28000", Message: "synthetic private detail"}, false, false},
		{"password refused wrapped", "28P01", fmt.Errorf("private wrapper: %w", &pgconn.PgError{Code: "28P01"}), false, false},
		{"CONNECT ACL refused", "42501", &pgconn.PgError{Code: "42501"}, false, true},
		{"database missing", "3D000", &pgconn.PgError{Code: "3D000"}, false, true},
		{"server unavailable", "57P03", &pgconn.PgError{Code: "57P03"}, false, true},
		{"transport", "no SQLSTATE", io.ErrUnexpectedEOF, false, true},
		{"timeout", "no SQLSTATE", context.DeadlineExceeded, false, true},
		{"canceled", "no SQLSTATE", context.Canceled, false, true},
		{"invalid state", "no SQLSTATE", &pgconn.PgError{Code: "secret text"}, false, true},
		{"invalid five characters", "no SQLSTATE", &pgconn.PgError{Code: "28\n01"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			login, err := roleLabAuthenticationResult(tc.err)
			if login != tc.login || (err != nil) != tc.wantErr {
				t.Fatalf("login=%v error=%v, want login=%v error=%v", login, err, tc.login, tc.wantErr)
			}
			if code := roleLabSQLState(tc.err); code != tc.code {
				t.Fatalf("diagnostic=%q, want %q", code, tc.code)
			}
			if tc.wantErr && !errors.Is(err, tc.err) {
				t.Fatal("infrastructure cause was lost")
			}
		})
	}
}

func TestRLF1LoginDatabaseSelection(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://fixture:synthetic@localhost/maintenance?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	lab := &roleLab{t: t, superCfg: cfg, database: &roleLabDatabase{Name: "ra2p1_lab_fixture"}}
	login := lab.loginConfig("executor", "synthetic-executor")
	if login.Database != lab.database.Name || login.User != "executor" || login.Password != "synthetic-executor" {
		t.Fatal("login configuration did not select the confirmed database and requested identity")
	}
	login.RuntimeParams["search_path"] = "pg_catalog"
	if cfg.Database != "maintenance" || cfg.User != "fixture" || cfg.Password != "synthetic" || cfg.RuntimeParams["search_path"] != "" {
		t.Fatal("copied login configuration mutated the maintenance configuration")
	}
}

func TestRLF1FixtureAccounting(t *testing.T) {
	own := fmt.Sprintf("p%d_token", os.Getpid())
	foreign := fmt.Sprintf("p%d_token", os.Getpid()+1)
	for _, tc := range []struct {
		name        string
		role, owned bool
	}{
		{"ra2p1_app_" + own, true, true},
		{"ra2p1_lab_" + own, true, true},
		{"ra2p1_app_" + foreign, false, false},
		{"ra2p1_lab_" + foreign, false, false},
		{"ra2p1_untagged", true, true},
		{"ra2p1_bad_pbad_token", true, true},
		{"olv_existing_" + own, true, true},
		{"olv_existing_" + foreign, false, false},
		{"unrelated", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if clusterFixtureRole(tc.name) != tc.role || clusterFixtureOwned(tc.name) != tc.owned {
				t.Fatal("fixture prefix/process attribution changed")
			}
		})
	}
}

func TestRLF1RequiredSnapshotExit(t *testing.T) {
	for _, required := range []bool{false, true} {
		for _, beforeOK := range []bool{false, true} {
			for _, afterOK := range []bool{false, true} {
				for _, code := range []int{0, 7} {
					t.Run(fmt.Sprintf("required=%v/before=%v/after=%v/code=%d", required, beforeOK, afterOK, code), func(t *testing.T) {
						got := clusterGateExitCode(code, required, beforeOK, afterOK, nil)
						if code != 0 && got != code {
							t.Fatal("existing failure code was replaced")
						}
						if code == 0 && (got != 0) != (required && (!beforeOK || !afterOK)) {
							t.Fatal("required unreadable snapshot did not propagate failure")
						}
					})
				}
			}
		}
	}
	if clusterGateExitCode(0, true, true, true, []string{"leak"}) == 0 {
		t.Fatal("observed leak did not fail the package")
	}
}

func TestRLF1ClusterComparison(t *testing.T) {
	before := clusterStateFingerprint{Roles: []string{"old_role"}, Databases: []string{"old_db"}, AppRoleInherits: true, PublicCanReadPgRoles: true}
	if got := clusterStateProblems(before, before); len(got) != 0 {
		t.Fatalf("unchanged snapshot failed: %v", got)
	}
	after := before
	after.Roles = []string{"old_role", "ra2p1_new_role"}
	after.Databases = []string{"old_db", "ra2p1_lab_new"}
	after.AppRoleInherits = false
	after.PublicCanReadPgRoles = false
	after.MaintenanceDatacl = "{=Tc/fixture,fixture=CTc/fixture}"
	if got := clusterStateProblems(before, after); len(got) != 5 {
		t.Fatalf("lost an existing posture/leak or exact ACL oracle: %v", got)
	}
	for _, acl := range []string{"", "{fixture=CTc/fixture,=Tc/fixture}"} {
		a := after
		a.MaintenanceDatacl = acl
		if got := clusterStateProblems(after, a); len(got) != 1 || !strings.Contains(got[0], "datacl representation") {
			t.Fatalf("different ACL representations were accepted as equal: %v", got)
		}
	}
}

// This in-memory driver exercises the real shared reader's iteration and closure.
// It opens no sockets and performs no PostgreSQL operations.
func TestRLF1SnapshotCompleteness(t *testing.T) {
	for _, failure := range []string{"none", "roles iteration", "databases iteration", "roles close", "databases close", "roles scan", "databases scan", "ACL query"} {
		t.Run(failure, func(t *testing.T) {
			conn := &rlf1SnapshotConn{failure: failure}
			db := sql.OpenDB(rlf1SnapshotConnector{conn})
			db.SetMaxOpenConns(1)
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			fp, err := readClusterSnapshot(context.Background(), db)
			if (err == nil) != (failure == "none") {
				t.Fatalf("snapshot error=%v for %s", err, failure)
			}
			if failure == "none" {
				want := clusterStateFingerprint{Roles: []string{"ra2p1_app_owned"}, Databases: []string{"ra2p1_lab_owned"}, AppRoleInherits: true, PublicCanReadPgRoles: true, MaintenanceDatacl: "exact ACL"}
				if !reflect.DeepEqual(fp, want) {
					t.Fatalf("snapshot=%+v, want %+v", fp, want)
				}
			} else if !reflect.DeepEqual(fp, clusterStateFingerprint{}) {
				t.Fatal("incomplete snapshot exposed a usable partial fingerprint")
			}
			if conn.openRows != 0 {
				t.Fatal("snapshot returned with open result sets")
			}
		})
	}
}

type rlf1SnapshotConnector struct{ conn *rlf1SnapshotConn }

func (c rlf1SnapshotConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c rlf1SnapshotConnector) Driver() driver.Driver                        { return rlf1SnapshotDriver{} }

type rlf1SnapshotDriver struct{}

func (rlf1SnapshotDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type rlf1SnapshotConn struct {
	failure  string
	openRows int
}

func (*rlf1SnapshotConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*rlf1SnapshotConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (*rlf1SnapshotConn) Close() error              { return nil }

func (c *rlf1SnapshotConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.openRows != 0 {
		return nil, errors.New("previous result set was not closed")
	}
	rows := &rlf1SnapshotRows{conn: c}
	switch {
	case strings.HasPrefix(query, "SELECT rolname"):
		rows.kind, rows.values = "roles", []driver.Value{"ra2p1_app_owned", fmt.Sprintf("ra2p1_app_p%d_foreign", os.Getpid()+1), "unrelated"}
	case strings.HasPrefix(query, "SELECT datname"):
		rows.kind, rows.values = "databases", []driver.Value{"ra2p1_lab_owned", fmt.Sprintf("ra2p1_lab_p%d_foreign", os.Getpid()+1)}
	case strings.Contains(query, "datacl"):
		if c.failure == "ACL query" {
			return nil, errors.New("injected ACL query failure")
		}
		rows.values = []driver.Value{"exact ACL"}
	default:
		rows.values = []driver.Value{true}
	}
	if c.failure == rows.kind+" scan" {
		rows.values = []driver.Value{nil}
	}
	c.openRows++
	return rows, nil
}

type rlf1SnapshotRows struct {
	conn   *rlf1SnapshotConn
	kind   string
	values []driver.Value
	closed bool
}

func (*rlf1SnapshotRows) Columns() []string { return []string{"value"} }
func (r *rlf1SnapshotRows) Close() error {
	if !r.closed {
		r.closed = true
		r.conn.openRows--
	}
	if r.conn.failure == r.kind+" close" {
		return errors.New("injected row close failure")
	}
	return nil
}
func (r *rlf1SnapshotRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		if r.conn.failure == r.kind+" iteration" {
			return errors.New("injected late iteration failure")
		}
		return io.EOF
	}
	dest[0], r.values = r.values[0], r.values[1:]
	return nil
}
