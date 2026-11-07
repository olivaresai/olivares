// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/connectors/email"
	"github.com/olivaresai/olivares/connectors/slack"
	"github.com/olivaresai/olivares/connectors/teams"
	"github.com/olivaresai/olivares/connectors/webhook"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Notification destinations an administrator adds from the console, the CLI or the API
// (core/api/notify_destinations.go). Each secret setting is sealed in the secret store
// under `notify/<name>/<setting>`, and the definition kept in the deployment settings
// holds only the `store:` reference, so the same reference resolver the operator file
// uses opens it. Business kinds and tenant scopes stay in the operator file; a name in
// both is the file's.

// notifyWebhookField is the Slack and Teams setting that carries the webhook URL.
const notifyWebhookField = "webhook_url"

// notifyConsoleKind is a destination kind an administrator can add. fields are the
// settings that may be set here; whether one is required or secret is the connector's
// own descriptor. address names the setting that says where it sends ("" = the
// connector's fixed vendor endpoint). With hosts, the address must be an https URL on
// the vendor's own hosts (a host starting with "." matches its subdomains); without,
// any public address is accepted and the engine's egress guard refuses private and
// reserved ones at connect time, so a destination added here is never a way to reach
// internal addresses. Anything else is provisioned in the operator file.
type notifyConsoleKind struct {
	title   string
	fields  []string
	address string
	hosts   []string
}

var notifyConsoleKinds = map[string]notifyConsoleKind{
	"slack":   {title: "Slack", fields: []string{notifyWebhookField}, address: notifyWebhookField, hosts: []string{"hooks.slack.com", "hooks.slack-gov.com"}},
	"teams":   {title: "Microsoft Teams", fields: []string{notifyWebhookField}, address: notifyWebhookField, hosts: []string{".logic.azure.com", ".webhook.office.com", ".powerplatform.com"}},
	"webhook": {title: "Signed webhook", fields: []string{"url", "signing_secret"}, address: "url"},
	"email":   {title: "Email (SMTP)", fields: []string{"smtp_host", "smtp_port", "smtp_username", "smtp_password", "from", "from_name", "to"}, address: "smtp_host"},
	"twilio":  {title: "Twilio SMS", fields: []string{"account_sid", "auth_token", "from", "messaging_service_sid", "to"}},
}

// notifyConsoleKindNames is the refusal text for an unknown kind.
const notifyConsoleKindNames = "slack, teams, webhook, email or twilio; other kinds are provisioned in the operator file"

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

