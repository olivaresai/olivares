// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Route reasons written by the local publication attestor. They are bounded
// tokens, unique and canonically sorted per contribution; "direct" is the only
// reason a direct selector may carry, which validateDirectNoticeSnapshot pins.
const (
	routeReasonDirect           RouteReason = "direct"
	routeReasonUserGroup        RouteReason = "user_group"
	routeReasonAgentGroup       RouteReason = "agent_group"
	routeReasonWorkspaceMembers RouteReason = "workspace_members"
	routeReasonSubscription     RouteReason = "subscription"
	routeReasonRoute            RouteReason = "route"

	publicationAudienceEvidenceRef   = "sessions.publication_audience.v1"
	publicationAudienceSubscriberCap = 512
)

// communicationPublicationAudienceAttestor composes the authoritative core
// directory (through the bound DirectorySnapshotResolver) with the Channel's
// CURRENT subscription and route state. The directory resolver cannot expand
// subscribers because core neither owns ChannelID nor its revisions; this type
// owns exactly that join and nothing else: it never authorizes, never reads
// grants and never writes.
//
// Direct selectors stay exact: one recipient, causal kind direct, reason
// "direct", no route or subscription provenance. Group and workspace selectors
// keep the directory's causal facts. A subscribers selector expands the active
// subscriptions by mode (all / mentions / critical / none) into synthetic
// directory selectors, resolves them under the SAME epoch fence as the rest of
// the request, and relabels every contribution as a subscriber arc that keeps
// the original subject, subscription id/generation and any group fact.
type communicationPublicationAudienceAttestor struct {
	module   *Module
	resolver DirectorySnapshotResolver
	now      func() time.Time
}

// NewCommunicationPublicationAudienceAttestor binds the local attestor. It is
// the constructor the composition root uses; a nil module or resolver keeps
// the attestor unbound so readiness stays OFF.
func NewCommunicationPublicationAudienceAttestor(
	module *Module,
	resolver DirectorySnapshotResolver,
	now func() time.Time,
) (PublicationAudienceAttestor, error) {
	if module == nil || !communicationPortBound(resolver) {
		return nil, communicationError(ErrCommunicationEvidenceUnknown,
			"publication audience attestor requires the module and a directory resolver")
	}
	if now == nil {
		now = func() time.Time { return module.now() }
	}
	return &communicationPublicationAudienceAttestor{module: module, resolver: resolver, now: now}, nil
}

type publicationChannelState struct {
	channel       Channel
	subscriptions []ChannelSubscription
	routes        []ChannelRouteRule
}

// subscriberExpansion remembers which synthetic directory selector came from
// which subscription so the resolved contributions can be relabeled.
type subscriberExpansion struct {
	requestOrdinal int64
	selector       AudienceSelector
	subscription   ChannelSubscription
}

