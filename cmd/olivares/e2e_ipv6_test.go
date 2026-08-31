// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/webui"
)

var ipv6SetupTokenRE = regexp.MustCompile(`olst_[A-Z0-9]+`)

type ipv6RealServer struct {
	baseURL   string
	httpPort  int
	grpcAddr  string
	certFile  string
	token     string
	client    *http.Client
	errCh     chan error
	httpSrv   *http.Server
	grpcSrv   *grpc.Server
	checks    *checkpointer
	eng       *engine
	announced string
}

func TestIPv6LoopbackRealSocketsHTTPSAndGRPC(t *testing.T) {
	requireIPv6Loopback(t)
	srv := startIPv6RealServer(t, "[::1]:0", "[::1]:0")

	admin, tenant := srv.setupLoginAndOrg(t)

	code, _, raw := srv.doJSON(t, http.MethodGet, "/v1/agents?limit=10", admin, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/agents over [::1] = %d: %s", code, raw)
	}

	conn := srv.grpcConn(t)
	defer conn.Close()
	cl := apiv1.NewControlPlaneClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := cl.GetServerInfo(ctx, &apiv1.Empty{})
	if err != nil {
		t.Fatalf("grpc GetServerInfo over [::1]: %v", err)
	}
	if info.GetSetupRequired() {
		t.Fatal("grpc GetServerInfo reports setup_required after setup completed")
	}
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+admin)
	if _, err := cl.ListAgents(authCtx, &apiv1.ListAgentsRequest{Tenant: tenant, Limit: 10}); err != nil {
		t.Fatalf("grpc ListAgents over [::1]: %v", err)
	}
}

func TestIPv6DualStackDefaultBindRealSocket(t *testing.T) {
	requireIPv6Loopback(t)
	srv := startIPv6RealServer(t, ":0", "[::1]:0")

	for _, base := range []string{
		"https://" + net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", srv.httpPort)),
		"https://" + net.JoinHostPort("::1", fmt.Sprintf("%d", srv.httpPort)),
	} {
		t.Run(base, func(t *testing.T) {
			srv.waitHTTP(t, base+"/livez", http.StatusOK)
		})
	}
}

func requireIPv6Loopback(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	_ = l.Close()
}

func startIPv6RealServer(t *testing.T, httpListen, grpcListen string) *ipv6RealServer {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	httpLis, err := net.Listen("tcp", httpListen)
	if err != nil {
		t.Fatalf("listen HTTP %q: %v", httpListen, err)
	}
	grpcLis, err := net.Listen("tcp", grpcListen)
	if err != nil {
		_ = httpLis.Close()
		t.Fatalf("listen gRPC %q: %v", grpcListen, err)
	}
	httpPort := httpLis.Addr().(*net.TCPAddr).Port
	grpcPort := grpcLis.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir:   dir,
		Engine:    "sqlite",
		Version:   "ipv6-e2e",
		Logger:    log,
		ServeMode: true,
	})
	if err != nil {
		_ = httpLis.Close()
		_ = grpcLis.Close()
		t.Fatalf("boot: %v", err)
	}

	checks := startCheckpointer(eng.signer, eng.store, time.Hour, log, eng.metrics)
	certFile := filepath.Join(eng.dataDir, "tls.crt")
	keyFile := filepath.Join(eng.dataDir, "tls.key")
	if _, _, err := secure.EnsureTLSCert(certFile, keyFile); err != nil {
		_ = eng.Close()
		_ = httpLis.Close()
		_ = grpcLis.Close()
		t.Fatalf("ensure TLS cert: %v", err)
	}
	tlsLoader, err := secure.NewCertificateLoader(certFile, keyFile)
	if err != nil {
		_ = eng.Close()
		_ = httpLis.Close()
		_ = grpcLis.Close()
		t.Fatalf("load TLS cert: %v", err)
	}

	baseURL := "https://" + net.JoinHostPort("::1", fmt.Sprintf("%d", httpPort))
	var announce bytes.Buffer
	if err := announceSetup(context.Background(), &announce, eng, baseURL, false); err != nil {
		_ = eng.Close()
		_ = httpLis.Close()
		_ = grpcLis.Close()
		t.Fatalf("announce setup: %v", err)
	}
	token := ipv6SetupTokenRE.FindString(announce.String())
	if token == "" {
		_ = eng.Close()
		_ = httpLis.Close()
		_ = grpcLis.Close()
		t.Fatalf("setup token not announced:\n%s", announce.String())
	}

	httpSrv := eng.api.NewHTTPServer(httpLis.Addr().String())
	httpSrv.Handler = withEnterpriseHTTP(newSPAHandler(eng.api.Handler(), webui.FS()), eng, log)
	if err := configureHTTPServerTLS(httpSrv, tlsLoader); err != nil {
		_ = eng.Close()
		_ = httpLis.Close()
		_ = grpcLis.Close()
		t.Fatalf("configure HTTP TLS: %v", err)
	}
	grpcSrv, err := newGRPCServer(eng, tlsLoader, "", false)
	if err != nil {
		_ = eng.Close()
		_ = httpLis.Close()
		_ = grpcLis.Close()
		t.Fatalf("new gRPC server: %v", err)
	}

	srv := &ipv6RealServer{
		baseURL:   baseURL,
		httpPort:  httpPort,
		grpcAddr:  net.JoinHostPort("::1", fmt.Sprintf("%d", grpcPort)),
		certFile:  certFile,
		token:     token,
		client:    pinnedHTTPClient(t, certFile),
		errCh:     make(chan error, 2),
		httpSrv:   httpSrv,
		grpcSrv:   grpcSrv,
		checks:    checks,
		eng:       eng,
		announced: announce.String(),
	}

	go func() { srv.errCh <- httpSrv.ServeTLS(httpLis, "", "") }()
	go func() { srv.errCh <- grpcSrv.Serve(grpcLis) }()
	t.Cleanup(func() { srv.close(t) })

	srv.waitHTTP(t, srv.baseURL+"/livez", http.StatusOK)
	return srv
}