// buildConsoleConnector builds the connector for a destination added here: Slack and
// Teams use consoleHTTPClient; a webhook and an SMTP server are reached through the
// engine's egress guard (no private or reserved address, no redirect, no proxy). Any
// other kind is built as the operator file builds it.
func buildConsoleConnector(kind string) (sdk.OutputConnector, error) {
	switch kind {
	case "slack":
		return slack.New().WithHTTPClient(consoleHTTPClient), nil
	case "teams":
		return teams.New().WithHTTPClient(consoleHTTPClient), nil
	case "webhook":
		return webhook.New().WithHTTPClient(eventing.GuardedClient(eventingAllowLoopback(envconfig.Get))), nil
	case "email":
		guarded, ok := eventing.GuardedClient(eventingAllowLoopback(envconfig.Get)).Transport.(*http.Transport)
		if !ok {
			return nil, errors.New("notify: the egress guard has no dialer for SMTP")
		}
		return email.New().WithDialer(guarded.DialContext), nil
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

func notifySecretName(name, field string) string { return "notify/" + name + "/" + field }

func notifySecretRef(name, field string) string {
	return secret.SchemeStore + ":" + notifySecretName(name, field)
}

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

// checkPublicAddress resolves where a webhook or an SMTP server is and refuses a
// private or reserved address with the remedy. The egress guard refuses the same
// addresses at connect time; this says why while the administrator is still typing
// it. Loopback passes only in the guard's own development posture.
func checkPublicAddress(ctx context.Context, setting, rawHost string, port int) error {
	host, err := egress.CanonicalHost(rawHost)
	if err != nil {
		return invalidNotifyDestination("%s is not a valid host name", setting)
	}
	dest := egress.Destination{Host: host, Port: port, IP: net.ParseIP(host)}
	ips, err := egress.Resolve(ctx, egress.NetResolver{}, dest)
	if err != nil || len(ips) == 0 {
		return invalidNotifyDestination("the %s host %s does not resolve", setting, host)
	}
	allowLoopback := eventingAllowLoopback(envconfig.Get)
	for _, ip := range ips {
		if ip.IsLoopback() && allowLoopback {
			continue
		}
		if egress.ReservedAddress(ip) {
			return invalidNotifyDestination("the %s host %s resolves to %s, a private or reserved address that a destination added here may not reach; provision an internal destination in the operator file (OLIVARES_NOTIFY_CONFIG)", setting, host, ip)
		}
	}
	return nil
}

// checkAddress validates the setting that says where a destination sends and returns
// its host for the list. Slack and Teams keep their vendor rule; a webhook is an
// https URL (http only to loopback in the development posture) and an SMTP server a
// host, both on a public address.
func checkAddress(ctx context.Context, kind notifyConsoleKind, settings map[string]string) (string, error) {
	raw := settings[kind.address]
	switch {
	case kind.hosts != nil:
		return validateNotifyWebhook(kind, raw)
	case kind.address == "url":
		dest, err := egress.ParseDestination(raw)
		if err != nil {
			return "", invalidNotifyDestination("url must be an https URL without credentials")
		}
		if dest.Scheme != "https" && !(eventingAllowLoopback(envconfig.Get) && dest.IP != nil && dest.IP.IsLoopback()) {
			return "", invalidNotifyDestination("url must be an https URL")
		}
		return dest.Host, checkPublicAddress(ctx, "url", dest.Host, dest.Port)
	case kind.address == "smtp_host":
		port := 587
		if p := settings["smtp_port"]; p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 65535 {
				return "", invalidNotifyDestination("smtp_port must be a port number")
			}
			port = n
		}
		return strings.ToLower(raw), checkPublicAddress(ctx, "smtp_host", raw, port)
	}
	return "", nil
}

// notifyKindFields describes the settings of a kind from its connector's descriptor:
// which are secret, which are required and what each means.
func notifyKindFields(kindName string, kind notifyConsoleKind) []api.NotifyDestinationField {
	conn, err := buildOutputConnector(kindName)
	if err != nil {
		return nil
	}
	declared := map[string]sdk.ConfigField{}
	for _, f := range conn.Descriptor().ConfigFields {
		declared[f.Key] = f
	}
	out := make([]api.NotifyDestinationField, 0, len(kind.fields))
	for _, key := range kind.fields {
		f := declared[key]
		out = append(out, api.NotifyDestinationField{Key: key, Secret: f.Secret, Required: f.Required || key == kind.address, Description: f.Description})
	}
	return out
}

// typedSettings collects the settings an administrator typed (webhook_url is the
// published Slack and Teams field) and checks each is one the kind accepts. A value
// is typed, never a reference: the resolver would otherwise open `env:`, `file:` or
// another `store:` secret into a destination.
func typedSettings(kindName string, fields []api.NotifyDestinationField, in api.NotifyDestinationInput) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in.Settings {
		out[k] = strings.TrimSpace(v)
	}
	if u := strings.TrimSpace(in.WebhookURL); u != "" {
		if prev, ok := out[notifyWebhookField]; ok && prev != u {
			return nil, invalidNotifyDestination("webhook_url is given twice with different values")
		}
		out[notifyWebhookField] = u
	}
	for k, v := range out {
		i := slices.IndexFunc(fields, func(f api.NotifyDestinationField) bool { return f.Key == k })
		if i < 0 {
			keys := make([]string, 0, len(fields))
			for _, f := range fields {
				keys = append(keys, f.Key)
			}
			return nil, invalidNotifyDestination("a %s destination has no setting %q; it takes %s", kindName, k, strings.Join(keys, ", "))
		}
		if _, isRef := secret.ParseReference(v); isRef && !fields[i].Secret {
			return nil, invalidNotifyDestination("%s must be a value, not a secret reference", k)
		}
		// A line break in from_name would add mail headers; no setting needs one.
		if !fields[i].Secret && strings.ContainsFunc(v, unicode.IsControl) {
			return nil, invalidNotifyDestination("%s must not contain control characters", k)
		}
	}
	return out, nil
}

// missingSetting names the first required setting cfg lacks, or "".
func missingSetting(fields []api.NotifyDestinationField, cfg map[string]string) string {
	for _, f := range fields {
		if f.Required && cfg[f.Key] == "" {
			return f.Key
		}
	}
	return ""
}

