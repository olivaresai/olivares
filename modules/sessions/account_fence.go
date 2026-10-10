// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Sessions' fenced writers store references that let an account act in the
// tenant or bind it to a duty: a user ChannelGrant, the two parties of a
// Handoff, a WorkItem's user owner, a protocol interrupt's recipient and the
// owner of an MCP task. Each reads the named accounts' standing before its
// transaction and pins their authority versions, with a tenant fact, as the
// transaction's first lock. A communication command pins them in its one
// authority lock (communication_tx.go), together with the complete fact set it
// already locks; the other writers pin them first in their own transaction.

var _ auth.StandingConsumer = (*Module)(nil)

// UseStanding implements auth.StandingConsumer: it binds the standing port of
// the writers that run outside a request.
func (m *Module) UseStanding(r auth.StandingReader) { m.Standing = r }

// requestStandingKey carries a route's standing port to the kernel it calls.
type requestStandingKey struct{}

type requestStanding struct{ reader auth.StandingReader }

// withRequestStanding returns ctx carrying a route's standing port, which the
// kernel's fenced writers read through instead of the module's own port. A nil
// port stays nil: the fence then refuses.
func withRequestStanding(ctx context.Context, r auth.StandingReader) context.Context {
	return context.WithValue(ctx, requestStandingKey{}, requestStanding{reader: r})
}

// standingFor returns the standing port a fenced writer reads through: the
// route's when a route called it, the module's otherwise.
func (m *Module) standingFor(ctx context.Context) auth.StandingReader {
	if v, ok := ctx.Value(requestStandingKey{}).(requestStanding); ok {
		return v.reader
	}
	return m.Standing
}

// userAccount returns the account a (kind, ref) pair names: a "user" kind with
// a canonical id. Any other pair names no account.
func userAccount(kind, ref string) (model.ID, bool) {
	if kind != "user" {
		return "", false
	}
	id, err := model.ParseID(ref)
	if err != nil || id.IsZero() {
		return "", false
	}
	return id, true
}

// recipientAccounts returns the accounts the recipients name.
func recipientAccounts(recipients ...RecipientRef) []model.ID {
	var out []model.ID
	for _, r := range recipients {
		if id, ok := userAccount(string(r.Kind), r.Ref); ok {
			out = append(out, id)
		}
	}
	return out
}

// actorAccounts returns the accounts the actors name.
func actorAccounts(actors ...CommunicationActorRef) []model.ID {
	var out []model.ID
	for _, a := range actors {
		if id, ok := userAccount(string(a.Kind), a.Ref); ok {
			out = append(out, id)
		}
	}
	return out
}

// channelGrantAccounts returns the accounts the grants' subjects name.
func channelGrantAccounts(grants ...ChannelGrantInput) []model.ID {
	var out []model.ID
	for _, g := range grants {
		if id, ok := userAccount(string(g.Subject.Kind), g.Subject.Ref); ok {
			out = append(out, id)
		}
	}
	return out
}

// workCommandOwnerAccounts returns the account a work command makes the item's
// owner: item.create and item.assign with a user owner.
func workCommandOwnerAccounts(cmd WorkCommand) []model.ID {
	if cmd.Command != "item.create" && cmd.Command != "item.assign" {
		return nil
	}
	if id, ok := userAccount(cmd.OwnerKind, cmd.OwnerRef); ok {
		return []model.ID{id}
	}
	return nil
}

// protocolMCPTaskAccounts returns the accounts an MCP task projection's owner
// names: its subject and the account it acts for.
func protocolMCPTaskAccounts(task *ProtocolMCPTaskProjection) []model.ID {
	if task == nil {
		return nil
	}
	var out []model.ID
	for _, ref := range []string{task.Owner.Subject, task.Owner.ActAs} {
		if id, err := model.ParseID(ref); err == nil && !id.IsZero() {
			out = append(out, id)
		}
	}
	return out
}

// writeAccountFenceRefusal answers a fence refusal with 409 and its stable
// code, in the core API error envelope, and reports whether err was one.
func writeAccountFenceRefusal(w http.ResponseWriter, err error) bool {
	code, ok := auth.FenceRefusalCode(err)
	if !ok {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": map[string]string{"code": code, "message": err.Error()},
	})
	return true
}

// communicationFenceKey carries one attempt of a fenced communication command.
type communicationFenceKey struct{}

// communicationFence is one attempt of a fenced communication command: the
// authority versions its transaction pins, whether the barrier found one of
// them, or a fact pinned with them, moved, and whether the attempt's outcome is
// final.
type communicationFence struct {
	refs  []store.UserAuthorityFactRef
	moved atomic.Bool
	final atomic.Bool
}

func communicationFenceFrom(ctx context.Context) *communicationFence {
	f, _ := ctx.Value(communicationFenceKey{}).(*communicationFence)
	return f
}

// keepCommunicationOutcome makes the running attempt's outcome final: the
// fenced command is not run again, even when its fence moved. A command calls
// it once the failure it met is one it answers itself.
func keepCommunicationOutcome(ctx context.Context) {
	if fence := communicationFenceFrom(ctx); fence != nil {
		fence.final.Store(true)
	}
}

// fencedCommunicationCommand runs a communication command that may store a
// reference naming accounts. The command arms its attempt's fence with
// fenceCommunicationAccounts before it opens its transaction; when the barrier
// then finds a pinned version moved, the whole command runs once more, reading
// the standing again, unless the attempt made its outcome final. A second move
// answers the command's own conflict.
func (m *Module) fencedCommunicationCommand(ctx context.Context, command func(context.Context) error) error {
	for attempt := 0; ; attempt++ {
		fence := &communicationFence{}
		err := command(context.WithValue(ctx, communicationFenceKey{}, fence))
		if err == nil || attempt > 0 || !fence.moved.Load() || fence.final.Load() {
			return err
		}
	}
}

// fenceCommunicationAccounts reads the standing of the accounts a command is
// about to name in tenant and arms the command's fence with their authority
// versions: the communication transactions the command opens next pin them in
// their authority lock. It answers the standing's refusal for an account being
// removed or erased. When no id names an account nothing is armed, and the
// command locks exactly as before.
func (m *Module) fenceCommunicationAccounts(ctx context.Context, tenant model.TenantID, users []model.ID) error {
	refs, err := auth.FenceSubjects(ctx, m.standingFor(ctx), tenant, users)
	if err != nil || len(refs) == 0 {
		return err
	}
	fence := communicationFenceFrom(ctx)
	if fence == nil {
		return communicationTransactionUnavailable("account fence", nil)
	}
	fence.refs = refs
	return nil
}
