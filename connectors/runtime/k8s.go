// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// subjectK8sCluster is the FindingReport SubjectKind for a Kubernetes API server
// that is configured/present but cannot be queried.
const subjectK8sCluster = "k8s.cluster"

// k8sConn is a resolved Kubernetes connection: a base API URL, a bearer token,
// and an HTTP client whose TLS trusts the cluster CA (or skips verification when
// explicitly configured). The token is held in memory only and never emitted.
type k8sConn struct {
	base   string // e.g. https://10.0.0.1:443
	host   string // host[:port] used as the cluster ref
	token  string // secret, in-memory only
	client *http.Client
}

// k8sList is the generic List envelope: only metadata.name (+ spec.nodeName /
// spec.containers[].image for pods) is decoded. Secrets, configmaps, env and
// annotations are never decoded, so they can never be emitted.
type k8sList struct {
	Items []k8sObject `json:"items"`
}

// k8sObject is the minimal-data view of a node/namespace/pod/deployment.
type k8sObject struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Image string `json:"image"`
		} `json:"containers"`
	} `json:"spec"`
}

// gatherK8s discovers nodes, namespaces, pods and deployments from the Kubernetes
// API over read-only HTTPS GETs. Connection resolution:
//   - if k8s_api_server is set, use it with k8s_token (or token file) and the CA
//     file (or insecure flag);
//   - else if running in-cluster (KUBERNETES_SERVICE_HOST/PORT set and the SA
//     token file readable), build the in-cluster connection;
//   - else SKIP silently (no cluster present ⇒ not a finding).
//
// A configured/present cluster whose API errors yields exactly one health finding
// and returns. Secrets, configmaps and pod env are never read.
func gatherK8s(ctx context.Context, cfg config, sink sdk.Sink, at time.Time) error {
	conn, present, err := resolveK8s(cfg)
	if !present {
		return nil // no cluster configured/detected — skip silently
	}
	if err != nil {
		return sink.Emit(ctx, healthFinding(subjectK8sCluster, k8sRef(cfg), "kubernetes connection setup failed", err, at))
	}

	// nodes: cluster -> node
	nodes, err := k8sGetList(ctx, conn, "/api/v1/nodes")
	if err != nil {
		return sink.Emit(ctx, healthFinding(subjectK8sCluster, conn.host, "kubernetes nodes list failed", err, at))
	}

	// namespaces: queried to confirm reachability; not itself an emitted entity.
	if _, err := k8sGetList(ctx, conn, "/api/v1/namespaces"); err != nil {
		return sink.Emit(ctx, healthFinding(subjectK8sCluster, conn.host, "kubernetes namespaces list failed", err, at))
	}

	// pods: node -> pod  and  pod -> image
	pods, err := listPods(ctx, conn, cfg.k8sNamespaces)
	if err != nil {
		return sink.Emit(ctx, healthFinding(subjectK8sCluster, conn.host, "kubernetes pods list failed", err, at))
	}

	// deployments: namespace -> deployment
	deploys, err := k8sGetList(ctx, conn, "/apis/apps/v1/deployments")
	if err != nil {
		return sink.Emit(ctx, healthFinding(subjectK8sCluster, conn.host, "kubernetes deployments list failed", err, at))
	}

	return emitK8s(ctx, sink, conn.host, nodes, pods, deploys, at)
}

// emitK8s sorts every list by its natural key and emits the topology edges in a
// stable order: nodes, then pods (+ their images), then deployments. ctx is
// honored between emissions.
func emitK8s(ctx context.Context, sink sdk.Sink, cluster string, nodes, pods, deploys k8sList, at time.Time) error {
	sort.Slice(nodes.Items, func(i, j int) bool { return nodes.Items[i].Metadata.Name < nodes.Items[j].Metadata.Name })
	for _, n := range nodes.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.Metadata.Name == "" {
			continue
		}
		if err := sink.Emit(ctx, k8sNodeEdge(cluster, n.Metadata.Name, at)); err != nil {
			return err
		}
	}

	sort.Slice(pods.Items, func(i, j int) bool { return podRef(pods.Items[i]) < podRef(pods.Items[j]) })
	for _, p := range pods.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		ref := podRef(p)
		if ref == "" {
			continue
		}
		node := p.Spec.NodeName
		if node == "" {
			node = "unscheduled"
		}
		firstImage := ""
		if len(p.Spec.Containers) > 0 {
			firstImage = redact.Clean(p.Spec.Containers[0].Image)
		}
		if err := sink.Emit(ctx, k8sPodEdge(node, ref, firstImage, at)); err != nil {
			return err
		}
		for _, ctr := range p.Spec.Containers {
			img := redact.Clean(ctr.Image)
			if img == "" {
				continue
			}
			if err := sink.Emit(ctx, k8sPodImageEdge(ref, img, at)); err != nil {
				return err
			}
		}
	}

	sort.Slice(deploys.Items, func(i, j int) bool { return deployRef(deploys.Items[i]) < deployRef(deploys.Items[j]) })
	for _, d := range deploys.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		ref := deployRef(d)
		if ref == "" || d.Metadata.Namespace == "" {
			continue
		}
		if err := sink.Emit(ctx, k8sDeploymentEdge(d.Metadata.Namespace, ref, at)); err != nil {
			return err
		}
	}
	return nil
}

// podRef is "<namespace>/<name>", the pod's natural key.
func podRef(o k8sObject) string {
	if o.Metadata.Name == "" {
		return ""
	}
	return o.Metadata.Namespace + "/" + o.Metadata.Name
}

