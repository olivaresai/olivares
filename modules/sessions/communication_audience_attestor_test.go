// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// attestorTestResolver is a scripted DirectorySnapshotResolver: it answers
// eligibility from a map and expands groups from a scripted roster with the
// causal fact the real adapter would carry. It refuses subscribers exactly like
// the production resolver, so the attestor's own expansion is what gets measured.
type attestorTestResolver struct {
	now      time.Time
	epoch    int64
	eligible map[RecipientRef]bool
	groups   map[string][]attestorTestMember
	calls    int
	err      error
}

type attestorTestMember struct {
	recipient RecipientRef
	fact      store.AuthorizationFactRef
}

func (r *attestorTestResolver) ResolveAudience(
	_ context.Context, scope DirectoryScopeRef, selectors []AudienceSelector,
) (DirectorySnapshot, error) {
	r.calls++
	if r.err != nil {
		return DirectorySnapshot{}, r.err
	}
	roster := map[RecipientRef]RecipientSnapshot{}
	snap := func(recipient RecipientRef) RecipientSnapshot {
		s := RecipientSnapshot{
			Scope: scope, Recipient: recipient, RecipientEpoch: 1, DirectoryEpoch: r.epoch,
			Eligible: r.eligible[recipient],
		}
		roster[recipient] = s
		return s
	}
	var contributions []ResolvedAudienceContribution
	for index, selector := range selectors {
		base := ResolvedAudienceContribution{
			SelectorOrdinal: int64(index + 1), Selector: selector, Required: selector.Required,
			WakePolicy: selector.WakePolicy,
		}
		switch selector.Kind {
		case AudienceUser, AudienceAgent, AudienceSession:
			kind := RecipientUser
			if selector.Kind == AudienceAgent {
				kind = RecipientAgent
			} else if selector.Kind == AudienceSession {
				kind = RecipientSession
			}
			s := snap(RecipientRef{Kind: kind, Ref: selector.Ref})
			if !s.Eligible {
				continue
			}
			c := base
			c.Recipient, c.RouteReasons, c.CausalKind, c.CausalRef = s, []RouteReason{"direct"}, CausalDirect, selector.Ref
			if kind == RecipientSession {
				c.ObservedSessionSID, c.ObservedClaimFence = selector.Ref, 7
			}
			contributions = append(contributions, c)
		case AudienceUserGroup, AudienceAgentGroup:
			for _, member := range r.groups[selector.Ref] {
				s := snap(member.recipient)
				if !s.Eligible {
					continue
				}
				c := base
				c.Recipient, c.CausalRef = s, selector.Ref
				fact := member.fact
				c.CausalFact = &fact
				if selector.Kind == AudienceUserGroup {
					c.CausalKind, c.RouteReasons = CausalUserGroup, []RouteReason{"user_group"}
				} else {
					c.CausalKind, c.RouteReasons = CausalAgentGroup, []RouteReason{"agent_group"}
				}
				contributions = append(contributions, c)
			}
		default:
			return DirectorySnapshot{}, errors.New("scripted resolver cannot expand " + string(selector.Kind))
		}
	}
	recipients := make([]RecipientSnapshot, 0, len(roster))
	for _, s := range roster {
		recipients = append(recipients, s)
	}
	sort.Slice(recipients, func(i, j int) bool {
		if recipients[i].Recipient.Kind != recipients[j].Recipient.Kind {
			return recipients[i].Recipient.Kind < recipients[j].Recipient.Kind
		}
		return recipients[i].Recipient.Ref < recipients[j].Recipient.Ref
	})
	hash, err := CanonicalDirectoryRosterHash(scope, r.epoch, recipients)
	if err != nil {
		return DirectorySnapshot{}, err
	}
	return DirectorySnapshot{
		Scope: scope, Epoch: r.epoch, Selectors: append([]AudienceSelector(nil), selectors...),
		Recipients: recipients, Contributions: contributions, RosterHash: hash,
		ObservedAt: r.now, FreshUntil: r.now.Add(5 * time.Minute),
	}, nil
}

func (r *attestorTestResolver) ResolveRecipient(context.Context, DirectoryScopeRef, RecipientRef) (RecipientSnapshot, error) {
	return RecipientSnapshot{}, errors.New("unused")
}

func (r *attestorTestResolver) ResolvePrincipal(context.Context, DirectoryScopeRef, CommunicationPrincipal) (PrincipalResolution, error) {
	return PrincipalResolution{}, errors.New("unused")
}

type attestorFixture struct {
	communicationSchemaFixture
	scope    DirectoryScopeRef
	channel  Channel
	now      time.Time
	resolver *attestorTestResolver
	attestor PublicationAudienceAttestor
	sender   model.ID
}

