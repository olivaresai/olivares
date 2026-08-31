// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// recordingMux wraps an http.ServeMux to record every request method+path so a
// test can assert the connector issued ONLY read (GET) calls.
type recordingMux struct {
	mu    sync.Mutex
	calls []string
	mux   *http.ServeMux
}

func (r *recordingMux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.calls = append(r.calls, req.Method+" "+req.URL.Path)
	r.mu.Unlock()
	r.mux.ServeHTTP(w, req)
}

func (r *recordingMux) methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// startDockerServer serves the given fixtures on a fresh unix socket and returns
// the socket path and the recording mux. It registers handlers for the read-only
// endpoints the connector calls.

// sunPathMax es el limite del campo `sun_path` de sockaddr_un en Linux: 108 BYTES, contando el
// NUL final. No es una constante de Go ni un limite de este proyecto: es del kernel.
const sunPathMax = 108

// shortSocketPath devuelve una ruta de socket que CABE, y falla ruidosamente si no puede.
//
// ⛔ POR QUE NO VALE t.TempDir(), medido el 2026-08-19 en ci-runner-8:
//
//	listen unix /home/runner/actions-runner-8/_work/_temp/o.0F2wyQ/
//	  TestGatherDocker_APIError_HealthFinding1400357983/001/d.sock: bind: invalid argument
//
// 111 bytes. `t.TempDir()` construye la ruta con TMPDIR + EL NOMBRE DEL TEST + un contador, asi
// que su longitud depende de como se llame el test: renombrar una funcion puede romper un socket
// que funcionaba, y el error que sale —`bind: invalid argument`— no menciona la longitud por
// ningun lado y se lee como un bug del codigo.
//
// El runner tampoco lo arregla: /dev/shm alli no ejecuta (rc=127) y el paso de CI cae a una base
// de 50 caracteres, dejando 58 para lo demas. Depender de que TMPDIR sea corto es depender de la
// maquina; esto no depende de nadie.
//
// Prueba bases cortas en orden y coge la PRIMERA que quepa. Para un socket no hace falta poder
// EJECUTAR ahi —eso es otra pregunta, la de los plugins—, asi que /tmp sirve aunque este noexec.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	for _, base := range []string{"/tmp", "/dev/shm", os.TempDir()} {
		if base == "" {
			continue
		}
		if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
			continue
		}
		dir, err := os.MkdirTemp(base, "d")
		if err != nil {
			continue
		}
		sock := filepath.Join(dir, "s")
		if len(sock)+1 > sunPathMax {
			_ = os.RemoveAll(dir)
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return sock
	}
	// Tercera respuesta: no es que el codigo falle, es que aqui no se puede medir. Se dice
	// entero en vez de dejar un `bind: invalid argument` que apunta al sitio equivocado.
	t.Skipf("ninguna base corta admite un socket unix de <=%d bytes (probadas /tmp, /dev/shm y %q); "+
		"no es un fallo del codigo bajo prueba, es que esta maquina no permite medirlo",
		sunPathMax, os.TempDir())
	return ""
}

func startDockerServer(t *testing.T, fixtures map[string]string, status map[string]int) (string, *recordingMux) {
	t.Helper()
	sock := shortSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	mux := http.NewServeMux()
	for path, body := range fixtures {
		body := body
		code := http.StatusOK
		if status != nil {
			if c, ok := status[path]; ok {
				code = c
			}
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		})
	}
	rec := &recordingMux{mux: mux}
	srv := &http.Server{Handler: rec, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); _ = ln.Close() })
	return sock, rec
}

func dockerCfg(host, socket string) config {
	return loadConfig(sinkConfig(map[string]string{
		cfgEnableLinux:  "false",
		cfgEnableDocker: "true",
		cfgEnableK8s:    "false",
		cfgHost:         host,
		cfgDockerSocket: socket,
	}))
}

func dockerFixtures() map[string]string {
	return map[string]string{
		"/version":         `{"Version":"25.0.0","ApiVersion":"1.44"}`,
		"/info":            `{"Name":"docker-host-1"}`,
		"/images/json":     `[{"Id":"sha256:abc","RepoTags":["ollama/ollama:latest"]}]`,
		"/networks":        `[{"Name":"bridge","Id":"net1"}]`,
		"/containers/json": `[{"Id":"zzz999","Names":["/web"],"Image":"nginx:1.25"},{"Id":"aaa111","Names":["/ai-agent"],"Image":"ollama/ollama:latest"}]`,
	}
}

