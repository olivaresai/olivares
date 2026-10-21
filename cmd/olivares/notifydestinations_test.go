// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/sdk"
)

const (
	syntheticSlackURL  = "https://hooks.slack.com/services/T0/B0/SYNTHETICSECRET"
	syntheticSlackURL2 = "https://hooks.slack.com/services/T0/B0/ROTATEDSECRET"
	syntheticTeamsURL  = "https://prod-12.westus.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke"
)

func TestValidateNotifyWebhook(t *testing.T) {
	slack, teams := notifyConsoleKinds["slack"], notifyConsoleKinds["teams"]
	for _, c := range []struct {
		name string
		kind notifyConsoleKind
		url  string
		host string // empty = refused
	}{
		{"slack hook", slack, syntheticSlackURL, "hooks.slack.com"},
		{"slack gov", slack, "https://hooks.slack-gov.com/services/T0/B0/X", "hooks.slack-gov.com"},
		{"teams workflow with the https port", teams, syntheticTeamsURL, "prod-12.westus.logic.azure.com"},
		{"teams legacy connector", teams, "https://contoso.webhook.office.com/webhookb2/abc", "contoso.webhook.office.com"},
		{"teams power platform", teams, "https://x.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/abc", "x.environment.api.powerplatform.com"},
		{"plain http", slack, "http://hooks.slack.com/services/T0/B0/X", ""},
		{"another host", slack, "https://evil.example/services/T0/B0/X", ""},
		{"vendor name as a subdomain of another host", slack, "https://hooks.slack.com.evil.example/services/T0/B0/X", ""},
		{"vendor name as a prefix of the host", teams, "https://evil-logic.azure.com/workflows/abc", ""},
		{"loopback", slack, "https://127.0.0.1/services/T0/B0/X", ""},
		{"metadata address", slack, "https://169.254.169.254/latest/meta-data", ""},
		{"internal name", slack, "https://localhost/services/T0/B0/X", ""},
		{"credentials in the URL", slack, "https://user:pw@hooks.slack.com/services/T0/B0/X", ""},
		{"other port", slack, "https://hooks.slack.com:8443/services/T0/B0/X", ""},
		{"no path", slack, "https://hooks.slack.com", ""},
		{"teams workflow with its credential in the query", teams, syntheticTeamsURL + "?api-version=2016-06-01&sp=%2Ftriggers%2Fmanual%2Frun&sv=1.0&sig=SYNTHETICSIG", "prod-12.westus.logic.azure.com"},
		{"uppercase host", slack, "https://HOOKS.SLACK.COM/services/T0/B0/X", "hooks.slack.com"},
		{"vendor apex without a subdomain", teams, "https://logic.azure.com/workflows/abc", ""},
		{"trailing dot", slack, "https://hooks.slack.com./services/T0/B0/X", ""},
		{"a letter that case-folds to ASCII (U+0130)", teams, "https://x.webhook.off\u0130ce.com/p", ""},
		{"the Kelvin sign (U+212A)", slack, "https://hooks.slac\u212a.com/services/T0/B0/X", ""},
		{"whitespace in the path", slack, "https://hooks.slack.com/services/T0/B0/X Y", ""},
		{"fragment", slack, "https://hooks.slack.com/services/T0/B0/X#f", ""},
		{"a Slack hook for Teams", teams, syntheticSlackURL, ""},
		{"not a URL", slack, "store:other/secret", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			host, err := validateNotifyWebhook(c.kind, c.url)
			if c.host == "" {
				if !errors.Is(err, api.ErrNotifyDestinationInvalid) {
					t.Fatalf("validate(%q) = %q, %v; want ErrNotifyDestinationInvalid", c.url, host, err)
				}
				// The URL is the secret: the refusal never repeats any of it.
				if err != nil && strings.Contains(err.Error(), "services/T0") {
					t.Fatalf("the refusal repeats the URL: %v", err)
				}
				return
			}
			if err != nil || host != c.host {
				t.Fatalf("validate(%q) = %q, %v; want %q", c.url, host, err, c.host)
			}
		})
	}
}

