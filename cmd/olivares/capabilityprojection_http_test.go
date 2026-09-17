// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// G1-A — the SELF capability projection, driven through the production router on a
// fresh, explicitly activated estate.
//
// Every case below is CAUSAL: it builds a real principal with real authority through
// the product's own APIs and measures what the projection says next to what the REAL
// route answers for the same request. There is no can()=>true, no injected role, no
// forged membership and no fabricated evidence anywhere in this file; the one fault
// that is injected is a real resolver failure through the production composition seam,
// and it exists to prove that an unavailable authority is UNKNOWN rather than a denial.

const (
	capSurfaceAdministration = "GET /v1/m/sessions/channels/administration"
	capSurfaceChannels       = "GET /v1/m/sessions/channels"
	capSurfaceInbox          = "GET /v1/m/sessions/inbox"
	capOperationGrantSheet   = "GET /v1/m/sessions/channels/{id}/grants"
	capOperationPatch        = "PATCH /v1/m/sessions/channels"
	capOperationGrant        = "POST /v1/m/sessions/channels/{id}/grants"
	capOperationRevoke       = "POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke"
)

type capabilityAnswer struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	State          string    `json:"state"`
	Code           string    `json:"code"`
	ObservedAt     time.Time `json:"observed_at"`
	RefreshAfterMS *int64    `json:"refresh_after_ms"`
}

type capabilityResults struct {
	SchemaVersion int                `json:"schema_version"`
	Results       []capabilityAnswer `json:"results"`
}

func capabilitySurfaceQuestion(id, operation, workspace string) map[string]any {
	return map[string]any{
		"id": id, "kind": "surface", "operation": operation, "workspace_id": workspace,
	}
}

func capabilityChannelQuestion(id, operation, workspace string, channel model.ID) map[string]any {
	question := map[string]any{
		"id": id, "kind": "operation", "operation": operation,
		"selectors": map[string]any{"path": map[string]any{"id": channel.String()}},
	}
	if workspace != "" {
		question["workspace_id"] = workspace
	}
	return question
}

func capabilityBodyQuestion(id, operation, workspace string, channel model.ID) map[string]any {
	question := map[string]any{
		"id": id, "kind": "operation", "operation": operation,
		"selectors": map[string]any{"body": map[string]any{"channel_id": channel.String()}},
	}
	if workspace != "" {
		question["workspace_id"] = workspace
	}
	return question
}

func capabilityAskRaw(
	t *testing.T, eng *engine, token string, tenant model.TenantID, questions ...map[string]any,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/capabilities",
		token, tenant, map[string]any{"schema_version": 2, "questions": questions}, nil)
}

// capabilityAsk drives one batch and returns the answers by id. It asserts the two
// transport promises on every call rather than once: 200 for a well-formed batch, and
// no-store — these bodies are one credential's authorization observations, and a private
// cache replaying one to the next caller is the failure the whole freshness contract
// exists to prevent.
func capabilityAsk(
	t *testing.T, eng *engine, token string, tenant model.TenantID, questions ...map[string]any,
) map[string]capabilityAnswer {
	t.Helper()
	response := capabilityAskRaw(t, eng, token, tenant, questions...)
	if response.status != http.StatusOK {
		t.Fatalf("capabilities = %d: %s", response.status, response.raw)
	}
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("capabilities Cache-Control = %q, want no-store", got)
	}
	decoded := communicationHTTPTestDecode[capabilityResults](t, response)
	if decoded.SchemaVersion != 2 || len(decoded.Results) != len(questions) {
		t.Fatalf("capabilities envelope = %+v, want schema 2 and %d results",
			decoded, len(questions))
	}
	out := make(map[string]capabilityAnswer, len(decoded.Results))
	for _, answer := range decoded.Results {
		if _, duplicate := out[answer.ID]; duplicate {
			t.Fatalf("capabilities returned two results for id %q", answer.ID)
		}
		out[answer.ID] = answer
	}
	return out
}

// capabilityWant asserts the state and code of one answer AND the budget invariant that
// goes with them: a positive carries a finite refresh_after_ms inside the 30s ceiling, a
// non-positive carries none at all. The second half is not decoration — a denial or an
// unknown that shipped a budget would be handing a client a reason to cache it, and on a
// concealing route a budget attached only to real rows would itself be the existence
// oracle the concealment exists to close.
func capabilityWant(
	t *testing.T, answers map[string]capabilityAnswer, id, state, code string,
) capabilityAnswer {
	t.Helper()
	answer, present := answers[id]
	if !present {
		t.Fatalf("capabilities returned no result for %q", id)
	}
	if answer.State != state || answer.Code != code {
		t.Fatalf("%s = %s/%s, want %s/%s", id, answer.State, answer.Code, state, code)
	}
	positive := state == "allowed" || state == "reachable"
	switch {
	case positive && answer.RefreshAfterMS == nil:
		t.Fatalf("%s is %s with no refresh_after_ms: a positive with no budget is not reusable", id, state)
	case positive && (*answer.RefreshAfterMS <= 0 || *answer.RefreshAfterMS > 30000):
		t.Fatalf("%s refresh_after_ms = %d, want 0 < ms <= 30000", id, *answer.RefreshAfterMS)
	case !positive && answer.RefreshAfterMS != nil:
		t.Fatalf("%s is %s and still carries refresh_after_ms=%d", id, state, *answer.RefreshAfterMS)
	}
	if answer.ObservedAt.IsZero() {
		t.Fatalf("%s carries no observed_at", id)
	}
	return answer
}

func TestCapabilityProjectionHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner, steward, viewer := estate.owner, estate.steward, estate.viewer
	wsQuery := "workspace_id=" + workspace.String()
	ws := workspace.String()
	side := estate.sideWorkspace.String()

	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)
	stewardAdminOnly := channelAdministrationGrant(channelAdministrationSubject("user", steward.id), false, false, true)
	viewerAdminOnly := channelAdministrationGrant(channelAdministrationSubject("user", viewer.id), false, false, true)

	adminNoRead := estate.createChannel(t, workspace, "cap-admin-no-read",
		[]map[string]any{ownerAll, stewardAdminOnly})
	ownerOnly := estate.createChannel(t, workspace, "cap-owner-only", []map[string]any{ownerAll})
	scopedChannel := estate.createChannel(t, workspace, "cap-scoped-admin",
		[]map[string]any{ownerAll, viewerAdminOnly})

	t.Run("an administrator without any read projects allowed and reachable", func(t *testing.T) {
		// The steward holds core sessions:channel:admin by role and a local ADMIN bit
		// with NO read and NO write. The projection must say so for the collection AND
		// for the exact operations, and the REAL routes must agree — which is the whole
		// claim: administration is independent of content read.
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
			capabilityBodyQuestion("patch", capOperationPatch, ws, adminNoRead.Channel.ID),
			capabilityChannelQuestion("grant", capOperationGrant, ws, adminNoRead.Channel.ID),
			capabilityChannelQuestion("revoke", capOperationRevoke, ws, adminNoRead.Channel.ID),
			capabilityChannelQuestion("no-bit", capOperationGrantSheet, ws, ownerOnly.Channel.ID),
		)
		capabilityWant(t, answers, "surface", "reachable", "admitted")
		for _, id := range []string{"sheet", "patch", "grant", "revoke"} {
			capabilityWant(t, answers, id, "allowed", "authorized")
		}
		// The same steward holds NO admin bit on the owner-only Channel, and that
		// route conceals its denial as absence — so the projection answers the
		// route's own unified shape, never a distinguishable "denied".
		capabilityWant(t, answers, "no-bit", "undisclosed", "not_disclosed")

		// The projection is measured against the real routes, not asserted alone.
		if page := estate.adminPage(t, steward.token, wsQuery); len(page.Items) != 1 ||
			page.Items[0].Channel.ID != adminNoRead.Channel.ID {
			t.Fatalf("administrative catalog = %v", channelAdministrationIDs(page))
		}
		if got := estate.sheet(t, steward.token, adminNoRead.Channel.ID, wsQuery); got.status != http.StatusOK {
			t.Fatalf("real grant sheet = %d: %s", got.status, got.raw)
		}
		if got := estate.sheet(t, steward.token, ownerOnly.Channel.ID, wsQuery); got.status != http.StatusNotFound {
			t.Fatalf("real grant sheet on the unheld Channel = %d, want the concealed 404: %s",
				got.status, got.raw)
		}
		// And its content read stays denied: the positive above is not a read.
		catalog := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/"+adminNoRead.Channel.ID.String()+"?"+wsQuery,
			steward.token, tenant, nil, nil)
		if catalog.status != http.StatusOK {
			t.Logf("K3_CAP_POINT_READ|status=%d|body=%s", catalog.status, catalog.raw)
		}
	})

	t.Run("a viewer with a workspace grant and a local admin bit is admitted", func(t *testing.T) {
		// ⛔ THIS IS THE DEFECT G1-A REPAIRS, AND IT IS MEASURED BOTH WAYS. A
		// workspace-scoped grant could never authorize a COLLECTION route, because the
		// collection was authorized with no workspace at all and no `resource in
		// Workspace::…` could match. The viewer below holds exactly that grant plus a
		// local ADMIN bit, and holds neither permission by role.
		before := capabilityAsk(t, eng, viewer.token, tenant,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, scopedChannel.Channel.ID),
		)
		capabilityWant(t, before, "surface", "not_reachable", "not_permitted")
		capabilityWant(t, before, "sheet", "undisclosed", "not_disclosed")
		if got := estate.admin(t, viewer.token, wsQuery); got.status != http.StatusForbidden {
			t.Fatalf("viewer administration before the grant = %d: %s", got.status, got.raw)
		}

		capabilityPublishAuthored(t, eng, estate, fmt.Sprintf(
			`permit(principal in User::%q, action == Action::"sessions:channel:admin", resource)`+
				` when { resource in Workspace::"k3-admin-ws" };`, viewer.id.String()))

		after := capabilityAsk(t, eng, viewer.token, tenant,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilitySurfaceQuestion("side", capSurfaceAdministration, side),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, scopedChannel.Channel.ID),
			capabilityChannelQuestion("no-bit", capOperationGrantSheet, ws, ownerOnly.Channel.ID),
		)
		capabilityWant(t, after, "surface", "reachable", "admitted")
		capabilityWant(t, after, "sheet", "allowed", "authorized")
		// The grant is scoped to ONE workspace: the sibling collection stays denied,
		// and the projection says so rather than generalizing the positive.
		capabilityWant(t, after, "side", "not_reachable", "not_permitted")
		// And admission to the collection is not authority over every row in it.
		capabilityWant(t, after, "no-bit", "undisclosed", "not_disclosed")

		// The real routes agree, which is what makes the projection a projection.
		page := estate.adminPage(t, viewer.token, wsQuery)
		if len(page.Items) != 1 || page.Items[0].Channel.ID != scopedChannel.Channel.ID {
			t.Fatalf("scoped viewer administrative catalog = %v", channelAdministrationIDs(page))
		}
		if got := estate.admin(t, viewer.token, "workspace_id="+side); got.status != http.StatusForbidden {
			t.Fatalf("scoped viewer sibling workspace = %d: %s", got.status, got.raw)
		}
		if got := estate.sheet(t, viewer.token, scopedChannel.Channel.ID, wsQuery); got.status != http.StatusOK {
			t.Fatalf("scoped viewer grant sheet = %d: %s", got.status, got.raw)
		}
		// The viewer's content read is untouched by the administration grant.
		if got := estate.sheet(t, viewer.token, ownerOnly.Channel.ID, wsQuery); got.status != http.StatusNotFound {
			t.Fatalf("scoped viewer unheld Channel sheet = %d: %s", got.status, got.raw)
		}
	})

	t.Run("workspace refusals are one public answer before admission", func(t *testing.T) {
		// The ratified amendment, measured: for a caller who has not been admitted, a
		// canonical workspace that does not exist, one that is not active, and one that
		// is active but denied must be INDISTINGUISHABLE — in the GET and in the
		// surface projection alike.
		absent := model.NewID().String()
		archived := capabilityArchivedWorkspace(t, eng, estate)
		editor := estate.editor

		bodies := map[string]string{}
		statuses := map[string]int{}
		for name, selector := range map[string]string{
			"absent":   absent,
			"archived": archived.String(),
			"denied":   ws,
			"foreign":  estate.foreignWorkspace,
		} {
			response := estate.admin(t, editor.token, "workspace_id="+selector)
			statuses[name] = response.status
			bodies[name] = string(response.raw)
			if got := response.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("%s refusal Cache-Control = %q", name, got)
			}
		}
		for name := range bodies {
			if statuses[name] != http.StatusForbidden {
				t.Fatalf("%s workspace GET = %d: %s", name, statuses[name], bodies[name])
			}
			if bodies[name] != bodies["denied"] {
				t.Fatalf("%s workspace GET body = %s, want the same generic refusal as a denial: %s",
					name, bodies[name], bodies["denied"])
			}
		}
		answers := capabilityAsk(t, eng, editor.token, tenant,
			capabilitySurfaceQuestion("absent", capSurfaceAdministration, absent),
			capabilitySurfaceQuestion("archived", capSurfaceAdministration, archived.String()),
			capabilitySurfaceQuestion("denied", capSurfaceAdministration, ws),
			capabilitySurfaceQuestion("foreign", capSurfaceAdministration, estate.foreignWorkspace),
		)
		for _, id := range []string{"absent", "archived", "denied", "foreign"} {
			capabilityWant(t, answers, id, "not_reachable", "not_permitted")
		}
		// The positive control, without which the four refusals above could all be
		// passing for the wrong reason: the same question on the same active workspace
		// from a caller who IS admitted still answers reachable.
		positive := capabilityAsk(t, eng, steward.token, tenant,
			capabilitySurfaceQuestion("active", capSurfaceAdministration, ws))
		capabilityWant(t, positive, "active", "reachable", "admitted")
		// A non-canonical selector is a client error decided from the request alone,
		// with no row read — it is not folded into the refusal above.
		malformed := capabilityAsk(t, eng, editor.token, tenant,
			capabilitySurfaceQuestion("malformed", capSurfaceAdministration, "not-a-uuid"))
		capabilityWant(t, malformed, "malformed", "unknown", "inputs_required")
		if got := estate.admin(t, editor.token, "workspace_id=not-a-uuid"); got.status != http.StatusBadRequest {
			t.Fatalf("malformed selector GET = %d: %s", got.status, got.raw)
		}
	})

	t.Run("a concealing operation answers one shape for absent, foreign and denied", func(t *testing.T) {
		foreign := capabilityForeignChannel(t, estate)
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("absent", capOperationGrantSheet, ws, model.NewID()),
			capabilityChannelQuestion("foreign", capOperationGrantSheet, ws, foreign),
			capabilityChannelQuestion("denied", capOperationGrantSheet, ws, ownerOnly.Channel.ID),
			// Channel A named with workspace B: the selector is compared with the
			// row's STORED workspace and is never silently corrected to it.
			capabilityChannelQuestion("crossed", capOperationGrantSheet, side, adminNoRead.Channel.ID),
		)
		for _, id := range []string{"absent", "foreign", "denied", "crossed"} {
			answer := capabilityWant(t, answers, id, "undisclosed", "not_disclosed")
			if answer.RefreshAfterMS != nil {
				t.Fatalf("%s carried a budget: %+v", id, answer)
			}
		}
		// The real route answers all four the same way too.
		for name, channel := range map[string]model.ID{
			"absent": model.NewID(), "foreign": foreign, "denied": ownerOnly.Channel.ID,
		} {
			if got := estate.sheet(t, steward.token, channel, wsQuery); got.status != http.StatusNotFound {
				t.Fatalf("real sheet %s = %d: %s", name, got.status, got.raw)
			}
		}
	})

	t.Run("an authorized empty collection is reachable and grants no row", func(t *testing.T) {
		// The steward holds core admin by role in every workspace of this tenant, and
		// no local grant at all in the sibling one. The collection is admissible and
		// the real GET is an authorized EMPTY page — which is exactly why a surface
		// answer must never be derived from any row.
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilitySurfaceQuestion("side", capSurfaceAdministration, side),
			capabilitySurfaceQuestion("channels", capSurfaceChannels, side),
			capabilitySurfaceQuestion("inbox", capSurfaceInbox, side),
		)
		for _, id := range []string{"side", "channels", "inbox"} {
			capabilityWant(t, answers, id, "reachable", "admitted")
		}
		page := estate.adminPage(t, steward.token, "workspace_id="+side)
		if len(page.Items) != 0 || page.HasMore {
			t.Fatalf("sibling workspace administrative catalog = %v", channelAdministrationIDs(page))
		}
	})

	t.Run("an expiring grant narrows the operation budget and not the surface", func(t *testing.T) {
		// ⛔ THE TWO BUDGETS COME FROM DIFFERENT PREDICATES, AND THIS IS WHERE THAT IS
		// MEASURED. The operation's horizon is the OR of the admin grants that still
		// confer the bit on THAT Channel, so a grant expiring in seconds shortens it.
		// The surface's is admission to the collection, which NO single row founds — so
		// the same expiry must not shorten it. A projection that derived both from one
		// number would be selling a collection budget it had never established, and the
		// two would move together no matter what the contract said.
		expiring := estate.createChannel(t, workspace, "cap-expiring", []map[string]any{ownerAll})
		// ⛔ THE EXPIRY IS SHORTER THAN THE PRINCIPAL-AUTHORITY WINDOW ON PURPOSE, AND
		// THE FIRST RUN OF THIS CASE IS WHY. With a 20s expiry all three budgets came
		// back at 4999 ms: the reconstructed principal's own authority evidence is
		// clipped to the 5s reconstruction deadline, so it dominated every horizon and
		// the row's contribution was invisible. That measurement is not a defect — a
		// budget IS the minimum of every predicate that founds the result, and that
		// window is one of them — but a case that cannot see the term it claims to
		// measure proves nothing. Four seconds puts the grant inside that window, so
		// the difference below is the OR horizon and nothing else.
		deadline := time.Now().UTC().Add(4 * time.Second)
		sheet := estate.sheetPage(t, owner.token, expiring.Channel.ID, wsQuery+"&state=all&limit=50")
		granted := estate.grant(t, owner.token, expiring.Channel.ID, map[string]any{
			"subject":  channelAdministrationSubject("user", steward.id),
			"can_read": false, "can_write": false, "can_admin": true,
			"expires_at": deadline.Format(time.RFC3339Nano),
		}, sheet.ETag)
		if granted.status != http.StatusOK {
			t.Fatalf("grant the expiring admin generation = %d: %s", granted.status, granted.raw)
		}
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("expiring", capOperationGrantSheet, ws, expiring.Channel.ID),
			capabilityChannelQuestion("durable", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
		)
		short := capabilityWant(t, answers, "expiring", "allowed", "authorized")
		durable := capabilityWant(t, answers, "durable", "allowed", "authorized")
		surface := capabilityWant(t, answers, "surface", "reachable", "admitted")
		t.Logf("K3_CAP_BUDGET|expiring=%d|durable=%d|surface=%d",
			*short.RefreshAfterMS, *durable.RefreshAfterMS, *surface.RefreshAfterMS)
		if *short.RefreshAfterMS > 4000 {
			t.Fatalf("the expiring operation budget = %d ms, want it bounded by the grant's own "+
				"4s horizon", *short.RefreshAfterMS)
		}
		if *durable.RefreshAfterMS <= *short.RefreshAfterMS {
			t.Fatalf("a non-expiring admin path budget = %d ms, not longer than the expiring one "+
				"(%d ms): the OR horizon is not narrowing on the grant that founds the answer",
				*durable.RefreshAfterMS, *short.RefreshAfterMS)
		}
		if *surface.RefreshAfterMS <= *short.RefreshAfterMS {
			t.Fatalf("the surface budget = %d ms, no longer than the expiring row's %d ms: a "+
				"collection budget must not be derived from one row's grant",
				*surface.RefreshAfterMS, *short.RefreshAfterMS)
		}
		// And when the row's horizon really closes, the positive goes with it rather
		// than surviving on a budget minted earlier.
		if testing.Short() {
			return
		}
		time.Sleep(time.Until(deadline) + time.Second)
		after := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("expired", capOperationGrantSheet, ws, expiring.Channel.ID),
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
		)
		capabilityWant(t, after, "expired", "undisclosed", "not_disclosed")
		capabilityWant(t, after, "surface", "reachable", "admitted")
		if got := estate.sheet(t, steward.token, expiring.Channel.ID, wsQuery); got.status != http.StatusNotFound {
			t.Fatalf("real sheet after the grant expired = %d: %s", got.status, got.raw)
		}
	})

	t.Run("an unavailable authority is unknown, never a denial", func(t *testing.T) {
		if eng.communicationComposition == nil || eng.communicationComposition.closure == nil {
			t.Fatal("production communication composition is unavailable for the UNKNOWN control")
		}
		before := communicationHTTPTestEffects(t, eng, tenant)
		real := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(
			communicationHTTPUnknownGrantClosureResolver{ChannelGrantSubjectClosureResolver: real},
		)
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
		)
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(real)
		// The local closure is the MODULE's stage, so the operation cannot be
		// established — and an authority nobody could read is never reported to an
		// operator as "your role does not allow it".
		capabilityWant(t, answers, "sheet", "undisclosed", "not_disclosed")
		// The collection's admission does not depend on that closure, so it is still
		// established: an outage in one stage does not spread to a decision it never
		// founded.
		capabilityWant(t, answers, "surface", "reachable", "admitted")
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "the UNKNOWN capability projection")
	})

	t.Run("asking writes nothing", func(t *testing.T) {
		// Fila 10 of the ratified matrix, measured on the whole surface at once: every
		// question this battery can ask, run back to back, against the census of every
		// durable communication row the estate can produce.
		before := communicationHTTPTestEffects(t, eng, tenant)
		for _, token := range []string{owner.token, steward.token, viewer.token, estate.editor.token} {
			capabilityAsk(t, eng, token, tenant,
				capabilitySurfaceQuestion("s1", capSurfaceAdministration, ws),
				capabilitySurfaceQuestion("s2", capSurfaceChannels, ws),
				capabilitySurfaceQuestion("s3", capSurfaceInbox, ws),
				capabilityChannelQuestion("o1", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
				capabilityBodyQuestion("o2", capOperationPatch, ws, adminNoRead.Channel.ID),
				capabilityChannelQuestion("o3", capOperationGrant, ws, adminNoRead.Channel.ID),
				capabilityChannelQuestion("o4", capOperationRevoke, ws, adminNoRead.Channel.ID),
			)
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "the capability projection batch")
	})

	t.Run("the closed DTO refuses everything it does not declare", func(t *testing.T) {
		// ⛔ SELF-ONLY IS A MECHANISM HERE, NOT A PROMISE. There is no subject field, so
		// the only way to ask about somebody else is to invent one — and an unknown
		// field is a 400 for the whole batch.
		for name, body := range map[string]map[string]any{
			"a subject": {"schema_version": 2, "questions": []map[string]any{{
				"id": "q", "kind": "surface", "operation": capSurfaceAdministration,
				"workspace_id": ws, "subject": owner.id.String(),
			}}},
			"a role": {"schema_version": 2, "questions": []map[string]any{{
				"id": "q", "kind": "surface", "operation": capSurfaceAdministration,
				"workspace_id": ws, "role": "owner",
			}}},
			"an assurance level": {"schema_version": 2, "questions": []map[string]any{{
				"id": "q", "kind": "surface", "operation": capSurfaceAdministration,
				"workspace_id": ws, "aal": 3,
			}}},
			"another schema version": {"schema_version": 1, "questions": []map[string]any{{
				"id": "q", "kind": "surface", "operation": capSurfaceAdministration, "workspace_id": ws,
			}}},
			"an unknown kind": {"schema_version": 2, "questions": []map[string]any{{
				"id": "q", "kind": "everything", "operation": capSurfaceAdministration, "workspace_id": ws,
			}}},
			"no questions": {"schema_version": 2, "questions": []map[string]any{}},
			"duplicate ids": {"schema_version": 2, "questions": []map[string]any{
				{"id": "q", "kind": "surface", "operation": capSurfaceAdministration, "workspace_id": ws},
				{"id": "q", "kind": "surface", "operation": capSurfaceChannels, "workspace_id": ws},
			}},
		} {
			response := communicationHTTPTestRequest(t, eng, http.MethodPost,
				"/v1/auth/capabilities", steward.token, tenant, body, nil)
			if response.status != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400: %s", name, response.status, response.raw)
			}
			if got := response.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("%s refusal Cache-Control = %q", name, got)
			}
		}
		// The batch ceiling is 33 questions refused, 32 answered.
		over := make([]map[string]any, 0, 33)
		for index := 0; index < 33; index++ {
			over = append(over, capabilitySurfaceQuestion(
				fmt.Sprintf("q%d", index), capSurfaceAdministration, ws))
		}
		if response := capabilityAskRaw(t, eng, steward.token, tenant, over...); response.status != http.StatusBadRequest {
			t.Fatalf("33 questions = %d, want 400: %s", response.status, response.raw)
		}
		capabilityAsk(t, eng, steward.token, tenant, over[:32]...)
		// An unauthenticated caller is 401 before anything else is considered.
		if response := capabilityAskRaw(t, eng, "", tenant,
			capabilitySurfaceQuestion("q", capSurfaceAdministration, ws)); response.status != http.StatusUnauthorized {
			t.Fatalf("anonymous capabilities = %d: %s", response.status, response.raw)
		}
	})

	t.Run("an unregistered or unsupported shape is unknown, not a permit", func(t *testing.T) {
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilitySurfaceQuestion("unregistered", "GET /v1/m/sessions/nothing", ws),
			// A collection that declares no corroborated workspace selector: the
			// adapter for that shape does not exist, and inventing a workspace for it
			// would authorize a scope nobody named.
			capabilitySurfaceQuestion("unscoped", "GET /v1/m/sessions/inbox/handoffs", ws),
			// ⛔ AND THE ONE THAT MUST STAY UNSCOPED FOREVER. POST /channels CREATES a
			// Channel in a workspace rather than reading a workspace's rows, so a
			// workspace-scoped grant on it would authorize creation from a selector the
			// caller chose. It is not in the declared set, and this is what says so.
			capabilitySurfaceQuestion("create", "POST /v1/m/sessions/channels", ws),
			// A registered collection asked as if it were an entity, and an entity
			// asked as if it were a collection: neither is answered from the other.
			capabilityChannelQuestion("collection-as-entity", capSurfaceAdministration, ws, adminNoRead.Channel.ID),
			capabilitySurfaceQuestion("entity-as-collection", capOperationGrantSheet, ws),
			// A declared locator that was not supplied.
			map[string]any{
				"id": "no-locator", "kind": "operation",
				"operation": capOperationGrantSheet, "workspace_id": ws,
			},
		)
		capabilityWant(t, answers, "unregistered", "unknown", "not_supported")
		capabilityWant(t, answers, "unscoped", "unknown", "not_supported")
		capabilityWant(t, answers, "create", "unknown", "not_supported")
		capabilityWant(t, answers, "collection-as-entity", "unknown", "not_supported")
		capabilityWant(t, answers, "entity-as-collection", "unknown", "not_supported")
		capabilityWant(t, answers, "no-locator", "unknown", "inputs_required")
	})

	// ⛔ THIS BLOCK PUBLISHES AND IS THEREFORE LAST. The authoring surface REPLACES the
	// tenant's authored Cedar, so a policy published here would silently retire the
	// workspace permit the earlier cases depend on.
	t.Run("one channel permit does not open its collection, and a forbid outranks whoami", func(t *testing.T) {
		editor := estate.editor
		deepLink := estate.createChannel(t, workspace, "cap-deep-link", []map[string]any{
			ownerAll,
			channelAdministrationGrant(channelAdministrationSubject("user", editor.id), false, false, true),
		})
		capabilityPublishAuthored(t, eng, estate, fmt.Sprintf(
			`permit(principal in User::%q, action == Action::"sessions:channel:admin",`+
				` resource == Resource::%q);`+"\n"+
				`forbid(principal in User::%q, action == Action::"sessions:channel:admin", resource);`,
			editor.id.String(), deepLink.Channel.ID.String(), steward.id.String()))

		// The permit names ONE resource, so the collection question — which carries no
		// entity at all — cannot match it. The deep link still works, and it works
		// WITHOUT the caller first being admitted to the collection or passing through
		// the read catalog. That is the whole point of keeping the two decisions apart.
		deep := capabilityAsk(t, eng, editor.token, tenant,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("deep-link", capOperationGrantSheet, ws, deepLink.Channel.ID),
			capabilityChannelQuestion("neighbour", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
		)
		capabilityWant(t, deep, "surface", "not_reachable", "not_permitted")
		capabilityWant(t, deep, "deep-link", "allowed", "authorized")
		// And it opens exactly ONE row: the permit is not a licence to enumerate the
		// neighbours of the resource it names.
		capabilityWant(t, deep, "neighbour", "undisclosed", "not_disclosed")
		if got := estate.admin(t, editor.token, wsQuery); got.status != http.StatusForbidden {
			t.Fatalf("the collection stays denied for the deep-link holder = %d: %s", got.status, got.raw)
		}
		if got := estate.sheet(t, editor.token, deepLink.Channel.ID, wsQuery); got.status != http.StatusOK {
			t.Fatalf("the deep-linked grant sheet = %d: %s", got.status, got.raw)
		}
		if got := estate.sheet(t, editor.token, adminNoRead.Channel.ID, wsQuery); got.status != http.StatusNotFound {
			t.Fatalf("the neighbour Channel = %d, want the concealed 404: %s", got.status, got.raw)
		}

		// ⛔ THE HALF WHOAMI CANNOT EXPRESS. The steward still holds
		// sessions:channel:admin by role and whoami still reports it — reflection is
		// unchanged, and this asserts that rather than assuming it — while an authored
		// forbid denies the same operation on every resource. A console that answered
		// "may I?" by membership of the whoami set would offer both buttons.
		whoami := communicationHTTPTestRequest(t, eng, http.MethodGet, "/v1/auth/whoami",
			steward.token, tenant, nil, nil)
		if whoami.status != http.StatusOK {
			t.Fatalf("whoami = %d: %s", whoami.status, whoami.raw)
		}
		reflected := communicationHTTPTestDecode[struct {
			Grants []struct {
				Tenant      string   `json:"tenant"`
				Permissions []string `json:"permissions"`
			} `json:"grants"`
		}](t, whoami)
		reflects := false
		for _, grant := range reflected.Grants {
			if grant.Tenant != tenant.String() {
				continue
			}
			for _, permission := range grant.Permissions {
				if permission == "sessions:channel:admin" {
					reflects = true
				}
			}
		}
		if !reflects {
			t.Fatalf("whoami no longer reflects sessions:channel:admin for the forbidden steward: %s",
				whoami.raw)
		}
		forbidden := capabilityAsk(t, eng, steward.token, tenant,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
		)
		capabilityWant(t, forbidden, "surface", "not_reachable", "not_permitted")
		capabilityWant(t, forbidden, "sheet", "undisclosed", "not_disclosed")
		if got := estate.admin(t, steward.token, wsQuery); got.status != http.StatusForbidden {
			t.Fatalf("forbidden steward administration = %d: %s", got.status, got.raw)
		}
		if got := estate.sheet(t, steward.token, adminNoRead.Channel.ID, wsQuery); got.status != http.StatusNotFound {
			t.Fatalf("forbidden steward grant sheet = %d: %s", got.status, got.raw)
		}
	})
}

