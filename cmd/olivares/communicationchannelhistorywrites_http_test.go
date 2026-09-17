// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	// channelHistoryWritesGenerations is the history the main journey seeds for
	// ONE subject: past the 256 rows the previous writer locked, so every
	// mutation below runs on a Channel that could not be mutated at all before.
	channelHistoryWritesGenerations = 300
	// channelHistoryWritesDeepGenerations is the second, deeper measurement.
	// Enumerating and mutating past a thousand generations is what shows the
	// bound is gone rather than raised.
	channelHistoryWritesDeepGenerations = 1200
	// channelHistoryWritesCrowd is how many OTHER subjects hold a CURRENT admin
	// grant on the same Channel while the journey runs. Unrelated subjects are
	// the other way a ceiling can hide: a writer bounded by "every active grant
	// of this Channel" would answer 503 here for a reason that has nothing to do
	// with the caller or with the row being changed.
	channelHistoryWritesCrowd = 400
)

// seedChannelGrantCrowd gives `count` synthetic subjects a current, non-expiring
// ADMIN grant on one Channel, through the typed store API. They are legal rows
// that no part of the journey addresses: their only job is to be there.
func seedChannelGrantCrowd(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	workspace model.ID,
	channelID model.ID,
	actor model.ID,
	count int,
) []model.ID {
	t.Helper()
	seeded := make([]model.ID, 0, count)
	if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel_grant")
		if err != nil {
			return err
		}
		for index := 0; index < count; index++ {
			id := model.NewID()
			if _, err := repo.CreateWithID(context.Background(), id, model.Record{
				"workspace_id": workspace.String(), "channel_id": channelID.String(),
				"subject_kind": "user", "subject_ref": model.NewID().String(),
				"generation": int64(1),
				"can_read":   true, "can_write": false, "can_admin": true,
				"state":           "active",
				"granted_by_kind": "user", "granted_by_ref": actor.String(),
			}); err != nil {
				return fmt.Errorf("seed crowd grant %d: %w", index, err)
			}
			seeded = append(seeded, id)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d crowd grants: %v", count, err)
	}
	return seeded
}

// channelGrantHistoryCensus counts the ChannelGrant rows of one tenant with
// pagination, because the shared effect census stops at a thousand rows and
// these journeys deliberately hold more. It also counts the communication
// success audits, so "nothing durable happened" and "exactly one act happened"
// are both measurable.
type channelGrantHistoryCensus struct {
	grants   int
	channels int
	audits   int
}

func censusChannelGrantHistory(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
) channelGrantHistoryCensus {
	t.Helper()
	var out channelGrantHistoryCensus
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, counter := range []struct {
			kind model.Kind
			set  *int
		}{
			{kind: "sessions.channel_grant", set: &out.grants},
			{kind: "sessions.channel", set: &out.channels},
		} {
			repo, err := sc.Ext(counter.kind)
			if err != nil {
				return err
			}
			query := model.Query{Limit: 500}
			for {
				rows, page, err := repo.List(context.Background(), query)
				if err != nil {
					return err
				}
				*counter.set += len(rows)
				if !page.HasMore {
					break
				}
				if page.Cursor == "" {
					return fmt.Errorf("%s census lost its cursor", counter.kind)
				}
				query.Cursor = page.Cursor
			}
		}
		return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			if strings.HasPrefix(event.Action, "sessions.communication.channel.") {
				out.audits++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("channel grant history census: %v", err)
	}
	return out
}

// walkChannelGrantSheet enumerates a Channel's whole grant history through the
// administrative read model, page by page, refusing repetition, disorder and a
// walk that does not terminate.
func walkChannelGrantSheet(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
	token string,
	channel model.ID,
	query string,
) []sessions.ChannelGrant {
	t.Helper()
	var out []sessions.ChannelGrant
	seen := map[string]bool{}
	continuation := ""
	for page := 0; page < 400; page++ {
		current := query
		if continuation != "" {
			current += "&continuation=" + continuation
		}
		got := estate.sheetPage(t, token, channel, current)
		for _, item := range got.Items {
			id := item.Grant.ID.String()
			if seen[id] {
				t.Fatalf("grant %s repeated across pages", id)
			}
			seen[id] = true
			if len(out) > 0 && out[len(out)-1].ID.String() >= id {
				t.Fatalf("grant page order broke at %s", id)
			}
			out = append(out, item.Grant)
		}
		if !got.HasMore {
			if got.Continuation != "" {
				t.Fatalf("terminal page carries a continuation: %+v", got)
			}
			return out
		}
		if got.Continuation == "" {
			t.Fatalf("non-terminal page carries no continuation: %+v", got)
		}
		continuation = got.Continuation
	}
	t.Fatalf("history walk did not terminate for %q", query)
	return nil
}

// assertChannelGrantChain proves a subject's generations are a single legal
// chain: generations 1..N with no gap and no repetition, generation 1 with no
// predecessor, every later generation superseding the exact row before it, and
// AT MOST ONE persisted-active row.
func assertChannelGrantChain(
	t *testing.T,
	grants []sessions.ChannelGrant,
	subject model.ID,
	want int,
) sessions.ChannelGrant {
	t.Helper()
	chain := make([]sessions.ChannelGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.Subject.Ref == subject.String() {
			chain = append(chain, grant)
		}
	}
	if len(chain) != want {
		t.Fatalf("subject %s holds %d generations, want %d", subject, len(chain), want)
	}
	sort.Slice(chain, func(i, j int) bool { return chain[i].Generation < chain[j].Generation })
	active := 0
	for index, grant := range chain {
		if grant.Generation != int64(index+1) {
			t.Fatalf("generation %d out of sequence at position %d: %+v", grant.Generation, index, grant)
		}
		if index == 0 {
			if grant.SupersedesID != "" {
				t.Fatalf("generation 1 carries a predecessor: %+v", grant)
			}
		} else if grant.SupersedesID != chain[index-1].ID {
			t.Fatalf("generation %d supersedes %s, want %s",
				grant.Generation, grant.SupersedesID, chain[index-1].ID)
		}
		if grant.State == sessions.ChannelGrantActive {
			active++
		}
	}
	if active > 1 {
		t.Fatalf("subject %s holds %d persisted-active generations", subject, active)
	}
	return chain[len(chain)-1]
}

