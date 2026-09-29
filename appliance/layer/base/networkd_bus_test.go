// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type networkdReply struct {
	path   dbus.ObjectPath
	method string
	arg    any
	body   []any
	err    error
}

type networkdFakeBus struct {
	t                 *testing.T
	replies           []networkdReply
	calls             int
	closed            bool
	authErr, helloErr error
}

func (b *networkdFakeBus) Auth([]dbus.Auth) error { return b.authErr }
func (b *networkdFakeBus) Hello() error           { return b.helloErr }
func (b *networkdFakeBus) Close() error           { b.closed = true; return nil }
func (b *networkdFakeBus) call(ctx context.Context, dest string, path dbus.ObjectPath, method string, flags dbus.Flags, args ...any) *dbus.Call {
	b.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		b.t.Fatal("observation lacks its bounded context")
	}
	if flags != dbus.FlagNoAutoStart {
		b.t.Fatalf("autoactivation permitted: %v", flags)
	}
	target := ":1.5"
	if method == "org.freedesktop.DBus.GetNameOwner" {
		target = "org.freedesktop.DBus"
	}
	if dest != target {
		b.t.Fatalf("destination %q, want existing owner %q", dest, target)
	}
	if b.calls >= len(b.replies) {
		b.t.Fatalf("unexpected call %s: no mutations or loading admitted", method)
	}
	r := b.replies[b.calls]
	b.calls++
	if path != r.path || method != r.method || len(args) != 1 || args[0] != r.arg {
		b.t.Fatalf("call %s %s %v, want %s %s [%v]", path, method, args, r.path, r.method, r.arg)
	}
	return &dbus.Call{Body: r.body, Err: r.err}
}

func networkdTranscript() []networkdReply {
	owner := networkdReply{path: "/org/freedesktop/DBus", method: "org.freedesktop.DBus.GetNameOwner", arg: "org.freedesktop.systemd1", body: []any{":1.5"}}
	rows := []networkdReply{owner}
	for _, unit := range []string{"systemd-networkd.service", "systemd-networkd.socket"} {
		path := dbus.ObjectPath("/org/freedesktop/systemd1/unit/" + strings.ReplaceAll(strings.ReplaceAll(unit, "-", "_2d"), ".", "_2e"))
		rows = append(rows,
			networkdReply{path: "/org/freedesktop/systemd1", method: "org.freedesktop.systemd1.Manager.GetUnit", arg: unit, body: []any{path}},
			networkdReply{path: path, method: "org.freedesktop.DBus.Properties.GetAll", arg: "org.freedesktop.systemd1.Unit", body: []any{map[string]dbus.Variant{
				"Id": dbus.MakeVariant(unit), "LoadState": dbus.MakeVariant("loaded"), "ActiveState": dbus.MakeVariant("inactive"), "Job": dbus.MakeVariantWithSignature([]any{uint32(0), dbus.ObjectPath("/")}, dbus.ParseSignatureMust("(uo)")),
			}}},
			networkdReply{path: "/org/freedesktop/systemd1", method: "org.freedesktop.systemd1.Manager.GetUnitFileState", arg: unit, body: []any{"disabled"}},
		)
	}
	return append(rows, owner)
}

func observeNetworkdFake(t *testing.T, rows []networkdReply) (NetworkdState, error) {
	t.Helper()
	bus := &networkdFakeBus{t: t, replies: rows}
	state, err := readNetworkd(context.Background(), func(context.Context) (networkdBus, error) { return bus, nil })
	if !bus.closed {
		t.Fatal("private bus was not closed")
	}
	if err == nil && bus.calls != len(rows) {
		t.Fatalf("only read %d/%d calls", bus.calls, len(rows))
	}
	return state, err
}

