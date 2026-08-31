// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package splunkhec

import (
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// TestHECVerdictIsTheOneTable pins the shared entry point, including the trap that
// broke its first version: parseHECResponse returns a non-nil error for EVERY
// non-zero code (that is its contract for the connector, which wants the message), so
// a caller reading that error as "not a HEC document" classifies every refusal as
// unrecognizable — the opposite of the verdict.
func TestHECVerdictIsTheOneTable(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       sdk.DeliveryOutcome
		wantOK     bool
	}{
		{name: "success", body: `{"text":"Success","code":0}`, want: sdk.OutcomeDelivered, wantOK: true},
		{name: "terminal refusal", body: `{"text":"Incorrect index","code":7}`, want: sdk.OutcomeRejected, wantOK: true},
		{name: "capacity warning is an acceptance", body: `{"text":"queue is approaching its capacity limit","code":24}`, want: sdk.OutcomeDeliveredWithWarning, wantOK: true},
		{name: "health answer is not an acceptance", body: `{"text":"HEC is healthy","code":17}`, want: sdk.OutcomeIndeterminate, wantOK: true},
		{name: "transient", body: `{"text":"Server is busy","code":9}`, want: sdk.OutcomeUnavailable, wantOK: true},
		{name: "ack submit response", body: `{"text":"Success","code":0,"ackId":42}`, want: sdk.OutcomeDelivered, wantOK: true},
		{name: "empty is not a document", body: ``, wantOK: false},
		{name: "not JSON", body: `<html>proxy error</html>`, wantOK: false},
		{name: "JSON but not HEC", body: `{"status":"ok"}`, wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, ok := HECVerdict([]byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("outcome = %v, want %v", got, tc.want)
			}
		})
	}
}