func newAttestorFixture(t *testing.T) attestorFixture {
	t.Helper()
	ctx := context.Background()
	base := communicationOpenFixture(t, communicationSchemaBackends(t)[0])
	channelID := model.NewID()
	record, err := communicationCreateWithID(
		ctx, base.m, base.tenant, channelKind, channelID, communicationChannelRecord(base.workspace, "attest"),
	)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel, err := channelFromRecord(record)
	if err != nil {
		t.Fatalf("decode channel: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	resolver := &attestorTestResolver{
		now: now.Add(time.Second), epoch: 3,
		eligible: map[RecipientRef]bool{}, groups: map[string][]attestorTestMember{},
	}
	attestor, err := NewCommunicationPublicationAudienceAttestor(base.m, resolver, func() time.Time { return now })
	if err != nil {
		t.Fatalf("construct attestor: %v", err)
	}
	return attestorFixture{
		communicationSchemaFixture: base,
		scope:                      DirectoryScopeRef{TenantID: base.tenant, WorkspaceID: base.workspace},
		channel:                    channel, now: now, resolver: resolver, attestor: attestor, sender: model.NewID(),
	}
}

func (f attestorFixture) entity(id model.ID) MutableCommunicationEntity {
	return MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
		ID: id, TenantID: f.tenant, WorkspaceID: f.workspace, Version: 1, CreatedAt: f.now.Add(-time.Minute),
	}, UpdatedAt: f.now.Add(-time.Minute)}
}

func (f attestorFixture) subscribe(
	t *testing.T, subject CommunicationSubjectRef, mode ChannelSubscriptionMode, requiredForCritical bool,
) ChannelSubscription {
	t.Helper()
	id := model.NewID()
	subscription := ChannelSubscription{
		MutableCommunicationEntity: f.entity(id), ChannelID: f.channel.ID, Subscriber: subject,
		Generation: 1, Mode: mode, Wake: WakePrimary, RequiredForCritical: requiredForCritical,
		State: SubscriptionActive,
	}
	if mode == SubscriptionNone {
		subscription.Wake, subscription.RequiredForCritical = WakeNone, false
	}
	record, err := channelSubscriptionToRecord(subscription)
	if err != nil {
		t.Fatalf("encode subscription: %v", err)
	}
	if _, err := communicationCreateWithID(context.Background(), f.m, f.tenant, channelSubscriptionKind, id, record); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return subscription
}

func (f attestorFixture) route(t *testing.T, key string, audience ChannelRouteAudienceKind, priority int64) ChannelRouteRule {
	t.Helper()
	id := model.NewID()
	route := ChannelRouteRule{
		MutableCommunicationEntity: f.entity(id), RouteKey: key, Generation: 1, Priority: priority,
		SourceKind: RouteSourceUserMessage, MessageKind: MessageNotice, TargetChannelID: f.channel.ID,
		AudienceKind: audience, AckPolicy: AckPolicyNone, WakePolicy: WakeNone, State: ChannelRouteActive,
	}
	record, err := channelRouteRuleToRecord(route)
	if err != nil {
		t.Fatalf("encode route: %v", err)
	}
	if _, err := communicationCreateWithID(context.Background(), f.m, f.tenant, channelRouteKind, id, record); err != nil {
		t.Fatalf("create route: %v", err)
	}
	return route
}

func (f attestorFixture) request(
	urgency MessageUrgency, mentioned []RecipientRef, selectors ...AudienceSelector,
) PublicationAudienceRequest {
	sort.Slice(mentioned, func(i, j int) bool {
		if mentioned[i].Kind != mentioned[j].Kind {
			return mentioned[i].Kind < mentioned[j].Kind
		}
		return mentioned[i].Ref < mentioned[j].Ref
	})
	return PublicationAudienceRequest{
		Scope: f.scope, ChannelID: f.channel.ID, ChannelACLRevision: f.channel.ACLRevision,
		RouteRevision: f.channel.RouteRevision, SubscriptionRevision: f.channel.SubscriptionRevision,
		MessageKind: MessageNotice, Urgency: urgency,
		Sender:     CommunicationActorRef{Kind: ActorUser, Ref: f.sender.String()},
		SourceKind: RouteSourceUserMessage, ChannelDefaultWake: f.channel.DefaultWake,
		ContentProtection: f.channel.ContentProtection, ProtectionGeneration: f.channel.ProtectionGeneration,
		RequestedAt: f.now, Selectors: selectors, MentionedRecipients: mentioned,
	}
}