func (a *communicationPublicationAudienceAttestor) AttestPublicationAudience(
	ctx context.Context,
	request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	if err := ValidatePublicationAudienceRequest(request); err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	state, err := a.readChannelState(ctx, request)
	if err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	if request.ChannelACLRevision != state.channel.ACLRevision ||
		request.RouteRevision != state.channel.RouteRevision ||
		request.SubscriptionRevision != state.channel.SubscriptionRevision {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
			ErrCommunicationSnapshotStale, "publication request revisions are not current")
	}

	// Build the exact directory request: plain selectors first (their ordinal is
	// the request ordinal), then every synthetic subscriber selector.
	directorySelectors := make([]AudienceSelector, 0, len(request.Selectors))
	plainOrdinal := make(map[int]int64, len(request.Selectors))
	expansions := make(map[int]subscriberExpansion)
	for index, selector := range request.Selectors {
		if selector.Kind == AudienceSubscribers {
			continue
		}
		plainOrdinal[len(directorySelectors)] = int64(index + 1)
		directorySelectors = append(directorySelectors, selector)
	}
	for index, selector := range request.Selectors {
		if selector.Kind != AudienceSubscribers {
			continue
		}
		expanded, err := expandSubscribers(request, state, selector)
		if err != nil {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
		}
		for _, item := range expanded {
			item.requestOrdinal = int64(index + 1)
			expansions[len(directorySelectors)] = item
			directorySelectors = append(directorySelectors, item.selector)
		}
	}

	var resolved DirectorySnapshot
	if len(directorySelectors) != 0 {
		resolved, err = a.resolver.ResolveAudience(ctx, request.Scope, directorySelectors)
		if err != nil {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
		}
		if resolved.Scope != request.Scope || resolved.Epoch < 1 || resolved.ObservedAt.IsZero() ||
			!resolved.FreshUntil.After(resolved.ObservedAt) || len(resolved.Selectors) != len(directorySelectors) {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
				ErrCommunicationEvidenceUnknown, "directory audience resolution is malformed")
		}
	} else {
		// Every selector was a subscribers selector with nothing to deliver to.
		// The snapshot still needs an epoch and observation window: take them
		// from a fenced empty resolution of the channel scope.
		resolved, err = a.resolver.ResolveAudience(ctx, request.Scope, nil)
		if err != nil {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
		}
	}

	roster := make(map[RecipientRef]RecipientSnapshot, len(resolved.Recipients))
	for _, recipient := range resolved.Recipients {
		roster[recipient.Recipient] = recipient
	}
	contributions := make([]ResolvedAudienceContribution, 0, len(resolved.Contributions))
	covered := make(map[int64]int, len(request.Selectors))
	for _, contribution := range resolved.Contributions {
		position := int(contribution.SelectorOrdinal - 1)
		if position < 0 || position >= len(directorySelectors) ||
			contribution.Selector != directorySelectors[position] {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
				ErrCommunicationEvidenceUnknown, "directory contribution is not bound to a requested selector")
		}
		if ordinal, plain := plainOrdinal[position]; plain {
			relabeled := contribution
			relabeled.SelectorOrdinal = ordinal
			relabeled.Selector = request.Selectors[ordinal-1]
			relabeled.RouteReasons = normalizeRouteReasons(relabeled.RouteReasons)
			applyRouteProvenance(&relabeled, request, state)
			contributions = append(contributions, relabeled)
			covered[ordinal]++
			continue
		}
		expansion, ok := expansions[position]
		if !ok {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
				ErrCommunicationEvidenceUnknown, "directory contribution lost its subscription provenance")
		}
		relabeled, keep, err := relabelSubscriberContribution(contribution, expansion, request)
		if err != nil {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
		}
		if !keep {
			continue
		}
		applyRouteProvenance(&relabeled, request, state)
		contributions = append(contributions, relabeled)
		covered[relabeled.SelectorOrdinal]++
	}
	for index, selector := range request.Selectors {
		ordinal := int64(index + 1)
		direct := oneOf(selector.Kind, AudienceUser, AudienceAgent, AudienceSession)
		if direct && covered[ordinal] != 1 {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
				ErrCommunicationNotFound, "direct recipient %s:%s is not an eligible principal",
				selector.Kind, selector.Ref)
		}
		if selector.Required && covered[ordinal] == 0 {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
				ErrCommunicationNotFound, "required selector %d resolved to no eligible recipient", ordinal)
		}
	}
	contributions = dedupeResolvedContributions(contributions)

	recipients := make([]RecipientSnapshot, 0, len(roster))
	for _, contribution := range contributions {
		if _, present := roster[contribution.Recipient.Recipient]; !present {
			return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
				ErrCommunicationEvidenceUnknown, "contribution recipient is missing from the directory roster")
		}
	}
	for _, recipient := range roster {
		recipients = append(recipients, recipient)
	}
	sort.Slice(recipients, func(i, j int) bool {
		if recipients[i].Recipient.Kind != recipients[j].Recipient.Kind {
			return recipients[i].Recipient.Kind < recipients[j].Recipient.Kind
		}
		return recipients[i].Recipient.Ref < recipients[j].Recipient.Ref
	})
	rosterHash, err := CanonicalDirectoryRosterHash(request.Scope, resolved.Epoch, recipients)
	if err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	snapshot := DirectorySnapshot{
		Scope: request.Scope, Epoch: resolved.Epoch,
		Selectors:     append([]AudienceSelector(nil), request.Selectors...),
		Recipients:    recipients,
		Contributions: contributions,
		RosterHash:    rosterHash,
		ObservedAt:    resolved.ObservedAt, FreshUntil: resolved.FreshUntil,
	}
	if request.RequestedAt.After(snapshot.ObservedAt) {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, communicationError(
			ErrCommunicationEvidenceUnknown, "publication request postdates the directory observation")
	}
	if err := ValidateDirectorySnapshotForSelectors(snapshot, request.Selectors); err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	requestHash, err := CanonicalPublicationAudienceRequestHash(request)
	if err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	snapshotHash, err := CanonicalPublicationAudienceSnapshotHash(snapshot)
	if err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	return snapshot, PublicationAudienceAttestation{
		Scope: request.Scope, DirectoryEpoch: snapshot.Epoch,
		RequestHash: requestHash, SnapshotHash: snapshotHash,
		ObservedAt: snapshot.ObservedAt, FreshUntil: snapshot.FreshUntil,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "audience_attested", EvidenceRef: publicationAudienceEvidenceRef,
		},
	}, nil
}

