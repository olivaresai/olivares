// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cloudqueue is the read-only managed-cloud-message-bus connector for
// Olivares AI. It is provider-pluggable (config `provider` = "aws"
// or "gcp") and does two things, never importing the engine (Apache-2.0
// boundary):
//
//   - Source (olivares.cloudqueue) — a batch, re-pollable TOPOLOGY observer. Each
//     Gather runs ONE non-destructive discovery pass over the enabled managed buses
//     via their list/describe APIs and emits minimal-data EDGE observations that say
//     which queues/topics/subscriptions/buses exist and how they fan out. The engine
//     owns re-scheduling, so the connector holds no ticker and Gather returns nil
//     when the pass completes (the batch-source contract, exactly as connectors/aws).
//   - Output (olivares.cloudqueue-egress) — a CloudEvents publisher. Notify wraps a
//     Notification in a CloudEvents 1.0 structured document and publishes it to the
//     operator-configured egress target (an SNS topic or a Pub/Sub topic). This is
//     the ONLY write the connector ever issues, and it writes only our own
//     CloudEvents — never a consumed message body.
//
// MINIMAL-DATA / READ-ONLY (docs/SECURITY-HARDENING.md,§3). The connector observes TOPOLOGY, never
// message content. It uses only list/describe (and the egress Publish); it never
// calls ReceiveMessage/Subscribe/Pull. CONSUMING A QUEUE WOULD DESTROY MESSAGES
// (SQS ReceiveMessage makes a message invisible and a delete removes it; a Pub/Sub
// pull acks and drops it) — so a destructive consume is intentionally OUT OF SCOPE.
// Topology is observed via non-destructive list/describe alone. Credentials live in
// memory only (Secret config fields), are never logged or emitted, and any error
// text that could embed a signed URL or token is hashed via redact.Hash before it
// becomes a health finding.
//
// AWS (provider=aws), all SigV4-signed via internal/awssig, region+creds from
// config or the conventional AWS_* env vars:
//
//   - SQS    — Query GET ?Action=ListQueues. Edge aws.account⊳sqs.queue (signal sqs).
//   - SNS    — Query GET ?Action=ListTopics, then best-effort
//     ListSubscriptionsByTopic. Edges aws.account⊳sns.topic and
//     sns.topic⊳sns.subscription fan-out (signal sns).
//   - EventBridge — AWS JSON 1.1 POST AWSEvents.ListEventBuses. Edge
//     aws.account⊳eventbridge.bus (signal eventbridge).
//
// GCP (provider=gcp), bearer access_token, read-only REST JSON:
//
//   - Pub/Sub topics        — GET /v1/projects/<p>/topics. Edge
//     gcp.project⊳pubsub.topic (signal pubsub).
//   - Pub/Sub subscriptions — GET /v1/projects/<p>/subscriptions. Edge
//     pubsub.topic⊳pubsub.subscription, Mode read (the subscription reads its
//     topic), signal pubsub.
//
// Each enabled service that errors yields exactly ONE health finding (Kind
// "health", Severity medium, DetailHash = redact.Hash(err)) and the pass continues
// with the next service — a gap is a signal, not silence (the precedent).
//
// FRONTIER vs the `aws` connector — no double-ingest. The `aws` connector
// does CloudTrail AUDIT: control-plane MANAGEMENT events under ResourceKind
// "aws.api" with signal "cloudtrail". THIS connector does managed-QUEUE TOPOLOGY +
// egress under ResourceKinds sqs.queue / sns.topic / sns.subscription /
// eventbridge.bus / pubsub.topic / pubsub.subscription, with the distinct signals
// sqs / sns / eventbridge / pubsub. The two are disjoint by construction — different
// APIs (ListQueues/ListTopics/ListEventBuses vs CloudTrail LookupEvents), different
// resource kinds, no graph collision — exactly as declared its own frontier
// against the datastores connector.
//
// REUSE — Azure buses are NOT re-implemented here. Azure Event Hubs speaks the
// Apache Kafka wire protocol, so it is reached via connectors/kafka;
// Azure Service Bus speaks AMQP 1.0, so it is reached via connectors/amqp
//. An operator points the kafka connector at an Event Hubs namespace
// and the amqp connector at a Service Bus namespace; this connector covers AWS
// (SQS/SNS/EventBridge) and GCP (Pub/Sub) only.
package cloudqueue
