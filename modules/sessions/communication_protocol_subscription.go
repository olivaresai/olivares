// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	maxProtocolSubscriptionPage       = 200
	maxProtocolSubscriptionParamsSize = 64 << 10
	protocolSubscriptionHashDomain    = "olivares.sessions.protocol-subscription.v1"
)

var (
	ErrInvalidProtocolSubscription  = errors.New("sessions: invalid protocol subscription")
	ErrProtocolSubscriptionConflict = errors.New("sessions: protocol subscription cursor conflict")
	ErrProtocolSubscriptionCursor   = errors.New("sessions: invalid protocol subscription cursor")
	ErrProtocolSubscriptionUnknown  = errors.New("sessions: protocol subscription evidence unavailable")
)

// ProtocolSubscriptionRoute is the server-resolved durable namespace of a
// relayed protocol subscription. Subject is accepted only at this in-process
// port and persisted as a domain-bound hash; FilterDigest is the connector's
// canonical filter digest. Neither value grants tenant/workspace authority.
type ProtocolSubscriptionRoute struct {
	WorkspaceID   model.ID        `json:"workspace_id"`
	Protocol      BindingProtocol `json:"protocol"`
	PeerAuthority string          `json:"peer_authority"`
	Subject       string          `json:"-"`
	FilterDigest  string          `json:"filter_digest"`
}

// ProtocolSubscriptionCursor is the mutable CAS head. LastEventID and LastSeq
// always move in the same transaction that appends the corresponding immutable
// ProtocolSubscriptionEvent.
type ProtocolSubscriptionCursor struct {
	MutableCommunicationEntity
	Protocol      BindingProtocol `json:"protocol"`
	PeerAuthority string          `json:"peer_authority"`
	RouteHash     []byte          `json:"route_hash"`
	SubjectHash   []byte          `json:"subject_hash"`
	FilterHash    []byte          `json:"filter_hash"`
	LastEventID   model.ID        `json:"last_event_id,omitempty"`
	LastSeq       int64           `json:"last_seq"`
}

// ProtocolSubscriptionEvent is one notification durably committed before the
// gateway emits it. Cursor is the canonical UUIDv7 event id rendered as text.
type ProtocolSubscriptionEvent struct {
	AppendOnlyCommunicationEntity
	SubscriptionCursorID model.ID        `json:"subscription_cursor_id"`
	Cursor               string          `json:"cursor"`
	Seq                  int64           `json:"seq"`
	Method               string          `json:"method"`
	Params               json.RawMessage `json:"params"`
	ParamsHash           []byte          `json:"params_hash"`
	PreviousEventID      model.ID        `json:"previous_event_id,omitempty"`
}

type ProtocolSubscriptionCatchUp struct {
	Route  ProtocolSubscriptionRoute `json:"route"`
	Cursor string                    `json:"cursor,omitempty"`
	Limit  int                       `json:"limit,omitempty"`
}

type ProtocolSubscriptionPage struct {
	Events     []ProtocolSubscriptionEvent `json:"events"`
	NextCursor string                      `json:"next_cursor,omitempty"`
	HasMore    bool                        `json:"has_more"`
}

type ProtocolSubscriptionAppend struct {
	Route          ProtocolSubscriptionRoute `json:"route"`
	ExpectedCursor string                    `json:"expected_cursor,omitempty"`
	Method         string                    `json:"method"`
	Params         json.RawMessage           `json:"params"`
}

// ProtocolSubscriptionStore is the composition-root port behind the MCP
// connector's durable subscription ledger.
type ProtocolSubscriptionStore interface {
	CatchUpProtocolSubscription(
		context.Context,
		model.TenantID,
		ProtocolSubscriptionCatchUp,
	) (ProtocolSubscriptionPage, error)
	AppendProtocolSubscriptionEvent(
		context.Context,
		model.TenantID,
		ProtocolSubscriptionAppend,
	) (ProtocolSubscriptionEvent, error)
}

var _ ProtocolSubscriptionStore = (*Module)(nil)

