// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudqueue

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// --- fixtures -----------------------------------------------------------------

// awsQueryServer routes AWS Query/XML requests by their Action parameter and
// EventBridge JSON 1.1 by its X-Amz-Target header. It returns canned bodies. Each
// per-Action handler may be nil (then the request 500s) so a test can make exactly
// one service fail.
type awsQueryServer struct {
	listQueues  string // ListQueues XML
	listTopics  string // ListTopics XML
	listSubs    string // ListSubscriptionsByTopic XML (one body for any topic)
	listBuses   string // ListEventBuses JSON
	failQueues  bool
	failTopics  bool
	failBuses   bool
	gotMessage  string // captured SNS Publish Message (egress)
	gotTopicArn string // captured SNS Publish TopicArn (egress)
}

func (h *awsQueryServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// EventBridge JSON 1.1.
	if t := r.Header.Get("X-Amz-Target"); t == eventBridgeListBuses {
		if h.failBuses {
			http.Error(w, "boom-eventbridge", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", eventBridgeContentType)
		io.WriteString(w, h.listBuses)
		return
	}

	// Query protocol: action is in the query (GET) or the form body (POST publish).
	_ = r.ParseForm()
	action := r.Form.Get("Action")
	switch action {
	case "ListQueues":
		if h.failQueues {
			http.Error(w, "boom-sqs", http.StatusInternalServerError)
			return
		}
		xmlReply(w, h.listQueues)
	case "ListTopics":
		if h.failTopics {
			http.Error(w, "boom-sns", http.StatusInternalServerError)
			return
		}
		xmlReply(w, h.listTopics)
	case "ListSubscriptionsByTopic":
		xmlReply(w, h.listSubs)
	case "Publish":
		h.gotMessage = r.Form.Get("Message")
		h.gotTopicArn = r.Form.Get("TopicArn")
		xmlReply(w, `<PublishResponse><PublishResult><MessageId>m-1</MessageId></PublishResult></PublishResponse>`)
	default:
		http.Error(w, "unknown action "+action, http.StatusBadRequest)
	}
}

func xmlReply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/xml")
	io.WriteString(w, body)
}

// gcpPubSubServer routes Pub/Sub REST requests by path. publish captures the body.
type gcpPubSubServer struct {
	topics     string // topics list JSON
	subs       string // subscriptions list JSON
	failTopics bool
	failSubs   bool
	gotPublish []byte // captured publish body
}

func (h *gcpPubSubServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, ":publish"):
		b, _ := io.ReadAll(r.Body)
		h.gotPublish = b
		jsonReply(w, `{"messageIds":["m-1"]}`)
	case strings.HasSuffix(r.URL.Path, "/topics"):
		if h.failTopics {
			http.Error(w, "boom-topics", http.StatusInternalServerError)
			return
		}
		jsonReply(w, h.topics)
	case strings.HasSuffix(r.URL.Path, "/subscriptions"):
		if h.failSubs {
			http.Error(w, "boom-subs", http.StatusInternalServerError)
			return
		}
		jsonReply(w, h.subs)
	default:
		http.Error(w, "unknown path "+r.URL.Path, http.StatusNotFound)
	}
}

func jsonReply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, body)
}

// canned bodies ---------------------------------------------------------------

const sqsXML = `<ListQueuesResponse><ListQueuesResult>` +
	`<QueueUrl>https://sqs.us-east-1.amazonaws.com/123456789012/orders</QueueUrl>` +
	`<QueueUrl>https://sqs.us-east-1.amazonaws.com/123456789012/billing</QueueUrl>` +
	`</ListQueuesResult></ListQueuesResponse>`

const snsTopicsXML = `<ListTopicsResponse><ListTopicsResult><Topics>` +
	`<member><TopicArn>arn:aws:sns:us-east-1:123456789012:alerts</TopicArn></member>` +
	`</Topics></ListTopicsResult></ListTopicsResponse>`

