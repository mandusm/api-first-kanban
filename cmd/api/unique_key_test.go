package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// itemKeyRE is the contract for item keys: <board prefix>-<positive sequence>.
var itemKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,4}-[1-9][0-9]*$`)

// TestItemKeysSurfacedAndStable asserts a created item's key is exposed on every
// read path (create response, board snapshot, paginated page, get_item) and
// never changes across patch/move/archive/unarchive/transfer.
func TestItemKeysSurfacedAndStable(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedBoard(t, st, fmt.Sprintf("%d", nowNanos()))

	out := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"keyed","column_id":%q,"agent":"test"}`, sd.columns[0]), http.StatusCreated)
	item := out["item"].(map[string]any)
	iid := item["id"].(string)
	key, _ := item["key"].(string)
	if !itemKeyRE.MatchString(key) {
		t.Fatalf("created item key = %q, want match %s", key, itemKeyRE)
	}

	// Board snapshot carries the same key.
	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	if got := itemKeyIn(snap["items"], iid); got != key {
		t.Errorf("snapshot key for %s = %q, want %q", iid, got, key)
	}

	// Paginated column page carries key on every item.
	page := st.do(t, "GET", "/api/boards/"+sd.boardID+"/items?column_id="+sd.columns[0], "", http.StatusOK)
	pageItems, _ := page["items"].([]any)
	if len(pageItems) == 0 {
		t.Fatal("paginated page returned no items")
	}
	for _, it := range pageItems {
		m := it.(map[string]any)
		if k, _ := m["key"].(string); !itemKeyRE.MatchString(k) {
			t.Errorf("page item %v key = %q, want match %s", m["id"], k, itemKeyRE)
		}
	}

	// get_item carries key.
	detail := st.do(t, "GET", "/api/items/"+iid, "", http.StatusOK)
	if got := detail["item"].(map[string]any)["key"]; got != key {
		t.Errorf("get_item key = %v, want %q", got, key)
	}

	// Immutability across lifecycle transitions.
	st.do(t, "PATCH", "/api/items/"+iid, `{"title":"keyed-2","agent":"test"}`, http.StatusOK)
	st.do(t, "POST", "/api/items/"+iid+"/move",
		fmt.Sprintf(`{"column_id":%q,"agent":"test"}`, sd.columns[1]), http.StatusOK)
	st.do(t, "POST", "/api/items/"+iid+"/archive", `{"agent":"test"}`, http.StatusOK)

	archived := st.do(t, "GET", "/api/items/"+iid, "", http.StatusOK)
	if got := archived["item"].(map[string]any)["key"]; got != key {
		t.Errorf("key after archive = %v, want %q", got, key)
	}

	st.do(t, "POST", "/api/items/"+iid+"/unarchive", `{"agent":"test"}`, http.StatusOK)
	st.do(t, "POST", "/api/items/"+iid+"/transfer", `{"to_agent":"bob","agent":"test"}`, http.StatusOK)

	final := st.do(t, "GET", "/api/items/"+iid, "", http.StatusOK)
	if got := final["item"].(map[string]any)["key"]; got != key {
		t.Errorf("key after transfer = %v, want %q", got, key)
	}
}

// itemKeyIn returns the key of the snapshot item with the given id ("" absent).
func itemKeyIn(raw any, id string) string {
	items, _ := raw.([]any)
	for _, it := range items {
		m, ok := it.(map[string]any)
		if ok && m["id"] == id {
			k, _ := m["key"].(string)
			return k
		}
	}
	return ""
}

// TestCreateItemKeysConcurrent is the race regression test: 20 parallel creates
// on ONE board must all succeed (no 409) with 20 distinct keys, proving the
// sequence allocation serializes per board inside the create transaction.
func TestCreateItemKeysConcurrent(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedBoard(t, st, fmt.Sprintf("%d", nowNanos()))

	const n = 20
	keys := make([]string, n)
	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"title":"conc-%d","column_id":%q,"agent":"test"}`, i, sd.columns[0])
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/boards/"+sd.boardID+"/items", strings.NewReader(body)))
			statuses[i] = rec.Code
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if item, ok := out["item"].(map[string]any); ok {
				keys[i], _ = item["key"].(string)
			}
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		if statuses[i] != http.StatusCreated {
			t.Errorf("create %d: status=%d, want 201 (spurious conflict?)", i, statuses[i])
		}
		k := keys[i]
		if !itemKeyRE.MatchString(k) {
			t.Errorf("create %d: key = %q, want match %s", i, k, itemKeyRE)
			continue
		}
		if seen[k] {
			t.Errorf("duplicate key %q across concurrent creates", k)
		}
		seen[k] = true
	}
	if len(seen) != n {
		t.Errorf("distinct keys = %d, want %d", len(seen), n)
	}
}

// TestBoardKeyPrefixesDistinct asserts same-initialism board names get distinct
// prefixes, and the colliding board's prefix is the initialism plus a numeric
// suffix (e.g. PB, PB2). The test DB is shared and never reset, so earlier
// boards may already own low suffixes; the invariant is distinctness + shape.
func TestBoardKeyPrefixesDistinct(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	out := st.do(t, "POST", "/api/workspaces", `{"name":"ws-prefix-`+tag+`","agent":"test"}`, http.StatusCreated)
	ws := out["workspace"].(map[string]any)["id"].(string)

	// All three names initialise to "QA" (Q from Quixote, A from the second
	// word), so the collision suffix must make their prefixes distinct.
	prefixes := make([]string, 0, 3)
	for _, name := range []string{"Quixote Alpha " + tag, "Quixote Amber " + tag, "Quixote Anchor " + tag} {
		bout := st.do(t, "POST", "/api/workspaces/"+ws+"/boards", `{"name":`+jsonString(name)+`,"agent":"test"}`, http.StatusCreated)
		board := bout["board"].(map[string]any)
		p, _ := board["key_prefix"].(string)
		if !regexp.MustCompile(`^[A-Z][A-Z0-9]{0,4}$`).MatchString(p) {
			t.Fatalf("board %q key_prefix = %q, want uppercase initialism", name, p)
		}
		if !strings.HasPrefix(p, "QA") {
			t.Errorf("board %q key_prefix = %q, want prefix \"QA\"", name, p)
		}
		prefixes = append(prefixes, p)
	}
	seen := map[string]bool{}
	for _, p := range prefixes {
		if seen[p] {
			t.Errorf("duplicate key_prefix %q across same-initialism boards", p)
		}
		seen[p] = true
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