type normalizedProtocolSubscriptionRoute struct {
	ProtocolSubscriptionRoute
	routeHash   []byte
	subjectHash []byte
	filterHash  []byte
}

func normalizeProtocolSubscriptionRoute(
	route ProtocolSubscriptionRoute,
) (normalizedProtocolSubscriptionRoute, error) {
	route.Protocol = BindingProtocol(strings.ToLower(strings.TrimSpace(string(route.Protocol))))
	peer, err := normalizeProtocolAuthority(route.PeerAuthority)
	if err != nil {
		return normalizedProtocolSubscriptionRoute{}, protocolSubscriptionInvalid("invalid_peer_authority")
	}
	route.PeerAuthority = peer
	if route.Subject != strings.TrimSpace(route.Subject) || !validateOpaqueRef(route.Subject) ||
		!validCanonicalCommunicationID(route.WorkspaceID) || !route.Protocol.valid() {
		return normalizedProtocolSubscriptionRoute{}, protocolSubscriptionInvalid("invalid_route")
	}
	filterHash, err := hex.DecodeString(strings.TrimSpace(route.FilterDigest))
	if err != nil || len(filterHash) != sha256.Size || hex.EncodeToString(filterHash) != route.FilterDigest {
		return normalizedProtocolSubscriptionRoute{}, protocolSubscriptionInvalid("invalid_filter_digest")
	}
	subjectDigest := sha256.Sum256([]byte(protocolSubscriptionHashDomain + "\x00subject\x00" + route.Subject))
	routeDigest := sha256.Sum256([]byte(
		protocolSubscriptionHashDomain + "\x00route\x00" + route.WorkspaceID.String() + "\x00" +
			string(route.Protocol) + "\x00" + route.PeerAuthority + "\x00" +
			hex.EncodeToString(subjectDigest[:]) + "\x00" + route.FilterDigest,
	))
	return normalizedProtocolSubscriptionRoute{
		ProtocolSubscriptionRoute: route,
		routeHash:                 append([]byte(nil), routeDigest[:]...),
		subjectHash:               append([]byte(nil), subjectDigest[:]...),
		filterHash:                append([]byte(nil), filterHash...),
	}, nil
}

func normalizeProtocolSubscriptionEvent(method string, params json.RawMessage) (string, json.RawMessage, []byte, error) {
	method = strings.TrimSpace(method)
	switch method {
	case "notifications/tools/list_changed",
		"notifications/prompts/list_changed",
		"notifications/resources/list_changed",
		"notifications/resources/updated":
	default:
		return "", nil, nil, protocolSubscriptionInvalid("invalid_method")
	}
	if len(params) == 0 || len(params) > maxProtocolSubscriptionParamsSize {
		return "", nil, nil, protocolSubscriptionInvalid("invalid_params")
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return "", nil, nil, protocolSubscriptionInvalid("invalid_params")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", nil, nil, protocolSubscriptionInvalid("invalid_params")
	}
	canonical, err := canonicalJSON(object)
	if err != nil || len(canonical) > maxProtocolSubscriptionParamsSize {
		return "", nil, nil, protocolSubscriptionInvalid("invalid_params")
	}
	return method, json.RawMessage(canonical), hashBytes(canonical), nil
}