const snsSubsXML = `<ListSubscriptionsByTopicResponse><ListSubscriptionsByTopicResult><Subscriptions>` +
	`<member><SubscriptionArn>arn:aws:sns:us-east-1:123456789012:alerts:abc-123</SubscriptionArn>` +
	`<Protocol>sqs</Protocol><Endpoint>arn:aws:sqs:us-east-1:123456789012:billing</Endpoint></member>` +
	`</Subscriptions></ListSubscriptionsByTopicResult></ListSubscriptionsByTopicResponse>`

const ebJSON = `{"EventBuses":[` +
	`{"Name":"default","Arn":"arn:aws:events:us-east-1:123456789012:event-bus/default"},` +
	`{"Name":"app-bus","Arn":"arn:aws:events:us-east-1:123456789012:event-bus/app-bus"}]}`

const pubsubTopicsJSON = `{"topics":[` +
	`{"name":"projects/demo/topics/ingest"},` +
	`{"name":"projects/demo/topics/audit"}]}`

const pubsubSubsJSON = `{"subscriptions":[` +
	`{"name":"projects/demo/subscriptions/ingest-worker","topic":"projects/demo/topics/ingest"},` +
	`{"name":"projects/demo/subscriptions/orphan","topic":"_deleted-topic_"}]}`

// --- helpers ------------------------------------------------------------------

func openAWSSource(t *testing.T, url string, extra map[string]string) *Source {
	t.Helper()
	t.Setenv(envAccessKeyID, "")
	t.Setenv(envSecretAccessKey, "")
	t.Setenv(envSessionToken, "")
	settings := map[string]string{
		cfgProvider:      providerAWS,
		cfgAccountID:     "123456789012",
		cfgSQSEndpoint:   url,
		cfgSNSEndpoint:   url,
		cfgEvBridgeEndpt: url,
	}
	for k, v := range awsTestCreds {
		settings[k] = v
	}
	for k, v := range extra {
		settings[k] = v
	}
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func openGCPSource(t *testing.T, url string, extra map[string]string) *Source {
	t.Helper()
	settings := map[string]string{
		cfgProvider:       providerGCP,
		cfgProject:        "demo",
		cfgAccessToken:    gcpTestToken,
		cfgPubSubEndpoint: url,
	}
	for k, v := range extra {
		settings[k] = v
	}
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func edgeKey(e model.EdgeObservation) string {
	return e.OriginKind + "|" + e.OriginRef + "|" + e.ResourceKind + "|" + e.ResourceRef +
		"|" + string(e.Mode) + "|" + string(e.Source)
}

// --- Descriptor / config tests ------------------------------------------------

func TestDescriptors(t *testing.T) {
	src := New().Descriptor()
	if src.Name != SourceName || src.Type != sdk.TypeSource || src.APIVersion != sdk.APIVersion {
		t.Fatalf("source descriptor: %+v", src)
	}
	out := NewOutput().Descriptor()
	if out.Name != OutputName || out.Type != sdk.TypeOutput {
		t.Fatalf("output descriptor: %+v", out)
	}
	if src.Name == out.Name {
		t.Fatal("source and output must have distinct names")
	}
	// Every credential field is Secret.
	secretKeys := map[string]bool{cfgAccessKeyID: true, cfgSecretAccessKey: true, cfgSessionToken: true, cfgAccessToken: true}
	for _, d := range []sdk.Descriptor{src, out} {
		for _, f := range d.ConfigFields {
			if secretKeys[f.Key] && !f.Secret {
				t.Fatalf("%s field %q must be Secret", d.Name, f.Key)
			}
		}
	}
}

func TestOpenMissingProvider(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestOpenUnsupportedProvider(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{cfgProvider: "azure"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported-provider error, got %v", err)
	}
}

func TestOpenAWSMissingCredentials(t *testing.T) {
	t.Setenv(envAccessKeyID, "")
	t.Setenv(envSecretAccessKey, "")
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{cfgProvider: providerAWS}})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credentials error, got %v", err)
	}
}

func TestOpenAWSDisabledNoCredentials(t *testing.T) {
	t.Setenv(envAccessKeyID, "")
	t.Setenv(envSecretAccessKey, "")
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgProvider:       providerAWS,
		cfgEnableSQS:      "false",
		cfgEnableSNS:      "false",
		cfgEnableEvBridge: "false",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("all-disabled AWS Open should succeed: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.edges())+len(sink.findings()) != 0 {
		t.Fatalf("disabled connector emitted: %+v", sink.obs)
	}
}