func TestReadNetworkd_DormantAndConflictingUnitsThroughActualBusReader(t *testing.T) {
	for _, tc := range []struct {
		name, file, active, load, refusal string
		socket                            bool
		job                               uint32
	}{
		{name: "loaded disabled", file: "disabled", active: "inactive", load: "loaded"},
		{name: "masked", file: "masked", active: "inactive", load: "masked"},
		{name: "active service", file: "disabled", active: "active", load: "loaded", refusal: "second_network_owner"},
		{name: "active socket", file: "disabled", active: "active", load: "loaded", refusal: "second_network_owner", socket: true},
		{name: "activating", file: "disabled", active: "activating", load: "loaded", refusal: "second_network_owner"},
		{name: "queued", file: "disabled", active: "inactive", load: "loaded", job: 7, refusal: "second_network_owner"},
		{name: "enabled service", file: "enabled", active: "inactive", load: "loaded", refusal: "second_network_owner"},
		{name: "enabled socket", file: "enabled-runtime", active: "inactive", load: "loaded", refusal: "second_network_owner", socket: true},
		{name: "linked", file: "linked", active: "inactive", load: "loaded", refusal: "second_network_owner"},
		{name: "static", file: "static", active: "inactive", load: "loaded", refusal: "second_network_owner"},
		{name: "failed", file: "disabled", active: "failed", load: "loaded", refusal: "network_owner_unmeasured"},
		{name: "unknown file", file: "future", active: "inactive", load: "loaded", refusal: "network_owner_unmeasured"},
		{name: "unknown load", file: "disabled", active: "inactive", load: "future", refusal: "network_owner_unmeasured"},
		{name: "contradictory mask", file: "disabled", active: "inactive", load: "masked", refusal: "network_owner_unmeasured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := networkdTranscript()
			i := 2
			if tc.socket {
				i = 5
			}
			props := rows[i].body[0].(map[string]dbus.Variant)
			props["ActiveState"] = dbus.MakeVariant(tc.active)
			props["LoadState"] = dbus.MakeVariant(tc.load)
			if tc.job > 0 {
				props["Job"] = dbus.MakeVariantWithSignature([]any{tc.job, dbus.ObjectPath("/org/freedesktop/systemd1/job/7")}, dbus.ParseSignatureMust("(uo)"))
			}
			rows[i+1].body = []any{tc.file}
			got, err := observeNetworkdFake(t, rows)
			if err == nil {
				err = verifyDormantNetworkd(context.Background(), func(context.Context) (NetworkdState, error) { return got, nil })
			}
			if tc.refusal == "" && err != nil || tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)) {
				t.Fatalf("want %q, got %v", tc.refusal, err)
			}
		})
	}
}

