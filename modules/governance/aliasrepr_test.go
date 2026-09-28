// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// wantContentError fails t unless err is a ContentError with format and reason.
func wantContentError(t *testing.T, err error, format, reason string) {
	t.Helper()
	var ce ContentError
	if !errors.As(err, &ce) || ce.Format != format || ce.Reason != reason {
		t.Fatalf("error = %v, want ContentError{%q, %q}", err, format, reason)
	}
}

// decodedHas reports whether decoded holds s.
func decodedHas(decoded []string, s string) bool {
	for _, d := range decoded {
		if d == s {
			return true
		}
	}
	return false
}

func TestSurfaceDocumentChecksSizeThenUTF8BeforeDecoding(t *testing.T) {
	t.Parallel()
	lim := auth.CountedContentLimits()

	t.Run("size is checked first", func(t *testing.T) {
		content := `{"a":"` + strings.Repeat("x", 262_144) + "\xff" + `"}`
		_, err := surfaceDocument(surfaceManagedSettings, content, lim)
		var capacity auth.CapacityError
		if !errors.As(err, &capacity) || capacity.Dimension != "content_bytes" || !errors.Is(err, auth.ErrAliasCapacity) {
			t.Fatalf("error = %v, want a content_bytes capacity refusal before the UTF-8 check", err)
		}
	})

	t.Run("invalid UTF-8 JSON is refused, not replaced", func(t *testing.T) {
		_, err := surfaceDocument(surfaceManagedSettings, `{"u":"jdoe`+"\xff"+`@x.test"}`, lim)
		wantContentError(t, err, "json", "utf8")
	})

	t.Run("invalid UTF-8 Cedar is refused", func(t *testing.T) {
		_, err := surfaceDocument(surfaceCedar, `permit(principal == User::"`+"\xff"+`", action, resource);`, lim)
		wantContentError(t, err, "cedar", "utf8")
	})

	t.Run("JSON strings are decoded", func(t *testing.T) {
		content := `{"u":"jdoe\u0040x.test","n":1}`
		doc, err := surfaceDocument(surfaceManagedSettings, content, lim)
		if err != nil {
			t.Fatalf("surfaceDocument: %v", err)
		}
		if doc.Raw != content {
			t.Fatalf("Raw = %q, want the stored bytes", doc.Raw)
		}
		if want := []string{"u", "jdoe@x.test", "n"}; !reflect.DeepEqual(doc.Decoded, want) {
			t.Fatalf("Decoded = %q, want %q", doc.Decoded, want)
		}
	})

	t.Run("Cedar strings are decoded", func(t *testing.T) {
		content := `permit(principal == User::"alice@x.test", action, resource) when { resource.owner == "bob\u{40}x.test" };`
		doc, err := surfaceDocument(surfaceCedar, content, lim)
		if err != nil {
			t.Fatalf("surfaceDocument: %v", err)
		}
		for _, s := range []string{"User", "alice@x.test", "owner", "bob@x.test"} {
			if !decodedHas(doc.Decoded, s) {
				t.Errorf("Decoded = %q, missing %q", doc.Decoded, s)
			}
		}
	})

	t.Run("a surface whose content is not counted has no document", func(t *testing.T) {
		_, err := surfaceDocument(surfaceOPA, "package x", lim)
		if !errors.Is(err, model.ErrUnknownKind) {
			t.Fatalf("error = %v, want %v", err, model.ErrUnknownKind)
		}
	})
}

