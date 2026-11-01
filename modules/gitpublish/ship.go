// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A ship is one decision made of the three steps a person takes by hand: commit
// the session's folder, push that commit, open the pull request. Each step is
// the module's own (the sessions module's confined commit, Push and
// OpenPullRequest, each admitted as the caller), so a ship adds no authority: it
// orders them, refuses before the commit what it can, and names the reason.
//
// The ship's operation id keys both effects ("<id>:push" and "<id>:pr"), so a
// retry replays their receipts instead of publishing again.

// The states of a ship.
const (
	ShipShipped          = "shipped"
	ShipAwaitingApproval = "awaiting_approval"
	ShipIncomplete       = "incomplete"
)

const (
	// shipOpMax leaves room for the effect suffix inside the 128 characters an operation id has.
	shipOpMax       = 100
	shipMessageMax  = 8192
	shipGateAction  = "gitpublish.push"
	shipGateSubject = "gitpublish.target"
)

// ShipInput is one ship. Target and SessionRun name where the work is and where
// it goes; Message is the commit message, needed only when the folder has
// changes. Base defaults to the target's first merge base.
type ShipInput struct {
	Target            model.ID
	OperationID       string
	SessionRun        string
	Message           string
	Title, Body, Base string
	Draft             bool
}

// ShipResult is how far the ship got. Push and PullRequest are the receipts of
// the steps that ran; PullRequestError is the refusal of a pull request that
// could not be opened after the push applied.
type ShipResult struct {
	State            string
	Branch, Commit   string
	Committed        bool
	Approval         string
	Push             *Receipt
	PullRequest      *Receipt
	PullRequestURL   string
	PullRequestError string
}

func (in ShipInput) check() *Error {
	if !opRe.MatchString(in.OperationID) || len(in.OperationID) > shipOpMax {
		return errInvalid
	}
	if id, err := model.ParseID(in.SessionRun); err != nil || id.String() != in.SessionRun {
		return errInvalid
	}
	if len(in.Message) > shipMessageMax || in.Title == "" || len(in.Title) > 256 || len(in.Body) > 65536 || (in.Base != "" && !validBranch(in.Base)) {
		return errInvalid
	}
	if strings.IndexFunc(in.Title, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return errInvalid
	}
	return nil
}

