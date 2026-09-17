// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	olivaresclient "github.com/olivaresai/olivares/clients/go"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type communicationHTTPTestResponse struct {
	status int
	header http.Header
	raw    []byte
}

func communicationHTTPTestPtr[T any](value T) *T { return &value }

// communicationHTTPTestStore is the owned boot, writer-activation and restart
// configuration for the authenticated HTTP journey. SQLite uses DataDir only.
// PostgreSQL uses Engine postgres plus file-backed application, owner and admin
// DSN references so every reopen and the real activate-directory-writer CLI
// receive the same roles and database. The fixture never selects a store through
// unscoped DSN environment or a package-level mutable map.
type communicationHTTPTestStore struct {
	dataDir      string
	engine       string
	dsnFile      string
	ownerDSNFile string
	adminDSNFile string
}

func communicationHTTPTestSQLiteStore(t *testing.T) communicationHTTPTestStore {
	t.Helper()
	return communicationHTTPTestStore{dataDir: t.TempDir(), engine: "sqlite"}
}

func communicationHTTPTestPostgresStore(t *testing.T) communicationHTTPTestStore {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		required, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("OLIVARES_TEST_POSTGRES_REQUIRED")))
		if err == nil && required {
			t.Fatal("PostgreSQL HTTP journey is required for this run and no isolated server is available")
		}
		t.Skipf("set %s to run the PostgreSQL HTTP journey; IsolatedPostgresSplitOwner provisions a private split-owner database",
			enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	dataDir := t.TempDir()
	estate := communicationHTTPTestStore{
		dataDir:      dataDir,
		engine:       "postgres",
		dsnFile:      writeCommunicationHTTPTestDSNFile(t, dataDir, "app.dsn", pg.App),
		ownerDSNFile: writeCommunicationHTTPTestDSNFile(t, dataDir, "owner.dsn", pg.Owner),
		adminDSNFile: writeCommunicationHTTPTestDSNFile(t, dataDir, "admin.dsn", pg.Admin),
	}
	assertCommunicationHTTPTestPostgresPosture(t, estate, pg.Database)
	return estate
}

func writeCommunicationHTTPTestDSNFile(t *testing.T, dataDir, name, dsn string) string {
	t.Helper()
	dir := filepath.Join(dataDir, "store")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create owned store DSN directory: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(dsn), 0o600); err != nil {
		t.Fatalf("write owned store DSN file: %v", err)
	}
	return path
}

func (s communicationHTTPTestStore) dsnRef(path string) string { return "file:" + path }

func (s communicationHTTPTestStore) bootConfig() bootConfig {
	engineName := s.engine
	if engineName == "" {
		engineName = "sqlite"
	}
	cfg := bootConfig{
		DataDir: s.dataDir, Engine: engineName, Version: "test", NoIngest: true, ServeMode: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if engineName == "postgres" {
		cfg.DSN = s.dsnRef(s.dsnFile)
		cfg.OwnerDSN = s.dsnRef(s.ownerDSNFile)
		cfg.AdminDSN = s.dsnRef(s.adminDSNFile)
	}
	return cfg
}

func bootCommunicationHTTPTestEngine(t *testing.T, estate communicationHTTPTestStore) *engine {
	t.Helper()
	prepareCompositionTestBoot(t)
	if estate.dataDir == "" {
		t.Fatal("communication HTTP estate is missing its owned data directory")
	}
	if estate.engine == "postgres" &&
		(estate.dsnFile == "" || estate.ownerDSNFile == "" || estate.adminDSNFile == "") {
		t.Fatal("postgres HTTP estate is missing owned application/owner/admin DSN files")
	}
	eng, err := boot(context.Background(), estate.bootConfig())
	if err != nil {
		t.Fatalf("boot communication HTTP estate: %v", err)
	}
	return eng
}

func activateCommunicationHTTPTestDirectoryWriter(t *testing.T, estate communicationHTTPTestStore) {
	t.Helper()
	args := []string{
		"activate-directory-writer", "--data-dir", estate.dataDir,
		"--expected-generation", "1", "--actor", "k3-http-test",
		"--reason", "fresh owned test estate", "--writers-upgraded", "--writers-drained",
	}
	if estate.engine == "postgres" {
		args = append(args,
			"--engine", "postgres",
			"--dsn", estate.dsnRef(estate.dsnFile),
			"--owner-dsn", estate.dsnRef(estate.ownerDSNFile),
			"--admin-dsn", estate.dsnRef(estate.adminDSNFile),
		)
	}
	if out, err := runDB(t, args...); err != nil {
		t.Fatalf("activate directory writer: %v\n%s", err, out)
	}
}

func assertCommunicationHTTPTestPostgresPosture(t *testing.T, estate communicationHTTPTestStore, database string) {
	t.Helper()
	out, err := runDB(t,
		"check", "--engine", "postgres", "--format", "json", "--strict",
		"--dsn", estate.dsnRef(estate.dsnFile),
		"--owner-dsn", estate.dsnRef(estate.ownerDSNFile),
		"--admin-dsn", estate.dsnRef(estate.adminDSNFile),
	)
	if err != nil {
		t.Fatalf("postgres HTTP role posture check: %v\n%s", err, out)
	}
	var results []dbCheckResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("decode postgres HTTP role posture: %v", err)
	}
	byLabel := make(map[string]dbCheckResult, len(results))
	for _, result := range results {
		byLabel[result.DSN] = result
	}
	app, haveApp := byLabel["--dsn"]
	owner, haveOwner := byLabel["--owner-dsn"]
	admin, haveAdmin := byLabel["--admin-dsn"]
	if !haveApp || !haveOwner || !haveAdmin {
		t.Fatalf("postgres HTTP role posture missing a split role (%d results)", len(results))
	}
	if !app.Accepted || !app.Reachable || app.Superuser || app.BypassRLS || app.Engine != "postgres" || app.Role == "" {
		t.Fatalf("application role posture = %+v, want reachable NOSUPERUSER NOBYPASSRLS", app)
	}
	if !owner.Accepted || !owner.Reachable || owner.Superuser || owner.BypassRLS ||
		owner.Engine != "postgres" || owner.Role == "" || owner.Role == app.Role {
		t.Fatalf("owner role posture = %+v, want distinct reachable NOSUPERUSER NOBYPASSRLS", owner)
	}
	if !admin.Accepted || !admin.Reachable || admin.Superuser || !admin.BypassRLS ||
		admin.Engine != "postgres" || admin.Role == "" {
		t.Fatalf("admin role posture = %+v, want reachable NOSUPERUSER BYPASSRLS", admin)
	}
	version := communicationHTTPTestPostgresServerVersionNum(t, estate)
	if version/10000 != 16 {
		t.Fatalf("postgres HTTP journey measured server_version_num=%d, want PostgreSQL 16", version)
	}
	t.Logf("K3_HTTP_PG_POSTURE|database=%s|app_role=%s|app_superuser=%t|app_bypassrls=%t|owner_role=%s|owner_superuser=%t|owner_bypassrls=%t|admin_role=%s|admin_superuser=%t|admin_bypassrls=%t|server_version_num=%d",
		database, app.Role, app.Superuser, app.BypassRLS, owner.Role, owner.Superuser, owner.BypassRLS,
		admin.Role, admin.Superuser, admin.BypassRLS, version)
}

func communicationHTTPTestPostgresServerVersionNum(t *testing.T, estate communicationHTTPTestStore) int {
	t.Helper()
	dsn, err := resolveDSNRef(context.Background(), "--dsn", estate.dsnRef(estate.dsnFile), os.Getenv)
	if err != nil {
		t.Fatalf("resolve owned postgres application DSN: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres version probe: %v", err)
	}
	defer db.Close() //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var num int
	if err := db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		t.Fatalf("read postgres server_version_num: %v", err)
	}
	return num
}

func communicationHTTPTestRequest(
	t *testing.T,
	eng *engine,
	method string,
	path string,
	token string,
	tenant model.TenantID,
	body any,
	headers map[string]string,
) communicationHTTPTestResponse {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	requestContext, cancelRequest := context.WithTimeout(req.Context(), 2*time.Minute)
	defer cancelRequest()
	req = req.WithContext(requestContext)
	req.RemoteAddr = "127.0.0.1:43210"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if !tenant.IsZero() {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	eng.api.Handler().ServeHTTP(recorder, req)
	return communicationHTTPTestResponse{
		status: recorder.Code, header: recorder.Header().Clone(), raw: recorder.Body.Bytes(),
	}
}

func communicationHTTPTestDecode[T any](t *testing.T, response communicationHTTPTestResponse) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(response.raw, &result); err != nil {
		t.Fatalf("decode HTTP %d response: %v (%s)", response.status, err, response.raw)
	}
	return result
}

func communicationHTTPTestInboxPath(workspace model.ID, limit int, continuation string) string {
	values := url.Values{"workspace_id": {workspace.String()}}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	if continuation != "" {
		values.Set("continuation", continuation)
	}
	return "/v1/m/sessions/inbox?" + values.Encode()
}

func communicationHTTPTestCursorPath(recipient string, workspace model.ID, target string) string {
	values := url.Values{"workspace_id": {workspace.String()}, "target": {target}}
	return "/v1/m/sessions/inbox/cursors/personal/" + url.PathEscape(recipient) + "?" + values.Encode()
}

func communicationHTTPTestRedactError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	text = postgresURLPattern.ReplaceAllString(text, "postgres://<redacted>")
	return text
}

