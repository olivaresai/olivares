// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package driverfacts

import (
	"slices"
	"strings"
	"testing"
)

// A tool may list several official login methods. LoginArgs stays the default:
// an empty method id runs it, exactly as before; a listed id runs that method's
// args; any other id is unknown.
func TestLoginArgsForMethod(t *testing.T) {
	f := Facts{
		LoginArgs: []string{"login", "--a"},
		LoginMethods: []LoginMethod{
			{ID: "a", Label: "Method A", Args: []string{"login", "--a"}},
			{ID: "b", Label: "Method B", Args: []string{"login", "--b"}},
		},
	}
	if args, ok := f.LoginArgsFor(""); !ok || !slices.Equal(args, []string{"login", "--a"}) {
		t.Errorf("default = %v %v, want LoginArgs", args, ok)
	}
	if args, ok := f.LoginArgsFor("b"); !ok || !slices.Equal(args, []string{"login", "--b"}) {
		t.Errorf("method b = %v %v", args, ok)
	}
	if args, ok := f.LoginArgsFor("z"); ok || args != nil {
		t.Errorf("unknown method = %v %v, want none", args, ok)
	}
	if ids := f.LoginMethodIDs(); !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("ids = %v", ids)
	}
	listed := f.LoginMethodList()
	if len(listed) != 2 || !listed[0].Default || listed[1].Default || listed[1].Label != "Method B" {
		t.Errorf("list = %+v, want a (default) and b", listed)
	}
	none := Facts{LoginArgs: []string{"login"}}
	if args, ok := none.LoginArgsFor(""); !ok || len(args) != 1 {
		t.Errorf("a tool without methods must run LoginArgs for the empty id: %v %v", args, ok)
	}
	if _, ok := none.LoginArgsFor("a"); ok {
		t.Error("a tool without methods accepted a method id")
	}
}

// OpenCode lists its own providers first (#2567): the OpenCode console key (Zen and
// the free models) and the Go subscription key, each read by the tool's own login
// prompt, then the ChatGPT device method. The default a start without a method
// runs stays the ChatGPT one, so a client that names no method is signed in
// exactly as before. The browser method is not relayed: it waits for a callback
// on the engine host.
func TestOpenCodeLoginMethods(t *testing.T) {
	f, _ := Lookup("opencode")
	if ids := f.LoginMethodIDs(); !slices.Equal(ids, []string{"opencode", "opencode-go", "chatgpt-headless"}) {
		t.Fatalf("opencode methods = %v", ids)
	}
	for id, provider := range map[string]string{"opencode": "opencode", "opencode-go": "opencode-go"} {
		args, ok := f.LoginArgsFor(id)
		if !ok || !slices.Equal(args, []string{"auth", "login", "--provider", provider}) {
			t.Fatalf("%s = %v %v, want auth login --provider %s", id, args, ok, provider)
		}
		m := f.LoginMethods[slices.Index(f.LoginMethodIDs(), id)]
		if m.KeyPrompt != "Enter your API key" || !strings.HasPrefix(m.KeyURL, "https://opencode.ai/") {
			t.Fatalf("%s key prompt %q and page %q, want the tool's prompt and an opencode.ai page", id, m.KeyPrompt, m.KeyURL)
		}
	}
	args, ok := f.LoginArgsFor("chatgpt-headless")
	if !ok || !slices.Equal(args, f.LoginArgs) {
		t.Fatalf("chatgpt-headless = %v, want the default LoginArgs %v", args, f.LoginArgs)
	}
	if !slices.Contains(f.LoginArgs, "openai") {
		t.Fatalf("the default login changed: %v", f.LoginArgs)
	}
	listed := f.LoginMethodList()
	if len(listed) != 3 || !listed[0].Key || !listed[1].Key || listed[2].Key || !listed[2].Default || listed[0].Default {
		t.Fatalf("opencode list = %+v, want two key methods first and the ChatGPT default last", listed)
	}
	if c, _ := Lookup("claude"); len(c.LoginMethodIDs()) != 0 {
		t.Fatalf("claude lists methods %v; it has one", c.LoginMethodIDs())
	}
}