// identity validates the kind and the name; the name must not belong to the operator file.
func (s *notifyDestinationService) identity(in api.NotifyDestinationInput) (string, string, notifyConsoleKind, error) {
	kindName := strings.TrimSpace(in.Kind)
	kind, ok := notifyConsoleKinds[kindName]
	if !ok {
		return "", "", notifyConsoleKind{}, invalidNotifyDestination("the kind must be %s", notifyConsoleKindNames)
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

// List returns the operator file's and the added destinations, sorted by name, and
// the kinds that can be added with their settings.
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
		out.Kinds = append(out.Kinds, api.NotifyDestinationKind{Kind: kind, Title: k.title, Fields: notifyKindFields(kind, k)})
	}
	slices.SortFunc(out.Kinds, func(a, b api.NotifyDestinationKind) int { return strings.Compare(a.Kind, b.Kind) })
	return out, nil
}

// sameAddress reports whether typed leaves the destination where stored sends: its
// address setting is blank or unchanged. A stored secret belongs to the address it was
// typed for, so it is kept or resolved only then; a moved destination needs its
// secrets again.
func sameAddress(kind notifyConsoleKind, typed, stored map[string]string) bool {
	a := typed[kind.address]
	return a == "" || a == stored[kind.address]
}

// Put seals the typed secret settings, stores the definition and opens the destination.
// A secret left blank keeps the stored value; when the address changes, every stored
// secret must be given again. Any other setting left out is cleared.
func (s *notifyDestinationService) Put(ctx context.Context, actor auth.Principal, in api.NotifyDestinationInput) (api.NotifyDestination, error) {
	name, kindName, kind, err := s.identity(in)
	if err != nil {
		return api.NotifyDestination{}, err
	}
	fields := notifyKindFields(kindName, kind)
	typed, err := typedSettings(kindName, fields, in)
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
	// The definition: typed settings, and the stored reference of each secret left blank.
	keep := sameAddress(kind, typed, existing.Config)
	config := map[string]string{}
	var seal []string
	for _, f := range fields {
		v := typed[f.Key]
		switch {
		case v != "" && f.Secret:
			config[f.Key] = notifySecretRef(name, f.Key)
			seal = append(seal, f.Key)
		case v != "":
			config[f.Key] = v
		case f.Secret && found && existing.Config[f.Key] != "":
			// Neither carried to a new address nor dropped in silence, which would send
			// unauthenticated or unsigned from then on.
			if !keep {
				return api.NotifyDestination{}, invalidNotifyDestination("%s changed: enter %s again; a stored secret is not sent to a new address", kind.address, f.Key)
			}
			config[f.Key] = existing.Config[f.Key]
		}
	}
	if missing := missingSetting(fields, config); missing != "" {
		return api.NotifyDestination{}, invalidNotifyDestination("a %s destination needs %s", kindName, missing)
	}
	host := existing.WebhookHost
	if kind.address != "" && typed[kind.address] != "" {
		if host, err = checkAddress(ctx, kind, typed); err != nil {
			return api.NotifyDestination{}, err
		}
	}
	if len(seal) > 0 && s.secrets == nil {
		return api.NotifyDestination{}, auth.ErrNoSecretSealer
	}
	// unseal runs when the definition is not saved. A new destination must not leave
	// sealed values that nothing lists and nothing can delete; an update keeps the sealed
	// values it just replaced: the old ones are gone, so say so.
	unseal := func(sealed []string) {
		for _, key := range sealed {
			if found {
				s.log.Error("notify destinations: the destination was not saved but its sealed setting was replaced; save it again", "name", name, "setting", key)
			} else if derr := s.secrets.Delete(ctx, actor, auth.GlobalSecretScope, notifySecretName(name, key)); derr != nil {
				s.log.Error("notify destinations: the destination was not saved and its sealed setting could not be removed", "name", name, "setting", key, "err", derr)
			}
		}
	}
	for i, key := range seal {
		desc := fmt.Sprintf("notification destination %q %s", name, key)
		if _, perr := s.secrets.Put(ctx, actor, auth.GlobalSecretScope, notifySecretName(name, key), typed[key], desc); perr != nil {
			unseal(seal[:i])
			return api.NotifyDestination{}, perr
		}
	}
	def := storedNotifyDestination{Name: name, Kind: kindName, Config: config, WebhookHost: host}
	if err := s.settings.saveNotifyDestination(ctx, actor, def); err != nil {
		unseal(seal)
		return api.NotifyDestination{}, err
	}
	live := s.dispatch.applyStored(ctx, def.spec(), s.resolver, s.log) == nil
	if !live {
		// The stored settings are the new ones. Leaving the connector built from the old
		// ones serving would deliver to the place being rotated away while the list says
		// it is not open, so the destination closes until it is saved again.
		s.dispatch.removeStored(ctx, name)
	}
	return api.NotifyDestination{Name: name, Kind: kindName, Source: api.NotifyDestinationSourceConsole, Live: live, WebhookHost: host}, nil
}

