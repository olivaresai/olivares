// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

func githubFor(t *testing.T, d Doer, key Secret) *GitHub {
	t.Helper()
	g, err := NewGitHub(GitHubConfig{
		APIBase:        "https://api.github.com",
		AppID:          "4242",
		InstallationID: "77",
		Key:            key,
		Owner:          "acme",
		Repo:           "widgets",
		Now:            func() time.Time { return fixedNow },
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// mintHandler answers the installation-token call with the given repositories.
func mintHandler(t *testing.T, pub *rsa.PublicKey, repos []string, gotBody *map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens" {
			verifyJWT(t, pub, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			b, _ := io.ReadAll(r.Body)
			if gotBody != nil {
				_ = json.Unmarshal(b, gotBody)
			}
			list := make([]map[string]any, 0, len(repos))
			for _, n := range repos {
				list = append(list, map[string]any{"name": n, "full_name": "acme/" + n})
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_stateless_APPID_JWT_not_40_chars_" + strings.Repeat("x", 30), "expires_at": fixedNow.Add(time.Hour).Format(time.RFC3339), "repositories": list})
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}
}

func verifyJWT(t *testing.T, pub *rsa.PublicKey, tok string) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	var h map[string]any
	_ = json.Unmarshal(hdr, &h)
	if h["alg"] != "RS256" {
		t.Fatalf("alg = %v", h["alg"])
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("jwt signature: %v", err)
	}
	var c map[string]any
	_ = json.Unmarshal(body, &c)
	if c["iss"] != "4242" {
		t.Fatalf("iss = %v, want the App ID", c["iss"])
	}
	iat, exp := int64(c["iat"].(float64)), int64(c["exp"].(float64))
	if iat != fixedNow.Add(-60*time.Second).Unix() {
		t.Fatalf("iat = %d", iat)
	}
	if exp <= fixedNow.Unix() || exp > fixedNow.Add(10*time.Minute).Unix() {
		t.Fatalf("exp = %d outside (now, now+10m]", exp)
	}
}

func TestInstallationTokenNarrowedToOneRepositoryAndEffect(t *testing.T) {
	k, key := testKey(t)
	cases := map[Effect]map[string]string{
		EffectPush:        {"contents": "write"},
		EffectPullRequest: {"pull_requests": "write", "contents": "read"},
		EffectMerge:       {"contents": "write", "pull_requests": "read"},
		EffectObserve:     {"contents": "read", "pull_requests": "read"},
	}
	for eff, want := range cases {
		var body map[string]any
		d := newFake(t, mintHandler(t, &k.PublicKey, []string{"widgets"}, &body))
		g := githubFor(t, d, key)
		tok, err := g.Mint(context.Background(), eff)
		if err != nil {
			t.Fatalf("%s: mint: %v", eff, err)
		}
		if repos, _ := body["repositories"].([]any); len(repos) != 1 || repos[0] != "widgets" {
			t.Fatalf("%s: repositories = %v", eff, body["repositories"])
		}
		perms, _ := body["permissions"].(map[string]any)
		if len(perms) != len(want) {
			t.Fatalf("%s: permissions = %v, want %v", eff, perms, want)
		}
		for k, v := range want {
			if perms[k] != v {
				t.Fatalf("%s: permission %s = %v, want %s", eff, k, perms[k], v)
			}
		}
		if _, ok := perms["workflows"]; ok {
			t.Fatalf("%s: the Workflows permission must never be requested", eff)
		}
		if tok.Value().Reveal() == "" || !tok.ExpiresAt.After(fixedNow) {
			t.Fatalf("%s: token = %+v", eff, tok)
		}
		reqs := d.requests()
		if v := reqs[0].Header.Get("X-GitHub-Api-Version"); v != GitHubAPIVersion {
			t.Fatalf("api version header = %q, want %q", v, GitHubAPIVersion)
		}
	}
	if GitHubAPIVersion != "2026-03-10" {
		t.Fatalf("pinned API version = %q", GitHubAPIVersion)
	}
}

func TestMintedTokenWiderThanTheRepositoryIsReleasedAndRefused(t *testing.T) {
	k, key := testKey(t)
	d := newFake(t, mintHandler(t, &k.PublicKey, []string{"widgets", "other"}, nil))
	g := githubFor(t, d, key)
	_, err := g.Mint(context.Background(), EffectPush)
	if !errors.Is(err, ErrTokenScope) {
		t.Fatalf("err = %v, want ErrTokenScope", err)
	}
	var revoked bool
	for _, r := range d.requests() {
		if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
			revoked = true
		}
	}
	if !revoked {
		t.Fatal("a token with a wider repository list must be released before refusing")
	}
}

func TestRevokedInstallationRefusesMint(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-GitHub-Request-Id", "ABCD:1234")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Integration not found ghs_leak"}`)
	})
	g := githubFor(t, d, key)
	_, err := g.Mint(context.Background(), EffectPush)
	var he *HostError
	if !errors.As(err, &he) || !errors.Is(err, ErrCredentialRefused) {
		t.Fatalf("err = %v, want a HostError wrapping ErrCredentialRefused", err)
	}
	if he.Status != 404 || he.RequestID != "ABCD:1234" || he.Code != "not_found" {
		t.Fatalf("host error = %+v", he)
	}
	if strings.Contains(err.Error(), "Integration") || strings.Contains(err.Error(), "ghs_leak") {
		t.Fatalf("a host body reached the error: %q", err.Error())
	}
}

func TestReleaseCallsRevoke(t *testing.T) {
	k, key := testKey(t)
	d := newFake(t, mintHandler(t, &k.PublicKey, []string{"widgets"}, nil))
	g := githubFor(t, d, key)
	tok, err := g.Mint(context.Background(), EffectObserve)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Release(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	reqs := d.requests()
	last := reqs[len(reqs)-1]
	if last.Method != http.MethodDelete || last.Header.Get("Authorization") != "Bearer "+tok.Value().Reveal() {
		t.Fatalf("release request = %s %s", last.Method, last.URL.Path)
	}
}

func TestHostSecretNeverRendered(t *testing.T) {
	s := NewSecret("ghs_SENTINEL_value")
	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "tok", s)
	js, _ := json.Marshal(struct{ S Secret }{s})
	outs := []string{
		fmt.Sprintf("%v %+v %#v %s %q", s, s, s, s, s),
		buf.String(),
		string(js),
		fmt.Errorf("wrap: %v", s).Error(),
		fmt.Sprint(Token{secret: s}),
	}
	for _, o := range outs {
		if strings.Contains(o, "SENTINEL") {
			t.Fatalf("secret rendered: %q", o)
		}
	}
	if s.Reveal() != "ghs_SENTINEL_value" {
		t.Fatal("Reveal must return the value to the one caller that needs it")
	}
}

func TestEndpointRules(t *testing.T) {
	bad := []string{
		"http://api.github.com", "https://127.0.0.1", "https://[::1]", "https://user:pw@api.github.com",
		"https://evil.example", "https://169.254.169.254", "https://api.github.com/../x?y=1",
	}
	for _, b := range bad {
		if err := ValidateEndpoint(b, nil); err == nil {
			t.Fatalf("%q accepted", b)
		}
	}
	if err := ValidateEndpoint("https://ghe.corp.example/api/v3", []string{"ghe.corp.example"}); err != nil {
		t.Fatalf("allowlisted GHES refused: %v", err)
	}
	if err := ValidateEndpoint("https://api.github.com", nil); err != nil {
		t.Fatal(err)
	}
	c := NewHTTPClient(5 * time.Second)
	if c.CheckRedirect == nil || c.CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("the adapter client must refuse every redirect")
	}
}

func TestPushTargetBuiltFromBinding(t *testing.T) {
	_, key := testKey(t)
	g := githubFor(t, newFake(t, http.NotFound), key)
	u, scheme, hdr := g.PushTarget(Token{secret: NewSecret("tok123")})
	if u != "https://github.com/acme/widgets.git" || scheme != "https" {
		t.Fatalf("push target = %s %s", u, scheme)
	}
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:tok123"))
	if hdr.Reveal() != want {
		t.Fatalf("header = %q", hdr.Reveal())
	}
	gh, _ := NewGitHub(GitHubConfig{APIBase: "https://ghe.corp.example/api/v3", AllowedHosts: []string{"ghe.corp.example"}, AppID: "1", InstallationID: "2", Key: key, Owner: "o", Repo: "r"}, newFake(t, http.NotFound))
	if u, _, _ := gh.PushTarget(Token{secret: NewSecret("t")}); u != "https://ghe.corp.example/o/r.git" {
		t.Fatalf("GHES push target = %s", u)
	}
}

func TestRefObservations(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/git/ref/heads/olivares/a":
			_, _ = io.WriteString(w, `{"object":{"sha":"aaa"}}`)
		case "/repos/acme/widgets/git/ref/heads/olivares/hidden":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	})
	g := githubFor(t, d, key)
	tok := Token{secret: NewSecret("t")}
	if o, err := g.Ref(context.Background(), tok, "olivares/a"); err != nil || o.State != RefPresent || o.SHA != "aaa" {
		t.Fatalf("present: %+v %v", o, err)
	}
	// A 404 is absence OR a permission-hidden ref: it is reported as such and is
	// never proof that the ref does not exist.
	if o, err := g.Ref(context.Background(), tok, "olivares/hidden"); err != nil || o.State != RefNotFoundOrHidden {
		t.Fatalf("hidden: %+v %v", o, err)
	}
	if o, err := g.Ref(context.Background(), tok, "olivares/down"); err == nil || o.State != RefUnknown {
		t.Fatalf("5xx must be unknown with an error: %+v %v", o, err)
	}
}

func TestOpenChangesIncompleteLookup(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("lookup state = %q, want open only", r.URL.Query().Get("state"))
		}
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Link", `<https://api.github.com/repos/acme/widgets/pulls?state=open&page=2>; rel="next"`)
		_, _ = io.WriteString(w, `[{"number":3,"state":"open","head":{"ref":"olivares/x","sha":"c1"},"base":{"ref":"main"}}]`)
	})
	g := githubFor(t, d, key)
	_, err := g.OpenChanges(context.Background(), Token{secret: NewSecret("t")}, "olivares/x", "main")
	if !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("err = %v, want ErrLookupIncomplete", err)
	}
	// A next link to another origin is never followed.
	d2 := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://evil.example/steal?page=2>; rel="next"`)
		_, _ = io.WriteString(w, `[]`)
	})
	g2 := githubFor(t, d2, key)
	if _, err := g2.OpenChanges(context.Background(), Token{secret: NewSecret("t")}, "olivares/x", "main"); !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("cross-origin next: err = %v", err)
	}
}