func TestPublicationAudienceAttestorKeepsDirectRoutesExact(t *testing.T) {
	t.Parallel()
	f := newAttestorFixture(t)
	recipient := model.NewID()
	f.resolver.eligible[RecipientRef{Kind: RecipientUser, Ref: recipient.String()}] = true
	request := f.request(UrgencyNormal, nil, AudienceSelector{Kind: AudienceUser, Ref: recipient.String(), WakePolicy: WakeNone})

	snapshot, attestation, err := f.attestor.AttestPublicationAudience(context.Background(), request)
	if err != nil {
		t.Fatalf("attest direct: %v", err)
	}
	if err := validateDirectNoticeSnapshot(request, snapshot, attestation,
		RecipientRef{Kind: RecipientUser, Ref: recipient.String()}); err != nil {
		t.Fatalf("direct attestation is not exact: %v\n%+v", err, snapshot)
	}
	if err := validatePublicationAudienceAttestation(request, snapshot, attestation, f.now.Add(2*time.Second)); err != nil {
		t.Fatalf("attestation does not bind request and snapshot: %v", err)
	}
	if attestation.Evidence.EvidenceRef != publicationAudienceEvidenceRef || snapshot.Epoch != 3 {
		t.Fatalf("attestation = %+v snapshot epoch %d", attestation, snapshot.Epoch)
	}

	// An ineligible direct recipient is a measured refusal, not a snapshot.
	missing := model.NewID()
	_, _, err = f.attestor.AttestPublicationAudience(context.Background(),
		f.request(UrgencyNormal, nil, AudienceSelector{Kind: AudienceUser, Ref: missing.String(), WakePolicy: WakeNone}))
	if !errors.Is(err, ErrCommunicationNotFound) {
		t.Fatalf("ineligible direct recipient = %v, want ErrCommunicationNotFound", err)
	}

	// Revisions are compared against the CURRENT channel row.
	stale := request
	stale.ChannelACLRevision++
	if _, _, err := f.attestor.AttestPublicationAudience(context.Background(), stale); !errors.Is(err, ErrCommunicationSnapshotStale) {
		t.Fatalf("stale revision = %v, want ErrCommunicationSnapshotStale", err)
	}
}

func TestPublicationAudienceAttestorExpandsSubscribersByMode(t *testing.T) {
	t.Parallel()
	f := newAttestorFixture(t)
	ids := make([]model.ID, 7)
	for i := range ids {
		ids[i] = model.NewID()
	}
	user := func(i int) RecipientRef { return RecipientRef{Kind: RecipientUser, Ref: ids[i].String()} }
	subject := func(i int) CommunicationSubjectRef {
		return CommunicationSubjectRef{Kind: SubjectUser, Ref: ids[i].String()}
	}
	for _, i := range []int{1, 2, 3, 4, 5} {
		f.resolver.eligible[user(i)] = true
	}
	// ids[6] is a group member that is NOT eligible.
	group := model.NewID()
	f.resolver.groups[group.String()] = []attestorTestMember{
		{recipient: user(5), fact: store.AuthorizationFactRef{Kind: "core.user_group_member", ID: model.NewID(), Version: 1}},
		{recipient: user(6), fact: store.AuthorizationFactRef{Kind: "core.user_group_member", ID: model.NewID(), Version: 1}},
	}
	subAll := f.subscribe(t, subject(1), SubscriptionAll, false)
	f.subscribe(t, subject(2), SubscriptionNone, false)
	subMentions := f.subscribe(t, subject(3), SubscriptionMentions, false)
	subCritical := f.subscribe(t, subject(4), SubscriptionCritical, true)
	subGroup := f.subscribe(t, CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: group.String()}, SubscriptionAll, false)

	selector := AudienceSelector{Kind: AudienceSubscribers, WakePolicy: WakeNone}
	byRecipient := func(snapshot DirectorySnapshot) map[RecipientRef]ResolvedAudienceContribution {
		out := map[RecipientRef]ResolvedAudienceContribution{}
		for _, c := range snapshot.Contributions {
			out[c.Recipient.Recipient] = c
		}
		return out
	}

	// Normal urgency, ids[3] mentioned: all + mentions + group member ids[5].
	request := f.request(UrgencyNormal, []RecipientRef{user(3)}, selector)
	snapshot, attestation, err := f.attestor.AttestPublicationAudience(context.Background(), request)
	if err != nil {
		t.Fatalf("attest subscribers: %v", err)
	}
	if err := validatePublicationAudienceAttestation(request, snapshot, attestation, f.now.Add(2*time.Second)); err != nil {
		t.Fatalf("subscriber attestation invalid: %v", err)
	}
	got := byRecipient(snapshot)
	if len(got) != 3 || got[user(1)].SubscriptionID != subAll.ID || got[user(3)].SubscriptionID != subMentions.ID ||
		got[user(5)].SubscriptionID != subGroup.ID {
		t.Fatalf("normal-urgency expansion = %+v", got)
	}
	for _, c := range got {
		if c.CausalKind != CausalSubscriber || c.OriginalSubscriber == nil || c.SubscriptionGeneration != 1 ||
			c.SelectorOrdinal != 1 || c.Selector != selector || len(c.RouteReasons) != 1 ||
			c.RouteReasons[0] != routeReasonSubscription || c.RouteRuleID != "" {
			t.Fatalf("subscriber contribution lost provenance: %+v", c)
		}
	}
	if got[user(1)].CausalFact != nil || got[user(5)].CausalFact == nil ||
		got[user(5)].CausalFact.Kind != "core.user_group_member" ||
		got[user(5)].OriginalSubscriber.Kind != SubjectUserGroup {
		t.Fatalf("group subscriber expansion lost or invented its fact: %+v / %+v", got[user(1)], got[user(5)])
	}
	if _, present := got[user(6)]; present {
		t.Fatal("ineligible group member was delivered to")
	}

	// Critical urgency, nobody mentioned: all + critical (required) + group.
	request = f.request(UrgencyCritical, nil, selector)
	snapshot, _, err = f.attestor.AttestPublicationAudience(context.Background(), request)
	if err != nil {
		t.Fatalf("attest critical: %v", err)
	}
	got = byRecipient(snapshot)
	if len(got) != 3 || got[user(4)].SubscriptionID != subCritical.ID || !got[user(4)].Required || got[user(1)].Required {
		t.Fatalf("critical-urgency expansion = %+v", got)
	}
	if _, present := got[user(3)]; present {
		t.Fatal("mentions-mode subscriber delivered without a mention")
	}
	if err := ValidateDirectorySnapshotForSelectors(snapshot, request.Selectors); err != nil {
		t.Fatalf("critical snapshot invalid: %v", err)
	}
}