// consoleDestinations boots a real engine and returns the service over its own
// settings record, secret store, resolver and notify dispatcher.
func consoleDestinations(t *testing.T) (*notifyDestinationService, *engine, *productSettings) {
	t.Helper()
	eng, settings := bootForSettings(t)
	if eng.notifyDispatcher == nil || eng.secretStore == nil || eng.secretResolver == nil {
		t.Fatalf("the engine did not wire notify (dispatcher=%v store=%v resolver=%v)", eng.notifyDispatcher != nil, eng.secretStore != nil, eng.secretResolver != nil)
	}
	return newNotifyDestinationService(settings, eng.secretStore, eng.secretResolver, eng.notifyDispatcher, quietLog()), eng, settings
}

func settingsJSON(t *testing.T, p *productSettings) string {
	t.Helper()
	doc, _, err := p.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestNotifyDestinationPutSealsTheURLStoresAReferenceAndOpensIt(t *testing.T) {
	svc, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)

	got, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !got.Live || got.Source != api.NotifyDestinationSourceConsole || got.WebhookHost != "hooks.slack.com" {
		t.Fatalf("put = %+v, want live console destination on hooks.slack.com", got)
	}
	// Open in the running engine, addressable by a route without a restart.
	if !strings.Contains(strings.Join(eng.notifyDispatcher.Destinations(), ","), "oncall") {
		t.Fatalf("the dispatcher does not list oncall: %v", eng.notifyDispatcher.Destinations())
	}
	// The sealed store holds the URL; the settings record holds only the reference.
	value, err := eng.secretStore.Resolve(ctx, auth.GlobalSecretScope, "notify/oncall/webhook_url")
	if err != nil || string(value) != syntheticSlackURL {
		t.Fatalf("sealed secret = %q, %v; want the typed URL", value, err)
	}
	record := settingsJSON(t, settings)
	if strings.Contains(record, "SYNTHETICSECRET") || !strings.Contains(record, "store:notify/oncall/webhook_url") {
		t.Fatalf("the settings record must hold the reference and no URL: %s", record)
	}
	// The list names the host, never the URL.
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listJSON, _ := json.Marshal(list)
	if strings.Contains(string(listJSON), "SYNTHETICSECRET") || !strings.Contains(string(listJSON), `"webhook_host":"hooks.slack.com"`) {
		t.Fatalf("list = %s", listJSON)
	}
	if !list.SealerAvailable || len(list.Kinds) != 2 {
		t.Fatalf("list = %+v, want the sealer available and two kinds", list)
	}
}

func TestNotifyDestinationPutWithBlankURLKeepsTheSealedValue(t *testing.T) {
	svc, eng, _ := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack"})
	if err != nil || !got.Live || got.WebhookHost != "hooks.slack.com" {
		t.Fatalf("blank-URL update = %+v, %v; want it to keep the stored URL and stay live", got, err)
	}
	if value, _ := eng.secretStore.Resolve(ctx, auth.GlobalSecretScope, "notify/oncall/webhook_url"); string(value) != syntheticSlackURL {
		t.Fatalf("sealed secret = %q after a blank-URL update, want the original", value)
	}
	// A new URL rotates the sealed value and the open connector.
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL2}); err != nil {
		t.Fatal(err)
	}
	if value, _ := eng.secretStore.Resolve(ctx, auth.GlobalSecretScope, "notify/oncall/webhook_url"); string(value) != syntheticSlackURL2 {
		t.Fatalf("sealed secret = %q after rotation, want the new URL", value)
	}
}

