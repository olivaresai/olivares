// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// providerwire.go is the composition-root half of the provider records.
//
// It supplies the two things the sessions module declares and is deliberately not
// allowed to be:
//
//   - the VAULT, over the engine's own sealed secret store. The module holds no
//     store handle for the auth partition and no key material, which is what makes
//     "a module can never read an operator secret" true rather than aspirational.
//     Sealing here keeps that sentence true and reuses the sealer, the AAD binding
//     and the audit trail the engine already has.
//   - the PROBE, an outbound HTTPS call that lists a provider's models. It lives
//     here and not in the module for the same reason every other egress does: a
//     module describes what it needs, the root decides what may leave the box.
//
// Both are wired only when their dependency exists. With no sealer the vault is NOT
// wired, so registering a provider is refused with the wiring named — the engine
// never stores a credential it cannot protect, and it says so.

// providerSecretVault seals provider credentials in the engine's runtime secret
// store, under a name derived from the record's own reference.
//
// The SCOPE is the record's TENANT and not the global scope the operator's own
// secrets use, and that is a decision with two consequences worth stating. First,
// the sealer binds the scope as AAD, so a tenant's sealed provider credential does
// not open under another tenant's scope even with the same key. Second, these
// entries do not appear in the operator's global secret list, which is correct:
// they are not operator-authored references, they are product state with their own
// CRUD, their own permission tier and their own rotation.
type providerSecretVault struct{ store *auth.SecretStore }

// Seal stores (or replaces) one provider credential and returns its locator.
//
// The locator IS the name. It is returned rather than assumed so the port stays
// honest about who owns the addressing: a future vault backed by an external
// secret manager would return that manager's own handle, and nothing above here
// would change.
func (v providerSecretVault) Seal(
	ctx context.Context,
	actor auth.Principal,
	tenant model.TenantID,
	name string,
	value []byte,
) (string, error) {
	if v.store == nil {
		return "", errors.New("provider vault: no secret store")
	}
	if _, err := v.store.Put(ctx, actor, tenant, name, string(value),
		"provider credential registered through the sessions provider plane"); err != nil {
		return "", err
	}
	return name, nil
}

// Open returns the sealed credential for one launch or one connection test.
func (v providerSecretVault) Open(ctx context.Context, tenant model.TenantID, locator string) ([]byte, error) {
	if v.store == nil {
		return nil, errors.New("provider vault: no secret store")
	}
	return v.store.Resolve(ctx, tenant, locator)
}

// Revoke destroys the sealed credential. A value that is already gone is success:
// the caller asked for a postcondition, and the postcondition holds.
func (v providerSecretVault) Revoke(
	ctx context.Context,
	actor auth.Principal,
	tenant model.TenantID,
	locator string,
) error {
	if v.store == nil {
		return errors.New("provider vault: no secret store")
	}
	err := v.store.Delete(ctx, actor, tenant, locator)
	if errors.Is(err, auth.ErrSecretNotFound) {
		return nil
	}
	return err
}

// --- the connection probe -----------------------------------------------------

// Probe bounds. They are small on purpose: this is a diagnostic an operator waits
// on with a spinner, not a job. A provider that cannot answer a model list in ten
// seconds is reported as unreachable, which is the honest answer for a console.
const (
	providerProbeTimeout  = 10 * time.Second
	providerProbeBodyCap  = 1 << 20 // 1 MiB of model list is already absurd
	providerProbeMaxHops  = 3
	providerProbeUserData = "olivares-provider-test"
)

// Official endpoints per kind. openai_compatible has none by design: a record of
// that kind carries its own, and assuming one would be assuming somebody's
// deployment.
const (
	anthropicDefaultBase = "https://api.anthropic.com"
	openaiDefaultBase    = "https://api.openai.com"
	xaiDefaultBase       = "https://api.x.ai"
)

// anthropicVersionHeader is the API version the models list is requested under.
// It is pinned rather than omitted because Anthropic's API refuses a request with
// no version, and a refusal for a missing header would be reported to the operator
// as a refused CREDENTIAL, which is the wrong remedy.
const anthropicVersionHeader = "2023-06-01"

// providerProbe asks a provider which models it serves.
//
// ⛔ IT NEVER SENDS A COMPLETION. The whole surface is one GET of a model list.
// A test that generated a token would spend the operator's money, on a model
// nobody chose, to answer a question the list already answers: can this endpoint
// be reached, and does it accept this credential. The HTTP client below has no
// method other than GET reachable from here, which is the mechanical form of that
// promise.
type providerProbe struct{ client *http.Client }

