// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The launch terms a driver DECLARES are what a console labels a launch choice
// with: the choice applies, or this tool does not use it. A declaration is
// therefore a claim about the launch, and this file holds each one to the launch
// itself.
//
// The proof surface is everything the launch hands the child: the argv and the
// environment the engine builds (buildLaunchSpec, the production path), and
// every frame the driver writes on the child's stdin through its handshake and
// its first turn. A term declared carried must reach one of them; a term
// declared not_carried must reach none of them. It is the whole handshake and
// not only its first frame because that is where the terms travel: a Codex
// model rides on thread/start, a Codex effort on turn/start, and an OpenCode
// model and effort on session/set_config_option.
//
// ⛔ A DECLARATION NOBODY CAN PROVE IS A LABEL THAT LIES, and it lies to every
// user of that driver at once. A driver that cannot show a term reaching its
// child declares it not_carried.

// The one launch every driver is held to. Each value is a token that no driver,
// no protocol and no official CLI uses for anything, so it can reach the child
// only because this launch chose it: an ordinary word such as "high" or "plan"
// could be sent by a driver of its own accord, and a term that driver dropped
// would then read as carried — the unsafe direction for a label.
const (
	launchTermsProbeModel          = "probe-model-7f3a"
	launchTermsProbeEffort         = "effort-probe-7f3a"
	launchTermsProbePermissionMode = "mode-probe-7f3a"
)

// launchTermsRow is one launch form of one driver this conformance holds to its
// launch.
type launchTermsRow struct {
	driver string
	// transport is the launch form, stream-json when empty. Only the Claude path
	// has another: remote-control, whose argv shares the governed tail, so the
	// one Claude declaration is proven on both forms.
	transport Transport
	// impl is the registered driver, nil for the Claude path, which is not one.
	impl ProviderDriver
	// answer plays the provider through the handshake and the first turn. The
	// Claude path has none: it runs no protocol handshake, so its argv and its
	// environment are the whole launch.
	answer launchTermsAnswer
}

// launchTermsRows is the ONE list of rows. The conformance runs every row, and
// TestLaunchTerms_EveryDeclarationInThisPackageHasARow proves that no type in
// this package declares launch terms without one.
func launchTermsRows() []launchTermsRow {
	return []launchTermsRow{
		{driver: providerDriverClaude},
		{driver: providerDriverClaude, transport: TransportRemoteControl},
		{driver: providerDriverCodex, impl: NewCodexDriver(), answer: launchTermsCodexAnswer},
		{driver: providerDriverGrok, impl: NewGrokDriver(), answer: launchTermsGrokAnswer},
		{driver: providerDriverOpenCode, impl: NewOpenCodeDriver(), answer: launchTermsOpenCodeAnswer},
	}
}