// TestCommunicationChannelHistoryWritesHTTP drives the corrected administrative
// writer through the production router on a fresh SQLite estate whose Channel
// carries far more grant generations than the retired 256-row lock allowed.
func TestCommunicationChannelHistoryWritesHTTP(t *testing.T) {
	exerciseChannelHistoryWritesHTTP(t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationChannelHistoryWritesHTTPPostgres runs the SAME journey
// against a real, isolated split-owner PostgreSQL 16 database.
func TestCommunicationChannelHistoryWritesHTTPPostgres(t *testing.T) {
	exerciseChannelHistoryWritesHTTP(t, communicationHTTPTestPostgresStore(t))
}

func exerciseChannelHistoryWritesHTTP(t *testing.T, storeEstate communicationHTTPTestStore) {
	estate := bootChannelAdministrationHTTPEstate(t, storeEstate)
	eng, tenant, workspace, owner := estate.eng, estate.tenant, estate.workspace, estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "history-writes", []map[string]any{ownerAll})
	// A SECOND Channel with its own history, so a statement that leaves its
	// Channel is visible as a wrong answer rather than as a slow one.
	neighbour := estate.createChannel(t, workspace, "history-writes-neighbour", []map[string]any{ownerAll})

	subject, seeded := seedChannelGrantHistory(t, eng, tenant, workspace,
		channel.Channel.ID, owner.id, channelHistoryWritesGenerations)
	neighbourSubject, neighbourSeeded := seedChannelGrantHistory(t, eng, tenant, workspace,
		neighbour.Channel.ID, owner.id, 5)
	crowd := seedChannelGrantCrowd(t, eng, tenant, workspace, channel.Channel.ID, owner.id,
		channelHistoryWritesCrowd)
	if len(seeded) != channelHistoryWritesGenerations || len(crowd) != channelHistoryWritesCrowd {
		t.Fatalf("seeded %d generations and %d crowd grants", len(seeded), len(crowd))
	}

	t.Run("a configuration change no longer depends on the grant history", func(t *testing.T) {
		before := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=1")
		census := censusChannelGrantHistory(t, eng, tenant)
		patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
			owner.token, tenant, map[string]any{
				"channel_id":  channel.Channel.ID.String(),
				"description": "administered past the retired bound",
			}, map[string]string{"If-Match": before.ETag})
		if patched.status != http.StatusOK {
			t.Fatalf("PATCH on a Channel with %d generations and %d other admins = %d: %s",
				channelHistoryWritesGenerations, channelHistoryWritesCrowd, patched.status, patched.raw)
		}
		result := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, patched)
		if result.Channel.Version != before.Channel.Version+1 {
			t.Fatalf("PATCH version %d, want %d", result.Channel.Version, before.Channel.Version+1)
		}
		// A configuration change is NOT an ACL change, and it preserves every
		// option the Channel was configured with.
		if result.Channel.ACLRevision != before.Channel.ACLRevision ||
			result.Channel.ProtectionGeneration != before.Channel.ProtectionGeneration {
			t.Fatalf("PATCH moved an ACL or protection generation: %+v", result.Channel)
		}
		if result.Channel.Description != "administered past the retired bound" ||
			result.Channel.Slug != before.Channel.Slug || result.Channel.Name != before.Channel.Name ||
			result.Channel.Kind != before.Channel.Kind || result.Channel.State != before.Channel.State ||
			result.Channel.Sensitivity != before.Channel.Sensitivity ||
			result.Channel.ContentProtection != before.Channel.ContentProtection ||
			result.Channel.DefaultAckPolicy != before.Channel.DefaultAckPolicy ||
			result.Channel.DefaultAckTimeoutMS != before.Channel.DefaultAckTimeoutMS ||
			result.Channel.DefaultWake != before.Channel.DefaultWake ||
			result.Channel.RetentionPolicyRef != before.Channel.RetentionPolicyRef ||
			result.Channel.MaxFanout != before.Channel.MaxFanout ||
			result.Channel.MaxAutomationDepth != before.Channel.MaxAutomationDepth ||
			result.Channel.RouteRevision != before.Channel.RouteRevision ||
			result.Channel.SubscriptionRevision != before.Channel.SubscriptionRevision {
			t.Fatalf("PATCH changed a configured option it was not asked to change:\nbefore=%+v\nafter=%+v",
				before.Channel, result.Channel)
		}
		after := censusChannelGrantHistory(t, eng, tenant)
		if after.grants != census.grants || after.channels != census.channels ||
			after.audits != census.audits+1 {
			t.Fatalf("PATCH durable census: before=%+v after=%+v", census, after)
		}
	})

	var successor sessions.ChannelGrant
	t.Run("revoke the exact deep generation and issue its successor", func(t *testing.T) {
		history := walkChannelGrantSheet(t, estate, owner.token, channel.Channel.ID,
			wsQuery+"&state=all&limit=200")
		head := assertChannelGrantChain(t, history, subject, channelHistoryWritesGenerations)
		if head.State != sessions.ChannelGrantActive ||
			head.Generation != channelHistoryWritesGenerations {
			t.Fatalf("head of the seeded chain = %+v", head)
		}
		sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=1")

		// A second ACTIVE generation is still refused while that head lives: the
		// explicit revoke -> grant flow is unchanged by this correction.
		refused := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
			"subject":  channelAdministrationSubject("user", subject),
			"can_read": true, "can_write": false, "can_admin": false,
		}, sheet.ETag)
		if refused.status != http.StatusConflict {
			t.Fatalf("grant over the persisted-active head = %d: %s", refused.status, refused.raw)
		}

		revoked := estate.revoke(t, owner.token, channel.Channel.ID, head.ID, sheet.ETag)
		if revoked.status != http.StatusOK {
			t.Fatalf("revoke generation %d = %d: %s", head.Generation, revoked.status, revoked.raw)
		}
		afterRevoke := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, revoked)
		if afterRevoke.Grant == nil || afterRevoke.Grant.ID != head.ID ||
			afterRevoke.Grant.State != sessions.ChannelGrantRevoked ||
			afterRevoke.Grant.RevokedBy == nil ||
			afterRevoke.Channel.ACLRevision != sheet.Channel.ACLRevision+1 {
			t.Fatalf("revoke result = %+v", afterRevoke)
		}

		granted := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
			"subject":  channelAdministrationSubject("user", subject),
			"can_read": true, "can_write": false, "can_admin": false,
		}, afterRevoke.ETag)
		if granted.status != http.StatusOK {
			t.Fatalf("grant the successor past %d generations = %d: %s",
				channelHistoryWritesGenerations, granted.status, granted.raw)
		}
		result := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, granted)
		if result.Grant == nil ||
			result.Grant.Generation != int64(channelHistoryWritesGenerations)+1 ||
			result.Grant.SupersedesID != head.ID ||
			result.Grant.State != sessions.ChannelGrantActive {
			t.Fatalf("successor generation = %+v, want %d superseding %s",
				result.Grant, channelHistoryWritesGenerations+1, head.ID)
		}
		successor = *result.Grant

		// Every previously stored row survives, the sequence is unbroken, and the
		// subject holds exactly one active generation.
		after := walkChannelGrantSheet(t, estate, owner.token, channel.Channel.ID,
			wsQuery+"&state=all&limit=200")
		present := map[string]bool{}
		for _, grant := range after {
			present[grant.ID.String()] = true
		}
		for _, id := range seeded {
			if !present[id.String()] {
				t.Fatalf("seeded generation %s disappeared", id)
			}
		}
		for _, id := range crowd {
			if !present[id.String()] {
				t.Fatalf("unrelated crowd grant %s disappeared", id)
			}
		}
		newest := assertChannelGrantChain(t, after, subject, channelHistoryWritesGenerations+1)
		if newest.ID != successor.ID || newest.State != sessions.ChannelGrantActive {
			t.Fatalf("chain head after the successor = %+v", newest)
		}
	})

	t.Run("revoking an old generation never retires its successor", func(t *testing.T) {
		predecessor := successor.SupersedesID
		sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=1")
		census := censusChannelGrantHistory(t, eng, tenant)
		for name, target := range map[string]model.ID{
			"the generation that was already revoked": predecessor,
			"the first generation of the chain":       seeded[0],
			"a row that does not exist":               model.NewID(),
			"a row of the neighbouring Channel":       neighbourSeeded[len(neighbourSeeded)-1],
		} {
			response := estate.revoke(t, owner.token, channel.Channel.ID, target, sheet.ETag)
			if response.status != http.StatusConflict {
				t.Fatalf("revoking %s = %d: %s", name, response.status, response.raw)
			}
		}
		if after := censusChannelGrantHistory(t, eng, tenant); after != census {
			t.Fatalf("refused revokes changed durable state: before=%+v after=%+v", census, after)
		}
		current := estate.sheetPage(t, owner.token, channel.Channel.ID,
			wsQuery+"&state=active&subject_kind=user&subject_ref="+subject.String())
		if len(current.Items) != 1 || current.Items[0].Grant.ID != successor.ID {
			t.Fatalf("successor after the refused revokes = %+v", current.Items)
		}
		// And the neighbouring Channel's own head is untouched: addressing its row
		// from here neither revoked it nor disturbed its chain.
		neighbourHistory := walkChannelGrantSheet(t, estate, owner.token, neighbour.Channel.ID,
			wsQuery+"&state=all&limit=200")
		neighbourHead := assertChannelGrantChain(t, neighbourHistory, neighbourSubject, 5)
		if neighbourHead.State != sessions.ChannelGrantActive {
			t.Fatalf("the neighbouring Channel's head changed state: %+v", neighbourHead)
		}
	})

	t.Run("cross-tenant and cross-workspace mutations reach nothing", func(t *testing.T) {
		census := censusChannelGrantHistory(t, eng, tenant)
		sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=1")
		// The stranger tenant addresses this Channel with its own credential.
		for name, response := range map[string]communicationHTTPTestResponse{
			"stranger revoke": communicationHTTPTestRequest(t, eng, http.MethodPost,
				"/v1/m/sessions/channels/"+channel.Channel.ID.String()+"/grants/"+successor.ID.String()+"/revoke",
				estate.stranger.token, estate.other, nil, map[string]string{"If-Match": sheet.ETag}),
			"stranger grant": communicationHTTPTestRequest(t, eng, http.MethodPost,
				"/v1/m/sessions/channels/"+channel.Channel.ID.String()+"/grants",
				estate.stranger.token, estate.other, map[string]any{
					"subject":  channelAdministrationSubject("user", estate.stranger.id),
					"can_read": true, "can_write": false, "can_admin": true,
				}, map[string]string{"If-Match": sheet.ETag}),
			"stranger patch": communicationHTTPTestRequest(t, eng, http.MethodPatch,
				"/v1/m/sessions/channels", estate.stranger.token, estate.other, map[string]any{
					"channel_id": channel.Channel.ID.String(), "description": "crossed",
				}, map[string]string{"If-Match": sheet.ETag}),
		} {
			if response.status == http.StatusOK {
				t.Fatalf("%s succeeded across the tenant boundary: %s", name, response.raw)
			}
		}
		if after := censusChannelGrantHistory(t, eng, tenant); after != census {
			t.Fatalf("cross-tenant attempts changed durable state: before=%+v after=%+v", census, after)
		}
	})

	t.Run("concurrent mutations on one ETag leave exactly one act", func(t *testing.T) {
		exerciseChannelHistoryWriteRaces(t, estate, channel.Channel.ID, subject)
	})

	t.Run("authority is re-established at the refreshed instant", func(t *testing.T) {
		exerciseChannelHistoryWriteAuthorityLoss(t, estate)
	})

	t.Run("a failed mutation leaves no partial act", func(t *testing.T) {
		exerciseChannelHistoryWriteRollback(t, estate, channel.Channel.ID)
	})
}

