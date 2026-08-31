// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Gather runs the dual-loop collection: a webhook HTTP listener for real-time
// events and a periodic API poller for reconciliation. It blocks until ctx is
// canceled or one of the loops returns an error.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWebhook(sink))

	srv := &http.Server{
		Addr:              s.webhookAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The socket is opened through the product's single admission point, never by
	// srv.ListenAndServe: that call binds first and asks nothing. netbind refuses
	// BEFORE the syscall, so a refused listener never exists.
	ln, err := netbind.Listen(ctx, "tcp", s.webhookAddr, s.bindPolicy())
	if err != nil {
		return fmt.Errorf("gitlab: webhook receiver: %w", err)
	}

	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ln) }()
	go func() { errc <- s.poll(ctx, sink) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errc:
		return err
	}
}

// poll runs the periodic API reconciliation and ACL sync loops.
func (s *Source) poll(ctx context.Context, sink sdk.Sink) error {
	pollTick := time.NewTicker(s.pollInterval)
	defer pollTick.Stop()
	aclTick := time.NewTicker(s.aclInterval)
	defer aclTick.Stop()

	// Run ACL sync once at startup.
	if err := s.syncACL(ctx, sink); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pollTick.C:
			if err := s.pollProjects(ctx, sink); err != nil {
				return err
			}
		case <-aclTick.C:
			if err := s.syncACL(ctx, sink); err != nil {
				return err
			}
		}
	}
}

// pollProjects lists all projects and emits read edges for each branch as a
// reconciliation signal — the webhook path covers real-time writes; the
// poller catches gaps.
func (s *Source) pollProjects(ctx context.Context, sink sdk.Sink) error {
	projects, err := s.listProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		branches, err := s.listBranches(ctx, p.ID)
		if err != nil {
			return err
		}
		for _, b := range branches {
			edge := buildPollEdge(p.PathWithNamespace, b)
			if err := sink.Emit(ctx, edge); err != nil {
				return err
			}
		}
	}
	return nil
}