func TestLaunchTerms_DeclarationMatchesWhatTheLaunchCarries(t *testing.T) {
	t.Parallel()
	rows := launchTermsRows()
	opts := []Option{WithDriverTimeouts(2*time.Second, 2*time.Second)}
	for _, row := range rows {
		if row.impl != nil {
			opts = append(opts, WithProviderDriver(row.impl))
		}
	}
	m := New(opts...)
	for _, tc := range rows {
		name, transport := tc.driver, tc.transport
		if transport == "" {
			transport = TransportStreamJSON
		} else {
			name += " " + string(transport)
		}
		t.Run(name, func(t *testing.T) {
			terms, ok := m.LaunchTermsFor(tc.driver)
			if !ok {
				t.Fatalf("%s declares no launch terms", tc.driver)
			}
			// The reader closes the vocabulary for a driver built elsewhere. A driver
			// of this package must not need it: what it declares is what is read.
			if declared, ok := tc.impl.(ProviderDriverLaunchTerms); ok && declared.LaunchTerms() != terms {
				t.Errorf("%s declares %+v, outside the closed vocabulary; the reader publishes %+v",
					tc.driver, declared.LaunchTerms(), terms)
			}
			p := CreateRunParams{
				Transport:      transport,
				Model:          launchTermsProbeModel,
				Effort:         launchTermsProbeEffort,
				PermissionMode: launchTermsProbePermissionMode,
				WorkspaceDir:   "/workspace/probe",
				ProviderHome: &ProviderHomeSnapshot{
					ProfileID:  "ppf_probe",
					Driver:     tc.driver,
					ConfigHome: "/homes/probe/config",
					UserHome:   "/homes/probe/user",
					AuthSource: AuthSourceAccountHome,
				},
			}
			spec, frames := launchTermsLaunch(t, m, p, tc.answer)

			for _, term := range []struct {
				name     string
				declared TermSupport
				value    string
			}{
				{"model", terms.Model, p.Model},
				{"effort", terms.Effort, p.Effort},
				{"permission mode", terms.PermissionMode, p.PermissionMode},
			} {
				where, found := launchTermsFind(spec, frames, term.value)
				switch term.declared {
				case TermCarried:
					if !found {
						t.Errorf("%s declares the %s %s, but %q reaches neither the argv %q, the environment, nor any of the %d frames the driver sent",
							tc.driver, term.name, TermCarried, term.value, spec.Args, len(frames))
					}
				case TermNotCarried:
					if found {
						t.Errorf("%s declares the %s %s, but %q reaches the child at %s",
							tc.driver, term.name, TermNotCarried, term.value, where)
					}
				default:
					t.Errorf("%s declares the %s %q, which is neither %q nor %q",
						tc.driver, term.name, term.declared, TermCarried, TermNotCarried)
				}
			}

			switch terms.ModelDiscovery {
			case ModelDiscoveryNone:
			case ModelDiscoveryBoundCredentialProbe:
				// The probe lists the models of the provider record a profile binds, so
				// a driver discovers them this way only if a record can be bound to it.
				// An openai_compatible record serves every driver by construction and
				// proves nothing about this one, so it is not asked.
				served := false
				for _, kind := range []string{ProviderKindAnthropic, ProviderKindOpenAI, ProviderKindXAI} {
					served = served || recordServesDriver(kind, tc.driver)
				}
				if !served {
					t.Errorf("%s declares model discovery %q, but no provider record of a named kind can be bound to it",
						tc.driver, terms.ModelDiscovery)
				}
			default:
				t.Errorf("%s declares model discovery %q, which is neither %q nor %q",
					tc.driver, terms.ModelDiscovery, ModelDiscoveryNone, ModelDiscoveryBoundCredentialProbe)
			}
		})
	}
}

// A driver that publishes no launch terms is UNKNOWN, not a guess. Nothing may
// infer "this driver takes a model" from a key, from a registration or from a
// resemblance to another driver: the reader answers false, and the console says
// it does not know.
func TestLaunchTerms_UnknownDriverIsNotSupported(t *testing.T) {
	t.Parallel()
	m := New(
		WithProviderDriver(NewCodexDriver()),
		WithProviderDriver(fakeTurnDriver{}),
	)
	for _, tc := range []struct {
		name   string
		driver string
	}{
		{"a driver key nothing registered", "not-registered"},
		{"an empty driver key", ""},
		{"a known driver this node did not register", providerDriverGrok},
		{"a registered driver without the optional half", fakeTurnDriverKey},
	} {
		if terms, ok := m.LaunchTermsFor(tc.driver); ok || terms != (DriverLaunchTerms{}) {
			t.Errorf("%s: LaunchTermsFor(%q) = %+v, %v; want the zero declaration and false",
				tc.name, tc.driver, terms, ok)
		}
	}
	// The positive control: the same module answers for a registered driver that
	// does publish its terms, so the refusals above are not a reader that refuses
	// everything.
	if _, ok := m.LaunchTermsFor(providerDriverCodex); !ok {
		t.Fatal("LaunchTermsFor(codex) = false; the refusals above prove nothing")
	}
}

// launchTermsOddDriverKey registers launchTermsOddDriver.
const launchTermsOddDriverKey = "oddterms"

// launchTermsOddDriver answers the optional half with values outside the closed
// vocabulary, the way a driver built outside this package could. Everything
// else is the existing fakeTurnDriver.
type launchTermsOddDriver struct{ fakeTurnDriver }

func (launchTermsOddDriver) Key() string { return launchTermsOddDriverKey }

func (launchTermsOddDriver) LaunchTerms() DriverLaunchTerms {
	return DriverLaunchTerms{
		Model:          "Carried",
		Effort:         TermCarried,
		PermissionMode: "",
		ModelDiscovery: "lists_models_itself",
	}
}

