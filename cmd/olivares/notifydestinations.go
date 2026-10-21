// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/slack"
	"github.com/olivaresai/olivares/connectors/teams"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Console notification destinations (core/api/notify_destinations.go). The webhook URL
// is the destination's secret: it is sealed in the secret store under
// `notify/<name>/webhook_url`, and the definition kept in the deployment settings holds
// only the `store:` reference, so the same reference resolver the operator file uses
// opens it. The operator file stays the route for every other kind and for tenant
// scopes; a name in both is the file's.

// notifyWebhookField is the connector setting that carries the secret for both kinds.
const notifyWebhookField = "webhook_url"

// notifyConsoleKind is a destination kind the console can create: Slack and Teams
// incoming webhooks. The URL is typed by an administrator and the engine posts to it, so
// it must be an https URL on the vendor's own hosts (a host starting with "." matches
// its subdomains): anywhere else would make the console a way to reach internal
// addresses. A deployment that needs another host provisions the destination in the
// operator file, as before.
type notifyConsoleKind struct {
	title string
	hosts []string
}

var notifyConsoleKinds = map[string]notifyConsoleKind{
	"slack": {title: "Slack", hosts: []string{"hooks.slack.com", "hooks.slack-gov.com"}},
	"teams": {title: "Microsoft Teams", hosts: []string{".logic.azure.com", ".webhook.office.com", ".powerplatform.com"}},
}

// consoleHTTPClient posts to a console destination without following redirects. The
// host check validates only the first hop: a flow on an allowed Teams host can answer
// 307 to an internal address, or to a server that would receive every alert, and the
// default client would follow it. A redirect is a failed delivery.
//
// cli-transport-exempt: ENGINE→vendor webhook (Slack, Teams), not a CLI-to-control-plane
// call. The destination is the vendor's public TLS endpoint, so the operator's --ca-cert,
// --pin-sha256 and client context, which cliTransport applies to the control plane, must
// not reach it.
var consoleHTTPClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// buildConsoleConnector builds the connector for a destination the console owns: the
// Slack and Teams ones use consoleHTTPClient. Any other kind is built as the operator
// file builds it.
func buildConsoleConnector(kind string) (sdk.OutputConnector, error) {
	switch kind {
	case "slack":
		return slack.New().WithHTTPClient(consoleHTTPClient), nil
	case "teams":
		return teams.New().WithHTTPClient(consoleHTTPClient), nil
	}
	return buildOutputConnector(kind)
}

// notifyDestinationName is the console destination name: lowercase, so two names never
// differ only by case.
var notifyDestinationName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// notifyTestTimeout bounds the test send.
const notifyTestTimeout = 15 * time.Second

// storedNotifyDestination is a console destination as the deployment settings keep it.
// Config holds secret references, never a value. WebhookHost is the non-secret host
// of the URL, kept so the list can show where a destination points without opening
// the sealed value.
type storedNotifyDestination struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Config      map[string]string `json:"config"`
	WebhookHost string            `json:"webhook_host,omitempty"`
}

func (d storedNotifyDestination) spec() notifyDestinationSpec {
	return notifyDestinationSpec{Name: d.Name, Kind: d.Kind, Config: d.Config}
}

func notifySecretName(name string) string { return "notify/" + name + "/" + notifyWebhookField }

func notifySecretRef(name string) string { return secret.SchemeStore + ":" + notifySecretName(name) }

// NotifyDestinations returns the console destinations in the deployment settings.
func (p *productSettings) NotifyDestinations(ctx context.Context) ([]storedNotifyDestination, error) {
	doc, _, err := p.load(ctx)
	return doc.NotifyDestinations, err
}

// saveNotifyDestination adds or replaces a console destination, audited.
func (p *productSettings) saveNotifyDestination(ctx context.Context, actor auth.Principal, d storedNotifyDestination) error {
	meta := map[string]any{"name": d.Name, "kind": d.Kind, "change": "put"}
	return p.update(ctx, actor, "deployment.settings.notify_destination", meta, func(doc *productSettingsDoc) {
		doc.NotifyDestinations = slices.DeleteFunc(doc.NotifyDestinations, func(o storedNotifyDestination) bool { return o.Name == d.Name })
		doc.NotifyDestinations = append(doc.NotifyDestinations, d)
	})
}

