// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/webpush"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

// fakePush is the push application server of a test: it records every message and
// answers each endpoint with the error the test set for it.
type fakePush struct {
	mu   sync.Mutex
	sent []pushSent
	errs map[string]error
}

type pushSent struct {
	sub     webpush.Subscription
	payload pushPayload
	opts    webpush.Options
}

func (f *fakePush) PublicKey() string { return "BFakeApplicationServerKey" }

func (f *fakePush) Send(_ context.Context, sub webpush.Subscription, payload []byte, opts webpush.Options) error {
	var p pushPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, pushSent{sub, p, opts})
	return f.errs[sub.Endpoint]
}

func (f *fakePush) all() []pushSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushSent(nil), f.sent...)
}

func pushEndpoint(name string) string { return "https://fcm.googleapis.com/fcm/send/" + name }

// deviceID is the id the console computes from a PushSubscription: the hex SHA-256 of its
// endpoint. Written here apart from the engine's own, so the two agree on the contract.
func deviceID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

// pushBody is what PushSubscription.toJSON() gives a browser: valid keys for a real P-256 point.
func pushBody(endpoint string) map[string]any {
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return map[string]any{"endpoint": endpoint, "keys": map[string]any{
		"p256dh": base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString(secret),
	}}
}

func countPushRows(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	var n int
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(pushSubscriptionKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 1000})
		n = len(recs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPushRoutesAreTheCallersOwn(t *testing.T) {
	fp := &fakePush{}
	m := New(WithPushSender(fp))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "push-own")
	alice := h.viewerToken(admin, tenant, "a@push.io")
	bob := h.viewerToken(admin, tenant, "b@push.io")
	hdr := tenantHdr(tenant)

	if r := h.do("GET", "/v1/m/sessions/push/key", alice, hdr); r.code != http.StatusOK || r.body["public_key"] != fp.PublicKey() {
		t.Fatalf("key = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/sessions/push/key", "", hdr); r.code != http.StatusUnauthorized {
		t.Fatalf("an anonymous key read = %d", r.code)
	}

	ep := pushEndpoint("alice-phone")
	for i := 0; i < 2; i++ { // the second is the same device registering again
		r := h.doJSON("POST", "/v1/m/sessions/push/subscriptions", alice, pushBody(ep), hdr)
		if r.code != http.StatusOK || r.body["id"] != deviceID(ep) {
			t.Fatalf("subscribe #%d = %d %s", i, r.code, r.raw)
		}
	}
	if n := countPushRows(t, h.st, tenant); n != 1 {
		t.Fatalf("one device registered twice made %d rows", n)
	}

	// Bob registering the same endpoint is his own row; his delete cannot touch Alice's.
	if r := h.doJSON("POST", "/v1/m/sessions/push/subscriptions", bob, pushBody(ep), hdr); r.code != http.StatusOK {
		t.Fatalf("bob subscribe = %d %s", r.code, r.raw)
	}
	if n := countPushRows(t, h.st, tenant); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	if r := h.do("DELETE", "/v1/m/sessions/push/subscriptions/"+deviceID(ep), bob, hdr); r.code != http.StatusNoContent {
		t.Fatalf("bob delete = %d %s", r.code, r.raw)
	}
	if n := countPushRows(t, h.st, tenant); n != 1 {
		t.Fatalf("bob's delete left %d rows, want only Alice's", n)
	}
	// Idempotent: a second delete, and one for a device never registered.
	for _, id := range []string{deviceID(ep), deviceID("https://nobody.example/x")} {
		if r := h.do("DELETE", "/v1/m/sessions/push/subscriptions/"+id, bob, hdr); r.code != http.StatusNoContent {
			t.Fatalf("repeat delete = %d %s", r.code, r.raw)
		}
	}
	if r := h.do("DELETE", "/v1/m/sessions/push/subscriptions/"+deviceID(ep), alice, hdr); r.code != http.StatusNoContent || countPushRows(t, h.st, tenant) != 0 {
		t.Fatalf("alice delete = %d, rows left %d", r.code, countPushRows(t, h.st, tenant))
	}
}

func TestPushSubscribeRefusesWhatSendWouldRefuse(t *testing.T) {
	m := New(WithPushSender(&fakePush{}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "push-bad")
	alice := h.viewerToken(admin, tenant, "a@bad.io")
	hdr := tenantHdr(tenant)

	good := pushBody(pushEndpoint("ok"))
	keys := good["keys"].(map[string]any)
	withEndpoint := func(e string) map[string]any { return map[string]any{"endpoint": e, "keys": keys} }
	withKeys := func(k map[string]any) map[string]any { return map[string]any{"endpoint": good["endpoint"], "keys": k} }
	for name, body := range map[string]map[string]any{
		"http endpoint":     withEndpoint("http://fcm.googleapis.com/fcm/send/x"),
		"loopback":          withEndpoint("https://127.0.0.1/x"),
		"metadata address":  withEndpoint("https://169.254.169.254/latest"),
		"credentials":       withEndpoint("https://u:p@fcm.googleapis.com/x"),
		"odd port":          withEndpoint("https://fcm.googleapis.com:8443/x"),
		"no endpoint":       withEndpoint(""),
		"short key":         withKeys(map[string]any{"p256dh": "AAAA", "auth": keys["auth"]}),
		"no auth":           withKeys(map[string]any{"p256dh": keys["p256dh"]}),
		"unknown field":     {"endpoint": good["endpoint"], "keys": keys, "extra": 1},
		"unknown key field": withKeys(map[string]any{"p256dh": keys["p256dh"], "auth": keys["auth"], "x": "y"}),
	} {
		if r := h.doJSON("POST", "/v1/m/sessions/push/subscriptions", alice, body, hdr); r.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, r.code, r.raw)
		}
	}
	if n := countPushRows(t, h.st, tenant); n != 0 {
		t.Fatalf("refused requests stored %d rows", n)
	}
}

