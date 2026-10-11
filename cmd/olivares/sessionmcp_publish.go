// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/cmd/olivares/internal/approvalbridge"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/gitpublish"
	"github.com/olivaresai/olivares/modules/governance"
)

// The agent proposes, a person confirms. A session asks for one push or one
// pull request; the request waits in the approval queue, and its session shows
// it waiting. When a person approves it, the approval is spent once and the
// publication runs as the session's launcher, through the same gitpublish verb
// and target the console uses. A session credential still never publishes
// directly (runtime_credential_refused), and merging stays with a person.
const (
	publishPushTool        = "olivares_publish_push"
	publishPullRequestTool = "olivares_publish_pull_request"
	publishSubjectKind     = "gitpublish.target"
	// publishSpendWindow bounds the launcher lookup and the spend after a
	// person decides.
	publishSpendWindow = 10 * time.Second
)

type sessionPublishPushArgs struct {
	TargetID    string `json:"target_id"`
	Ref         string `json:"ref"`
	ExpectedOld string `json:"expected_old,omitempty"`
	Commit      string `json:"commit"`
	Tree        string `json:"tree"`
}

type sessionPublishPullRequestArgs struct {
	TargetID string `json:"target_id"`
	HeadRef  string `json:"head_ref"`
	Base     string `json:"base"`
	Commit   string `json:"commit"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Draft    bool   `json:"draft,omitempty"`
}

// sessionPublisher is the engine side of the publish proposal tools.
// hold keeps the call's writer open for d from now, so the answer still
// reaches the agent.
type sessionPublisher interface {
	proposePublication(ctx context.Context, w http.ResponseWriter, hold func(d time.Duration) error, session auth.Principal, tenant model.TenantID, tool string, arguments json.RawMessage) error
}

// publisherModule is the gitpublish verbs a proposal carries out, and how
// long one of them may take until its receipt is written.
type publisherModule interface {
	Push(context.Context, gitpublish.Caller, gitpublish.PushInput) (gitpublish.Receipt, error)
	OpenPullRequest(context.Context, gitpublish.Caller, gitpublish.PullRequestInput) (gitpublish.Receipt, error)
	PublicationBudget() time.Duration
}

// sessionPublications holds what a proposal needs: the publication verbs, the
// approval queue, the session's live turn and approval wait, and its launcher.
type sessionPublications struct {
	module    publisherModule
	approvals *governance.EngineApprovals
	beginCall func(context.Context, auth.Principal) (context.Context, func(), error)
	beginWait func(context.Context, auth.Principal, string, time.Time) (func(), error)
	launcher  func(context.Context, auth.Principal) (auth.Principal, error)
	log       *slog.Logger
}

const publishAfterApproval = " A person decides in the console's approvals while this session shows it waiting (under a minute, like other session approvals); once approved, Olivares publishes it as your launcher with the target's host credential and answers with the publication receipt. One request waits at a time. A refusal or a declined request is final: never retry with other authority."

// publishTools lists the proposal tools: only to issued session credentials,
// whose launcher can be resolved, on an engine with gitpublish wired.
func (h *sessionMCPHandler) publishTools() []mcpc.Tool {
	if h.publisher == nil || !h.issuedSessionOnly {
		return nil
	}
	return []mcpc.Tool{
		sessionTool(publishPushTool, "Ask a person to push one exact commit from this session's folder to a branch under a publication target's push prefix in this session's workspace (target_id from the console's Git publication)."+publishAfterApproval,
			sessionPublishPushArgs{}, []string{"target_id", "ref", "commit", "tree"}, false),
		sessionTool(publishPullRequestTool, "Ask a person to open a pull request from a pushed branch under the target's push prefix into one of its merge bases. Merging stays with a person other than this session's launcher."+publishAfterApproval,
			sessionPublishPullRequestArgs{}, []string{"target_id", "head_ref", "base", "commit", "title"}, false),
	}
}

// callPublishTool serves a call to a proposal tool this edge offers and
// reports whether it did.
func (h *sessionMCPHandler) callPublishTool(w http.ResponseWriter, r *http.Request, p auth.Principal, tenant model.TenantID, id json.RawMessage, name string, arguments json.RawMessage, bearer string) bool {
	if (name != publishPushTool && name != publishPullRequestTool) || h.publisher == nil || !h.issuedSessionOnly {
		return false
	}
	// The call outlives the server's 60s write bound: first the approval wait,
	// then the publication after it. A writer without deadlines (a test
	// recorder) needs none; any other failure to hold it is refused before
	// the step that needs it.
	rc := http.NewResponseController(w)
	hold := func(d time.Duration) error {
		if err := rc.SetWriteDeadline(time.Now().Add(d)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}
	response := &sessionAPIResponse{header: make(http.Header)}
	if err := h.publisher.proposePublication(r.Context(), response, hold, p, tenant, name, arguments); err != nil {
		sessionRPCError(w, id, -32602, err.Error())
		return true
	}
	writeSessionAPIResult(w, id, response, bearer)
	return true
}

// sessionPublisher returns the proposal service when every part it needs runs
// on this node, else an untyped nil, so no tool is listed.
func (e *engine) sessionPublisher() sessionPublisher {
	if e == nil || e.gitpublish == nil || e.engineApprovals == nil || e.sessionsMod == nil || e.sessionHooks == nil || e.sessionHooks.SessionCredentials == nil {
		return nil
	}
	return sessionPublications{module: e.gitpublish, approvals: e.engineApprovals, beginCall: e.sessionsMod.BeginSessionCall,
		beginWait: e.sessionsMod.BeginApprovalWait, launcher: e.sessionHooks.LauncherForRun, log: e.log}
}

// publication is one proposed effect: what the person reviews and what runs.
type publication struct {
	action, tool, summary, review string
	target                        model.ID
	run                           func(context.Context, gitpublish.Caller, gitpublish.Proposal) (gitpublish.Receipt, error)
}

// singleLine refuses control and invisible format characters (line breaks,
// bidirectional and zero-width marks) in a value the reviewer reads on one line.
func singleLine(field, v string) error {
	if strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return fmt.Errorf("%s must be one line of visible text", field)
	}
	return nil
}

// publication decodes and checks a proposal before anything queues: the
// verb's own shape checks, so a person never approves what would be refused.
func (s sessionPublications) publication(p auth.Principal, tool string, raw json.RawMessage) (publication, error) {
	strict := func(v any) error {
		if strictSessionJSON(bytes.NewReader(raw), v) != nil {
			return errors.New("Invalid arguments; use the schema from tools/list")
		}
		return nil
	}
	refused := func(err error) error {
		return fmt.Errorf("The publication request is invalid (%v); fix it and ask again", err)
	}
	run := p.SessionRunRef
	switch tool {
	case publishPushTool:
		var a sessionPublishPushArgs
		if err := strict(&a); err != nil {
			return publication{}, err
		}
		target, err := model.ParseID(a.TargetID)
		if err != nil {
			return publication{}, refused(errors.New("target_id is not a publication target id"))
		}
		in := gitpublish.PushInput{Target: target, Ref: a.Ref, ExpectedOld: a.ExpectedOld, Commit: a.Commit, Tree: a.Tree, SessionRun: run}
		if err := in.Validate(); err != nil {
			return publication{}, refused(err)
		}
		lease := "the branch must not exist"
		if a.ExpectedOld != "" {
			lease = "the branch must be at " + a.ExpectedOld
		}
		return publication{action: "gitpublish.push", tool: "Git push", target: target,
			summary: fmt.Sprintf("Session run %s asks to push commit %s to %s on publication target %s.", run, a.Commit, a.Ref, target),
			review:  fmt.Sprintf("target: %s\nref: %s\ncommit: %s\ntree: %s\nlease: %s\nfrom session run: %s", target, a.Ref, a.Commit, a.Tree, lease, run),
			run: func(ctx context.Context, c gitpublish.Caller, pr gitpublish.Proposal) (gitpublish.Receipt, error) {
				in.OperationID, in.Proposal = "proposal:"+pr.Approval, pr
				return s.module.Push(ctx, c, in)
			}}, nil
	case publishPullRequestTool:
		var a sessionPublishPullRequestArgs
		if err := strict(&a); err != nil {
			return publication{}, err
		}
		target, err := model.ParseID(a.TargetID)
		if err != nil {
			return publication{}, refused(errors.New("target_id is not a publication target id"))
		}
		in := gitpublish.PullRequestInput{Target: target, HeadRef: a.HeadRef, Base: a.Base, Commit: a.Commit, Title: a.Title, Body: a.Body, Draft: a.Draft}
		if err := in.Validate(); err != nil {
			return publication{}, refused(err)
		}
		if err := singleLine("title", a.Title); err != nil {
			return publication{}, refused(err)
		}
		return publication{action: "gitpublish.pull_request", tool: "Pull request", target: target,
			summary: fmt.Sprintf("Session run %s asks to open a pull request %s -> %s at %s on publication target %s.", run, a.HeadRef, a.Base, a.Commit, target),
			review: fmt.Sprintf("target: %s\nhead: %s\nbase: %s\ncommit: %s\ndraft: %t\ntitle: %s\nfrom session run: %s\nbody:\n%s",
				target, a.HeadRef, a.Base, a.Commit, a.Draft, a.Title, run, a.Body),
			run: func(ctx context.Context, c gitpublish.Caller, pr gitpublish.Proposal) (gitpublish.Receipt, error) {
				in.OperationID, in.Proposal = "proposal:"+pr.Approval, pr
				return s.module.OpenPullRequest(ctx, c, in)
			}}, nil
	}
	return publication{}, errors.New("unknown publish tool")
}

// waiting reports whether this session already has a publication waiting for
// a person: the approval queue itself is the record, read to its last page.
func (s sessionPublications) waiting(ctx context.Context, tenant model.TenantID, sid string) (bool, error) {
	for _, action := range []string{"gitpublish.push", "gitpublish.pull_request"} {
		for cursor := ""; ; {
			items, page, err := s.approvals.List(ctx, tenant, action, nbPending, cursor)
			if err != nil {
				return false, err
			}
			for _, item := range items {
				if item.SessionRef == sid {
					return true, nil
				}
			}
			if !page.HasMore {
				break
			}
			if page.Cursor == "" || page.Cursor == cursor {
				return false, errors.New("pending approvals have no next page")
			}
			cursor = page.Cursor
		}
	}
	return false, nil
}

// proposePublication opens the approval as the session, waits for the person,
// resolves the launcher, spends the approval once and publishes as the
// launcher. Every refusal is an answer to the agent; nothing publishes without
// an approved, spent request.
func (s sessionPublications) proposePublication(ctx context.Context, w http.ResponseWriter, hold func(time.Duration) error, p auth.Principal, tenant model.TenantID, tool string, raw json.RawMessage) error {
	pub, err := s.publication(p, tool, raw)
	if err != nil {
		return err
	}
	workspace, confined := p.ConfinedWorkspaceIn(tenant)
	if !confined || p.SessionIdentity == "" || p.SessionRunRef == "" {
		s.log.Warn("session publish: the session principal names no run or workspace", "session", p.SessionIdentity)
		publishRefusal(w, http.StatusForbidden, "session_turn_ended", "This session cannot publish.")
		return nil
	}
	unavailable := func(what string, err error, args ...any) error {
		s.log.Warn("session publish: "+what, append(args, "err", err)...)
		publishRefusal(w, http.StatusServiceUnavailable, "approval_unavailable", "The approval service could not answer; ask again later.")
		return nil
	}
	// Until the decision; the publication's own budget starts again before the spend.
	if err := hold(managedMCPApprovalWait + publishSpendWindow + s.module.PublicationBudget()); err != nil {
		return unavailable("the reply deadline could not be set", err, "session", p.SessionIdentity)
	}
	ctx, end, err := s.beginCall(ctx, p)
	if err != nil {
		if !errors.Is(err, auth.ErrUnauthenticated) {
			return unavailable("session turn unavailable", err, "session", p.SessionIdentity)
		}
		publishRefusal(w, http.StatusForbidden, "session_turn_ended", "The session turn has ended")
		return nil
	}
	defer end()
	if busy, err := s.waiting(ctx, tenant, p.SessionIdentity); err != nil {
		return unavailable("pending proposals unreadable", err, "session", p.SessionIdentity)
	} else if busy {
		publishRefusal(w, http.StatusConflict, "proposal_pending", "This session already has a publication waiting for a person; wait for its answer.")
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, managedMCPApprovalWait)
	defer cancel()
	subject := approvalbridge.EncodeSubjectRef(pub.target.String(), hexSHA(pub.action+"\x00"+pub.review))
	approval, err := s.approvals.Request(waitCtx, tenant, p, governance.ApprovalRequest{
		SessionRef: p.SessionIdentity, Action: pub.action, SubjectKind: publishSubjectKind, SubjectRef: subject,
		Reason: pub.summary, Review: &governance.ApprovalReview{Tool: pub.tool, Text: pub.review},
		ExpiresInSeconds: int64(managedMCPApprovalWait / time.Second),
	})
	if err != nil {
		return unavailable("the approval could not be queued", err, "session", p.SessionIdentity, "action", pub.action)
	}
	pending := true
	// Withdraw on every exit that leaves it undecided. Cancel never overwrites
	// a terminal answer.
	defer func() {
		if !pending {
			return
		}
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		if _, err := s.approvals.Cancel(cleanup, tenant, p, approval.ID); err != nil {
			s.log.Warn("session publish: approval withdrawal incomplete", "approval_ref", approval.ID, "err", err)
		}
	}()
	// What the person sees must be exactly what runs: a masked secret or a
	// policy rewrite withdraws the request.
	if approval.Reason != pub.summary || approval.Review == nil || approval.Review.Text != pub.review {
		publishRefusal(w, http.StatusUnprocessableEntity, "not_reviewable", "The request holds text a reviewer would see masked; remove it and ask again.")
		return nil
	}
	deadline, _ := waitCtx.Deadline()
	if approval.ExpiresAt != "" {
		at, err := model.ParseTimestamp(approval.ExpiresAt)
		if err != nil {
			return unavailable("approval expiry unreadable", err, "approval_ref", approval.ID)
		}
		if at.Time().Before(deadline) {
			deadline = at.Time()
		}
	}
	endWait, err := s.beginWait(waitCtx, p, approval.ID, deadline)
	if err != nil {
		if !errors.Is(err, auth.ErrUnauthenticated) {
			return unavailable("session approval wait unavailable", err, "approval_ref", approval.ID)
		}
		publishRefusal(w, http.StatusForbidden, "session_turn_ended", "The session could not wait for the approval")
		return nil
	}
	defer endWait()
	answer, err := s.approvals.Wait(waitCtx, tenant, approval.ID)
	endWait()
	// A decision recorded as the window closed comes back with the deadline:
	// the person's answer stands.
	if errors.Is(err, context.DeadlineExceeded) && answer.Status != "" && answer.Status != nbExpired {
		err = nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		pending = false // Wait recorded the expiry.
		publishRefusal(w, http.StatusConflict, "approval_not_decided", "No person decided the publication in time ("+approval.ID+"); ask again when someone can review it.")
		return nil
	}
	if err != nil {
		return unavailable("approval wait failed", err, "approval_ref", approval.ID)
	}
	pending = false
	if answer.Status != nbApproved {
		publishRefusal(w, http.StatusForbidden, "approval_"+strings.ToLower(answer.Status), "A person did not approve the publication ("+answer.Status+", "+approval.ID+").")
		return nil
	}
	if answer.SessionRef != p.SessionIdentity || answer.Action != pub.action || answer.SubjectKind != publishSubjectKind || answer.SubjectRef != subject {
		publishRefusal(w, http.StatusForbidden, "approval_scope_changed", "The approval no longer matches this publication ("+approval.ID+").")
		return nil
	}
	// A person decided: the steps after it do not end with the wait window, so
	// a decision at its end is carried out, not left approved and unspent.
	after, done := context.WithTimeout(context.WithoutCancel(ctx), publishSpendWindow)
	defer done()
	// The launcher before the spend: an ended session leaves the approval unspent.
	launcher, err := s.launcher(after, p)
	if err != nil {
		if !errors.Is(err, auth.ErrUnauthenticated) {
			return unavailable("launcher unresolved", err, "approval_ref", approval.ID)
		}
		s.log.Info("session publish: the launcher can no longer publish", "approval_ref", approval.ID, "session_run", p.SessionRunRef, "err", err)
		publishRefusal(w, http.StatusForbidden, "session_access_ended", "This session's launcher can no longer publish; nothing was published.")
		return nil
	}
	// The receipt must outlive gitpublish's own phases, counted from here: an
	// applied publication whose answer is cut would be asked for again.
	if err := hold(publishSpendWindow + s.module.PublicationBudget()); err != nil {
		s.log.Warn("session publish: the reply deadline could not be set after the approval", "approval_ref", approval.ID, "err", err)
		publishRefusal(w, http.StatusServiceUnavailable, "approval_unavailable", "A person approved the publication ("+approval.ID+"), but its answer could not be held open; nothing was published and the approval is unspent.")
		return nil
	}
	spent, err := s.approvals.Consume(after, tenant, approval.ID, newSingleUseConsumerID(), "")
	if err != nil {
		return unavailable("approval spend failed", err, "approval_ref", approval.ID)
	}
	if !spent.Granted || spent.Replay {
		publishRefusal(w, http.StatusConflict, "approval_spent", "The approval is no longer valid for this publication ("+approval.ID+").")
		return nil
	}
	// A spent approval publishes even if the call is interrupted now; the
	// module bounds its own admission and dispatch.
	rc, err := pub.run(context.WithoutCancel(ctx), gitpublish.Caller{Principal: launcher, Tenant: tenant}, gitpublish.Proposal{SessionRun: p.SessionRunRef, Workspace: workspace, Approval: approval.ID})
	s.log.Info("session publish: approved proposal carried out", "approval_ref", approval.ID, "session_run", p.SessionRunRef, "action", pub.action, "state", rc.Answer, "err", err)
	gitpublish.WriteReceipt(w, rc, err)
	return nil
}

func publishRefusal(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