// exerciseChannelHistoryWriteRaces runs two administrative mutations against the
// SAME Channel ETag at the same time. The Channel row lock and the version
// precondition decide it: exactly one commits, the loser changes nothing, and
// the subject never ends with two active generations.
func exerciseChannelHistoryWriteRaces(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
	channel model.ID,
	subject model.ID,
) {
	eng, tenant := estate.eng, estate.tenant
	wsQuery := "workspace_id=" + estate.workspace.String()
	owner := estate.owner

	race := func(name string, left, right func() communicationHTTPTestResponse) {
		t.Helper()
		before := censusChannelGrantHistory(t, eng, tenant)
		var results [2]communicationHTTPTestResponse
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); results[0] = left() }()
		go func() { defer wait.Done(); results[1] = right() }()
		wait.Wait()
		accepted := 0
		for _, response := range results {
			switch response.status {
			case http.StatusOK:
				accepted++
			case http.StatusConflict:
			default:
				t.Fatalf("%s produced %d: %s", name, response.status, response.raw)
			}
		}
		if accepted != 1 {
			t.Fatalf("%s accepted %d of two mutations sharing one ETag: %s | %s",
				name, accepted, results[0].raw, results[1].raw)
		}
		after := censusChannelGrantHistory(t, eng, tenant)
		if after.audits != before.audits+1 {
			t.Fatalf("%s appended %d acts, want exactly one", name, after.audits-before.audits)
		}
	}

	// Two grants for the SAME subject on one ETag. Only one may become the next
	// generation; the other must not create a second active row.
	fresh := estate.sheetPage(t, owner.token, channel, wsQuery+"&state=active&limit=1")
	target := model.NewID()
	race("two grants for one subject", func() communicationHTTPTestResponse {
		return estate.grant(t, owner.token, channel, map[string]any{
			"subject":  channelAdministrationSubject("user", target),
			"can_read": true, "can_write": false, "can_admin": false,
		}, fresh.ETag)
	}, func() communicationHTTPTestResponse {
		return estate.grant(t, owner.token, channel, map[string]any{
			"subject":  channelAdministrationSubject("user", target),
			"can_read": false, "can_write": true, "can_admin": false,
		}, fresh.ETag)
	})
	settled := estate.sheetPage(t, owner.token, channel,
		wsQuery+"&state=all&limit=50&subject_kind=user&subject_ref="+target.String())
	if len(settled.Items) != 1 || settled.Items[0].Grant.Generation != 1 ||
		settled.Items[0].Grant.State != sessions.ChannelGrantActive {
		t.Fatalf("the raced subject holds %+v", settled.Items)
	}

	// A grant and a revoke racing on one ETag.
	current := estate.sheetPage(t, owner.token, channel,
		wsQuery+"&state=active&subject_kind=user&subject_ref="+subject.String())
	if len(current.Items) != 1 {
		t.Fatalf("the deep subject holds %+v", current.Items)
	}
	head := current.Items[0].Grant
	other := model.NewID()
	race("a grant against a revoke", func() communicationHTTPTestResponse {
		return estate.revoke(t, owner.token, channel, head.ID, current.ETag)
	}, func() communicationHTTPTestResponse {
		return estate.grant(t, owner.token, channel, map[string]any{
			"subject":  channelAdministrationSubject("user", other),
			"can_read": true, "can_write": false, "can_admin": false,
		}, current.ETag)
	})
	// Whichever won, the deep subject still has at most one active generation and
	// the chain is still a chain.
	history := walkChannelGrantSheet(t, estate, owner.token, channel,
		wsQuery+"&state=all&limit=200&subject_kind=user&subject_ref="+subject.String())
	assertChannelGrantChain(t, history, subject, channelHistoryWritesGenerations+1)
}

