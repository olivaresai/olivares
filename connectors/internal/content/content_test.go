// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package content

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

func TestValidateCredentialRef(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		ok   bool
	}{
		{"empty allowed", "", true},
		{"vault ref", "vault:secret/data/x#k", true},
		{"env ref", "env:GDRIVE_TOKEN", true},
		{"unknown scheme", "myscheme:foo", false},
		{"no scheme", "justatoken", false},
		{"inline aws key", "AKIAIOSFODNN7EXAMPLEABCDEFGHIJKLMNOPQRST", false},
		{"scheme but raw blob locator", "env:abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := ValidateCredentialRef(c.ref)
			if c.ok && msg != "" {
				t.Fatalf("ref %q should be valid, got %q", c.ref, msg)
			}
			if !c.ok && msg == "" {
				t.Fatalf("ref %q should be rejected", c.ref)
			}
		})
	}
}

func TestStorePaginatesDeterministically(t *testing.T) {
	var st Store
	docs := make([]contentsource.Document, 0, DefaultPageSize+5)
	for i := 0; i < DefaultPageSize+5; i++ {
		docs = append(docs, contentsource.Document{DocID: zeroPad(i), Title: "t", ModifiedAt: time.Unix(int64(i), 0).UTC()})
	}
	st.SetDocs(docs)
	if st.Len() != DefaultPageSize+5 {
		t.Fatalf("Len = %d", st.Len())
	}
	page1, next, err := st.List("")
	if err != nil || next == "" {
		t.Fatalf("page1 err=%v next=%q", err, next)
	}
	if len(page1) != DefaultPageSize {
		t.Fatalf("page1 = %d, want %d", len(page1), DefaultPageSize)
	}
	page2, next2, err := st.List(next)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 5 || next2 != "" {
		t.Fatalf("page2 = %d, next2 = %q", len(page2), next2)
	}
	if _, err := st.Fetch(zeroPad(7)); err != nil {
		t.Fatalf("Fetch existing: %v", err)
	}
	if _, err := st.Fetch("missing"); err != ErrNotFound {
		t.Fatalf("Fetch missing = %v, want ErrNotFound", err)
	}
	if _, _, err := st.List("notanumber"); err == nil {
		t.Fatal("bad cursor should error")
	}
}

func zeroPad(i int) string {
	const digits = "0123456789"
	return string([]byte{'d', digits[(i/100)%10], digits[(i/10)%10], digits[i%10]})
}