func TestPushRoutesAnswer503WithoutASender(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "push-none")
	alice := h.viewerToken(admin, tenant, "a@none.io")
	hdr := tenantHdr(tenant)
	if r := h.do("GET", "/v1/m/sessions/push/key", alice, hdr); r.code != http.StatusServiceUnavailable {
		t.Fatalf("key = %d %s", r.code, r.raw)
	}
	if r := h.doJSON("POST", "/v1/m/sessions/push/subscriptions", alice, pushBody(pushEndpoint("x")), hdr); r.code != http.StatusServiceUnavailable {
		t.Fatalf("subscribe = %d %s", r.code, r.raw)
	}
}

// The ceiling is per person, and the oldest device leaves: a person who registers an
// eleventh device means to replace one.
func TestPushSubscriptionsAreCappedPerPerson(t *testing.T) {
	m := New(WithPushSender(&fakePush{}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "push-cap")
	alice := h.viewerToken(admin, tenant, "a@cap.io")
	bob := h.viewerToken(admin, tenant, "b@cap.io")
	hdr := tenantHdr(tenant)
	if r := h.doJSON("POST", "/v1/m/sessions/push/subscriptions", bob, pushBody(pushEndpoint("bob")), hdr); r.code != http.StatusOK {
		t.Fatal(r.raw)
	}
	for i := 0; i <= maxPushSubscriptions; i++ {
		if r := h.doJSON("POST", "/v1/m/sessions/push/subscriptions", alice, pushBody(pushEndpoint(fmt.Sprintf("d%d", i))), hdr); r.code != http.StatusOK {
			t.Fatalf("device %d = %d %s", i, r.code, r.raw)
		}
	}
	if n := countPushRows(t, h.st, tenant); n != maxPushSubscriptions+1 { // Alice at her cap, plus Bob's one
		t.Fatalf("rows = %d, want %d", n, maxPushSubscriptions+1)
	}
	// The device that left is the first one registered, not the newest.
	stored := map[string]bool{}
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext(pushSubscriptionKind)
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		for _, r := range recs {
			stored[r.String(colPushEndpoint)] = true
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if stored[pushEndpoint("d0")] || !stored[pushEndpoint("d1")] || !stored[pushEndpoint(fmt.Sprintf("d%d", maxPushSubscriptions))] || !stored[pushEndpoint("bob")] {
		t.Fatalf("after the eleventh device the stored endpoints are %v: d0 should be gone, the rest kept", stored)
	}
}

// seedPush registers a subscription row for one user, the way the route does.
func seedPush(t *testing.T, st store.Store, tenant model.TenantID, user, endpoint string) {
	t.Helper()
	keys := pushBody(endpoint)["keys"].(map[string]any)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(pushSubscriptionKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colPushOwner: user, colPushEndpointSHA: deviceID(endpoint), colPushEndpoint: endpoint,
			colPushP256dh: keys["p256dh"], colPushAuth: keys["auth"],
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// launcherOf reads who the run says launched it.
func launcherOf(t *testing.T, m *Module, tenant model.TenantID, run string) string {
	t.Helper()
	rec, err := m.loadRun(context.Background(), tenant, run)
	if err != nil {
		t.Fatal(err)
	}
	return rec.String(colRunLauncherUserID)
}

func waitPushes(t *testing.T, fp *fakePush, n int) []pushSent {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d push messages", n), func() bool { return len(fp.all()) >= n })
	return fp.all()
}

func TestAnEndedSessionPushesToItsLauncherOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
		kind string
	}{
		{"a process that died", 1, "failed"},
		{"a clean exit", 0, "finished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{initSID: dupID}
			h, admin, tenant, profile, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
			fp := &fakePush{}
			h.m.rt.PushSender = fp
			run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
			launcher := launcherOf(t, h.m, tenant, run)
			if launcher == "" {
				t.Fatal("a run launched over the API records no launcher: nothing can be addressed")
			}
			seedPush(t, h.st, tenant, launcher, pushEndpoint("launcher-phone"))
			seedPush(t, h.st, tenant, model.NewID().String(), pushEndpoint("someone-else"))

			fr.lastProc().finish(tc.exit)
			waitPushes(t, fp, 1)
			time.Sleep(50 * time.Millisecond) // a second, wrong delivery would land by now
			sent := fp.all()
			if len(sent) != 1 || sent[0].sub.Endpoint != pushEndpoint("launcher-phone") {
				t.Fatalf("deliveries = %+v, want exactly one to the launcher's device", sent)
			}
			got := sent[0].payload
			wantURL := "/sessions?session=run%3A" + run + "&pane=narrative"
			if got.Kind != tc.kind || got.URL != wantURL || got.Tag != "run:"+run {
				t.Fatalf("payload = %+v, want kind %s url %s", got, tc.kind, wantURL)
			}
		})
	}
}

func TestAStopTheOperatorRequestedPushesNothing(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	h, admin, tenant, profile, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
	fp := &fakePush{}
	h.m.rt.PushSender = fp
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	seedPush(t, h.st, tenant, launcherOf(t, h.m, tenant, run), pushEndpoint("phone"))
	if r := h.do("POST", "/v1/m/sessions/runs/"+run+"/stop", admin, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	waitFor(t, "the stop to settle", func() bool {
		d, _ := h.m.getRun(context.Background(), tenant, run)
		return d.State == stateStopped
	})
	time.Sleep(50 * time.Millisecond)
	if sent := fp.all(); len(sent) != 0 {
		t.Fatalf("a stop the person asked for pushed %+v", sent)
	}
}

func TestPushChecksTheLaunchersCurrentEventChoice(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	h, _, tenant, _, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
	fp := &fakePush{}
	owner := model.NewID().String()
	seedPush(t, h.st, tenant, owner, pushEndpoint("phone"))
	h.m.rt.NotificationAllowed = func(_ context.Context, gotTenant model.TenantID, gotOwner, kind string) (bool, error) {
		if gotTenant != tenant || gotOwner != owner || kind != pushFinished {
			t.Errorf("wrong preference scope: %s %s %s", gotTenant, gotOwner, kind)
		}
		return false, nil
	}
	body, opts := pushMessageFor(pushFinished, model.Record{colRunRef: "run-one"})
	h.m.deliverPush(context.Background(), fp, tenant, owner, pushFinished, body, opts)
	if len(fp.all()) != 0 {
		t.Fatal("disabled finished event was pushed")
	}
}

// A runtime policy or a different operator ending a run is news to its launcher.
func TestAStopNotRequestedByTheLauncherPushes(t *testing.T) {
	for _, policy := range []bool{true, false} {
		name := "another operator"
		if policy {
			name = "duration policy"
		}
		t.Run(name, func(t *testing.T) {
			fr := &fakeRunner{initSID: dupID}
			h, admin, tenant, profile, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
			fp := &fakePush{}
			h.m.rt.PushSender = fp
			run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
			launcher := launcherOf(t, h.m, tenant, run)
			seedPush(t, h.st, tenant, launcher, pushEndpoint("launcher-phone"))
			seedPush(t, h.st, tenant, model.NewID().String(), pushEndpoint("other-phone"))
			if policy {
				lr, ok := h.m.rt.getLive(tenant, run)
				if !ok {
					t.Fatal("run is not supervised")
				}
				if !h.m.stopOwnedRun(lr, "max session duration reached") {
					t.Fatal("policy stop refused")
				}
			} else {
				other := h.userToken(admin, tenant, "stopper@push.io", "admin")
				if r := h.do("POST", "/v1/m/sessions/runs/"+run+"/stop", other, tenantHdr(tenant)); r.code != http.StatusOK {
					t.Fatalf("stop = %d %s", r.code, r.raw)
				}
			}
			waitFor(t, "stop settled", func() bool {
				d, _ := h.m.getRun(context.Background(), tenant, run)
				return d.State == stateStopped
			})
			sent := waitPushes(t, fp, 1)
			if len(sent) != 1 || sent[0].sub.Endpoint != pushEndpoint("launcher-phone") ||
				sent[0].payload.Kind != pushFinished || sent[0].payload.URL != "/sessions?session=run%3A"+run+"&pane=narrative" {
				t.Fatalf("deliveries = %+v, want one finished message to the launcher", sent)
			}
		})
	}
}

func TestADeadDeviceIsForgottenAndAFailingOneIsKept(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	h, admin, tenant, profile, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
	fp := &fakePush{errs: map[string]error{
		pushEndpoint("gone"):  fmt.Errorf("%w: answered 410", webpush.ErrGone),
		pushEndpoint("flaky"): errors.New("webpush: answered 503"),
	}}
	h.m.rt.PushSender = fp
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	launcher := launcherOf(t, h.m, tenant, run)
	for _, name := range []string{"gone", "flaky", "healthy"} {
		seedPush(t, h.st, tenant, launcher, pushEndpoint(name))
	}
	fr.lastProc().finish(0)
	waitPushes(t, fp, 3)
	waitFor(t, "the dead subscription to be dropped", func() bool { return countPushRows(t, h.st, tenant) == 2 })
	left := map[string]bool{}
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext(pushSubscriptionKind)
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 10})
		for _, r := range recs {
			left[r.String(colPushEndpoint)] = true
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if left[pushEndpoint("gone")] || !left[pushEndpoint("flaky")] || !left[pushEndpoint("healthy")] {
		t.Fatalf("subscriptions left = %v", left)
	}
}

func TestAToolRequestForApprovalPushesToTheLauncher(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	clk := &testClock{now: baseTime}
	h, admin, tenant, profile, _ := profiledHTTP(t, fr, clk)
	fp := &fakePush{}
	h.m.rt.PushSender = fp
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	seedPush(t, h.st, tenant, launcherOf(t, h.m, tenant, run), pushEndpoint("phone"))

	a := auth.NewAuthenticator(h.st, nil)
	human, err := a.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(ctx context.Context, scope auth.SessionScope) error {
		return h.m.Authority(ctx, scope.TenantID, scope.SessionRef, scope.Holder, scope.Fence)
	})
	lr, ok := h.m.rt.getLive(tenant, run)
	if !ok {
		t.Fatal("launched run is not supervised")
	}
	var workspace model.ID
	if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(t.Context())
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Mint(t.Context(), human, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: run, SessionRef: lr.claim.SID, RunRef: run, Holder: lr.claim.Holder, Fence: lr.claim.Fence})
	if err != nil {
		t.Fatal(err)
	}
	p, err := issuer.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}

	end, err := h.m.BeginApprovalWait(t.Context(), p, model.NewID().String(), clk.get().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	got := waitPushes(t, fp, 1)[0]
	if got.sub.Endpoint != pushEndpoint("phone") || got.payload.Kind != "approval" ||
		got.payload.URL != "/sessions?session=run%3A"+run+"&pane=narrative" {
		t.Fatalf("delivery = %+v", got)
	}
	if got.opts.Urgency != webpush.UrgencyHigh || got.opts.TTL <= 0 || got.opts.TTL > time.Hour {
		t.Fatalf("an approval is urgent and goes stale fast, got %+v", got.opts)
	}
	if !strings.HasPrefix(got.payload.URL, "/") || strings.HasPrefix(got.payload.URL, "//") {
		t.Fatalf("the link must stay on this origin: %q", got.payload.URL)
	}
}

