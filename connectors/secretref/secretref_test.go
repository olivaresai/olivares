// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package secretref

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// fakeDoer routes a request to a handler that returns a canned response, so a test
// can assert the exact request shape and supply the backend's response body.
type fakeDoer struct {
	fn func(*http.Request) (int, string, error)
}

func (d fakeDoer) Do(req *http.Request) (*http.Response, error) {
	code, body, err := d.fn(req)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func mustResolve(t *testing.T, r Reader, ok bool, locator string) string {
	t.Helper()
	if !ok || r == nil {
		t.Fatal("reader not configured")
	}
	v, err := r.Resolve(context.Background(), locator)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", locator, err)
	}
	return string(v)
}

func TestVaultReaderKVv2(t *testing.T) {
	doer := fakeDoer{fn: func(req *http.Request) (int, string, error) {
		if req.Method != http.MethodGet {
			t.Errorf("method = %s", req.Method)
		}
		if req.URL.Path != "/v1/secret/data/gdrive" {
			t.Errorf("path = %s", req.URL.Path)
		}
		if req.Header.Get("X-Vault-Token") != "tok-123" {
			t.Errorf("missing X-Vault-Token: %q", req.Header.Get("X-Vault-Token"))
		}
		return 200, `{"data":{"data":{"token":"s3cr3t","other":"x"},"metadata":{}}}`, nil
	}}
	r, ok := newVaultReader(env(map[string]string{
		"VAULT_ADDR":                     "https://vault.example:8200",
		"OLIVARES_SECRETREF_VAULT_TOKEN": "tok-123",
	}), doer)
	if got := mustResolve(t, r, ok, "secret/data/gdrive#token"); got != "s3cr3t" {
		t.Errorf("value = %q", got)
	}
}

func TestVaultReaderKVv1AndSingleField(t *testing.T) {
	doer := fakeDoer{fn: func(_ *http.Request) (int, string, error) {
		return 200, `{"data":{"password":"hunter2"}}`, nil
	}}
	r, ok := newVaultReader(env(map[string]string{
		"VAULT_ADDR":  "https://v.example",
		"VAULT_TOKEN": "t",
	}), doer)
	// No #key: a single-field secret returns its sole value.
	if got := mustResolve(t, r, ok, "kv/myapp"); got != "hunter2" {
		t.Errorf("v1 single field = %q", got)
	}
}

// TestVaultReaderKVv1WithDataField is the regression for the v1-vs-v2 detection
// bug: a KV v1 secret that itself carries a field literally named "data" holding
// an object must be read as v1 (no metadata sibling), not mistaken for a v2
// envelope that would silently hide the other fields.
func TestVaultReaderKVv1WithDataField(t *testing.T) {
	doer := fakeDoer{fn: func(_ *http.Request) (int, string, error) {
		// v1 response: the secret's own fields directly under data, one named "data".
		return 200, `{"data":{"data":{"inner":"x"},"password":"hunter2"}}`, nil
	}}
	r, ok := newVaultReader(env(map[string]string{"VAULT_ADDR": "https://v.example", "VAULT_TOKEN": "t"}), doer)
	if got := mustResolve(t, r, ok, "kv/myapp#password"); got != "hunter2" {
		t.Errorf("v1 secret with a 'data' field: password = %q, want hunter2 (must not be read as a v2 envelope)", got)
	}
}

func TestGCPReader(t *testing.T) {
	doer := fakeDoer{fn: func(req *http.Request) (int, string, error) {
		want := "/v1/projects/my-proj/secrets/gdrive/versions/latest:access"
		if req.URL.Path != want {
			t.Errorf("path = %s want %s", req.URL.Path, want)
		}
		if req.Header.Get("Authorization") != "Bearer gtok" {
			t.Errorf("auth = %q", req.Header.Get("Authorization"))
		}
		// base64("s3cr3t") = czNjcjN0
		return 200, `{"payload":{"data":"czNjcjN0"}}`, nil
	}}
	r, ok := newGCPReader(env(map[string]string{
		"OLIVARES_SECRETREF_GCP_PROJECT": "my-proj",
		"OLIVARES_SECRETREF_GCP_TOKEN":   "gtok",
	}), doer)
	if got := mustResolve(t, r, ok, "gdrive"); got != "s3cr3t" {
		t.Errorf("value = %q", got)
	}
}

func TestAzureReader(t *testing.T) {
	doer := fakeDoer{fn: func(req *http.Request) (int, string, error) {
		if req.URL.Host != "mykv.vault.azure.net" {
			t.Errorf("host = %s", req.URL.Host)
		}
		if req.URL.Path != "/secrets/gdrive" {
			t.Errorf("path = %s", req.URL.Path)
		}
		if req.URL.Query().Get("api-version") != "7.4" {
			t.Errorf("api-version = %s", req.URL.Query().Get("api-version"))
		}
		return 200, `{"value":"az-secret","id":"https://mykv.vault.azure.net/secrets/gdrive/abc"}`, nil
	}}
	r, ok := newAzureReader(env(map[string]string{"OLIVARES_SECRETREF_AZURE_TOKEN": "atok"}), doer)
	if got := mustResolve(t, r, ok, "mykv/gdrive"); got != "az-secret" {
		t.Errorf("value = %q", got)
	}
}

