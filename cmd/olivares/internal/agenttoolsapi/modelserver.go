// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// LOCAL OPENAI-COMPATIBLE MODEL SERVERS. llama.cpp's `llama-server` and `vllm serve`
// were started by hand, and nothing tied them to a session. They now run the way Ollama does
// (ollama.go): the program the operator installed (found on the engine's PATH) is started as a
// managed child of the engine, on loopback only, confined, at low priority, in its own process
// group, stopped with the engine and started again with it. Unlike Ollama they answer a key:
// the engine makes one at every start, hands it to the child in its environment (never its
// arguments), and registers the endpoint in Providers as an openai_compatible record holding
// that key. OpenCode is bound to that record like to any other provider.

const (
	modelServerStopped  = "stopped"
	modelServerStarting = "starting"
	modelServerRunning  = "running"
	modelServerFailed   = "failed"

	// A model may have to be downloaded by the server before it answers.
	modelServerReadyTimeout = 30 * time.Minute
	maxModelServerTail      = 4 << 10
)

// modelServerSpec is what differs between the servers: the program, its arguments and the
// variable it reads its key from. Everything else is one lifecycle.
type modelServerSpec struct {
	kind, name, program string
	addr                string   // the default listen address; port 0 is a free one the OS picks at each start
	keyEnv              string   // the variable the server reads its API key from
	lead                []string // arguments before the model (vllm: serve)
	env                 []string // fixed additions to the child's environment
	// model validates what the operator named and returns the arguments that name it for
	// the server, and the directory of a local model the child must be able to read.
	model func(ref string) (args []string, readDir string, err error)
}