func TestOpenGCPMissingProject(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgProvider: providerGCP, cfgAccessToken: gcpTestToken,
	}})
	if err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("expected project error, got %v", err)
	}
}

func TestOpenGCPMissingToken(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgProvider: providerGCP, cfgProject: "demo",
	}})
	if err == nil || !strings.Contains(err.Error(), "access_token") {
		t.Fatalf("expected access_token error, got %v", err)
	}
}

func TestOutputMissingTarget(t *testing.T) {
	o := NewOutput()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgProvider: providerAWS, cfgAccessKeyID: "AKIDEXAMPLE", cfgSecretAccessKey: testSecret,
	}})
	if err == nil || !strings.Contains(err.Error(), "egress_target") {
		t.Fatalf("expected egress_target error, got %v", err)
	}
}

// --- AWS topology end-to-end --------------------------------------------------

func TestGatherAWSEndToEnd(t *testing.T) {
	h := &awsQueryServer{listQueues: sqsXML, listTopics: snsTopicsXML, listSubs: snsSubsXML, listBuses: ebJSON}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openAWSSource(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}

	got := map[string]bool{}
	var sawSQS, sawSNS, sawEB bool
	for _, e := range sink.edges() {
		got[edgeKey(e)] = true
		switch e.Source {
		case brokerobs.SignalSQS:
			sawSQS = true
		case brokerobs.SignalSNS:
			sawSNS = true
		case brokerobs.SignalEventBridge:
			sawEB = true
		}
		if e.ObservedAt.IsZero() {
			t.Fatalf("edge has zero ObservedAt: %+v", e)
		}
		if e.Confidence != model.ConfidenceAttributed {
			t.Fatalf("topology edge confidence should be attributed: %+v", e)
		}
	}
	if !sawSQS || !sawSNS || !sawEB {
		t.Fatalf("missing a signal: sqs=%v sns=%v eb=%v", sawSQS, sawSNS, sawEB)
	}

	want := []string{
		"aws.account|123456789012|sqs.queue|https://sqs.us-east-1.amazonaws.com/123456789012/orders|unknown|sqs",
		"aws.account|123456789012|sqs.queue|https://sqs.us-east-1.amazonaws.com/123456789012/billing|unknown|sqs",
		"aws.account|123456789012|sns.topic|arn:aws:sns:us-east-1:123456789012:alerts|unknown|sns",
		"sns.topic|arn:aws:sns:us-east-1:123456789012:alerts|sns.subscription|arn:aws:sns:us-east-1:123456789012:alerts:abc-123|unknown|sns",
		"aws.account|123456789012|eventbridge.bus|arn:aws:events:us-east-1:123456789012:event-bus/default|unknown|eventbridge",
		"aws.account|123456789012|eventbridge.bus|arn:aws:events:us-east-1:123456789012:event-bus/app-bus|unknown|eventbridge",
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("missing expected edge:\n  %s\ngot edges:\n  %v", w, keysOf(got))
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNoMessageBodyEverEmitted proves no edge carries message content: a poisoned
// queue URL containing a credential shape is redacted, and the literal SQS message
// fixtures are never present in any ref (we never call ReceiveMessage).
func TestNoSecretOrBodyInEdges(t *testing.T) {
	// A queue URL that embeds an AWS access key shape — a worst case.
	const leakKey = "AKIAIOSFODNN7EXAMPLE"
	poisoned := `<ListQueuesResponse><ListQueuesResult>` +
		`<QueueUrl>https://sqs.us-east-1.amazonaws.com/123456789012/` + leakKey + `</QueueUrl>` +
		`</ListQueuesResult></ListQueuesResponse>`
	h := &awsQueryServer{listQueues: poisoned, listTopics: snsTopicsXML, listSubs: snsSubsXML, listBuses: ebJSON}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openAWSSource(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, e := range sink.edges() {
		if strings.Contains(e.ResourceRef, leakKey) || strings.Contains(e.OriginRef, leakKey) {
			t.Fatalf("raw secret survived in edge: %+v", e)
		}
		if strings.Contains(e.OriginRef, testSecret) || strings.Contains(e.ResourceRef, testSecret) {
			t.Fatalf("credential leaked into edge: %+v", e)
		}
	}
	// The redaction marker must be present in the one poisoned queue edge.
	var sawRedaction bool
	marker := strings.TrimSuffix(redact.Placeholder, "]")
	for _, e := range sink.edges() {
		if e.ResourceKind == resSQSQueue && strings.Contains(e.ResourceRef, marker) {
			sawRedaction = true
		}
	}
	if !sawRedaction {
		t.Fatal("expected redaction marker in the poisoned queue ref")
	}
}

// TestHealthFindingOnServiceFailure proves an enabled-but-failing service yields
// exactly one health finding (hashed detail) and the OTHER services still run.
func TestHealthFindingOnServiceFailure(t *testing.T) {
	h := &awsQueryServer{
		failQueues: true, // SQS 500 ⇒ one finding
		listTopics: snsTopicsXML, listSubs: snsSubsXML, listBuses: ebJSON,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openAWSSource(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather should not fail on a service error: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("expected exactly 1 finding, got %d: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Kind != "health" || f.SubjectKind != subjectSQS || f.Severity != model.SeverityMedium {
		t.Fatalf("wrong finding: %+v", f)
	}
	if len(f.DetailHash) != 64 || strings.Contains(f.DetailHash, "boom") {
		t.Fatalf("detail must be hashed, got %q", f.DetailHash)
	}
	// The other services still produced edges.
	var sawSNS, sawEB bool
	for _, e := range sink.edges() {
		if e.Source == brokerobs.SignalSNS {
			sawSNS = true
		}
		if e.Source == brokerobs.SignalEventBridge {
			sawEB = true
		}
	}
	if !sawSNS || !sawEB {
		t.Fatalf("a failing SQS aborted the pass: sns=%v eb=%v", sawSNS, sawEB)
	}
}

// TestDisabledServiceNoFinding proves a DISABLED service is skipped silently (no
// finding), distinguishing "not present" from "failing".
func TestDisabledServiceNoFinding(t *testing.T) {
	h := &awsQueryServer{listTopics: snsTopicsXML, listSubs: snsSubsXML}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openAWSSource(t, srv.URL, map[string]string{
		cfgEnableSQS:      "false",
		cfgEnableEvBridge: "false",
	})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("disabled service must not produce a finding: %+v", sink.findings())
	}
	if len(sink.edges()) == 0 {
		t.Fatal("SNS edges missing")
	}
}

func TestGatherCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := openAWSSource(t, srv.URL, map[string]string{cfgEnableSNS: "false", cfgEnableEvBridge: "false"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, &fakeSink{}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Gather did not return promptly after cancellation")
	}
}

// --- GCP topology -------------------------------------------------------------

func TestGatherGCPEndToEnd(t *testing.T) {
	h := &gcpPubSubServer{topics: pubsubTopicsJSON, subs: pubsubSubsJSON}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openGCPSource(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}
	got := map[string]bool{}
	for _, e := range sink.edges() {
		got[edgeKey(e)] = true
		if e.Source != brokerobs.SignalPubSub {
			t.Fatalf("expected pubsub signal: %+v", e)
		}
		// The bearer token must never reach an edge.
		if strings.Contains(e.OriginRef, gcpTestToken) || strings.Contains(e.ResourceRef, gcpTestToken) {
			t.Fatalf("access token leaked into edge: %+v", e)
		}
	}
	want := []string{
		"gcp.project|demo|pubsub.topic|projects/demo/topics/ingest|unknown|pubsub",
		"gcp.project|demo|pubsub.topic|projects/demo/topics/audit|unknown|pubsub",
		"pubsub.topic|projects/demo/topics/ingest|pubsub.subscription|projects/demo/subscriptions/ingest-worker|read|pubsub",
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("missing expected edge:\n  %s\ngot:\n  %v", w, keysOf(got))
		}
	}
	// The orphan subscription (deleted topic) is dropped — no edge for it.
	for k := range got {
		if strings.Contains(k, "orphan") {
			t.Fatalf("orphan subscription should be dropped, got edge %q", k)
		}
	}
}

func TestGCPHealthFinding(t *testing.T) {
	h := &gcpPubSubServer{failTopics: true, subs: pubsubSubsJSON}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openGCPSource(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectPubSub || fs[0].Severity != model.SeverityMedium {
		t.Fatalf("expected one pubsub health finding, got %+v", fs)
	}
	if len(fs[0].DetailHash) != 64 {
		t.Fatalf("detail must be hashed: %q", fs[0].DetailHash)
	}
	// Subscriptions still ran (the topics failure did not abort the pass).
	if len(sink.edges()) == 0 {
		t.Fatal("subscription edges missing; topics failure aborted the pass")
	}
}

// --- Egress (CloudEvents publish) ---------------------------------------------

func TestEgressAWSPublishesCloudEvent(t *testing.T) {
	h := &awsQueryServer{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOutput()
	t.Setenv(envAccessKeyID, "")
	t.Setenv(envSecretAccessKey, "")
	cfg := sdk.Config{Settings: map[string]string{
		cfgProvider:        providerAWS,
		cfgSNSEndpoint:     srv.URL,
		cfgAccessKeyID:     "AKIDEXAMPLE",
		cfgSecretAccessKey: testSecret,
		cfgEgressTarget:    "arn:aws:sns:us-east-1:123456789012:control",
	}}
	if err := o.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	n := sdk.Notification{
		Type: "finding.reported", Title: "drift detected", Body: "an access exceeded policy",
		Severity: model.SeverityHigh, Tenant: "acme", Time: time.Now().UTC(),
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if h.gotTopicArn != "arn:aws:sns:us-east-1:123456789012:control" {
		t.Fatalf("wrong TopicArn: %q", h.gotTopicArn)
	}
	assertCloudEvent(t, []byte(h.gotMessage), "ai.olivares.finding.reported", "high")
	if strings.Contains(h.gotMessage, testSecret) {
		t.Fatal("credential leaked into published CloudEvent")
	}
}

func TestEgressGCPPublishesCloudEvent(t *testing.T) {
	h := &gcpPubSubServer{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOutput()
	cfg := sdk.Config{Settings: map[string]string{
		cfgProvider:       providerGCP,
		cfgProject:        "demo",
		cfgAccessToken:    gcpTestToken,
		cfgPubSubEndpoint: srv.URL,
		cfgEgressTarget:   "control",
	}}
	if err := o.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	n := sdk.Notification{Type: "finding.reported", Title: "drift", Severity: model.SeverityHigh, Time: time.Now().UTC()}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// The published body is {messages:[{data: base64(cloudevents-json)}]}.
	var req pubsubPublishRequest
	if err := json.Unmarshal(h.gotPublish, &req); err != nil {
		t.Fatalf("publish body not JSON: %v (%s)", err, h.gotPublish)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("expected one message, got %d", len(req.Messages))
	}
	decoded, err := base64.StdEncoding.DecodeString(req.Messages[0].Data)
	if err != nil {
		t.Fatalf("data not base64: %v", err)
	}
	assertCloudEvent(t, decoded, "ai.olivares.finding.reported", "high")
	if strings.Contains(string(h.gotPublish), gcpTestToken) {
		t.Fatal("access token leaked into published body")
	}
}

// assertCloudEvent parses doc back through cloudevents.Parse and checks its type and
// severity extension — proving the egress body is a valid CloudEvent, not opaque.
func assertCloudEvent(t *testing.T, doc []byte, wantType, wantSeverity string) {
	t.Helper()
	ev, err := cloudevents.Parse(doc)
	if err != nil {
		t.Fatalf("published body is not a valid CloudEvent: %v (%s)", err, doc)
	}
	if ev.Type != wantType {
		t.Fatalf("CloudEvent type = %q, want %q", ev.Type, wantType)
	}
	if ev.ID == "" || ev.Source == "" {
		t.Fatalf("CloudEvent missing id/source: %+v", ev)
	}
	if ev.Extensions["severity"] != wantSeverity {
		t.Fatalf("CloudEvent severity extension = %q, want %q", ev.Extensions["severity"], wantSeverity)
	}
}