// The published contract is closed: a console builds its request from it, and the id it
// gets back is the one the delete takes.
func TestPushRoutesArePublishedWithTheirShapes(t *testing.T) {
	t.Parallel()
	m := New()
	key := oastest.Op(t, m, "get", "/v1/m/sessions/push/key")
	keySchema := oastest.Map(t, oastest.Map(t, oastest.Map(t, oastest.Map(t, key["responses"], "responses")["200"], "200")["content"], "content")["application/json"], "JSON")
	if got := oastest.Strings(oastest.Map(t, keySchema["schema"], "schema")["required"]); len(got) != 1 || got[0] != "public_key" {
		t.Fatalf("key answer requires %v", got)
	}

	sub := oastest.Op(t, m, "post", "/v1/m/sessions/push/subscriptions")
	body := oastest.Map(t, oastest.Map(t, oastest.Map(t, sub["requestBody"], "request body")["content"], "content")["application/json"], "JSON body")
	schema := oastest.Map(t, body["schema"], "schema")
	keys := oastest.Map(t, oastest.Map(t, schema["properties"], "properties")["keys"], "keys")
	if schema["additionalProperties"] != false || keys["additionalProperties"] != false ||
		len(oastest.Strings(schema["required"])) != 2 || len(oastest.Strings(keys["required"])) != 2 {
		t.Fatalf("subscribe body = %#v", schema)
	}
	ok := oastest.Map(t, oastest.Map(t, sub["responses"], "responses")["200"], "200")
	idSchema := oastest.Map(t, oastest.Map(t, oastest.Map(t, oastest.Map(t, ok["content"], "content")["application/json"], "JSON")["schema"], "schema")["properties"], "properties")
	if oastest.Map(t, idSchema["id"], "id")["pattern"] != "^[0-9a-f]{64}$" {
		t.Fatalf("id schema = %#v", idSchema)
	}

	del := oastest.Op(t, m, "delete", "/v1/m/sessions/push/subscriptions/{id}")
	if _, has := del["requestBody"]; has {
		t.Fatalf("a delete by id carries no body, got %#v", del["requestBody"])
	}
	if oastest.Map(t, del["responses"], "responses")["204"] == nil {
		t.Fatalf("delete does not publish its 204: %#v", del["responses"])
	}
}