// readChannelState reads the Channel and, only when a selector needs them, its
// active subscriptions and the active routes targeting it, in ONE workspace
// view so the three agree with each other and with the revisions compared by
// the caller.
func (a *communicationPublicationAudienceAttestor) readChannelState(
	ctx context.Context,
	request PublicationAudienceRequest,
) (publicationChannelState, error) {
	needSubscriptions, needRoutes := false, false
	for _, selector := range request.Selectors {
		switch selector.Kind {
		case AudienceSubscribers:
			needSubscriptions, needRoutes = true, true
		case AudienceUserGroup, AudienceAgentGroup, AudienceWorkspaceMembers:
			needRoutes = true
		}
	}
	var state publicationChannelState
	err := a.module.viewCommunication(ctx, request.Scope, func(sc store.Scope) error {
		channelRepo, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		record, err := channelRepo.Get(ctx, request.ChannelID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return communicationError(ErrCommunicationNotFound, "publication channel does not exist")
			}
			return err
		}
		channel, err := channelFromRecord(record)
		if err != nil {
			return err
		}
		if channel.TenantID != request.Scope.TenantID || channel.WorkspaceID != request.Scope.WorkspaceID ||
			channel.ID != request.ChannelID {
			return communicationError(ErrCommunicationEvidenceUnknown, "publication channel crosses scope")
		}
		if channel.State != ChannelActive {
			return communicationError(ErrInvalidCommunicationTransition, "publication channel is not active")
		}
		state.channel = channel
		if needSubscriptions {
			repo, err := sc.Ext(channelSubscriptionKind)
			if err != nil {
				return err
			}
			rows, err := listAllCommunicationRecords(ctx, repo, []model.Filter{
				{Column: colCommChannelID, Op: model.OpEq, Value: channel.ID.String()},
				{Column: colCommState, Op: model.OpEq, Value: string(SubscriptionActive)},
			}, publicationAudienceSubscriberCap)
			if err != nil {
				return err
			}
			for _, row := range rows {
				subscription, decodeErr := channelSubscriptionFromRecord(row)
				if decodeErr != nil || subscription.ChannelID != channel.ID ||
					subscription.TenantID != channel.TenantID || subscription.WorkspaceID != channel.WorkspaceID {
					return communicationError(ErrCommunicationEvidenceUnknown, "channel subscription snapshot is malformed")
				}
				state.subscriptions = append(state.subscriptions, subscription)
			}
		}
		if needRoutes {
			repo, err := sc.Ext(channelRouteKind)
			if err != nil {
				return err
			}
			rows, err := listAllCommunicationRecords(ctx, repo, []model.Filter{
				{Column: colCommTargetChannelID, Op: model.OpEq, Value: channel.ID.String()},
				{Column: colCommState, Op: model.OpEq, Value: string(ChannelRouteActive)},
			}, publicationAudienceSubscriberCap)
			if err != nil {
				return err
			}
			for _, row := range rows {
				route, decodeErr := channelRouteRuleFromRecord(row)
				if decodeErr != nil || route.TargetChannelID != channel.ID ||
					route.TenantID != channel.TenantID || route.WorkspaceID != channel.WorkspaceID {
					return communicationError(ErrCommunicationEvidenceUnknown, "channel route snapshot is malformed")
				}
				state.routes = append(state.routes, route)
			}
		}
		return nil
	})
	return state, err
}

func listAllCommunicationRecords(
	ctx context.Context,
	repo store.GenericRepo,
	filters []model.Filter,
	bound int,
) ([]model.Record, error) {
	query := model.Query{Filters: filters, Limit: 100}
	var out []model.Record
	for {
		rows, page, err := repo.List(ctx, query)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(out) > bound {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"channel publication state exceeds the bounded snapshot")
		}
		if !page.HasMore {
			return out, nil
		}
		if page.Cursor == "" {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"channel publication state pagination lost its continuation")
		}
		query.Cursor = page.Cursor
	}
}