var (
	// ggufRepo is a Hugging Face reference llama-server downloads itself (-hf): user/repo,
	// optionally :quantization. A path is absolute; a name ending .gguf is a path typed wrong.
	ggufRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}(:[A-Za-z0-9._-]{1,64})?$`)
	// hfRepo is a Hugging Face model vllm downloads itself: user/repo.
	hfRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
)

var modelServerSpecs = []modelServerSpec{
	{
		kind: "llama-cpp", name: "llama.cpp", program: "llama-server", addr: "127.0.0.1:0", keyEnv: "LLAMA_API_KEY",
		model: func(ref string) ([]string, string, error) {
			if !strings.HasPrefix(ref, "/") {
				if !ggufRepo.MatchString(ref) || strings.HasSuffix(strings.ToLower(strings.SplitN(ref, ":", 2)[0]), ".gguf") {
					return nil, "", errors.New("Name a .gguf file by its absolute path, or a Hugging Face model as user/repo[:quantization].")
				}
				return []string{"-hf", ref}, "", nil
			}
			file, err := localModelPath(ref, false)
			if err != nil {
				return nil, "", err
			}
			if !strings.EqualFold(filepath.Ext(file), ".gguf") {
				return nil, "", errors.New("The model file must be a .gguf file.")
			}
			// The id the server lists is the file's name without its extension, not its path.
			alias := strings.TrimSuffix(filepath.Base(ref), filepath.Ext(ref))
			return []string{"-m", file, "--alias", alias}, filepath.Dir(file), nil
		},
	},
	{
		kind: "vllm", name: "vLLM", program: "vllm", addr: "127.0.0.1:0", keyEnv: "VLLM_API_KEY", lead: []string{"serve"},
		// vLLM's own usage statistics and Hugging Face telemetry are off: a background request
		// must not leave the host unannounced.
		env: []string{"VLLM_NO_USAGE_STATS=1", "DO_NOT_TRACK=1", "HF_HUB_DISABLE_TELEMETRY=1"},
		model: func(ref string) ([]string, string, error) {
			if !strings.HasPrefix(ref, "/") {
				if !hfRepo.MatchString(ref) {
					return nil, "", errors.New("Name a model directory by its absolute path, or a Hugging Face model as user/repo.")
				}
				return []string{ref}, "", nil
			}
			dir, err := localModelPath(ref, true)
			if err != nil {
				return nil, "", err
			}
			return []string{dir}, dir, nil
		},
	},
}

// check validates what the operator named. The directory the child may read must not be the
// whole filesystem or the engine user's home: the child only needs its model.
func (s modelServerSpec) check(ref string) (args []string, readDir string, err error) {
	args, readDir, err = s.model(ref)
	if err == nil && readDir != "" && broadReadRoot(readDir) {
		return nil, "", errors.New("Keep the model in a folder of its own: its folder would give the server read access to your whole home or filesystem.")
	}
	return args, readDir, err
}

// broadReadRoot reports whether granting read access to root would reach the whole filesystem
// or the engine user's home (the user's own tool logins live there).
func broadReadRoot(root string) bool {
	root = resolvedPath(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return true // a folder that cannot be placed is not granted
	}
	// Folders every user and process shares: reading one is reading other people's files.
	for _, shared := range sharedReadRoots {
		if root == shared {
			return true
		}
	}
	home := engineUserHome()
	if home == "" {
		return false // no home to protect
	}
	// Both sides are compared as the filesystem sees them: a home folder reached through a
	// link (/home -> /data/home) is the same folder.
	home = resolvedPath(home)
	if contains(root, home) {
		return true
	}
	// A folder that holds other tools' logins and keys, or lies inside one (~/.local holds
	// ~/.local/share/opencode, for one), is as wide as the home for what matters here.
	for _, secret := range homeCredentialDirs {
		dir := filepath.Join(home, secret)
		if contains(root, dir) || contains(dir, root) {
			return true
		}
	}
	return false
}

// homeCredentialDirs are folders of the engine user's home that hold tool logins and keys.
var homeCredentialDirs = []string{".ssh", ".aws", ".gnupg", ".kube", ".docker", ".config", ".local/share/opencode", ".local/share/keyrings", ".claude", ".codex", ".gemini", ".grok"}

// sharedReadRoots are folders other users and processes write to; a model sits in a folder inside
// one, never in the folder itself.
var sharedReadRoots = []string{"/tmp", "/var/tmp", "/dev/shm", "/run", "/etc"}

// contains reports whether p is root or lies inside it.
func contains(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// engineUserHome is the home of the user the engine runs as: $HOME, else the account's.
func engineUserHome() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// resolvedPath is the path with its links followed, or cleaned as it is when it cannot be.
func resolvedPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func modelServerSpecFor(kind string) (modelServerSpec, bool) {
	for _, s := range modelServerSpecs {
		if s.kind == kind {
			return s, true
		}
	}
	return modelServerSpec{}, false
}

// localModelPath checks an absolute path the operator named and returns it with symbolic
// links resolved, so the directory the child may read is the one the model is in.
func localModelPath(ref string, dir bool) (string, error) {
	if filepath.Clean(ref) != ref || strings.ContainsAny(ref, "\x00\r\n") {
		return "", errors.New("The model path must be absolute and clean, without . or .. parts.")
	}
	resolved, err := filepath.EvalSymlinks(ref)
	if err != nil {
		return "", errors.New("The model was not found on this node.")
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() != dir {
		if dir {
			return "", errors.New("The model must be a directory.")
		}
		return "", errors.New("The model must be a file.")
	}
	return resolved, nil
}

// ModelServerConfig is the composition's part of the servers.
type ModelServerConfig struct {
	// Dir is the engine's own directory: <Dir>/<kind>/ is the server's home and scratch
	// space (the child writes it), <Dir>/<kind>.json remembers a person's start outside it.
	Dir string
	// Addrs pins a server's loopback listen address by kind. Empty: a free loopback port the
	// OS picks at each start, so a start never fails on a port something else holds; the
	// record in Providers follows the server to its new address.
	Addrs map[string]string
	// Register runs once the server answers, with the person who started it, the tenant
	// they named, the record's name, the endpoint (with its /v1) and the key the engine made
	// for this start. It adds the endpoint to that tenant's Providers, or replaces the key of
	// the record already there. A failure is reported on the row; the server keeps running.
	Register func(ctx context.Context, actor auth.Principal, tenant model.TenantID, name, endpoint, key string) error
	// Audit records a start the engine makes by itself (RestartModelServers) as a system
	// action. Without it, or when the write fails, the engine starts nothing by itself.
	Audit func(ctx context.Context, draft model.AuditDraft) error
	// ReadyTimeout is how long a server may take to answer, a model download included;
	// zero is 30 minutes.
	ReadyTimeout time.Duration
}

// ModelServerStatus is the row's view of one server.
type ModelServerStatus struct {
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Installed bool     `json:"installed"`
	State     string   `json:"state"`
	Message   string   `json:"message,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	Model     string   `json:"model,omitempty"`
	Models    []string `json:"models"`
}