// CatchUpProtocolSubscription returns committed events strictly after the
// supplied opaque cursor. A cursor is accepted only when its event belongs to
// this exact tenant/workspace/peer/subject/filter route.
func (m *Module) CatchUpProtocolSubscription(
	ctx context.Context,
	tenant model.TenantID,
	request ProtocolSubscriptionCatchUp,
) (ProtocolSubscriptionPage, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return ProtocolSubscriptionPage{}, protocolSubscriptionInvalid("invalid_tenant")
	}
	route, err := normalizeProtocolSubscriptionRoute(request.Route)
	if err != nil {
		return ProtocolSubscriptionPage{}, err
	}
	request.Cursor = strings.TrimSpace(request.Cursor)
	if request.Limit <= 0 {
		request.Limit = maxProtocolSubscriptionPage
	}
	if request.Limit > maxProtocolSubscriptionPage || strings.ContainsAny(request.Cursor, "\r\n") {
		return ProtocolSubscriptionPage{}, protocolSubscriptionInvalid("invalid_catch_up")
	}
	result := ProtocolSubscriptionPage{Events: []ProtocolSubscriptionEvent{}, NextCursor: request.Cursor}
	err = m.workData(tenant).View(ctx, func(sc store.Scope) error {
		headRepo, err := sc.Ext(protocolSubscriptionCursorKind)
		if err != nil {
			return err
		}
		head, found, err := findProtocolSubscriptionCursor(ctx, headRepo, route)
		if err != nil {
			return err
		}
		if !found {
			if request.Cursor != "" {
				return protocolSubscriptionCursor("cursor_route_not_found")
			}
			return nil
		}

		afterSeq := int64(0)
		previousEventID := model.ID("")
		if request.Cursor != "" {
			cursorID, err := model.ParseID(request.Cursor)
			if err != nil || !validCanonicalCommunicationID(cursorID) {
				return protocolSubscriptionCursor("invalid_cursor")
			}
			eventRepo, err := sc.Ext(protocolSubscriptionEventKind)
			if err != nil {
				return err
			}
			row, err := eventRepo.Get(ctx, cursorID)
			if errors.Is(err, store.ErrNotFound) {
				return protocolSubscriptionCursor("cursor_not_found")
			}
			if err != nil {
				return err
			}
			event, err := protocolSubscriptionEventFromRecord(row)
			if err != nil {
				return err
			}
			if event.SubscriptionCursorID != head.ID || event.Cursor != request.Cursor {
				return protocolSubscriptionCursor("cursor_route_mismatch")
			}
			afterSeq = event.Seq
			previousEventID = event.ID
		}

		eventRepo, err := sc.Ext(protocolSubscriptionEventKind)
		if err != nil {
			return err
		}
		rows, _, err := eventRepo.List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: colWorkWorkspaceID, Op: model.OpEq, Value: route.WorkspaceID.String()},
				{Column: colProtocolSubscriptionHeadID, Op: model.OpEq, Value: head.ID.String()},
				{Column: colProtocolSubscriptionCursorSeq, Op: model.OpGt, Value: afterSeq},
			},
			Sort:  []model.Sort{{Column: colProtocolSubscriptionCursorSeq}},
			Limit: request.Limit + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > request.Limit {
			result.HasMore = true
			rows = rows[:request.Limit]
		}
		for _, row := range rows {
			event, err := protocolSubscriptionEventFromRecord(row)
			if err != nil {
				return err
			}
			if event.SubscriptionCursorID != head.ID || event.Seq != afterSeq+1 ||
				event.PreviousEventID != previousEventID {
				return protocolSubscriptionUnknown("event_order_invalid", nil)
			}
			result.Events = append(result.Events, event)
			result.NextCursor = event.Cursor
			afterSeq = event.Seq
			previousEventID = event.ID
		}
		return nil
	})
	if err != nil {
		return ProtocolSubscriptionPage{}, classifyProtocolSubscriptionStoreError(err)
	}
	return result, nil
}