// expandSubscribers turns the current active subscriptions into synthetic
// directory selectors under the subscribers selector's required/wake policy.
// The newest generation per subject wins; mode decides participation:
// all: always · mentions: only if the subject (or a member of it) is mentioned
// · critical: only for critical urgency · none: never.
func expandSubscribers(
	request PublicationAudienceRequest,
	state publicationChannelState,
	selector AudienceSelector,
) ([]subscriberExpansion, error) {
	latest := make(map[CommunicationSubjectRef]ChannelSubscription, len(state.subscriptions))
	for _, subscription := range state.subscriptions {
		if current, present := latest[subscription.Subscriber]; !present ||
			subscription.Generation > current.Generation {
			latest[subscription.Subscriber] = subscription
		}
	}
	subjects := make([]CommunicationSubjectRef, 0, len(latest))
	for subject := range latest {
		subjects = append(subjects, subject)
	}
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].Kind != subjects[j].Kind {
			return subjects[i].Kind < subjects[j].Kind
		}
		return subjects[i].Ref < subjects[j].Ref
	})
	mentioned := make(map[RecipientRef]struct{}, len(request.MentionedRecipients))
	for _, recipient := range request.MentionedRecipients {
		mentioned[recipient] = struct{}{}
	}
	out := make([]subscriberExpansion, 0, len(subjects))
	for _, subject := range subjects {
		subscription := latest[subject]
		switch subscription.Mode {
		case SubscriptionNone:
			continue
		case SubscriptionCritical:
			if request.Urgency != UrgencyCritical {
				continue
			}
		case SubscriptionMentions:
			// A direct subject must itself be mentioned; a group subject is
			// expanded and filtered per member during relabeling.
			if kind, direct := subscriberRecipientKind(subject.Kind); direct {
				if _, ok := mentioned[RecipientRef{Kind: kind, Ref: subject.Ref}]; !ok {
					continue
				}
			} else if len(mentioned) == 0 {
				continue
			}
		}
		synthetic, ok := subscriberSelector(subject)
		if !ok {
			return nil, communicationError(ErrInvalidCommunicationModel, "unknown subscriber subject kind")
		}
		synthetic.Required = selector.Required ||
			(subscription.RequiredForCritical && request.Urgency == UrgencyCritical)
		synthetic.WakePolicy = subscription.Wake
		if synthetic.WakePolicy == WakeInherit || !synthetic.WakePolicy.Valid() {
			synthetic.WakePolicy = selector.WakePolicy
		}
		out = append(out, subscriberExpansion{selector: synthetic, subscription: subscription})
	}
	return out, nil
}

func subscriberRecipientKind(kind CommunicationSubjectKind) (RecipientKind, bool) {
	switch kind {
	case SubjectUser:
		return RecipientUser, true
	case SubjectAgent:
		return RecipientAgent, true
	case SubjectSession:
		return RecipientSession, true
	default:
		return "", false
	}
}

func subscriberSelector(subject CommunicationSubjectRef) (AudienceSelector, bool) {
	switch subject.Kind {
	case SubjectUser:
		return AudienceSelector{Kind: AudienceUser, Ref: subject.Ref}, true
	case SubjectAgent:
		return AudienceSelector{Kind: AudienceAgent, Ref: subject.Ref}, true
	case SubjectSession:
		return AudienceSelector{Kind: AudienceSession, Ref: subject.Ref}, true
	case SubjectUserGroup:
		return AudienceSelector{Kind: AudienceUserGroup, Ref: subject.Ref}, true
	case SubjectAgentGroup:
		return AudienceSelector{Kind: AudienceAgentGroup, Ref: subject.Ref}, true
	default:
		return AudienceSelector{}, false
	}
}

// relabelSubscriberContribution rewrites a synthetic-selector contribution as
// a subscriber arc of the request's subscribers selector. A direct subject
// drops the (absent) causal fact; a group subject keeps its membership fact,
// which validateAudienceCausality requires. Under mentions mode a group member
// that is not mentioned is dropped.
func relabelSubscriberContribution(
	contribution ResolvedAudienceContribution,
	expansion subscriberExpansion,
	request PublicationAudienceRequest,
) (ResolvedAudienceContribution, bool, error) {
	subject := expansion.subscription.Subscriber
	relabeled := contribution
	relabeled.SelectorOrdinal = expansion.requestOrdinal
	relabeled.Selector = request.Selectors[expansion.requestOrdinal-1]
	relabeled.CausalKind = CausalSubscriber
	relabeled.CausalRef = subject.Ref
	original := subject
	relabeled.OriginalSubscriber = &original
	relabeled.SubscriptionID = expansion.subscription.ID
	relabeled.SubscriptionGeneration = expansion.subscription.Generation
	relabeled.Required = expansion.selector.Required
	relabeled.WakePolicy = expansion.selector.WakePolicy
	relabeled.RouteReasons = normalizeRouteReasons([]RouteReason{routeReasonSubscription})
	relabeled.RouteRuleID, relabeled.RouteRuleGeneration = "", 0
	if _, direct := subscriberRecipientKind(subject.Kind); direct {
		relabeled.CausalFact = nil
	} else if relabeled.CausalFact == nil {
		return ResolvedAudienceContribution{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "group subscriber expansion lost its membership fact")
	}
	if expansion.subscription.Mode == SubscriptionMentions {
		mentioned := false
		for _, recipient := range request.MentionedRecipients {
			if recipient == relabeled.Recipient.Recipient {
				mentioned = true
				break
			}
		}
		if !mentioned {
			return ResolvedAudienceContribution{}, false, nil
		}
	}
	return relabeled, true, nil
}