// An answer outside the vocabulary says nothing a console can show honestly, so
// the reader publishes the safe reading of it: the term is not carried, and
// nothing lists the models. A value inside the vocabulary passes unchanged, so
// the reader is not one that answers not_carried for everything.
func TestLaunchTerms_AnswerOutsideTheVocabularyReadsAsNotCarried(t *testing.T) {
	t.Parallel()
	m := New(WithProviderDriver(launchTermsOddDriver{}))
	terms, ok := m.LaunchTermsFor(launchTermsOddDriverKey)
	if !ok {
		t.Fatalf("LaunchTermsFor(%q) = false; a registered driver with the half must answer", launchTermsOddDriverKey)
	}
	want := DriverLaunchTerms{
		Model:          TermNotCarried,
		Effort:         TermCarried,
		PermissionMode: TermNotCarried,
		ModelDiscovery: ModelDiscoveryNone,
	}
	if terms != want {
		t.Fatalf("LaunchTermsFor(%q) = %+v, want %+v", launchTermsOddDriverKey, terms, want)
	}
}

// Every type of this package that declares launch terms has a row, so no
// declaration ships unproven. The declarations are found in the SOURCE, not in
// a list kept beside the rows: a list is what a new driver forgets to join.
func TestLaunchTerms_EveryDeclarationInThisPackageHasARow(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{}
	for _, row := range launchTermsRows() {
		if row.impl == nil {
			continue
		}
		if row.impl.Key() != row.driver {
			t.Errorf("row %q holds a driver whose key is %q", row.driver, row.impl.Key())
		}
		covered[launchTermsTypeName(row.impl)] = true
	}
	declaring := launchTermsDeclaringTypes(t)
	if len(declaring) == 0 {
		t.Fatal("the source scan found no LaunchTerms method; it is not measuring anything")
	}
	for _, name := range declaring {
		if !covered[name] {
			t.Errorf("%s declares launch terms and has no row in launchTermsRows; its declaration would ship unproven", name)
		}
	}
}

// launchTermsTypeName is the declared name of a driver's type, through a pointer.
func launchTermsTypeName(d ProviderDriver) string {
	typ := reflect.TypeOf(d)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Name()
}

// launchTermsDeclaringTypes parses the package's non-test sources and returns,
// sorted, the receiver type of every method that implements the optional half:
// `LaunchTerms() DriverLaunchTerms`.
func launchTermsDeclaringTypes(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Name.Name != "LaunchTerms" {
				continue
			}
			if fn.Type.Params.NumFields() != 0 || fn.Type.Results.NumFields() != 1 {
				continue
			}
			if result, ok := fn.Type.Results.List[0].Type.(*ast.Ident); !ok || result.Name != "DriverLaunchTerms" {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if ident, ok := recv.(*ast.Ident); ok {
				names = append(names, ident.Name)
			} else {
				t.Errorf("%s: a LaunchTerms method on a receiver this scan cannot name: %T", name, recv)
			}
		}
	}
	sort.Strings(names)
	return names
}

// launchTermsAnswer is the provider's side of one request the driver sent: the
// result to send back, or false to leave it open (a turn still running).
type launchTermsAnswer func(method string, params map[string]any) (any, bool)

// launchTermsFrame is one line the driver wrote on the child's stdin.
type launchTermsFrame struct {
	method string
	body   any
}