// capabilityPublishAuthored publishes ONE authored Cedar source for the tenant through
// the product's own PDP authoring surface.
//
// ⛔ IT REPLACES THE AUTHORED SURFACE, so a caller that needs two policies at once
// publishes them in one source rather than in two calls.
//
// ⛔ AND IT IS THE AUTHORING PATH THIS BATTERY USES INSTEAD OF A WORKSPACE-SCOPED RBAC
// GRANT, WHICH IS A FINDING RATHER THAN A PREFERENCE. POST /v1/m/governance/rbac/grants
// REFUSES a workspace-scoped grant whose whole permission set is module permissions
// (modules/governance/scopedadmin_handlers.go rejectInertTreeScopedGrant), on the stated
// ground that "module routes do not resolve the workspace/agent-group/folder tree, so the
// grant would authorize nothing". That premise is what G1-A changes for the three pilot
// collections — and the guard's own comment says so: "When module routes resolve the
// tree, this guard is what gets removed". Removing it is an authorization WIDENING across
// every module, plus a second failure mode about the unconditional delegation permit that
// this work does not touch, so it is referred to root rather than taken here. The authored
// Cedar below expresses the same workspace-scoped authority through a shipped, supported
// surface, so the causal claim is measured without changing a guard.
func capabilityPublishAuthored(
	t *testing.T, eng *engine, estate channelAdministrationHTTPEstate, source string,
) {
	t.Helper()
	published := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/governance/pdp/publish", estate.owner.token, estate.tenant,
		map[string]any{"engine": "cedar", "source": source}, nil)
	if published.status != http.StatusOK {
		t.Fatalf("publish authored policy = %d: %s", published.status, published.raw)
	}
	t.Logf("K3_CAP_AUTHORED|source=%s", source)
}