// exerciseChannelHistoryWriteAuthorityLoss proves the mutation re-establishes
// the caller's admin closure at the refreshed database time: a path that expired
// while nothing else changed, and a path revoked after the caller last read the
// Channel, both refuse the mutation and leave nothing behind.
func exerciseChannelHistoryWriteAuthorityLoss(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
) {
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner, steward := estate.owner, estate.steward
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "writer-authority-loss", []map[string]any{ownerAll})
	seedChannelGrantHistory(t, eng, tenant, workspace, channel.Channel.ID, owner.id, 260)

	expiresAt := time.Now().UTC().Add(5 * time.Second)
	granted := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", steward.id),
		"can_read": false, "can_write": false, "can_admin": true,
		"expires_at": expiresAt.Format(time.RFC3339Nano),
	}, channel.ETag)
	if granted.status != http.StatusOK {
		t.Fatalf("grant the expiring admin path = %d: %s", granted.status, granted.raw)
	}
	expiring := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, granted)

	// While it is valid, the steward administers a Channel with 260 generations.
	sheet := estate.sheetPage(t, steward.token, channel.Channel.ID, wsQuery+"&state=active&limit=1")
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		steward.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "while the path is valid",
		}, map[string]string{"If-Match": sheet.ETag})
	if patched.status != http.StatusOK {
		t.Fatalf("PATCH with a valid expiring admin path = %d: %s", patched.status, patched.raw)
	}
	valid := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, patched)

	// Cross the horizon on the engine clock and reuse the ETag the caller already
	// holds: the ETag is a Channel precondition, never a permit.
	time.Sleep(time.Until(expiresAt) + 1500*time.Millisecond)
	census := censusChannelGrantHistory(t, eng, tenant)
	expired := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		steward.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "after the horizon",
		}, map[string]string{"If-Match": valid.ETag})
	if expired.status != http.StatusForbidden {
		t.Fatalf("PATCH after the only admin path expired = %d: %s", expired.status, expired.raw)
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after != census {
		t.Fatalf("the refused PATCH changed durable state: before=%+v after=%+v", census, after)
	}

	// Now revoke that path outright and repeat with a freshly-read ETag.
	ownerSheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=50")
	dropped := estate.revoke(t, owner.token, channel.Channel.ID, expiring.Grant.ID, ownerSheet.ETag)
	if dropped.status != http.StatusOK {
		t.Fatalf("revoke the steward path = %d: %s", dropped.status, dropped.raw)
	}
	afterRevoke := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, dropped)
	census = censusChannelGrantHistory(t, eng, tenant)
	revokedPath := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		steward.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "after the revocation",
		}, map[string]string{"If-Match": afterRevoke.ETag})
	if revokedPath.status != http.StatusForbidden {
		t.Fatalf("PATCH after the admin path was revoked = %d: %s", revokedPath.status, revokedPath.raw)
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after != census {
		t.Fatalf("the refused PATCH changed durable state: before=%+v after=%+v", census, after)
	}
}