// launchTermsLaunch runs the probe launch the way the runtime does and returns
// what it handed the child: the spec buildLaunchSpec built, and every frame the
// driver wrote through its handshake and its first turn.
//
// The driver's session is opened by the runtime's own glue, not by a config
// this test assembles, so a launch term the runtime starts handing a driver
// reaches this conformance without anybody editing it.
func launchTermsLaunch(t *testing.T, m *Module, p CreateRunParams, answer launchTermsAnswer) (LaunchSpec, []launchTermsFrame) {
	t.Helper()
	spec := m.buildLaunchSpec(p, Credential{}, WorkSessionCredential{}, CommunicationSessionCredential{},
		"", nil, nil, nil)

	// The same steps a create takes, in its order: the live run, the driver's
	// session (a no-op on the Claude path), then the reservation window, which
	// keeps every row effect of the handshake queued — there is no row here.
	child := &launchTermsChild{t: t, answer: answer, inbox: make(chan []byte, 64)}
	lr := &liveRun{runRef: "run-probe", transport: p.Transport, proc: child, profile: p.ProviderHome}
	m.attachDriverSession(lr, p, spec.Dir, "")
	lr.abreVentanaDeReserva()
	if lr.session == nil {
		return spec, nil
	}
	if answer == nil {
		t.Fatalf("%s opened a protocol session and this test has no provider answer for it", launchDriverKey(p))
	}

	stop, served := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(served)
		child.serve(lr.session, stop)
	}()
	defer func() {
		m.closeDriverSession(lr)
		close(stop)
		<-served
	}()

	ctx := context.Background()
	if err := m.finishDriverLaunch(ctx, lr); err != nil {
		t.Fatalf("%s: the probe handshake failed: %v", launchDriverKey(p), err)
	}
	if _, err := lr.session.Input(ctx, "probe turn"); err != nil {
		t.Fatalf("%s: the probe turn failed: %v", launchDriverKey(p), err)
	}
	return spec, child.frames(t)
}

// launchTermsChild stands in for the owned child's stdin. It keeps every line
// the runtime writes and answers each request the way the provider would,
// through the same Deliver the bridge uses. It starts no process.
type launchTermsChild struct {
	t      *testing.T
	answer launchTermsAnswer
	inbox  chan []byte

	mu    sync.Mutex
	lines [][]byte
}

func (c *launchTermsChild) Send(_ context.Context, line []byte) error {
	kept := append([]byte(nil), line...)
	c.mu.Lock()
	c.lines = append(c.lines, kept)
	c.mu.Unlock()
	c.inbox <- kept
	return nil
}

func (c *launchTermsChild) Output() <-chan OutputFrame { return nil }
func (c *launchTermsChild) Wait() (int, error)         { return 0, nil }
func (c *launchTermsChild) Stop(context.Context) error { return nil }
func (c *launchTermsChild) PID() int                   { return 0 }