func TestNotifyDestinationPutRefusesWhatTheConsoleMayNotSave(t *testing.T) {
	svc, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	svc.dispatch.specs = []notifyDestinationSpec{{Name: "from-file", Kind: "jira"}}
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "kept", Kind: "teams", WebhookURL: syntheticTeamsURL}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		in   api.NotifyDestinationInput
	}{
		{"a Business kind", api.NotifyDestinationInput{Name: "pager", Kind: "pagerduty", WebhookURL: syntheticSlackURL}},
		{"an unknown kind", api.NotifyDestinationInput{Name: "x", Kind: "nope", WebhookURL: syntheticSlackURL}},
		{"a name with capitals", api.NotifyDestinationInput{Name: "OnCall", Kind: "slack", WebhookURL: syntheticSlackURL}},
		{"an empty name", api.NotifyDestinationInput{Name: " ", Kind: "slack", WebhookURL: syntheticSlackURL}},
		{"a name the operator file owns", api.NotifyDestinationInput{Name: "from-file", Kind: "slack", WebhookURL: syntheticSlackURL}},
		{"a new destination with no URL", api.NotifyDestinationInput{Name: "fresh", Kind: "slack"}},
		{"another host", api.NotifyDestinationInput{Name: "fresh", Kind: "slack", WebhookURL: "https://evil.example/services/T0/B0/X"}},
		{"a secret reference instead of a URL", api.NotifyDestinationInput{Name: "fresh", Kind: "slack", WebhookURL: "store:source/prod/password"}},
		{"another kind for an existing name", api.NotifyDestinationInput{Name: "kept", Kind: "slack", WebhookURL: syntheticSlackURL}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.Put(ctx, actor, c.in); !errors.Is(err, api.ErrNotifyDestinationInvalid) {
				t.Fatalf("put(%+v) = %v, want ErrNotifyDestinationInvalid", c.in, err)
			}
		})
	}
	// A refused write seals nothing, stores nothing and opens nothing.
	stored, _ := settings.NotifyDestinations(ctx)
	if len(stored) != 1 || stored[0].Name != "kept" {
		t.Fatalf("stored = %+v, want only the earlier destination", stored)
	}
	views, _ := eng.secretStore.List(ctx, auth.GlobalSecretScope)
	for _, v := range views {
		if strings.HasPrefix(v.Name, "notify/") && v.Name != "notify/kept/webhook_url" {
			t.Fatalf("a refused write sealed %q", v.Name)
		}
	}
}

func TestNotifyDestinationDeleteRemovesTheDefinitionTheConnectorAndTheSecret(t *testing.T) {
	svc, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	svc.dispatch.specs = []notifyDestinationSpec{{Name: "from-file", Kind: "jira"}}
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(ctx, actor, "oncall"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if stored, _ := settings.NotifyDestinations(ctx); len(stored) != 0 {
		t.Fatalf("stored = %+v after delete", stored)
	}
	if strings.Contains(strings.Join(eng.notifyDispatcher.Destinations(), ","), "oncall") {
		t.Fatalf("the dispatcher still lists oncall: %v", eng.notifyDispatcher.Destinations())
	}
	if _, found, _ := eng.secretStore.Get(ctx, auth.GlobalSecretScope, "notify/oncall/webhook_url"); found {
		t.Fatal("the sealed webhook URL survived the delete")
	}
	// An operator-file destination and an unknown name are the same answer.
	for _, name := range []string{"from-file", "never-existed", "oncall"} {
		if err := svc.Delete(ctx, actor, name); !errors.Is(err, api.ErrNotifyDestinationNotFound) {
			t.Fatalf("delete(%q) = %v, want ErrNotifyDestinationNotFound", name, err)
		}
	}
}

// A console destination survives a restart: the next boot reads the settings record,
// resolves the sealed reference and opens it, with no console action.
func TestNotifyDestinationOpensAtBoot(t *testing.T) {
	svc, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}

	restarted := newConnectorDispatcher(nil, nil, quietLog())
	loadStoredNotifyDestinations(ctx, settings, restarted, quietLog())
	restarted.openAll(ctx, eng.secretResolver, quietLog())
	if got := restarted.Destinations(); len(got) != 1 || got[0] != "oncall" {
		t.Fatalf("destinations after restart = %v, want [oncall]", got)
	}
}