// exerciseChannelHistoryWriteRollback interrupts administrative mutations at
// many different points of their transaction — from before the first statement
// to past the audit append — by giving the request a deadline that expires
// while it runs. Whatever the interruption point, the outcome must be all or
// nothing: the count of durable rows and of appended acts moves by exactly what
// a success moves it by, or not at all.
func exerciseChannelHistoryWriteRollback(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
	channel model.ID,
) {
	eng, tenant := estate.eng, estate.tenant
	wsQuery := "workspace_id=" + estate.workspace.String()
	owner := estate.owner

	// A cancelled context never reaches an effect at all.
	before := censusChannelGrantHistory(t, eng, tenant)
	sheet := estate.sheetPage(t, owner.token, channel, wsQuery+"&state=active&limit=1")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	response := communicationHTTPTestRequestWithContext(t, cancelled, eng, http.MethodPatch,
		"/v1/m/sessions/channels", owner.token, tenant, map[string]any{
			"channel_id": channel.String(), "description": "cancelled before it started",
		}, map[string]string{"If-Match": sheet.ETag})
	if response.status == http.StatusOK {
		t.Fatalf("a cancelled request committed: %s", response.raw)
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after != before {
		t.Fatalf("a cancelled PATCH changed durable state: before=%+v after=%+v", before, after)
	}

	// Interrupted at increasing depths. Each attempt grants a NEW subject, so a
	// commit is one row and one act and a rollback is neither; there is no
	// outcome in between for the census to show.
	interrupted, committed := 0, 0
	for _, budget := range []time.Duration{
		200 * time.Microsecond, 500 * time.Microsecond, time.Millisecond,
		2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
		20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond,
		160 * time.Millisecond, 320 * time.Millisecond,
	} {
		census := censusChannelGrantHistory(t, eng, tenant)
		current := estate.sheetPage(t, owner.token, channel, wsQuery+"&state=active&limit=1")
		deadline, stop := context.WithTimeout(context.Background(), budget)
		attempt := communicationHTTPTestRequestWithContext(t, deadline, eng, http.MethodPost,
			"/v1/m/sessions/channels/"+channel.String()+"/grants", owner.token, tenant,
			map[string]any{
				"subject":  channelAdministrationSubject("user", model.NewID()),
				"can_read": true, "can_write": false, "can_admin": false,
			}, map[string]string{"If-Match": current.ETag})
		stop()
		after := censusChannelGrantHistory(t, eng, tenant)
		switch attempt.status {
		case http.StatusOK:
			committed++
			if after.grants != census.grants+1 || after.audits != census.audits+1 {
				t.Fatalf("a committed grant at budget %s moved the census by %+v -> %+v",
					budget, census, after)
			}
		default:
			interrupted++
			if after != census {
				t.Fatalf("an interrupted grant at budget %s left a partial act: %+v -> %+v (status %d: %s)",
					budget, census, after, attempt.status, attempt.raw)
			}
		}
	}
	t.Logf("K3_WRITER_ROLLBACK|interrupted=%d|committed=%d", interrupted, committed)
	if interrupted == 0 {
		t.Fatal("no attempt was interrupted: the rollback probe measured nothing")
	}
}

// communicationHTTPTestRequestWithContext is communicationHTTPTestRequest with a
// caller-owned context, so a request can be cancelled or given a deadline that
// expires inside the transaction. It is a separate function rather than a change
// to the shared helper: every existing journey keeps the two-minute budget it
// was written with.
func communicationHTTPTestRequestWithContext(
	t *testing.T,
	ctx context.Context,
	eng *engine,
	method string,
	path string,
	token string,
	tenant model.TenantID,
	body any,
	headers map[string]string,
) communicationHTTPTestResponse {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:43210"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if !tenant.IsZero() {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	eng.api.Handler().ServeHTTP(recorder, req)
	return communicationHTTPTestResponse{
		status: recorder.Code, header: recorder.Header().Clone(), raw: recorder.Body.Bytes(),
	}
}

// TestCommunicationChannelDeepHistoryWritesHTTP repeats the essential writer
// operations on a Channel carrying more than a THOUSAND generations of one
// subject, on a real SQLite estate. Three hundred already proves the retired
// bound is gone; a thousand proves it was removed rather than raised.
func TestCommunicationChannelDeepHistoryWritesHTTP(t *testing.T) {
	exerciseChannelDeepHistoryWritesHTTP(t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationChannelDeepHistoryWritesHTTPPostgres runs the same deep
// measurement against a real isolated split-owner PostgreSQL 16 database.
func TestCommunicationChannelDeepHistoryWritesHTTPPostgres(t *testing.T) {
	exerciseChannelDeepHistoryWritesHTTP(t, communicationHTTPTestPostgresStore(t))
}

func exerciseChannelDeepHistoryWritesHTTP(t *testing.T, storeEstate communicationHTTPTestStore) {
	estate := bootChannelAdministrationHTTPEstate(t, storeEstate)
	eng, tenant, workspace, owner := estate.eng, estate.tenant, estate.workspace, estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "deep-history", []map[string]any{ownerAll})
	subject, seeded := seedChannelGrantHistory(t, eng, tenant, workspace,
		channel.Channel.ID, owner.id, channelHistoryWritesDeepGenerations)
	if len(seeded) != channelHistoryWritesDeepGenerations {
		t.Fatalf("seeded %d generations", len(seeded))
	}
	before := censusChannelGrantHistory(t, eng, tenant)

	// The whole history enumerates, and it is one legal chain.
	history := walkChannelGrantSheet(t, estate, owner.token, channel.Channel.ID,
		wsQuery+"&state=all&limit=200")
	if len(history) != channelHistoryWritesDeepGenerations+1 {
		t.Fatalf("deep history enumerated %d rows, want %d",
			len(history), channelHistoryWritesDeepGenerations+1)
	}
	head := assertChannelGrantChain(t, history, subject, channelHistoryWritesDeepGenerations)

	// PATCH, revoke and grant, in that order, on that Channel.
	sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=1")
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		owner.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "past a thousand generations",
		}, map[string]string{"If-Match": sheet.ETag})
	if patched.status != http.StatusOK {
		t.Fatalf("PATCH past %d generations = %d: %s",
			channelHistoryWritesDeepGenerations, patched.status, patched.raw)
	}
	afterPatch := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, patched)
	revoked := estate.revoke(t, owner.token, channel.Channel.ID, head.ID, afterPatch.ETag)
	if revoked.status != http.StatusOK {
		t.Fatalf("revoke generation %d = %d: %s", head.Generation, revoked.status, revoked.raw)
	}
	afterRevoke := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, revoked)
	granted := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", subject),
		"can_read": true, "can_write": false, "can_admin": false,
	}, afterRevoke.ETag)
	if granted.status != http.StatusOK {
		t.Fatalf("grant the successor past %d generations = %d: %s",
			channelHistoryWritesDeepGenerations, granted.status, granted.raw)
	}
	successor := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, granted)
	if successor.Grant == nil ||
		successor.Grant.Generation != int64(channelHistoryWritesDeepGenerations)+1 ||
		successor.Grant.SupersedesID != head.ID {
		t.Fatalf("deep successor = %+v, want generation %d superseding %s",
			successor.Grant, channelHistoryWritesDeepGenerations+1, head.ID)
	}

	// Every row is retained and the chain is still exactly one chain.
	after := walkChannelGrantSheet(t, estate, owner.token, channel.Channel.ID,
		wsQuery+"&state=all&limit=200")
	if len(after) != len(history)+1 {
		t.Fatalf("deep history holds %d rows after one grant, want %d", len(after), len(history)+1)
	}
	present := map[string]bool{}
	for _, grant := range after {
		present[grant.ID.String()] = true
	}
	for _, id := range seeded {
		if !present[id.String()] {
			t.Fatalf("seeded generation %s disappeared from the deep history", id)
		}
	}
	newest := assertChannelGrantChain(t, after, subject, channelHistoryWritesDeepGenerations+1)
	if newest.ID != successor.Grant.ID {
		t.Fatalf("deep chain head = %+v, want the successor %s", newest, successor.Grant.ID)
	}
	census := censusChannelGrantHistory(t, eng, tenant)
	if census.grants != before.grants+1 || census.audits != before.audits+3 {
		t.Fatalf("deep journey census: before=%+v after=%+v (want one row and three acts)", before, census)
	}
}

