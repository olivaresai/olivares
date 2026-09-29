// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localframe

import "testing"

func TestReader_CarriesTheModuleReadOpAndNoOther(t *testing.T) {
	for _, body := range []string{
		`{"t":"request","op":"module.read","body":{"query":"services.list","surface":"cli"}}`,
		`{"t":"response","op":"module.read","status":200,"body":{"query":"services.list"}}`,
		`{"t":"error","op":"module.read","status":422,"code":"input_refused"}`,
		`{"t":"error","op":"module.read","status":503,"code":"consumer_unavailable"}`,
	} {
		s := source(body)
		if r, err := next(s.receive, Expect{UID: 1000, PID: 42}, nil, 65534, 65534); err != nil || r.Op != "module.read" {
			t.Errorf("%s: %#v %v", body, r, err)
		}
	}
	for _, body := range []string{
		`{"t":"request","op":"module.write","body":{}}`,
		`{"t":"request","op":"module.read.all","body":{}}`,
		`{"t":"request","op":"Module.Read","body":{}}`,
		`{"t":"request","op":"module.act","body":{}}`,
	} {
		s := source(body)
		if _, err := next(s.receive, Expect{UID: 1000, PID: 42}, nil, 65534, 65534); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}