// The operator file wins a name that is in both, so a file edit is never shadowed.
func TestNotifyDestinationFileWinsAtBoot(t *testing.T) {
	svc, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	hits := 0
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer file.Close()
	fileSpec := notifyDestinationSpec{Name: "oncall", Kind: "webhook", Config: map[string]string{"url": file.URL}}
	restarted := newConnectorDispatcher([]notifyDestinationSpec{fileSpec}, nil, quietLog())
	loadStoredNotifyDestinations(ctx, settings, restarted, quietLog())
	restarted.openAll(ctx, eng.secretResolver, quietLog())
	if err := restarted.Deliver(ctx, model.NewTenantID(), "oncall", sdk.Notification{Title: "t"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("the operator file's destination received %d deliveries, want 1 (the console's definition must not shadow it)", hits)
	}
}

type recordingConnector struct {
	mu       sync.Mutex
	opened   sdk.Config
	notified []sdk.Notification
	closed   int
	openErr  error
	sendErr  error
}

func (c *recordingConnector) Descriptor() sdk.Descriptor { return sdk.Descriptor{} }
func (c *recordingConnector) Open(_ context.Context, cfg sdk.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened = cfg
	return c.openErr
}
func (c *recordingConnector) Notify(_ context.Context, n sdk.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notified = append(c.notified, n)
	return c.sendErr
}
func (c *recordingConnector) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func TestNotifyDestinationTestSendsOneMessageWithoutSavingAnything(t *testing.T) {
	svc, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	rec := &recordingConnector{}
	svc.connect = func(kind string) (sdk.OutputConnector, error) { return rec, nil }

	if err := svc.Test(ctx, actor, api.NotifyDestinationInput{Name: "candidate", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := rec.opened.Settings["webhook_url"]; got != syntheticSlackURL {
		t.Fatalf("the connector was opened with %q, want the typed URL", got)
	}
	if len(rec.notified) != 1 || rec.notified[0].Type != "olivares.test" || rec.closed != 1 {
		t.Fatalf("notified=%+v closed=%d, want one test message and a close", rec.notified, rec.closed)
	}
	if stored, _ := settings.NotifyDestinations(ctx); len(stored) != 0 {
		t.Fatalf("a test saved %+v", stored)
	}
	if got := eng.notifyDispatcher.Destinations(); len(got) != 0 {
		t.Fatalf("a test opened %v", got)
	}
	if _, found, _ := eng.secretStore.Get(ctx, auth.GlobalSecretScope, "notify/candidate/webhook_url"); found {
		t.Fatal("a test sealed a secret")
	}
}

func TestNotifyDestinationTestWithBlankURLUsesTheStoredOne(t *testing.T) {
	svc, _, _ := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}
	rec := &recordingConnector{}
	svc.connect = func(string) (sdk.OutputConnector, error) { return rec, nil }
	if err := svc.Test(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack"}); err != nil {
		t.Fatalf("test with the stored URL: %v", err)
	}
	if got := rec.opened.Settings["webhook_url"]; got != syntheticSlackURL {
		t.Fatalf("the connector was opened with %q, want the stored URL", got)
	}
	// Nothing stored under that name: there is nothing to test.
	if err := svc.Test(ctx, actor, api.NotifyDestinationInput{Name: "unknown", Kind: "slack"}); !errors.Is(err, api.ErrNotifyDestinationInvalid) {
		t.Fatalf("test of an unknown stored destination = %v, want ErrNotifyDestinationInvalid", err)
	}
}

func TestNotifyDestinationTestFailureNeverEchoesTheURL(t *testing.T) {
	svc, _, _ := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	for name, rec := range map[string]*recordingConnector{
		"send refused": {sendErr: errors.New("post " + syntheticSlackURL + ": 404")},
		"open refused": {openErr: errors.New("bad url " + syntheticSlackURL)},
	} {
		svc.connect = func(string) (sdk.OutputConnector, error) { return rec, nil }
		err := svc.Test(ctx, actor, api.NotifyDestinationInput{Name: "c", Kind: "slack", WebhookURL: syntheticSlackURL})
		if !errors.Is(err, api.ErrNotifyDestinationTestFailed) {
			t.Fatalf("%s: test = %v, want ErrNotifyDestinationTestFailed", name, err)
		}
		if strings.Contains(err.Error(), "SYNTHETICSECRET") {
			t.Fatalf("%s: the error echoes the URL: %v", name, err)
		}
		if rec.closed != 1 {
			t.Fatalf("%s: the connector was closed %d times, want 1", name, rec.closed)
		}
	}
	// A host the console may not send to is refused before any connector is built.
	svc.connect = func(string) (sdk.OutputConnector, error) {
		t.Fatal("a connector was built for a refused host")
		return nil, nil
	}
	if err := svc.Test(ctx, actor, api.NotifyDestinationInput{Name: "c", Kind: "slack", WebhookURL: "https://evil.example/x/y"}); !errors.Is(err, api.ErrNotifyDestinationInvalid) {
		t.Fatalf("test of another host = %v, want ErrNotifyDestinationInvalid", err)
	}
}

// applyStored and removeStored change what a route reaches in the running engine: a new
// connector replaces the old one atomically, and a connector that cannot open leaves
// the old one serving.
func TestConnectorDispatcherApplyStoredReplacesAndRemoves(t *testing.T) {
	_, eng, _ := consoleDestinations(t)
	var hitsA, hitsB int
	var mu sync.Mutex
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hitsA++
		mu.Unlock()
	}))
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hitsB++
		mu.Unlock()
	}))
	defer a.Close()
	defer b.Close()
	d := newConnectorDispatcher(nil, nil, quietLog())
	ctx, tenant := context.Background(), model.NewTenantID()
	deliver := func() error { return d.Deliver(ctx, tenant, "hook", sdk.Notification{Title: "t"}) }

	if err := deliver(); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Fatalf("before apply: %v, want ErrUnknownDestination", err)
	}
	if err := d.applyStored(ctx, notifyDestinationSpec{Name: "hook", Kind: "webhook", Config: map[string]string{"url": a.URL}}, eng.secretResolver, quietLog()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := deliver(); err != nil {
		t.Fatalf("deliver after apply: %v", err)
	}
	if err := d.applyStored(ctx, notifyDestinationSpec{Name: "hook", Kind: "webhook", Config: map[string]string{"url": b.URL}}, eng.secretResolver, quietLog()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := deliver(); err != nil {
		t.Fatalf("deliver after replace: %v", err)
	}
	// A spec that cannot open leaves the destination that is serving.
	if err := d.applyStored(ctx, notifyDestinationSpec{Name: "hook", Kind: "no-such-kind"}, eng.secretResolver, quietLog()); err == nil {
		t.Fatal("apply of an unknown kind succeeded")
	}
	if err := deliver(); err != nil {
		t.Fatalf("deliver after a refused replace: %v", err)
	}
	mu.Lock()
	gotA, gotB := hitsA, hitsB
	mu.Unlock()
	if gotA != 1 || gotB != 2 {
		t.Fatalf("deliveries a=%d b=%d, want 1 and 2 (the replace took effect, the refused one did not)", gotA, gotB)
	}

	d.removeStored(ctx, "hook")
	if err := deliver(); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Fatalf("after remove: %v, want ErrUnknownDestination", err)
	}
	if _, ok := d.ConnectorFingerprint("hook"); ok {
		t.Fatal("a removed destination still has a fingerprint")
	}
}

