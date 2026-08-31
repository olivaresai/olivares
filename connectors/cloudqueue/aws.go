// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudqueue

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// AWS Query/JSON API versions.
const (
	sqsAPIVersion = "2012-11-05"
	snsAPIVersion = "2010-03-31"
)

// eventBridgeContentType is the AWS JSON 1.1 content type EventBridge expects.
const eventBridgeContentType = "application/x-amz-json-1.1"

// eventBridgeListBuses is the X-Amz-Target for ListEventBuses (AWS JSON 1.1).
const eventBridgeListBuses = "AWSEvents.ListEventBuses"

// gatherAWS runs one AWS topology pass over the enabled services (SQS, then SNS,
// then EventBridge). Each enabled service that errors yields exactly one health
// finding and the pass continues. ctx is honored before each service.
func (s *Source) gatherAWS(ctx context.Context, sink sdk.Sink, at time.Time) error {
	origin := s.cfg.originRef()

	if s.cfg.enableSQS {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherSQS(ctx, sink, origin, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectSQS, origin, "AWS SQS topology failed", err, at)); e != nil {
				return e
			}
		}
	}

	if s.cfg.enableSNS {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherSNS(ctx, sink, origin, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectSNS, origin, "AWS SNS topology failed", err, at)); e != nil {
				return e
			}
		}
	}

	if s.cfg.enableEvB {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherEventBridge(ctx, sink, origin, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectEventBridge, origin, "AWS EventBridge topology failed", err, at)); e != nil {
				return e
			}
		}
	}
	return nil
}

// --- SQS ----------------------------------------------------------------------

// listQueuesEnvelope decodes the ListQueues Query/XML response. Only the queue URLs
// are read — never a message or queue attribute beyond the URL identity.
type listQueuesEnvelope struct {
	XMLName xml.Name `xml:"ListQueuesResponse"`
	Result  struct {
		QueueURLs []string `xml:"QueueUrl"`
	} `xml:"ListQueuesResult"`
}

// gatherSQS lists the account's SQS queues and emits one aws.account⊳sqs.queue
// topology edge per queue, in deterministic (sorted) order.
func (s *Source) gatherSQS(ctx context.Context, sink sdk.Sink, origin string, at time.Time) error {
	q := url.Values{"Action": {"ListQueues"}, "Version": {sqsAPIVersion}}
	var env listQueuesEnvelope
	if err := s.awsQueryGet(ctx, s.cfg.sqsEndpoint, sqsService, q, &env); err != nil {
		return err
	}
	urls := append([]string(nil), env.Result.QueueURLs...)
	sort.Strings(urls)
	for _, qu := range urls {
		qu = strings.TrimSpace(qu)
		if qu == "" {
			continue
		}
		if err := emit(ctx, sink, topologyEdge(originAWSAccount, origin, resSQSQueue, qu, brokerobs.SignalSQS, at)); err != nil {
			return err
		}
	}
	return nil
}

// --- SNS ----------------------------------------------------------------------

// listTopicsEnvelope decodes ListTopics. Only the topic ARNs are read.
type listTopicsEnvelope struct {
	XMLName xml.Name `xml:"ListTopicsResponse"`
	Result  struct {
		Topics []struct {
			TopicArn string `xml:"TopicArn"`
		} `xml:"Topics>member"`
	} `xml:"ListTopicsResult"`
}

// listSubsEnvelope decodes ListSubscriptionsByTopic. Only the subscription ARN and
// the delivery endpoint identity are read (never a message).
type listSubsEnvelope struct {
	XMLName xml.Name `xml:"ListSubscriptionsByTopicResponse"`
	Result  struct {
		Subscriptions []struct {
			SubscriptionArn string `xml:"SubscriptionArn"`
			Endpoint        string `xml:"Endpoint"`
			Protocol        string `xml:"Protocol"`
		} `xml:"Subscriptions>member"`
	} `xml:"ListSubscriptionsByTopicResult"`
}

