// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package secretref

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/tlsx"
)

// k8sReader resolves `k8s-secret:<namespace>/<name>/<key>` against the Kubernetes
// API: it GETs the Secret object and base64-decodes data[<key>]. (A Secret mounted
// as a file is better read with `file:`; this reader is the in-cluster API path
// for an unmounted Secret.) It reads only the named Secret — the engine's
// ServiceAccount must be granted get on it.
//
// Engine config (in-cluster defaults, all overridable):
//
//	OLIVARES_SECRETREF_K8S_APISERVER  — API base (default https://$KUBERNETES_SERVICE_HOST:$PORT)
//	OLIVARES_SECRETREF_K8S_TOKEN_FILE — SA token (default /var/run/secrets/kubernetes.io/serviceaccount/token)
//	OLIVARES_SECRETREF_K8S_CA_FILE    — API CA (default /var/run/secrets/kubernetes.io/serviceaccount/ca.crt)
type k8sReader struct {
	apiserver string
	token     envToken
	doer      httpx.Doer
}

const (
	k8sDefaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	k8sDefaultCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

func newK8sReader(getenv func(string) string, doer httpx.Doer) (Reader, bool) {
	apiserver := firstEnv(getenv, "OLIVARES_SECRETREF_K8S_APISERVER")
	if apiserver == "" {
		host := firstEnv(getenv, "KUBERNETES_SERVICE_HOST")
		if host == "" {
			return nil, false // not in a cluster and no override
		}
		port := firstEnv(getenv, "KUBERNETES_SERVICE_PORT_HTTPS", "KUBERNETES_SERVICE_PORT")
		if port == "" {
			port = "443"
		}
		apiserver = "https://" + net.JoinHostPort(host, port)
	}
	tokenFile := firstEnv(getenv, "OLIVARES_SECRETREF_K8S_TOKEN_FILE")
	if tokenFile == "" {
		tokenFile = k8sDefaultTokenFile
	}
	r := &k8sReader{apiserver: strings.TrimRight(apiserver, "/"), token: envToken{file: tokenFile}, doer: doer}

	// Build the CA-pinned transport once (the CA is static). A test injects a doer
	// and skips this; in-cluster the default CA file is present.
	if doer == nil {
		caFile := firstEnv(getenv, "OLIVARES_SECRETREF_K8S_CA_FILE")
		if caFile == "" {
			caFile = k8sDefaultCAFile
		}
		pool, err := tlsx.CAPool(caFile)
		if err != nil {
			return nil, false // no usable CA — fail closed (not wired)
		}
		r.doer = &http.Client{
			Timeout:   defaultTimeout,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		}
	}
	return r, true
}

func (r *k8sReader) Resolve(ctx context.Context, locator string) ([]byte, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(locator), "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("k8s-secret: locator must be <namespace>/<name>/<key>")
	}
	ns, name, key := parts[0], parts[1], parts[2]
	tokenVal, err := r.token.value()
	if err != nil {
		return nil, fmt.Errorf("k8s-secret: read service-account token: %w", err)
	}
	client := httpx.New(r.apiserver, r.doer, httpx.Bearer(tokenVal), nil)

	path := "/api/v1/namespaces/" + url.PathEscape(ns) + "/secrets/" + url.PathEscape(name)
	var resp struct {
		Data map[string]string `json:"data"`
	}
	if err := client.GetJSON(ctx, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("k8s-secret: %w", err)
	}
	b64, ok := resp.Data[key]
	if !ok {
		return nil, fmt.Errorf("k8s-secret: %s/%s has no key %q", ns, name, key)
	}
	dec, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("k8s-secret: decode data[%q]: %w", key, err)
	}
	return dec, nil
}