// hitCounter is an operator-file destination: a real webhook endpoint that counts what
// it receives.
func hitCounter(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return hits }
}

// serviceOver is the service over the engine's settings and secret store but a
// dispatcher of the test's own, so an operator file can be modelled.
func serviceOver(eng *engine, settings *productSettings, d *connectorDispatcher) *notifyDestinationService {
	return newNotifyDestinationService(settings, eng.secretStore, eng.secretResolver, d, quietLog())
}

func TestNotifyDestinationCannotReplaceOrRemoveAFileDestination(t *testing.T) {
	_, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	srv, hits := hitCounter(t)
	d := newConnectorDispatcher([]notifyDestinationSpec{{Name: "ops", Kind: "webhook", Config: map[string]string{"url": srv.URL}}}, nil, quietLog())
	d.openAll(ctx, eng.secretResolver, quietLog())
	// A destination a SIGHUP reload added from the file is open but not in d.specs.
	hup, _ := buildOutputConnector("webhook")
	if err := hup.Open(ctx, sdk.Config{Settings: map[string]string{"url": srv.URL}}); err != nil {
		t.Fatal(err)
	}
	d.put("hup", hup, "fp")
	svc := serviceOver(eng, settings, d)

	for _, name := range []string{"ops", "hup"} {
		if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: name, Kind: "slack", WebhookURL: syntheticSlackURL}); !errors.Is(err, api.ErrNotifyDestinationInvalid) {
			t.Fatalf("put over the file's %q = %v, want ErrNotifyDestinationInvalid", name, err)
		}
		if err := d.applyStored(ctx, notifyDestinationSpec{Name: name, Kind: "webhook", Config: map[string]string{"url": srv.URL}}, eng.secretResolver, quietLog()); err == nil {
			t.Fatalf("applyStored replaced the file's %q", name)
		}
		d.removeStored(ctx, name)
		if err := d.Deliver(ctx, model.NewTenantID(), name, sdk.Notification{Title: "t"}); err != nil {
			t.Fatalf("the file's %q stopped delivering: %v", name, err)
		}
	}
	if got := hits(); got != 2 {
		t.Fatalf("deliveries to the file's destinations = %d, want 2", got)
	}
}

