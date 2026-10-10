// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/connectors/local"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
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
// ⛔ IT NEVER SENDS A COMPLETION. The whole surface is bounded GETs of a model list.
// Ollama uses the local connector's model list and read-only show metadata.
// A test that generated a token would spend the operator's money, on a model
// nobody chose, to answer a question the list already answers: can this endpoint
// be reached, and does it accept this credential. The HTTP client below has no
// method other than GET reachable from here, which is the mechanical form of that
// promise.
type providerProbe struct{ client *http.Client }

type providerProbeModel struct {
	ID string `json:"id"`
}

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
			if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("refusing a redirect that downgrades HTTPS")
			}
			return nil
		},
	}}
}

// modelsURL builds the model-list URL of one kind. Each uses the
// provider's own documented listing path.
func modelsURL(kind, baseURL string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch kind {
	case sessions.ProviderKindGemini:
		if base != "" {
			return "", errors.New("Gemini providers cannot override Google's API address")
		}
		return "https://generativelanguage.googleapis.com/v1beta/models", nil
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
			return xaiDefaultBase + "/v1/models", nil
		}
		// Grok Build is launched on base_url as registered, with its /v1 like the
		// vendor default (providerVendorEndpoints), so the probe asks that address.
		return base + "/models", nil
	case sessions.ProviderKindOpenAICompatible:
		if base == "" {
			return "", errors.New("an openai_compatible provider has no endpoint to test")
		}
		return base + "/models", nil
	}
	return "", fmt.Errorf("unknown provider kind")
}

