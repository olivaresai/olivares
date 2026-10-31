// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestGitHubIssueComments(t *testing.T) {
	_, key := testKey(t)
	created := time.Date(2026, 10, 26, 7, 30, 0, 0, time.UTC)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/17/comments":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["body"] != "Olivares run: completed" || len(body) != 1 {
				t.Errorf("comment body = %v", body)
			}
			w.Header().Set("X-GitHub-Request-Id", "comment-rid")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":901,"html_url":"https://github.com/acme/widgets/issues/17#issuecomment-901","body":"Olivares run: completed","created_at":"2026-10-26T07:30:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/17/comments":
			if r.URL.Query().Get("per_page") != "100" {
				t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
			}
			_, _ = io.WriteString(w, `[{"id":901,"html_url":"https://github.com/acme/widgets/issues/17#issuecomment-901","body":"Olivares run: completed","created_at":"2026-10-26T07:30:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	})
	g := githubFor(t, d, key)
	tok := Token{secret: NewSecret("t")}
	comment, res := g.CreateComment(context.Background(), tok, CommentSpec{Number: 17, Body: "Olivares run: completed"})
	if res.Class != Applied || res.Host.RequestID != "comment-rid" || comment.ID != "901" || comment.URL != "https://github.com/acme/widgets/issues/17#issuecomment-901" || !comment.CreatedAt.Equal(created) {
		t.Fatalf("create = %+v %+v", comment, res)
	}
	comments, err := g.ListComments(context.Background(), tok, 17)
	if err != nil || len(comments) != 1 || comments[0] != comment {
		t.Fatalf("list = %+v, %v; want %+v", comments, err, comment)
	}
}

func TestGitHubIssueCommentWriteClassification(t *testing.T) {
	_, key := testKey(t)
	status := http.StatusCreated
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusCreated {
			_, _ = io.WriteString(w, `{"id":901,"body":"ok"}`)
			return
		}
		_, _ = io.WriteString(w, `{"message":"host body must not escape"}`)
	})
	g := githubFor(t, d, key)
	for _, tc := range []struct {
		status int
		class  Class
		reason string
	}{{http.StatusUnprocessableEntity, Rejected, "validation_failed"}, {http.StatusForbidden, Rejected, "forbidden"}, {http.StatusTooManyRequests, Ambiguous, "rate_limited"}, {http.StatusBadGateway, Ambiguous, "server_error"}} {
		status = tc.status
		_, res := g.CreateComment(context.Background(), Token{secret: NewSecret("t")}, CommentSpec{Number: 17, Body: "ok"})
		if res.Class != tc.class || res.Reason != tc.reason {
			t.Fatalf("%d = %+v, want %s %q", tc.status, res, tc.class, tc.reason)
		}
	}
}

func TestGitHubIssueCommentLookupIsBounded(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://evil.example/steal?page=2>; rel="next"`)
		_, _ = io.WriteString(w, `[]`)
	})
	g := githubFor(t, d, key)
	if _, err := g.ListComments(context.Background(), Token{secret: NewSecret("t")}, 17); !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("cross-origin next: %v", err)
	}
}

func TestGitLabIssueNotes(t *testing.T) {
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/api/v4/projects/acme%2Ftools%2Fwidgets/issues/17/notes":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["body"] != "Olivares run: completed" || len(body) != 1 {
				t.Errorf("note body = %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":902,"body":"Olivares run: completed","created_at":"2026-10-26T07:30:00Z"}`)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/v4/projects/acme%2Ftools%2Fwidgets/issues/17/notes":
			if r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
				t.Errorf("query = %v", r.URL.Query())
			}
			_, _ = io.WriteString(w, `[{"id":902,"body":"Olivares run: completed","created_at":"2026-10-26T07:30:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	})
	g := gitlabFor(t, d)
	tok := Token{secret: NewSecret("glpat-bot")}
	comment, res := g.CreateComment(context.Background(), tok, CommentSpec{Number: 17, Body: "Olivares run: completed"})
	if res.Class != Applied || comment.ID != "902" || comment.Body != "Olivares run: completed" || comment.URL != "https://gitlab.com/acme/tools/widgets/-/issues/17#note_902" || comment.CreatedAt.IsZero() {
		t.Fatalf("create = %+v %+v", comment, res)
	}
	comments, err := g.ListComments(context.Background(), tok, 17)
	if err != nil || len(comments) != 1 || comments[0] != comment {
		t.Fatalf("list = %+v, %v; want %+v", comments, err, comment)
	}
}

func TestGitHubIssueCommentRejectsUnsafeEvidenceURL(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":901,"html_url":"http://evil.example/comment","body":"ok"}`)
	})
	g := githubFor(t, d, key)
	if _, res := g.CreateComment(context.Background(), Token{secret: NewSecret("t")}, CommentSpec{Number: 17, Body: "ok"}); res.Class != Ambiguous || res.Reason != "malformed_response" {
		t.Fatalf("unsafe evidence URL = %+v", res)
	}
}