// A console definition that a file name shadows can still be deleted (with its secret),
// and deleting it leaves the file's destination serving.
func TestNotifyDestinationDeleteOfAShadowedDefinitionLeavesTheFilesDestination(t *testing.T) {
	_, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	srv, hits := hitCounter(t)
	d := newConnectorDispatcher([]notifyDestinationSpec{{Name: "ops", Kind: "webhook", Config: map[string]string{"url": srv.URL}}}, nil, quietLog())
	d.openAll(ctx, eng.secretResolver, quietLog())
	svc := serviceOver(eng, settings, d)
	if err := settings.saveNotifyDestination(ctx, actor, storedNotifyDestination{Name: "ops", Kind: "slack", Config: map[string]string{notifyWebhookField: notifySecretRef("ops")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.secretStore.Put(ctx, actor, auth.GlobalSecretScope, notifySecretName("ops"), syntheticSlackURL, "test"); err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(ctx, actor, "ops"); err != nil {
		t.Fatalf("delete of the shadowed definition: %v", err)
	}
	if stored, _ := settings.NotifyDestinations(ctx); len(stored) != 0 {
		t.Fatalf("stored = %+v", stored)
	}
	if _, found, _ := eng.secretStore.Get(ctx, auth.GlobalSecretScope, notifySecretName("ops")); found {
		t.Fatal("the shadowed definition's sealed URL survived")
	}
	if err := d.Deliver(ctx, model.NewTenantID(), "ops", sdk.Notification{Title: "t"}); err != nil || hits() != 1 {
		t.Fatalf("the file's destination = %v, hits %d; want it still serving", err, hits())
	}
}

// If the file's own destination cannot open, the console's definition of that name must
// not take its place (nor inherit its tenant scope).
func TestNotifyDestinationDoesNotTakeAFileNameWhenTheFilesDoesNotOpen(t *testing.T) {
	_, eng, _ := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := eng.secretStore.Put(ctx, actor, auth.GlobalSecretScope, notifySecretName("oncall"), syntheticSlackURL, "test"); err != nil {
		t.Fatal(err)
	}
	d := newConnectorDispatcher([]notifyDestinationSpec{{Name: "oncall", Kind: "no-such-kind"}}, nil, quietLog())
	d.setStored([]notifyDestinationSpec{{Name: "oncall", Kind: "slack", Config: map[string]string{notifyWebhookField: notifySecretRef("oncall")}}}, quietLog())
	d.openAll(ctx, eng.secretResolver, quietLog())
	if got := d.Destinations(); len(got) != 0 {
		t.Fatalf("destinations = %v, want none: the console took the file's name", got)
	}
}

func TestNotifyDestinationRotationReplacesTheOpenConnector(t *testing.T) {
	svc, eng, _ := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}
	first, ok := eng.notifyDispatcher.ConnectorFingerprint("oncall")
	if !ok {
		t.Fatal("oncall is not open")
	}
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack"}); err != nil {
		t.Fatal(err)
	}
	if again, _ := eng.notifyDispatcher.ConnectorFingerprint("oncall"); again != first {
		t.Fatal("a blank-URL update changed the connector")
	}
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL2}); err != nil {
		t.Fatal(err)
	}
	if rotated, _ := eng.notifyDispatcher.ConnectorFingerprint("oncall"); rotated == first {
		t.Fatal("a new URL left the connector built from the old one")
	}
	// Teams opens too, and saving the same name twice keeps one definition.
	got, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "chat", Kind: "teams", WebhookURL: syntheticTeamsURL})
	if err != nil || !got.Live {
		t.Fatalf("teams put = %+v, %v; want live", got, err)
	}
	stored, _ := svc.settings.NotifyDestinations(ctx)
	if len(stored) != 2 {
		t.Fatalf("stored = %+v, want oncall and chat once each", stored)
	}
}