// AppendProtocolSubscriptionEvent atomically appends a notification and advances
// the mutable head from ExpectedCursor. A mismatch is a CAS conflict, never a
// last-writer-wins update.
func (m *Module) AppendProtocolSubscriptionEvent(
	ctx context.Context,
	tenant model.TenantID,
	request ProtocolSubscriptionAppend,
) (ProtocolSubscriptionEvent, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return ProtocolSubscriptionEvent{}, protocolSubscriptionInvalid("invalid_tenant")
	}
	route, err := normalizeProtocolSubscriptionRoute(request.Route)
	if err != nil {
		return ProtocolSubscriptionEvent{}, err
	}
	request.ExpectedCursor = strings.TrimSpace(request.ExpectedCursor)
	if strings.ContainsAny(request.ExpectedCursor, "\r\n") {
		return ProtocolSubscriptionEvent{}, protocolSubscriptionInvalid("invalid_expected_cursor")
	}
	method, params, paramsHash, err := normalizeProtocolSubscriptionEvent(request.Method, request.Params)
	if err != nil {
		return ProtocolSubscriptionEvent{}, err
	}

	var result ProtocolSubscriptionEvent
	err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		headRepo, err := sc.Ext(protocolSubscriptionCursorKind)
		if err != nil {
			return err
		}
		head, found, err := findProtocolSubscriptionCursor(ctx, headRepo, route)
		if err != nil {
			return err
		}
		if !found {
			if request.ExpectedCursor != "" {
				return protocolSubscriptionConflict("cursor_changed")
			}
			if _, err := sc.Workspaces().Get(ctx, route.WorkspaceID); err != nil {
				return err
			}
			now, err := transactionNow(ctx, sc)
			if err != nil {
				return err
			}
			head = ProtocolSubscriptionCursor{
				MutableCommunicationEntity: MutableCommunicationEntity{
					CommunicationEntity: CommunicationEntity{
						ID: model.NewID(), TenantID: tenant, WorkspaceID: route.WorkspaceID,
						Version: 1, CreatedAt: now.Time(),
					},
					UpdatedAt: now.Time(),
				},
				Protocol: route.Protocol, PeerAuthority: route.PeerAuthority,
				RouteHash:   cloneCommunicationBytes(route.routeHash),
				SubjectHash: cloneCommunicationBytes(route.subjectHash),
				FilterHash:  cloneCommunicationBytes(route.filterHash),
			}
			created, err := headRepo.CreateWithID(ctx, head.ID, protocolSubscriptionCursorRecord(head))
			if err != nil {
				return err
			}
			head, err = protocolSubscriptionCursorFromRecord(created)
			if err != nil {
				return err
			}
		} else {
			locker, ok := headRepo.(store.RowLocker[model.Record])
			if !ok {
				return protocolSubscriptionUnknown("cursor_lock_unavailable", nil)
			}
			locked, err := locker.Lock(ctx, head.ID)
			if err != nil {
				return err
			}
			head, err = protocolSubscriptionCursorFromRecord(locked)
			if err != nil {
				return err
			}
		}

		actualCursor := head.LastEventID.String()
		if actualCursor != request.ExpectedCursor {
			return protocolSubscriptionConflict("cursor_changed")
		}
		if head.LastSeq == math.MaxInt64 {
			return protocolSubscriptionUnknown("cursor_exhausted", nil)
		}
		now, err := transactionNow(ctx, sc)
		if err != nil {
			return err
		}
		eventID := model.NewID()
		event := ProtocolSubscriptionEvent{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: eventID, TenantID: tenant, WorkspaceID: route.WorkspaceID,
					Version: 1, CreatedAt: now.Time(),
				},
			},
			SubscriptionCursorID: head.ID, Cursor: eventID.String(), Seq: head.LastSeq + 1,
			Method: method, Params: params, ParamsHash: paramsHash, PreviousEventID: head.LastEventID,
		}
		eventRepo, err := sc.Ext(protocolSubscriptionEventKind)
		if err != nil {
			return err
		}
		created, err := eventRepo.CreateWithID(ctx, eventID, protocolSubscriptionEventRecord(event))
		if err != nil {
			return err
		}
		result, err = protocolSubscriptionEventFromRecord(created)
		if err != nil {
			return err
		}
		head.LastEventID = eventID
		head.LastSeq = event.Seq
		head.UpdatedAt = now.Time()
		updated, err := headRepo.Update(ctx, protocolSubscriptionCursorRecord(head))
		if err != nil {
			return err
		}
		updatedHead, err := protocolSubscriptionCursorFromRecord(updated)
		if err != nil {
			return err
		}
		if updatedHead.LastEventID != eventID || updatedHead.LastSeq != event.Seq {
			return protocolSubscriptionUnknown("cursor_settlement_mismatch", nil)
		}
		return nil
	})
	if err != nil {
		return ProtocolSubscriptionEvent{}, classifyProtocolSubscriptionStoreError(err)
	}
	return result, nil
}