// modelServerRemembered is what <Dir>/<kind>.json holds while a person's start stands.
type modelServerRemembered struct {
	TenantID  string `json:"tenant_id"`
	Model     string `json:"model"`
	StartedBy string `json:"started_by,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

type modelServer struct {
	state, message, model string
	tenant                model.TenantID // the organization whose Providers hold its record
	addr                  string         // the address it listens on, with the port the OS picked
	key                   string         // this start's key; in memory only
	stopping              bool
	stop                  context.CancelFunc
	done                  chan struct{}
}

type modelServers struct {
	cfg ModelServerConfig
	by  map[string]*modelServer
	// life orders the starts and stops of the servers: a stop finishes (and forgets the start
	// it ended) before the next start takes the slot and keeps its own record.
	life sync.Mutex
}

// UseModelServers enables the servers with the composition's directory and registration.
// Without it the routes answer that this node cannot run them.
func (m *Module) UseModelServers(cfg ModelServerConfig) {
	m.mu.Lock()
	m.modelServers.cfg = cfg
	m.mu.Unlock()
}

func (m *Module) findProgram(name string) (string, error) {
	m.mu.Lock()
	look := m.lookPath
	m.mu.Unlock()
	if look == nil {
		look = exec.LookPath
	}
	return look(name)
}

func (c ModelServerConfig) addr(spec modelServerSpec) string {
	if a := c.Addrs[spec.kind]; a != "" {
		return a
	}
	return spec.addr
}

func (m *Module) modelServerStatus(ctx context.Context, kind string) ModelServerStatus {
	spec, _ := modelServerSpecFor(kind)
	out := ModelServerStatus{Kind: spec.kind, Name: spec.name, State: modelServerStopped, Models: []string{}}
	m.mu.Lock()
	var key, addr string
	if s := m.modelServers.by[kind]; s != nil && s.state != "" {
		out.State, out.Message, out.Model, key, addr = s.state, s.message, s.model, s.key, s.addr
	}
	m.mu.Unlock()
	_, err := m.findProgram(spec.program)
	out.Installed = err == nil
	if out.State == modelServerRunning {
		out.Endpoint = "http://" + addr + "/v1"
		if models, err := m.modelServerModels(ctx, out.Endpoint, key); err == nil {
			out.Models = models
		}
	}
	return out
}

// modelServerModels lists what the running server serves (GET /v1/models, with its key).
func (m *Module) modelServerModels(ctx context.Context, endpoint, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Data))
	for _, d := range list.Data {
		out = append(out, d.ID)
	}
	return out, nil
}

// handleModelServers reports each server: installed, running or not, its endpoint and the
// models it serves. System admin only.
func (m *Module) handleModelServers(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		servers := make([]ModelServerStatus, 0, len(modelServerSpecs))
		for _, spec := range modelServerSpecs {
			servers = append(servers, m.modelServerStatus(r.Context(), spec.kind))
		}
		write(w, 200, map[string]any{"servers": servers})
	})(w, r, mc)
}

// errModelServerBusy is a start that lost to another start of the same server.
var errModelServerBusy = errors.New("already starting or running")

// handleModelServerStart starts the installed server as a managed child of the engine and
// returns at once; the row reads "running" once the server answers.
func (m *Module) handleModelServerStart(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		spec, ok := modelServerSpecFor(chi.URLParam(r, "kind"))
		if !ok {
			fail(w, 404, "not_found", "No such model server. Use llama-cpp or vllm.")
			return
		}
		if m.readOnly {
			fail(w, 409, "read_only", "This control plane cannot start services.")
			return
		}
		m.mu.Lock()
		cfg := m.modelServers.cfg
		m.mu.Unlock()
		if cfg.Dir == "" {
			fail(w, 503, "unavailable", "This node has no place for model servers.")
			return
		}
		var in struct {
			Model    string `json:"model"`
			TenantID string `json:"tenant_id"`
		}
		if err := api.DecodeRequestBody(w, r, &in, api.RequestBodySpec{MaxBytes: 4096}); err != nil {
			fail(w, 400, "bad_request", api.RequestBodyErrorMessage(err, "Provide a valid request with only the supported fields."))
			return
		}
		// The tenant whose Providers get the endpoint and the key: the key has nowhere else to
		// go, so a start that names none would run a server nobody can use.
		tenant, err := model.ParseTenantID(in.TenantID)
		if err != nil || tenant.IsZero() {
			fail(w, 400, "bad_request", "tenant_id must name the organization whose Providers get this server.")
			return
		}
		ref := strings.TrimSpace(in.Model)
		if ref == "" || len(ref) > 1024 {
			fail(w, 400, "bad_request", "model must name the model to serve.")
			return
		}
		args, readDir, err := spec.check(ref)
		if err != nil {
			fail(w, 400, "bad_request", err.Error())
			return
		}
		if m.answerTakenServer(w, r, spec, ref, tenant) {
			return
		}
		program, err := m.findProgram(spec.program)
		if err != nil {
			fail(w, 409, "tool_not_installed", "Install "+spec.name+" ("+spec.program+") on this node first: it must be on the engine's PATH.")
			return
		}
		if err := audit(r.Context(), mc, "model_server.start", "", map[string]any{"kind": spec.kind, "model": ref, "register_in": tenant.String()}); err != nil {
			unavailable(w)
			return
		}
		was := &modelServerRemembered{TenantID: tenant.String(), Model: ref, StartedBy: mc.Principal.Actor()}
		switch err := m.startModelServer(spec, program, ref, args, readDir, mc.Principal, tenant, was); {
		case errors.Is(err, errModelServerBusy):
			// another start took the slot while this one was being prepared: answer as if it had
			// come first, for what was asked
			if !m.answerTakenServer(w, r, spec, ref, tenant) {
				fail(w, 409, "already_running", spec.name+" is starting. Try again in a moment.")
			}
		case err != nil:
			fail(w, 409, "not_started", err.Error())
		default:
			write(w, 202, m.modelServerStatus(r.Context(), spec.kind))
		}
	})(w, r, mc)
}

// answerTakenServer answers a start that finds the server starting or running, and says whether
// it did: the same model for the same organization is the state of the server that is there;
// another model, or another organization, is refused.
func (m *Module) answerTakenServer(w http.ResponseWriter, r *http.Request, spec modelServerSpec, ref string, tenant model.TenantID) bool {
	m.mu.Lock()
	cur := m.modelServers.by[spec.kind]
	busy := cur != nil && (cur.state == modelServerStarting || cur.state == modelServerRunning)
	same := busy && cur.model == ref && cur.tenant == tenant
	forOther := busy && cur.model == ref && cur.tenant != tenant
	m.mu.Unlock()
	switch {
	case same:
		write(w, 202, m.modelServerStatus(r.Context(), spec.kind))
	case forOther:
		fail(w, 409, "already_running", spec.name+" already runs this model for another organization. Stop it first.")
	case busy:
		fail(w, 409, "already_running", spec.name+" already runs another model. Stop it first.")
	default:
		return false
	}
	return true
}

// handleModelServerStop stops the server this engine started. Models stay on disk.
func (m *Module) handleModelServerStop(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		spec, ok := modelServerSpecFor(chi.URLParam(r, "kind"))
		if !ok {
			fail(w, 404, "not_found", "No such model server. Use llama-cpp or vllm.")
			return
		}
		if err := audit(r.Context(), mc, "model_server.stop", "", map[string]any{"kind": spec.kind}); err != nil {
			unavailable(w)
			return
		}
		m.modelServers.life.Lock()
		defer m.modelServers.life.Unlock()
		m.stopModelServer(spec.kind)
		if err := m.forgetModelServer(spec); err != nil {
			// stopped, but it would come back after the next restart
			fail(w, 409, "not_forgotten", spec.name+" stopped, but the record that brings it back after a restart could not be removed: "+boundedText(err.Error()))
			return
		}
		write(w, 200, m.modelServerStatus(r.Context(), spec.kind))
	})(w, r, mc)
}

func newModelServerKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "olv-" + hex.EncodeToString(b), nil
}

// lastLineWithout is the last line the child printed, cut for a status row, after the secret has
// been replaced everywhere in what it printed: a secret that straddles the cut cannot survive.
func (t *tailBuffer) lastLineWithout(secret string) string {
	t.mu.Lock()
	text := string(t.buf)
	t.mu.Unlock()
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "[key]")
	}
	lines := strings.Split(strings.TrimSpace(ansiEscape.ReplaceAllString(text, "")), "\n")
	return boundedText(strings.TrimSpace(lines[len(lines)-1]))
}

// boundedText keeps a sentence short enough for a status row.
func boundedText(s string) string {
	if len(s) > 240 {
		return strings.ToValidUTF8(s[:240], "") // a cut inside a character drops the character
	}
	return s
}

// startModelServer spawns the server and watches it until it answers or ends. The slot of the
// server is taken before anything slow happens, so two starts of the same server cannot both
// spawn a child; the one that loses gets errModelServerBusy. A person's start (was != nil) is
// kept for the next boot before the child exists, so a start that could not be kept is refused.
func (m *Module) startModelServer(spec modelServerSpec, program, ref string, modelArgs []string, readDir string, actor auth.Principal, tenant model.TenantID, was *modelServerRemembered) error {
	m.modelServers.life.Lock()
	defer m.modelServers.life.Unlock()
	m.mu.Lock()
	cfg := m.modelServers.cfg
	m.mu.Unlock()
	addr := cfg.addr(spec)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return errors.New(spec.name + " has no usable address on this node")
	}
	dir := filepath.Join(cfg.Dir, spec.kind)
	home, tmp := filepath.Join(dir, "home"), filepath.Join(dir, "tmp")
	for _, d := range []string{dir, home, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("the directory for %s could not be created on this node: %w", spec.name, err)
		}
	}
	// One service per address, on loopback only: the probe goes through the admission point,
	// which admits no other address.
	l, err := netbind.Listen(m.ctx, "tcp", addr, netbind.Policy{Component: spec.name, Purpose: "local model server"})
	switch {
	case errors.Is(err, netbind.ErrPublicPlaintextBind):
		return err
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Errorf("something else already listens on %s; stop it first", addr)
	case err != nil:
		return fmt.Errorf("%s could not bind %s: %w", spec.name, addr, err)
	}
	// With no port pinned the OS chose one; the server is given that exact address.
	addr = l.Addr().String()
	_ = l.Close()
	host, port, _ := net.SplitHostPort(addr)
	key, err := newModelServerKey()
	if err != nil {
		return errors.New("a key for " + spec.name + " could not be made on this node")
	}
	args := append(append(append([]string{}, spec.lead...), modelArgs...), "--host", host, "--port", port)
	// The child reads its program's folder (its libraries are beside it, or one folder up for
	// an environment), and its model. Never the whole filesystem or the engine user's home.
	resolved := resolvedPath(program)
	release := filepath.Dir(filepath.Dir(resolved))
	if broadReadRoot(release) {
		release = filepath.Dir(resolved)
	}
	if broadReadRoot(release) {
		return fmt.Errorf("%s sits where reading its folder would reach your whole home or filesystem; install it in a folder of its own", spec.program)
	}
	readable := []string{release}
	if readDir != "" {
		readable = append(readable, readDir)
	}

	ctx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	s := &modelServer{state: modelServerStarting, model: ref, tenant: tenant, addr: addr, key: key, stop: cancel, done: done}
	m.mu.Lock()
	if cur := m.modelServers.by[spec.kind]; cur != nil && (cur.state == modelServerStarting || cur.state == modelServerRunning) {
		m.mu.Unlock()
		cancel()
		return errModelServerBusy
	}
	if m.modelServers.by == nil {
		m.modelServers.by = map[string]*modelServer{}
	}
	m.modelServers.by[spec.kind] = s
	m.mu.Unlock()
	// give the slot back when the child never came to exist
	abort := func(err error) error {
		cancel()
		m.mu.Lock()
		s.state, s.key = modelServerStopped, ""
		m.mu.Unlock()
		close(done)
		if was != nil {
			if ferr := m.forgetModelServer(spec); ferr != nil {
				// nothing was started, but the record that starts it at boot is still there
				err = fmt.Errorf("%w; the record that brings it back after a restart could not be removed either: %s", err, boundedText(ferr.Error()))
			}
		}
		return err
	}
	if was != nil {
		if err := m.rememberModelServer(spec, *was); err != nil {
			return abort(fmt.Errorf("the engine could not keep this start, so %s would not come back after a restart: %w", spec.name, err))
		}
	}
	cmd, err := m.build()(ctx, []string{dir}, readable, program, args...)
	if err != nil {
		return abort(fmt.Errorf("%s could not be prepared on this node: %w", spec.name, err))
	}
	// The key is in the environment, never in the arguments a process listing shows.
	cmd.Env = append([]string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", spec.keyEnv + "=" + key}, spec.env...)
	cmd.Dir = home
	tail := &tailBuffer{max: maxModelServerTail}
	cmd.Stdout, cmd.Stderr = tail, tail
	// Its own process group: stopping it (or the engine) ends the server and every worker it
	// spawned, not only the first process.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = ollamaStopGrace
	m.mu.Lock()
	stopping := m.ctx.Err() != nil
	if !stopping {
		m.wg.Add(2) // under the lock Close takes before it waits, so Close sees both
	}
	m.mu.Unlock()
	if stopping {
		return abort(errors.New("the engine is stopping"))
	}
	if err := cmd.Start(); err != nil {
		m.wg.Done()
		m.wg.Done()
		return abort(fmt.Errorf("%s could not be started on this node: %w", spec.name, err))
	}
	pid := cmd.Process.Pid
	// Low priority: model work must not starve the control plane on the same host.
	_ = syscall.Setpriority(syscall.PRIO_PGRP, pid, ollamaLowPriority)
	// What the child prints may be shown in a status row; the key never is.
	lastLine := func() string { return tail.lastLineWithout(key) }
	ready := cfg.ReadyTimeout
	if ready <= 0 {
		ready = modelServerReadyTimeout
	}

	go func() { // the server ends: by Stop, by the engine stopping, or on its own
		defer m.wg.Done()
		defer close(done)
		err := cmd.Wait()
		_ = syscall.Kill(-pid, syscall.SIGKILL) // nothing of the group outlives it
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		s.key = ""
		switch {
		case s.stopping || m.ctx.Err() != nil:
			s.state, s.message = modelServerStopped, ""
		case s.state == modelServerFailed:
			// the readiness check stopped it and already said why
		default:
			s.state = modelServerFailed
			s.message = spec.name + " stopped on its own."
			if last := lastLine(); last != "" {
				s.message += " " + last
			} else if err != nil {
				s.message += " " + err.Error()
			}
		}
	}()
	go func() { // the server answers: running, and its endpoint is registered
		defer m.wg.Done()
		if !waitHTTPOK(ctx, "http://"+addr+"/health", ready) {
			m.mu.Lock()
			if s.state == modelServerStarting && !s.stopping && m.ctx.Err() == nil && ctx.Err() == nil {
				s.state, s.message = modelServerFailed, spec.name+" did not answer in time. Its last line: "+lastLine()
			}
			m.mu.Unlock()
			cancel()
			return
		}
		// /health is open on both servers. The key is only registered, and sent anywhere, once the
		// one that answered turns away a request without it: a process that took the port, or a
		// build that ignores the variable, would hand the key out or serve anyone.
		if !asksForItsKey(ctx, addr, key) {
			m.mu.Lock()
			if s.state == modelServerStarting && !s.stopping && m.ctx.Err() == nil && ctx.Err() == nil {
				s.state, s.message = modelServerFailed, spec.name+" answers without asking for its key, so it was stopped and nothing was added to Providers."
			}
			m.mu.Unlock()
			cancel()
			return
		}
		message := ""
		if cfg.Register != nil {
			// the server's own context: a Stop ends the registration of the server it stopped
			rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
			err := cfg.Register(rctx, actor, tenant, spec.name+" on this server", "http://"+addr+"/v1", key)
			rcancel()
			if err != nil {
				message = "Running, but its endpoint could not be added to Providers: " + boundedText(err.Error()) + " Stop it and start it again once that is fixed."
				slog.Warn("model server running, endpoint not registered", "server", spec.kind, "error", boundedText(err.Error()))
			}
		}
		m.mu.Lock()
		if s.state == modelServerStarting && !s.stopping {
			s.state, s.message = modelServerRunning, message
		}
		m.mu.Unlock()
	}()
	return nil
}

// asksForItsKey says whether the server at addr refuses a completion request that carries no key
// and does not refuse one that carries its key. The body is empty, so the second request is
// answered with a complaint about it and nothing is generated. The key is not sent unless the
// first request was refused.
func asksForItsKey(ctx context.Context, addr, key string) bool {
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(bearer string) int {
		req, err := http.NewRequestWithContext(ctx, "POST", "http://"+addr+"/v1/chat/completions", strings.NewReader("{}"))
		if err != nil {
			return 0
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	refused := func(code int) bool { return code == http.StatusUnauthorized || code == http.StatusForbidden }
	if !refused(status("")) {
		return false
	}
	code := status(key)
	return code != 0 && !refused(code)
}

// waitHTTPOK polls url until it answers 200, the context ends or the timeout passes.
func waitHTTPOK(ctx context.Context, url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return false
		}
		if resp, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}

// stopModelServer ends the server this engine started and waits for it, bounded.
func (m *Module) stopModelServer(kind string) {
	m.mu.Lock()
	s := m.modelServers.by[kind]
	if s == nil {
		m.mu.Unlock()
		return
	}
	if s.stop == nil { // a row that only says why it did not come back
		s.state, s.message = modelServerStopped, ""
		m.mu.Unlock()
		return
	}
	s.stopping = true
	stop, done := s.stop, s.done
	m.mu.Unlock()
	stop()
	select {
	case <-done:
	case <-time.After(ollamaStopGrace + 5*time.Second):
	}
	// Stopping is the person's answer to a server that failed: the row no longer says it failed.
	m.mu.Lock()
	if s.state == modelServerFailed {
		s.state, s.message = modelServerStopped, ""
	}
	m.mu.Unlock()
}

func (m *Module) modelServerStateFile(spec modelServerSpec) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modelServers.cfg.Dir == "" {
		return ""
	}
	return filepath.Join(m.modelServers.cfg.Dir, spec.kind+".json")
}

// rememberModelServer keeps the person's start; forgetModelServer drops it on Stop.
func (m *Module) rememberModelServer(spec modelServerSpec, was modelServerRemembered) error {
	path := m.modelServerStateFile(spec)
	if path == "" {
		return errors.New("no place to keep it")
	}
	was.StartedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.Marshal(was)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (m *Module) forgetModelServer(spec modelServerSpec) error {
	path := m.modelServerStateFile(spec)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// RestartModelServers starts again, at engine start, each server a person started and did
// not stop: after a restart it stayed "stopped" until someone pressed Start, and the sessions
// bound to it failed meanwhile. The engine starts it as itself, for the tenant and model the
// person named, with a new key; one system audit row is written first, and with no audit row
// nothing is started. Whatever keeps it from coming back is said on its row.
func (m *Module) RestartModelServers() {
	m.mu.Lock()
	cfg := m.modelServers.cfg
	m.mu.Unlock()
	if m.readOnly || cfg.Dir == "" || cfg.Audit == nil {
		return
	}
	for _, spec := range modelServerSpecs {
		m.restartModelServer(spec, cfg)
	}
}

func (m *Module) restartModelServer(spec modelServerSpec, cfg ModelServerConfig) {
	raw, err := os.ReadFile(m.modelServerStateFile(spec))
	if errors.Is(err, os.ErrNotExist) {
		return // nobody started it
	}
	failed := func(why string) {
		m.mu.Lock()
		if m.modelServers.by == nil {
			m.modelServers.by = map[string]*modelServer{}
		}
		m.modelServers.by[spec.kind] = &modelServer{state: modelServerFailed, message: spec.name + " was running before the engine restarted; " + why}
		m.mu.Unlock()
	}
	if err != nil {
		failed("the record of its start could not be read, so it was not started again.")
		return
	}
	var was modelServerRemembered
	if json.Unmarshal(raw, &was) != nil {
		failed("the record of its start is not readable, so it was not started again.")
		return
	}
	tenant, err := model.ParseTenantID(was.TenantID)
	if err != nil || tenant.IsZero() {
		failed("the record of its start names no organization, so it was not started again.")
		return
	}
	program, err := m.findProgram(spec.program)
	if err != nil {
		failed(spec.program + " is no longer on the engine's PATH, so it was not started again.")
		return
	}
	args, readDir, err := spec.check(was.Model)
	if err != nil {
		failed("it was not started again: " + err.Error())
		return
	}
	actor, err := auth.NewSystemOperator("engine start", "restart the "+spec.name+" a person started before the engine stopped")
	if err != nil {
		failed("the engine could not act for itself, so it was not started again.")
		return
	}
	// The person's Start was audited (handleModelServerStart); this start is the engine's,
	// once per boot, and is recorded as such before it happens.
	reason := "restarted with the engine; started by " + boundedText(was.StartedBy) + " at " + boundedText(was.StartedAt)
	if err := cfg.Audit(m.ctx, model.AuditDraft{
		Actor: actor.Actor(), ActorKind: actor.ActorKind(), Action: "agenttools.model_server.restart", TargetKind: "agenttools.job",
		Meta: map[string]any{"kind": spec.kind, "reason": reason, "register_in": was.TenantID, "model": was.Model},
	}); err != nil {
		failed("it was not started again because its audit record could not be written.")
		return
	}
	if err := m.startModelServer(spec, program, was.Model, args, readDir, actor, tenant, nil); err != nil {
		failed("it could not start again: " + err.Error())
	}
}