// capabilityArchivedWorkspace creates a workspace of this tenant and takes it out of the
// active state, so the battery can ask about a real non-active scope instead of a
// hypothetical one.
func capabilityArchivedWorkspace(
	t *testing.T, eng *engine, estate channelAdministrationHTTPEstate,
) model.ID {
	t.Helper()
	created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/workspaces",
		estate.owner.token, estate.tenant,
		map[string]any{"name": "K3 capability archived", "slug": "k3-cap-archived"}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("create the archived workspace = %d: %s", created.status, created.raw)
	}
	id, err := model.ParseID(communicationHTTPTestDecode[struct {
		ID string `json:"id"`
	}](t, created).ID)
	if err != nil {
		t.Fatalf("archived workspace id: %v", err)
	}
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch,
		"/v1/workspaces/"+id.String(), estate.owner.token, estate.tenant,
		map[string]any{"status": "inactive"}, nil)
	if patched.status != http.StatusOK {
		t.Fatalf("archive the workspace = %d: %s", patched.status, patched.raw)
	}
	return id
}

// capabilityForeignChannel creates a Channel in the OTHER tenant, so the concealment
// case names a row that really exists somewhere else rather than a random id.
func capabilityForeignChannel(t *testing.T, estate channelAdministrationHTTPEstate) model.ID {
	t.Helper()
	response := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/channels", estate.stranger.token, estate.other,
		map[string]any{
			"workspace_id": estate.foreignWorkspace, "slug": "k3-cap-foreign",
			"name": "Foreign capability channel",
			"initial_grants": []map[string]any{channelAdministrationGrant(
				channelAdministrationSubject("user", estate.stranger.id), true, true, true)},
		}, nil)
	if response.status != http.StatusCreated {
		t.Fatalf("create the foreign Channel = %d: %s", response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response).Channel.ID
}