func findProtocolSubscriptionCursor(
	ctx context.Context,
	repo store.GenericRepo,
	route normalizedProtocolSubscriptionRoute,
) (ProtocolSubscriptionCursor, bool, error) {
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colWorkWorkspaceID, Op: model.OpEq, Value: route.WorkspaceID.String()},
		{Column: colProtocolSubscriptionRouteHash, Op: model.OpEq, Value: route.routeHash},
	}, Limit: 2})
	if err != nil || len(rows) == 0 {
		return ProtocolSubscriptionCursor{}, false, err
	}
	if len(rows) != 1 {
		return ProtocolSubscriptionCursor{}, false, protocolSubscriptionUnknown("cursor_not_unique", nil)
	}
	head, err := protocolSubscriptionCursorFromRecord(rows[0])
	if err != nil {
		return ProtocolSubscriptionCursor{}, false, err
	}
	if !bytes.Equal(head.RouteHash, route.routeHash) || !bytes.Equal(head.SubjectHash, route.subjectHash) ||
		!bytes.Equal(head.FilterHash, route.filterHash) || head.Protocol != route.Protocol ||
		head.PeerAuthority != route.PeerAuthority || head.WorkspaceID != route.WorkspaceID {
		return ProtocolSubscriptionCursor{}, false, protocolSubscriptionUnknown("cursor_route_corrupt", nil)
	}
	return head, true, nil
}

func protocolSubscriptionCursorRecord(cursor ProtocolSubscriptionCursor) model.Record {
	record := mutableCommunicationRecord(cursor.MutableCommunicationEntity)
	record[colProtocolSubscriptionProtocol] = string(cursor.Protocol)
	record[colProtocolSubscriptionPeerAuthority] = cursor.PeerAuthority
	record[colProtocolSubscriptionRouteHash] = cloneCommunicationBytes(cursor.RouteHash)
	record[colProtocolSubscriptionSubjectHash] = cloneCommunicationBytes(cursor.SubjectHash)
	record[colProtocolSubscriptionFilterHash] = cloneCommunicationBytes(cursor.FilterHash)
	record[colProtocolSubscriptionLastEventID] = optionalCommunicationID(cursor.LastEventID)
	record[colProtocolSubscriptionLastSeq] = cursor.LastSeq
	return record
}

func protocolSubscriptionCursorFromRecord(record model.Record) (ProtocolSubscriptionCursor, error) {
	reader := newCommunicationRecordReader(protocolSubscriptionCursorKind, record)
	cursor := ProtocolSubscriptionCursor{
		MutableCommunicationEntity: reader.mutableEntity(),
		Protocol:                   BindingProtocol(reader.text(colProtocolSubscriptionProtocol)),
		PeerAuthority:              reader.text(colProtocolSubscriptionPeerAuthority),
		RouteHash:                  reader.bytes(colProtocolSubscriptionRouteHash),
		SubjectHash:                reader.bytes(colProtocolSubscriptionSubjectHash),
		FilterHash:                 reader.bytes(colProtocolSubscriptionFilterHash),
		LastEventID:                reader.optionalID(colProtocolSubscriptionLastEventID),
		LastSeq:                    reader.integer(colProtocolSubscriptionLastSeq),
	}
	if reader.err != nil || !cursor.Protocol.valid() || cursor.PeerAuthority == "" ||
		len(cursor.RouteHash) != sha256.Size || len(cursor.SubjectHash) != sha256.Size ||
		len(cursor.FilterHash) != sha256.Size || cursor.LastSeq < 0 ||
		(cursor.LastSeq == 0) != cursor.LastEventID.IsZero() {
		if reader.err != nil {
			return ProtocolSubscriptionCursor{}, reader.err
		}
		return ProtocolSubscriptionCursor{}, protocolSubscriptionUnknown("invalid_durable_cursor", nil)
	}
	return cursor, nil
}

