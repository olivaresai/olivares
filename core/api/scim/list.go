// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

// Page holds the SCIM pagination parameters (RFC 7644 §3.4.2.4): startIndex is
// 1-based (default 1); count is the requested page size (provider-capped). A
// count of 0 is a valid "count only" request (return totalResults, no Resources).
type Page struct {
	StartIndex int
	Count      int
	CountSet   bool
}

// ParsePage derives a Page from the raw startIndex/count query values, clamping
// to safe defaults and the provider cap.
func ParsePage(startIndex, count string, countProvided bool) Page {
	p := Page{StartIndex: 1, Count: MaxPageSize, CountSet: countProvided}
	if startIndex != "" {
		if n := atoiDefault(startIndex, 1); n >= 1 {
			p.StartIndex = n
		}
	}
	if countProvided {
		n := atoiDefault(count, MaxPageSize)
		if n < 0 {
			n = 0
		}
		if n > MaxPageSize {
			n = MaxPageSize
		}
		p.Count = n
	}
	return p
}

// ListResponse builds the SCIM ListResponse envelope for a page of resources
// drawn from the full matched set. total is the total number of matches across
// all pages; resources are the already-paginated items for this page.
func ListResponse(total int, page Page, resources []map[string]any) map[string]any {
	if resources == nil {
		resources = []map[string]any{}
	}
	return map[string]any{
		"schemas":      []string{MsgListResponse},
		"totalResults": total,
		"startIndex":   page.StartIndex,
		"itemsPerPage": len(resources),
		"Resources":    resources,
	}
}

// Slice applies 1-based startIndex + count pagination to a matched set, returning
// the page window. A CountSet page with Count==0 returns no items (count-only).
func Slice[T any](all []T, page Page) []T {
	if page.CountSet && page.Count == 0 {
		return nil
	}
	start := page.StartIndex - 1
	if start < 0 {
		start = 0
	}
	if start >= len(all) {
		return nil
	}
	end := len(all)
	if page.Count > 0 && start+page.Count < end {
		end = start + page.Count
	}
	return all[start:end]
}

func atoiDefault(s string, def int) int {
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n
}