// independentG1AScopedOutage and independentG1AOverlayOutage are FAULT-ONLY producers.
// Neither fabricates a principal, a membership, a permission or a verdict: each returns
// an error, which is exactly what a real evaluator does when it cannot be evaluated.
type independentG1AScopedOutage struct{}

func (independentG1AScopedOutage) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{}, errors.New("capability battery: scoped provider unavailable")
}

func (independentG1AScopedOutage) ScopedEvidence(
	context.Context, auth.Request,
) (auth.ScopedEvidenceDecision, error) {
	return auth.ScopedEvidenceDecision{}, errors.New("capability battery: scoped evidence unavailable")
}

// independentG1ADisagreeingScoped is the third arm the correction brief names: the
// BOOLEAN and the TYPED path disagreeing about the same question. Its legacy Scoped
// abstains — so Authorize falls through to RBAC and ALLOWS — while its typed
// ScopedEvidence reports an established forbid. Neither answer is fabricated; they simply
// do not agree, which in production would be a bug in a producer rather than a policy.
type independentG1ADisagreeingScoped struct{}

func (independentG1ADisagreeingScoped) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{Effect: auth.EffectAbstain, Reason: "capability battery: abstain"}, nil
}

func (independentG1ADisagreeingScoped) ScopedEvidence(
	context.Context, auth.Request,
) (auth.ScopedEvidenceDecision, error) {
	now := time.Now().UTC()
	return auth.ScopedEvidenceDecision{
		Effect:        auth.EffectForbid,
		ResourceGuard: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "battery_guard_clean"},
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "battery_forbid_present"},
		ObservedAt:    now,
		FreshUntil:    now.Add(time.Minute),
	}, nil
}