func protocolSubscriptionEventRecord(event ProtocolSubscriptionEvent) model.Record {
	record := appendOnlyCommunicationRecord(event.AppendOnlyCommunicationEntity)
	record[colProtocolSubscriptionHeadID] = event.SubscriptionCursorID.String()
	record[colProtocolSubscriptionCursorID] = event.Cursor
	record[colProtocolSubscriptionCursorSeq] = event.Seq
	record[colProtocolSubscriptionMethod] = event.Method
	record[colProtocolSubscriptionParamsJSON] = string(event.Params)
	record[colProtocolSubscriptionParamsHash] = cloneCommunicationBytes(event.ParamsHash)
	record[colProtocolSubscriptionPreviousID] = optionalCommunicationID(event.PreviousEventID)
	return record
}

func protocolSubscriptionEventFromRecord(record model.Record) (ProtocolSubscriptionEvent, error) {
	reader := newCommunicationRecordReader(protocolSubscriptionEventKind, record)
	event := ProtocolSubscriptionEvent{
		AppendOnlyCommunicationEntity: reader.appendOnlyEntity(),
		SubscriptionCursorID:          reader.id(colProtocolSubscriptionHeadID),
		Cursor:                        reader.text(colProtocolSubscriptionCursorID),
		Seq:                           reader.integer(colProtocolSubscriptionCursorSeq),
		Method:                        reader.text(colProtocolSubscriptionMethod),
		Params:                        reader.canonicalJSON(colProtocolSubscriptionParamsJSON),
		ParamsHash:                    reader.bytes(colProtocolSubscriptionParamsHash),
		PreviousEventID:               reader.optionalID(colProtocolSubscriptionPreviousID),
	}
	if reader.err != nil || event.Cursor != event.ID.String() || event.Seq < 1 ||
		len(event.ParamsHash) != sha256.Size || !bytes.Equal(hashBytes(event.Params), event.ParamsHash) ||
		(event.Seq == 1) != event.PreviousEventID.IsZero() {
		if reader.err != nil {
			return ProtocolSubscriptionEvent{}, reader.err
		}
		return ProtocolSubscriptionEvent{}, protocolSubscriptionUnknown("invalid_durable_event", nil)
	}
	if _, _, _, err := normalizeProtocolSubscriptionEvent(event.Method, event.Params); err != nil {
		return ProtocolSubscriptionEvent{}, protocolSubscriptionUnknown("invalid_durable_event", err)
	}
	return event, nil
}

func protocolSubscriptionInvalid(code string) error {
	return fmt.Errorf("%w: %s", ErrInvalidProtocolSubscription, code)
}

func protocolSubscriptionConflict(code string) error {
	return fmt.Errorf("%w: %s", ErrProtocolSubscriptionConflict, code)
}

func protocolSubscriptionCursor(code string) error {
	return fmt.Errorf("%w: %s", ErrProtocolSubscriptionCursor, code)
}

func protocolSubscriptionUnknown(code string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrProtocolSubscriptionUnknown, code)
	}
	return fmt.Errorf("%w: %s: %v", ErrProtocolSubscriptionUnknown, code, cause)
}

func classifyProtocolSubscriptionStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidProtocolSubscription), errors.Is(err, ErrProtocolSubscriptionConflict),
		errors.Is(err, ErrProtocolSubscriptionCursor), errors.Is(err, ErrProtocolSubscriptionUnknown):
		return err
	case errors.Is(err, store.ErrConflict):
		return protocolSubscriptionConflict("cursor_changed")
	case errors.Is(err, store.ErrWorkspaceConfinement), errors.Is(err, store.ErrWorkspaceLineageRequired),
		errors.Is(err, store.ErrNotFound):
		return protocolSubscriptionCursor("cursor_not_found")
	default:
		return protocolSubscriptionUnknown("store_unavailable", err)
	}
}