// Delete removes an added destination, closes it and deletes its sealed settings.
func (s *notifyDestinationService) Delete(ctx context.Context, actor auth.Principal, name string) error {
	name = strings.TrimSpace(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, found, err := s.find(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		return api.ErrNotifyDestinationNotFound
	}
	// The sealed settings go first: a failure leaves the definition in place, so the
	// same request can be repeated. The other order leaves secrets nothing lists.
	if s.secrets != nil {
		for key, ref := range stored.Config {
			if ref != notifySecretRef(name, key) {
				continue
			}
			if derr := s.secrets.Delete(ctx, actor, auth.GlobalSecretScope, notifySecretName(name, key)); derr != nil && !errors.Is(derr, auth.ErrSecretNotFound) {
				return derr
			}
		}
	}
	if err := s.settings.deleteNotifyDestination(ctx, actor, name); err != nil {
		return err
	}
	// A name the operator file also defines is the file's: its connector is not closed.
	s.dispatch.removeStored(ctx, name)
	return nil
}

// Test sends one test message without saving or wiring anything: through the typed
// settings, over the stored ones of that name. A blank kind is the stored one's.
func (s *notifyDestinationService) Test(ctx context.Context, _ auth.Principal, in api.NotifyDestinationInput) error {
	stored, found, err := s.find(ctx, strings.TrimSpace(in.Name))
	if err != nil {
		return err
	}
	kindName := strings.TrimSpace(in.Kind)
	if kindName == "" && found {
		kindName = stored.Kind
	}
	kind, ok := notifyConsoleKinds[kindName]
	if !ok {
		return invalidNotifyDestination("the kind must be %s", notifyConsoleKindNames)
	}
	fields := notifyKindFields(kindName, kind)
	typed, err := typedSettings(kindName, fields, in)
	if err != nil {
		return err
	}
	if kind.address != "" && typed[kind.address] != "" {
		if _, err := checkAddress(ctx, kind, typed); err != nil {
			return err
		}
	}
	conn, err := s.connect(kindName)
	if err != nil {
		return invalidNotifyDestination("the kind must be %s", notifyConsoleKindNames)
	}
	cfg := map[string]string{}
	if found && stored.Kind == kindName && sameAddress(kind, typed, stored.Config) {
		resolved, rerr := resolveConfig(ctx, s.resolver, conn.Descriptor(), sdk.Config{Settings: stored.Config})
		if rerr != nil {
			s.log.Warn("notify destinations: the stored settings cannot be read", "name", stored.Name, "err", rerr)
			return invalidNotifyDestination("the stored settings cannot be read; enter them again")
		}
		cfg = resolved.Settings
	}
	for k, v := range typed {
		if v != "" {
			cfg[k] = v
		}
	}
	if missing := missingSetting(fields, cfg); missing != "" {
		return invalidNotifyDestination("enter %s to test", missing)
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTestTimeout)
	defer cancel()
	defer func() { _ = conn.Close(ctx) }()
	// An Open error can embed a typed value: it is never logged or returned. Neither
	// error from a failed test is returned, because the administrator's answer is the
	// generic ErrNotifyDestinationTestFailed.
	if oerr := conn.Open(ctx, sdk.Config{Settings: cfg}); oerr != nil {
		s.log.Debug("notify destinations: test open failed", "kind", kindName)
		return api.ErrNotifyDestinationTestFailed
	}
	// The connectors redact their credential from what they return (their
	// error_redaction tests), so the status or the network failure is safe to log:
	// it is what tells an administrator a revoked credential from an unreachable host.
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
