// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Gather starts the dual-loop capture: a webhook HTTP listener for real-time
// events and a background poller for reconciliation and ACL sync. It blocks
// until ctx is canceled or a fatal error occurs.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWebhook(sink))
	srv := &http.Server{
		Addr:              s.webhookAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The socket is opened through the product's single admission point, never by
	// srv.ListenAndServe: that call binds first and asks nothing, which is how
	// this receiver served plaintext on a wildcard address for as long as it did.
	// netbind refuses BEFORE the syscall, so a refused listener never exists.
	ln, err := netbind.Listen(ctx, "tcp", s.webhookAddr, s.bindPolicy())
	if err != nil {
		return fmt.Errorf("github: webhook receiver: %w", err)
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return err
	}
}

// poll runs the reconciliation and ACL sync loops on separate tickers until
// ctx is canceled.
func (s *Source) poll(ctx context.Context, sink sdk.Sink) error {
	pollTick := time.NewTicker(s.pollInterval)
	defer pollTick.Stop()
	aclTick := time.NewTicker(s.aclInterval)
	defer aclTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pollTick.C:
			if err := s.reconcile(ctx, sink); err != nil {
				return err
			}
		case <-aclTick.C:
			if err := s.syncACL(ctx, sink); err != nil {
				return err
			}
		}
	}
}

// reconcile polls the GitHub API for recent pushes across all org repos and
// emits observed edges for any activity the webhook may have missed.
func (s *Source) reconcile(ctx context.Context, sink sdk.Sink) error {
	repos, err := s.listRepos(ctx)
	if err != nil {
		return err
	}
	for _, r := range repos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		branches, err := s.listBranches(ctx, r.FullName)
		if err != nil {
			return err
		}
		for _, branch := range branches {
			edges := s.buildReconEdges(r.FullName, branch)
			for _, e := range edges {
				if err := sink.Emit(ctx, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
