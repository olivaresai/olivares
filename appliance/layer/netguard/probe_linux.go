//go:build linux && (amd64 || arm64)

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package netguard

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProbePlan contains only targets from an already-authorized network plan. It is a
// measurement input, never proof of authority. The production helper remains closed
// until its protected plan producer and confirmation receipt verifier are composed.
type ProbePlan struct {
	Interface, Source, Gateway, Name string
	DNS                              []string
	Port                             uint16
}
type ProbeMeasurement struct {
	Interface, Source string
	Targets           []string
	BootID            string
	Started, Finished time.Duration
}

func validateProbe(uid int, p ProbePlan) error {
	bad := func() error { return errors.New("network_probe_plan_refused") }
	if uid == 0 || len(p.Interface) < 1 || len(p.Interface) > 15 || strings.ContainsAny(p.Interface, "/ :\t\n") || p.Interface == "lo" || (p.Port != 443 && p.Port != 8443) || len(p.DNS) < 1 || len(p.DNS) > 16 {
		return bad()
	}
	source, err := netip.ParseAddr(p.Source)
	if err != nil || !unicast(source) {
		return bad()
	}
	for _, value := range append(append([]string{}, p.DNS...), p.Gateway) {
		a, err := netip.ParseAddr(value)
		if err != nil || !unicast(a) || a.Is4() != source.Is4() {
			return bad()
		}
	}
	if len(p.Name) < 1 || len(p.Name) > 253 {
		return bad()
	}
	for _, label := range strings.Split(p.Name, ".") {
		if len(label) < 1 || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" || label[0] == '-' || label[len(label)-1] == '-' {
			return bad()
		}
	}
	return nil
}

// MeasureProbe uses the actual unprivileged account and binds every socket to the
// plan's interface and source. It never returns a confirmation receipt or token.
func MeasureProbe(ctx context.Context, p ProbePlan) (ProbeMeasurement, error) {
	var result ProbeMeasurement
	if err := validateProbe(os.Geteuid(), p); err != nil {
		return result, err
	}
	iface, err := net.InterfaceByName(p.Interface)
	if err != nil {
		return result, err
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return result, err
	}
	found := false
	for _, a := range addresses {
		prefix, err := netip.ParsePrefix(a.String())
		if err == nil && prefix.Addr().String() == p.Source {
			found = true
		}
	}
	if !found {
		return result, errors.New("network_probe_source_unmeasured")
	}
	clock, err := NewBootClock()
	if err != nil {
		return result, err
	}
	result.BootID, result.Started, err = clock.Now()
	if err != nil {
		return result, err
	}
	result.Interface = p.Interface
	result.Source = p.Source
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	bind := func(_, _ string, raw syscall.RawConn) error {
		var inner error
		err := raw.Control(func(fd uintptr) {
			inner = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, p.Interface)
		})
		if err != nil {
			return err
		}
		return inner
	}
	source := net.ParseIP(p.Source)
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: source}, Control: bind}
	gateway := net.JoinHostPort(p.Gateway, strconv.Itoa(int(p.Port)))
	conn, err := d.DialContext(ctx, "tcp", gateway)
	if err != nil {
		return result, errors.New("network_probe_gateway_unreachable")
	}
	conn.Close()
	result.Targets = append(result.Targets, gateway)
	for _, server := range p.DNS {
		d.LocalAddr = &net.UDPAddr{IP: source}
		target := net.JoinHostPort(server, "53")
		conn, err := d.DialContext(ctx, "udp", target)
		if err != nil {
			return result, errors.New("network_probe_dns_unreachable")
		}
		deadline, _ := ctx.Deadline()
		conn.SetDeadline(deadline)
		question := make([]byte, 12)
		if _, err := rand.Read(question[:2]); err != nil {
			conn.Close()
			return result, err
		}
		question[2] = 1
		question[5] = 1
		for _, label := range strings.Split(p.Name, ".") {
			question = append(question, byte(len(label)))
			question = append(question, label...)
		}
		question = append(question, 0, 0, 1, 0, 1)
		if source.To4() == nil {
			question[len(question)-3] = 28
		}
		if _, err := conn.Write(question); err != nil {
			conn.Close()
			return result, errors.New("network_probe_dns_unreachable")
		}
		answer := make([]byte, 4097)
		n, err := conn.Read(answer)
		conn.Close()
		if err != nil || n < len(question) || n > 4096 || !bytes.Equal(answer[:2], question[:2]) || answer[2]&0x80 == 0 || answer[2]&0x02 != 0 || answer[3]&15 != 0 || binary.BigEndian.Uint16(answer[4:6]) != 1 || binary.BigEndian.Uint16(answer[6:8]) == 0 || !bytes.Equal(answer[12:len(question)], question[12:]) {
			return result, errors.New("network_probe_dns_resolution_failed")
		}
		result.Targets = append(result.Targets, target)
	}
	_, result.Finished, err = clock.Now()
	return result, err
}