func newProviderProbe() providerProbe {
	// cli-transport-exempt: this is not the CLI talking to a control plane. `cliTransport`
	// carries the OPERATOR's --ca-cert, --pin-sha256 and --insecure for the one host they
	// chose to trust as their engine; this is the ENGINE reaching a THIRD PARTY the tenant
	// named, over the public web PKI, from inside the server process. Borrowing the
	// operator's pins for it would mean a pin set for their control plane silently decides
	// whether a provider's certificate is acceptable — two different trust questions
	// answered by one anchor. The hardening this path does need is here and not there:
	// a bounded timeout, a redirect cap, and a refusal to follow a hop to another host.
	return providerProbe{client: &http.Client{
		Timeout: providerProbeTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= providerProbeMaxHops {
				return errors.New("too many redirects")
			}
			// A redirect that leaves the host the operator registered would send the
			// credential somewhere else. The Go client already strips Authorization
			// across hosts, and this refuses the hop as well: a silently unauthenticated
			// request to a third host would be reported as a refused credential.
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("refusing a redirect to another host")
			}
			return nil
		},
	}}
}

// modelsURL builds the model-list URL of one kind. Every one of the four is the
// provider's own documented listing path.
func modelsURL(kind, baseURL string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch kind {
	case sessions.ProviderKindAnthropic:
		if base == "" {
			base = anthropicDefaultBase
		}
		return base + "/v1/models", nil
	case sessions.ProviderKindOpenAI:
		if base == "" {
			base = openaiDefaultBase
		}
		return base + "/v1/models", nil
	case sessions.ProviderKindXAI:
		if base == "" {
			base = xaiDefaultBase
		}
		return base + "/v1/models", nil
	case sessions.ProviderKindOpenAICompatible:
		if base == "" {
			return "", errors.New("an openai_compatible provider has no endpoint to test")
		}
		return base + "/v1/models", nil
	}
	return "", fmt.Errorf("unknown provider kind")
}

// authHeaders sets the credential header each kind documents. Anthropic uses
// x-api-key plus a version; the other three are bearer.
func authHeaders(req *http.Request, kind, key string) {
	switch kind {
	case sessions.ProviderKindAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicVersionHeader)
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

// Probe performs the model-list call and classifies the answer.
//
// The classification is the product of this function, not the model list: 401 and
// 403 are the PROVIDER's verdict on the credential and are reported as a refusal;
// everything else — a timeout, a DNS failure, a 5xx, a body that is not the shape
// the provider documents — is reported as unreachable, because none of them is
// evidence about the key.
func (p providerProbe) Probe(ctx context.Context, req sessions.ProviderProbeRequest) (sessions.ProviderProbeResult, error) {
	url, err := modelsURL(req.Kind, req.BaseURL)
	if err != nil {
		return sessions.ProviderProbeResult{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return sessions.ProviderProbeResult{}, err
	}
	authHeaders(hreq, req.Kind, req.APIKey)
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Set("User-Agent", providerProbeUserData+"/"+version)
	resp, err := p.client.Do(hreq)
	if err != nil {
		// The transport error can embed the URL. It cannot embed the key — the key
		// is a header, never a query parameter, which is why modelsURL builds a path
		// and nothing else.
		return sessions.ProviderProbeResult{}, fmt.Errorf("the provider endpoint could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return sessions.ProviderProbeResult{}, fmt.Errorf(
			"%w: the provider answered %d for this credential", sessions.ErrProviderRefused, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		// A 404 on a model list is an ENDPOINT answer, not a credential one. An
		// openai_compatible deployment that serves no /v1/models is the common case,
		// and calling it a bad key would send the operator to regenerate one.
		return sessions.ProviderProbeResult{}, fmt.Errorf(
			"the endpoint answered 404 for the model list; the credential was not tested")
	case resp.StatusCode >= 400:
		return sessions.ProviderProbeResult{}, fmt.Errorf("the provider answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, providerProbeBodyCap))
	if err != nil {
		return sessions.ProviderProbeResult{}, fmt.Errorf("the provider's answer could not be read")
	}
	models := parseModelList(body)
	detail := fmt.Sprintf("%d models listed", len(models))
	if len(models) == 0 {
		detail = "the provider accepted the credential and listed no models"
	}
	return sessions.ProviderProbeResult{Models: models, Detail: detail}, nil
}

// parseModelList reads the one shape all four kinds share: `{"data":[{"id":...}]}`.
//
// It returns what it could read rather than failing on an unexpected extra field,
// because the operator's question was "does this work", and a provider that adds a
// field to its catalogue has not broken their credential.
func parseModelList(body []byte) []string {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	out := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