// deployRef is "<namespace>/<name>", the deployment's natural key.
func deployRef(o k8sObject) string {
	if o.Metadata.Name == "" {
		return ""
	}
	return o.Metadata.Namespace + "/" + o.Metadata.Name
}

// listPods fetches pods cluster-wide, or per-namespace when namespaces are given,
// merging the results. Per-namespace scoping keeps the connector usable with a
// namespaced (non-cluster) read role.
func listPods(ctx context.Context, conn *k8sConn, namespaces []string) (k8sList, error) {
	if len(namespaces) == 0 {
		return k8sGetList(ctx, conn, "/api/v1/pods")
	}
	var merged k8sList
	for _, ns := range namespaces {
		if err := ctx.Err(); err != nil {
			return k8sList{}, err
		}
		list, err := k8sGetList(ctx, conn, "/api/v1/namespaces/"+url.PathEscape(ns)+"/pods")
		if err != nil {
			return k8sList{}, err
		}
		merged.Items = append(merged.Items, list.Items...)
	}
	return merged, nil
}

// k8sGetList issues a read-only GET and decodes the List envelope. Method is GET
// unconditionally — this connector never mutates the cluster.
func k8sGetList(ctx context.Context, conn *k8sConn, path string) (k8sList, error) {
	body, err := k8sGET(ctx, conn, path)
	if err != nil {
		return k8sList{}, err
	}
	var list k8sList
	if err := json.Unmarshal(body, &list); err != nil {
		return k8sList{}, fmt.Errorf("kubernetes decode %s: %w", path, err)
	}
	return list, nil
}

// k8sGET performs a single read-only GET with bearer auth and returns the body.
func k8sGET(ctx context.Context, conn *k8sConn, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, conn.base+path, nil)
	if err != nil {
		return nil, err
	}
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := conn.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kubernetes GET %s: status %d", path, resp.StatusCode)
	}
	return body, nil
}

// resolveK8s resolves the cluster connection. It returns present=false (silent
// skip) when no cluster is configured and none is detected in-cluster; present=
// true with a non-nil error when a cluster is configured/present but its
// connection (token/CA) cannot be built.
func resolveK8s(cfg config) (conn *k8sConn, present bool, err error) {
	switch {
	case cfg.k8sAPIServer != "":
		return resolveExplicitK8s(cfg)
	case inCluster():
		return resolveInClusterK8s(cfg)
	default:
		return nil, false, nil
	}
}

// resolveExplicitK8s builds a connection from k8s_api_server + token (inline or
// file) + CA file (or insecure flag).
func resolveExplicitK8s(cfg config) (*k8sConn, bool, error) {
	base := strings.TrimRight(cfg.k8sAPIServer, "/")
	host, err := hostOf(base)
	if err != nil {
		return nil, true, err
	}
	token := cfg.k8sToken
	if token == "" && cfg.k8sTokenFile != "" {
		if b, rerr := os.ReadFile(cfg.k8sTokenFile); rerr == nil { //nolint:gosec // operator-provided path, read-only
			token = strings.TrimSpace(string(b))
		}
	}
	client, err := k8sHTTPClient(cfg.k8sCAFile, cfg.k8sInsecureSkipVerify, cfg.timeout)
	if err != nil {
		return nil, true, err
	}
	return &k8sConn{base: base, host: host, token: token, client: client}, true, nil
}

// resolveInClusterK8s builds an in-cluster connection from the standard env vars
// and the ServiceAccount token + CA files.
func resolveInClusterK8s(cfg config) (*k8sConn, bool, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	base := "https://" + net.JoinHostPort(host, port)
	tokenBytes, err := os.ReadFile(cfg.k8sTokenFile) //nolint:gosec // standard SA token path, read-only
	if err != nil {
		return nil, true, fmt.Errorf("kubernetes in-cluster token unreadable: %w", err)
	}
	client, err := k8sHTTPClient(cfg.k8sCAFile, cfg.k8sInsecureSkipVerify, cfg.timeout)
	if err != nil {
		return nil, true, err
	}
	return &k8sConn{
		base:   base,
		host:   net.JoinHostPort(host, port),
		token:  strings.TrimSpace(string(tokenBytes)),
		client: client,
	}, true, nil
}

// inCluster reports whether the standard in-cluster env vars are present.
func inCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("KUBERNETES_SERVICE_PORT") != ""
}

// k8sHTTPClient builds an HTTPS client trusting the cluster CA (or skipping
// verification when explicitly configured). It never disables verification
// unless the operator set the insecure flag.
func k8sHTTPClient(caFile string, insecure bool, timeout time.Duration) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // InsecureSkipVerify set below only when explicitly opted in
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	} else if caFile != "" {
		pem, err := os.ReadFile(caFile) //nolint:gosec // operator-provided CA path, read-only
		if err != nil {
			return nil, fmt.Errorf("kubernetes CA file unreadable: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("kubernetes CA file has no usable certificates")
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}

// k8sRef is the cluster reference used in a setup-failure finding (before a conn
// exists): the configured API server host, or "in-cluster".
func k8sRef(cfg config) string {
	if cfg.k8sAPIServer != "" {
		if h, err := hostOf(cfg.k8sAPIServer); err == nil {
			return h
		}
		return redact.SanitizeURL(cfg.k8sAPIServer)
	}
	return "in-cluster"
}

// hostOf returns the host[:port] of a URL, used as the cluster ref. It never
// includes userinfo, query or any credential.
func hostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("kubernetes api server has no host: %q", redact.SanitizeURL(raw))
	}
	return u.Host, nil
}
