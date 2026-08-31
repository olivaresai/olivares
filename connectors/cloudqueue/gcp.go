// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudqueue

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// gatherGCP runs one GCP Pub/Sub topology pass: list topics (project⊳topic edges),
// then list subscriptions (topic⊳subscription read edges). A failure of either call
// yields one health finding and the pass continues with the next. ctx is honored
// before each call.
func (s *Source) gatherGCP(ctx context.Context, sink sdk.Sink, at time.Time) error {
	origin := s.cfg.originRef()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.gatherPubSubTopics(ctx, sink, origin, at); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e := emit(ctx, sink, healthFinding(subjectPubSub, origin, "GCP Pub/Sub topics topology failed", err, at)); e != nil {
			return e
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.gatherPubSubSubscriptions(ctx, sink, at); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e := emit(ctx, sink, healthFinding(subjectPubSub, origin, "GCP Pub/Sub subscriptions topology failed", err, at)); e != nil {
			return e
		}
	}
	return nil
}

// pubsubTopicsResponse decodes GET /v1/projects/<p>/topics. Only the resource name
// is read.
type pubsubTopicsResponse struct {
	Topics []struct {
		Name string `json:"name"`
	} `json:"topics"`
	NextPageToken string `json:"nextPageToken"`
}

// pubsubSubsResponse decodes GET /v1/projects/<p>/subscriptions. Only the
// subscription name and its parent topic are read (never a message).
type pubsubSubsResponse struct {
	Subscriptions []struct {
		Name  string `json:"name"`
		Topic string `json:"topic"`
	} `json:"subscriptions"`
	NextPageToken string `json:"nextPageToken"`
}

// gatherPubSubTopics lists the project's topics and emits one gcp.project⊳pubsub.topic
// edge per topic, in deterministic order, following nextPageToken pagination.
func (s *Source) gatherPubSubTopics(ctx context.Context, sink sdk.Sink, origin string, at time.Time) error {
	var names []string
	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		u := s.pubsubURL("/v1/projects/"+s.cfg.project+"/topics", pageToken)
		var resp pubsubTopicsResponse
		if err := s.gcpGet(ctx, u, &resp); err != nil {
			return err
		}
		for _, t := range resp.Topics {
			if n := strings.TrimSpace(t.Name); n != "" {
				names = append(names, n)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	sort.Strings(names)
	for _, n := range names {
		if err := emit(ctx, sink, topologyEdge(originGCPProject, origin, resPubSubTopic, n, brokerobs.SignalPubSub, at)); err != nil {
			return err
		}
	}
	return nil
}

// gatherPubSubSubscriptions lists the project's subscriptions and emits one
// pubsub.topic⊳pubsub.subscription edge per subscription, Mode read (a subscription
// READS its topic). A subscription whose topic is "_deleted-topic_" or empty is
// skipped (no real topology edge to draw).
func (s *Source) gatherPubSubSubscriptions(ctx context.Context, sink sdk.Sink, at time.Time) error {
	type subEdge struct{ topic, sub string }
	var edges []subEdge
	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		u := s.pubsubURL("/v1/projects/"+s.cfg.project+"/subscriptions", pageToken)
		var resp pubsubSubsResponse
		if err := s.gcpGet(ctx, u, &resp); err != nil {
			return err
		}
		for _, sub := range resp.Subscriptions {
			name := strings.TrimSpace(sub.Name)
			topic := strings.TrimSpace(sub.Topic)
			if name == "" || topic == "" || strings.HasSuffix(topic, "_deleted-topic_") {
				continue
			}
			edges = append(edges, subEdge{topic: topic, sub: name})
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].topic != edges[j].topic {
			return edges[i].topic < edges[j].topic
		}
		return edges[i].sub < edges[j].sub
	})
	for _, e := range edges {
		if err := emit(ctx, sink, fanoutEdge(resPubSubTopic, e.topic, resPubSubSub, e.sub, model.ModeRead, brokerobs.SignalPubSub, at)); err != nil {
			return err
		}
	}
	return nil
}

// --- Egress (Pub/Sub publish) -------------------------------------------------

// pubsubPublishRequest is the Pub/Sub publish body: one message whose data is the
// base64 of the CloudEvents document.
type pubsubPublishRequest struct {
	Messages []pubsubMessage `json:"messages"`
}

type pubsubMessage struct {
	Data string `json:"data"`
}

// publishPubSub publishes one CloudEvents document to the configured Pub/Sub topic
// via topics.publish (bearer-authed). The data member is base64 of the CloudEvents
// JSON — the only content published.
func (o *Output) publishPubSub(ctx context.Context, body []byte) error {
	reqBody, err := json.Marshal(pubsubPublishRequest{
		Messages: []pubsubMessage{{Data: base64.StdEncoding.EncodeToString(body)}},
	})
	if err != nil {
		return err
	}
	u := strings.TrimRight(o.cfg.pubsubEndpoint, "/") +
		"/v1/projects/" + o.cfg.project + "/topics/" + o.cfg.egressTarget + ":publish"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.cfg.accessToken)

	resp, err := o.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudqueue egress(pubsub): publish returned status %d", resp.StatusCode)
	}
	return nil
}

// --- shared GCP request helpers ----------------------------------------------

// pubsubURL builds a Pub/Sub REST URL for a path under the configured endpoint,
// appending a pageToken when paginating.
func (s *Source) pubsubURL(path, pageToken string) string {
	u := strings.TrimRight(s.cfg.pubsubEndpoint, "/") + path
	if pageToken != "" {
		u += "?pageToken=" + pageToken
	}
	return u
}

// gcpGet issues one bearer-authed GET and decodes the JSON response into out. The
// access token rides in the Authorization header only; it is never placed in the
// URL or emitted.
func (s *Source) gcpGet(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.accessToken)

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
		return fmt.Errorf("cloudqueue(gcp): GET returned status %d", resp.StatusCode)
	}
	if err := decodeJSON(data, out); err != nil {
		return fmt.Errorf("cloudqueue(gcp): decode response: %w", err)
	}
	return nil
}

// decodeJSON unmarshals JSON into out. It is the shared decoder for the AWS JSON 1.1
// and GCP REST responses, keeping a single decode policy.
func decodeJSON(data []byte, out any) error {
	return json.Unmarshal(data, out)
}