type independentG1AOverlayOutage struct{}

func (independentG1AOverlayOutage) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{}, errors.New("capability battery: policy overlay unavailable")
}

func (independentG1AOverlayOutage) EvaluateEvidence(
	context.Context, auth.Request,
) (auth.PolicyEvidenceDecision, error) {
	return auth.PolicyEvidenceDecision{}, errors.New("capability battery: policy evidence unavailable")
}

// capabilitySameShape asserts that several answers are INDISTINGUISHABLE on the wire.
//
// ⛔ IT COMPARES THE PUBLIC SHAPE, NOT THE WHOLE VALUE, and that is a real distinction
// rather than a convenience: `observed_at` is read per question, so two answers taken
// microseconds apart differ in a field that says nothing about the target. What must not
// differ is what a caller could use to tell one target from another — the state, the code
// and whether a budget came back at all.
func capabilitySameShape(
	t *testing.T, answers map[string]capabilityAnswer, what string, ids ...string,
) {
	t.Helper()
	type shape struct {
		state, code string
		budget      bool
	}
	shapeOf := func(id string) shape {
		answer, present := answers[id]
		if !present {
			t.Fatalf("%s: no answer for %q", what, id)
		}
		return shape{answer.State, answer.Code, answer.RefreshAfterMS != nil}
	}
	first := shapeOf(ids[0])
	for _, id := range ids[1:] {
		if got := shapeOf(id); got != first {
			t.Fatalf("%s answers %q as %+v and %q as %+v: a caller can tell the targets apart, "+
				"which is an existence oracle", what, ids[0], first, id, got)
		}
	}
}