// applyRouteProvenance stamps the single best matching active route rule that
// targets the channel with the contribution's audience kind. Direct selectors
// never carry route provenance. Best = lowest Priority, then route key, then
// id, so two attestations of the same state agree.
func applyRouteProvenance(
	contribution *ResolvedAudienceContribution,
	request PublicationAudienceRequest,
	state publicationChannelState,
) {
	var wantKind ChannelRouteAudienceKind
	switch contribution.Selector.Kind {
	case AudienceSubscribers:
		wantKind = RouteAudienceSubscribers
	case AudienceUserGroup:
		wantKind = RouteAudienceUserGroup
	case AudienceAgentGroup:
		wantKind = RouteAudienceAgentGroup
	case AudienceWorkspaceMembers:
		wantKind = RouteAudienceWorkspaceMember
	default:
		return
	}
	var best *ChannelRouteRule
	for index := range state.routes {
		route := &state.routes[index]
		if route.AudienceKind != wantKind || !routeRuleMatchesRequest(*route, request) {
			continue
		}
		if wantKind == RouteAudienceUserGroup || wantKind == RouteAudienceAgentGroup {
			if route.AudienceRef != contribution.Selector.Ref {
				continue
			}
		}
		if best == nil || route.Priority < best.Priority ||
			(route.Priority == best.Priority && route.RouteKey < best.RouteKey) ||
			(route.Priority == best.Priority && route.RouteKey == best.RouteKey &&
				route.ID.String() < best.ID.String()) {
			best = route
		}
	}
	if best == nil {
		return
	}
	contribution.RouteRuleID = best.ID
	contribution.RouteRuleGeneration = best.Generation
	contribution.RouteReasons = normalizeRouteReasons(append(contribution.RouteReasons, routeReasonRoute))
}

// routeRuleMatchesRequest applies the rule's matcher to the publication:
// source kind always; then catch-all, or event type for event sources, or
// message kind (and minimum urgency) for user messages; labels as an exact
// subset match of the request's labels.
func routeRuleMatchesRequest(route ChannelRouteRule, request PublicationAudienceRequest) bool {
	if route.SourceKind != request.SourceKind {
		return false
	}
	if !route.CatchAll {
		if route.SourceKind == RouteSourceUserMessage {
			if route.MessageKind != "" && route.MessageKind != request.MessageKind {
				return false
			}
			if route.MinimumUrgency != "" && urgencyRank(request.Urgency) < urgencyRank(route.MinimumUrgency) {
				return false
			}
		} else if route.EventType != "" && route.EventType != request.EventType {
			return false
		}
	}
	if len(route.LabelMatchJSON) == 0 {
		return true
	}
	want, err := validateCanonicalLabelMap(route.LabelMatchJSON)
	if err != nil {
		return false
	}
	if len(request.LabelsJSON) == 0 {
		return false
	}
	have, err := validateCanonicalLabelMap(request.LabelsJSON)
	if err != nil {
		return false
	}
	for key, value := range want {
		if have[key] != value {
			return false
		}
	}
	return true
}

func urgencyRank(urgency MessageUrgency) int {
	switch urgency {
	case UrgencyCritical:
		return 3
	case UrgencyHigh:
		return 2
	case UrgencyNormal:
		return 1
	default:
		return 0
	}
}

// dedupeResolvedContributions removes exact duplicate causal arcs within one
// selector ordinal, which the snapshot validator would otherwise reject, and
// keeps the first occurrence in resolver order.
func dedupeResolvedContributions(
	contributions []ResolvedAudienceContribution,
) []ResolvedAudienceContribution {
	seen := make(map[resolvedAudienceArcKey]struct{}, len(contributions))
	out := make([]ResolvedAudienceContribution, 0, len(contributions))
	for _, contribution := range contributions {
		key := resolvedAudienceArcKey{
			SelectorOrdinal: contribution.SelectorOrdinal,
			Arc:             resolvedAudienceCausalArcIdentity(contribution),
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, contribution)
	}
	return out
}

var _ PublicationAudienceAttestor = (*communicationPublicationAudienceAttestor)(nil)