func pinnedHTTPClient(t *testing.T, certFile string) *http.Client {
	t.Helper()
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: pinnedTLSConfig(t, certFile, ""),
		},
	}
}

func pinnedTLSConfig(t *testing.T, certFile, serverName string) *tls.Config {
	t.Helper()
	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("read TLS cert: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("TLS cert %s contains no usable certificate", certFile)
	}
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, ServerName: serverName}
}

func (s *ipv6RealServer) setupLoginAndOrg(t *testing.T) (adminToken, tenant string) {
	t.Helper()
	code, _, raw := s.doJSON(t, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": s.token, "email": "admin@ipv6.test", "password": "supersecret-ipv6",
	})
	if code != http.StatusCreated {
		t.Fatalf("setup over [::1] = %d: %s\nannouncement:\n%s", code, raw, s.announced)
	}
	code, login, raw := s.doJSON(t, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": "admin@ipv6.test", "password": "supersecret-ipv6",
	})
	if code != http.StatusOK {
		t.Fatalf("login over [::1] = %d: %s", code, raw)
	}
	adminToken, _ = login["token"].(string)
	if adminToken == "" {
		t.Fatalf("login over [::1] returned no token: %s", raw)
	}
	code, org, raw := s.doJSON(t, http.MethodPost, "/v1/system/orgs", adminToken, "", map[string]any{
		"name": "IPv6 Acme", "slug": "ipv6-acme",
	})
	if code != http.StatusCreated {
		t.Fatalf("create org over [::1] = %d: %s", code, raw)
	}
	tenant, _ = org["tenant_id"].(string)
	if tenant == "" {
		t.Fatalf("create org over [::1] returned no tenant_id: %s", raw)
	}
	return adminToken, tenant
}

func (s *ipv6RealServer) doJSON(t *testing.T, method, path, token, tenant string, body any) (int, map[string]any, string) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.baseURL+path, rdr)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s over [::1]: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

func (s *ipv6RealServer) grpcConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(s.grpcAddr, grpc.WithTransportCredentials(credentials.NewTLS(pinnedTLSConfig(t, s.certFile, "::1"))))
	if err != nil {
		t.Fatalf("grpc dial %s: %v", s.grpcAddr, err)
	}
	return conn
}

func (s *ipv6RealServer) waitHTTP(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var last string
	for {
		resp, err := s.client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
			last = fmt.Sprintf("status %d", resp.StatusCode)
		} else {
			last = err.Error()
		}
		select {
		case err := <-s.errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
				t.Fatalf("server exited before %s became ready: %v", url, err)
			}
		case <-tick.C:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never returned %d before deadline; last: %s", url, want, last)
		}
	}
}

func (s *ipv6RealServer) close(t *testing.T) {
	t.Helper()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
		t.Errorf("http shutdown: %v", err)
	}
	s.grpcSrv.GracefulStop()
	s.checks.stop(context.Background())
	if err := s.eng.Close(); err != nil {
		t.Errorf("engine close: %v", err)
	}
	for i := 0; i < cap(s.errCh); i++ {
		select {
		case err := <-s.errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
				t.Errorf("serve error: %v", err)
			}
		default:
			return
		}
	}
}