// relaxChannelGrantRowInvariants removes, from a DISPOSABLE SQLite estate and
// over its own connection, the three engine-level guards that make an ambiguous
// ChannelGrant history impossible: the unique generation index, the unique
// predecessor index and the insert guard that serialises the supersedes chain.
//
// It reproduces an estate whose row invariants are NOT enforced — one restored
// from a source that never had them, or upgraded across a schema that lost
// them. That is the only state in which the writer's two "cannot establish a
// unique legal predecessor" refusals are reachable, and reaching them is the
// point: a writer that selects the RELEVANT rows instead of the whole history no
// longer stumbles over an ambiguity by accident, so it has to ask, and the
// answer has to be a refusal rather than a row chosen by luck.
//
// It refuses to run against anything but SQLite, and every caller owns its
// estate for the duration of one test.
func relaxChannelGrantRowInvariants(t *testing.T, storeEstate communicationHTTPTestStore) {
	t.Helper()
	if storeEstate.engine != "sqlite" {
		t.Fatalf("the degraded-invariant fixture is SQLite-only, got %q", storeEstate.engine)
	}
	raw, err := sql.Open("sqlite", filepath.Join(storeEstate.dataDir, "olivares.db"))
	if err != nil {
		t.Fatalf("open the disposable estate: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	for _, statement := range []string{
		"DROP INDEX IF EXISTS sessions_channel_grant_uniq",
		"DROP INDEX IF EXISTS sessions_channel_grant_predecessor_uniq",
		"DROP TRIGGER IF EXISTS sessions_channel_grant_guard_ins",
		"DROP TRIGGER IF EXISTS sessions_channel_grant_guard_upd",
	} {
		if _, err := raw.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// insertChannelGrantRow writes ONE ChannelGrant row through the typed store API.
// It exists for the degraded-estate fixtures, where the row being written is
// deliberately one the product itself would never create.
func insertChannelGrantRow(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	record model.Record,
) model.ID {
	t.Helper()
	id := model.NewID()
	if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel_grant")
		if err != nil {
			return err
		}
		_, err = repo.CreateWithID(context.Background(), id, record)
		return err
	}); err != nil {
		t.Fatalf("insert ChannelGrant row: %v", err)
	}
	return id
}

// TestCommunicationChannelWriterRefusesAmbiguousAdminClosureHTTP proves the
// first invariant refusal: when ONE subject of the caller's own closure holds
// two current admin generations on a Channel, "the" current grant of that
// subject is whichever row the engine happened to return first, so the mutation
// refuses instead of choosing. The refusal is about the CALLER's authority, and
// the test proves that too: another administrator, whose closure is
// unambiguous, is not blocked by it.
func TestCommunicationChannelWriterRefusesAmbiguousAdminClosureHTTP(t *testing.T) {
	storeEstate := communicationHTTPTestSQLiteStore(t)
	estate := bootChannelAdministrationHTTPEstate(t, storeEstate)
	eng, tenant, workspace, owner := estate.eng, estate.tenant, estate.workspace, estate.owner
	steward := estate.steward
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)
	stewardAdmin := channelAdministrationGrant(channelAdministrationSubject("user", steward.id), false, false, true)

	channel := estate.createChannel(t, workspace, "ambiguous-closure",
		[]map[string]any{ownerAll, stewardAdmin})
	first := findChannelAdministrationGrant(t, channel.Grants, steward.id)
	sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=50")

	// With the invariants in force the steward administers normally.
	warmup := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		steward.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "before the ambiguity",
		}, map[string]string{"If-Match": sheet.ETag})
	if warmup.status != http.StatusOK {
		t.Fatalf("PATCH with an unambiguous closure = %d: %s", warmup.status, warmup.raw)
	}
	current := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, warmup)

	relaxChannelGrantRowInvariants(t, storeEstate)
	second := insertChannelGrantRow(t, eng, tenant, model.Record{
		"workspace_id": workspace.String(), "channel_id": channel.Channel.ID.String(),
		"subject_kind": "user", "subject_ref": steward.id.String(),
		"generation": int64(2),
		"can_read":   false, "can_write": false, "can_admin": true,
		"state":           "active",
		"granted_by_kind": "user", "granted_by_ref": owner.id.String(),
		"supersedes_id": first.ID.String(),
	})

	census := censusChannelGrantHistory(t, eng, tenant)
	for name, response := range map[string]communicationHTTPTestResponse{
		"patch": communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
			steward.token, tenant, map[string]any{
				"channel_id": channel.Channel.ID.String(), "description": "on an ambiguous closure",
			}, map[string]string{"If-Match": current.ETag}),
		"revoke": estate.revoke(t, steward.token, channel.Channel.ID, second, current.ETag),
		"grant": estate.grant(t, steward.token, channel.Channel.ID, map[string]any{
			"subject":  channelAdministrationSubject("user", model.NewID()),
			"can_read": true, "can_write": false, "can_admin": false,
		}, current.ETag),
	} {
		if response.status != http.StatusServiceUnavailable ||
			!strings.Contains(string(response.raw), "evidence_unavailable") {
			t.Fatalf("%s with two current admin generations for the caller = %d: %s",
				name, response.status, response.raw)
		}
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after != census {
		t.Fatalf("the refused mutations changed durable state: before=%+v after=%+v", census, after)
	}

	// The owner's own closure is unambiguous, and another subject's broken chain
	// does not reach it: the writer no longer reads a Channel's whole history to
	// decide who may administer it.
	unaffected := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		owner.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "the owner is unaffected",
		}, map[string]string{"If-Match": current.ETag})
	if unaffected.status != http.StatusOK {
		t.Fatalf("PATCH by a caller with an unambiguous closure = %d: %s",
			unaffected.status, unaffected.raw)
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after.grants != census.grants ||
		after.audits != census.audits+1 {
		t.Fatalf("the accepted PATCH census: before=%+v after=%+v", census, after)
	}
}