// deleteNotifyDestination removes a console destination, audited.
func (p *productSettings) deleteNotifyDestination(ctx context.Context, actor auth.Principal, name string) error {
	meta := map[string]any{"name": name, "change": "delete"}
	return p.update(ctx, actor, "deployment.settings.notify_destination", meta, func(doc *productSettingsDoc) {
		doc.NotifyDestinations = slices.DeleteFunc(doc.NotifyDestinations, func(o storedNotifyDestination) bool { return o.Name == name })
	})
}

// loadStoredNotifyDestinations hands the console's destinations to the dispatcher so
// openAll opens them at boot. A settings record that cannot be read leaves them out
// with a warning: a console destination is never worth refusing to start for, and the
// next start reads it again.
func loadStoredNotifyDestinations(ctx context.Context, settings *productSettings, dispatch *connectorDispatcher, log *slog.Logger) {
	if dispatch == nil || settings == nil {
		return
	}
	stored, err := settings.NotifyDestinations(ctx)
	if err != nil {
		log.Error("notify: the destinations added in the console could not be read from the deployment settings; they are not open until the next start", "err", err)
		return
	}
	specs := make([]notifyDestinationSpec, 0, len(stored))
	for _, d := range stored {
		specs = append(specs, d.spec())
	}
	dispatch.setStored(specs, log)
}

// notifyDestinationService implements api.NotifyDestinations.
type notifyDestinationService struct {
	settings *productSettings
	secrets  *auth.SecretStore
	resolver *secret.Resolver
	dispatch *connectorDispatcher
	log      *slog.Logger
	// connect builds the connector a test send uses; tests replace it so no request
	// leaves the process.
	connect func(kind string) (sdk.OutputConnector, error)
	// mu serializes the read-modify-write of one definition with its live apply.
	mu sync.Mutex
}

var _ api.NotifyDestinations = (*notifyDestinationService)(nil)

func newNotifyDestinationService(settings *productSettings, secrets *auth.SecretStore, resolver *secret.Resolver, dispatch *connectorDispatcher, log *slog.Logger) *notifyDestinationService {
	return &notifyDestinationService{settings: settings, secrets: secrets, resolver: resolver, dispatch: dispatch, log: log, connect: buildConsoleConnector}
}

func invalidNotifyDestination(format string, args ...any) error {
	return fmt.Errorf("%w: %s", api.ErrNotifyDestinationInvalid, fmt.Sprintf(format, args...))
}

