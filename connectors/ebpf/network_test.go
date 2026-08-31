// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTupleFromArgs(t *testing.T) {
	t.Run("sock arg", func(t *testing.T) {
		args := []tetragonArg{{SockArg: &tetragonSockArg{Daddr: "93.184.216.34", Dport: 443}}}
		tup, ok := tupleFromArgs(args)
		assert.True(t, ok)
		assert.Equal(t, "93.184.216.34", tup.dstIP)
		assert.Equal(t, uint32(443), tup.dport)
	})
	t.Run("sock arg with sni", func(t *testing.T) {
		args := []tetragonArg{
			{SockArg: &tetragonSockArg{Daddr: "93.184.216.34", Dport: 443}},
			{StringArg: ptr("example.com")},
		}
		tup, ok := tupleFromArgs(args)
		assert.True(t, ok)
		assert.Equal(t, "example.com", tup.sni)
	})
	t.Run("skb fallback", func(t *testing.T) {
		args := []tetragonArg{{SkbArg: &tetragonSkbArg{Daddr: "10.0.0.5", Dport: 8080}}}
		tup, ok := tupleFromArgs(args)
		assert.True(t, ok)
		assert.Equal(t, "10.0.0.5", tup.dstIP)
		assert.Equal(t, uint32(8080), tup.dport)
	})
	t.Run("no network arg", func(t *testing.T) {
		_, ok := tupleFromArgs([]tetragonArg{{IntArg: ptr(4)}})
		assert.False(t, ok)
	})
}

func TestEndpointRef(t *testing.T) {
	assert.Equal(t, "tcp://example.com:443", netTuple{dstIP: "93.184.216.34", dport: 443, sni: "example.com"}.endpointRef())
	assert.Equal(t, "tcp://93.184.216.34:443", netTuple{dstIP: "93.184.216.34", dport: 443}.endpointRef())
	assert.Equal(t, "tcp://[2001:db8::1]:443", netTuple{dstIP: "2001:db8::1", dport: 443}.endpointRef())
	assert.Equal(t, "", netTuple{dport: 443}.endpointRef(), "no host")
	assert.Equal(t, "", netTuple{dstIP: "1.2.3.4"}.endpointRef(), "no port")
}

func TestMatchesEndpoint(t *testing.T) {
	endpoints := []string{"127.0.0.1:4317", "127.0.0.1:4318"}
	assert.True(t, netTuple{dstIP: "127.0.0.1", dport: 4317}.matchesEndpoint(endpoints))
	assert.True(t, netTuple{dstIP: "127.0.0.1", dport: 4318}.matchesEndpoint(endpoints))
	assert.False(t, netTuple{dstIP: "127.0.0.1", dport: 443}.matchesEndpoint(endpoints))
	assert.False(t, netTuple{dstIP: "10.0.0.1", dport: 4317}.matchesEndpoint(endpoints))
	assert.False(t, netTuple{dport: 4317}.matchesEndpoint(endpoints), "no ip")
}

func ptr[T any](v T) *T { return &v }
