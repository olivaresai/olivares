// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxItemBody bounds the body of an issue or pull request this package returns,
// so one long description cannot become an unbounded first message.
const maxItemBody = 32 << 10

// Item is an issue or pull request as a person wrote it: the text a session
// starts from. URL is the host's own page for it.
type Item struct {
	Number      int
	PullRequest bool
	Title, Body string
	URL         string
}

// ItemHost is the additive read of one issue or pull request. Host
// implementations that predate it remain valid Host values.
type ItemHost interface {
	GetItem(ctx context.Context, t Token, number int) (Item, error)
}

// GetItem reads through a host that offers items.
func GetItem(ctx context.Context, host Host, t Token, number int) (Item, error) {
	h, ok := host.(ItemHost)
	if !ok {
		return Item{}, ErrNotSupported
	}
	return h.GetItem(ctx, t, number)
}

type ghItem struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        *string         `json:"body"`
	HTMLURL     string          `json:"html_url"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// GetItem reads an issue or pull request of the bound repository. GitHub serves
// both at /issues/{number}; a pull request carries a pull_request member. The
// page URL it names must be this repository's own, so a response for another
// repository is never taken for the one asked about.
func (g *GitHub) GetItem(ctx context.Context, t Token, number int) (Item, error) {
	if number < 1 {
		return Item{}, ErrItemNotFound
	}
	c, err := do(ctx, g.d, http.MethodGet, g.repoURL("/issues/"+strconv.Itoa(number)), g.headers(t.secret.Reveal()), nil)
	if err != nil {
		he := hostError(0, nil, ErrHostUnavailable)
		return Item{}, &he
	}
	switch c.status {
	case http.StatusOK:
	case http.StatusNotFound:
		he := hostError(c.status, c.header, ErrItemNotFound)
		return Item{}, &he
	case http.StatusUnauthorized, http.StatusForbidden:
		he := hostError(c.status, c.header, ErrCredentialRefused)
		return Item{}, &he
	default:
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return Item{}, &he
	}
	var out ghItem
	if json.Unmarshal(c.body, &out) != nil || out.Number != number || !g.validItemURL(out.HTMLURL, number) {
		return Item{}, ErrHostUnavailable
	}
	item := Item{Number: number, PullRequest: len(out.PullRequest) > 0 && string(out.PullRequest) != "null", Title: out.Title, URL: out.HTMLURL}
	if out.Body != nil {
		item.Body = cutOnRune(*out.Body, maxItemBody)
	}
	return item, nil
}

func (g *GitHub) validItemURL(raw string, number int) bool {
	for _, kind := range []string{"issues", "pull"} {
		if strings.EqualFold(raw, githubWebBase(g.cfg.APIBase)+"/"+g.cfg.Owner+"/"+g.cfg.Repo+"/"+kind+"/"+strconv.Itoa(number)) {
			return true
		}
	}
	return false
}

// cutOnRune shortens s to at most n bytes without splitting a character.
func cutOnRune(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