// serve answers the driver's requests until stop closes. A notification, and a
// request the provider leaves open, get no answer. The reply carries the
// `jsonrpc` member exactly when the request did, which is the one place the
// Codex app-server and ACP differ on the wire.
func (c *launchTermsChild) serve(session DriverSession, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case line := <-c.inbox:
			var req struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
				Params  map[string]any  `json:"params"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				c.t.Errorf("the driver wrote a line that is not a JSON object: %s", line)
				continue
			}
			if len(req.ID) == 0 || string(req.ID) == "null" || req.Method == "" {
				continue
			}
			result, ok := c.answer(req.Method, req.Params)
			if !ok {
				continue
			}
			reply := map[string]any{"id": req.ID, "result": result}
			if req.JSONRPC != "" {
				reply["jsonrpc"] = req.JSONRPC
			}
			raw, err := json.Marshal(reply)
			if err != nil {
				c.t.Errorf("marshal the answer to %s: %v", req.Method, err)
				continue
			}
			session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
		}
	}
}

// frames decodes every line the driver wrote, in order.
func (c *launchTermsChild) frames(t *testing.T) []launchTermsFrame {
	t.Helper()
	c.mu.Lock()
	lines := append([][]byte(nil), c.lines...)
	c.mu.Unlock()
	out := make([]launchTermsFrame, 0, len(lines))
	for _, line := range lines {
		var body any
		if err := json.Unmarshal(line, &body); err != nil {
			t.Fatalf("the driver wrote a line that is not JSON: %s", line)
		}
		obj, _ := body.(map[string]any)
		method, _ := obj["method"].(string)
		out = append(out, launchTermsFrame{method: method, body: body})
	}
	return out
}

// launchTermsFind reports where value reaches the child: an argv item, an
// environment value, or a string anywhere inside a frame the driver wrote. Each
// is compared for EQUALITY, so a value is found only where it was put, never
// inside a longer string that happens to contain it.
func launchTermsFind(spec LaunchSpec, frames []launchTermsFrame, value string) (string, bool) {
	for i, arg := range spec.Args {
		if arg == value {
			return fmt.Sprintf("argv[%d] of %q", i, spec.Args), true
		}
	}
	for _, env := range spec.Env {
		if env.Value == value {
			return "the environment variable " + env.Name, true
		}
	}
	for i, f := range frames {
		if at, ok := launchTermsFindIn(f.body, "", value); ok {
			return fmt.Sprintf("frame %d (%s) at %s", i+1, f.method, at), true
		}
	}
	return "", false
}

// launchTermsFindIn walks one decoded frame and returns the path of the first
// string equal to value. Object keys are walked in sorted order so the path a
// failure names is the same on every run.
func launchTermsFindIn(v any, path, value string) (string, bool) {
	switch x := v.(type) {
	case string:
		return path, x == value
	case []any:
		for i, item := range x {
			if at, ok := launchTermsFindIn(item, fmt.Sprintf("%s[%d]", path, i), value); ok {
				return at, true
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if at, ok := launchTermsFindIn(x[k], path+"."+k, value); ok {
				return at, true
			}
		}
	}
	return "", false
}

// launchTermsCodexAnswer plays the Codex app-server: initialize, account/read,
// thread/start, then turn/start for the first input.
func launchTermsCodexAnswer(method string, _ map[string]any) (any, bool) {
	switch method {
	case codexMethodInitialize:
		return map[string]any{
			"userAgent": "probe", "codexHome": "/probe", "platformFamily": "unix", "platformOs": "linux",
		}, true
	case codexMethodAccountRead:
		return map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}, true
	case codexMethodThreadStart:
		return map[string]any{
			"thread": map[string]any{"id": "thread-probe", "parentThreadId": nil, "agentRole": nil},
		}, true
	case codexMethodTurnStart:
		return map[string]any{"turn": map[string]any{"id": "turn-probe", "status": "inProgress", "items": []any{}}}, true
	}
	return nil, false
}

// launchTermsGrokAnswer plays the Grok agent: initialize with the recorded
// authentication methods, the cached-token authenticate an account-home profile
// selects, and session/new. The first session/prompt stays open, as a running
// turn does.
func launchTermsGrokAnswer(method string, _ map[string]any) (any, bool) {
	switch method {
	case grokMethodInitialize:
		return grokInitializeResult(grokAllAuthMethods()), true
	case grokMethodAuthenticate:
		return map[string]any{}, true
	case grokMethodSessionNew:
		return map[string]any{"sessionId": "grok-probe"}, true
	}
	return nil, false
}

// launchTermsOpenCodeAnswer plays the OpenCode agent: initialize, session/new
// offering the probe model and effort as exact selectable values, and a
// session/set_config_option that confirms the value it was asked to set. The
// first session/prompt stays open, as a running turn does.
func launchTermsOpenCodeAnswer(method string, params map[string]any) (any, bool) {
	switch method {
	case openCodeMethodInitialize:
		return openCodeInitializeResult(), true
	case openCodeMethodSessionNew:
		return map[string]any{
			"sessionId": "ses_probe", "configOptions": launchTermsOpenCodeOptions("opencode/big-pickle", "medium"),
		}, true
	case openCodeMethodSetConfigOption:
		model, effort := "opencode/big-pickle", "medium"
		value, _ := params["value"].(string)
		switch params["configId"] {
		case openCodeConfigIDModel:
			model = value
		case openCodeConfigIDEffort:
			effort = value
		}
		return map[string]any{"configOptions": launchTermsOpenCodeOptions(model, effort)}, true
	}
	return nil, false
}

// launchTermsOpenCodeOptions is the model and effort selectors of the OpenCode
// agent, each offering the probe value beside a default.
func launchTermsOpenCodeOptions(model, effort string) []any {
	return []any{
		map[string]any{
			"id": openCodeConfigIDModel, "name": "Model", "type": "select", "category": "model",
			"currentValue": model,
			"options": []any{
				map[string]any{"value": "opencode/big-pickle", "name": "Big Pickle"},
				map[string]any{"value": launchTermsProbeModel, "name": "Probe"},
			},
		},
		map[string]any{
			"id": openCodeConfigIDEffort, "name": "Effort", "type": "select", "category": "thought_level",
			"currentValue": effort,
			"options": []any{
				map[string]any{"value": "medium", "name": "Medium"},
				map[string]any{"value": launchTermsProbeEffort, "name": "Probe"},
			},
		},
	}
}
