// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// This executable is an unpackaged guest fixture. It uses the production engine and
// real NM bus as the guard account, with explicit probe and root-library seams.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/appliance/answers/carriers"
	"github.com/olivaresai/olivares/appliance/layer/base"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"io"
	"net"
	"net/netip"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const controlSocket = "/run/olivares-network-core/control.sock"
const rootSocket = "/run/olivares-network-root-fixture/restore.sock"

type fixtureProbe struct{}

func (fixtureProbe) Check(_ context.Context, w netguard.Window, o netguard.Observation, c netguard.Confirmation) error {
	if c.Witness != "fixture:"+w.Digest+":"+w.Candidate.Interface || !o.RuntimeUsable {
		return errors.New("fixture_device_probe_missing")
	}
	return nil
}

type fixtureRoot struct{}

func (fixtureRoot) Report(_ context.Context, w netguard.Window) (netguard.RestoreReport, error) {
	return netguard.ReadRestoreReport(w)
}
func (fixtureRoot) Restore(ctx context.Context, w netguard.Window) (netguard.RestoreReport, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", rootSocket)
	if err != nil {
		return netguard.RestoreReport{}, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(25 * time.Second))
	err = json.NewEncoder(c).Encode(netguard.RestoreRequest{ConnectionUUID: w.Baseline.UUID, OperationID: w.OperationID})
	if err != nil {
		return netguard.RestoreReport{}, err
	}
	c.(*net.UnixConn).CloseWrite()
	_, _ = io.Copy(io.Discard, io.LimitReader(c, 4096))
	return netguard.ReadRestoreReport(w)
}

type request struct {
	Action, OperationID string
	Change              netguard.Change
	Confirmation        netguard.Confirmation
}
type response struct {
	Scope       string                `json:"scope"`
	Error       string                `json:"error,omitempty"`
	Status      *netguard.Status      `json:"status,omitempty"`
	Token       string                `json:"token,omitempty"`
	Digest      string                `json:"digest,omitempty"`
	Observation *netguard.Observation `json:"observation,omitempty"`
	Window      *netguard.Window      `json:"window,omitempty"`
}

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "verify-firstboot":
		if os.Geteuid() != 0 {
			os.Exit(2)
		}
		doc, err := os.ReadFile(carriers.NoCloudPath)
		if err != nil {
			os.Exit(2)
		}
		input, err := base.NewInput("nocloud:"+carriers.NoCloudPath, "", doc)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		effect, err := (base.CloudInitHost{Host: base.Host{Root: "/"}}).Apply(context.Background(), input)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(effect)

	case "guard":
		if err := guard(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "root-fixture":
		if err := rootFixture(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	default:
		os.Exit(2)
	}
}
func guard() error {
	account, err := user.Lookup("olivares-net-guard")
	if err != nil || account.Uid != strconv.Itoa(os.Geteuid()) || os.Geteuid() == 0 {
		return errors.New("fixture_requires_actual_guard_account")
	}
	bus, err := netguard.NewSystemTransport([]string{"nic0"})
	if err != nil {
		return err
	}
	defer bus.Close()
	clock, err := netguard.NewBootClock()
	if err != nil {
		return err
	}
	lock, err := netguard.NewFileLock()
	if err != nil {
		return err
	}
	journal, err := netguard.OpenJournal("/var/lib/olivares-net-guard/acts")
	if err != nil {
		return err
	}
	engine, err := netguard.NewEngine(netguard.Config{Bus: bus, Clock: clock, Lock: lock, Journal: journal, Probe: fixtureProbe{}, Restorer: fixtureRoot{}})
	if err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := engine.Tick(context.Background()); err != nil {
				fmt.Fprintln(os.Stderr, "fixture recovery unresolved:", err)
			}
		}
	}()
	_ = os.Remove(controlSocket)
	l, err := net.Listen("unix", controlSocket)
	if err != nil {
		return err
	}
	defer l.Close()
	if err := os.Chmod(controlSocket, 0600); err != nil {
		return err
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(40 * time.Second))
			var r request
			d := json.NewDecoder(io.LimitReader(c, 16385))
			d.DisallowUnknownFields()
			if err := d.Decode(&r); err != nil {
				return
			}
			out := response{Scope: "real-NM; actual-guard-account; injected-probe; root-library-fixture"}
			var failure error
			switch r.Action {
			case "observe":
				reader, e := netguard.NewSystemTransport([]string{"nic0"})
				if e != nil {
					failure = e
					break
				}
				o, e := reader.Observe(context.Background(), "nic1")
				reader.Close()
				out.Observation = &o
				failure = e
			case "apply":
				s, token, e := engine.Apply(context.Background(), r.Change)
				out.Status = &s
				out.Token = token
				out.Digest, _ = r.Change.Digest()
				failure = e
			case "confirm":
				failure = engine.Confirm(context.Background(), r.Confirmation)
				s, e := engine.Status(r.Confirmation.OperationID)
				if failure == nil {
					failure = e
				}
				out.Status = &s
			case "revert":
				failure = engine.Revert(context.Background(), r.OperationID)
				s, e := engine.Status(r.OperationID)
				if failure == nil {
					failure = e
				}
				out.Status = &s
			case "status":
				s, e := engine.Status(r.OperationID)
				out.Status = &s
				failure = e
			case "journal":
				w, e := journal.Load(r.OperationID)
				out.Window = &w
				failure = e
			default:
				failure = errors.New("fixture_action_refused")
			}
			if failure != nil {
				out.Error = failure.Error()
			}
			_ = json.NewEncoder(c).Encode(out)
		}()
	}
}
func rootFixture() error {
	if os.Geteuid() != 0 {
		return errors.New("root_fixture_requires_root")
	}
	account, err := user.Lookup("olivares-net-guard")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	_ = os.Remove(rootSocket)
	l, err := net.Listen("unix", rootSocket)
	if err != nil {
		return err
	}
	defer l.Close()
	if err := os.Chown(rootSocket, 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(rootSocket, 0660); err != nil {
		return err
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(30 * time.Second))
			var request netguard.RestoreRequest
			if err := helperschema.Decode(c, &request); err != nil {
				return
			}
			err := netguard.RunRootRestore(context.Background(), request)
			code := "root-library-returned"
			if err != nil {
				code = "root-library-unresolved"
			}
			_ = json.NewEncoder(c).Encode(map[string]string{"scope": "root-library-fixture; production-admission-unqualified", "code": code})
		}()
	}
}

