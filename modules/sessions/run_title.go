// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A session's TITLE is a phrase cut from the first message the person sent, shown
// where the session has no name of its own. It is made on this host, from the
// transcript the engine already reads for GET /runs/{ref}/messages: no model is asked,
// so nothing leaves the session's bound provider and a session signed in with the
// person's own account (which gives the engine no credential to call a model with)
// is titled the same way as one on a key.
//
// The title is not the name. A name is the person's handle for the session (`session
// start` and `rename` keep it unique so that it picks one session), and a generated
// phrase would collide with it; the title is display-only, so two sessions may share
// one and a rename never loses to it.

// The styles are how much of the first message the title keeps. The default is the
// middle one.
const (
	titleStyleShort    = "short"
	titleStyleSentence = "sentence"
	titleStyleLong     = "long"
)

// titleLimits is the most runes, ellipsis included, a title of each style holds.
var titleLimits = map[string]int{titleStyleShort: 28, titleStyleSentence: 60, titleStyleLong: 100}

// titleMinPhrase is the fewest characters a sentence must have to end a title.
const titleMinPhrase = 12

var (
	// titleListMarker is a numbered-list marker such as "1." or "2)".
	titleListMarker = regexp.MustCompile(`^\d+[.)]\s+`)
	// titleSentenceEnd is a full stop, question or exclamation mark that ends a sentence:
	// followed by a space, so "v1.2" and "a.b" stay whole.
	titleSentenceEnd = regexp.MustCompile(`[.?!]\s`)
)

// titleFromMessage is the title a message makes in a style, or "" when it holds no
// words (empty, or only a code block). An unknown style takes the default; the route
// refuses one before it gets here.
func titleFromMessage(text, style string) string {
	limit, ok := titleLimits[style]
	if !ok {
		limit = titleLimits[titleStyleSentence]
	}
	line := titleFirstLine(text)
	if line == "" {
		return ""
	}
	// The title ends at the first sentence end that leaves a phrase with something to say:
	// "Hello. Please refactor the login" is not titled "Hello".
	for _, loc := range titleSentenceEnd.FindAllStringIndex(line+" ", -1) {
		if utf8.RuneCountInString(line[:loc[0]]) < titleMinPhrase {
			continue
		}
		// The mark stays when it is a question: it changes what the phrase says.
		end := loc[0]
		if line[end] == '?' {
			end++
		}
		line = line[:end]
		break
	}
	line = strings.TrimRight(line, ".:,;!")
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	cut := runes[:limit-1]
	// Back up to a word boundary when that keeps at least half the room.
	if !unicode.IsSpace(runes[limit-1]) {
		for i := len(cut) - 1; i >= limit/2; i-- {
			if unicode.IsSpace(cut[i]) {
				cut = cut[:i]
				break
			}
		}
	}
	return strings.TrimRight(string(cut), " .:,;!") + "…"
}

// titleFirstLine is the first line of the message that has words: code blocks are
// skipped, markdown markers, invisible characters and runs of space are dropped.
func titleFirstLine(text string) string {
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		line = strings.TrimLeft(line, "#>-+* \t")
		line = titleListMarker.ReplaceAllString(line, "")
		line = strings.Map(func(r rune) rune {
			switch {
			case r == '`' || r == '*':
				return -1
			case unicode.IsSpace(r):
				return ' '
			case invisibleInName(r):
				return -1
			}
			return r
		}, line)
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			return line
		}
	}
	return ""
}

// generateRunTitleRequest is the body of POST /runs/{ref}/title. Both fields are
// optional: no style takes the default, and IfUntitled leaves a session that already
// has a name or a title as it is (the console asks for that when a first message shows
// up, so two people viewing one session cannot title it twice).
type generateRunTitleRequest struct {
	Style      string `json:"style"`
	IfUntitled bool   `json:"if_untitled"`
}

// handleGenerateRunTitle cuts the run's title from its first message. The route
// supplies run-write authorization in the run's workspace; the title is made from the
// conversation, so the caller must also be allowed to read its output. The ledger
// records that a title was made, with the run's reference and not the title.
func (m *Module) handleGenerateRunTitle(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !mc.Admits(r.Context(), permRunOutput, mc.Resource) {
		writeRunErr(w, forbiddenErr("reading the session's output is required to title it from its first message"))
		return
	}
	if m.refuseForeignSteering(w, r, mc) {
		return
	}
	var body generateRunTitleRequest
	if !decodeOptionalJSONBody(w, r, &body) {
		return
	}
	if body.Style == "" {
		body.Style = titleStyleSentence
	}
	if _, ok := titleLimits[body.Style]; !ok {
		writeRunErr(w, badRequest("style must be short, sentence or long"))
		return
	}
	ref := chi.URLParam(r, "ref")
	if body.IfUntitled {
		// The row answers it, so the transcript is not scanned for a change that will not
		// be made. Two callers at once may both pass here: the write below asks again under
		// the run's lock.
		rec, err := m.loadRun(r.Context(), mc.Tenant, ref)
		if err != nil {
			writeRunErr(w, err)
			return
		}
		if rec.String(colRunName) != "" || rec.String(colRunTitle) != "" {
			dto := m.toRunDTO(rec)
			m.showTitle(r.Context(), mc, rec, &dto)
			writeJSON(w, http.StatusOK, dto)
			return
		}
	}
	first, err := m.runMessages(r.Context(), mc.Tenant, ref, messageQuery{limit: 1, text: true})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	title := ""
	if len(first.Items) > 0 {
		title = titleFromMessage(first.Items[0].Text, body.Style)
	}
	if title == "" {
		writeRunErr(w, conflictErr("this session has no message with words to title it from yet"))
		return
	}
	if err := checkRunName(title); err != nil {
		writeRunErr(w, err)
		return
	}
	m.changeRun(w, r, mc, "sessions.run.title_generated", func(rec model.Record) bool {
		if body.IfUntitled && (rec.String(colRunName) != "" || rec.String(colRunTitle) != "") {
			return false
		}
		if rec.String(colRunTitle) == title {
			return false
		}
		rec[colRunTitle] = title
		return true
	})
}

// showTitle puts the run's title on dto when the caller may read the conversation it was
// cut from (sessions:output:read on that run, in its own workspace), the permission
// GET /runs/{ref}/messages asks. run:read alone, which a viewer holds, is not enough: the
// title is up to 100 characters of what the person typed. A caller refused sees the row
// as it was before titles, and the console falls back to the name ladder. The run row
// has no title until one is made, so the common read asks nothing.
func (m *Module) showTitle(ctx context.Context, mc api.ModuleContext, rec model.Record, dto *runDTO) {
	title := rec.String(colRunTitle)
	if title == "" {
		return
	}
	res := auth.ResourceAttrs{Kind: permRunRead.Resource(), ID: rec.String(model.ColID), WorkspaceID: model.ID(rec.String(colRunAuthzWorkspaceID))}
	if mc.Admits(ctx, permRunOutput, res) {
		dto.Title = title
	}
}