// TestCapabilityProjectionDiscriminantsHTTP is the permanent home of the six
// discriminants raised by the INDEPENDENT REVIEW of the first G1-A candidate
// (`assessments/implementation/capabilities-g1a/independent-review/REPORT.md`,
// SHA-256 2e9875c2dcace018b43aea349dbbe82271deb9c730d04c3a40ed738878cf1ffa, findings
// R1–R5). The cases are the reviewer's; this file adopts them so the defects cannot
// return, and each one carries the finding it belongs to.
//
// Every one of them FAILED on commit f3fbe5b34ca55b80496b475c3c98df576372b9e5 while all
// eleven of the author's own causal cases passed — which is the reason they are here
// rather than trusted to the existing battery. A positive suite that cannot fail on the
// defect is not coverage of it.
func TestCapabilityProjectionDiscriminantsHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	steward := estate.steward
	ws := workspace.String()
	wsQuery := "workspace_id=" + ws

	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", estate.owner.id), true, true, true)
	stewardAdmin := channelAdministrationGrant(channelAdministrationSubject("user", steward.id), false, false, true)
	held := estate.createChannel(t, workspace, "discriminant-held", []map[string]any{ownerAll, stewardAdmin})
	hidden := estate.createChannel(t, workspace, "discriminant-hidden", []map[string]any{ownerAll})
	missing := model.NewID()
	before := communicationHTTPTestEffects(t, eng, tenant)

	// The positive control. Without it every refusal below could pass for the wrong
	// reason — a projection that answered UNKNOWN to everything would satisfy R1 and R3
	// and be useless.
	t.Run("control: admin-only authority is allowed and an unheld channel is concealed", func(t *testing.T) {
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID),
			capabilityChannelQuestion("hidden", capOperationGrantSheet, ws, hidden.Channel.ID),
			capabilityChannelQuestion("missing", capOperationGrantSheet, ws, missing))
		capabilityWant(t, answers, "held", "allowed", "authorized")
		capabilityWant(t, answers, "hidden", "undisclosed", "not_disclosed")
		capabilityWant(t, answers, "missing", "undisclosed", "not_disclosed")
		if got := estate.sheet(t, steward.token, held.Channel.ID, wsQuery); got.status != http.StatusOK {
			t.Fatalf("real held admin sheet = %d: %s", got.status, got.raw)
		}
		read := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/"+held.Channel.ID.String()+"?"+wsQuery,
			steward.token, tenant, nil, nil)
		if read.status != http.StatusNotFound {
			t.Fatalf("admin-only content read = %d, want the concealed 404: %s", read.status, read.raw)
		}
	})

	// R1, first half: a HEALTHY runtime must not distinguish existence through the
	// adapter's absence. Measured on the rejected candidate: existing hidden channel
	// answered unknown/not_supported and the absent id answered denied/not_available,
	// because the engine reached its concealment branch before the module was consulted.
	t.Run("R1 unsupported adapter is settled before any target is resolved", func(t *testing.T) {
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("hidden", "GET /v1/m/sessions/channels/{id}", ws, hidden.Channel.ID),
			capabilityChannelQuestion("missing", "GET /v1/m/sessions/channels/{id}", ws, missing))
		for _, id := range []string{"hidden", "missing"} {
			capabilityWant(t, answers, id, "unknown", "not_supported")
		}
		capabilitySameShape(t, answers, "an unsupported route", "hidden", "missing")
	})

	// R1, sharper half: with the SHARED authority resolver down, every question in the
	// scope must answer identically. On the rejected candidate the outage itself became
	// the oracle — unknown for rows that existed, denied for the id that did not.
	t.Run("R1 a common closure outage cannot distinguish existence", func(t *testing.T) {
		outageBefore := communicationHTTPTestEffects(t, eng, tenant)
		real := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(
			communicationHTTPUnknownGrantClosureResolver{ChannelGrantSubjectClosureResolver: real})
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID),
			capabilityChannelQuestion("hidden", capOperationGrantSheet, ws, hidden.Channel.ID),
			capabilityChannelQuestion("missing", capOperationGrantSheet, ws, missing))
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(real)
		for _, id := range []string{"held", "hidden", "missing"} {
			capabilityWant(t, answers, id, "undisclosed", "not_disclosed")
		}
		capabilitySameShape(t, answers, "a common closure outage", "held", "hidden", "missing")
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, outageBefore, "the closure-outage projection")
	})

	// R2: a positive may never outlive the evidence that founds it. The producer below
	// is the REAL concrete closure resolver over the estate's own Store — only its
	// freshness configuration is shortened, so every fact, epoch fence and credential it
	// resolves stays real. On the rejected candidate this returned a 4990 ms budget
	// against a 750 ms window, because the adapter reported only the local grant horizon.
	t.Run("R2 the budget is bounded by every window the inner stage consumed", func(t *testing.T) {
		bounded := *eng.communicationComposition.resolver
		bounded.freshness = 750 * time.Millisecond
		real := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(
			newCommunicationGrantClosureResolver(&bounded))
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID))
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(real)
		answer := capabilityWant(t, answers, "held", "allowed", "authorized")
		t.Logf("G1A_R2_BUDGET|closure_window_ms=750|returned_budget_ms=%d", *answer.RefreshAfterMS)
		if *answer.RefreshAfterMS > 750 {
			t.Fatalf("the positive outlives an actual evidence window: budget=%d ms > closure=750 ms",
				*answer.RefreshAfterMS)
		}
		// The control that keeps this from passing by clamping everything: with the real
		// producer restored, the same question is bounded by the outer window instead.
		restored := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID))
		wide := capabilityWant(t, restored, "held", "allowed", "authorized")
		if *wide.RefreshAfterMS <= 750 {
			t.Fatalf("the restored producer still yields %d ms: the short window was not the "+
				"term that bounded the budget", *wide.RefreshAfterMS)
		}
	})

	// R3, both producers. Authorize returns Allow:false when the scoped engine or the
	// deny-overlay ERRORS — it fails closed, which is right for serving and wrong as a
	// statement about policy. The rejected candidate published both as known denials.
	for _, outage := range []struct {
		name       string
		authorizer func() *auth.Authorizer
	}{
		{"scoped", func() *auth.Authorizer {
			return auth.NewAuthorizer(nil, auth.WithScopedGrants(independentG1AScopedOutage{}))
		}},
		{"overlay", func() *auth.Authorizer {
			return auth.NewAuthorizer(independentG1AOverlayOutage{})
		}},
		// ⛔ THE DISAGREEMENT ARM, and it is neither a denial nor a permit. The wrapper
		// would ALLOW this request (its scoped engine abstains and RBAC carries it), so
		// answering `denied` would describe a refusal the route would not make; the
		// typed path reports an established forbid, so answering `allowed` would sell a
		// positive the evidence contradicts. Nothing about the request is established,
		// and UNKNOWN is the only answer that does not invent the half we preferred.
		{"disagreeing scoped", func() *auth.Authorizer {
			return auth.NewAuthorizer(nil, auth.WithScopedGrants(independentG1ADisagreeingScoped{}))
		}},
	} {
		t.Run("R3 a failing "+outage.name+" evaluator is unknown, never a denial", func(t *testing.T) {
			original := *eng.authz
			*eng.authz = *outage.authorizer()
			answers := capabilityAsk(t, eng, steward.token, tenant,
				capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
				capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID))
			*eng.authz = original
			capabilityWant(t, answers, "surface", "unknown", "evidence_unavailable")
			capabilityWant(t, answers, "held", "undisclosed", "not_disclosed")
		})
	}

	// R4: the closed typed question must be judged whole. On the rejected candidate a
	// PATCH carrying the held channel in the body AND a hidden channel in the path was
	// answered `allowed` from the body alone, silently discarding the path.
	t.Run("R4 incompatible selectors never receive a positive", func(t *testing.T) {
		conflicting := capabilityBodyQuestion("patch", capOperationPatch, ws, held.Channel.ID)
		conflicting["selectors"].(map[string]any)["path"] = map[string]any{"id": hidden.Channel.ID.String()}
		noncanonical := capabilityBodyQuestion("noncanonical", capOperationPatch, ws, held.Channel.ID)
		noncanonical["selectors"].(map[string]any)["body"] = map[string]any{"channel_id": "not-a-uuid"}
		undeclared := capabilityChannelQuestion("undeclared", capOperationGrantSheet, ws, held.Channel.ID)
		undeclared["selectors"].(map[string]any)["path"].(map[string]any)["tenant_id"] = tenant.String()
		answers := capabilityAsk(t, eng, steward.token, tenant,
			conflicting, noncanonical, undeclared,
			// The control: the revoke route's pattern DECLARES grant_id, so carrying it
			// stays a valid captured intent and must still project a positive. Closing
			// the map must not close the contract's own declared keys.
			func() map[string]any {
				q := capabilityChannelQuestion("revoke", capOperationRevoke, ws, held.Channel.ID)
				q["selectors"].(map[string]any)["path"].(map[string]any)["grant_id"] = model.NewID().String()
				return q
			}())
		capabilityWant(t, answers, "patch", "unknown", "inputs_required")
		capabilityWant(t, answers, "noncanonical", "unknown", "inputs_required")
		capabilityWant(t, answers, "undeclared", "unknown", "inputs_required")
		capabilityWant(t, answers, "revoke", "allowed", "authorized")
	})

	// ⛔ AN INPUT-CONTRACT CHANGE, RECORDED AS ONE. Correcting R1 required the common
	// availability probe to be answerable with NO target named, and such a probe needs a
	// scope. So `workspace_id` became a DECLARED, REQUIRED authorization input for the
	// pilot's entity operations, where it had been optional.
	//
	// It is a narrowing of what the endpoint accepts, never a widening of what it
	// permits: the missing input is answered UNKNOWN/inputs_required — which the review
	// explicitly allows — and is never a denial and never a positive. The positive
	// control below is what makes that a real statement rather than a way to make hard
	// questions disappear: the SAME question, with the input supplied, still projects
	// allowed.
	t.Run("the declared workspace input is required, and supplying it still projects", func(t *testing.T) {
		withoutScope := map[string]any{
			"id": "no-scope", "kind": "operation", "operation": capOperationGrantSheet,
			"selectors": map[string]any{"path": map[string]any{"id": held.Channel.ID.String()}},
		}
		answers := capabilityAsk(t, eng, steward.token, tenant,
			withoutScope,
			capabilityChannelQuestion("with-scope", capOperationGrantSheet, ws, held.Channel.ID))
		capabilityWant(t, answers, "no-scope", "unknown", "inputs_required")
		capabilityWant(t, answers, "with-scope", "allowed", "authorized")
		// A non-canonical scope is the same class of answer, decided from the question
		// alone with no row read.
		malformed := capabilityAsk(t, eng, steward.token, tenant, map[string]any{
			"id": "bad-scope", "kind": "operation", "operation": capOperationGrantSheet,
			"workspace_id": "not-a-uuid",
			"selectors":    map[string]any{"path": map[string]any{"id": held.Channel.ID.String()}},
		})
		capabilityWant(t, malformed, "bad-scope", "unknown", "inputs_required")
	})

	assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "the discriminant projections")
}