// validateNotifyWebhook checks a typed webhook URL against the kind's hosts and returns
// its host. The error text never repeats the URL: the path of a Slack webhook is the secret.
func validateNotifyWebhook(kind notifyConsoleKind, raw string) (string, error) {
	// ASCII only: strings.ToLower folds some non-ASCII characters (U+0130, U+212A) to
	// ASCII letters, so a host that merely resembles a vendor's would pass the match
	// below while the HTTP client resolves a different name. A Teams Workflows URL
	// carries its credential in a query (?sig=…), so a query is allowed.
	for i := 0; i < len(raw); i++ {
		if raw[i] <= ' ' || raw[i] >= 0x7f {
			return "", invalidNotifyDestination("the webhook URL must be plain ASCII without spaces")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", invalidNotifyDestination("the webhook URL must be an https URL without credentials or fragment")
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", invalidNotifyDestination("the webhook URL must use the https port")
	}
	if u.Path == "" || u.Path == "/" {
		return "", invalidNotifyDestination("the webhook URL must include its path")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range kind.hosts {
		if host == h || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return host, nil
		}
	}
	return "", invalidNotifyDestination("the webhook URL must be on %s", strings.Join(kind.hosts, ", "))
}

// identity validates the kind and the name; the name must not belong to the operator file.
func (s *notifyDestinationService) identity(in api.NotifyDestinationInput) (string, string, notifyConsoleKind, error) {
	kindName := strings.TrimSpace(in.Kind)
	kind, ok := notifyConsoleKinds[kindName]
	if !ok {
		return "", "", notifyConsoleKind{}, invalidNotifyDestination("the console can create slack or teams destinations; other kinds are provisioned in the operator file")
	}
	name := strings.TrimSpace(in.Name)
	if !notifyDestinationName.MatchString(name) {
		return "", "", notifyConsoleKind{}, invalidNotifyDestination("the name must be 1 to 63 lowercase letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	if s.dispatch.notConsoleOwned(name) {
		return "", "", notifyConsoleKind{}, invalidNotifyDestination("%q is provisioned in the operator file and cannot be edited here", name)
	}
	return name, kindName, kind, nil
}

func (s *notifyDestinationService) find(ctx context.Context, name string) (storedNotifyDestination, bool, error) {
	stored, err := s.settings.NotifyDestinations(ctx)
	if err != nil {
		return storedNotifyDestination{}, false, err
	}
	for _, d := range stored {
		if d.Name == name {
			return d, true, nil
		}
	}
	return storedNotifyDestination{}, false, nil
}

// List returns the operator file's and the console's destinations, sorted by name.
func (s *notifyDestinationService) List(ctx context.Context) (api.NotifyDestinationList, error) {
	stored, err := s.settings.NotifyDestinations(ctx)
	if err != nil {
		return api.NotifyDestinationList{}, err
	}
	out := api.NotifyDestinationList{SealerAvailable: s.secrets != nil && s.secrets.SealerWired()}
	for _, f := range s.dispatch.specs {
		out.Destinations = append(out.Destinations, api.NotifyDestination{
			Name: f.Name, Kind: f.Kind, Source: api.NotifyDestinationSourceFile, Live: s.dispatch.has(f.Name),
		})
	}
	for _, d := range stored {
		if _, file := s.dispatch.fileSpec(d.Name); file {
			continue // the file wins a name in both; the stored one never opened
		}
		out.Destinations = append(out.Destinations, api.NotifyDestination{
			Name: d.Name, Kind: d.Kind, Source: api.NotifyDestinationSourceConsole, Live: s.dispatch.has(d.Name), WebhookHost: d.WebhookHost,
		})
	}
	slices.SortFunc(out.Destinations, func(a, b api.NotifyDestination) int { return strings.Compare(a.Name, b.Name) })
	for kind, k := range notifyConsoleKinds {
		out.Kinds = append(out.Kinds, api.NotifyDestinationKind{Kind: kind, Title: k.title})
	}
	slices.SortFunc(out.Kinds, func(a, b api.NotifyDestinationKind) int { return strings.Compare(a.Kind, b.Kind) })
	return out, nil
}

// Put seals a typed webhook URL, stores the definition and opens the destination.
func (s *notifyDestinationService) Put(ctx context.Context, actor auth.Principal, in api.NotifyDestinationInput) (api.NotifyDestination, error) {
	name, kindName, kind, err := s.identity(in)
	if err != nil {
		return api.NotifyDestination{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, found, err := s.find(ctx, name)
	if err != nil {
		return api.NotifyDestination{}, err
	}
	if found && existing.Kind != kindName {
		return api.NotifyDestination{}, invalidNotifyDestination("%q is a %s destination; delete it to create a %s one", name, existing.Kind, kindName)
	}
	raw := strings.TrimSpace(in.WebhookURL)
	host := existing.WebhookHost
	switch {
	case raw == "" && !found:
		return api.NotifyDestination{}, invalidNotifyDestination("the webhook URL is required")
	case raw != "":
		if host, err = validateNotifyWebhook(kind, raw); err != nil {
			return api.NotifyDestination{}, err
		}
		if s.secrets == nil {
			return api.NotifyDestination{}, auth.ErrNoSecretSealer
		}
		desc := fmt.Sprintf("console notification destination %q webhook URL", name)
		if _, perr := s.secrets.Put(ctx, actor, auth.GlobalSecretScope, notifySecretName(name), raw, desc); perr != nil {
			return api.NotifyDestination{}, perr
		}
	}
	def := storedNotifyDestination{Name: name, Kind: kindName, Config: map[string]string{notifyWebhookField: notifySecretRef(name)}, WebhookHost: host}
	if err := s.settings.saveNotifyDestination(ctx, actor, def); err != nil {
		// A new destination whose definition was not saved must not leave a sealed URL
		// that nothing lists and nothing can delete. An update keeps the sealed value it
		// just replaced: the old one is gone, so say so.
		if !found && raw != "" {
			if derr := s.secrets.Delete(ctx, actor, auth.GlobalSecretScope, notifySecretName(name)); derr != nil {
				s.log.Error("notify destinations: the destination was not saved and its sealed webhook URL could not be removed", "name", name, "err", derr)
			}
		} else if raw != "" {
			s.log.Error("notify destinations: the destination was not saved but its sealed webhook URL was replaced; save it again", "name", name)
		}
		return api.NotifyDestination{}, err
	}
	live := s.dispatch.applyStored(ctx, def.spec(), s.resolver, s.log) == nil
	if !live {
		// The stored URL is the new one. Leaving the connector built from the old one
		// serving would deliver to the URL being rotated away while the list says it is
		// not open, so the destination closes until it is saved again.
		s.dispatch.removeStored(ctx, name)
	}
	return api.NotifyDestination{Name: name, Kind: kindName, Source: api.NotifyDestinationSourceConsole, Live: live, WebhookHost: host}, nil
}

// Delete removes a console destination, closes it and deletes its sealed URL.
func (s *notifyDestinationService) Delete(ctx context.Context, actor auth.Principal, name string) error {
	name = strings.TrimSpace(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found, err := s.find(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		return api.ErrNotifyDestinationNotFound
	}
	// The sealed URL goes first: a failure leaves the definition in place, so the same
	// request can be repeated. The other order leaves a secret nothing lists.
	if s.secrets != nil {
		if derr := s.secrets.Delete(ctx, actor, auth.GlobalSecretScope, notifySecretName(name)); derr != nil && !errors.Is(derr, auth.ErrSecretNotFound) {
			return derr
		}
	}
	if err := s.settings.deleteNotifyDestination(ctx, actor, name); err != nil {
		return err
	}
	// A name the operator file also defines is the file's: its connector is not closed.
	s.dispatch.removeStored(ctx, name)
	return nil
}

// Test sends one test message through the candidate without saving or wiring it.
func (s *notifyDestinationService) Test(ctx context.Context, _ auth.Principal, in api.NotifyDestinationInput) error {
	kindName := strings.TrimSpace(in.Kind)
	kind, ok := notifyConsoleKinds[kindName]
	if !ok {
		return invalidNotifyDestination("the console can create slack or teams destinations; other kinds are provisioned in the operator file")
	}
	raw := strings.TrimSpace(in.WebhookURL)
	if raw == "" {
		stored, found, err := s.find(ctx, strings.TrimSpace(in.Name))
		if err != nil {
			return err
		}
		if !found || stored.Kind != kindName || s.secrets == nil {
			return invalidNotifyDestination("enter the webhook URL to test")
		}
		value, err := s.secrets.Resolve(ctx, auth.GlobalSecretScope, notifySecretName(stored.Name))
		if err != nil {
			s.log.Warn("notify destinations: the stored webhook URL cannot be read", "name", stored.Name, "err", err)
			return invalidNotifyDestination("the stored webhook URL cannot be read; enter it again")
		}
		raw = string(value)
	}
	if _, err := validateNotifyWebhook(kind, raw); err != nil {
		return err
	}
	conn, err := s.connect(kindName)
	if err != nil {
		return invalidNotifyDestination("the console can create slack or teams destinations; other kinds are provisioned in the operator file")
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTestTimeout)
	defer cancel()
	defer func() { _ = conn.Close(ctx) }()
	// An Open error can embed the typed URL: it is never logged or returned. Neither
	// error from a failed test is returned, because the administrator's answer is the
	// generic ErrNotifyDestinationTestFailed.
	if oerr := conn.Open(ctx, sdk.Config{Settings: map[string]string{notifyWebhookField: raw}}); oerr != nil {
		s.log.Debug("notify destinations: test open failed", "kind", kindName)
		return api.ErrNotifyDestinationTestFailed
	}
	// The Slack and Teams connectors redact the URL from what they return (their
	// error_redaction tests), so the status or the network failure is safe to log:
	// it is what tells an administrator a revoked webhook from an unreachable host.
	if nerr := conn.Notify(ctx, sdk.Notification{
		Type:     "olivares.test",
		Title:    "Olivares test notification",
		Body:     "This message confirms that this destination accepts notifications from Olivares.",
		Severity: sdkmodel.SeverityLow,
		Time:     time.Now(),
	}); nerr != nil {
		s.log.Warn("notify destinations: the test message was not accepted", "kind", kindName, "err", nerr)
		return api.ErrNotifyDestinationTestFailed
	}
	return nil
}
