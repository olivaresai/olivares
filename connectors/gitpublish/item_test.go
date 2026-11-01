// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubGetItem(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer t" {
			t.Errorf("request = %s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/repos/acme/widgets/issues/17":
			_, _ = io.WriteString(w, `{"number":17,"title":"Crash on save","body":"Steps:\n1. save","html_url":"https://github.com/acme/widgets/issues/17"}`)
		case "/repos/acme/widgets/issues/18":
			_, _ = io.WriteString(w, `{"number":18,"title":"Fix save","body":null,"html_url":"https://github.com/acme/widgets/pull/18","pull_request":{"url":"x"}}`)
		case "/repos/acme/widgets/issues/19":
			_, _ = io.WriteString(w, `{"number":19,"title":"Elsewhere","body":"","html_url":"https://github.com/evil/other/issues/19"}`)
		case "/repos/acme/widgets/issues/20":
			_, _ = io.WriteString(w, `{"number":20,"title":"Huge","body":"`+strings.Repeat("\u00e9", maxItemBody)+`","html_url":"https://github.com/acme/widgets/issues/20"}`)
		default:
			http.NotFound(w, r)
		}
	})
	g := githubFor(t, d, key)
	tok := Token{secret: NewSecret("t")}
	ctx := context.Background()

	issue, err := g.GetItem(ctx, tok, 17)
	if err != nil || issue != (Item{Number: 17, Title: "Crash on save", Body: "Steps:\n1. save", URL: "https://github.com/acme/widgets/issues/17"}) {
		t.Fatalf("issue = %+v, %v", issue, err)
	}
	pr, err := g.GetItem(ctx, tok, 18)
	if err != nil || !pr.PullRequest || pr.Title != "Fix save" || pr.Body != "" || pr.URL != "https://github.com/acme/widgets/pull/18" {
		t.Fatalf("pull request = %+v, %v", pr, err)
	}
	if _, err := g.GetItem(ctx, tok, 19); err == nil {
		t.Fatal("an item whose URL is another repository was accepted")
	}
	if _, err := g.GetItem(ctx, tok, 99); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing item = %v; want ErrItemNotFound", err)
	}
	huge, err := g.GetItem(ctx, tok, 20)
	if err != nil || len(huge.Body) > maxItemBody || !strings.HasSuffix(huge.Body, "\u00e9") {
		t.Fatalf("huge body = %d bytes, %v; want at most %d, cut on a character", len(huge.Body), err, maxItemBody)
	}
}

func TestItemReadAsksOnlyForRead(t *testing.T) {
	got := permissionsFor(EffectItemRead)
	if len(got) != 2 || got["issues"] != "read" || got["pull_requests"] != "read" {
		t.Fatalf("permissions = %v; want issues and pull_requests, read only", got)
	}
}

func TestGetItemNeedsAnItemHost(t *testing.T) {
	var host Host = (*GitLab)(nil)
	if _, err := GetItem(context.Background(), host, Token{}, 1); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("GitLab = %v; want ErrNotSupported", err)
	}
}