// TestCommunicationChannelWriterRefusesDuplicateGenerationHTTP proves the second
// invariant refusal: when a subject's HIGHEST generation is not unique there is
// no defensible next number and no defensible supersedes_id, so the grant
// refuses rather than superseding whichever of the two rows came back first.
func TestCommunicationChannelWriterRefusesDuplicateGenerationHTTP(t *testing.T) {
	storeEstate := communicationHTTPTestSQLiteStore(t)
	estate := bootChannelAdministrationHTTPEstate(t, storeEstate)
	eng, tenant, workspace, owner := estate.eng, estate.tenant, estate.workspace, estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "duplicate-generation", []map[string]any{ownerAll})
	// Three generations, the last one revoked, so the "already active" refusal
	// cannot mask the one being measured.
	subject, seeded := seedChannelGrantHistory(t, eng, tenant, workspace,
		channel.Channel.ID, owner.id, 3)
	revokeSeededHead(t, eng, tenant, seeded[len(seeded)-1], owner.id)

	sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=50")
	// With the invariants in force the successor is issued normally, which is
	// what makes the refusal below attributable to the ambiguity alone.
	warmup := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", subject),
		"can_read": true, "can_write": false, "can_admin": false,
	}, sheet.ETag)
	if warmup.status != http.StatusOK {
		t.Fatalf("grant the successor on an unambiguous chain = %d: %s", warmup.status, warmup.raw)
	}
	fourth := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, warmup)
	if fourth.Grant == nil || fourth.Grant.Generation != 4 {
		t.Fatalf("warm-up successor = %+v, want generation 4", fourth.Grant)
	}
	revokeSeededHead(t, eng, tenant, fourth.Grant.ID, owner.id)

	relaxChannelGrantRowInvariants(t, storeEstate)
	duplicate := insertChannelGrantRow(t, eng, tenant, model.Record{
		"workspace_id": workspace.String(), "channel_id": channel.Channel.ID.String(),
		"subject_kind": "user", "subject_ref": subject.String(),
		"generation": int64(4),
		"can_read":   true, "can_write": false, "can_admin": false,
		"state":           "revoked",
		"granted_by_kind": "user", "granted_by_ref": owner.id.String(),
		"revoked_by_kind": "user", "revoked_by_ref": owner.id.String(),
		"supersedes_id": seeded[len(seeded)-1].String(),
	})
	if duplicate.IsZero() {
		t.Fatal("the duplicate generation was not written")
	}

	current := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=50")
	census := censusChannelGrantHistory(t, eng, tenant)
	refused := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", subject),
		"can_read": true, "can_write": false, "can_admin": false,
	}, current.ETag)
	if refused.status != http.StatusServiceUnavailable ||
		!strings.Contains(string(refused.raw), "evidence_unavailable") {
		t.Fatalf("grant over a duplicated highest generation = %d: %s", refused.status, refused.raw)
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after != census {
		t.Fatalf("the refused grant changed durable state: before=%+v after=%+v", census, after)
	}

	// A DIFFERENT subject on the same Channel is unaffected: the ambiguity is the
	// subject's, and it is not read on anyone else's behalf.
	healthy := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", model.NewID()),
		"can_read": true, "can_write": false, "can_admin": false,
	}, current.ETag)
	if healthy.status != http.StatusOK {
		t.Fatalf("grant to an unrelated subject on the same Channel = %d: %s",
			healthy.status, healthy.raw)
	}
	// And a PATCH, which addresses no grant row at all, is not blocked either.
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		owner.token, tenant, map[string]any{
			"channel_id":  channel.Channel.ID.String(),
			"description": "a configuration change reads no grant history",
		}, map[string]string{
			"If-Match": communicationHTTPTestDecode[sessions.ChannelMutationResult](t, healthy).ETag,
		})
	if patched.status != http.StatusOK {
		t.Fatalf("PATCH on a Channel with one broken subject chain = %d: %s",
			patched.status, patched.raw)
	}
}

// revokeSeededHead marks one seeded row revoked through the typed store API, so
// a fixture chain can be extended without going through the product. It is a
// fixture step, never the journey's own revoke.
func revokeSeededHead(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	grantID model.ID,
	actor model.ID,
) {
	t.Helper()
	if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel_grant")
		if err != nil {
			return err
		}
		record, err := repo.Get(context.Background(), grantID)
		if err != nil {
			return err
		}
		record["state"] = "revoked"
		record["revoked_by_kind"] = "user"
		record["revoked_by_ref"] = actor.String()
		_, err = repo.Update(context.Background(), record)
		return err
	}); err != nil {
		t.Fatalf("revoke seeded row %s: %v", grantID, err)
	}
}

