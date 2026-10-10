// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitbinding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

func TestOpenValidatesTheRemote(t *testing.T) {
	for _, c := range []struct {
		name, remote string
		refuse       bool
	}{
		{"ssh", "ssh://git@lan.example:2222/srv/git/tools.git", false},
		{"https", "https://git.example.com/acme/tools.git", false},
		{"missing", "", true},
		{"file", "file:///srv/git/tools.git", true},
		{"scp-like", "git@lan.example:srv/git/tools.git", true},
		{"ssh without user", "ssh://lan.example/srv/git/tools.git", true},
		{"one-segment path the custody refuses", "ssh://git@lan.example/tools.git", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := New()
			err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"remote": c.remote}})
			if c.refuse {
				if err == nil || !strings.Contains(err.Error(), "remote") {
					t.Fatalf("err = %v, want a remote refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatherEmitsNothing(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"remote": "ssh://git@lan.example/srv/git/tools.git"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Gather(context.Background(), nil); err != nil {
		t.Fatalf("gather = %v, want the batch to be complete immediately", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGatherHonorsEndedContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"canceled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, context.Canceled},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Unix(0, 0))
		}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"remote": "ssh://git@lan.example/srv/git/tools.git"}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := tc.ctx()
			defer cancel()
			if err := s.Gather(ctx, nil); !errors.Is(err, tc.want) {
				t.Fatalf("gather = %v, want %v", err, tc.want)
			}
		})
	}
}

// The descriptor declares the one field the row needs; the publication
// credential stays a custody concern and is never declared here.
func TestDescriptorDeclaresOnlyTheRemote(t *testing.T) {
	d := New().Descriptor()
	if len(d.ConfigFields) != 1 || d.ConfigFields[0].Key != "remote" || !d.ConfigFields[0].Required {
		t.Fatalf("config fields = %+v, want exactly the required remote", d.ConfigFields)
	}
}