func TestReadNetworkd_NotLoadedIsNotAnAbsentFile(t *testing.T) {
	for _, file := range []string{"disabled", "masked", "absent", "enabled", "static"} {
		t.Run(file, func(t *testing.T) {
			rows := networkdTranscript()
			rows[1].body = nil
			rows[1].err = dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", nil)
			if file == "absent" {
				rows[3].body = nil
				rows[3].err = dbus.NewError("org.freedesktop.DBus.Error.FileNotFound", nil)
			} else {
				rows[3].body = []any{file}
			}
			rows = append(rows[:2], rows[3:]...)
			got, err := observeNetworkdFake(t, rows)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Service.NotLoaded || got.Service.FileAbsent != (file == "absent") {
				t.Fatalf("lost independent file measurement: %+v", got.Service)
			}
			err = verifyDormantNetworkd(context.Background(), func(context.Context) (NetworkdState, error) { return got, nil })
			if file == "enabled" || file == "static" {
				if err == nil || !strings.Contains(err.Error(), "second_network_owner") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadNetworkd_InvalidOrFailedReadsNeverProveDormancy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]networkdReply)
	}{
		{"owner missing", func(r []networkdReply) { r[0].err = dbus.NewError("org.freedesktop.DBus.Error.NameHasNoOwner", nil) }},
		{"owner moved", func(r []networkdReply) { r[7].body = []any{":1.6"} }},
		{"well-known owner", func(r []networkdReply) { r[0].body = []any{"org.freedesktop.systemd1"} }},
		{"timeout", func(r []networkdReply) { r[1].err = context.DeadlineExceeded }},
		{"arbitrary missing text", func(r []networkdReply) { r[1].err = errors.New("org.freedesktop.systemd1.NoSuchUnit") }},
		{"access denied", func(r []networkdReply) { r[3].err = dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", nil) }},
		{"invented missing file", func(r []networkdReply) { r[3].err = dbus.NewError("org.freedesktop.systemd1.NoSuchUnitFile", nil) }},
		{"wrong unit path type", func(r []networkdReply) { r[1].body = []any{uint32(1)} }},
		{"string unit path", func(r []networkdReply) { r[1].body = []any{string(r[1].body[0].(dbus.ObjectPath))} }},
		{"duplicate reply", func(r []networkdReply) { r[3].body = []any{"disabled", "disabled"} }},
		{"missing active", func(r []networkdReply) { delete(r[2].body[0].(map[string]dbus.Variant), "ActiveState") }},
		{"wrong active type", func(r []networkdReply) {
			r[2].body[0].(map[string]dbus.Variant)["ActiveState"] = dbus.MakeVariant(true)
		}},
		{"wrong identity", func(r []networkdReply) {
			r[2].body[0].(map[string]dbus.Variant)["Id"] = dbus.MakeVariant("other.service")
		}},
		{"wrong job type", func(r []networkdReply) { r[2].body[0].(map[string]dbus.Variant)["Job"] = dbus.MakeVariant("none") }},
		{"contradictory job", func(r []networkdReply) {
			r[2].body[0].(map[string]dbus.Variant)["Job"] = dbus.MakeVariantWithSignature([]any{uint32(0), dbus.ObjectPath("/org/freedesktop/systemd1/job/7")}, dbus.ParseSignatureMust("(uo)"))
		}},
		{"mismatched job ID", func(r []networkdReply) {
			r[2].body[0].(map[string]dbus.Variant)["Job"] = dbus.MakeVariantWithSignature([]any{uint32(7), dbus.ObjectPath("/org/freedesktop/systemd1/job/9")}, dbus.ParseSignatureMust("(uo)"))
		}},
		{"wrong file type", func(r []networkdReply) { r[3].body = []any{false} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := networkdTranscript()
			tc.mutate(rows)
			if _, err := observeNetworkdFake(t, rows); err == nil {
				t.Fatal("unmeasured bus read accepted")
			}
		})
	}
}

func TestReadNetworkd_ClosesOnHandshakeFailureAndHonorsCancellation(t *testing.T) {
	for _, phase := range []string{"auth", "hello"} {
		t.Run(phase, func(t *testing.T) {
			b := &networkdFakeBus{t: t}
			if phase == "auth" {
				b.authErr = errors.New("auth")
			} else {
				b.helloErr = errors.New("hello")
			}
			if _, err := readNetworkd(context.Background(), func(context.Context) (networkdBus, error) { return b, nil }); err == nil || !b.closed || b.calls != 0 {
				t.Fatalf("err=%v closed=%v calls=%d", err, b.closed, b.calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dialed := false
	if _, err := readNetworkd(ctx, func(context.Context) (networkdBus, error) { dialed = true; return nil, errors.New("unexpected dial") }); !errors.Is(err, context.Canceled) || dialed {
		t.Fatalf("err=%v dialed=%v", err, dialed)
	}
}

func TestFirstBoot_VendorObservationUsesBusReaderWithoutRunningPrograms(t *testing.T) {
	for _, failOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "dormant", true: "unmeasured owner"}[failOwner], func(t *testing.T) {
			s, in := networkHost(t)
			place(t, s.Host.Root, "usr/lib/systemd/network/80-vendor.network", "[Match]\nName=host0\n[Network]\nDHCP=yes\n")
			rows := networkdTranscript()
			if failOwner {
				rows[0].err = dbus.NewError("org.freedesktop.DBus.Error.NameHasNoOwner", nil)
			}
			bus := &networkdFakeBus{t: t, replies: rows}
			s.Networkd = func(ctx context.Context) (NetworkdState, error) {
				return readNetworkd(ctx, func(context.Context) (networkdBus, error) { return bus, nil })
			}
			s.Host.Run = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("vendor observation executed a program")
				return nil, errors.New("forbidden")
			}
			_, err := s.Apply(context.Background(), in)
			if failOwner {
				if err == nil || !strings.Contains(err.Error(), "network_owner_unmeasured") {
					t.Fatalf("got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !bus.closed {
				t.Fatal("bus left open")
			}
		})
	}
}

func TestReadNetworkd_AbsentFileRequiresConsistentLoadedState(t *testing.T) {
	for _, load := range []string{"not-found", "loaded"} {
		t.Run(load, func(t *testing.T) {
			rows := networkdTranscript()
			rows[2].body[0].(map[string]dbus.Variant)["LoadState"] = dbus.MakeVariant(load)
			rows[3].body = nil
			rows[3].err = dbus.Error{Name: "org.freedesktop.DBus.Error.FileNotFound"}
			got, err := observeNetworkdFake(t, rows)
			if err != nil {
				t.Fatal(err)
			}
			err = verifyDormantNetworkd(context.Background(), func(context.Context) (NetworkdState, error) { return got, nil })
			if load == "loaded" {
				if err == nil || !strings.Contains(err.Error(), "network_owner_unmeasured") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