// authHeaders sets the credential header each kind documents. Anthropic uses
// x-api-key plus a version; the other three are bearer.
func authHeaders(req *http.Request, kind, key string) {
	switch kind {
	case sessions.ProviderKindGemini:
		req.Header.Set("x-goog-api-key", key)
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
	return p.ProbeService(ctx, req, "")
}

func (p providerProbe) ProbeService(ctx context.Context, req sessions.ProviderProbeRequest, service string) (sessions.ProviderProbeResult, error) {
	if service != "" && (service != modelprovider.ServiceDeepSeek || req.Kind != sessions.ProviderKindOpenAICompatible || req.BaseURL != modelprovider.DeepSeekBaseURL) {
		return sessions.ProviderProbeResult{}, errors.New("unsupported provider service or endpoint")
	}
	ctx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
	defer cancel()
	if req.Kind == sessions.ProviderKindOllama {
		source := local.NewWithClient(providerMetadataClient{p.client})
		if err := source.Open(ctx, sdk.Config{Settings: map[string]string{"ollama_url": req.BaseURL, "vllm_url": ""}}); err != nil {
			return sessions.ProviderProbeResult{}, err
		}
		defer source.Close(ctx)
		catalog, err := source.Snapshot(ctx)
		if err != nil {
			return sessions.ProviderProbeResult{}, fmt.Errorf("the local model endpoint could not be read")
		}
		models := make([]string, 0, len(catalog.Models))
		for _, model := range catalog.Models {
			models = append(models, model.Ref)
		}
		sort.Strings(models)
		return sessions.ProviderProbeResult{Models: models, Detail: fmt.Sprintf("%d local models listed", len(models))}, nil
	}
	endpoint, err := modelsURL(req.Kind, req.BaseURL)
	if service == modelprovider.ServiceDeepSeek {
		endpoint, err = modelprovider.DeepSeekModelsURL, nil
	}
	if err != nil {
		return sessions.ProviderProbeResult{}, err
	}
	client := *p.client
	if service != "" {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	var models []string
	for page := 0; page < 10; page++ {
		hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return sessions.ProviderProbeResult{}, err
		}
		authHeaders(hreq, req.Kind, req.APIKey)
		hreq.Header.Set("Accept", "application/json")
		hreq.Header.Set("User-Agent", providerProbeUserData+"/"+version)
		resp, err := client.Do(hreq)
		if err != nil {
			// The transport error can embed the URL. It cannot embed the key — the key
			// is a header, never a query parameter, which is why modelsURL builds a path
			// and nothing else.
			return sessions.ProviderProbeResult{}, fmt.Errorf("the provider endpoint could not be reached")
		}
		switch {
		case service != "" && resp.StatusCode >= 300 && resp.StatusCode < 400:
			_ = resp.Body.Close()
			return sessions.ProviderProbeResult{}, errors.New("provider service redirects are refused")
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			_ = resp.Body.Close()
			return sessions.ProviderProbeResult{}, fmt.Errorf(
				"%w: the provider answered %d for this credential", sessions.ErrProviderRefused, resp.StatusCode)
		case resp.StatusCode == http.StatusNotFound:
			_ = resp.Body.Close()
			// A 404 on a model list is an ENDPOINT answer, not a credential one. An
			// openai_compatible deployment that serves no /v1/models is the common case,
			// and calling it a bad key would send the operator to regenerate one.
			return sessions.ProviderProbeResult{}, fmt.Errorf(
				"the endpoint answered 404 for the model list; the credential was not tested")
		case resp.StatusCode < 200 || resp.StatusCode >= 300:
			_ = resp.Body.Close()
			return sessions.ProviderProbeResult{}, fmt.Errorf("the provider answered %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, providerProbeBodyCap+1))
		_ = resp.Body.Close()
		if err != nil {
			return sessions.ProviderProbeResult{}, fmt.Errorf("the provider's answer could not be read")
		}
		if len(body) > providerProbeBodyCap {
			return sessions.ProviderProbeResult{}, errors.New("the provider model list exceeded its response bound")
		}
		var payload struct {
			Data   []providerProbeModel `json:"data"`
			Models []struct {
				Name    string   `json:"name"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
			HasMore       bool   `json:"has_more"`
			LastID        string `json:"last_id"`
		}
		if json.Unmarshal(body, &payload) != nil || req.Kind == sessions.ProviderKindGemini && payload.Models == nil || req.Kind != sessions.ProviderKindGemini && payload.Data == nil {
			return sessions.ProviderProbeResult{}, errors.New("the provider returned an invalid model list")
		}
		if req.Kind == sessions.ProviderKindGemini {
			for _, md := range payload.Models {
				if !strings.HasPrefix(md.Name, "models/") {
					return sessions.ProviderProbeResult{}, errors.New("the provider returned an invalid model name")
				}
				for _, method := range md.Methods {
					if method == "generateContent" {
						payload.Data = append(payload.Data, providerProbeModel{ID: strings.TrimPrefix(md.Name, "models/")})
						break
					}
				}
			}
			payload.HasMore, payload.LastID = payload.NextPageToken != "", payload.NextPageToken
		}
		for _, md := range payload.Data {
			if !validProviderModelID(md.ID) {
				return sessions.ProviderProbeResult{}, errors.New("the provider returned an invalid model list")
			}
			if req.APIKey != "" && strings.Contains(md.ID, req.APIKey) {
				return sessions.ProviderProbeResult{}, errors.New("the provider returned unsafe model metadata")
			}
			models = append(models, md.ID)
		}
		if len(models) > 1000 {
			return sessions.ProviderProbeResult{}, errors.New("the provider model catalog exceeded its model bound")
		}
		if !payload.HasMore {
			sort.Strings(models)
			detail := fmt.Sprintf("%d models listed", len(models))
			if len(models) == 0 {
				detail = "the provider accepted the credential and listed no models"
			}
			return sessions.ProviderProbeResult{Models: models, Detail: detail}, nil
		}
		if (req.Kind != sessions.ProviderKindAnthropic && req.Kind != sessions.ProviderKindGemini) || payload.LastID == "" || len(payload.LastID) > 4096 {
			return sessions.ProviderProbeResult{}, errors.New("the provider model list cannot be paged")
		}
		if req.APIKey != "" && strings.Contains(payload.LastID, req.APIKey) {
			return sessions.ProviderProbeResult{}, errors.New("the provider returned unsafe model metadata")
		}
		next, parseErr := url.Parse(endpoint)
		if parseErr != nil {
			return sessions.ProviderProbeResult{}, parseErr
		}
		query := next.Query()
		cursor := "after_id"
		if req.Kind == sessions.ProviderKindGemini {
			cursor = "pageToken"
		}
		if query.Get(cursor) == payload.LastID {
			return sessions.ProviderProbeResult{}, errors.New("the provider model cursor did not advance")
		}
		query.Set(cursor, payload.LastID)
		if req.Kind == sessions.ProviderKindGemini {
			query.Set("pageSize", "100")
		} else {
			query.Set("limit", "100")
		}
		next.RawQuery = query.Encode()
		endpoint = next.String()
	}
	return sessions.ProviderProbeResult{}, errors.New("the provider model catalog exceeded its page bound")
}

func validProviderModelID(id string) bool {
	return id != "" && len(id) <= 200 && strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

// providerMetadataClient bounds a native metadata response before the connector
// decodes it. It retains the same timeout, host/redirect policy and auth behavior.
type providerMetadataClient struct{ client *http.Client }

func (c providerMetadataClient) Do(req *http.Request) (*http.Response, error) {
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, providerProbeBodyCap+1))
	if err != nil {
		return nil, err
	}
	if len(body) > providerProbeBodyCap {
		return nil, errors.New("native metadata response exceeds its bound")
	}
	// Validate the native list before the connector fans out metadata reads.
	// Missing/null lists are malformed; an explicit empty list is successful.
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/api/tags") && response.StatusCode >= 200 && response.StatusCode < 300 {
		var catalog struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.Unmarshal(body, &catalog) != nil || catalog.Models == nil || len(catalog.Models) > 1000 {
			return nil, errors.New("the provider returned an invalid model list")
		}
		for _, entry := range catalog.Models {
			if !validProviderModelID(entry.Name) {
				return nil, errors.New("the provider returned an invalid model list")
			}
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}
