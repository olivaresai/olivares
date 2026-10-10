// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package driverfacts

import (
	"slices"
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

// OpenCode lists the method its sign-in relay can complete, and its default is
// the one it ran before the list existed. The other methods the pinned binary
// offers are not relayed: the browser method waits for a callback on the engine
// host, and the key method reads a secret from the tool's own prompt.
func TestOpenCodeLoginMethods(t *testing.T) {
	f, _ := Lookup("opencode")
	if ids := f.LoginMethodIDs(); !slices.Equal(ids, []string{"chatgpt-headless"}) {
		t.Fatalf("opencode methods = %v", ids)
	}
	args, ok := f.LoginArgsFor("chatgpt-headless")
	if !ok || !slices.Equal(args, f.LoginArgs) {
		t.Fatalf("chatgpt-headless = %v, want the default LoginArgs %v", args, f.LoginArgs)
	}
	if c, _ := Lookup("claude"); len(c.LoginMethodIDs()) != 0 {
		t.Fatalf("claude lists methods %v; it has one", c.LoginMethodIDs())
	}
}