func TestInfisicalReader(t *testing.T) {
	doer := fakeDoer{fn: func(req *http.Request) (int, string, error) {
		if req.URL.Path != "/api/v3/secrets/raw/GDRIVE_TOKEN" {
			t.Errorf("path = %s", req.URL.Path)
		}
		q := req.URL.Query()
		if q.Get("workspaceId") != "ws1" || q.Get("environment") != "prod" {
			t.Errorf("query = %v", q)
		}
		return 200, `{"secret":{"secretKey":"GDRIVE_TOKEN","secretValue":"inf-secret"}}`, nil
	}}
	r, ok := newInfisicalReader(env(map[string]string{
		"OLIVARES_SECRETREF_INFISICAL_TOKEN":        "itok",
		"OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID": "ws1",
		"OLIVARES_SECRETREF_INFISICAL_ENV":          "prod",
	}), doer)
	if got := mustResolve(t, r, ok, "GDRIVE_TOKEN"); got != "inf-secret" {
		t.Errorf("value = %q", got)
	}
}

func TestAWSReaderStringAndJSONKey(t *testing.T) {
	doer := fakeDoer{fn: func(req *http.Request) (int, string, error) {
		if req.Method != http.MethodPost {
			t.Errorf("method = %s", req.Method)
		}
		if req.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" {
			t.Errorf("x-amz-target = %q", req.Header.Get("X-Amz-Target"))
		}
		if !strings.HasPrefix(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("missing SigV4 Authorization: %q", req.Header.Get("Authorization"))
		}
		if req.Header.Get("X-Amz-Date") == "" {
			t.Error("missing X-Amz-Date")
		}
		return 200, `{"Name":"prod/gdrive","SecretString":"{\"token\":\"aws-secret\"}"}`, nil
	}}
	cfg := env(map[string]string{
		"AWS_REGION":            "eu-west-1",
		"AWS_ACCESS_KEY_ID":     "AKIA",
		"AWS_SECRET_ACCESS_KEY": "shh",
	})
	r, ok := newAWSReader(cfg, doer)
	if got := mustResolve(t, r, ok, "prod/gdrive#token"); got != "aws-secret" {
		t.Errorf("jsonkey value = %q", got)
	}
}

func TestK8sReader(t *testing.T) {
	doer := fakeDoer{fn: func(req *http.Request) (int, string, error) {
		if req.URL.Path != "/api/v1/namespaces/ns1/secrets/gdrive" {
			t.Errorf("path = %s", req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer sa-token" {
			t.Errorf("auth = %q", req.Header.Get("Authorization"))
		}
		// base64("k8s-secret") = azhzLXNlY3JldA==
		return 200, `{"data":{"token":"azhzLXNlY3JldA=="}}`, nil
	}}
	// Inject a doer so the CA transport is skipped; token from a temp file.
	dir := t.TempDir()
	tokenFile := dir + "/token"
	if err := os.WriteFile(tokenFile, []byte("sa-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, ok := newK8sReader(env(map[string]string{
		"OLIVARES_SECRETREF_K8S_APISERVER":  "https://kube.local:6443",
		"OLIVARES_SECRETREF_K8S_TOKEN_FILE": tokenFile,
	}), doer)
	if got := mustResolve(t, r, ok, "ns1/gdrive/token"); got != "k8s-secret" {
		t.Errorf("value = %q", got)
	}
}

func TestK8sReaderInClusterIPv6APIServer(t *testing.T) {
	dir := t.TempDir()
	tokenFile := dir + "/token"
	if err := os.WriteFile(tokenFile, []byte("sa-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "bare compressed",
			env: map[string]string{
				"KUBERNETES_SERVICE_HOST": "2001:db8::1",
				"KUBERNETES_SERVICE_PORT": "443",
			},
			want: "https://[2001:db8::1]:443",
		},
		{
			name: "explicit bracketed port override",
			env: map[string]string{
				"OLIVARES_SECRETREF_K8S_APISERVER": "https://[2001:db8::1]:443",
			},
			want: "https://[2001:db8::1]:443",
		},
		{
			name: "link local zone",
			env: map[string]string{
				"KUBERNETES_SERVICE_HOST": "fe80::1%eth0",
				"KUBERNETES_SERVICE_PORT": "443",
			},
			want: "https://[fe80::1%eth0]:443",
		},
		{
			name: "v4 mapped",
			env: map[string]string{
				"KUBERNETES_SERVICE_HOST": "::ffff:192.0.2.1",
				"KUBERNETES_SERVICE_PORT": "443",
			},
			want: "https://[::ffff:192.0.2.1]:443",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.env["OLIVARES_SECRETREF_K8S_TOKEN_FILE"] = tokenFile
			r, ok := newK8sReader(env(tt.env), fakeDoer{fn: func(_ *http.Request) (int, string, error) {
				return 200, `{"data":{"token":"azhzLXNlY3JldA=="}}`, nil
			}})
			if !ok {
				t.Fatal("reader not configured")
			}
			kr, ok := r.(*k8sReader)
			if !ok {
				t.Fatalf("reader type = %T, want *k8sReader", r)
			}
			if kr.apiserver != tt.want {
				t.Fatalf("apiserver = %q, want %q", kr.apiserver, tt.want)
			}
		})
	}
}

func TestHandlersWiresOnlyConfigured(t *testing.T) {
	// Only vault is configured; the rest are omitted (references fail closed).
	h := Handlers(env(map[string]string{
		"VAULT_ADDR":                     "https://v.example",
		"OLIVARES_SECRETREF_VAULT_TOKEN": "t",
	}), fakeDoer{fn: func(_ *http.Request) (int, string, error) { return 200, `{"data":{"k":"v"}}`, nil }}, nil)
	if _, ok := h[SchemeVault]; !ok {
		t.Error("vault should be wired")
	}
	for _, s := range []string{SchemeAWSSecretsManager, SchemeGCPSecretManager, SchemeAzureKeyVault, SchemeInfisical} {
		if _, ok := h[s]; ok {
			t.Errorf("scheme %s should NOT be wired without config", s)
		}
	}
}