func TestCedarOwnerEmitsEachDistinctStringOnce(t *testing.T) {
	t.Parallel()

	// `e has a0.a1…` is parsed as `e has a0 && e.a0 has a1 && …`: the parser
	// repeats e and every attribute before the last, so a walk that emitted per
	// visit would emit the 10,000-byte literal 400 times.
	big := strings.Repeat("x", 10_000)
	attrs := make([]string, 400)
	for i := range attrs {
		attrs[i] = fmt.Sprintf("a%d", i)
	}
	content := `permit(principal, action, resource) when { {k: "` + big + `"} has ` + strings.Join(attrs, ".") + ` };` + "\n" +
		`forbid(principal, action, resource) when { {k: "` + big + `"} has a0 };`
	doc, err := surfaceDocument(surfaceCedar, content, auth.CountedContentLimits())
	if err != nil {
		t.Fatalf("surfaceDocument: %v", err)
	}
	seen := map[string]int{}
	total := 0
	for _, s := range doc.Decoded {
		seen[s]++
		total += len(s)
	}
	for s, n := range seen {
		if n != 1 {
			t.Errorf("decoded string %.20q emitted %d times, want once", s, n)
		}
	}
	if total > len(content) {
		t.Errorf("decoded %d bytes from %d raw bytes; each distinct string once cannot exceed the raw size", total, len(content))
	}
	for _, s := range []string{big, "k", "a0", "a399"} {
		if seen[s] != 1 {
			t.Errorf("decoded strings miss %.20q", s)
		}
	}
}

func TestJSONOwnerRefusesTrailingNonJSON(t *testing.T) {
	t.Parallel()
	lim := auth.CountedContentLimits()

	for _, c := range []struct {
		name, content string
	}{
		{"bytes after the first value", `{"a":"b"} trailing`},
		{"a value left open", `{"a":"b"`},
		{"a key without its value", `{"a":`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := surfaceDocument(surfaceManagedMCP, c.content, lim)
			wantContentError(t, err, "json", "syntax")
		})
	}

	t.Run("every top-level value is read", func(t *testing.T) {
		doc, err := surfaceDocument(surfaceManagedMCP, `{"a":"b"}{"c":"d"}`+"\n  ", lim)
		if err != nil {
			t.Fatalf("surfaceDocument: %v", err)
		}
		if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(doc.Decoded, want) {
			t.Fatalf("Decoded = %q, want %q", doc.Decoded, want)
		}
	})
}

func TestCedarLikePatternLiteralsAreDecoded(t *testing.T) {
	t.Parallel()

	content := `permit(principal, action, resource) when { resource.name like "ab*cd" && resource.path like "*" };`
	doc, err := surfaceDocument(surfaceCedar, content, auth.CountedContentLimits())
	if err != nil {
		t.Fatalf("surfaceDocument: %v", err)
	}
	for _, s := range []string{"name", "ab", "cd", "path"} {
		if !decodedHas(doc.Decoded, s) {
			t.Errorf("Decoded = %q, missing %q", doc.Decoded, s)
		}
	}
}

func TestPatternLiteralsRefuseAnUnknownPart(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name, raw string
		want      []string
	}{
		{"no parts", `[]`, nil},
		{"a wildcard alone", `["Wildcard"]`, nil},
		{"literals around a wildcard, one of them empty", `[{"Literal":"ab"}, "Wildcard", {"Literal":""}]`, []string{"ab", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := patternLiterals([]byte(c.raw))
			if err != nil || len(got) != len(c.want) || (len(got) > 0 && !reflect.DeepEqual(got, c.want)) {
				t.Fatalf("patternLiterals(%s) = %q, %v; want %q", c.raw, got, err, c.want)
			}
		})
	}

	for _, raw := range []string{
		`["Star"]`,
		`[{"literal":"ab"}]`,
		`[{"Literal":1}]`,
		`[{"Literal":null}]`,
		`[{"Literal":"a","Other":"b"}]`,
		`[{"Literal":"a","Literal":"b"}]`,
		`[{"Literal":"a","Literal":"a"}]`,
		`[{}]`,
		`[7]`,
		`[null]`,
		`[["Wildcard"]]`,
		`{"Literal":"a"}`,
		`null`,
		`not json`,
		// Anything after the array, and an array that never closes.
		`["Wildcard"] []`,
		`[] x`,
		`[{"Literal":"a"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := patternLiterals([]byte(raw))
			if got != nil {
				t.Errorf("patternLiterals(%s) returned %q with its error", raw, got)
			}
			wantContentError(t, err, "cedar", "syntax")
		})
	}
}
