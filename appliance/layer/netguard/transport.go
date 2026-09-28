// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package netguard

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const nmName = "org.freedesktop.NetworkManager"
const nmPath = dbus.ObjectPath("/org/freedesktop/NetworkManager")
const deviceInterface = nmName + ".Device"

type settingsMap = map[string]map[string]dbus.Variant

type outgoingCall struct {
	expected string
	channel  chan Reply
	before   func(CallRecord) error
	method   Method
	failure  error
}

// NMTransport owns a private connection and its one original process descriptor.
// There is no reconnect or destination substitution operation on this type.
type NMTransport struct {
	successor                 *NMTransport
	conn                      *dbus.Conn
	clock                     *BootClock
	process                   *processFD
	busID, caller, generation string
	management                []string
	sendMu                    sync.Mutex
	active                    *outgoingCall
	replyMu                   sync.Mutex
	waiting                   map[uint32]*outgoingCall
	lastSerial                uint32
}

func NewSystemTransport(management []string) (*NMTransport, error) {
	clock, err := NewBootClock()
	if err != nil {
		return nil, err
	}
	generation, err := randomHex()
	if err != nil {
		return nil, err
	}
	t := &NMTransport{clock: clock, generation: generation, management: append([]string(nil), management...), waiting: map[uint32]*outgoingCall{}}
	conn, err := dbus.SystemBusPrivate(dbus.WithIncomingInterceptor(t.incoming), dbus.WithOutgoingInterceptor(t.outgoing))
	if err != nil {
		return nil, err
	}
	t.conn = conn
	fail := func(err error) (*NMTransport, error) { t.Close(); return nil, err }
	authDone := make(chan error, 1)
	go func() {
		if err := conn.Auth(nil); err != nil {
			authDone <- err
			return
		}
		authDone <- conn.Hello()
	}()
	select {
	case err := <-authDone:
		if err != nil {
			return fail(err)
		}
	case <-time.After(5 * time.Second):
		return fail(errors.New("network_bus_auth_timeout"))
	}
	names := conn.Names()
	if len(names) == 0 || !strings.HasPrefix(names[0], ":") {
		return fail(errors.New("network_caller_identity_unavailable"))
	}
	t.caller = names[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, err := t.read(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetId")
	if err != nil {
		return fail(err)
	}
	if len(body) != 1 {
		return fail(errors.New("network_bus_identity_unavailable"))
	}
	t.busID, _ = body[0].(string)
	if t.busID == "" {
		return fail(errors.New("network_bus_identity_unavailable"))
	}
	body, err = t.read(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetNameOwner", nmName)
	if err != nil {
		return fail(err)
	}
	if len(body) != 1 {
		return fail(errors.New("network_owner_unavailable"))
	}
	owner, _ := body[0].(string)
	if !strings.HasPrefix(owner, ":") {
		return fail(errors.New("network_owner_unavailable"))
	}
	body, err = t.read(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetConnectionCredentials", owner)
	if err != nil {
		return fail(err)
	}
	if len(body) != 1 {
		return fail(errors.New("network_process_identity_unavailable"))
	}
	credentials, ok := body[0].(map[string]dbus.Variant)
	if !ok {
		return fail(errors.New("network_process_identity_unavailable"))
	}
	descriptor, ok := credentials["ProcessFD"].Value().(dbus.UnixFD)
	if !ok {
		return fail(errors.New("network_process_identity_unavailable"))
	}
	process, err := credentialProcess(int(descriptor), owner, clock.boot)
	if err != nil {
		return fail(err)
	}
	t.process = process
	return t, nil
}
func (t *NMTransport) Close() {
	if t.successor != nil {
		t.successor.Close()
	}
	if t.conn != nil {
		t.conn.Close()
	}
	if t.process != nil {
		closeProcess(t.process)
		t.process = nil
	}
}
func (t *NMTransport) OriginalDeath(id ProcessIdentity) Death {
	if t.successor != nil && (t.process == nil || t.process.identity != id) {
		return t.successor.OriginalDeath(id)
	}
	return originalDeath(id, t.process, t.clock.boot)
}
func (t *NMTransport) outgoing(msg *dbus.Message) {
	if msg.Type != dbus.TypeMethodCall {
		return
	}
	if msg.Serial() == 0 || msg.Serial() <= t.lastSerial {
		t.conn.Close()
		if t.active != nil {
			t.active.failure = errors.New("network_serial_generation_exhausted")
		}
		return
	}
	t.lastSerial = msg.Serial()
	active := t.active
	if active == nil {
		return
	}
	if active.before != nil {
		digest := sha256.New()
		if err := msg.EncodeTo(digest, binary.LittleEndian); err != nil {
			active.failure = err
			t.conn.Close()
			return
		}
		object, _ := msg.Headers[dbus.FieldPath].Value().(dbus.ObjectPath)
		c := CallRecord{BusID: t.busID, Caller: t.caller, ConnectionGeneration: t.generation, Serial: msg.Serial(), Target: t.process.identity, Method: active.method, Object: string(object), ArgumentsDigest: hex.EncodeToString(digest.Sum(nil))}
		if err := active.before(c); err != nil {
			active.failure = err
			t.conn.Close()
			return
		}
	}
	t.replyMu.Lock()
	t.waiting[msg.Serial()] = active
	t.replyMu.Unlock()
}
func (t *NMTransport) incoming(msg *dbus.Message) {
	if msg.Type != dbus.TypeMethodReply && msg.Type != dbus.TypeError {
		return
	}
	serial, ok := msg.Headers[dbus.FieldReplySerial].Value().(uint32)
	if !ok {
		return
	}
	sender, ok := msg.Headers[dbus.FieldSender].Value().(string)
	if !ok {
		return
	}
	t.replyMu.Lock()
	active := t.waiting[serial]
	if active == nil || active.expected != sender {
		t.replyMu.Unlock()
		return
	}
	delete(t.waiting, serial)
	t.replyMu.Unlock()
	reply := Reply{BusID: t.busID, ConnectionGeneration: t.generation, Sender: sender, ReplySerial: serial, Completed: true, Success: msg.Type == dbus.TypeMethodReply, Body: msg.Body}
	select {
	case active.channel <- reply:
	default:
	}
}
func (t *NMTransport) dispatch(destination string, object dbus.ObjectPath, iface, member string, body []any, before func(CallRecord) error, method Method) (<-chan Reply, error) {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if !t.conn.Connected() {
		return nil, errors.New("network_bus_disconnected")
	}
	call := &outgoingCall{expected: destination, channel: make(chan Reply, 1), before: before, method: method}
	t.active = call
	defer func() { t.active = nil }()
	msg := &dbus.Message{Type: dbus.TypeMethodCall, Flags: dbus.FlagNoAutoStart, Headers: map[dbus.HeaderField]dbus.Variant{dbus.FieldDestination: dbus.MakeVariant(destination), dbus.FieldPath: dbus.MakeVariant(object), dbus.FieldInterface: dbus.MakeVariant(iface), dbus.FieldMember: dbus.MakeVariant(member)}, Body: body}
	if len(body) > 0 {
		msg.Headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(body...))
	}
	// godbus Call.Err deliberately has no settlement authority. Only incoming above
	// supplies a reply, after sender and reply serial match this exact connection.
	t.conn.Send(msg, make(chan *dbus.Call, 1))
	if call.failure != nil {
		return nil, call.failure
	}
	return call.channel, nil
}
func (t *NMTransport) read(ctx context.Context, destination string, object dbus.ObjectPath, iface, member string, args ...any) ([]any, error) {
	ch, err := t.dispatch(destination, object, iface, member, args, nil, "")
	if err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if !r.Success {
			return nil, errors.New("network_read_refused")
		}
		return r.Body, nil
	case <-ctx.Done():
		t.replyMu.Lock()
		for serial, waiting := range t.waiting {
			if waiting.channel == ch {
				delete(t.waiting, serial)
			}
		}
		t.replyMu.Unlock()
		return nil, errors.New("network_read_unavailable")
	}
}
func (t *NMTransport) get(ctx context.Context, object dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	body, err := t.read(ctx, t.process.identity.Unique, object, "org.freedesktop.DBus.Properties", "Get", iface, name)
	if err != nil {
		return dbus.Variant{}, err
	}
	if len(body) != 1 {
		return dbus.Variant{}, errors.New("network_property_invalid")
	}
	v, ok := body[0].(dbus.Variant)
	if !ok {
		return dbus.Variant{}, errors.New("network_property_invalid")
	}
	return v, nil
}
func (t *NMTransport) verifyTarget(ctx context.Context) error {
	if t.process == nil || processPoll(t.process.fd) != DeathAlive {
		return errors.New("network_original_process_unavailable")
	}
	names := t.conn.Names()
	if len(names) == 0 || names[0] != t.caller {
		return errors.New("network_caller_identity_changed")
	}
	body, err := t.read(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetId")
	if err != nil || len(body) != 1 || body[0] != t.busID {
		return errors.New("network_bus_identity_changed")
	}
	body, err = t.read(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetNameOwner", nmName)
	if err != nil || len(body) != 1 || body[0] != t.process.identity.Unique {
		return errors.New("network_owner_changed")
	}
	return nil
}
func (t *NMTransport) Permissions(ctx context.Context) error {
	if t.successor != nil {
		return t.successor.Permissions(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := t.verifyTarget(ctx); err != nil {
		return err
	}
	body, err := t.read(ctx, t.process.identity.Unique, nmPath, nmName, "GetPermissions")
	if err != nil {
		return err
	}
	if len(body) != 1 {
		return errors.New("network_permissions_unavailable")
	}
	permissions, ok := body[0].(map[string]string)
	if !ok {
		return errors.New("network_permissions_unavailable")
	}
	for _, action := range []string{"settings.modify.system", "network-control", "checkpoint-rollback"} {
		if permissions[nmName+"."+action] != "yes" {
			return errors.New("network_permissions_missing")
		}
	}
	return nil
}
func (t *NMTransport) Send(m Mutation, before func(CallRecord) error) (<-chan Reply, error) {
	if t.successor != nil {
		return t.successor.Send(m, before)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := t.verifyTarget(ctx); err != nil {
		return nil, err
	}
	var settings settingsMap
	if m.Method == UpdateInMemory || m.Method == Reapply || m.Method == UpdateToDisk || m.Method == RestoreActivation {
		device, err := t.device(ctx, m.Profile.Interface)
		if err != nil {
			return nil, err
		}
		m.Profile.Device = string(device)
		if m.Method == RestoreActivation {
			cp, _, found, err := t.findProfile(ctx, m.Profile.UUID)
			if err != nil || !found {
				return nil, errors.New("network_restored_profile_unavailable")
			}
			stored, err := t.storedProfile(ctx, cp, m.Profile.Interface, string(device))
			if err != nil {
				return nil, err
			}
			if !stored.Persistent() || stored.Filename != m.Profile.Filename || !stored.Matches(m.Profile) {
				return nil, errors.New("network_restored_profile_unmeasured")
			}
			m.Profile.Connection = string(cp)
			m.restorationVerified = true
		} else {
			current, all, err := t.profile(ctx, device, m.Method == UpdateInMemory)
			if err != nil {
				return nil, err
			}
			if current.Profile.UUID != m.Profile.UUID {
				return nil, errors.New("profile_identity_changed")
			}
			m.Profile.Connection = current.Profile.Connection
			if m.Method == UpdateInMemory {
				if current.Profile.ProfileVersion != m.Version {
					return nil, errors.New("profile_version_changed")
				}
				if !current.SecretsComplete {
					return nil, errors.New("network_secrets_unavailable")
				}
				settings = all
				replaceIP(settings, "ipv4", m.Profile.IPv4)
				replaceIP(settings, "ipv6", m.Profile.IPv6)
			}
		}
	}

	object, iface, member, body, err := mutationMessage(m, settings)
	if err != nil {
		return nil, err
	}
	return t.dispatch(t.process.identity.Unique, object, iface, member, body, before, m.Method)
}
func mutationMessage(m Mutation, settings settingsMap) (dbus.ObjectPath, string, string, []any, error) {
	refuse := func() (dbus.ObjectPath, string, string, []any, error) {
		return "", "", "", nil, errors.New("network_mutation_refused")
	}
	switch m.Method {
	case CheckpointCreate:
		if m.Flags != 6 || m.TimeoutSeconds < 90 || m.TimeoutSeconds > 630 || m.Profile.Device == "" {
			return refuse()
		}
		return nmPath, nmName, "CheckpointCreate", []any{[]dbus.ObjectPath{dbus.ObjectPath(m.Profile.Device)}, m.TimeoutSeconds, uint32(6)}, nil
	case UpdateInMemory:
		if m.Flags != 2 || m.Version == 0 || len(settings) == 0 || m.EmptySettings {
			return refuse()
		}
		return dbus.ObjectPath(m.Profile.Connection), ProfileInterface, "Update2", []any{settings, uint32(2), map[string]dbus.Variant{"version-id": dbus.MakeVariant(m.Version)}}, nil
	case UpdateToDisk:
		if m.Flags != 1 || m.Version == 0 || !m.EmptySettings {
			return refuse()
		}
		return dbus.ObjectPath(m.Profile.Connection), ProfileInterface, "Update2", []any{settingsMap{}, uint32(1), map[string]dbus.Variant{"version-id": dbus.MakeVariant(m.Version)}}, nil
	case Reapply:
		if m.Version == 0 || !m.EmptySettings || m.Flags != 0 {
			return refuse()
		}
		return dbus.ObjectPath(m.Profile.Device), deviceInterface, "Reapply", []any{settingsMap{}, m.Version, uint32(0)}, nil
	case RestoreActivation:
		if !m.restorationVerified || m.Profile.Connection == "" || m.Profile.Device == "" {
			return refuse()
		}
		return nmPath, nmName, "ActivateConnection", []any{dbus.ObjectPath(m.Profile.Connection), dbus.ObjectPath(m.Profile.Device), dbus.ObjectPath("/")}, nil
	case CheckpointDestroy, CheckpointRollback:
		if m.Checkpoint == "" || m.Checkpoint == "/" || !dbus.ObjectPath(m.Checkpoint).IsValid() {
			return refuse()
		}
		member := "CheckpointDestroy"
		if m.Method == CheckpointRollback {
			member = "CheckpointRollback"
		}
		return nmPath, nmName, member, []any{dbus.ObjectPath(m.Checkpoint)}, nil
	}
	return refuse()
}

func storeReply(body []any, out ...any) error {
	if err := dbus.Store(body, out...); err != nil {
		return errors.New("network_reply_shape_invalid")
	}
	return nil
}

// QualifyRecovery creates an explicitly new binding only after Engine has settled
// every earlier effect. Old callbacks keep their original immutable connection identity.
func (t *NMTransport) QualifyRecovery(ctx context.Context) (TargetBinding, error) {
	if t.successor != nil {
		return t.successor.QualifyRecovery(ctx)
	}
	if err := t.verifyTarget(ctx); err == nil {
		return TargetBinding{BusID: t.busID, Process: t.process.identity}, nil
	}
	fresh, err := NewSystemTransport(t.management)
	if err != nil {
		return TargetBinding{}, err
	}
	t.successor = fresh
	return TargetBinding{BusID: fresh.busID, Process: fresh.process.identity}, nil
}
