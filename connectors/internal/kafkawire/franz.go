// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kafkawire

import (
	"context"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// This file is the ONLY place franz-go is used. The real, pure-Go Kafka client —
// KRaft, SASL (PLAIN/SCRAM), TLS/mTLS, consumer-group, producer, admin metadata —
// behind the Consumer/Producer seams so the rest of every connector, and all CI
// tests, run without franz-go's network path. The pure config→options mapping
// (Opts) IS unit-tested offline; the live dial is integration-tested only.

// Opts maps the wire Config to franz-go client options shared by consumer and
// producer: seed brokers, SASL mechanism, TLS/mTLS. Pure (no I/O), so it is
// unit-tested without dialing.
func Opts(c Config) ([]kgo.Opt, error) {
	opts := []kgo.Opt{kgo.SeedBrokers(c.Brokers...)}
	if c.TLS != nil {
		opts = append(opts, kgo.DialTLSConfig(c.TLS))
	}
	switch c.SASLMech {
	case "":
		// no SASL
	case "plain":
		opts = append(opts, kgo.SASL(plain.Auth{User: c.SASLUser, Pass: c.SASLPass}.AsMechanism()))
	case "scram-sha-256":
		opts = append(opts, kgo.SASL(scram.Auth{User: c.SASLUser, Pass: c.SASLPass}.AsSha256Mechanism()))
	case "scram-sha-512":
		opts = append(opts, kgo.SASL(scram.Auth{User: c.SASLUser, Pass: c.SASLPass}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("kafkawire: unsupported sasl mechanism %q", c.SASLMech)
	}
	return opts, nil
}

type franzConsumer struct {
	cl         *kgo.Client
	clusterRef string
}

// NewConsumer dials the cluster as a consumer-group member.
func NewConsumer(c Config) (Consumer, error) {
	opts, err := Opts(c)
	if err != nil {
		return nil, err
	}
	if c.Group != "" {
		opts = append(opts, kgo.ConsumerGroup(c.Group))
	}
	if len(c.Topics) > 0 {
		opts = append(opts, kgo.ConsumeTopics(c.Topics...))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafkawire: new consumer: %w", err)
	}
	return &franzConsumer{cl: cl, clusterRef: c.ClusterRef}, nil
}

func (f *franzConsumer) Poll(ctx context.Context) ([]Record, error) {
	fetches := f.cl.PollFetches(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errs := fetches.Errors(); len(errs) > 0 {
		return nil, fmt.Errorf("kafkawire: fetch: %w", errs[0].Err)
	}
	var out []Record
	fetches.EachRecord(func(r *kgo.Record) {
		headers := make(map[string][]byte, len(r.Headers))
		for _, h := range r.Headers {
			headers[h.Key] = h.Value
		}
		out = append(out, Record{
			Topic:     r.Topic,
			Partition: r.Partition,
			Offset:    r.Offset,
			Timestamp: r.Timestamp,
			Key:       r.Key,
			Value:     r.Value,
			Headers:   headers,
		})
	})
	return out, nil
}

// Topology returns a read-only snapshot of topics and consumer groups via admin
// metadata (plain kmsg over the existing client; no kadm dependency). Best-effort:
// a group whose assignment cannot be decoded yields its id and member count without
// topic subscriptions, never an error.
func (f *franzConsumer) Topology(ctx context.Context) (Topology, error) {
	t := Topology{ClusterRef: f.clusterRef}

	metaReq := kmsg.NewPtrMetadataRequest()
	metaReq.Topics = nil // nil ⇒ all topics
	metaResp, err := metaReq.RequestWith(ctx, f.cl)
	if err != nil {
		return Topology{}, fmt.Errorf("kafkawire: metadata: %w", err)
	}
	for _, mt := range metaResp.Topics {
		if mt.Topic != nil && *mt.Topic != "" {
			t.Topics = append(t.Topics, *mt.Topic)
		}
	}

	listReq := kmsg.NewPtrListGroupsRequest()
	listResp, err := listReq.RequestWith(ctx, f.cl)
	if err != nil {
		return t, nil // topics still useful
	}
	var groupIDs []string
	for _, g := range listResp.Groups {
		groupIDs = append(groupIDs, g.Group)
	}
	if len(groupIDs) == 0 {
		return t, nil
	}

	descReq := kmsg.NewPtrDescribeGroupsRequest()
	descReq.Groups = groupIDs
	descResp, err := descReq.RequestWith(ctx, f.cl)
	if err != nil {
		return t, nil
	}
	for _, g := range descResp.Groups {
		t.Groups = append(t.Groups, GroupInfo{
			Group:   g.Group,
			Members: len(g.Members),
			Topics:  topicsFromMembers(g.Members),
		})
	}
	return t, nil
}

func topicsFromMembers(members []kmsg.DescribeGroupsResponseGroupMember) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range members {
		if len(m.MemberAssignment) == 0 {
			continue
		}
		a := kmsg.NewConsumerMemberAssignment()
		if err := a.ReadFrom(m.MemberAssignment); err != nil {
			continue
		}
		for _, at := range a.Topics {
			if _, ok := seen[at.Topic]; ok {
				continue
			}
			seen[at.Topic] = struct{}{}
			out = append(out, at.Topic)
		}
	}
	return out
}

func (f *franzConsumer) Close() {
	if f.cl != nil {
		f.cl.Close()
	}
}

type franzProducer struct{ cl *kgo.Client }

// NewProducer dials the cluster as a producer.
func NewProducer(c Config) (Producer, error) {
	opts, err := Opts(c)
	if err != nil {
		return nil, err
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafkawire: new producer: %w", err)
	}
	return &franzProducer{cl: cl}, nil
}

func (f *franzProducer) Produce(ctx context.Context, topic string, key, value []byte, headers map[string][]byte) error {
	rec := &kgo.Record{Topic: topic, Key: key, Value: value}
	for k, v := range headers {
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: v})
	}
	if err := f.cl.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("kafkawire: produce to %s: %w", topic, err)
	}
	return nil
}

func (f *franzProducer) Close() {
	if f.cl != nil {
		f.cl.Close()
	}
}

var (
	_ Consumer = (*franzConsumer)(nil)
	_ Producer = (*franzProducer)(nil)
)