func TestGitHubIssueCommentListRejectsUnsafeEvidenceURL(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":901,"html_url":"https://evil.example/comment","body":"ok"}]`)
	})
	g := githubFor(t, d, key)
	if _, err := g.ListComments(context.Background(), Token{secret: NewSecret("t")}, 17); !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("unsafe listed evidence URL: %v", err)
	}
}

func TestGitHubIssueCommentPullRequestURL(t *testing.T) {
	for name, tc := range map[string]struct{ apiBase, htmlURL string }{
		"cloud":               {"https://api.github.com", "https://github.com/acme/widgets/pull/17#issuecomment-901"},
		"enterprise port 443": {"https://ghe.example:443/api/v3", "https://ghe.example/acme/widgets/pull/17#issuecomment-901"},
	} {
		t.Run(name, func(t *testing.T) {
			_, key := testKey(t)
			d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"id":901,"html_url":`+strconv.Quote(tc.htmlURL)+`,"body":"ok"}`)
					return
				}
				_, _ = io.WriteString(w, `[{"id":901,"html_url":`+strconv.Quote(tc.htmlURL)+`,"body":"ok"}]`)
			})
			g := githubFor(t, d, key)
			g.cfg.APIBase = tc.apiBase
			comment, res := g.CreateComment(context.Background(), Token{secret: NewSecret("t")}, CommentSpec{Number: 17, Body: "ok"})
			if res.Class != Applied || comment.URL != tc.htmlURL {
				t.Fatalf("pull-request comment = %+v %+v", comment, res)
			}
			comments, err := g.ListComments(context.Background(), Token{secret: NewSecret("t")}, 17)
			if err != nil || len(comments) != 1 || comments[0] != comment {
				t.Fatalf("pull-request comments = %+v, %v", comments, err)
			}
		})
	}
}

func TestGitHubIssueCommentPagination(t *testing.T) {
	_, key := testKey(t)
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `[{"id":902,"html_url":"https://github.com/acme/widgets/issues/17#issuecomment-902","body":"second"}]`)
			return
		}
		w.Header().Set("Link", `<https://api.github.com/repos/acme/widgets/issues/17/comments?per_page=100&page=2>; rel="next"`)
		_, _ = io.WriteString(w, `[{"id":901,"html_url":"https://github.com/acme/widgets/issues/17#issuecomment-901","body":"first"}]`)
	})
	g := githubFor(t, d, key)
	comments, err := g.ListComments(context.Background(), Token{secret: NewSecret("t")}, 17)
	if err != nil || len(comments) != 2 || comments[0].ID != "901" || comments[1].ID != "902" {
		t.Fatalf("pages = %+v, %v", comments, err)
	}
}

func TestGitLabIssueCommentPagination(t *testing.T) {
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `[{"id":902,"body":"second"}]`)
			return
		}
		w.Header().Set("X-Next-Page", "2")
		_, _ = io.WriteString(w, `[{"id":901,"body":"first"}]`)
	})
	g := gitlabFor(t, d)
	comments, err := g.ListComments(context.Background(), Token{secret: NewSecret("glpat-bot")}, 17)
	if err != nil || len(comments) != 2 || comments[0].ID != "901" || comments[1].ID != "902" || comments[1].URL == "" {
		t.Fatalf("pages = %+v, %v", comments, err)
	}
}

func TestIssueCommentPaginationExhaustion(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		_, key := testKey(t)
		d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Link", `<https://api.github.com/repos/acme/widgets/issues/17/comments?page=2>; rel="next"`)
			_, _ = io.WriteString(w, `[]`)
		})
		g := githubFor(t, d, key)
		if _, err := g.ListComments(context.Background(), Token{secret: NewSecret("t")}, 17); !errors.Is(err, ErrLookupIncomplete) {
			t.Fatalf("unbounded GitHub pagination: %v", err)
		}
	})
	t.Run("gitlab", func(t *testing.T) {
		d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Next-Page", "2")
			_, _ = io.WriteString(w, `[]`)
		})
		g := gitlabFor(t, d)
		if _, err := g.ListComments(context.Background(), Token{secret: NewSecret("glpat-bot")}, 17); !errors.Is(err, ErrLookupIncomplete) {
			t.Fatalf("unbounded GitLab pagination: %v", err)
		}
	})
}