// CheckRestoration measures each dynamic family of the baseline through its interface: a
// DNS query to a labelled fixture target (NETWORK_CORE_REACH_TARGETS) from one of the
// device's current addresses, bound to the device. Addresses alone prove nothing.
func (fixtureProbe) CheckRestoration(ctx context.Context, w netguard.Window, o netguard.Observation) ([]netguard.Reachability, error) {
	if !o.RuntimeUsable {
		return nil, errors.New("fixture_restoration_probe_missing")
	}
	clock, err := netguard.NewBootClock()
	if err != nil {
		return nil, err
	}
	var reach []netguard.Reachability
	for _, family := range []struct {
		method    string
		addresses []string
		v4        bool
	}{{w.Baseline.IPv4.Method, o.Runtime.IPv4.Addresses, true}, {w.Baseline.IPv6.Method, o.Runtime.IPv6.Addresses, false}} {
		if family.method != "auto" && family.method != "dhcp" {
			continue
		}
		r, err := fixtureReach(ctx, clock, w.Baseline.Interface, family.addresses, family.v4)
		if err != nil {
			return nil, err
		}
		reach = append(reach, r)
	}
	return reach, nil
}

func fixtureReach(ctx context.Context, clock *netguard.BootClock, iface string, addresses []string, v4 bool) (netguard.Reachability, error) {
	for _, value := range strings.Split(os.Getenv("NETWORK_CORE_REACH_TARGETS"), ",") {
		target, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || target.Is4() != v4 {
			continue
		}
		for _, a := range addresses {
			prefix, err := netip.ParsePrefix(a)
			if err != nil || prefix.Addr().Is4() != v4 {
				continue
			}
			if dnsAnswers(ctx, iface, prefix.Addr(), target) != nil {
				continue
			}
			boot, now, err := clock.Now()
			if err != nil {
				return netguard.Reachability{}, err
			}
			return netguard.Reachability{Interface: iface, Source: prefix.Addr().String(), Target: target.String(), BootID: boot, At: now}, nil
		}
	}
	return netguard.Reachability{}, errors.New("fixture_reachability_unmeasured")
}

// dnsAnswers sends one DNS question through iface from source and requires the answer.
func dnsAnswers(ctx context.Context, iface string, source, target netip.Addr) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	bind := func(_, _ string, raw syscall.RawConn) error {
		var inner error
		if err := raw.Control(func(fd uintptr) {
			inner = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
		}); err != nil {
			return err
		}
		return inner
	}
	d := net.Dialer{LocalAddr: &net.UDPAddr{IP: source.AsSlice()}, Control: bind}
	c, err := d.DialContext(ctx, "udp", net.JoinHostPort(target.String(), "53"))
	if err != nil {
		return err
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	c.SetDeadline(deadline)
	question := []byte{0x4e, 0x43, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	if _, err := c.Write(question); err != nil {
		return err
	}
	answer := make([]byte, 512)
	n, err := c.Read(answer)
	if err != nil {
		return err
	}
	if n < 12 || answer[0] != question[0] || answer[1] != question[1] || answer[2]&0x80 == 0 {
		return errors.New("fixture_dns_answer_invalid")
	}
	return nil
}