func TestCreateAndMergeClassification(t *testing.T) {
	_, key := testKey(t)
	status := 0
	var mergeBodies []map[string]any
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			mergeBodies = append(mergeBodies, m)
		}
		w.Header().Set("X-GitHub-Request-Id", "RID-1")
		if status == 0 {
			// the connection dies after the host received the request
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			_ = c.Close()
			return
		}
		w.WriteHeader(status)
		if status < 300 {
			_, _ = io.WriteString(w, `{"number":9,"state":"open","head":{"ref":"olivares/x","sha":"c1"},"base":{"ref":"main"},"merged":true,"sha":"m1"}`)
		} else {
			_, _ = io.WriteString(w, `{"message":"secret body"}`)
		}
	})
	g := githubFor(t, d, key)
	tok := Token{secret: NewSecret("t")}
	ctx := context.Background()
	for _, c := range []struct {
		status int
		class  Class
		reason string
	}{{201, Applied, ""}, {422, Rejected, "validation_failed"}, {403, Rejected, "forbidden"}, {502, Ambiguous, "server_error"}, {429, Ambiguous, "rate_limited"}, {0, Ambiguous, "transport"}} {
		status = c.status
		_, res := g.CreateChange(ctx, tok, ChangeSpec{Head: "olivares/x", Base: "main", Title: "t"})
		if res.Class != c.class || res.Reason != c.reason {
			t.Fatalf("create %d: %+v, want %v %s", c.status, res, c.class, c.reason)
		}
	}
	for _, c := range []struct {
		status int
		class  Class
		reason string
	}{{200, Applied, ""}, {409, Rejected, "head_mismatch"}, {405, Rejected, "not_mergeable"}, {500, Ambiguous, "server_error"}, {0, Ambiguous, "transport"}} {
		status = c.status
		_, res := g.MergeChange(ctx, tok, 9, "c1", "merge")
		if res.Class != c.class || res.Reason != c.reason {
			t.Fatalf("merge %d: %+v, want %v %s", c.status, res, c.class, c.reason)
		}
		if c.status >= 300 && res.Host.RequestID != "RID-1" {
			t.Fatalf("merge %d: request id not kept: %+v", c.status, res.Host)
		}
	}
	if len(mergeBodies) == 0 {
		t.Fatal("no merge request reached the host")
	}
	for _, m := range mergeBodies {
		if m["sha"] != "c1" || m["merge_method"] != "merge" {
			t.Fatalf("merge body = %v", m)
		}
		if _, ok := m["auto_merge"]; ok {
			t.Fatal("auto_merge must never be sent")
		}
	}
}

func TestBranchReportsDefaultAndProtection(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets":
			_, _ = io.WriteString(w, `{"default_branch":"main"}`)
		case "/repos/acme/widgets/branches/olivares/rel":
			_, _ = io.WriteString(w, `{"name":"olivares/rel","protected":true}`)
		case "/repos/acme/widgets/branches/olivares/new":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	})
	g := githubFor(t, d, key)
	tok := Token{secret: NewSecret("t")}
	if b, err := g.Branch(context.Background(), tok, "olivares/rel"); err != nil || b.Default != "main" || !b.Exists || !b.Protected {
		t.Fatalf("protected = %+v %v", b, err)
	}
	if b, err := g.Branch(context.Background(), tok, "olivares/new"); err != nil || b.Default != "main" || b.Exists || b.Protected {
		t.Fatalf("new branch = %+v %v", b, err)
	}
	if _, err := g.Branch(context.Background(), tok, "olivares/down"); err == nil {
		t.Fatal("a failed branch read must be an error, never 'unprotected'")
	}
}