func TestGatherDocker_GoldenEdges(t *testing.T) {
	sock, rec := startDockerServer(t, dockerFixtures(), nil)
	cfg := dockerCfg("ignored-host", sock)
	sink := &fakeSink{}
	if err := gatherDocker(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherDocker: %v", err)
	}

	got := sink.sortedEdgeKeys()
	// host name comes from /info .Name = "docker-host-1". Containers sorted by Id
	// ascending: aaa111 (ai-agent) then zzz999 (web).
	want := []string{
		// ai-agent container + its image
		"docker.host|docker-host-1 -> container|ai-agent tool=ollama/ollama:latest mode=unknown src=runtime conf=attributed",
		"container|ai-agent -> container.image|ollama/ollama:latest tool= mode=unknown src=runtime conf=attributed",
		// web container + its image
		"docker.host|docker-host-1 -> container|web tool=nginx:1.25 mode=unknown src=runtime conf=attributed",
		"container|web -> container.image|nginx:1.25 tool= mode=unknown src=runtime conf=attributed",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edge set mismatch:\n got=%v\nwant=%v", got, want)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %d: %+v", len(fs), fs)
	}

	// Read-only assertion: every recorded request is a GET.
	for _, call := range rec.methods() {
		if call[:4] != "GET " {
			t.Fatalf("non-GET request issued to docker: %q", call)
		}
	}
}

func TestGatherDocker_AbsentSocket_SilentSkip(t *testing.T) {
	cfg := dockerCfg("box", filepath.Join(t.TempDir(), "no-such.sock"))
	sink := &fakeSink{}
	if err := gatherDocker(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherDocker on absent socket should be nil, got %v", err)
	}
	if es := sink.edges(); len(es) != 0 {
		t.Fatalf("absent socket must emit no edges, got %d", len(es))
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("absent socket must emit NO finding, got %d: %+v", len(fs), fs)
	}
}

func TestGatherDocker_APIError_HealthFinding(t *testing.T) {
	// Socket exists, but /version returns 500.
	fx := dockerFixtures()
	sock, _ := startDockerServer(t, fx, map[string]int{"/version": http.StatusInternalServerError})
	cfg := dockerCfg("box", sock)
	sink := &fakeSink{}
	if err := gatherDocker(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherDocker should turn an API error into a finding, got err %v", err)
	}
	if es := sink.edges(); len(es) != 0 {
		t.Fatalf("expected 0 edges on API error, got %d", len(es))
	}
	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("expected exactly 1 health finding, got %d: %+v", len(fs), fs)
	}
	if fs[0].Kind != "health" || fs[0].SubjectKind != subjectDockerHost {
		t.Fatalf("unexpected finding: %+v", fs[0])
	}
	if fs[0].DetailHash == "" {
		t.Fatalf("finding must carry a hashed detail")
	}
}

// TestDockerDiscoveryOffByDefault locks the secure default (docs/SECURITY-HARDENING.md, §4):
// read access to docker.sock is root-equivalent, so Docker discovery must be an
// explicit opt-in. Linux procfs and the Kubernetes ServiceAccount path stay on
// by default (lower privilege); only Docker is off until the operator enables it.
func TestDockerDiscoveryOffByDefault(t *testing.T) {
	c := loadConfig(sinkConfig(map[string]string{}))
	if c.enableDocker {
		t.Fatal("enable_docker must default to false (docker.sock is root-equivalent; opt-in only)")
	}
	if !c.enableLinux {
		t.Fatal("enable_linux should remain on by default (procfs read is low-privilege)")
	}
	if !c.enableK8s {
		t.Fatal("enable_k8s should remain on by default (scoped ServiceAccount token)")
	}
	// Explicit opt-in turns it on.
	if on := loadConfig(sinkConfig(map[string]string{cfgEnableDocker: "true"})); !on.enableDocker {
		t.Fatal("enable_docker=true must opt in")
	}
}

func TestContainerRef(t *testing.T) {
	cases := []struct {
		c    dockerContainer
		want string
	}{
		{dockerContainer{ID: "abcdef0123456789", Names: []string{"/svc"}}, "svc"},
		{dockerContainer{ID: "abcdef0123456789", Names: []string{"/"}}, "abcdef012345"},
		{dockerContainer{ID: "abcdef0123456789"}, "abcdef012345"},
		{dockerContainer{ID: "short"}, "short"},
	}
	for _, tc := range cases {
		if got := containerRef(tc.c); got != tc.want {
			t.Fatalf("containerRef(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}