// gatherSNS lists topics, emits one aws.account⊳sns.topic edge per topic, then
// best-effort lists each topic's subscriptions to emit sns.topic⊳sns.subscription
// fan-out edges. A subscription-listing failure is non-fatal (best effort): the
// topic edges still stand and the service is not marked unhealthy for a fan-out gap.
func (s *Source) gatherSNS(ctx context.Context, sink sdk.Sink, origin string, at time.Time) error {
	q := url.Values{"Action": {"ListTopics"}, "Version": {snsAPIVersion}}
	var env listTopicsEnvelope
	if err := s.awsQueryGet(ctx, s.cfg.snsEndpoint, snsService, q, &env); err != nil {
		return err
	}
	arns := make([]string, 0, len(env.Result.Topics))
	for _, t := range env.Result.Topics {
		if a := strings.TrimSpace(t.TopicArn); a != "" {
			arns = append(arns, a)
		}
	}
	sort.Strings(arns)
	for _, arn := range arns {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(ctx, sink, topologyEdge(originAWSAccount, origin, resSNSTopic, arn, brokerobs.SignalSNS, at)); err != nil {
			return err
		}
		// Best-effort fan-out: a failure here does not fail the SNS pass.
		if subErr := s.gatherSNSSubscriptions(ctx, sink, arn, at); subErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return nil
}

// gatherSNSSubscriptions lists one topic's subscriptions and emits a
// sns.topic⊳sns.subscription fan-out edge per subscription. The subscription is
// referenced by its ARN (its identity); the delivery endpoint and protocol are not
// emitted (an endpoint can carry a token/PII — minimal-data). Mode is unknown: a
// subscription binding is a topology fact, not a read/write.
func (s *Source) gatherSNSSubscriptions(ctx context.Context, sink sdk.Sink, topicArn string, at time.Time) error {
	q := url.Values{"Action": {"ListSubscriptionsByTopic"}, "Version": {snsAPIVersion}, "TopicArn": {topicArn}}
	var env listSubsEnvelope
	if err := s.awsQueryGet(ctx, s.cfg.snsEndpoint, snsService, q, &env); err != nil {
		return err
	}
	subs := make([]string, 0, len(env.Result.Subscriptions))
	for _, sub := range env.Result.Subscriptions {
		if a := strings.TrimSpace(sub.SubscriptionArn); a != "" && !strings.EqualFold(a, "PendingConfirmation") {
			subs = append(subs, a)
		}
	}
	sort.Strings(subs)
	for _, subArn := range subs {
		if err := emit(ctx, sink, fanoutEdge(resSNSTopic, topicArn, resSNSSubscription, subArn, model.ModeUnknown, brokerobs.SignalSNS, at)); err != nil {
			return err
		}
	}
	return nil
}

// --- EventBridge --------------------------------------------------------------

// listBusesResponse decodes the EventBridge ListEventBuses JSON response. Only the
// bus name/ARN are read.
type listBusesResponse struct {
	EventBuses []struct {
		Name string `json:"Name"`
		Arn  string `json:"Arn"`
	} `json:"EventBuses"`
}

// gatherEventBridge lists the account's event buses (AWS JSON 1.1) and emits one
// aws.account⊳eventbridge.bus edge per bus, referencing the bus by ARN when present
// (a stable global id) else by name.
func (s *Source) gatherEventBridge(ctx context.Context, sink sdk.Sink, origin string, at time.Time) error {
	var resp listBusesResponse
	if err := s.awsJSONPost(ctx, s.cfg.evbEndpoint, eventBridgeService, eventBridgeListBuses, []byte("{}"), &resp); err != nil {
		return err
	}
	refs := make([]string, 0, len(resp.EventBuses))
	for _, b := range resp.EventBuses {
		ref := strings.TrimSpace(b.Arn)
		if ref == "" {
			ref = strings.TrimSpace(b.Name)
		}
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if err := emit(ctx, sink, topologyEdge(originAWSAccount, origin, resEventBridgeBus, ref, brokerobs.SignalEventBridge, at)); err != nil {
			return err
		}
	}
	return nil
}

// --- Egress (SNS Publish) -----------------------------------------------------

// publishSNS publishes one CloudEvents document to the configured SNS topic ARN via
// the Query protocol POST (Action=Publish). The request is SigV4-signed; the
// CloudEvents JSON is the Message parameter. This is the only write the connector
// issues.
func (o *Output) publishSNS(ctx context.Context, body []byte) error {
	form := url.Values{
		"Action":   {"Publish"},
		"Version":  {snsAPIVersion},
		"TopicArn": {o.cfg.egressTarget},
		"Message":  {string(body)},
	}
	encoded := form.Encode()
	endpoint := strings.TrimRight(o.cfg.snsEndpoint, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	awssig.Sign(req, []byte(encoded), snsService, o.cfg.region, o.cfg.creds, time.Now())

	resp, err := o.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudqueue egress(sns): Publish returned status %d", resp.StatusCode)
	}
	return nil
}

// --- shared AWS request helpers ----------------------------------------------

// awsQueryGet issues one SigV4-signed AWS Query-protocol GET (the action is in the
// URL query string — a read; the request carries no body) and decodes the XML
// response into out.
func (s *Source) awsQueryGet(ctx context.Context, endpoint, service string, q url.Values, out any) error {
	full := strings.TrimRight(endpoint, "/") + "/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return err
	}
	awssig.Sign(req, nil, service, s.cfg.region, s.cfg.creds, time.Now())

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudqueue(aws): %s %s returned status %d", service, q.Get("Action"), resp.StatusCode)
	}
	if err := xml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("cloudqueue(aws): decode %s %s response: %w", service, q.Get("Action"), err)
	}
	return nil
}

// awsJSONPost issues one SigV4-signed AWS JSON 1.1 POST (X-Amz-Target target, body
// payload) and decodes the JSON response into out. EventBridge mandates POST even
// for this read-only list (the body carries paging/filters); ListEventBuses
// performs NO mutation.
func (s *Source) awsJSONPost(ctx context.Context, endpoint, service, target string, payload []byte, out any) error {
	full := strings.TrimRight(endpoint, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", eventBridgeContentType)
	req.Header.Set("X-Amz-Target", target)
	awssig.Sign(req, payload, service, s.cfg.region, s.cfg.creds, time.Now())

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudqueue(aws): %s %s returned status %d", service, target, resp.StatusCode)
	}
	if err := decodeJSON(data, out); err != nil {
		return fmt.Errorf("cloudqueue(aws): decode %s response: %w", target, err)
	}
	return nil
}
