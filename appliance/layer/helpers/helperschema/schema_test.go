// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// requestTypes are the documents of every helper of the seam, each with one valid document.
var requestTypes = []struct {
	name  string
	new   func() Request
	valid string
}{
	{"power", func() Request { return &PowerRequest{} }, `{"verb": "reboot", "operation_id": "` + testOperationID + `"}`},
	{"support-bundle", func() Request { return &SupportBundleRequest{} }, `{"op": "produce", "operation_id": "` + testOperationID + `"}`},
}

// testOperationID is an operation id a client minted.
const testOperationID = "0123456789abcdef0123456789abcdef"

// refused asserts that err is an input refusal that repeats none of the values sent.
func refused(t *testing.T, err error, sent ...string) {
	t.Helper()
	var input *InputError
	if !errors.As(err, &input) {
		t.Fatalf("got %v, want an input refusal", err)
	}
	for _, value := range sent {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("the refusal repeats the value %q: %v", value, err)
		}
	}
}

func TestHelperInput_UnknownFieldRefusesAndNoPathArgumentExists(t *testing.T) {
	for _, rt := range requestTypes {
		t.Run(rt.name, func(t *testing.T) {
			if err := Decode(strings.NewReader(rt.valid), rt.new()); err != nil {
				t.Fatalf("control: the valid document %s was refused: %v", rt.valid, err)
			}
			open := strings.TrimSuffix(rt.valid, "}")
			// A field the schema does not name is refused, never ignored: a caller cannot
			// declare a path, a mode, a unit or who it is.
			for _, field := range []string{"path", "file", "mode", "unit", "invoker", "uid", "DO-NOT-PRINT"} {
				err := Decode(strings.NewReader(open+`, "`+field+`": "/etc/DO-NOT-PRINT"}`), rt.new())
				refused(t, err, "/etc/DO-NOT-PRINT", "DO-NOT-PRINT")
			}
			for name, document := range map[string]string{
				"a repeated key":        open + `, ` + strings.TrimPrefix(rt.valid, "{"),
				"a null":                strings.Replace(rt.valid, `"reboot"`, "null", 1),
				"a second document":     rt.valid + rt.valid,
				"an array":              "[" + rt.valid + "]",
				"a string":              `"reboot"`,
				"nothing":               "",
				"more than 4096 bytes":  rt.valid + strings.Repeat(" ", MaxDocument),
				"a nested unknown":      open + `, "extra": {"path": "/etc"}}`,
				"a number for a string": strings.Replace(strings.Replace(rt.valid, `"reboot"`, "7", 1), `"produce"`, "7", 1),
			} {
				if name == "a null" && !strings.Contains(rt.valid, "reboot") {
					document = strings.Replace(rt.valid, `"produce"`, "null", 1)
				}
				refused(t, Decode(strings.NewReader(document), rt.new()))
			}
		})
	}

	t.Run("a key is its tag's exact bytes: a case variant is refused, never folded", func(t *testing.T) {
		nonce := strings.Repeat("ab", 32)
		for name, row := range map[string]struct {
			new      func() Request
			document string
		}{
			"a VERB case variant repeating verb": {func() Request { return &PowerRequest{} },
				`{"verb": "reboot", "operation_id": "` + testOperationID + `", "VERB": "shutdown"}`},
			"a Verb alias for verb": {func() Request { return &PowerRequest{} },
				`{"Verb": "reboot", "operation_id": "` + testOperationID + `"}`},
			"an Operation_ID alias for operation_id": {func() Request { return &PowerRequest{} },
				`{"verb": "reboot", "Operation_ID": "` + testOperationID + `"}`},
			"an OP case variant repeating op": {func() Request { return &SupportBundleRequest{} },
				`{"op": "produce", "operation_id": "` + testOperationID + `", "OP": "fetch"}`},
			"a Nonce alias for nonce": {func() Request { return &SupportBundleRequest{} },
				`{"op": "fetch", "Nonce": "` + nonce + `"}`},
		} {
			t.Run(name, func(t *testing.T) {
				refused(t, Decode(strings.NewReader(row.document), row.new()))
			})
		}
	})

	t.Run("every value is from a closed set, so a path is never a value", func(t *testing.T) {
		for _, value := range []string{"/sbin/reboot", "../reboot", "reboot now", "REBOOT", "poweroff", "halt", ""} {
			refused(t, Decode(strings.NewReader(`{"verb": "`+value+`"}`), &PowerRequest{}), value)
		}
		for _, document := range []string{
			`{"op": "fetch", "nonce": "/var/lib/olivares-support-bundle/x.bundle.json"}`,
			`{"op": "fetch", "nonce": "../../../etc/DO-NOT-PRINT"}`,
			`{"op": "fetch", "nonce": "` + strings.Repeat("A", 64) + `"}`,
			`{"op": "fetch"}`,
			`{"op": "produce", "nonce": "` + strings.Repeat("a", 64) + `"}`,
			`{"op": "rm"}`,
		} {
			refused(t, Decode(strings.NewReader(document), &SupportBundleRequest{}), "DO-NOT-PRINT")
		}
	})

	t.Run("no document has a field that could carry a path", func(t *testing.T) {
		for _, rt := range requestTypes {
			typ := reflect.TypeOf(rt.new()).Elem()
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				lower := strings.ToLower(field.Name + " " + field.Tag.Get("json"))
				for _, word := range []string{"path", "file", "dir", "name", "unit", "mode", "user", "uid"} {
					if strings.Contains(lower, word) {
						t.Errorf("%s has a field %s (%s)", rt.name, field.Name, field.Tag.Get("json"))
					}
				}
				if field.Type.Kind() != reflect.String {
					t.Errorf("%s.%s is %s; every field is a string from a closed set", rt.name, field.Name, field.Type.Kind())
				}
				// Setting any field to a path makes the document invalid.
				v := reflect.New(typ)
				if err := Decode(strings.NewReader(rt.valid), v.Interface().(Request)); err != nil {
					t.Fatal(err)
				}
				v.Elem().Field(i).SetString("/etc/DO-NOT-PRINT")
				if err := v.Interface().(Request).Validate(); err == nil {
					t.Errorf("%s accepts a path in %s", rt.name, field.Name)
				}
			}
		}
	})

	t.Run("a helper takes no argument but --help", func(t *testing.T) {
		if action, err := CheckArgs(nil); err != nil || action != ArgsServe {
			t.Fatalf("no argument: %v %v", action, err)
		}
		if action, err := CheckArgs([]string{"--help"}); err != nil || action != ArgsHelp {
			t.Fatalf("--help: %v %v", action, err)
		}
		for _, args := range [][]string{
			{"/etc/DO-NOT-PRINT"}, {"--path", "/etc"}, {"reboot"}, {"--mode=tty1"}, {"--help", "/etc"}, {""},
		} {
			if _, err := CheckArgs(args); err == nil || strings.Contains(err.Error(), "DO-NOT-PRINT") {
				t.Errorf("the command line %q was accepted or echoed: %v", args, err)
			}
		}
		for _, word := range []string{"standard input", "no argument", "no path", "Exit 0", "1: refused", "2: a usage"} {
			if !strings.Contains(Usage, word) {
				t.Errorf("--help does not state %q", word)
			}
		}
	})
}
