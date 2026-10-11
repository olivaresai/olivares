// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package replay

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func serveWith(g *Guard, event, id string, code int) (ran bool, got int) {
	w := httptest.NewRecorder()
	g.Once(w, event, id, func(w http.ResponseWriter) { ran = true; w.WriteHeader(code) })
	return ran, w.Code
}

func TestOnce(t *testing.T) {
	g := New()
	if ran, _ := serveWith(g, "push", "a", 200); !ran {
		t.Fatal("first delivery must run")
	}
	if ran, code := serveWith(g, "push", "a", 200); ran || code != http.StatusOK {
		t.Fatalf("repeat: ran=%v code=%d, want skipped with 200", ran, code)
	}
	// The event header is unsigned: a copy under another event must not use up the ID.
	if ran, _ := serveWith(g, "other", "b", 200); !ran {
		t.Fatal("first delivery of b must run")
	}
	if ran, _ := serveWith(g, "push", "b", 200); !ran {
		t.Fatal("the same id under another event is another key")
	}
	for i := 0; i < 2; i++ {
		if ran, _ := serveWith(g, "push", "", 200); !ran {
			t.Fatal("an empty id must always run")
		}
	}
	// A failed delivery is forgotten, so the retry runs; then it is remembered.
	serveWith(g, "push", "c", http.StatusInternalServerError)
	if ran, _ := serveWith(g, "push", "c", 200); !ran {
		t.Fatal("retry after failure must run")
	}
	if ran, _ := serveWith(g, "push", "c", 200); ran {
		t.Fatal("repeat after a successful retry must be skipped")
	}
}

// A repeat while the first delivery is still served gets 409, not 200, so the
// sender retries if the first then fails.
func TestOnceInFlight(t *testing.T) {
	g := New()
	var inner int
	g.Once(httptest.NewRecorder(), "push", "a", func(w http.ResponseWriter) {
		_, inner = serveWith(g, "push", "a", 200)
		w.WriteHeader(http.StatusInternalServerError)
	})
	if inner != http.StatusConflict {
		t.Fatalf("in-flight repeat: %d, want 409", inner)
	}
	if ran, _ := serveWith(g, "push", "a", 200); !ran {
		t.Fatal("after the first failed, the retry must run")
	}
}

// A panicking delivery is forgotten, so the sender's retry runs.
func TestOncePanicForgets(t *testing.T) {
	g := New()
	func() {
		defer func() { _ = recover() }()
		g.Once(httptest.NewRecorder(), "push", "a", func(http.ResponseWriter) { panic("boom") })
	}()
	if ran, _ := serveWith(g, "push", "a", 200); !ran {
		t.Fatal("retry after a panic must run")
	}
}

func TestOnceBounded(t *testing.T) {
	g := New()
	serveWith(g, "e", "old", 200)
	for i := 0; i < Capacity-1; i++ {
		serveWith(g, "e", strconv.Itoa(i), 200)
	}
	if ran, _ := serveWith(g, "e", "old", 200); ran {
		t.Fatal("the guard holds Capacity ids; old must still be remembered")
	}
	serveWith(g, "e", "newest", 200) // evicts "old"
	if ran, _ := serveWith(g, "e", "old", 200); !ran {
		t.Fatal("the oldest id must be evicted past Capacity")
	}
	if len(g.seen) > Capacity {
		t.Fatalf("seen holds %d ids, cap %d", len(g.seen), Capacity)
	}
}

// An id forgotten and claimed again lives in its new slot: its stale old slot
// coming round must not evict it.
func TestOnceReclaimSurvivesOldSlot(t *testing.T) {
	g := New()
	serveWith(g, "e", "x", http.StatusInternalServerError) // slot 0, forgotten
	serveWith(g, "e", "x", 200)                            // slot 1
	for i := 0; i < Capacity-1; i++ {                      // fills slots 2..Capacity, wrapping to 0
		serveWith(g, "e", strconv.Itoa(i), 200)
	}
	if ran, _ := serveWith(g, "e", "x", 200); ran {
		t.Fatal("x was evicted through its stale slot")
	}
}
