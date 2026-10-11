// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// EgressPolicyReader is the existing tenant policy read, evaluated at each dial.
// A missing or failed reader is not an absent policy.
type EgressPolicyReader interface {
	EgressPolicy(context.Context, model.TenantID) (egress.Policy, error)
}

// GitImporter uses a bounded bare Git process behind an engine HTTPS relay. Git
// can reach only that relay, not resolve/dial the source again, consult credentials
// or execute checkout filters/hooks. Linux prlimit protects the engine from pack
// allocation/expansion attacks; unsupported hosts use archive uploads.
type GitImporter struct {
	ScratchRoot string
	Policy      EgressPolicyReader
	Resolver    egress.Resolver
	Dialer      egress.Dialer
	TLSConfig   *tls.Config // engine trust configuration, never a source input
}

var gitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)
var gitRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,199}$`)

func validateGitSource(raw, ref, subdir string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00\\") {
		return nil, refuse("unsupported_source", "git URL")
	}
	host, err := egress.CanonicalHost(u.Hostname())
	if err != nil {
		return nil, refuse("unsupported_source", "git host")
	}
	dest, err := egress.ParseDestination(u.String())
	if err != nil {
		return nil, refuse("unsupported_source", "git destination")
	}
	if strings.Contains(u.Path, "..") || u.Path == "" {
		return nil, refuse("unsupported_source", "git path")
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(dest.Port))
	if dest.Port == 443 {
		u.Host = host
		if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
	}
	if !gitRef.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") || (subdir != "" && !safePath(subdir)) {
		return nil, refuse("unsupported_source", "git ref/subdirectory")
	}
	return u, nil
}

func (g GitImporter) Import(ctx context.Context, tenant model.TenantID, raw, ref, subdir string) (pack *ValidatedPack, resolved string, importErr error) {
	origin, err := validateGitSource(raw, ref, subdir)
	if err != nil {
		return nil, "", err
	}
	if g.Policy == nil || g.ScratchRoot == "" {
		return nil, "", refuse("source_unavailable", "git transport not configured")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, "", refuse("source_unavailable", "git unavailable")
	}
	limiter, err := exec.LookPath("prlimit")
	if err != nil {
		return nil, "", refuse("source_unavailable", "bounded git unavailable; upload an archive")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := os.MkdirAll(g.ScratchRoot, 0700); err != nil {
		return nil, "", refuse("source_unavailable", "staging unavailable")
	}
	dir, err := os.MkdirTemp(g.ScratchRoot, "git-")
	if err != nil {
		return nil, "", refuse("source_unavailable", "staging unavailable")
	}
	defer func() {
		// This fresh, owned bare tree never contains a checkout or caller paths.
		if err := os.RemoveAll(dir); err != nil {
			pack = nil
			resolved = ""
			importErr = refuse("source_unavailable", "staging cleanup failed")
		}
	}()
	empty := filepath.Join(dir, "empty-template")
	if err := os.Mkdir(empty, 0700); err != nil {
		return nil, "", refuse("source_unavailable", "staging unavailable")
	}
	run := func(max int, args ...string) ([]byte, error) {
		common := []string{"--as=536870912", "--cpu=60", "--fsize=33554432", "--", git, "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.http.allow=always", "-c", "http.proxy=", "-c", "http.followRedirects=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "pack.threads=1", "-c", "index.threads=1", "-c", "core.deltaBaseCacheLimit=8388608", "-c", "core.bigFileThreshold=8388608", "-c", "fetch.unpackLimit=0", "-c", "fetch.writeCommitGraph=false", "-c", "transfer.fsckObjects=true"}
		cmd, err := boundedGitCommand(ctx, limiter, append(common, args...)...)
		if err != nil {
			return nil, err
		}
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "GIT_PROTOCOL_FROM_USER=0", "LC_ALL=C"}
		var out limitedBuffer
		out.max = max
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		runErr := cmd.Run()
		stopGitGroup(cmd)
		if err := runErr; err != nil {
			if out.exceeded {
				return nil, refuse("import_limit", "git output")
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, refuse("source_unavailable", "git read failed")
		}
		return out.data.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if _, err := run(1024, "init", "--bare", "--quiet", "--template="+empty, "."); err != nil {
		return nil, "", err
	}
	var budget atomic.Int64
	budget.Store(MaxSourceBytes)
	client := g.client(ctx, tenant)
	defer client.CloseIdleConnections()
	var connections sync.WaitGroup
	listener, err := netbind.Listen(ctx, "tcp", "127.0.0.1:0", netbind.Policy{Component: "skills", Purpose: "Git relay"})
	if err != nil {
		return nil, "", refuse("source_unavailable", "git relay unavailable")
	}
	nonce := string(model.NewID())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := strings.TrimPrefix(r.URL.Path, "/"+nonce)
		if r.URL.Path != "/"+nonce+endpoint || !((r.Method == "GET" && endpoint == "/info/refs" && r.URL.RawQuery == "service=git-upload-pack") || (r.Method == "POST" && endpoint == "/git-upload-pack" && r.URL.RawQuery == "")) {
			http.Error(w, "refused", 403)
			return
		}
		target := *origin
		target.Path = strings.TrimSuffix(origin.Path, "/") + endpoint
		target.RawPath = ""
		target.RawQuery = r.URL.RawQuery
		request, err := http.NewRequestWithContext(ctx, r.Method, target.String(), io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "refused", 502)
			return
		}
		request.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		request.Header.Set("Git-Protocol", "version=1")
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "refused", 502)
			return
		}
		defer func() { _ = response.Body.Close() }()
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		buffer := make([]byte, 32<<10)
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if budget.Add(-int64(n)) < 0 {
					cancel()
					return
				}
				if _, err := w.Write(buffer[:n]); err != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 60 * time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				connections.Add(1)
			case http.StateClosed, http.StateHijacked:
				connections.Done()
			}
		},
	}
	closed := make(chan struct{})
	go func() { defer close(closed); _ = server.Serve(listener) }()
	defer func() {
		cancel()
		_ = server.Close()
		<-closed
		connections.Wait()
	}()
	local := "http://" + listener.Addr().String() + "/" + nonce
	if _, err := run(4096, "fetch", "--quiet", "--depth=1", "--no-tags", "--no-recurse-submodules", "--", local, ref); err != nil {
		if budget.Load() < 0 {
			return nil, "", refuse("import_limit", "git network bytes")
		}
		return nil, "", err
	}
	hash, err := run(128, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return nil, "", err
	}
	commit := strings.TrimSpace(string(hash))
	if !gitHash.MatchString(commit) || (gitHash.MatchString(ref) && commit != ref) {
		return nil, "", refuse("source_changed", "git commit")
	}
	listArgs := []string{"ls-tree", "-r", "-z", commit}
	if subdir != "" {
		listArgs = append(listArgs, "--", subdir)
	}
	listing, err := run(2<<20, listArgs...)
	if err != nil {
		return nil, "", err
	}
	entries := bytes.Split(bytes.TrimSuffix(listing, []byte{0}), []byte{0})
	if len(entries) > MaxFiles {
		return nil, "", refuse("import_limit", "git entry count")
	}
	prefix := ""
	if subdir != "" {
		for _, entry := range entries {
			fields := bytes.SplitN(entry, []byte{'\t'}, 2)
			if len(fields) == 2 && string(fields[1]) == subdir+"/SKILL.md" {
				prefix = path.Base(subdir)
				break
			}
		}
	}
	tree := newTree()
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		fields := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(fields) != 2 {
			return nil, "", refuse("unsafe_pack", "git tree")
		}
		attrs := strings.Fields(string(fields[0]))
		name := string(fields[1])
		if len(attrs) != 3 || !gitHash.MatchString(attrs[2]) {
			return nil, "", refuse("unsafe_pack", "git tree")
		}
		if subdir != "" {
			if !strings.HasPrefix(name, subdir+"/") {
				continue
			}
			name = strings.TrimPrefix(name, subdir+"/")
		}
		if prefix != "" {
			name = prefix + "/" + name
		}
		if attrs[1] != "blob" || (attrs[0] != "100644" && attrs[0] != "100755") {
			return nil, "", refuse("unsafe_pack", name)
		}
		if err := tree.entry(name, false); err != nil {
			return nil, "", err
		}
		body, err := run(tree.fileLimit(name), "cat-file", "blob", attrs[2])
		if err != nil {
			return nil, "", err
		}
		mode := os.FileMode(0644)
		if attrs[0] == "100755" {
			mode = 0755
		}
		if err := tree.file(name, body, mode); err != nil {
			return nil, "", err
		}
	}
	packFiles, err := filepath.Glob(filepath.Join(dir, "objects", "pack", "*.pack"))
	if err != nil || len(packFiles) != 1 {
		return nil, "", refuse("source_unavailable", "git artifact unavailable")
	}
	artifact, err := os.Open(packFiles[0])
	if err != nil {
		return nil, "", refuse("source_unavailable", "git artifact unavailable")
	}
	rawPack, err := boundedRead(ctx, artifact, MaxSourceBytes)
	_ = artifact.Close()
	if err != nil {
		return nil, "", err
	}
	result, err := tree.validate(digest(rawPack))
	return result, commit, err
}

type limitedBuffer struct {
	data     bytes.Buffer
	max      int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.data.Len() {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.data.Write(p)
}
func (g GitImporter) client(_ context.Context, tenant model.TenantID) *http.Client {
	dialer := g.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second}
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: g.TLSConfig, TLSHandshakeTimeout: 10 * time.Second, DisableCompression: true}
	transport.DialContext = func(call context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, refuse("unsupported_source", "git destination")
		}
		u, _ := url.Parse("https://" + net.JoinHostPort(host, port))
		dest, err := egress.ParseDestination(u.String())
		if err != nil {
			return nil, refuse("unsupported_source", "git destination")
		}
		policy, err := g.Policy.EgressPolicy(call, tenant)
		if err != nil {
			return nil, refuse("source_unavailable", "egress policy unavailable")
		}
		ips, err := egress.Resolve(call, g.Resolver, dest)
		if err != nil {
			return nil, refuse("source_unavailable", "git destination unavailable")
		}
		decision := egress.Evaluate(policy, dest, ips)
		if !decision.Permitted || len(decision.Pin) == 0 {
			return nil, refuse("source_unavailable", "git destination refused")
		}
		for _, ip := range decision.Pin {
			if egress.ReservedAddress(ip) && !decision.Lifts(ip) {
				return nil, refuse("source_unavailable", "git destination refused")
			}
		}
		return egress.DialPinned(egress.WithPin(call, decision.Pin), dialer, network, address)
	}
	return &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 || r.URL.Scheme != "https" || r.URL.User != nil || r.URL.Fragment != "" || r.URL.Opaque != "" || (r.URL.RawQuery != "" && r.URL.RawQuery != "service=git-upload-pack") {
			return refuse("unsupported_source", "git redirect")
		}
		return nil
	}}
}