// Ship commits, pushes and opens the pull request for a session run's folder.
// With a policy asking for review, it stops before the push as
// ShipAwaitingApproval; the same call, once a second person approved, goes on.
func (m *Module) Ship(ctx context.Context, c Caller, in ShipInput) (ShipResult, error) {
	if e := in.check(); e != nil {
		return ShipResult{}, e
	}
	if m.opts.Work == nil || m.opts.Gate == nil {
		return ShipResult{}, errUnavailable
	}
	tg, withPR, err := m.shipPreflight(ctx, c, &in)
	if err != nil {
		return ShipResult{}, err
	}
	head, err := m.opts.Work.CommitRun(ctx, c.Principal, c.Tenant, tg.Workspace, in.SessionRun, in.Message)
	if err != nil {
		return ShipResult{}, commitError(err)
	}
	res := ShipResult{Branch: head.Branch, Commit: head.Commit, Committed: head.Committed}
	branch := head.Branch
	if !strings.HasPrefix(branch, tg.PushPrefix) {
		branch = tg.PushPrefix + branch
	}
	push := PushInput{Target: tg.ID, OperationID: in.OperationID + ":push", Ref: "refs/heads/" + branch, Commit: head.Commit, Tree: head.Tree, SessionRun: in.SessionRun}
	// The read feeds the commit into the server repository and asks the host,
	// as the push will: what it refuses, it refuses before anyone reviews it.
	read, err := m.read(ctx, c, tg.Workspace, spec{effect: effectPush, perm: permPush, action: actionPush, host: gp.EffectPush, target: tg.ID,
		req: Requested{Ref: push.Ref, Commit: push.Commit, Tree: push.Tree}, branch: branch, session: in.SessionRun})
	if err != nil {
		return res, err
	}
	if len(read.paths) == 0 {
		return res, refuse("nothing_to_ship", http.StatusConflict)
	}
	lease, seen, err := m.shipLease(ctx, c, tg.ID, push.OperationID, read.tip)
	if err != nil {
		return res, err
	}
	push.ExpectedOld = lease
	// A ship the module has already pushed for is a retry: it replays that
	// push's receipt and needs no second approval for it.
	if !seen {
		decision, err := m.opts.Gate.Authorize(ctx, c.Tenant, GateRequest{Action: shipGateAction, SubjectKind: shipGateSubject, SubjectRef: tg.ID.String(),
			PlanHash: shipPlan(push), RequestedBy: c.Principal.Actor(), ConsumerID: in.OperationID,
			Reason: fmt.Sprintf("Push commit %s to %s on publication target %s, then open a pull request into %s.", push.Commit, push.Ref, tg.ID, in.Base)})
		switch {
		case err != nil:
			return res, refuse("approval_unavailable", http.StatusServiceUnavailable)
		case decision.State == GatePending:
			res.State, res.Approval = ShipAwaitingApproval, decision.Ref
			return res, nil
		case decision.State != GateGranted:
			return res, refuse("approval_denied", http.StatusForbidden)
		}
	}
	pushed, err := m.Push(ctx, c, push)
	if err != nil {
		return res, err
	}
	res.Push, res.State = &pushed, ShipIncomplete
	if !landed(pushed) {
		return res, nil
	}
	if !withPR {
		res.State = ShipShipped
		return res, nil
	}
	pull, err := m.OpenPullRequest(ctx, c, PullRequestInput{Target: tg.ID, OperationID: in.OperationID + ":pr", HeadRef: branch, Base: in.Base,
		Commit: head.Commit, Title: in.Title, Body: in.Body, Draft: in.Draft})
	var refused *Error
	if errors.As(err, &refused) {
		res.PullRequestError = refused.Code
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.PullRequest = &pull
	if landed(pull) {
		res.State = ShipShipped
		res.PullRequestURL = m.changeLinker(ctx, c, tg)(pull.Intent.Observed.Number)
	}
	return res, nil
}

// shipPreflight is everything that can refuse a ship before the folder is
// committed: the caller's authority for the push and the pull request, the
// approved bindings, the run in the target's workspace and the base.
func (m *Module) shipPreflight(ctx context.Context, c Caller, in *ShipInput) (Target, bool, error) {
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	tg, err := m.admitted(actx, c, "", in.Target, permPush, actionPush)
	if err != nil {
		return Target{}, false, err
	}
	cb, _, e := m.bindings(actx, c.Tenant, tg.Workspace, tg.CredentialBinding, tg.RepositoryBinding)
	if e != nil {
		return Target{}, false, e
	}
	caps := gp.TargetCapabilitiesFor(cb.Host)
	if !caps.Supports(gp.EffectPush, "") {
		return Target{}, false, errUnsupported
	}
	withPR := caps.Supports(gp.EffectPullRequest, "")
	if withPR {
		if _, err := m.admitted(actx, c, tg.Workspace, tg.ID, permPullRequest, actionPullRequest); err != nil {
			return Target{}, false, err
		}
		if in.Base == "" && len(tg.MergeBases) > 0 {
			in.Base = tg.MergeBases[0]
		}
		if !contains(tg.MergeBases, in.Base) {
			return Target{}, false, refuse("base_not_allowed", http.StatusUnprocessableEntity)
		}
	}
	if m.opts.Sessions == nil {
		return Target{}, false, refuse("session_source_unavailable", http.StatusServiceUnavailable)
	}
	if _, err := m.opts.Sessions.ReadRunWorkspacePath(actx, c.Tenant, tg.Workspace, in.SessionRun); errors.Is(err, store.ErrNotFound) {
		return Target{}, false, refuse("session_source_refused", http.StatusUnprocessableEntity)
	} else if err != nil {
		return Target{}, false, refuse("session_source_unavailable", http.StatusServiceUnavailable)
	}
	return tg, withPR, nil
}

// shipLease is the lease of the ship's push, and whether the module has seen a
// push under this operation id. Such a ship keeps the lease it pushed with, so
// its retry is the same request and replays its receipt, even though the host's
// tip has moved to the commit that push made. A new ship leases the tip it just read.
func (m *Module) shipLease(ctx context.Context, c Caller, target model.ID, op, tip string) (string, bool, error) {
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	lease, seen := tip, false
	if err := m.data.View(actx, c.Tenant, func(sc store.Scope) error {
		prior, found, err := findByOperation(actx, sc, target, effectPush, op)
		if found {
			lease, seen = prior.Requested.ExpectedOld, true
		}
		return err
	}); err != nil {
		return "", false, storeError(err)
	}
	return lease, seen, nil
}

// commitError names why the folder could not be committed.
func commitError(err error) error {
	switch {
	case errors.Is(err, gp.ErrNoBranch):
		return refuse("no_branch", http.StatusConflict)
	case errors.Is(err, gp.ErrNoCommit):
		return refuse("no_commit", http.StatusConflict)
	case errors.Is(err, gp.ErrLocalInput):
		return refuse("commit_refused", http.StatusUnprocessableEntity)
	case errors.Is(err, store.ErrNotFound):
		return refuse("session_source_refused", http.StatusUnprocessableEntity)
	}
	return refuse("session_source_unavailable", http.StatusServiceUnavailable)
}

// landed reports a receipt whose effect is on the host.
func landed(r Receipt) bool { return r.Answer == StateApplied || r.Answer == StateAdopted }

// shipPlan binds an approval to the exact push: the target, ref, commit and
// tree. The lease is the host's moving tip and the pull request's text is not
// what a person reviews here, so neither is in it.
func shipPlan(p PushInput) string {
	sum := sha256.Sum256([]byte("olivares.gitpublish.ship.v1\x00" + p.Target.String() + "\x00" + p.Ref + "\x00" + p.Commit + "\x00" + p.Tree))
	return hex.EncodeToString(sum[:])
}

// changeLinker names a pull request's page on the host, for the target's
// repository: the resolved web address is read once. It answers "" when that
// address is not known, so a link is shown only when it is certain.
func (m *Module) changeLinker(ctx context.Context, c Caller, tg Target) func(number int) string {
	none := func(int) string { return "" }
	resolver, ok := m.opts.Custody.(WebBaseResolver)
	if !ok {
		return none
	}
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	cb, _, e := m.bindings(actx, c.Tenant, tg.Workspace, tg.CredentialBinding, tg.RepositoryBinding)
	if e != nil {
		return none
	}
	base, err := resolver.RepositoryWebBase(actx, c.Tenant, tg.Workspace, tg.RepositoryBinding)
	if err != nil {
		return none
	}
	path := "/pull/"
	if cb.Host == "gitlab" {
		path = "/-/merge_requests/"
	}
	return func(number int) string {
		if number <= 0 {
			return ""
		}
		return base + path + strconv.Itoa(number)
	}
}

// withLinks fills the page of every landed pull request among the intents, which
// belong to one target.
func (m *Module) withLinks(ctx context.Context, c Caller, target model.ID, intents []intentDTO) {
	var link func(int) string
	for i := range intents {
		in := &intents[i]
		if in.Effect != effectPullRequest || (in.State != StateApplied && in.State != StateAdopted) || in.Observed.Number <= 0 {
			continue
		}
		if link == nil {
			link = func(int) string { return "" }
			actx, cancel := m.admissionContext(ctx)
			var tg Target
			if err := m.data.View(actx, c.Tenant, func(sc store.Scope) error {
				var err error
				tg, err = loadTarget(actx, sc, target)
				return err
			}); err == nil {
				link = m.changeLinker(ctx, c, tg)
			}
			cancel()
		}
		in.Observed.URL = link(in.Observed.Number)
	}
}

type shipBody struct {
	OperationID string `json:"operation_id"`
	SessionRun  string `json:"session_run"`
	Message     string `json:"message"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Base        string `json:"base"`
	Draft       bool   `json:"draft"`
}

type shipDTO struct {
	State            string     `json:"state"`
	Branch           string     `json:"branch,omitempty"`
	Commit           string     `json:"commit,omitempty"`
	Committed        bool       `json:"committed"`
	ApprovalID       string     `json:"approval_id,omitempty"`
	Push             *intentDTO `json:"push,omitempty"`
	PullRequest      *intentDTO `json:"pull_request,omitempty"`
	PullRequestURL   string     `json:"pull_request_url,omitempty"`
	PullRequestError string     `json:"pull_request_error,omitempty"`
}

func toShipDTO(r ShipResult) shipDTO {
	d := shipDTO{State: r.State, Branch: r.Branch, Commit: r.Commit, Committed: r.Committed, ApprovalID: r.Approval,
		PullRequestURL: r.PullRequestURL, PullRequestError: r.PullRequestError}
	if r.Push != nil {
		p := toIntentDTO(r.Push.Intent, r.Push.Answer)
		d.Push = &p
	}
	if r.PullRequest != nil {
		p := toIntentDTO(r.PullRequest.Intent, r.PullRequest.Answer)
		d.PullRequest = &p
	}
	return d
}

// handleShip commits the named session run's folder, pushes the commit to a
// branch under the target's push prefix and opens the pull request, as one
// decision. It answers 200 when shipped, 202 while a second person has to
// approve or a step is not settled, and the module's refusal otherwise.
func (m *Module) handleShip(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var b shipBody
	if !decode(w, r, &b) {
		return
	}
	res, err := m.Ship(r.Context(), callerOf(mc), ShipInput{Target: idParam(r), OperationID: b.OperationID, SessionRun: b.SessionRun, Message: b.Message,
		Title: b.Title, Body: b.Body, Base: b.Base, Draft: b.Draft})
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if res.State != ShipShipped {
		status = http.StatusAccepted
	}
	writeResponse(w, status, toShipDTO(res))
}
