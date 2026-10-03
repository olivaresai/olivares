// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"google.golang.org/grpc"
)

// AuthorizationDecisionRecorder is the existing evidence store's writer at the
// request boundary. A recorded PDP answer asserts no execution or capability.
type AuthorizationDecisionRecorder interface {
	RecordAuthorization(context.Context, auth.AuthorizationRecord) error
}

func (s *Server) recordAuthorizationDecisions(next http.Handler) http.Handler {
	if s.authorizationRecorder == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, flush := s.beginAuthorizationRecording(r.Context())
		// Store callbacks, including row authorization, have all returned before
		// the flush. Do not dispatch detached work that could outlive this request.
		defer flush()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type authorizationRecordingKey struct{}

// beginAuthorizationRecording owns one bounded buffer until its operation's
// store callbacks have returned. Nested operations share the buffer and leave
// its flush to the outer owner; a streaming authorization owns just its question.
func (s *Server) beginAuthorizationRecording(ctx context.Context) (context.Context, func()) {
	if s.authorizationRecorder == nil || ctx.Value(authorizationRecordingKey{}) != nil {
		return ctx, func() {}
	}
	parent := ctx
	var mu sync.Mutex
	var records []auth.AuthorizationRecord
	var bytes int
	var omitted bool
	ctx = auth.WithAuthorizationRecording(ctx, func(record auth.AuthorizationRecord) {
		mu.Lock()
		defer mu.Unlock()
		// The requester caused this audit event; the evaluated subject remains in
		// the retained question, including cross-subject PDP and review requests.
		if requester, ok := principalFrom(parent); ok {
			actor, err := requester.AttributableActor()
			if err != nil {
				omitted = true
				return
			}
			record.Actor, record.ActorKind = actor, requester.ActorKind()
		}
		if len(records) >= 128 {
			omitted = true
			return
		}
		budget := min(model.MaxPolicyArtifactBytes, (2<<20)-bytes)
		b, err := json.Marshal(record.Snapshot)
		if err != nil || len(b) > budget {
			// Retain the exact question and observed answer without claiming a
			// shortened policy artifact could reconstruct it.
			snapshot := record.Snapshot
			record.Snapshot = auth.RetainedAuthorization{
				Version: snapshot.Version, Tenant: snapshot.Tenant,
				Kind: snapshot.Kind, UserID: snapshot.UserID, CredID: snapshot.CredID,
				Permission: snapshot.Permission, Precondition: snapshot.Precondition,
				Resource: auth.ResourceAttrs{Kind: snapshot.Resource.Kind, ID: snapshot.Resource.ID},
			}
			b, err = json.Marshal(record.Snapshot)
		}
		if err != nil || len(b) > budget {
			omitted = true
			return
		}
		bytes += len(b)
		records = append(records, record)
	})
	flush := func() {
		mu.Lock()
		pending := records
		records = nil
		bytes = 0
		lost := omitted
		omitted = false
		mu.Unlock()
		if len(pending) == 0 && !lost {
			return
		}
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
		defer cancel()
		for _, record := range pending {
			if err := s.authorizationRecorder.RecordAuthorization(flushCtx, record); err != nil {
				lost = true
				break
			}
		}
		if lost && s.log != nil {
			s.log.Warn("authorization history could not be retained", "reason", "authorization_history_unavailable")
		}
	}
	return context.WithValue(ctx, authorizationRecordingKey{}, flush), flush
}

func (s *Server) grpcAuthorizationRecordingInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, flush := s.beginAuthorizationRecording(ctx)
	defer flush()
	return handler(ctx, req)
}