var postgresURLPattern = regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s]+`)

func communicationHTTPTestPgError(err error) string {
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr == nil {
		return ""
	}
	return fmt.Sprintf("sqlstate=%s table=%s constraint=%s routine=%s message=%s",
		pgErr.Code, pgErr.TableName, pgErr.ConstraintName, pgErr.Routine, pgErr.Message)
}

func communicationHTTPTestInboxFailureCensus(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	workspace model.ID,
	recipient model.ID,
	token string,
	page olivaresclient.SessionsCommunicationInboxPage,
	clientErr error,
) {
	t.Helper()
	raw := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(workspace, 1, ""), token, tenant, nil, nil)
	var (
		confineErr, inboxListErr, allDeliveryErr, cursorErr, identityLockErr error
		inboxCount, allCount, cursorCount                                    int
		inboxSeqs, allSeqs                                                   []int64
		deliveryIDs, messageIDs                                              []string
		deliveryGetErrs, messageGetErrs                                      []string
		identityKind                                                         string
		identityVersion                                                      int64
	)
	viewErr := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(context.Background(), sc, workspace)
		if err != nil {
			confineErr = err
			return nil
		}
		deliveries, err := confined.Ext("sessions.message_delivery")
		if err != nil {
			inboxListErr = err
			return nil
		}
		rows, _, err := deliveries.List(context.Background(), model.Query{
			Filters: []model.Filter{
				{Column: "recipient_kind", Op: model.OpEq, Value: "user"},
				{Column: "recipient_ref", Op: model.OpEq, Value: recipient.String()},
				{Column: "delivery_seq", Op: model.OpGt, Value: int64(0)},
			},
			Sort: []model.Sort{{Column: "delivery_seq"}}, Limit: 10,
		})
		inboxListErr = err
		if err == nil {
			inboxCount = len(rows)
			messages, messageRepoErr := confined.Ext("sessions.message")
			for _, row := range rows {
				inboxSeqs = append(inboxSeqs, row.Int("delivery_seq"))
				deliveryID := row.String("id")
				messageID := row.String("message_id")
				deliveryIDs = append(deliveryIDs, deliveryID)
				messageIDs = append(messageIDs, messageID)
				if parsed, parseErr := model.ParseID(deliveryID); parseErr == nil {
					_, getErr := deliveries.Get(context.Background(), parsed)
					deliveryGetErrs = append(deliveryGetErrs, communicationHTTPTestRedactError(getErr)+communicationHTTPTestPgError(getErr))
				}
				if messageRepoErr != nil {
					messageGetErrs = append(messageGetErrs, communicationHTTPTestRedactError(messageRepoErr))
				} else if parsed, parseErr := model.ParseID(messageID); parseErr == nil {
					_, getErr := messages.Get(context.Background(), parsed)
					messageGetErrs = append(messageGetErrs, communicationHTTPTestRedactError(getErr)+communicationHTTPTestPgError(getErr))
				}
			}
		}
		allRows, _, err := deliveries.List(context.Background(), model.Query{Limit: 20})
		allDeliveryErr = err
		if err == nil {
			allCount = len(allRows)
			for _, row := range allRows {
				allSeqs = append(allSeqs, row.Int("delivery_seq"))
			}
		}
		cursors, err := confined.Ext("sessions.inbox_cursor")
		if err != nil {
			cursorErr = err
			return nil
		}
		cursorRows, _, err := cursors.List(context.Background(), model.Query{Limit: 10})
		cursorErr = err
		if err == nil {
			cursorCount = len(cursorRows)
		}
		return nil
	})
	pointReads := make([]string, 0, len(deliveryIDs))
	for _, id := range deliveryIDs {
		point := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/deliveries/"+id, token, tenant, nil, nil)
		pointReads = append(pointReads, fmt.Sprintf("%s=%d", id, point.status))
	}
	epochLockErr := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		locker, ok := sc.(store.AuthoritySnapshotLocker)
		if !ok {
			return errors.New("tenant scope is not an authority snapshot locker")
		}
		return locker.LockAuthoritySnapshot(context.Background(), []store.AuthorizationFactRef{
			{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 1},
		})
	})
	identityLockErr = eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(context.Background(), recipient)
		if err != nil {
			return err
		}
		identityKind = string(identity.Kind)
		identityVersion = identity.Version
		locker, ok := sc.(store.AuthoritySnapshotLocker)
		if !ok {
			return errors.New("tenant scope is not an authority snapshot locker")
		}
		return locker.LockAuthoritySnapshot(context.Background(), []store.AuthorizationFactRef{
			{Kind: "core.identity", ID: identity.ID, Version: identity.Version},
		})
	})
	t.Fatalf("generated client first opaque inbox page = %+v, %v; raw_http=%d %s; view=%s confine=%s inbox_list=%s inbox_pg=%s inbox_count=%d inbox_seq=%v delivery_ids=%v delivery_gets=%v message_ids=%v message_gets=%v point_reads=%v all_list=%s all_pg=%s all_count=%d all_seq=%v cursor_list=%s cursor_pg=%s cursors=%d identity_kind=%s identity_version=%d identity_lock=%s identity_lock_pg=%s epoch_lock=%s epoch_lock_pg=%s",
		page, clientErr, raw.status, raw.raw,
		communicationHTTPTestRedactError(viewErr),
		communicationHTTPTestRedactError(confineErr),
		communicationHTTPTestRedactError(inboxListErr),
		communicationHTTPTestPgError(inboxListErr),
		inboxCount, inboxSeqs, deliveryIDs, deliveryGetErrs, messageIDs, messageGetErrs, pointReads,
		communicationHTTPTestRedactError(allDeliveryErr),
		communicationHTTPTestPgError(allDeliveryErr),
		allCount, allSeqs,
		communicationHTTPTestRedactError(cursorErr),
		communicationHTTPTestPgError(cursorErr),
		cursorCount, identityKind, identityVersion,
		communicationHTTPTestRedactError(identityLockErr),
		communicationHTTPTestPgError(identityLockErr),
		communicationHTTPTestRedactError(epochLockErr),
		communicationHTTPTestPgError(epochLockErr))
}

type communicationHTTPTestCursorWireClaims struct {
	Version          int    `json:"v"`
	TenantID         string `json:"ten"`
	WorkspaceID      string `json:"ws"`
	ReaderKind       string `json:"rk"`
	ReaderRef        string `json:"rr"`
	MailboxKind      string `json:"mk"`
	MailboxRef       string `json:"mr"`
	CarrierClass     string `json:"cc"`
	FilterHash       string `json:"fh"`
	CursorID         string `json:"cid,omitempty"`
	CursorVersion    int64  `json:"cv"`
	BaseDeliverySeq  int64  `json:"base"`
	AfterDeliverySeq int64  `json:"after"`
	DeliveryID       string `json:"did,omitempty"`
	IssuedAtUnix     int64  `json:"iat"`
	ExpiresAtUnix    int64  `json:"exp"`
}

// communicationHTTPTestExpiredToken re-signs only the owned cursor-k1 test
// fixture after moving its canonical five-minute lifetime into the past. It
// lets the real router exercise authenticated expiry without a six-minute
// wall-clock sleep or a production clock seam.
func communicationHTTPTestExpiredToken(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "c2n1" && parts[0] != "c2v2" {
		t.Fatalf("expire unsupported communication token syntax")
	}
	kid, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || string(kid) != "cursor-k1" {
		t.Fatalf("expire communication test token kid = %q, %v", kid, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode communication test token claims: %v", err)
	}
	var claims communicationHTTPTestCursorWireClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("decode communication test token claims JSON: %v", err)
	}
	claims.ExpiresAtUnix = time.Now().UTC().Add(-time.Minute).Unix()
	claims.IssuedAtUnix = claims.ExpiresAtUnix - int64((5*time.Minute)/time.Second)
	canonical, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("encode expired communication test token: %v", err)
	}
	domain := "olivares.sessions.inbox.navigation.c2.v1\x00"
	if parts[0] == "c2v2" {
		domain = "olivares.sessions.inbox.cursor.c2.v2\x00"
	}
	mac := hmac.New(sha256.New, bytes.Repeat([]byte{0x51}, sha256.Size))
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write(kid)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(canonical)
	return parts[0] + "." + parts[1] + "." +
		base64.RawURLEncoding.EncodeToString(canonical) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func bootActivatedCommunicationHTTPTestEngine(t *testing.T, estate communicationHTTPTestStore) *engine {
	t.Helper()
	contentPath, cursorPath := writeCommunicationCustodyForTest(t)
	// ⚠ A CALLER THAT ALREADY CONFIGURED THE LIMITER KEEPS ITS CONFIGURATION.
	// The report-only fixture below exists so a long journey is never throttled by
	// its own reads, and it used to be written unconditionally — which silently
	// discarded the operator config of any test that needed a REAL 429 on these
	// routes, leaving that test to fail with "the bucket never emptied" and no clue
	// why. Deferring to an already-set value costs nothing and makes the fixture a
	// default rather than an override.
	rateLimitPath := strings.TrimSpace(os.Getenv("OLIVARES_RATELIMIT_CONFIG"))
	if rateLimitPath == "" {
		rateLimitPath = filepath.Join(t.TempDir(), "ratelimit.json")
		if err := os.WriteFile(rateLimitPath, []byte(`{"mode":"report_only"}`), 0o600); err != nil {
			t.Fatalf("write communication HTTP rate-limit fixture: %v", err)
		}
	}
	t.Setenv(envKeyWrap, "")
	// Keep committed K3 rows pending until the test explicitly invokes the real
	// composed pump after restart. This makes the recovery assertion
	// deterministic instead of racing the production 15-second cadence.
	t.Setenv(workOutboxPumpIntervalEnv, "1h")
	t.Setenv(envCommunicationActivation, "on")
	t.Setenv(envCommunicationContentKeyringFile, contentPath)
	t.Setenv(envCommunicationCursorKeyringFile, cursorPath)
	t.Setenv("OLIVARES_RATELIMIT_CONFIG", rateLimitPath)

	eng := bootCommunicationHTTPTestEngine(t, estate)
	if err := eng.Close(); err != nil {
		t.Fatalf("close pre-activation engine: %v", err)
	}
	activateCommunicationHTTPTestDirectoryWriter(t, estate)
	eng = bootCommunicationHTTPTestEngine(t, estate)
	t.Cleanup(func() { _ = eng.Close() })
	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background())
	if err != nil || !readiness.Effective {
		t.Fatalf("activated communication readiness = %+v, err %v", readiness, err)
	}
	return eng
}

type communicationHTTPTestUser struct {
	id    model.ID
	token string
}

type communicationHTTPTestSession struct {
	sid           string
	runRef        string
	agentRef      string
	claim         sessions.Lease
	communication auth.CommunicationSessionCredential
	work          auth.WorkSessionCredential
}

type communicationHTTPTestAgent struct {
	identityID model.ID
	externalID string
	token      string
}

type communicationHTTPUnknownDirectoryResolver struct {
	sessions.DirectorySnapshotResolver
}

func (communicationHTTPUnknownDirectoryResolver) ResolvePrincipal(
	context.Context,
	sessions.DirectoryScopeRef,
	sessions.CommunicationPrincipal,
) (sessions.PrincipalResolution, error) {
	return sessions.PrincipalResolution{}, fmt.Errorf(
		"%w: injected current directory observation failure",
		sessions.ErrCommunicationEvidenceUnknown,
	)
}

type communicationHTTPUnknownGrantClosureResolver struct {
	sessions.ChannelGrantSubjectClosureResolver
}

func (communicationHTTPUnknownGrantClosureResolver) ResolveChannelGrantSubjects(
	context.Context,
	sessions.DirectoryScopeRef,
	sessions.CommunicationPrincipal,
) (sessions.ChannelGrantSubjectClosure, error) {
	return sessions.ChannelGrantSubjectClosure{}, fmt.Errorf(
		"%w: injected current Channel ACL closure failure",
		sessions.ErrCommunicationEvidenceUnknown,
	)
}

func exchangeCommunicationHTTPTestAgentToken(
	t *testing.T,
	eng *engine,
	sponsorToken string,
	agentRef string,
) string {
	t.Helper()
	form := url.Values{
		"grant_type":         {auth.GrantTypeTokenExchange},
		"subject_token":      {sponsorToken},
		"subject_token_type": {auth.TokenTypeAccessToken},
		"requested_actor":    {agentRef},
		"scope":              {auth.VerbWrite},
		"name":               {"k3-http-agent"},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/token-exchange",
		strings.NewReader(form.Encode()))
	requestContext, cancelRequest := context.WithTimeout(req.Context(), 2*time.Minute)
	defer cancelRequest()
	req = req.WithContext(requestContext)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Authorization", "Bearer "+sponsorToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	eng.api.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("agent token exchange = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.AccessToken == "" {
		t.Fatalf("decode agent token exchange: token=%t err=%v body=%s",
			result.AccessToken != "", err, recorder.Body.String())
	}
	return result.AccessToken
}

func createCommunicationHTTPTestSession(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	workspace model.ID,
	label string,
) communicationHTTPTestSession {
	t.Helper()
	ctx := context.Background()
	runRef := model.NewID().String()
	agentRef := "agent:k3-http-" + label
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"run_ref": runRef, "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "state": "running", "last_event_seq": int64(0),
			"agent_ref": agentRef,
		})
		return err
	}); err != nil {
		t.Fatalf("create operated %s run evidence: %v", label, err)
	}
	sid, err := eng.sessionsMod.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: sessions.ProviderOperated, ExternalID: runRef,
		Origin: sessions.OriginOperated, WorkspaceID: workspace, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve operated %s session: %v", label, err)
	}
	claim, err := eng.sessionsMod.Claim(ctx, tenant, sid, agentRef, time.Hour)
	if err != nil {
		t.Fatalf("claim operated %s session: %v", label, err)
	}
	issuer, err := auth.NewSystemOperator(
		"test:k3-http-runtime", "issue exact test runtime credentials",
	)
	if err != nil {
		t.Fatalf("runtime credential issuer: %v", err)
	}
	communication, err := eng.authr.IssueCommunicationSessionCredential(
		ctx, issuer, auth.CommunicationSessionCredentialSpec{
			Tenant: tenant, WorkspaceID: workspace, SessionRef: sid,
			RunRef: runRef, AgentRef: agentRef, ClaimFence: claim.Fence,
		},
	)
	if err != nil {
		t.Fatalf("issue %s communication credential: %v", label, err)
	}
	work, err := eng.authr.IssueWorkSessionCredential(ctx, issuer, auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: sid, RunRef: runRef,
		AgentRef: agentRef, ClaimFence: claim.Fence,
	})
	if err != nil {
		t.Fatalf("issue %s work credential: %v", label, err)
	}
	return communicationHTTPTestSession{
		sid: sid, runRef: runRef, agentRef: agentRef, claim: claim,
		communication: communication, work: work,
	}
}

func loginCommunicationHTTPTestUser(
	t *testing.T,
	eng *engine,
	id model.ID,
	email string,
) communicationHTTPTestUser {
	t.Helper()
	loggedIn := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/login", "", "",
		map[string]any{"email": email, "password": "k3-http-member-password"}, nil)
	if loggedIn.status != http.StatusOK {
		t.Fatalf("login %s = %d: %s", email, loggedIn.status, loggedIn.raw)
	}
	login := communicationHTTPTestDecode[struct {
		Token string `json:"token"`
	}](t, loggedIn)
	return communicationHTTPTestUser{id: id, token: login.Token}
}

// stepUpCommunicationHTTPTestUser terminates the same authentication elevation
// primitive as the WebAuthn/PIV ceremonies. The workspace itself is still
// provisioned through the authenticated product route; this local fixture only
// supplies the hardware-backed assurance that route deliberately requires.
func stepUpCommunicationHTTPTestUser(t *testing.T, eng *engine, token string) {
	t.Helper()
	principal, err := eng.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate communication HTTP operator for step-up: %v", err)
	}
	if _, err := eng.authr.ElevateSession(
		context.Background(), principal, "webauthn", auth.AAL3,
	); err != nil {
		t.Fatalf("step up communication HTTP operator: %v", err)
	}
}

func createCommunicationHTTPTestUser(
	t *testing.T,
	eng *engine,
	adminToken string,
	tenant model.TenantID,
	email string,
	role string,
) communicationHTTPTestUser {
	t.Helper()
	password := "k3-http-member-password"
	created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/users", adminToken, "",
		map[string]any{"email": email, "password": password}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("create user %s = %d: %s", email, created.status, created.raw)
	}
	var user struct {
		ID model.ID `json:"id"`
	}
	user = communicationHTTPTestDecode[struct {
		ID model.ID `json:"id"`
	}](t, created)
	granted := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/memberships", adminToken, "",
		map[string]any{"user_id": user.ID, "tenant": tenant, "role": role}, nil)
	if granted.status != http.StatusCreated {
		t.Fatalf("grant %s membership = %d: %s", email, granted.status, granted.raw)
	}
	return loginCommunicationHTTPTestUser(t, eng, user.ID, email)
}

func assertCommunicationStoredPayload(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	messageID model.ID,
	wantEncoding string,
	secret string,
) {
	t.Helper()
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.message")
		if err != nil {
			return err
		}
		record, err := repo.Get(context.Background(), messageID)
		if err != nil {
			return err
		}
		if got := record.String("payload_encoding"); got != wantEncoding {
			return fmt.Errorf("payload encoding = %q, want %q", got, wantEncoding)
		}
		plain, sealed := record.String("payload_plain_json"), record.String("payload_sealed_json")
		switch wantEncoding {
		case "sealed_v1":
			if plain != "" || sealed == "" || strings.Contains(sealed, secret) {
				return fmt.Errorf("sealed payload exposed plaintext: plain=%q sealed=%q", plain, sealed)
			}
		case "plain_json":
			if sealed != "" || !strings.Contains(plain, secret) {
				return fmt.Errorf("plain payload projection mismatch: plain=%q sealed=%q", plain, sealed)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("stored Message %s: %v", messageID, err)
	}
}

type communicationHTTPTestEffectCounts struct {
	channels      int
	grants        int
	messages      int
	deliveries    int
	acks          int
	handoffs      int
	outbox        int
	commands      int
	workItems     int
	workLeases    int
	workEvents    int
	cursors       int
	barriers      int
	successAudits int
	digest        [sha256.Size]byte
}

func communicationHTTPTestEffects(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
) communicationHTTPTestEffectCounts {
	t.Helper()
	var out communicationHTTPTestEffectCounts
	digest := sha256.New()
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		counts := []struct {
			kind model.Kind
			set  *int
		}{
			{kind: "sessions.channel", set: &out.channels},
			{kind: "sessions.channel_grant", set: &out.grants},
			{kind: "sessions.message", set: &out.messages},
			{kind: "sessions.message_delivery", set: &out.deliveries},
			{kind: "sessions.message_ack", set: &out.acks},
			{kind: "sessions.handoff", set: &out.handoffs},
			{kind: "sessions.work_outbox", set: &out.outbox},
			{kind: "sessions.communication_command", set: &out.commands},
			{kind: "sessions.work_item", set: &out.workItems},
			{kind: "sessions.work_lease", set: &out.workLeases},
			{kind: "sessions.work_event", set: &out.workEvents},
			{kind: "sessions.inbox_cursor", set: &out.cursors},
			{kind: "sessions.inbox_cursor_barrier", set: &out.barriers},
		}
		for _, counter := range counts {
			repo, err := sc.Ext(counter.kind)
			if err != nil {
				return err
			}
			rows, page, err := repo.List(context.Background(), model.Query{Limit: 1000})
			if err != nil {
				return err
			}
			if page.HasMore {
				return fmt.Errorf("%s effect census exceeded 1000 rows", counter.kind)
			}
			*counter.set = len(rows)
			_, _ = digest.Write([]byte(counter.kind))
			_, _ = digest.Write([]byte{0})
			for _, row := range rows {
				raw, err := json.Marshal(row)
				if err != nil {
					return fmt.Errorf("marshal %s effect census: %w", counter.kind, err)
				}
				_, _ = digest.Write(raw)
				_, _ = digest.Write([]byte{0})
			}
		}
		successActions := map[string]bool{
			"sessions.communication.channel.create":       true,
			"sessions.communication.channel.update":       true,
			"sessions.communication.channel.grant":        true,
			"sessions.communication.channel.revoke":       true,
			"sessions.communication.message.publish":      true,
			"sessions.communication.inbox_cursor.advance": true,
			"sessions.communication.message_delivery.ack": true,
			"sessions.communication.handoff.offer":        true,
			"sessions.communication.handoff.respond":      true,
		}
		if err := sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			if !successActions[event.Action] {
				return nil
			}
			out.successAudits++
			raw, err := json.Marshal(event)
			if err != nil {
				return fmt.Errorf("marshal communication success audit census: %w", err)
			}
			_, _ = digest.Write([]byte("success-audit"))
			_, _ = digest.Write([]byte{0})
			_, _ = digest.Write(raw)
			_, _ = digest.Write([]byte{0})
			return nil
		}); err != nil {
			return fmt.Errorf("walk communication success audit census: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("communication effect census: %v", err)
	}
	copy(out.digest[:], digest.Sum(nil))
	return out
}

func assertCommunicationHTTPTestNoEffects(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	before communicationHTTPTestEffectCounts,
	label string,
) {
	t.Helper()
	if after := communicationHTTPTestEffects(t, eng, tenant); after != before {
		t.Fatalf("%s changed communication effects: before=%+v after=%+v", label, before, after)
	}
}

func exerciseCommunicationHTTPUnknownEvidence(
	t *testing.T,
	eng *engine,
	target communicationHTTPTestUser,
	tenant model.TenantID,
	workspace model.ID,
	deliveryID model.ID,
	secret string,
) {
	t.Helper()
	if eng.communicationComposition == nil || eng.communicationComposition.resolver == nil ||
		eng.communicationComposition.closure == nil {
		t.Fatal("production communication composition is unavailable for UNKNOWN controls")
	}

	before := communicationHTTPTestEffects(t, eng, tenant)
	realDirectory := sessions.DirectorySnapshotResolver(eng.communicationComposition.resolver)
	eng.sessionsMod.UseCommunicationDirectorySnapshotResolver(
		communicationHTTPUnknownDirectoryResolver{DirectorySnapshotResolver: realDirectory},
	)
	directoryUnknown := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+deliveryID.String(), target.token, tenant, nil, nil)
	eng.sessionsMod.UseCommunicationDirectorySnapshotResolver(realDirectory)
	if directoryUnknown.status != http.StatusServiceUnavailable ||
		strings.Contains(string(directoryUnknown.raw), secret) {
		t.Fatalf("UNKNOWN current directory send = %d: %s",
			directoryUnknown.status, directoryUnknown.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, before,
		"UNKNOWN current directory Delivery read")

	before = communicationHTTPTestEffects(t, eng, tenant)
	realClosure := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
	eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(
		communicationHTTPUnknownGrantClosureResolver{
			ChannelGrantSubjectClosureResolver: realClosure,
		},
	)
	aclUnknown := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(workspace, 10, ""), target.token, tenant, nil, nil)
	eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(realClosure)
	if aclUnknown.status != http.StatusServiceUnavailable ||
		strings.Contains(string(aclUnknown.raw), secret) {
		t.Fatalf("UNKNOWN current Channel ACL inbox = %d: %s",
			aclUnknown.status, aclUnknown.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, before,
		"UNKNOWN current Channel ACL inbox")
}

func exerciseCommunicationHTTPScheduledVisibility(
	t *testing.T,
	eng *engine,
	owner communicationHTTPTestUser,
	target communicationHTTPTestUser,
	tenant model.TenantID,
	channelID model.ID,
	workspace model.ID,
) string {
	t.Helper()
	const secret = "scheduled-http-boundary-secret"
	availableAt := time.Now().UTC().Add(2 * time.Second)
	availableStamp := model.NewTimestamp(availableAt).String()
	send := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", owner.token, tenant, map[string]any{
			"channel_id": channelID,
			"recipient":  map[string]any{"kind": "user", "ref": target.id},
			"content": map[string]any{
				"subject": "Scheduled governed notice",
				"blocks": []map[string]any{{
					"type": "text", "format": "plain", "text": secret,
				}},
			},
			"urgency": "normal", "available_at": availableStamp,
		}, map[string]string{"Idempotency-Key": model.NewID().String()})
	if send.status != http.StatusCreated {
		t.Fatalf("schedule Message over authenticated HTTP = %d: %s", send.status, send.raw)
	}
	scheduled := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, send)

	beforeBoundary := communicationHTTPTestEffects(t, eng, tenant)
	premature := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+scheduled.DeliveryID.String(),
		target.token, tenant, nil, nil)
	if premature.status != http.StatusNotFound || strings.Contains(string(premature.raw), secret) {
		t.Fatalf("premature scheduled Delivery read = %d: %s", premature.status, premature.raw)
	}
	prematureInbox := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(workspace, 20, ""), target.token, tenant, nil, nil)
	if prematureInbox.status != http.StatusOK || strings.Contains(string(prematureInbox.raw), secret) {
		t.Fatalf("premature scheduled inbox = %d: %s", prematureInbox.status, prematureInbox.raw)
	}
	prematureAck := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/deliveries/"+scheduled.DeliveryID.String()+"/ack",
		target.token, tenant, nil, map[string]string{
			"If-Match": `"v1"`, "Idempotency-Key": model.NewID().String(),
		})
	if prematureAck.status >= 200 && prematureAck.status < 300 ||
		strings.Contains(string(prematureAck.raw), secret) {
		t.Fatalf("premature scheduled Ack = %d: %s", prematureAck.status, prematureAck.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, beforeBoundary,
		"scheduled visibility before availability boundary")

	wait := time.Until(availableAt) + 150*time.Millisecond
	if wait <= 0 || wait > 5*time.Second {
		t.Fatalf("scheduled HTTP boundary wait = %s", wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	<-timer.C
	visible := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+scheduled.DeliveryID.String(),
		target.token, tenant, nil, nil)
	if visible.status != http.StatusOK || !strings.Contains(string(visible.raw), secret) {
		t.Fatalf("scheduled Delivery after availability boundary = %d: %s", visible.status, visible.raw)
	}
	visibleInbox := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(workspace, 20, ""), target.token, tenant, nil, nil)
	if visibleInbox.status != http.StatusOK || !strings.Contains(string(visibleInbox.raw), secret) {
		t.Fatalf("scheduled inbox after availability boundary = %d: %s",
			visibleInbox.status, visibleInbox.raw)
	}
	return secret
}

func communicationHTTPTestOutboxStates(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
) map[string]int {
	t.Helper()
	states := make(map[string]int)
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.work_outbox")
		if err != nil {
			return err
		}
		rows, page, err := repo.List(context.Background(), model.Query{Limit: 1000})
		if err != nil {
			return err
		}
		if page.HasMore {
			return errors.New("work outbox recovery census exceeded 1000 rows")
		}
		for _, row := range rows {
			states[row.String("state")]++
		}
		return nil
	}); err != nil {
		t.Fatalf("work outbox recovery census: %v", err)
	}
	return states
}

func communicationHTTPTestActiveBarrierDeliveries(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	reader sessions.RecipientRef,
) []model.ID {
	t.Helper()
	var deliveries []model.ID
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.inbox_cursor_barrier")
		if err != nil {
			return err
		}
		rows, page, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{
				{Column: "reader_kind", Op: model.OpEq, Value: string(reader.Kind)},
				{Column: "reader_ref", Op: model.OpEq, Value: reader.Ref},
				{Column: "state", Op: model.OpEq, Value: "active"},
			},
			Limit: 1000,
		})
		if err != nil {
			return err
		}
		if page.HasMore {
			return errors.New("active cursor barrier census exceeded 1000 rows")
		}
		for _, row := range rows {
			deliveries = append(deliveries, model.ID(row.String("delivery_id")))
		}
		return nil
	}); err != nil {
		t.Fatalf("active cursor barrier census: %v", err)
	}
	return deliveries
}

func communicationHTTPTestContainsID(ids []model.ID, target model.ID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func assertCommunicationMarkersAbsentFromProjections(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	markers ...string,
) {
	t.Helper()
	projectionKinds := []model.Kind{
		"sessions.work_event",
		"sessions.work_outbox",
		"sessions.communication_command",
		"sessions.inbox_cursor",
		"sessions.inbox_cursor_barrier",
	}
	check := func(location string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal %s projection: %w", location, err)
		}
		for _, marker := range markers {
			if marker != "" && bytes.Contains(raw, []byte(marker)) {
				return fmt.Errorf("%s projection retained payload marker %q", location, marker)
			}
		}
		return nil
	}
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, kind := range projectionKinds {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			rows, page, err := repo.List(context.Background(), model.Query{Limit: 1000})
			if err != nil {
				return err
			}
			if page.HasMore {
				return fmt.Errorf("%s marker scan exceeded 1000 rows", kind)
			}
			for _, row := range rows {
				if err := check(string(kind), row); err != nil {
					return err
				}
			}
		}
		return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			return check("audit", event)
		})
	}); err != nil {
		t.Fatalf("communication payload projection hygiene: %v", err)
	}
}

// TestCommunicationMessagingFreshHTTP exercises the production composition and
// HTTP router on a brand-new estate. Every principal and grant is provisioned
// through authenticated product APIs; the test never seeds a communication row.
func TestCommunicationMessagingFreshHTTP(t *testing.T) {
	exerciseCommunicationMessagingFreshHTTP(t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationMessagingFreshHTTPPostgres is the same authenticated product
// HTTP journey on an owned IsolatedPostgresSplitOwner estate. Boot, writer
// activation and every restart reuse the same postgres engine, application DSN,
// owner DSN and admin DSN. Absent PostgreSQL skips; a required misconfiguration
// fails.
func TestCommunicationMessagingFreshHTTPPostgres(t *testing.T) {
	exerciseCommunicationMessagingFreshHTTP(t, communicationHTTPTestPostgresStore(t))
}

func exerciseCommunicationMessagingFreshHTTP(t *testing.T, estate communicationHTTPTestStore) {
	t.Helper()
	eng := bootActivatedCommunicationHTTPTestEngine(t, estate)

	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	setup := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/setup", "", "",
		map[string]any{
			"token": setupToken, "email": "admin@k3-http.test", "password": "k3-http-admin-password",
		}, nil)
	if setup.status != http.StatusCreated {
		t.Fatalf("setup = %d: %s", setup.status, setup.raw)
	}
	login := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/login", "", "",
		map[string]any{"email": "admin@k3-http.test", "password": "k3-http-admin-password"}, nil)
	if login.status != http.StatusOK {
		t.Fatalf("admin login = %d: %s", login.status, login.raw)
	}
	var loginBody struct {
		Token string `json:"token"`
	}
	loginBody = communicationHTTPTestDecode[struct {
		Token string `json:"token"`
	}](t, login)
	createdOrg := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/system/orgs",
		loginBody.Token, "", map[string]any{"name": "K3 HTTP", "slug": "k3-http"}, nil)
	if createdOrg.status != http.StatusCreated {
		t.Fatalf("create tenant = %d: %s", createdOrg.status, createdOrg.raw)
	}
	var org struct {
		TenantID model.TenantID `json:"tenant_id"`
	}
	org = communicationHTTPTestDecode[struct {
		TenantID model.TenantID `json:"tenant_id"`
	}](t, createdOrg)
	owner := createCommunicationHTTPTestUser(
		t, eng, loginBody.Token, org.TenantID, "owner@k3-http.test", auth.RoleOwner,
	)
	target := createCommunicationHTTPTestUser(
		t, eng, loginBody.Token, org.TenantID, "target@k3-http.test", auth.RoleEditor,
	)
	writeOnly := createCommunicationHTTPTestUser(
		t, eng, loginBody.Token, org.TenantID, "writer@k3-http.test", auth.RoleEditor,
	)
	third := createCommunicationHTTPTestUser(
		t, eng, loginBody.Token, org.TenantID, "third@k3-http.test", auth.RoleEditor,
	)
	otherOrg := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/system/orgs",
		loginBody.Token, "", map[string]any{"name": "Other", "slug": "k3-http-other"}, nil)
	if otherOrg.status != http.StatusCreated {
		t.Fatalf("create other tenant = %d: %s", otherOrg.status, otherOrg.raw)
	}
	other := communicationHTTPTestDecode[struct {
		TenantID model.TenantID `json:"tenant_id"`
	}](t, otherOrg)

	// The production boot reloads the governance PDP for every durable tenant.
	// Reopen the brand-new estate before activating its K3 traffic, exactly as a
	// configured installation does after its initial tenant bootstrap.
	if err := eng.Close(); err != nil {
		t.Fatalf("close bootstrapped estate: %v", err)
	}
	eng = bootCommunicationHTTPTestEngine(t, estate)
	t.Cleanup(func() { _ = eng.Close() })
	if readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background()); err != nil || !readiness.Effective {
		t.Fatalf("communication readiness after bootstrap restart = %+v, err %v", readiness, err)
	}
	owner = loginCommunicationHTTPTestUser(t, eng, owner.id, "owner@k3-http.test")
	target = loginCommunicationHTTPTestUser(t, eng, target.id, "target@k3-http.test")
	writeOnly = loginCommunicationHTTPTestUser(t, eng, writeOnly.id, "writer@k3-http.test")
	third = loginCommunicationHTTPTestUser(t, eng, third.id, "third@k3-http.test")
	stepUpCommunicationHTTPTestUser(t, eng, owner.token)
	var defaultWorkspace model.ID
	if err := eng.store.View(context.Background(), org.TenantID, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(context.Background())
		defaultWorkspace = workspace.ID
		return err
	}); err != nil {
		t.Fatalf("read K3 HTTP default workspace: %v", err)
	}

	workspaceResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/workspaces", owner.token, org.TenantID,
		map[string]any{"name": "K3 non-default", "slug": "k3-non-default"}, nil)
	if workspaceResponse.status != http.StatusCreated {
		t.Fatalf("create non-default workspace through product API = %d: %s",
			workspaceResponse.status, workspaceResponse.raw)
	}
	workspaceDTO := communicationHTTPTestDecode[struct {
		ID string `json:"id"`
	}](t, workspaceResponse)
	nonDefaultWorkspace, err := model.ParseID(workspaceDTO.ID)
	if err != nil || nonDefaultWorkspace == defaultWorkspace {
		t.Fatalf("non-default workspace ref = %q, %v", workspaceDTO.ID, err)
	}
	selectorBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	selectorCases := []struct {
		name string
		path string
	}{
		{name: "missing inbox workspace", path: "/v1/m/sessions/inbox?limit=1"},
		{name: "malformed inbox workspace", path: "/v1/m/sessions/inbox?workspace_id=not-a-uuid&limit=1"},
		{name: "removed raw inbox position alphabetic", path: "/v1/m/sessions/inbox?workspace_id=" +
			nonDefaultWorkspace.String() + "&after_delivery_seq=abc&limit=1"},
		{name: "removed raw inbox position negative", path: "/v1/m/sessions/inbox?workspace_id=" +
			nonDefaultWorkspace.String() + "&after_delivery_seq=-1&limit=1"},
		{name: "removed raw inbox position overflow", path: "/v1/m/sessions/inbox?workspace_id=" +
			nonDefaultWorkspace.String() + "&after_delivery_seq=9223372036854775808&limit=1"},
		{name: "removed raw inbox position canonical zero", path: "/v1/m/sessions/inbox?workspace_id=" +
			nonDefaultWorkspace.String() + "&after_delivery_seq=0&limit=1"},
		{name: "removed raw inbox position canonical positive", path: "/v1/m/sessions/inbox?workspace_id=" +
			nonDefaultWorkspace.String() + "&after_delivery_seq=1&limit=1"},
		{name: "missing cursor workspace", path: "/v1/m/sessions/inbox/cursors/personal/" +
			target.id.String() + "?target=c2n1.invalid"},
	}
	for _, test := range selectorCases {
		response := communicationHTTPTestRequest(t, eng, http.MethodGet, test.path,
			target.token, org.TenantID, nil, nil)
		if response.status != http.StatusBadRequest || strings.Contains(string(response.raw), "c2n1.invalid") {
			t.Fatalf("%s = %d: %s", test.name, response.status, response.raw)
		}
	}
	missingWorkspaceBody := map[string]any{
		"slug": "missing-workspace", "name": "Missing workspace",
		"initial_grants": []map[string]any{{
			"subject":  map[string]any{"kind": "user", "ref": owner.id},
			"can_read": true, "can_write": true, "can_admin": true,
		}},
	}
	if response := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/channels", owner.token, org.TenantID, missingWorkspaceBody, nil); response.status != http.StatusBadRequest {
		t.Fatalf("missing Channel workspace = %d: %s", response.status, response.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, org.TenantID, selectorBefore,
		"invalid explicit workspace selectors")

	server := httptest.NewServer(eng.api.Handler())
	t.Cleanup(server.Close)
	generatedOwner, err := olivaresclient.New(server.URL, owner.token,
		olivaresclient.WithTenant(org.TenantID.String()), olivaresclient.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("construct generated owner client: %v", err)
	}
	generatedTarget, err := olivaresclient.New(server.URL, target.token,
		olivaresclient.WithTenant(org.TenantID.String()), olivaresclient.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("construct generated target client: %v", err)
	}
	clientContext, cancelClient := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelClient()
	nonDefaultCreated, err := generatedOwner.PostV1MSessionsChannels(clientContext,
		olivaresclient.PostV1MSessionsChannelsInput{Body: olivaresclient.SessionsCommunicationChannelCreateBody{
			WorkspaceID: nonDefaultWorkspace.String(), Slug: "generated-non-default",
			Name: "Generated non-default", Kind: communicationHTTPTestPtr("work"),
			Sensitivity:         communicationHTTPTestPtr("restricted"),
			ContentProtection:   communicationHTTPTestPtr("application_sealed"),
			DefaultAckPolicy:    communicationHTTPTestPtr("each_required"),
			DefaultAckTimeoutMS: communicationHTTPTestPtr[int64](300000),
			DefaultWake:         communicationHTTPTestPtr("none"), MaxFanout: communicationHTTPTestPtr[int64](8),
			MaxAutomationDepth: communicationHTTPTestPtr[int64](2),
			InitialGrants: []olivaresclient.SessionsCommunicationChannelGrantInput{
				{Subject: olivaresclient.SessionsCommunicationRef{Kind: "user", Ref: owner.id.String()}, CanRead: true, CanWrite: true, CanAdmin: true},
				{Subject: olivaresclient.SessionsCommunicationRef{Kind: "user", Ref: target.id.String()}, CanRead: true},
			},
		}})
	if err != nil || nonDefaultCreated.Channel.WorkspaceID != nonDefaultWorkspace.String() ||
		nonDefaultCreated.Channel.ID == "" {
		t.Fatalf("generated client non-default Channel = %+v, %v", nonDefaultCreated, err)
	}
	for index := 1; index <= 2; index++ {
		result, sendErr := generatedOwner.PostV1MSessionsMessagesSend(clientContext,
			olivaresclient.PostV1MSessionsMessagesSendInput{
				Body: olivaresclient.SessionsCommunicationMessageSendBody{
					ChannelID: nonDefaultCreated.Channel.ID,
					Recipient: olivaresclient.SessionsCommunicationRef{Kind: "user", Ref: target.id.String()},
					Content: olivaresclient.SessionsCommunicationMessageContent{
						Subject: fmt.Sprintf("Generated page %d", index),
						Blocks: []olivaresclient.SessionsCommunicationContentBlock{{
							Type: "text", Format: communicationHTTPTestPtr("plain"),
							Text: communicationHTTPTestPtr(fmt.Sprintf("generated-secret-%d", index)),
						}},
					},
				},
				IdempotencyKey: model.NewID().String(),
			})
		if sendErr != nil || result.DeliveryID == "" || result.Replayed ||
			result.DeliveryCount != 1 || result.RequiredCount != 1 ||
			result.AudienceHash == "" || result.PayloadDigest == "" {
			t.Fatalf("generated client non-default send %d = %+v, %v", index, result, sendErr)
		}
	}
	firstClientPage, err := generatedTarget.GetV1MSessionsInbox(clientContext,
		olivaresclient.GetV1MSessionsInboxInput{
			WorkspaceID: nonDefaultWorkspace.String(), Limit: communicationHTTPTestPtr[int64](1),
		})
	if err != nil || len(firstClientPage.Items) != 1 || !firstClientPage.HasMore ||
		firstClientPage.Continuation == nil || !strings.HasPrefix(*firstClientPage.Continuation, "c2n1.") {
		communicationHTTPTestInboxFailureCensus(
			t, eng, org.TenantID, nonDefaultWorkspace, target.id, target.token, firstClientPage, err,
		)
	}
	firstContinuation := *firstClientPage.Continuation
	navigationBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	navigationOtherTenantBefore := communicationHTTPTestEffects(t, eng, other.TenantID)
	tamperedNavigation := firstContinuation[:len(firstContinuation)-1] + "A"
	if strings.HasSuffix(firstContinuation, "A") {
		tamperedNavigation = firstContinuation[:len(firstContinuation)-1] + "B"
	}
	unknownKeyParts := strings.Split(firstContinuation, ".")
	unknownKeyParts[1] = base64.RawURLEncoding.EncodeToString([]byte("unknown-kid"))
	expiredNavigation := communicationHTTPTestExpiredToken(t, firstContinuation)
	navigationDenials := []struct {
		name      string
		path      string
		token     string
		tenant    model.TenantID
		want      int
		plaintext string
	}{
		{name: "malformed continuation", path: "/v1/m/sessions/inbox?workspace_id=" + nonDefaultWorkspace.String() + "&continuation=not-a-token&limit=1", token: target.token, want: http.StatusBadRequest, plaintext: "generated-secret"},
		{name: "tampered continuation", path: communicationHTTPTestInboxPath(nonDefaultWorkspace, 1, tamperedNavigation), token: target.token, want: http.StatusBadRequest, plaintext: "generated-secret"},
		{name: "expired continuation", path: communicationHTTPTestInboxPath(nonDefaultWorkspace, 1, expiredNavigation), token: target.token, want: http.StatusBadRequest, plaintext: "generated-secret"},
		{name: "unknown-key continuation", path: communicationHTTPTestInboxPath(nonDefaultWorkspace, 1, strings.Join(unknownKeyParts, ".")), token: target.token, want: http.StatusBadRequest, plaintext: "generated-secret"},
		{name: "cross-workspace continuation", path: communicationHTTPTestInboxPath(defaultWorkspace, 1, firstContinuation), token: target.token, want: http.StatusBadRequest, plaintext: "generated-secret"},
		{name: "cross-recipient continuation", path: communicationHTTPTestInboxPath(nonDefaultWorkspace, 1, firstContinuation), token: third.token, want: http.StatusBadRequest, plaintext: "generated-secret"},
		{name: "cross-tenant continuation", path: communicationHTTPTestInboxPath(nonDefaultWorkspace, 1, firstContinuation), token: target.token, tenant: other.TenantID, want: http.StatusForbidden, plaintext: "generated-secret"},
	}
	for _, denial := range navigationDenials {
		requestTenant := denial.tenant
		if requestTenant.IsZero() {
			requestTenant = org.TenantID
		}
		response := communicationHTTPTestRequest(t, eng, http.MethodGet, denial.path,
			denial.token, requestTenant, nil, nil)
		if response.status != denial.want || strings.Contains(string(response.raw), denial.plaintext) {
			t.Fatalf("%s = %d: %s", denial.name, response.status, response.raw)
		}
	}
	assertCommunicationHTTPTestNoEffects(t, eng, org.TenantID, navigationBefore,
		"invalid opaque inbox continuations")
	assertCommunicationHTTPTestNoEffects(t, eng, other.TenantID, navigationOtherTenantBefore,
		"cross-tenant opaque inbox continuation")
	secondClientPage, err := generatedTarget.GetV1MSessionsInbox(clientContext,
		olivaresclient.GetV1MSessionsInboxInput{
			WorkspaceID: nonDefaultWorkspace.String(), Limit: communicationHTTPTestPtr[int64](1),
			Continuation: communicationHTTPTestPtr(firstContinuation),
		})
	if err != nil || len(secondClientPage.Items) != 1 ||
		secondClientPage.Items[0].Delivery.ID == firstClientPage.Items[0].Delivery.ID ||
		secondClientPage.CursorTarget == nil || !strings.HasPrefix(*secondClientPage.CursorTarget, "c2n1.") {
		t.Fatalf("generated client second opaque inbox page = %+v, %v", secondClientPage, err)
	}
	secondCursorTarget := *secondClientPage.CursorTarget
	read := secondClientPage.Items[0]
	if read.Message.AckPolicy != "each_required" || read.Message.AvailableAt == "" ||
		read.Message.AckDueAt == nil ||
		!read.Delivery.Required || read.Delivery.AvailableAt == "" || read.Delivery.AckDueAt == nil {
		t.Fatalf("generated client nested schedule/Ack read metadata = %+v", read)
	}
	clientAck, err := generatedTarget.PostV1MSessionsDeliveriesByIDAck(
		clientContext, secondClientPage.Items[0].Delivery.ID,
		olivaresclient.PostV1MSessionsDeliveriesByIDAckInput{
			IfMatch:        fmt.Sprintf(`"v%d"`, secondClientPage.Items[0].Delivery.Version),
			IdempotencyKey: model.NewID().String(),
		})
	if err != nil || clientAck.AckID == "" || clientAck.DeliveryID != secondClientPage.Items[0].Delivery.ID ||
		clientAck.Replayed {
		t.Fatalf("generated client explicit Ack = %+v, %v", clientAck, err)
	}
	clientCursor, err := generatedTarget.GetV1MSessionsInboxCursorsPersonalByRecipient(
		clientContext, target.id.String(),
		olivaresclient.GetV1MSessionsInboxCursorsPersonalByRecipientInput{
			WorkspaceID: nonDefaultWorkspace.String(), Target: secondCursorTarget,
		})
	if err != nil || !strings.HasPrefix(clientCursor.Cursor, "c2v2.") || clientCursor.ETag != `"v0"` {
		t.Fatalf("generated client cursor mint = %+v, %v", clientCursor, err)
	}
	spliceBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	expiredCursor := communicationHTTPTestExpiredToken(t, clientCursor.Cursor)
	expiredCursorResponse := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+target.id.String(), target.token, org.TenantID,
		map[string]any{
			"cursor": expiredCursor, "delivery_id": secondClientPage.Items[0].Delivery.ID,
		}, map[string]string{
			"If-Match": clientCursor.ETag, "Idempotency-Key": model.NewID().String(),
		})
	if expiredCursorResponse.status != http.StatusBadRequest ||
		strings.Contains(string(expiredCursorResponse.raw), "generated-secret") {
		t.Fatalf("expired cursor target = %d: %s", expiredCursorResponse.status, expiredCursorResponse.raw)
	}
	splicedCursor := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+target.id.String(), target.token, org.TenantID,
		map[string]any{
			"cursor":      clientCursor.Cursor,
			"delivery_id": firstClientPage.Items[0].Delivery.ID,
		}, map[string]string{
			"If-Match": clientCursor.ETag, "Idempotency-Key": model.NewID().String(),
		})
	if splicedCursor.status != http.StatusBadRequest || strings.Contains(string(splicedCursor.raw), "generated-secret") {
		t.Fatalf("target/token cursor splice = %d: %s", splicedCursor.status, splicedCursor.raw)
	}
	wrongWorkspaceTarget := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestCursorPath(target.id.String(), defaultWorkspace, secondCursorTarget),
		target.token, org.TenantID, nil, nil)
	if wrongWorkspaceTarget.status != http.StatusBadRequest ||
		strings.Contains(string(wrongWorkspaceTarget.raw), "generated-secret") {
		t.Fatalf("wrong-workspace cursor target = %d: %s", wrongWorkspaceTarget.status, wrongWorkspaceTarget.raw)
	}
	wrongRecipientTarget := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestCursorPath(third.id.String(), nonDefaultWorkspace, secondCursorTarget),
		third.token, org.TenantID, nil, nil)
	if wrongRecipientTarget.status != http.StatusBadRequest ||
		strings.Contains(string(wrongRecipientTarget.raw), "generated-secret") {
		t.Fatalf("wrong-recipient cursor target = %d: %s", wrongRecipientTarget.status, wrongRecipientTarget.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, org.TenantID, spliceBefore,
		"opaque cursor target splicing")
	clientAdvanced, err := generatedTarget.PutV1MSessionsInboxCursorsPersonalByRecipient(
		clientContext, target.id.String(),
		olivaresclient.PutV1MSessionsInboxCursorsPersonalByRecipientInput{
			Body: olivaresclient.SessionsCommunicationCursorAdvanceBody{
				Cursor: clientCursor.Cursor, DeliveryID: secondClientPage.Items[0].Delivery.ID,
			},
			IfMatch: clientCursor.ETag, IdempotencyKey: model.NewID().String(),
		})
	if err != nil || clientAdvanced.CursorID == "" || clientAdvanced.Version != 1 ||
		clientAdvanced.Projection.LastSeenSeq == nil ||
		*clientAdvanced.Projection.LastSeenSeq != secondClientPage.Items[0].Delivery.DeliverySeq {
		t.Fatalf("generated client conditional cursor advance = %+v, %v", clientAdvanced, err)
	}
	staleLineageBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	staleLineage := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(nonDefaultWorkspace, 1, firstContinuation),
		target.token, org.TenantID, nil, nil)
	if staleLineage.status != http.StatusPreconditionFailed ||
		strings.Contains(string(staleLineage.raw), "generated-secret") {
		t.Fatalf("stale-lineage continuation = %d: %s", staleLineage.status, staleLineage.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, org.TenantID, staleLineageBefore,
		"stale opaque continuation lineage")
	nonDefaultChannelResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels/"+nonDefaultCreated.Channel.ID, owner.token, org.TenantID, nil, nil)
	if nonDefaultChannelResponse.status != http.StatusOK {
		t.Fatalf("read generated non-default Channel = %d: %s",
			nonDefaultChannelResponse.status, nonDefaultChannelResponse.raw)
	}
	nonDefaultChannel := communicationHTTPTestDecode[sessions.Channel](t, nonDefaultChannelResponse)

	channelBody := map[string]any{
		"workspace_id": defaultWorkspace,
		"slug":         "sealed-work", "name": "Sealed work", "kind": "work",
		"sensitivity": "restricted", "content_protection": "application_sealed",
		"default_ack_policy": "each_required", "default_ack_timeout_ms": 300000,
		"default_wake": "none", "max_fanout": 8, "max_automation_depth": 2,
		"initial_grants": []map[string]any{
			{"subject": map[string]any{"kind": "user", "ref": owner.id}, "can_read": true, "can_write": true, "can_admin": true},
			{"subject": map[string]any{"kind": "user", "ref": target.id}, "can_read": true},
			{"subject": map[string]any{"kind": "user", "ref": writeOnly.id}, "can_write": true},
		},
	}
	withoutAuth := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/channels", "", org.TenantID, channelBody, nil)
	if withoutAuth.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated channel create = %d: %s", withoutAuth.status, withoutAuth.raw)
	}
	createdChannel := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/channels", owner.token, org.TenantID, channelBody, nil)
	if createdChannel.status != http.StatusCreated {
		readiness, readinessErr := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background())
		t.Fatalf("create sealed Channel = %d: %s; readiness=%+v err=%v",
			createdChannel.status, createdChannel.raw, readiness, readinessErr)
	}
	created := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, createdChannel)
	channelID := created.Channel.ID
	if channelID.IsZero() || created.ETag != `"v1"` || len(created.Grants) != 3 ||
		created.Channel.ContentProtection != sessions.ContentProtectionApplicationSealed {
		t.Fatalf("created sealed Channel = %+v", created)
	}
	var targetGrantID model.ID
	for _, grant := range created.Grants {
		if grant.Subject.Kind == sessions.SubjectUser && grant.Subject.Ref == target.id.String() {
			targetGrantID = grant.ID
			break
		}
	}
	if targetGrantID.IsZero() {
		t.Fatalf("created sealed Channel omitted target grant: %+v", created.Grants)
	}

	for name, user := range map[string]communicationHTTPTestUser{
		"third": third, "write-only": writeOnly,
	} {
		denied := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/"+channelID.String(), user.token, org.TenantID, nil, nil)
		if denied.status != http.StatusNotFound {
			t.Fatalf("%s Channel read = %d: %s", name, denied.status, denied.raw)
		}
	}
	if allowed := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channelID.String(), target.token, org.TenantID, nil, nil); allowed.status != http.StatusOK {
		t.Fatalf("read-granted Channel read = %d: %s", allowed.status, allowed.raw)
	}
	if crossed := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channelID.String(), target.token, other.TenantID, nil, nil); crossed.status != http.StatusNotFound {
		t.Fatalf("cross-tenant Channel read = %d: %s", crossed.status, crossed.raw)
	}

	sealedSecret := "sealed-body-must-not-leak"
	sendBody := map[string]any{
		"channel_id": channelID,
		"recipient":  map[string]any{"kind": "user", "ref": target.id},
		"content": map[string]any{
			"subject": "Governed sealed notice",
			"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": sealedSecret}},
		},
		"urgency": "high",
	}
	readOnlySendBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	readOnlySend := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", target.token, org.TenantID, sendBody,
		map[string]string{"Idempotency-Key": model.NewID().String()})
	if readOnlySend.status != http.StatusForbidden || strings.Contains(string(readOnlySend.raw), sealedSecret) {
		t.Fatalf("local read-only send = %d: %s", readOnlySend.status, readOnlySend.raw)
	}
	assertCommunicationHTTPTestNoEffects(
		t, eng, org.TenantID, readOnlySendBefore, "local read-only send",
	)
	sealedSendKey := model.NewID().String()
	sentResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", owner.token, org.TenantID, sendBody,
		map[string]string{"Idempotency-Key": sealedSendKey})
	if sentResponse.status != http.StatusCreated {
		t.Fatalf("send sealed Message = %d: %s", sentResponse.status, sentResponse.raw)
	}
	sent := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, sentResponse)
	assertCommunicationStoredPayload(t, eng, org.TenantID, sent.MessageID, "sealed_v1", sealedSecret)
	sendReplay := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", owner.token, org.TenantID, sendBody,
		map[string]string{"Idempotency-Key": sealedSendKey})
	replayedSend := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, sendReplay)
	if sendReplay.status != http.StatusOK ||
		!replayedSend.Replayed ||
		replayedSend.CommandID != sent.CommandID || replayedSend.MessageID != sent.MessageID ||
		replayedSend.DeliveryID != sent.DeliveryID || replayedSend.EventID != sent.EventID ||
		replayedSend.AuditSeq != sent.AuditSeq {
		t.Fatalf("sealed send replay = %d: %s; first=%s", sendReplay.status, sendReplay.raw, sentResponse.raw)
	}
	changedSend := map[string]any{
		"channel_id": channelID,
		"recipient":  map[string]any{"kind": "user", "ref": target.id},
		"content": map[string]any{
			"subject": "Different sealed notice",
			"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": "different-input"}},
		},
		"urgency": "high",
	}
	if conflict := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", owner.token, org.TenantID, changedSend,
		map[string]string{"Idempotency-Key": sealedSendKey}); conflict.status != http.StatusConflict {
		t.Fatalf("sealed send idempotency reuse = %d: %s", conflict.status, conflict.raw)
	}
	exerciseCommunicationHTTPUnknownEvidence(
		t, eng, target, org.TenantID, created.Channel.WorkspaceID, sent.DeliveryID, sealedSecret,
	)

	deniedDeliveryBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	deniedDelivery := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), third.token, org.TenantID, nil, nil)
	if deniedDelivery.status != http.StatusNotFound || strings.Contains(string(deniedDelivery.raw), sealedSecret) {
		t.Fatalf("unaddressed Delivery read = %d: %s", deniedDelivery.status, deniedDelivery.raw)
	}
	assertCommunicationHTTPTestNoEffects(
		t, eng, org.TenantID, deniedDeliveryBefore, "unaddressed Delivery read",
	)
	readDelivery := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), target.token, org.TenantID, nil, nil)
	if readDelivery.status != http.StatusOK {
		t.Fatalf("target Delivery read = %d: %s", readDelivery.status, readDelivery.raw)
	}
	opened := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, readDelivery)
	if len(opened.Message.Content.Blocks) != 1 || opened.Message.Content.Blocks[0].Text != sealedSecret ||
		opened.Delivery.Recipient != (sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: target.id.String()}) {
		t.Fatalf("opened sealed Delivery = %+v", opened)
	}
	crossTenantBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	if crossed := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), target.token, other.TenantID, nil, nil); crossed.status != http.StatusNotFound || strings.Contains(string(crossed.raw), sealedSecret) {
		t.Fatalf("cross-tenant Delivery read = %d: %s", crossed.status, crossed.raw)
	}
	assertCommunicationHTTPTestNoEffects(
		t, eng, org.TenantID, crossTenantBefore, "cross-tenant Delivery read",
	)

	inboxResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(created.Channel.WorkspaceID, 10, ""),
		target.token, org.TenantID, nil, nil)
	if inboxResponse.status != http.StatusOK {
		t.Fatalf("target inbox = %d: %s", inboxResponse.status, inboxResponse.raw)
	}
	inbox := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, inboxResponse)
	if len(inbox.Items) != 1 || inbox.Items[0].Delivery.ID != sent.DeliveryID {
		t.Fatalf("target inbox = %+v", inbox)
	}
	thirdInbox := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(created.Channel.WorkspaceID, 10, ""),
		third.token, org.TenantID, nil, nil)
	if thirdInbox.status != http.StatusOK || strings.Contains(string(thirdInbox.raw), sealedSecret) {
		t.Fatalf("third-party inbox = %d: %s", thirdInbox.status, thirdInbox.raw)
	}
	if page := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, thirdInbox); len(page.Items) != 0 {
		t.Fatalf("third-party inbox exposed %d items", len(page.Items))
	}

	cursorGet := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestCursorPath(target.id.String(), created.Channel.WorkspaceID, inbox.CursorTarget),
		target.token, org.TenantID, nil, nil)
	if cursorGet.status != http.StatusOK {
		t.Fatalf("cursor GET = %d: %s", cursorGet.status, cursorGet.raw)
	}
	cursor := communicationHTTPTestDecode[sessions.DirectNoticeCursorTokenResult](t, cursorGet)
	if !strings.HasPrefix(cursor.CursorToken, "c2v2.") {
		t.Fatalf("cursor token result = %+v", cursor)
	}
	cursorKey := model.NewID().String()
	cursorPut := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+target.id.String(), target.token, org.TenantID,
		map[string]any{"cursor": cursor.CursorToken, "delivery_id": sent.DeliveryID}, map[string]string{
			"If-Match": cursor.ETag, "Idempotency-Key": cursorKey,
		})
	if cursorPut.status != http.StatusOK {
		t.Fatalf("cursor PUT = %d: %s", cursorPut.status, cursorPut.raw)
	}
	advanced := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, cursorPut)
	if advanced.Version != 1 || advanced.Projection.LastSeenSeq != opened.Delivery.DeliverySeq {
		t.Fatalf("cursor advance = %+v", advanced)
	}
	cursorReplay := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+target.id.String(), target.token, org.TenantID,
		map[string]any{"cursor": cursor.CursorToken, "delivery_id": sent.DeliveryID}, map[string]string{
			"If-Match": cursor.ETag, "Idempotency-Key": cursorKey,
		})
	replayedAdvance := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, cursorReplay)
	if cursorReplay.status != http.StatusOK || !replayedAdvance.Replayed ||
		replayedAdvance.CommandID != advanced.CommandID || replayedAdvance.CursorID != advanced.CursorID ||
		replayedAdvance.Version != advanced.Version || replayedAdvance.Projection != advanced.Projection ||
		replayedAdvance.AuditSeq != advanced.AuditSeq {
		t.Fatalf("cursor replay = %d: %s; first=%s", cursorReplay.status, cursorReplay.raw, cursorPut.raw)
	}

	ackKey := model.NewID().String()
	ackResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String()+"/ack", target.token, org.TenantID,
		nil, map[string]string{
			"If-Match": fmt.Sprintf(`"v%d"`, opened.Delivery.Version), "Idempotency-Key": ackKey,
		})
	if ackResponse.status != http.StatusOK {
		t.Fatalf("Delivery Ack = %d: %s", ackResponse.status, ackResponse.raw)
	}
	acknowledged := communicationHTTPTestDecode[sessions.DirectNoticeDeliveryAckResult](t, ackResponse)
	if acknowledged.AckID.IsZero() || acknowledged.State != sessions.DeliveryAcknowledged {
		t.Fatalf("Delivery Ack = %+v", acknowledged)
	}
	ackReplay := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String()+"/ack", target.token, org.TenantID,
		nil, map[string]string{
			"If-Match": fmt.Sprintf(`"v%d"`, opened.Delivery.Version), "Idempotency-Key": ackKey,
		})
	replayedAck := communicationHTTPTestDecode[sessions.DirectNoticeDeliveryAckResult](t, ackReplay)
	if ackReplay.status != http.StatusOK || !replayedAck.Replayed ||
		replayedAck.CommandID != acknowledged.CommandID || replayedAck.AckID != acknowledged.AckID ||
		replayedAck.DeliveryID != acknowledged.DeliveryID || replayedAck.EventID != acknowledged.EventID ||
		replayedAck.AuditSeq != acknowledged.AuditSeq {
		t.Fatalf("Ack replay = %d: %s; first=%s", ackReplay.status, ackReplay.raw, ackResponse.raw)
	}
	scheduledSecret := exerciseCommunicationHTTPScheduledVisibility(
		t, eng, owner, target, org.TenantID, channelID, created.Channel.WorkspaceID,
	)

	writeOnlySendKey := model.NewID().String()
	writeOnlySent := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", writeOnly.token, org.TenantID, sendBody,
		map[string]string{"Idempotency-Key": writeOnlySendKey})
	if writeOnlySent.status != http.StatusCreated {
		t.Fatalf("write-only grant send = %d: %s", writeOnlySent.status, writeOnlySent.raw)
	}
	writeOnlyMessage := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, writeOnlySent)
	writeOnlyReadBefore := communicationHTTPTestEffects(t, eng, org.TenantID)
	writeOnlyRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/messages/"+writeOnlyMessage.MessageID.String(), writeOnly.token, org.TenantID, nil, nil)
	if writeOnlyRead.status != http.StatusNotFound || strings.Contains(string(writeOnlyRead.raw), sealedSecret) {
		t.Fatalf("write-only grant Message read = %d: %s", writeOnlyRead.status, writeOnlyRead.raw)
	}
	assertCommunicationHTTPTestNoEffects(
		t, eng, org.TenantID, writeOnlyReadBefore, "write-only grant Message read",
	)

	downgrade := communicationHTTPTestRequest(t, eng, http.MethodPatch,
		"/v1/m/sessions/channels", owner.token, org.TenantID,
		map[string]any{"channel_id": channelID, "content_protection": "storage"},
		map[string]string{"If-Match": created.ETag})
	if downgrade.status != http.StatusBadRequest {
		t.Fatalf("sealed Channel downgrade = %d: %s", downgrade.status, downgrade.raw)
	}
	stillSealed := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channelID.String(), owner.token, org.TenantID, nil, nil)
	if stillSealed.status != http.StatusOK {
		t.Fatalf("read Channel after downgrade refusal = %d: %s", stillSealed.status, stillSealed.raw)
	}
	channelAfter := communicationHTTPTestDecode[sessions.Channel](t, stillSealed)
	if channelAfter.Version != 1 || channelAfter.ContentProtection != sessions.ContentProtectionApplicationSealed {
		t.Fatalf("Channel after downgrade refusal = %+v", channelAfter)
	}

	plainBody := map[string]any{
		"workspace_id": defaultWorkspace,
		"slug":         "plain-coordination", "name": "Plain coordination", "kind": "coordination",
		"sensitivity": "internal", "content_protection": "storage",
		"default_ack_policy": "none", "default_ack_timeout_ms": 0,
		"default_wake": "none", "max_fanout": 4, "max_automation_depth": 1,
		"initial_grants": []map[string]any{
			{"subject": map[string]any{"kind": "user", "ref": owner.id}, "can_read": true, "can_write": true, "can_admin": true},
			{"subject": map[string]any{"kind": "user", "ref": target.id}, "can_read": true},
		},
	}
	plainChannelResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/channels", owner.token, org.TenantID, plainBody, nil)
	if plainChannelResponse.status != http.StatusCreated {
		t.Fatalf("create explicit plain Channel = %d: %s", plainChannelResponse.status, plainChannelResponse.raw)
	}
	plainChannel := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, plainChannelResponse)
	plainSecret := "explicit-plain-body"
	plainSendBody := map[string]any{
		"channel_id": plainChannel.Channel.ID,
		"recipient":  map[string]any{"kind": "user", "ref": target.id},
		"content": map[string]any{
			"subject": "Explicit plain notice",
			"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": plainSecret}},
		},
		"urgency": "normal",
	}
	plainSentResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", owner.token, org.TenantID, plainSendBody,
		map[string]string{"Idempotency-Key": model.NewID().String()})
	if plainSentResponse.status != http.StatusCreated {
		t.Fatalf("send explicit plain Message = %d: %s", plainSentResponse.status, plainSentResponse.raw)
	}
	plainSent := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, plainSentResponse)
	assertCommunicationStoredPayload(t, eng, org.TenantID, plainSent.MessageID, "plain_json", plainSecret)
	plainRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+plainSent.DeliveryID.String(), target.token, org.TenantID, nil, nil)
	if plainRead.status != http.StatusOK || !strings.Contains(string(plainRead.raw), plainSecret) {
		t.Fatalf("read explicit plain Delivery = %d: %s", plainRead.status, plainRead.raw)
	}
	barrierRecovery := exerciseCommunicationHTTPRevocationBarrier(
		t, eng, loginBody.Token, owner, target, org.TenantID,
		created.Channel.WorkspaceID, channelID, targetGrantID,
	)
	exerciseCommunicationHTTPAgent(t, eng, owner, target, org.TenantID,
		nonDefaultChannel, nonDefaultChannel.ID)
	exerciseCommunicationHTTPSessions(
		t, &eng, estate, owner, org.TenantID, other.TenantID, created.Channel, channelID,
		barrierRecovery,
	)
	assertCommunicationMarkersAbsentFromProjections(t, eng, org.TenantID,
		sealedSecret,
		plainSecret,
		"standalone-agent-to-user-sealed",
		"user-to-standalone-agent-sealed",
		"session-a-to-session-b-sealed",
		"session-b-reply",
		"Transfer session-owned work",
		"Claim takeover rollback",
		"revoked-grant-cursor-barrier-secret",
		scheduledSecret,
	)
}

type communicationHTTPBarrierRecovery struct {
	target             communicationHTTPTestUser
	workspace          model.ID
	targetDeliveryID   model.ID
	barrierDeliveryID  model.ID
	barrierDeliverySeq int64
	cursor             sessions.DirectNoticeCursorTokenResult
	key                string
	result             sessions.DirectNoticeCursorAdvanceResult
}

func exerciseCommunicationHTTPRevocationBarrier(
	t *testing.T,
	eng *engine,
	adminToken string,
	owner communicationHTTPTestUser,
	target communicationHTTPTestUser,
	tenant model.TenantID,
	workspace model.ID,
	channelID model.ID,
	targetGrantID model.ID,
) communicationHTTPBarrierRecovery {
	t.Helper()
	secret := "revoked-grant-cursor-barrier-secret"
	sentResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", owner.token, tenant, map[string]any{
			"channel_id": channelID,
			"recipient":  map[string]any{"kind": "user", "ref": target.id},
			"content": map[string]any{
				"subject": "Revocation barrier notice",
				"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": secret}},
			},
		}, map[string]string{"Idempotency-Key": model.NewID().String()})
	if sentResponse.status != http.StatusCreated {
		t.Fatalf("send revocation-barrier Message = %d: %s", sentResponse.status, sentResponse.raw)
	}
	sent := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, sentResponse)
	readResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), target.token, tenant, nil, nil)
	if readResponse.status != http.StatusOK || !strings.Contains(string(readResponse.raw), secret) {
		t.Fatalf("read revocation-barrier Delivery = %d: %s", readResponse.status, readResponse.raw)
	}
	opened := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, readResponse)
	inboxResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(workspace, 200, ""), target.token, tenant, nil, nil)
	if inboxResponse.status != http.StatusOK {
		t.Fatalf("list pre-revocation inbox = %d: %s", inboxResponse.status, inboxResponse.raw)
	}
	inbox := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, inboxResponse)
	if inbox.CursorTarget == "" || inbox.Items[len(inbox.Items)-1].Delivery.ID != sent.DeliveryID {
		t.Fatalf("pre-revocation inbox target = %+v", inbox)
	}
	tokenResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestCursorPath(target.id.String(), workspace, inbox.CursorTarget),
		target.token, tenant, nil, nil)
	if tokenResponse.status != http.StatusOK {
		t.Fatalf("mint pre-revocation cursor = %d: %s", tokenResponse.status, tokenResponse.raw)
	}
	preRevocation := communicationHTTPTestDecode[sessions.DirectNoticeCursorTokenResult](t, tokenResponse)
	channelResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channelID.String(), owner.token, tenant, nil, nil)
	if channelResponse.status != http.StatusOK {
		t.Fatalf("read Channel before grant revocation = %d: %s", channelResponse.status, channelResponse.raw)
	}
	channel := communicationHTTPTestDecode[sessions.Channel](t, channelResponse)
	revokedResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		fmt.Sprintf("/v1/m/sessions/channels/%s/grants/%s/revoke", channelID, targetGrantID),
		owner.token, tenant, nil,
		map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, channel.Version)})
	if revokedResponse.status != http.StatusOK {
		t.Fatalf("revoke target Channel grant = %d: %s", revokedResponse.status, revokedResponse.raw)
	}
	revoked := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, revokedResponse)
	if revoked.Grant == nil || revoked.Grant.ID != targetGrantID ||
		revoked.Grant.State != sessions.ChannelGrantRevoked {
		t.Fatalf("revoked target Channel grant = %+v", revoked)
	}
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel_grant")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{
			{Column: "channel_id", Op: model.OpEq, Value: channelID.String()},
			{Column: "subject_kind", Op: model.OpEq, Value: string(sessions.SubjectUser)},
			{Column: "subject_ref", Op: model.OpEq, Value: target.id.String()},
		}, Limit: 10})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.String("state") == string(sessions.ChannelGrantActive) {
				return fmt.Errorf("target still has active grant %s", row.String(model.ColID))
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("revoked target grant persistence: %v", err)
	}

	deniedBefore := communicationHTTPTestEffects(t, eng, tenant)
	deniedRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), target.token, tenant, nil, nil)
	if deniedRead.status != http.StatusNotFound || strings.Contains(string(deniedRead.raw), secret) {
		t.Fatalf("revoked-grant Delivery read = %d: %s", deniedRead.status, deniedRead.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, deniedBefore, "revoked-grant Delivery read")

	barrierKey := model.NewID().String()
	advanceResponse := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+target.id.String(), target.token, tenant,
		map[string]any{"cursor": preRevocation.CursorToken, "delivery_id": sent.DeliveryID},
		map[string]string{
			"If-Match": preRevocation.ETag, "Idempotency-Key": barrierKey,
		})
	if advanceResponse.status != http.StatusOK {
		t.Fatalf("advance across revoked grant = %d: %s", advanceResponse.status, advanceResponse.raw)
	}
	barrierResult := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, advanceResponse)
	if barrierResult.Projection.BarrierDeliveryID.IsZero() ||
		barrierResult.Projection.BarrierReason != sessions.BarrierTemporarilyInvisible ||
		barrierResult.Projection.LastSeenSeq >= opened.Delivery.DeliverySeq {
		t.Fatalf("revoked grant cursor barrier = %+v", barrierResult)
	}
	activeBarriers := communicationHTTPTestActiveBarrierDeliveries(
		t, eng, tenant, sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: target.id.String()},
	)
	if len(activeBarriers) == 0 ||
		!communicationHTTPTestContainsID(activeBarriers, barrierResult.Projection.BarrierDeliveryID) {
		t.Fatalf("active revoked-grant barriers = %v", activeBarriers)
	}

	regrantResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/channels/"+channelID.String()+"/grants", owner.token, tenant,
		map[string]any{
			"subject": map[string]any{"kind": "user", "ref": target.id}, "can_read": true,
		}, map[string]string{"If-Match": revoked.ETag})
	if regrantResponse.status != http.StatusOK {
		t.Fatalf("regrant target Channel read = %d: %s", regrantResponse.status, regrantResponse.raw)
	}
	if barriers := communicationHTTPTestActiveBarrierDeliveries(
		t, eng, tenant, sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: target.id.String()},
	); len(barriers) != len(activeBarriers) ||
		!communicationHTTPTestContainsID(barriers, barrierResult.Projection.BarrierDeliveryID) {
		t.Fatalf("regrant cleared cursor barrier without explicit PUT: %v", barriers)
	}
	freshInboxResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(workspace, 200, ""), target.token, tenant, nil, nil)
	if freshInboxResponse.status != http.StatusOK {
		t.Fatalf("list post-regrant inbox = %d: %s", freshInboxResponse.status, freshInboxResponse.raw)
	}
	freshInbox := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, freshInboxResponse)
	freshTokenResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestCursorPath(target.id.String(), workspace, freshInbox.CursorTarget),
		target.token, tenant, nil, nil)
	if freshTokenResponse.status != http.StatusOK {
		t.Fatalf("mint post-regrant cursor = %d: %s", freshTokenResponse.status, freshTokenResponse.raw)
	}
	if barriers := communicationHTTPTestActiveBarrierDeliveries(
		t, eng, tenant, sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: target.id.String()},
	); len(barriers) != len(activeBarriers) {
		t.Fatalf("cursor GET cleared durable barrier: %v", barriers)
	}
	freshToken := communicationHTTPTestDecode[sessions.DirectNoticeCursorTokenResult](t, freshTokenResponse)
	if !strings.HasPrefix(freshToken.CursorToken, "c2v2.") || freshToken.Version != barrierResult.Version {
		t.Fatalf("post-regrant cursor observation = %+v; barrier=%+v", freshToken, barrierResult)
	}

	otherWorkspaceResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/workspaces", owner.token, tenant,
		map[string]any{"name": "K3 membership change", "slug": "k3-membership-change"}, nil)
	if otherWorkspaceResponse.status != http.StatusCreated {
		t.Fatalf("create membership-change workspace = %d: %s",
			otherWorkspaceResponse.status, otherWorkspaceResponse.raw)
	}
	otherWorkspaceBody := communicationHTTPTestDecode[struct {
		ID model.ID `json:"id"`
	}](t, otherWorkspaceResponse)
	otherWorkspace := otherWorkspaceBody.ID
	confinedResponse := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/memberships",
		adminToken, "", map[string]any{
			"user_id": target.id, "tenant": tenant, "role": auth.RoleEditor,
			"workspace_id": otherWorkspace,
		}, nil)
	if confinedResponse.status != http.StatusCreated {
		t.Fatalf("confine target membership = %d: %s", confinedResponse.status, confinedResponse.raw)
	}
	confinedBefore := communicationHTTPTestEffects(t, eng, tenant)
	confinedRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), target.token, tenant, nil, nil)
	if confinedRead.status != http.StatusNotFound || strings.Contains(string(confinedRead.raw), secret) {
		t.Fatalf("membership-confined Delivery read = %d: %s", confinedRead.status, confinedRead.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, confinedBefore, "membership-confined Delivery read")
	restoredResponse := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/memberships",
		adminToken, "", map[string]any{
			"user_id": target.id, "tenant": tenant, "role": auth.RoleEditor,
		}, nil)
	if restoredResponse.status != http.StatusCreated {
		t.Fatalf("restore tenant-wide target membership = %d: %s", restoredResponse.status, restoredResponse.raw)
	}
	restoredRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), target.token, tenant, nil, nil)
	if restoredRead.status != http.StatusOK || !strings.Contains(string(restoredRead.raw), secret) {
		t.Fatalf("restored membership Delivery read = %d: %s", restoredRead.status, restoredRead.raw)
	}
	barrierRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+barrierResult.Projection.BarrierDeliveryID.String(),
		target.token, tenant, nil, nil)
	if barrierRead.status != http.StatusOK {
		t.Fatalf("read projected barrier Delivery after regrant = %d: %s",
			barrierRead.status, barrierRead.raw)
	}
	barrierOpened := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, barrierRead)
	return communicationHTTPBarrierRecovery{
		target: target, workspace: workspace, targetDeliveryID: sent.DeliveryID,
		barrierDeliveryID:  barrierResult.Projection.BarrierDeliveryID,
		barrierDeliverySeq: barrierOpened.Delivery.DeliverySeq, cursor: preRevocation,
		key: barrierKey, result: barrierResult,
	}
}

func exerciseCommunicationHTTPAgent(
	t *testing.T,
	eng *engine,
	owner communicationHTTPTestUser,
	target communicationHTTPTestUser,
	tenant model.TenantID,
	channel sessions.Channel,
	channelID model.ID,
) {
	t.Helper()
	sponsorRef := "human:k3-http-owner:" + owner.id.String()
	scimUpdate := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/scim/v2/Users/"+owner.id.String(), owner.token, tenant,
		map[string]any{
			"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
			"userName": "owner@k3-http.test", "displayName": "K3 owner",
			"externalId": sponsorRef, "active": true,
		}, map[string]string{"Content-Type": "application/scim+json"})
	if scimUpdate.status != http.StatusOK {
		t.Fatalf("set agent sponsor external identity = %d: %s", scimUpdate.status, scimUpdate.raw)
	}
	// This is directory-source materialization, not a communication fixture: the
	// roster row is written through the typed Store API and then every lifecycle,
	// agent, credential, channel and message action below uses authenticated
	// product operations. No communication row or credential is seeded.
	if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: "K3 HTTP owner", Kind: "user", ExternalID: sponsorRef,
			Provider: "test-roster",
			Metadata: map[string]any{"principal_type": "human"},
		})
		return err
	}); err != nil {
		t.Fatalf("materialize sponsor roster identity: %v", err)
	}

	agentRef := "agent:k3-http-standalone"
	registered := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/governance/agents", owner.token, tenant, map[string]any{
			"identity_ref": agentRef, "source": "test-roster",
			"sponsor_ref": sponsorRef, "criticality": "medium",
		}, nil)
	if registered.status != http.StatusCreated {
		t.Fatalf("register agent lifecycle = %d: %s", registered.status, registered.raw)
	}
	createdAgent := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/agents", owner.token, tenant, map[string]any{
			"name": "K3 standalone agent", "kind": "api", "external_id": agentRef,
			"status": "active", "workspace_id": channel.WorkspaceID,
		}, nil)
	if createdAgent.status != http.StatusCreated {
		t.Fatalf("create core Agent = %d: %s", createdAgent.status, createdAgent.raw)
	}
	createdAgentBody := communicationHTTPTestDecode[struct {
		ID model.ID `json:"id"`
	}](t, createdAgent)
	bound := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/governance/agents/"+createdAgentBody.ID.String()+"/identity",
		owner.token, tenant, map[string]any{"identity_ref": agentRef}, nil)
	if bound.status != http.StatusOK {
		t.Fatalf("bind agent identity = %d: %s", bound.status, bound.raw)
	}
	boundBody := communicationHTTPTestDecode[struct {
		IdentityID model.ID `json:"identity_id"`
	}](t, bound)
	if boundBody.IdentityID.IsZero() {
		t.Fatal("bound agent identity is empty")
	}
	agent := communicationHTTPTestAgent{
		identityID: boundBody.IdentityID,
		externalID: agentRef,
		token:      exchangeCommunicationHTTPTestAgentToken(t, eng, owner.token, agentRef),
	}
	principal, err := eng.authr.Authenticate(context.Background(), agent.token)
	if err != nil || principal.AgentIdentity != agent.externalID || principal.UserID != owner.id {
		t.Fatalf("agent credential identity = %+v, err %v", principal, err)
	}

	channelRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channelID.String(), owner.token, tenant, nil, nil)
	if channelRead.status != http.StatusOK {
		t.Fatalf("read Channel before agent grant = %d: %s", channelRead.status, channelRead.raw)
	}
	currentChannel := communicationHTTPTestDecode[sessions.Channel](t, channelRead)
	agentGrant := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/channels/"+channelID.String()+"/grants", owner.token, tenant,
		map[string]any{
			"subject":  map[string]any{"kind": "agent", "ref": agent.identityID},
			"can_read": true, "can_write": true,
		}, map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, currentChannel.Version)})
	if agentGrant.status != http.StatusOK {
		t.Fatalf("grant Channel to agent = %d: %s", agentGrant.status, agentGrant.raw)
	}
	agentGrantResult := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, agentGrant)
	if agentGrantResult.Grant == nil {
		t.Fatalf("agent grant result = %+v", agentGrantResult)
	}

	agentSecret := "standalone-agent-to-user-sealed"
	agentSend := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", agent.token, tenant, map[string]any{
			"channel_id": channelID,
			"recipient":  map[string]any{"kind": "user", "ref": target.id},
			"content": map[string]any{
				"subject": "Agent governed notice",
				"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": agentSecret}},
			},
		}, map[string]string{"Idempotency-Key": model.NewID().String()})
	if agentSend.status != http.StatusCreated {
		t.Fatalf("standalone agent send = %d: %s", agentSend.status, agentSend.raw)
	}
	agentSent := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, agentSend)
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.message")
		if err != nil {
			return err
		}
		row, err := repo.Get(context.Background(), agentSent.MessageID)
		if err != nil {
			return err
		}
		if row.String("sender_kind") != "agent" || row.String("sender_ref") != agent.identityID.String() ||
			row.String("sender_ref") == owner.id.String() {
			return fmt.Errorf("agent sender attribution = %s:%s", row.String("sender_kind"), row.String("sender_ref"))
		}
		return nil
	}); err != nil {
		t.Fatalf("standalone agent sender evidence: %v", err)
	}
	if read := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+agentSent.DeliveryID.String(), target.token, tenant,
		nil, nil); read.status != http.StatusOK || !strings.Contains(string(read.raw), agentSecret) {
		t.Fatalf("read standalone agent Delivery = %d: %s", read.status, read.raw)
	}

	toAgentSecret := "user-to-standalone-agent-sealed"
	toAgent := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/messages/send", owner.token, tenant, map[string]any{
			"channel_id": channelID,
			"recipient":  map[string]any{"kind": "agent", "ref": agent.identityID},
			"content": map[string]any{
				"subject": "Agent mailbox notice",
				"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": toAgentSecret}},
			},
		}, map[string]string{"Idempotency-Key": model.NewID().String()})
	if toAgent.status != http.StatusCreated {
		t.Fatalf("send to standalone agent = %d: %s", toAgent.status, toAgent.raw)
	}
	toAgentResult := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, toAgent)
	agentRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+toAgentResult.DeliveryID.String(), agent.token, tenant, nil, nil)
	if agentRead.status != http.StatusOK || !strings.Contains(string(agentRead.raw), toAgentSecret) {
		t.Fatalf("standalone agent Delivery read = %d: %s", agentRead.status, agentRead.raw)
	}
	agentOpened := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, agentRead)
	agentInbox := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(channel.WorkspaceID, 20, ""),
		agent.token, tenant, nil, nil)
	if agentInbox.status != http.StatusOK || !strings.Contains(string(agentInbox.raw), toAgentSecret) {
		t.Fatalf("standalone agent inbox = %d: %s", agentInbox.status, agentInbox.raw)
	}
	agentInboxPage := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, agentInbox)
	agentCursorGet := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestCursorPath(
			agent.identityID.String(), channel.WorkspaceID, agentInboxPage.CursorTarget,
		), agent.token, tenant, nil, nil)
	if agentCursorGet.status != http.StatusOK {
		t.Fatalf("standalone agent cursor GET = %d: %s", agentCursorGet.status, agentCursorGet.raw)
	}
	agentCursor := communicationHTTPTestDecode[sessions.DirectNoticeCursorTokenResult](t, agentCursorGet)
	agentCursorKey := model.NewID().String()
	agentCursorPut := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+agent.identityID.String(),
		agent.token, tenant, map[string]any{
			"cursor": agentCursor.CursorToken, "delivery_id": toAgentResult.DeliveryID,
		}, map[string]string{
			"If-Match": agentCursor.ETag, "Idempotency-Key": agentCursorKey,
		})
	if agentCursorPut.status != http.StatusOK {
		t.Fatalf("standalone agent cursor PUT = %d: %s", agentCursorPut.status, agentCursorPut.raw)
	}
	agentAdvanced := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, agentCursorPut)
	agentCursorReplay := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+agent.identityID.String(),
		agent.token, tenant, map[string]any{
			"cursor": agentCursor.CursorToken, "delivery_id": toAgentResult.DeliveryID,
		}, map[string]string{
			"If-Match": agentCursor.ETag, "Idempotency-Key": agentCursorKey,
		})
	agentReplayed := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](
		t, agentCursorReplay,
	)
	if agentCursorReplay.status != http.StatusOK || !agentReplayed.Replayed ||
		agentReplayed.CommandID != agentAdvanced.CommandID ||
		agentReplayed.CursorID != agentAdvanced.CursorID ||
		agentReplayed.Version != agentAdvanced.Version ||
		agentReplayed.Projection != agentAdvanced.Projection ||
		agentReplayed.AuditSeq != agentAdvanced.AuditSeq {
		t.Fatalf("standalone agent cursor replay = %d: %s; first=%s",
			agentCursorReplay.status, agentCursorReplay.raw, agentCursorPut.raw)
	}
	agentAckKey := model.NewID().String()
	agentAck := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/deliveries/"+toAgentResult.DeliveryID.String()+"/ack",
		agent.token, tenant, nil, map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, agentOpened.Delivery.Version),
			"Idempotency-Key": agentAckKey,
		})
	if agentAck.status != http.StatusOK {
		t.Fatalf("standalone agent Ack = %d: %s", agentAck.status, agentAck.raw)
	}
	retired := communicationHTTPTestRequest(t, eng, http.MethodDelete,
		"/v1/agents/"+createdAgentBody.ID.String(), owner.token, tenant, nil, nil)
	if retired.status != http.StatusNoContent {
		t.Fatalf("retire core Agent lifecycle = %d: %s", retired.status, retired.raw)
	}
	if stillAuthenticated, err := eng.authr.Authenticate(context.Background(), agent.token); err != nil ||
		stillAuthenticated.AgentIdentity != agent.externalID {
		t.Fatalf("retired Agent token no longer isolates lifecycle revalidation: %+v, %v",
			stillAuthenticated, err)
	}
	revokedBefore := communicationHTTPTestEffects(t, eng, tenant)
	revokedAgentChannelBody := map[string]any{
		"workspace_id": channel.WorkspaceID, "slug": "retired-agent-create",
		"name": "Retired Agent create", "initial_grants": []map[string]any{{
			"subject":  map[string]any{"kind": "agent", "ref": agent.identityID},
			"can_read": true, "can_write": true, "can_admin": true,
		}},
	}
	revokedAgentCalls := []struct {
		name, method, path string
		body               any
		headers            map[string]string
	}{
		{"channel create", http.MethodPost, "/v1/m/sessions/channels", revokedAgentChannelBody, nil},
		{"channel update", http.MethodPatch, "/v1/m/sessions/channels", map[string]any{"channel_id": channelID, "name": "revoked"}, map[string]string{"If-Match": agentGrantResult.ETag}},
		{"channel read", http.MethodGet, "/v1/m/sessions/channels/" + channelID.String(), nil, nil},
		{"channel grant", http.MethodPost, "/v1/m/sessions/channels/" + channelID.String() + "/grants", map[string]any{"subject": map[string]any{"kind": "agent", "ref": agent.identityID}, "can_read": true}, map[string]string{"If-Match": agentGrantResult.ETag}},
		{"channel revoke", http.MethodPost, "/v1/m/sessions/channels/" + channelID.String() + "/grants/" + agentGrantResult.Grant.ID.String() + "/revoke", nil, map[string]string{"If-Match": agentGrantResult.ETag}},
		{"message send", http.MethodPost, "/v1/m/sessions/messages/send", map[string]any{"channel_id": channelID, "recipient": map[string]any{"kind": "user", "ref": target.id}, "content": map[string]any{"subject": "retired-agent", "blocks": []map[string]any{{"type": "text", "text": "retired-agent-secret"}}}}, map[string]string{"Idempotency-Key": model.NewID().String()}},
		{"message read", http.MethodGet, "/v1/m/sessions/messages/" + toAgentResult.MessageID.String(), nil, nil},
		{"delivery read", http.MethodGet, "/v1/m/sessions/deliveries/" + toAgentResult.DeliveryID.String(), nil, nil},
		{"inbox", http.MethodGet, communicationHTTPTestInboxPath(channel.WorkspaceID, 20, ""), nil, nil},
		{"cursor target", http.MethodGet, communicationHTTPTestCursorPath(agent.identityID.String(), channel.WorkspaceID, agentInboxPage.CursorTarget), nil, nil},
		{"cursor receipt replay", http.MethodPut, "/v1/m/sessions/inbox/cursors/personal/" + agent.identityID.String(), map[string]any{"cursor": agentCursor.CursorToken, "delivery_id": toAgentResult.DeliveryID}, map[string]string{"If-Match": agentCursor.ETag, "Idempotency-Key": agentCursorKey}},
		{"Ack receipt replay", http.MethodPost, "/v1/m/sessions/deliveries/" + toAgentResult.DeliveryID.String() + "/ack", nil, map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, agentOpened.Delivery.Version), "Idempotency-Key": agentAckKey}},
		{"handoff offer", http.MethodPost, "/v1/m/sessions/handoffs", map[string]any{"channel_id": channelID, "work_item_id": model.NewID(), "recipient": map[string]any{"kind": "user", "ref": target.id}, "handoff": map[string]any{"summary": "retired", "next_action": "deny"}, "ack_deadline": time.Now().UTC().Add(time.Minute), "expected_owner_epoch": 1}, map[string]string{"If-Match": `"v1"`, "Idempotency-Key": model.NewID().String()}},
		{"handoff response", http.MethodPost, "/v1/m/sessions/handoffs/" + model.NewID().String() + "/responses", map[string]any{"transition": "reject"}, map[string]string{"If-Match": `"v1"`, "Idempotency-Key": model.NewID().String()}},
	}
	if len(revokedAgentCalls) != 14 {
		t.Fatalf("retired Agent communication route census = %d, want 14", len(revokedAgentCalls))
	}
	for _, call := range revokedAgentCalls {
		response := communicationHTTPTestRequest(t, eng, call.method, call.path,
			agent.token, tenant, call.body, call.headers)
		if response.status >= 200 && response.status < 300 ||
			strings.Contains(string(response.raw), toAgentSecret) ||
			strings.Contains(string(response.raw), "retired-agent-secret") {
			t.Fatalf("retired Agent %s = %d: %s", call.name, response.status, response.raw)
		}
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, revokedBefore,
		"retired Agent across communication routes")
}

func exerciseCommunicationHTTPSessions(
	t *testing.T,
	eng **engine,
	estate communicationHTTPTestStore,
	owner communicationHTTPTestUser,
	tenant model.TenantID,
	otherTenant model.TenantID,
	channel sessions.Channel,
	channelID model.ID,
	barrierRecovery communicationHTTPBarrierRecovery,
) {
	t.Helper()
	sessionA := createCommunicationHTTPTestSession(t, *eng, tenant, channel.WorkspaceID, "a")
	sessionB := createCommunicationHTTPTestSession(t, *eng, tenant, channel.WorkspaceID, "b")
	sessionThird := createCommunicationHTTPTestSession(t, *eng, tenant, channel.WorkspaceID, "third")
	sessionStale := createCommunicationHTTPTestSession(t, *eng, tenant, channel.WorkspaceID, "stale")
	sessionRevoked := createCommunicationHTTPTestSession(t, *eng, tenant, channel.WorkspaceID, "revoked")
	sessionTakeoverSource := createCommunicationHTTPTestSession(
		t, *eng, tenant, channel.WorkspaceID, "takeover-source",
	)
	sessionTakeover := createCommunicationHTTPTestSession(t, *eng, tenant, channel.WorkspaceID, "takeover")
	sessionConcurrentSource := createCommunicationHTTPTestSession(
		t, *eng, tenant, channel.WorkspaceID, "concurrent-source",
	)
	otherWorkspaceResponse := communicationHTTPTestRequest(t, *eng, http.MethodPost,
		"/v1/workspaces", owner.token, tenant,
		map[string]any{"name": "K3 wrong workspace", "slug": "k3-wrong-workspace"}, nil)
	if otherWorkspaceResponse.status != http.StatusCreated {
		t.Fatalf("create wrong-workspace session scope = %d: %s",
			otherWorkspaceResponse.status, otherWorkspaceResponse.raw)
	}
	otherWorkspaceBody := communicationHTTPTestDecode[struct {
		ID model.ID `json:"id"`
	}](t, otherWorkspaceResponse)
	otherWorkspace := otherWorkspaceBody.ID
	sessionWrongWorkspace := createCommunicationHTTPTestSession(
		t, *eng, tenant, otherWorkspace, "wrong-workspace",
	)
	var otherTenantWorkspace model.ID
	if err := (*eng).store.View(context.Background(), otherTenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(context.Background())
		otherTenantWorkspace = workspace.ID
		return err
	}); err != nil {
		t.Fatalf("read other-tenant default workspace: %v", err)
	}
	sessionWrongTenant := createCommunicationHTTPTestSession(
		t, *eng, otherTenant, otherTenantWorkspace, "wrong-tenant",
	)

	channelRead := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channelID.String(), owner.token, tenant, nil, nil)
	if channelRead.status != http.StatusOK {
		t.Fatalf("read Channel before session grants = %d: %s", channelRead.status, channelRead.raw)
	}
	currentChannel := communicationHTTPTestDecode[sessions.Channel](t, channelRead)
	channelETag := fmt.Sprintf(`"v%d"`, currentChannel.Version)
	grantSession := func(session communicationHTTPTestSession) {
		t.Helper()
		response := communicationHTTPTestRequest(t, *eng, http.MethodPost,
			"/v1/m/sessions/channels/"+channelID.String()+"/grants", owner.token, tenant,
			map[string]any{
				"subject":  map[string]any{"kind": "session", "ref": session.sid},
				"can_read": true, "can_write": true,
			}, map[string]string{"If-Match": channelETag})
		if response.status != http.StatusOK {
			t.Fatalf("grant Channel to %s = %d: %s", session.sid, response.status, response.raw)
		}
		result := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response)
		if result.Grant == nil || result.Grant.Subject.Ref != session.sid {
			t.Fatalf("session Channel grant = %+v", result)
		}
		channelETag = result.ETag
	}
	grantSession(sessionA)
	grantSession(sessionB)
	grantSession(sessionStale)
	grantSession(sessionRevoked)
	grantSession(sessionTakeoverSource)
	grantSession(sessionTakeover)
	grantSession(sessionConcurrentSource)

	secret := "session-a-to-session-b-sealed"
	sendBody := map[string]any{
		"channel_id": channelID,
		"recipient":  map[string]any{"kind": "session", "ref": sessionB.sid},
		"content": map[string]any{
			"subject": "Session governed notice",
			"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": secret}},
		},
		"urgency": "normal",
	}
	sentResponse := communicationHTTPTestRequest(t, *eng, http.MethodPost,
		"/v1/m/sessions/messages/send", sessionA.communication.Token, tenant,
		sendBody, map[string]string{"Idempotency-Key": model.NewID().String()})
	if sentResponse.status != http.StatusCreated {
		t.Fatalf("session A send = %d: %s", sentResponse.status, sentResponse.raw)
	}
	sent := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, sentResponse)
	deniedSend := func(
		label string,
		credential string,
		wantStatus int,
		body map[string]any,
	) {
		t.Helper()
		before := communicationHTTPTestEffects(t, *eng, tenant)
		response := communicationHTTPTestRequest(t, *eng, http.MethodPost,
			"/v1/m/sessions/messages/send", credential, tenant, body,
			map[string]string{"Idempotency-Key": model.NewID().String()})
		if response.status != wantStatus || strings.Contains(string(response.raw), secret) {
			t.Fatalf("%s send = %d: %s", label, response.status, response.raw)
		}
		assertCommunicationHTTPTestNoEffects(t, *eng, tenant, before, label+" send")
	}
	deniedSend("ungranted third session", sessionThird.communication.Token,
		http.StatusForbidden, sendBody)
	deniedSend("wrong-workspace session", sessionWrongWorkspace.communication.Token,
		http.StatusForbidden, sendBody)
	deniedSend("wrong-tenant session", sessionWrongTenant.communication.Token,
		http.StatusForbidden, sendBody)
	if err := (*eng).sessionsMod.Release(
		context.Background(), tenant, sessionStale.sid,
		sessionStale.agentRef, sessionStale.claim.Fence,
	); err != nil {
		t.Fatalf("release stale session Claim: %v", err)
	}
	reclaimed, err := (*eng).sessionsMod.Claim(
		context.Background(), tenant, sessionStale.sid, sessionStale.agentRef, time.Hour,
	)
	if err != nil || reclaimed.Fence <= sessionStale.claim.Fence {
		t.Fatalf("take over stale session Claim = %+v, err %v", reclaimed, err)
	}
	deniedSend("stale-Claim session", sessionStale.communication.Token,
		http.StatusServiceUnavailable, sendBody)
	issuer, err := auth.NewSystemOperator(
		"test:k3-http-runtime", "revoke exact test runtime credential",
	)
	if err != nil {
		t.Fatalf("runtime credential revoker: %v", err)
	}
	if err := (*eng).authr.RevokeCommunicationSessionCredential(
		context.Background(), issuer, sessionRevoked.communication.ID,
		auth.CommunicationSessionCredentialSpec{
			Tenant: tenant, WorkspaceID: channel.WorkspaceID,
			SessionRef: sessionRevoked.sid, RunRef: sessionRevoked.runRef,
			AgentRef: sessionRevoked.agentRef, ClaimFence: sessionRevoked.claim.Fence,
		},
	); err != nil {
		t.Fatalf("revoke exact communication-session credential: %v", err)
	}
	deniedSend("revoked-token session", sessionRevoked.communication.Token,
		http.StatusUnauthorized, sendBody)
	thirdReadBefore := communicationHTTPTestEffects(t, *eng, tenant)
	thirdRead := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(),
		sessionThird.communication.Token, tenant, nil, nil)
	if thirdRead.status != http.StatusNotFound || strings.Contains(string(thirdRead.raw), secret) {
		t.Fatalf("third session Delivery read = %d: %s", thirdRead.status, thirdRead.raw)
	}
	assertCommunicationHTTPTestNoEffects(
		t, *eng, tenant, thirdReadBefore, "third session Delivery read",
	)
	read := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(),
		sessionB.communication.Token, tenant, nil, nil)
	if read.status != http.StatusOK || !strings.Contains(string(read.raw), secret) {
		t.Fatalf("session B Delivery read = %d: %s", read.status, read.raw)
	}
	opened := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, read)
	inboxResponse := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		communicationHTTPTestInboxPath(channel.WorkspaceID, 20, ""),
		sessionB.communication.Token, tenant, nil, nil)
	if inboxResponse.status != http.StatusOK {
		t.Fatalf("session B inbox = %d: %s", inboxResponse.status, inboxResponse.raw)
	}
	inbox := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, inboxResponse)
	cursorGet := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		communicationHTTPTestCursorPath(sessionB.sid, channel.WorkspaceID, inbox.CursorTarget),
		sessionB.communication.Token, tenant, nil, nil)
	if cursorGet.status != http.StatusOK {
		t.Fatalf("session B cursor GET = %d: %s", cursorGet.status, cursorGet.raw)
	}
	cursor := communicationHTTPTestDecode[sessions.DirectNoticeCursorTokenResult](t, cursorGet)
	if !strings.HasPrefix(cursor.CursorToken, "c2v2.") {
		t.Fatalf("session B cursor = %+v", cursor)
	}
	cursorKey := model.NewID().String()
	cursorPut := communicationHTTPTestRequest(t, *eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+sessionB.sid,
		sessionB.communication.Token, tenant, map[string]any{
			"cursor": cursor.CursorToken, "delivery_id": sent.DeliveryID,
		},
		map[string]string{"If-Match": cursor.ETag, "Idempotency-Key": cursorKey})
	if cursorPut.status != http.StatusOK {
		t.Fatalf("session B cursor PUT = %d: %s", cursorPut.status, cursorPut.raw)
	}
	advanced := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, cursorPut)
	if advanced.Version != 1 || advanced.Projection.LastSeenSeq != opened.Delivery.DeliverySeq {
		t.Fatalf("session B cursor advance = %+v", advanced)
	}
	cursorReplay := communicationHTTPTestRequest(t, *eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+sessionB.sid,
		sessionB.communication.Token, tenant, map[string]any{
			"cursor": cursor.CursorToken, "delivery_id": sent.DeliveryID,
		},
		map[string]string{"If-Match": cursor.ETag, "Idempotency-Key": cursorKey})
	replayedCursor := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, cursorReplay)
	if cursorReplay.status != http.StatusOK || !replayedCursor.Replayed ||
		replayedCursor.CommandID != advanced.CommandID || replayedCursor.CursorID != advanced.CursorID ||
		replayedCursor.Version != advanced.Version || replayedCursor.Projection != advanced.Projection ||
		replayedCursor.AuditSeq != advanced.AuditSeq {
		t.Fatalf("session B cursor replay = %d: %s; first=%s",
			cursorReplay.status, cursorReplay.raw, cursorPut.raw)
	}
	sessionAckKey := model.NewID().String()
	sessionAckETag := fmt.Sprintf(`"v%d"`, opened.Delivery.Version)
	ack := communicationHTTPTestRequest(t, *eng, http.MethodPost,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String()+"/ack",
		sessionB.communication.Token, tenant, nil, map[string]string{
			"If-Match": sessionAckETag, "Idempotency-Key": sessionAckKey,
		})
	if ack.status != http.StatusOK {
		t.Fatalf("session B Ack = %d: %s", ack.status, ack.raw)
	}
	sessionAck := communicationHTTPTestDecode[sessions.DirectNoticeDeliveryAckResult](t, ack)
	if sessionAck.AckID.IsZero() || sessionAck.DeliveryID != sent.DeliveryID || sessionAck.Replayed {
		t.Fatalf("session B Ack result = %+v", sessionAck)
	}

	replyBody := map[string]any{
		"channel_id": channelID,
		"recipient":  map[string]any{"kind": "session", "ref": sessionA.sid},
		"content": map[string]any{
			"subject": "Session reply",
			"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": "session-b-reply"}},
		},
		"urgency": "normal",
	}
	reply := communicationHTTPTestRequest(t, *eng, http.MethodPost,
		"/v1/m/sessions/messages/send", sessionB.communication.Token, tenant,
		replyBody, map[string]string{"Idempotency-Key": model.NewID().String()})
	if reply.status != http.StatusCreated {
		t.Fatalf("session B reply = %d: %s", reply.status, reply.raw)
	}
	replied := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, reply)
	readReply := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+replied.DeliveryID.String(),
		sessionA.communication.Token, tenant, nil, nil)
	if readReply.status != http.StatusOK {
		t.Fatalf("session A reply read = %d: %s", readReply.status, readReply.raw)
	}
	exerciseCommunicationHTTPClaimTakeoverRollback(
		t, *eng, owner, tenant, channelID, sessionTakeoverSource, sessionTakeover,
	)
	exerciseCommunicationHTTPConcurrentOwnerRollback(
		t, *eng, owner, tenant, channelID, sessionConcurrentSource, sessionB, sessionThird,
	)

	exerciseCommunicationHTTPSessionHandoff(
		t, eng, estate, owner, tenant, channelID, sessionA, sessionB,
		sessionStale.communication.Token, sessionRevoked.communication.Token, sendBody, secret,
		communicationHTTPCommittedRecovery{
			session: sessionB, cursor: cursor, cursorKey: cursorKey,
			cursorResult: advanced, cursorDeliveryID: sent.DeliveryID,
			ackETag: sessionAckETag, ackKey: sessionAckKey, ackResult: sessionAck,
			barrier: barrierRecovery,
		},
	)
}

func exerciseCommunicationHTTPConcurrentOwnerRollback(
	t *testing.T,
	eng *engine,
	owner communicationHTTPTestUser,
	tenant model.TenantID,
	channelID model.ID,
	source, target, concurrentOwner communicationHTTPTestSession,
) {
	t.Helper()
	work, leased := createCommunicationHTTPSessionOwnedWork(
		t, eng, owner, tenant, source, "Concurrent HTTP owner change rollback",
	)
	offerResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/handoffs", source.communication.Token, tenant, map[string]any{
			"channel_id": channelID, "work_item_id": work.ResultID,
			"recipient": map[string]any{"kind": "session", "ref": target.sid},
			"handoff": map[string]any{
				"summary": "Concurrent owner change", "next_action": "Deny the stale transfer",
			},
			"ack_deadline": time.Now().UTC().Add(5 * time.Minute), "expected_owner_epoch": 1,
		}, map[string]string{
			"If-Match": fmt.Sprintf(`"v%d"`, leased.Version), "Idempotency-Key": model.NewID().String(),
		})
	if offerResponse.status != http.StatusCreated {
		t.Fatalf("offer before concurrent HTTP owner change = %d: %s", offerResponse.status, offerResponse.raw)
	}
	offered := communicationHTTPTestDecode[sessions.HandoffOfferResult](t, offerResponse)
	currentWorkResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+work.ResultID.String(), owner.token, tenant, nil, nil)
	if currentWorkResponse.status != http.StatusOK {
		t.Fatalf("read WorkItem after Handoff offer = %d: %s",
			currentWorkResponse.status, currentWorkResponse.raw)
	}
	currentWork := communicationHTTPTestDecode[sessions.WorkSnapshot](t, currentWorkResponse)
	assignment := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+work.ResultID.String()+"/assignments?mode=apply",
		owner.token, tenant, map[string]any{
			"owner_kind": "session", "owner_ref": concurrentOwner.sid,
		}, map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, currentWork.Item.Version),
			"Idempotency-Key": model.NewID().String(),
		})
	if assignment.status != http.StatusOK {
		t.Fatalf("concurrent WorkItem assignment over HTTP = %d: %s", assignment.status, assignment.raw)
	}
	assigned := communicationHTTPTestDecode[sessions.CommandResult](t, assignment)
	if assigned.Version <= leased.Version {
		t.Fatalf("concurrent assignment did not advance WorkItem: %+v", assigned)
	}
	before := communicationHTTPTestEffects(t, eng, tenant)
	response := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offered.HandoffID.String()+"/responses",
		target.communication.Token, tenant, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": offered.ETag, "Idempotency-Key": model.NewID().String()})
	if response.status != http.StatusConflict {
		t.Fatalf("Handoff response after concurrent owner change = %d: %s", response.status, response.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, before,
		"Handoff response after concurrent owner change")
	workResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+work.ResultID.String(), owner.token, tenant, nil, nil)
	if workResponse.status != http.StatusOK {
		t.Fatalf("read concurrently assigned WorkItem = %d: %s", workResponse.status, workResponse.raw)
	}
	snapshot := communicationHTTPTestDecode[sessions.WorkSnapshot](t, workResponse)
	if snapshot.Item.OwnerKind != "session" || snapshot.Item.OwnerRef != concurrentOwner.sid ||
		snapshot.Item.OwnerEpoch != 2 {
		t.Fatalf("failed Handoff changed concurrent owner/epoch = %+v", snapshot.Item)
	}
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		handoffs, err := sc.Ext("sessions.handoff")
		if err != nil {
			return err
		}
		handoff, err := handoffs.Get(context.Background(), offered.HandoffID)
		if err != nil {
			return err
		}
		if handoff.String("state") != string(sessions.HandoffOffered) || handoff.String("ack_id") != "" {
			return fmt.Errorf("concurrent-owner Handoff = %v", handoff)
		}
		deliveries, err := sc.Ext("sessions.message_delivery")
		if err != nil {
			return err
		}
		delivery, err := deliveries.Get(context.Background(), offered.DeliveryID)
		if err != nil {
			return err
		}
		if delivery.String("state") != string(sessions.DeliveryAvailable) || delivery.String("ack_id") != "" {
			return fmt.Errorf("concurrent-owner Delivery = %v", delivery)
		}
		return nil
	}); err != nil {
		t.Fatalf("concurrent-owner HTTP rollback evidence: %v", err)
	}
}

func createCommunicationHTTPSessionOwnedWork(
	t *testing.T,
	eng *engine,
	owner communicationHTTPTestUser,
	tenant model.TenantID,
	session communicationHTTPTestSession,
	title string,
) (sessions.CommandResult, sessions.CommandResult) {
	t.Helper()
	workResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/work-items?mode=apply", owner.token, tenant,
		map[string]any{
			"workspace_id": session.communication.WorkspaceID, "work_kind": "implementation",
			"title":        title,
			"brief_md":     "Created over the real Work API for a K3 session handoff test.",
			"context_refs": []any{}, "priority": "p1", "owner_kind": "session",
			"owner_ref": session.sid, "provenance_kind": "human",
			"provenance_ref": "test:k3-http", "acceptance": []map[string]any{{
				"criterion_key": "handoff", "ordinal": 0,
				"statement": "Session ownership transfers atomically", "required": true,
			}},
		}, map[string]string{"Idempotency-Key": model.NewID().String()})
	if workResponse.status != http.StatusOK {
		t.Fatalf("create session WorkItem over HTTP = %d: %s", workResponse.status, workResponse.raw)
	}
	work := communicationHTTPTestDecode[sessions.CommandResult](t, workResponse)
	readyResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+work.ResultID.String()+"/transitions?mode=apply",
		owner.token, tenant, map[string]any{"command": "item.ready"}, map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, work.Version),
			"Idempotency-Key": model.NewID().String(),
		})
	if readyResponse.status != http.StatusOK {
		t.Fatalf("ready session WorkItem = %d: %s", readyResponse.status, readyResponse.raw)
	}
	ready := communicationHTTPTestDecode[sessions.CommandResult](t, readyResponse)
	leaseResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+work.ResultID.String()+"/lease/acquire?mode=apply",
		session.work.Token, tenant, map[string]any{
			"holder_sid": session.sid, "holder_run_ref": session.runRef, "ttl_seconds": 300,
		}, map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, ready.Version),
			"Idempotency-Key": model.NewID().String(),
		})
	if leaseResponse.status != http.StatusOK {
		t.Fatalf("acquire session WorkLease = %d: %s", leaseResponse.status, leaseResponse.raw)
	}
	leased := communicationHTTPTestDecode[sessions.CommandResult](t, leaseResponse)
	if leased.LeaseFence < 1 {
		t.Fatalf("session WorkLease lacks fence: %+v", leased)
	}
	return work, leased
}

func exerciseCommunicationHTTPClaimTakeoverRollback(
	t *testing.T,
	eng *engine,
	owner communicationHTTPTestUser,
	tenant model.TenantID,
	channelID model.ID,
	source communicationHTTPTestSession,
	target communicationHTTPTestSession,
) {
	t.Helper()
	work, leased := createCommunicationHTTPSessionOwnedWork(
		t, eng, owner, tenant, source, "Session Claim takeover rollback",
	)
	offerResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/handoffs", source.communication.Token, tenant, map[string]any{
			"channel_id": channelID, "work_item_id": work.ResultID,
			"recipient": map[string]any{"kind": "session", "ref": target.sid},
			"handoff": map[string]any{
				"summary": "Claim takeover rollback", "next_action": "Reject the stale claimant",
			},
			"ack_deadline": time.Now().UTC().Add(5 * time.Minute), "expected_owner_epoch": 1,
		}, map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, leased.Version),
			"Idempotency-Key": model.NewID().String(),
		})
	if offerResponse.status != http.StatusCreated {
		t.Fatalf("offer Claim-takeover Handoff = %d: %s", offerResponse.status, offerResponse.raw)
	}
	offered := communicationHTTPTestDecode[sessions.HandoffOfferResult](t, offerResponse)
	if err := eng.sessionsMod.Release(
		context.Background(), tenant, target.sid, target.agentRef, target.claim.Fence,
	); err != nil {
		t.Fatalf("release target Claim before stale Handoff response: %v", err)
	}
	reclaimed, err := eng.sessionsMod.Claim(
		context.Background(), tenant, target.sid, target.agentRef, time.Hour,
	)
	if err != nil || reclaimed.Fence <= target.claim.Fence {
		t.Fatalf("take over target Claim before Handoff response = %+v, err %v", reclaimed, err)
	}
	before := communicationHTTPTestEffects(t, eng, tenant)
	staleResponse := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offered.HandoffID.String()+"/responses",
		target.communication.Token, tenant, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": offered.ETag, "Idempotency-Key": model.NewID().String()})
	if staleResponse.status != http.StatusNotFound {
		t.Fatalf("stale-Claim Handoff response = %d: %s", staleResponse.status, staleResponse.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "stale-Claim Handoff response")
	workResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+work.ResultID.String(), owner.token, tenant, nil, nil)
	if workResponse.status != http.StatusOK {
		t.Fatalf("read WorkItem after stale-Claim response = %d: %s", workResponse.status, workResponse.raw)
	}
	workSnapshot := communicationHTTPTestDecode[sessions.WorkSnapshot](t, workResponse)
	if workSnapshot.Item.OwnerKind != "session" || workSnapshot.Item.OwnerRef != source.sid ||
		workSnapshot.Item.OwnerEpoch != 1 || !workSnapshot.Item.Leased {
		t.Fatalf("stale-Claim response changed WorkItem = %+v", workSnapshot.Item)
	}
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		handoffs, err := sc.Ext("sessions.handoff")
		if err != nil {
			return err
		}
		handoffRow, err := handoffs.Get(context.Background(), offered.HandoffID)
		if err != nil {
			return err
		}
		if handoffRow.String("state") != string(sessions.HandoffOffered) ||
			handoffRow.String("ack_id") != "" {
			return fmt.Errorf("stale-Claim Handoff row = %v", handoffRow)
		}
		deliveries, err := sc.Ext("sessions.message_delivery")
		if err != nil {
			return err
		}
		deliveryRow, err := deliveries.Get(context.Background(), offered.DeliveryID)
		if err != nil {
			return err
		}
		if deliveryRow.String("state") != string(sessions.DeliveryAvailable) ||
			deliveryRow.String("ack_id") != "" {
			return fmt.Errorf("stale-Claim Handoff Delivery = %v", deliveryRow)
		}
		leases, err := sc.Ext("sessions.work_lease")
		if err != nil {
			return err
		}
		rows, _, err := leases.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: "work_item_id", Op: model.OpEq, Value: work.ResultID.String(),
		}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].String("state") != "active" ||
			rows[0].Int("fence") != leased.LeaseFence {
			return fmt.Errorf("stale-Claim Handoff lease = %v", rows)
		}
		return nil
	}); err != nil {
		t.Fatalf("stale-Claim Handoff rollback evidence: %v", err)
	}
}

type communicationHTTPCommittedRecovery struct {
	session          communicationHTTPTestSession
	cursor           sessions.DirectNoticeCursorTokenResult
	cursorKey        string
	cursorResult     sessions.DirectNoticeCursorAdvanceResult
	cursorDeliveryID model.ID
	ackETag          string
	ackKey           string
	ackResult        sessions.DirectNoticeDeliveryAckResult
	barrier          communicationHTTPBarrierRecovery
}

func exerciseCommunicationHTTPCommittedRecoveryAfterRestart(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	recovery communicationHTTPCommittedRecovery,
) {
	t.Helper()
	cursorReplay := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+recovery.session.sid,
		recovery.session.communication.Token, tenant, map[string]any{
			"cursor": recovery.cursor.CursorToken, "delivery_id": recovery.cursorDeliveryID,
		}, map[string]string{
			"If-Match": recovery.cursor.ETag, "Idempotency-Key": recovery.cursorKey,
		})
	replayedCursor := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, cursorReplay)
	if cursorReplay.status != http.StatusOK || !replayedCursor.Replayed ||
		replayedCursor.CommandID != recovery.cursorResult.CommandID ||
		replayedCursor.CursorID != recovery.cursorResult.CursorID ||
		replayedCursor.Version != recovery.cursorResult.Version ||
		replayedCursor.Projection != recovery.cursorResult.Projection ||
		replayedCursor.AuditSeq != recovery.cursorResult.AuditSeq {
		t.Fatalf("cursor receipt re-read after restart = %d: %s; committed=%+v",
			cursorReplay.status, cursorReplay.raw, recovery.cursorResult)
	}

	ackReplay := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/sessions/deliveries/"+recovery.ackResult.DeliveryID.String()+"/ack",
		recovery.session.communication.Token, tenant, nil, map[string]string{
			"If-Match": recovery.ackETag, "Idempotency-Key": recovery.ackKey,
		})
	replayedAck := communicationHTTPTestDecode[sessions.DirectNoticeDeliveryAckResult](t, ackReplay)
	if ackReplay.status != http.StatusOK || !replayedAck.Replayed ||
		replayedAck.CommandID != recovery.ackResult.CommandID ||
		replayedAck.AckID != recovery.ackResult.AckID ||
		replayedAck.DeliveryID != recovery.ackResult.DeliveryID ||
		replayedAck.EventID != recovery.ackResult.EventID ||
		replayedAck.AuditSeq != recovery.ackResult.AuditSeq {
		t.Fatalf("Ack receipt re-read after restart = %d: %s; committed=%+v",
			ackReplay.status, ackReplay.raw, recovery.ackResult)
	}
	acknowledgedRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+recovery.ackResult.DeliveryID.String(),
		recovery.session.communication.Token, tenant, nil, nil)
	if acknowledgedRead.status != http.StatusOK {
		t.Fatalf("acknowledged Delivery read after restart = %d: %s",
			acknowledgedRead.status, acknowledgedRead.raw)
	}
	acknowledged := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, acknowledgedRead)
	if acknowledged.Delivery.State != sessions.DeliveryAcknowledged ||
		acknowledged.Fulfillment.Acknowledged < 1 {
		t.Fatalf("Ack state after restart = %+v", acknowledged)
	}

	barrier := recovery.barrier
	barrierReplay := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+barrier.target.id.String(),
		barrier.target.token, tenant, map[string]any{
			"cursor": barrier.cursor.CursorToken, "delivery_id": barrier.targetDeliveryID,
		}, map[string]string{
			"If-Match": barrier.cursor.ETag, "Idempotency-Key": barrier.key,
		})
	replayedBarrier := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, barrierReplay)
	if barrierReplay.status != http.StatusOK || !replayedBarrier.Replayed ||
		replayedBarrier.CommandID != barrier.result.CommandID ||
		replayedBarrier.CursorID != barrier.result.CursorID ||
		replayedBarrier.Version != barrier.result.Version ||
		replayedBarrier.Projection != barrier.result.Projection ||
		replayedBarrier.AuditSeq != barrier.result.AuditSeq ||
		replayedBarrier.Projection.BarrierDeliveryID != barrier.barrierDeliveryID {
		t.Fatalf("barrier receipt re-read after restart = %d: %s; committed=%+v",
			barrierReplay.status, barrierReplay.raw, barrier.result)
	}
	if active := communicationHTTPTestActiveBarrierDeliveries(t, eng, tenant,
		sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: barrier.target.id.String()}); !communicationHTTPTestContainsID(active, barrier.barrierDeliveryID) {
		t.Fatalf("restart lost active cursor barrier: %v", active)
	}
	barrierRead := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+barrier.barrierDeliveryID.String(),
		barrier.target.token, tenant, nil, nil)
	if barrierRead.status != http.StatusOK {
		t.Fatalf("regranted barrier Delivery after restart = %d: %s",
			barrierRead.status, barrierRead.raw)
	}
	barrierOpened := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, barrierRead)
	if barrierOpened.Delivery.ID != barrier.barrierDeliveryID ||
		len(barrierOpened.Message.Content.Blocks) == 0 {
		t.Fatalf("regranted barrier Delivery content after restart = %+v", barrierOpened)
	}
	barrierInboxResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestInboxPath(barrier.workspace, 200, ""),
		barrier.target.token, tenant, nil, nil)
	if barrierInboxResponse.status != http.StatusOK {
		t.Fatalf("barrier inbox after restart = %d: %s",
			barrierInboxResponse.status, barrierInboxResponse.raw)
	}
	barrierInbox := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, barrierInboxResponse)
	if len(barrierInbox.Items) == 0 || barrierInbox.CursorTarget == "" {
		t.Fatalf("barrier inbox target after restart = %+v", barrierInbox)
	}
	targetDelivery := barrierInbox.Items[len(barrierInbox.Items)-1].Delivery
	barrierCursorResponse := communicationHTTPTestRequest(t, eng, http.MethodGet,
		communicationHTTPTestCursorPath(
			barrier.target.id.String(), barrier.workspace, barrierInbox.CursorTarget,
		), barrier.target.token, tenant, nil, nil)
	if barrierCursorResponse.status != http.StatusOK {
		t.Fatalf("barrier cursor mint after restart = %d: %s",
			barrierCursorResponse.status, barrierCursorResponse.raw)
	}
	barrierCursor := communicationHTTPTestDecode[sessions.DirectNoticeCursorTokenResult](t, barrierCursorResponse)
	resolvedResponse := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/m/sessions/inbox/cursors/personal/"+barrier.target.id.String(),
		barrier.target.token, tenant, map[string]any{
			"cursor": barrierCursor.CursorToken, "delivery_id": targetDelivery.ID,
		}, map[string]string{
			"If-Match": barrierCursor.ETag, "Idempotency-Key": model.NewID().String(),
		})
	if resolvedResponse.status != http.StatusOK {
		t.Fatalf("explicit barrier resolution after restart = %d: %s",
			resolvedResponse.status, resolvedResponse.raw)
	}
	resolved := communicationHTTPTestDecode[sessions.DirectNoticeCursorAdvanceResult](t, resolvedResponse)
	if !resolved.Projection.BarrierDeliveryID.IsZero() ||
		resolved.Projection.LastSeenSeq < barrier.barrierDeliverySeq {
		t.Fatalf("barrier resolution after restart = %+v", resolved)
	}
	if active := communicationHTTPTestActiveBarrierDeliveries(t, eng, tenant,
		sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: barrier.target.id.String()}); len(active) != 0 {
		t.Fatalf("explicit post-restart PUT retained barriers: %v", active)
	}
}

func exerciseCommunicationHTTPSessionHandoff(
	t *testing.T,
	eng **engine,
	estate communicationHTTPTestStore,
	owner communicationHTTPTestUser,
	tenant model.TenantID,
	channelID model.ID,
	sessionA communicationHTTPTestSession,
	sessionB communicationHTTPTestSession,
	staleToken string,
	revokedToken string,
	sessionSendBody map[string]any,
	secret string,
	recovery communicationHTTPCommittedRecovery,
) {
	t.Helper()
	work, leased := createCommunicationHTTPSessionOwnedWork(
		t, *eng, owner, tenant, sessionA, "Session HTTP handoff",
	)

	handoffBody := map[string]any{
		"channel_id": channelID, "work_item_id": work.ResultID,
		"recipient": map[string]any{"kind": "session", "ref": sessionB.sid},
		"handoff": map[string]any{
			"summary": "Transfer session-owned work", "next_action": "Accept and continue",
		},
		"ack_deadline": time.Now().UTC().Add(5 * time.Minute), "expected_owner_epoch": 1,
	}
	handoffResponse := communicationHTTPTestRequest(t, *eng, http.MethodPost,
		"/v1/m/sessions/handoffs", sessionA.communication.Token, tenant, handoffBody,
		map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, leased.Version),
			"Idempotency-Key": model.NewID().String(),
		})
	if handoffResponse.status != http.StatusCreated {
		t.Fatalf("session Handoff offer = %d: %s", handoffResponse.status, handoffResponse.raw)
	}
	offered := communicationHTTPTestDecode[sessions.HandoffOfferResult](t, handoffResponse)
	if offered.State != sessions.HandoffOffered || offered.WorkItemID != work.ResultID ||
		offered.MessageID.IsZero() || offered.DeliveryID.IsZero() {
		t.Fatalf("session Handoff offer = %+v", offered)
	}
	workAfterOfferResponse := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+work.ResultID.String(), owner.token, tenant, nil, nil)
	if workAfterOfferResponse.status != http.StatusOK {
		t.Fatalf("read session WorkItem after offer = %d: %s",
			workAfterOfferResponse.status, workAfterOfferResponse.raw)
	}
	workAfterOffer := communicationHTTPTestDecode[sessions.WorkSnapshot](t, workAfterOfferResponse)
	if workAfterOffer.Item.OwnerKind != "session" || workAfterOffer.Item.OwnerRef != sessionA.sid ||
		workAfterOffer.Item.OwnerEpoch != 1 || !workAfterOffer.Item.Leased {
		t.Fatalf("Handoff offer changed WorkItem owner/lease = %+v", workAfterOffer.Item)
	}
	if err := (*eng).store.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.work_lease")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: "work_item_id", Op: model.OpEq, Value: work.ResultID.String(),
		}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].String("state") != "active" ||
			rows[0].Int("fence") != leased.LeaseFence {
			return fmt.Errorf("offered session Handoff lease = %v", rows)
		}
		return nil
	}); err != nil {
		t.Fatalf("Handoff offer lease evidence: %v", err)
	}
	acceptKey := model.NewID().String()
	acceptedResponse := communicationHTTPTestRequest(t, *eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offered.HandoffID.String()+"/responses",
		sessionB.communication.Token, tenant, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": offered.ETag, "Idempotency-Key": acceptKey})
	if acceptedResponse.status != http.StatusOK {
		t.Fatalf("session Handoff accept = %d: %s", acceptedResponse.status, acceptedResponse.raw)
	}
	accepted := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, acceptedResponse)
	if accepted.State != sessions.HandoffAccepted || accepted.AckID.IsZero() ||
		accepted.OwnerEpoch != 2 || accepted.ResultingLeaseFence < 2 {
		t.Fatalf("session Handoff accept = %+v", accepted)
	}
	pendingBeforeRestart := communicationHTTPTestOutboxStates(t, *eng, tenant)
	if pendingBeforeRestart["pending"] == 0 {
		t.Fatalf("restart fixture has no committed pending K3 outbox work: %+v", pendingBeforeRestart)
	}

	if err := (*eng).Close(); err != nil {
		t.Fatalf("close accepted session estate: %v", err)
	}
	*eng = bootCommunicationHTTPTestEngine(t, estate)
	if (*eng).communicationPump == nil {
		t.Fatal("restarted engine has no composed communication outbox pump")
	}
	for _, rejected := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "stale Claim after restart", token: staleToken, status: http.StatusServiceUnavailable},
		{name: "revoked token after restart", token: revokedToken, status: http.StatusUnauthorized},
	} {
		before := communicationHTTPTestEffects(t, *eng, tenant)
		response := communicationHTTPTestRequest(t, *eng, http.MethodPost,
			"/v1/m/sessions/messages/send", rejected.token, tenant, sessionSendBody,
			map[string]string{"Idempotency-Key": model.NewID().String()})
		if response.status != rejected.status || strings.Contains(string(response.raw), secret) {
			t.Fatalf("%s = %d: %s", rejected.name, response.status, response.raw)
		}
		assertCommunicationHTTPTestNoEffects(t, *eng, tenant, before, rejected.name)
	}
	exerciseCommunicationHTTPCommittedRecoveryAfterRestart(t, *eng, tenant, recovery)
	pumpCtx, cancelPump := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := (*eng).communicationPump.runOnce(pumpCtx); err != nil {
		cancelPump()
		t.Fatalf("real communication outbox recovery pump: %v", err)
	}
	cancelPump()
	afterPump := communicationHTTPTestOutboxStates(t, *eng, tenant)
	if afterPump["pending"] != 0 || afterPump["delivering"] != 0 ||
		afterPump["published"] < pendingBeforeRestart["pending"] {
		t.Fatalf("communication outbox did not recover pending work: before=%+v after=%+v",
			pendingBeforeRestart, afterPump)
	}
	pumpCtx, cancelPump = context.WithTimeout(context.Background(), 2*time.Minute)
	if err := (*eng).communicationPump.runOnce(pumpCtx); err != nil {
		cancelPump()
		t.Fatalf("communication outbox deduplication pump: %v", err)
	}
	cancelPump()
	if repeated := communicationHTTPTestOutboxStates(t, *eng, tenant); !maps.Equal(repeated, afterPump) {
		t.Fatalf("second communication pump changed settled outbox: first=%+v second=%+v",
			afterPump, repeated)
	}
	replayedAccept := communicationHTTPTestRequest(t, *eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offered.HandoffID.String()+"/responses",
		sessionB.communication.Token, tenant, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": offered.ETag, "Idempotency-Key": acceptKey})
	replayedHandoff := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, replayedAccept)
	if replayedAccept.status != http.StatusOK || !replayedHandoff.Replayed ||
		replayedHandoff.CommandID != accepted.CommandID || replayedHandoff.HandoffID != accepted.HandoffID ||
		replayedHandoff.WorkItemID != accepted.WorkItemID || replayedHandoff.EventID != accepted.EventID ||
		replayedHandoff.OwnerEpoch != accepted.OwnerEpoch ||
		replayedHandoff.ResultingLeaseFence != accepted.ResultingLeaseFence ||
		replayedHandoff.AuditSeq != accepted.AuditSeq {
		t.Fatalf("session Handoff restart replay = %d: %s; first=%s",
			replayedAccept.status, replayedAccept.raw, acceptedResponse.raw)
	}
	workRead := communicationHTTPTestRequest(t, *eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+work.ResultID.String(), owner.token, tenant, nil, nil)
	if workRead.status != http.StatusOK {
		t.Fatalf("read transferred session WorkItem = %d: %s", workRead.status, workRead.raw)
	}
	workSnapshot := communicationHTTPTestDecode[sessions.WorkSnapshot](t, workRead)
	if workSnapshot.Item.OwnerKind != "session" || workSnapshot.Item.OwnerRef != sessionB.sid ||
		workSnapshot.Item.OwnerEpoch != 2 || workSnapshot.Item.Leased ||
		workSnapshot.Item.Lease != nil {
		t.Fatalf("transferred session WorkItem = %+v", workSnapshot.Item)
	}
	if err := (*eng).store.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.work_lease")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: "work_item_id", Op: model.OpEq, Value: work.ResultID.String(),
		}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].String("state") != "revoked" ||
			rows[0].Int("fence") != accepted.ResultingLeaseFence {
			return fmt.Errorf("durable transferred lease = %v", rows)
		}
		return nil
	}); err != nil {
		t.Fatalf("transferred session lease evidence: %v", err)
	}
}