// The new URL is already sealed when the connector is rebuilt. If that fails, the old
// connector must not keep delivering to the URL that was just replaced.
func TestNotifyDestinationClosesWhenTheStoredURLCannotBeResolved(t *testing.T) {
	svc, eng, _ := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}
	// Someone removes the sealed value on the Secrets page; the definition stays.
	if err := eng.secretStore.Delete(ctx, actor, auth.GlobalSecretScope, notifySecretName("oncall")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack"})
	if err != nil || got.Live {
		t.Fatalf("put = %+v, %v; want saved and not live", got, err)
	}
	if strings.Contains(strings.Join(eng.notifyDispatcher.Destinations(), ","), "oncall") {
		t.Fatal("the old connector kept serving after the rebuild failed")
	}
}

func TestNotifyDestinationPutWithoutASealerIsRefused(t *testing.T) {
	_, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	svc := newNotifyDestinationService(settings, nil, eng.secretResolver, eng.notifyDispatcher, quietLog())
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); !errors.Is(err, auth.ErrNoSecretSealer) {
		t.Fatalf("put without a secret store = %v, want ErrNoSecretSealer", err)
	}
	if stored, _ := settings.NotifyDestinations(ctx); len(stored) != 0 {
		t.Fatalf("stored = %+v: a definition without its secret", stored)
	}
	if list, _ := svc.List(ctx); list.SealerAvailable {
		t.Fatal("List says a sealer is available")
	}
}

// A refused write must not touch the sealed value of the destination it targeted.
func TestNotifyDestinationRefusedWriteKeepsTheExistingSecret(t *testing.T) {
	svc, eng, _ := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "kept", Kind: "teams", WebhookURL: syntheticTeamsURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "kept", Kind: "slack", WebhookURL: syntheticSlackURL}); !errors.Is(err, api.ErrNotifyDestinationInvalid) {
		t.Fatalf("kind change = %v, want ErrNotifyDestinationInvalid", err)
	}
	if value, _ := eng.secretStore.Resolve(ctx, auth.GlobalSecretScope, notifySecretName("kept")); string(value) != syntheticTeamsURL {
		t.Fatalf("the refused write replaced the sealed URL with %q", value)
	}
}