func TestIssueCommentMalformedResponses(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		for _, tc := range []struct {
			name, operation, body string
		}{
			{"create malformed JSON", "create", `{`},
			{"create missing id", "create", `{"id":0,"html_url":"https://github.com/acme/widgets/issues/17#issuecomment-0"}`},
			{"list malformed JSON", "list", `{`},
			{"list missing id", "list", `[{"id":0,"html_url":"https://github.com/acme/widgets/issues/17#issuecomment-0"}]`},
		} {
			t.Run(provider+" "+tc.name, func(t *testing.T) {
				d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
					if tc.operation == "create" {
						w.WriteHeader(http.StatusCreated)
					}
					_, _ = io.WriteString(w, tc.body)
				})
				var create func(context.Context, Token, CommentSpec) (Comment, Result)
				var list func(context.Context, Token, int) ([]Comment, error)
				if provider == "github" {
					_, key := testKey(t)
					g := githubFor(t, d, key)
					create, list = g.CreateComment, g.ListComments
				} else {
					g := gitlabFor(t, d)
					create, list = g.CreateComment, g.ListComments
				}
				tok := Token{secret: NewSecret("t")}
				if tc.operation == "create" {
					if _, res := create(context.Background(), tok, CommentSpec{Number: 17, Body: "ok"}); res.Class != Ambiguous || res.Reason != "malformed_response" {
						t.Fatalf("malformed create = %+v", res)
					}
					return
				}
				if _, err := list(context.Background(), tok, 17); !errors.Is(err, ErrLookupIncomplete) {
					t.Fatalf("malformed list: %v", err)
				}
			})
		}
	}
}

func TestGitLabIssueCommentWriteClassification(t *testing.T) {
	status := http.StatusCreated
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusCreated {
			_, _ = io.WriteString(w, `{"id":902,"body":"ok"}`)
			return
		}
		_, _ = io.WriteString(w, `{"message":"host body must not escape"}`)
	})
	g := gitlabFor(t, d)
	for _, tc := range []struct {
		status int
		class  Class
		reason string
	}{{http.StatusUnprocessableEntity, Rejected, "validation_failed"}, {http.StatusForbidden, Rejected, "forbidden"}, {http.StatusTooManyRequests, Ambiguous, "rate_limited"}, {http.StatusBadGateway, Ambiguous, "server_error"}} {
		status = tc.status
		_, res := g.CreateComment(context.Background(), Token{secret: NewSecret("glpat-bot")}, CommentSpec{Number: 17, Body: "ok"})
		if res.Class != tc.class || res.Reason != tc.reason {
			t.Fatalf("%d = %+v, want %s %q", tc.status, res, tc.class, tc.reason)
		}
	}
}

type failingCommentDoer struct{}

func (failingCommentDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("injected transport failure")
}

func TestIssueCommentTransportFailures(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			var create func(context.Context, Token, CommentSpec) (Comment, Result)
			var list func(context.Context, Token, int) ([]Comment, error)
			if provider == "github" {
				_, key := testKey(t)
				g := githubFor(t, failingCommentDoer{}, key)
				create, list = g.CreateComment, g.ListComments
			} else {
				g := gitlabFor(t, failingCommentDoer{})
				create, list = g.CreateComment, g.ListComments
			}
			tok := Token{secret: NewSecret("t")}
			if _, res := create(context.Background(), tok, CommentSpec{Number: 17, Body: "ok"}); res.Class != Ambiguous || res.Reason != "transport" {
				t.Fatalf("transport create = %+v", res)
			}
			if _, err := list(context.Background(), tok, 17); !errors.Is(err, ErrLookupIncomplete) {
				t.Fatalf("transport list: %v", err)
			}
		})
	}
}

func TestCommentHelpersRequireCommentHost(t *testing.T) {
	var host Host = &struct{ Host }{&PlainGit{}}
	if _, res := CreateComment(context.Background(), host, Token{}, CommentSpec{Number: 1, Body: "x"}); res.Class != Rejected || res.Reason != "unsupported_effect" {
		t.Fatalf("create = %+v", res)
	}
	if _, err := ListComments(context.Background(), host, Token{}, 1); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("list: %v", err)
	}
}

func TestCommentHelpersRefuseNilHost(t *testing.T) {
	var host Host
	if _, res := CreateComment(context.Background(), host, Token{}, CommentSpec{Number: 1, Body: "x"}); res.Class != Rejected || res.Reason != "unsupported_effect" {
		t.Fatalf("create = %+v", res)
	}
	if _, err := ListComments(context.Background(), host, Token{}, 1); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("list: %v", err)
	}
}