// TestCommunicationChannelWriterReChecksTheDirectoryClosureHTTP proves the third
// way an administrator can stop being one between reading a Channel and changing
// it: not the grant being revoked and not its horizon elapsing, but the SUBJECT
// that carried the grant leaving the caller's closure.
//
// The grant row is untouched throughout — it still names the group, and the group
// still holds admin. What changes is the directory, and the mutation re-resolves
// the closure rather than trusting the ETag it handed out.
func TestCommunicationChannelWriterReChecksTheDirectoryClosureHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, tenant, workspace, owner := estate.eng, estate.tenant, estate.workspace, estate.owner
	steward := estate.steward
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "closure-recheck", []map[string]any{ownerAll})
	seedChannelGrantHistory(t, eng, tenant, workspace, channel.Channel.ID, owner.id, 270)

	group := scimGroupWithMember(t, eng, owner.token, tenant, "K3 writer closure stewards", steward.id)
	granted := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  map[string]any{"kind": "user_group", "ref": group.String()},
		"can_read": false, "can_write": false, "can_admin": true,
	}, channel.ETag)
	if granted.status != http.StatusOK {
		t.Fatalf("grant the group admin path = %d: %s", granted.status, granted.raw)
	}
	groupGrant := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, granted)

	// The steward administers through the group, on a Channel past the retired
	// bound, and holds the ETag it was given.
	sheet := estate.sheetPage(t, steward.token, channel.Channel.ID, wsQuery+"&state=active&limit=1")
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		steward.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "administered through the group",
		}, map[string]string{"If-Match": sheet.ETag})
	if patched.status != http.StatusOK {
		t.Fatalf("PATCH through the group admin path = %d: %s", patched.status, patched.raw)
	}
	held := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, patched)

	// The IdP removes the steward from the group. The GRANT is untouched.
	removed := communicationHTTPTestRequest(t, eng, http.MethodPatch,
		"/v1/scim/v2/Groups/"+group.String(), owner.token, tenant, map[string]any{
			"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
			"Operations": []map[string]any{{"op": "remove", "path": "members"}},
		}, map[string]string{"Content-Type": "application/scim+json"})
	if removed.status != http.StatusOK {
		t.Fatalf("remove the group membership = %d: %s", removed.status, removed.raw)
	}

	census := censusChannelGrantHistory(t, eng, tenant)
	for name, response := range map[string]communicationHTTPTestResponse{
		"patch": communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
			steward.token, tenant, map[string]any{
				"channel_id": channel.Channel.ID.String(), "description": "after leaving the group",
			}, map[string]string{"If-Match": held.ETag}),
		"revoke": estate.revoke(t, steward.token, channel.Channel.ID, groupGrant.Grant.ID, held.ETag),
		"grant": estate.grant(t, steward.token, channel.Channel.ID, map[string]any{
			"subject":  channelAdministrationSubject("user", steward.id),
			"can_read": false, "can_write": false, "can_admin": true,
		}, held.ETag),
	} {
		if response.status == http.StatusOK {
			t.Fatalf("%s after the subject left the closure succeeded: %s", name, response.raw)
		}
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after != census {
		t.Fatalf("the refused mutations changed durable state: before=%+v after=%+v", census, after)
	}
	// The grant row itself is exactly as it was: nothing about this refusal is a
	// change to the Channel's access list.
	current := estate.sheetPage(t, owner.token, channel.Channel.ID,
		wsQuery+"&state=active&limit=50&subject_kind=user_group&subject_ref="+group.String())
	if len(current.Items) != 1 || current.Items[0].Grant.ID != groupGrant.Grant.ID ||
		current.Items[0].Grant.State != sessions.ChannelGrantActive ||
		!current.Items[0].Grant.CanAdmin {
		t.Fatalf("the group's admin grant changed: %+v", current.Items)
	}
	// And the OWNER, whose path never went through that group, still administers.
	stillWorks := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		owner.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID.String(), "description": "the owner is unaffected",
		}, map[string]string{"If-Match": current.ETag})
	if stillWorks.status != http.StatusOK {
		t.Fatalf("PATCH by the owner after the group changed = %d: %s",
			stillWorks.status, stillWorks.raw)
	}
}

// TestCommunicationChannelWriterRefusesASecondActiveGenerationHTTP gives the
// grant's "does this subject already hold a persisted-active generation?"
// question its own witness, on the only history where it is not answered by the
// subject's HIGHEST generation as well: one where an active row sits BEHIND a
// revoked head.
//
// On a conforming engine that cannot happen — a successor may only supersede a
// predecessor already in a terminal state — which is exactly why the writer asks
// the question separately instead of inferring the answer from the newest row.
func TestCommunicationChannelWriterRefusesASecondActiveGenerationHTTP(t *testing.T) {
	storeEstate := communicationHTTPTestSQLiteStore(t)
	estate := bootChannelAdministrationHTTPEstate(t, storeEstate)
	eng, tenant, workspace, owner := estate.eng, estate.tenant, estate.workspace, estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "active-behind-the-head", []map[string]any{ownerAll})
	subject, seeded := seedChannelGrantHistory(t, eng, tenant, workspace,
		channel.Channel.ID, owner.id, 3)
	// The seeded head is active; revoke it so the chain's newest row is terminal
	// and cannot be the reason the grant below is refused.
	revokeSeededHead(t, eng, tenant, seeded[len(seeded)-1], owner.id)

	relaxChannelGrantRowInvariants(t, storeEstate)
	reviveSeededGeneration(t, eng, tenant, seeded[0])

	sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=50")
	// The estate is exactly the one this refusal is for: the subject's newest
	// generation is revoked, and an older one is active.
	var newest, revived sessions.ChannelGrant
	for _, item := range sheet.Items {
		if item.Grant.Subject.Ref != subject.String() {
			continue
		}
		if item.Grant.Generation == 3 {
			newest = item.Grant
		}
		if item.Grant.ID == seeded[0] {
			revived = item.Grant
		}
	}
	if newest.State != sessions.ChannelGrantRevoked || revived.State != sessions.ChannelGrantActive {
		t.Fatalf("fixture is not the intended one: newest=%+v revived=%+v", newest, revived)
	}

	census := censusChannelGrantHistory(t, eng, tenant)
	refused := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", subject),
		"can_read": true, "can_write": false, "can_admin": false,
	}, sheet.ETag)
	if refused.status != http.StatusConflict {
		t.Fatalf("grant over an active generation behind the head = %d: %s",
			refused.status, refused.raw)
	}
	if after := censusChannelGrantHistory(t, eng, tenant); after != census {
		t.Fatalf("the refused grant changed durable state: before=%+v after=%+v", census, after)
	}
	// Revoking that older row explicitly is what opens the way, and then the
	// successor is issued from the HIGHEST generation, not from the row that was
	// just revoked.
	revoked := estate.revoke(t, owner.token, channel.Channel.ID, revived.ID, sheet.ETag)
	if revoked.status != http.StatusOK {
		t.Fatalf("revoke the active generation behind the head = %d: %s", revoked.status, revoked.raw)
	}
	afterRevoke := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, revoked)
	granted := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", subject),
		"can_read": true, "can_write": false, "can_admin": false,
	}, afterRevoke.ETag)
	if granted.status != http.StatusOK {
		t.Fatalf("grant after the explicit revoke = %d: %s", granted.status, granted.raw)
	}
	successor := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, granted)
	if successor.Grant == nil || successor.Grant.Generation != 4 ||
		successor.Grant.SupersedesID != newest.ID {
		t.Fatalf("successor = %+v, want generation 4 superseding the head %s",
			successor.Grant, newest.ID)
	}
}

// reviveSeededGeneration puts one seeded row back into `active` through the typed
// store API. It is a fixture step for the degraded estate above, and it is the
// reason that estate also has to drop the UPDATE guard: a conforming engine
// refuses this transition.
func reviveSeededGeneration(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	grantID model.ID,
) {
	t.Helper()
	if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel_grant")
		if err != nil {
			return err
		}
		record, err := repo.Get(context.Background(), grantID)
		if err != nil {
			return err
		}
		record["state"] = "active"
		delete(record, "revoked_by_kind")
		delete(record, "revoked_by_ref")
		_, err = repo.Update(context.Background(), record)
		return err
	}); err != nil {
		t.Fatalf("revive seeded row %s: %v", grantID, err)
	}
}