func TestNotifyDestinationListMergesTheFileAndTheConsole(t *testing.T) {
	_, eng, settings := consoleDestinations(t)
	ctx, actor := context.Background(), operator(t)
	srv, _ := hitCounter(t)
	d := newConnectorDispatcher([]notifyDestinationSpec{{Name: "file-a", Kind: "webhook", Config: map[string]string{"url": srv.URL}}}, nil, quietLog())
	d.openAll(ctx, eng.secretResolver, quietLog())
	svc := serviceOver(eng, settings, d)
	if _, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "console-b", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil {
		t.Fatal(err)
	}
	// A console definition the file shadows is not listed twice.
	if err := settings.saveNotifyDestination(ctx, actor, storedNotifyDestination{Name: "file-a", Kind: "slack"}); err != nil {
		t.Fatal(err)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []api.NotifyDestination{
		{Name: "console-b", Kind: "slack", Source: api.NotifyDestinationSourceConsole, Live: true, WebhookHost: "hooks.slack.com"},
		{Name: "file-a", Kind: "webhook", Source: api.NotifyDestinationSourceFile, Live: true},
	}
	if len(list.Destinations) != len(want) || list.Destinations[0] != want[0] || list.Destinations[1] != want[1] {
		t.Fatalf("destinations = %+v, want %+v", list.Destinations, want)
	}
}

// A console destination is open again after the engine restarts, through the real boot
// path: the settings record, the sealed secret and the dispatcher together.
func TestNotifyDestinationSurvivesARestart(t *testing.T) {
	prepareCompositionTestBoot(t)
	dir := t.TempDir()
	ctx, actor := context.Background(), operator(t)
	start := func() *engine {
		eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: slog.Default()})
		if err != nil {
			t.Fatalf("boot: %v", err)
		}
		return eng
	}
	first := start()
	svc := newNotifyDestinationService(newProductSettings(first.store, first.dataDir), first.secretStore, first.secretResolver, first.notifyDispatcher, quietLog())
	if got, err := svc.Put(ctx, actor, api.NotifyDestinationInput{Name: "oncall", Kind: "slack", WebhookURL: syntheticSlackURL}); err != nil || !got.Live {
		t.Fatalf("put = %+v, %v", got, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := start()
	t.Cleanup(func() { _ = second.Close() })
	if got := second.notifyDispatcher.Destinations(); !strings.Contains(strings.Join(got, ","), "oncall") {
		t.Fatalf("destinations after the restart = %v, want oncall", got)
	}
	if _, ok := second.notifyDispatcher.ConnectorFingerprint("oncall"); !ok {
		t.Fatal("oncall is listed but has no live connector")
	}
}

// The host check sees only the first hop, so a console destination never follows a
// redirect: a flow on an allowed host cannot send the engine, or the alert, elsewhere.
func TestConsoleHTTPClientDoesNotFollowRedirects(t *testing.T) {
	elsewhere, hits := hitCounter(t)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	resp, err := consoleHTTPClient.Post(redirector.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || hits() != 0 {
		t.Fatalf("status %d, %d requests reached the redirect target; want the 307 returned and none followed", resp.StatusCode, hits())
	}
}

// The whole stack on a booted engine, over its real HTTP handler: the route gate, the
// sealed secret store, the deployment settings and the dispatcher together. The
// administrator never sees the webhook URL again, and what is sealed is removed with
// the destination.
func TestNotifyConsoleDestinationsThroughTheRealHandler(t *testing.T) {
	_, call, _ := mcpRealHandler(t)
	secretNames := func() string {
		raw, _ := json.Marshal(call("GET", "/v1/console/secrets", nil, http.StatusOK)["secrets"])
		return string(raw)
	}

	empty := call("GET", "/v1/console/notify-destinations", nil, http.StatusOK)
	if dests, _ := empty["destinations"].([]any); len(dests) != 0 || empty["sealer_available"] != true {
		t.Fatalf("a fresh engine lists %v (sealer %v), want none and a sealer", empty["destinations"], empty["sealer_available"])
	}

	// Another host is refused, and the refusal does not repeat the URL.
	bad := call("PUT", "/v1/console/notify-destinations", map[string]any{"name": "oncall", "kind": "slack", "webhook_url": "https://evil.example/services/T0/B0/SYNTHETICSECRET"}, http.StatusBadRequest)
	if raw, _ := json.Marshal(bad); strings.Contains(string(raw), "SYNTHETICSECRET") {
		t.Fatalf("the refusal echoes the URL: %s", raw)
	}
	// So is a Business kind: Jira, PagerDuty and the SIEM kinds stay in the operator file.
	call("PUT", "/v1/console/notify-destinations", map[string]any{"name": "pager", "kind": "pagerduty", "webhook_url": syntheticSlackURL}, http.StatusBadRequest)

	saved := call("PUT", "/v1/console/notify-destinations", map[string]any{"name": "oncall", "kind": "slack", "webhook_url": syntheticSlackURL}, http.StatusOK)
	if saved["live"] != true || saved["webhook_host"] != "hooks.slack.com" || saved["source"] != "console" {
		t.Fatalf("put = %v", saved)
	}
	listed := call("GET", "/v1/console/notify-destinations", nil, http.StatusOK)
	if raw, _ := json.Marshal(listed); strings.Contains(string(raw), "SYNTHETICSECRET") || !strings.Contains(string(raw), `"name":"oncall"`) {
		t.Fatalf("list = %s", raw)
	}
	// The sealed secret store lists the name and a hint, never the value.
	if names := secretNames(); !strings.Contains(names, "notify/oncall/webhook_url") || strings.Contains(names, "SYNTHETICSECRET") {
		t.Fatalf("secrets = %s", names)
	}

	call("DELETE", "/v1/console/notify-destinations", map[string]any{"name": "oncall"}, http.StatusOK)
	call("DELETE", "/v1/console/notify-destinations", map[string]any{"name": "oncall"}, http.StatusNotFound)
	if after := call("GET", "/v1/console/notify-destinations", nil, http.StatusOK); len(after["destinations"].([]any)) != 0 {
		t.Fatalf("list after delete = %v", after["destinations"])
	}
	if names := secretNames(); strings.Contains(names, "notify/oncall") {
		t.Fatalf("the sealed URL survived the delete: %s", names)
	}
}