func TestPublicationAudienceAttestorStampsMatchingRouteProvenance(t *testing.T) {
	t.Parallel()
	f := newAttestorFixture(t)
	subscriber, direct := model.NewID(), model.NewID()
	f.resolver.eligible[RecipientRef{Kind: RecipientUser, Ref: subscriber.String()}] = true
	f.resolver.eligible[RecipientRef{Kind: RecipientUser, Ref: direct.String()}] = true
	f.subscribe(t, CommunicationSubjectRef{Kind: SubjectUser, Ref: subscriber.String()}, SubscriptionAll, false)
	// Two rules target the channel for subscribers; the lower priority value wins.
	f.route(t, "later", RouteAudienceSubscribers, 5)
	winner := f.route(t, "first", RouteAudienceSubscribers, 1)
	f.route(t, "other-audience", RouteAudienceWorkspaceMember, 0)

	request := f.request(UrgencyNormal, nil,
		AudienceSelector{Kind: AudienceUser, Ref: direct.String(), WakePolicy: WakeNone},
		AudienceSelector{Kind: AudienceSubscribers, WakePolicy: WakeNone},
	)
	snapshot, attestation, err := f.attestor.AttestPublicationAudience(context.Background(), request)
	if err != nil {
		t.Fatalf("attest with routes: %v", err)
	}
	if err := validatePublicationAudienceAttestation(request, snapshot, attestation, f.now.Add(2*time.Second)); err != nil {
		t.Fatalf("routed attestation invalid: %v", err)
	}
	if len(snapshot.Contributions) != 2 {
		t.Fatalf("contributions = %+v", snapshot.Contributions)
	}
	for _, c := range snapshot.Contributions {
		switch c.Selector.Kind {
		case AudienceUser:
			if c.RouteRuleID != "" || len(c.RouteReasons) != 1 || c.RouteReasons[0] != routeReasonDirect {
				t.Fatalf("direct selector picked up route provenance: %+v", c)
			}
		case AudienceSubscribers:
			if c.RouteRuleID != winner.ID || c.RouteRuleGeneration != 1 || len(c.RouteReasons) != 2 ||
				c.RouteReasons[0] != routeReasonRoute || c.RouteReasons[1] != routeReasonSubscription {
				t.Fatalf("subscriber contribution route provenance = %+v, want rule %s", c, winner.ID)
			}
		}
	}
}

func TestPublicationAudienceAttestorRefusesWithoutModuleOrResolver(t *testing.T) {
	t.Parallel()
	if _, err := NewCommunicationPublicationAudienceAttestor(nil, &attestorTestResolver{}, nil); err == nil {
		t.Fatal("nil module accepted")
	}
	var typedNil *attestorTestResolver
	if _, err := NewCommunicationPublicationAudienceAttestor(New(), typedNil, nil); err == nil {
		t.Fatal("typed-nil resolver accepted")
	}
}
