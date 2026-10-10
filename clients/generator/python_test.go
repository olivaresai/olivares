// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os/exec"
	"strconv"
	"testing"
)

func TestPythonPathParameterCollisions(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python runtime check requires python3")
	}
	for _, tc := range []struct {
		name, path, call, wantPath string
		body, raw, typed           bool
		parameters                 []OperationParameter
	}{
		{"tenant", "/v1/widgets/{tenant}", `"route/a b", tenant="transport", limit="5"`, "/v1/widgets/route%2Fa%20b", false, false, false, nil},
		{"self", "/v1/widgets/{self}", `"route", tenant="transport", limit="5"`, "/v1/widgets/route", false, false, false, nil},
		{"query", "/v1/widgets/{query}", `"route", tenant="transport", limit="5"`, "/v1/widgets/route", false, false, false, nil},
		{"body", "/v1/widgets/{body}", `"route", body={"value": 1}, tenant="transport", limit="5"`, "/v1/widgets/route", true, false, false, nil},
		{"body_without_payload", "/v1/widgets/{body}", `body="route", tenant="transport", limit="5"`, "/v1/widgets/route", false, false, false, nil},
		{"headers_without_locals", "/v1/widgets/{headers}", `headers="route", tenant="transport", limit="5"`, "/v1/widgets/route", false, false, false, nil},
		{"raw", "/v1/widgets/{tenant}/export", `"route", tenant="transport", limit="5"`, "/v1/widgets/route/export", false, true, false, nil},
		{"alias_collision", "/v1/widgets/{tenant}/{tenant_path}/{tenant_path_path}", `"route", tenant_path="existing", tenant_path_path="also-existing", tenant="transport", limit="5"`, "/v1/widgets/route/existing/also-existing", false, false, false, nil},
		{"typed_parameter", "/v1/m/sessions/channels/{id}", `"route", id="query-id", tenant="transport", limit=5`, "/v1/m/sessions/channels/route", false, false, true, []OperationParameter{{Name: "id", In: "query", Type: "string"}, {Name: "limit", In: "query", Type: "integer"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := Operation{Method: "GET", Path: tc.path, PathParams: pathParams(tc.path), HasBody: tc.body, RawBody: tc.raw, Parameters: tc.parameters}
			if tc.body {
				op.Method = "POST"
			}
			if tc.typed {
				op.SDKFamily = sessionsCommunicationFamily
			}
			source := string(emitPython(&Document{APIVersion: "v1", Operations: []Operation{op}}))
			// Compile and call the emitted method itself: string matching alone misses
			// duplicate arguments, local shadowing and a renamed path using the wrong value.
			script := source + `
class Capture(OperationsMixin):
    def _do(self, method, template, path, **options):
        return method, template, path, options
    _do_raw = _do
`
			if tc.typed {
				script += "SessionsCommunicationChannel = dict\n"
			}
			script += "result = Capture()." + op.pyName() + "(" + tc.call + ")\n"
			script += "assert result[:3] == (" + strconv.Quote(op.Method) + ", " + strconv.Quote(op.Path) + ", " + strconv.Quote(tc.wantPath) + "), result\n"
			script += "assert result[3]['tenant'] == 'transport', result\nassert result[3]['query']['limit'] == '5', result\n"
			if tc.body {
				script += "assert result[3]['body'] == {'value': 1}, result\n"
			}
			if tc.name == "typed_parameter" {
				script += "assert result[3]['query']['id'] == 'query-id', result\n"
			}
			if tc.typed {
				script += "assert result[3]['headers'] == {}, result\n"
			}
			if output, err := exec.Command(python, "-B", "-c", script).CombinedOutput(); err != nil {
				t.Fatalf("generated Python failed: %v\n%s\n%s", err, output, source)
			}
		})
	}
}
