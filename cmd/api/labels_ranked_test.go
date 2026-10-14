package main

import (
	"fmt"
	"net/http"
	"sort"
	"testing"
)

// TestLabelsRankedTopAndSearch covers PB-73: ?top=N usage ranking (ties by
// name asc, zero-usage included), ?q= case-insensitive substring search,
// combined params (filter-then-rank), invalid top, empty workspace, and the
// no-param backward-compat shape (exactly the legacy keys, no usage_count).
func TestLabelsRankedTopAndSearch(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	mkLabel := func(name string) string {
		return st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels",
			fmt.Sprintf(`{"name":%q,"agent":"test"}`, name), http.StatusCreated)["label"].(map[string]any)["id"].(string)
	}
	mkItem := func(labelID string) {
		st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
			fmt.Sprintf(`{"title":"it-%s","column_id":%q,"label_ids":[%q],"agent":"test"}`, labelID, col, labelID), http.StatusCreated)
	}

	// usage: feat=2, ui=1, bug=1 (tie with ui, "bug" < "ui"), zeta=0, alpha=0 (zero-usage tie).
	feat, ui, bug := mkLabel("feat-"+tag), mkLabel("ui-"+tag), mkLabel("bug-"+tag)
	mkLabel("zeta-" + tag)  // zero usage
	mkLabel("alpha-" + tag) // zero usage, name-tie with zeta
	mkItem(feat)
	mkItem(feat)
	mkItem(ui)
	mkItem(bug)

	// No params: legacy byte-shape — exactly the v1 keys per label, name order.
	legacy := st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels", "", http.StatusOK)["labels"].([]any)
	if len(legacy) != 5 {
		t.Fatalf("legacy labels = %d, want 5", len(legacy))
	}
	names := listLabelNames(t, map[string]any{"labels": legacy})
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if fmt.Sprint(names) != fmt.Sprint(sorted) {
		t.Errorf("legacy order %v not name-sorted", names)
	}
	wantKeys := "[color id name workspace_id]"
	for _, l := range legacy {
		m := l.(map[string]any)
		if fmt.Sprint(keysOf(m)) != wantKeys {
			t.Errorf("legacy label keys = %v, want exactly [color id name workspace_id] (no usage_count)", keysOf(m))
		}
	}

	// Empty top/q count as absent → legacy shape (no usage_count leak).
	emptyTop := st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?top=&q=", "", http.StatusOK)["labels"].([]any)
	if len(emptyTop) != 5 || len(emptyTop[0].(map[string]any)) != 4 {
		t.Errorf("?top=&q= = %d labels, first keys %v, want legacy shape", len(emptyTop), keysOf(emptyTop[0].(map[string]any)))
	}

	// Whitespace-only q counts as blank/absent → legacy shape (PB-80 review
	// ruling N1; blank = blank, consistent with the service's blank-q filter).
	blankQ := st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?q=%20", "", http.StatusOK)["labels"].([]any)
	if len(blankQ) != 5 || len(blankQ[0].(map[string]any)) != 4 {
		t.Errorf("?q=%%20 = %d labels, first keys %v, want legacy shape (no usage_count)", len(blankQ), keysOf(blankQ[0].(map[string]any)))
	}

	// ?top=2: feat (2 uses) first, then name-asc tie between bug and ui (1 each).
	top2 := st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?top=2", "", http.StatusOK)["labels"].([]any)
	got := rankedNames(t, top2)
	if fmt.Sprint(got) != fmt.Sprint([]string{"feat-" + tag, "bug-" + tag}) {
		t.Errorf("top=2 = %v, want [feat bug]", got)
	}
	// top=3: the bug/ui usage tie resolves by name; zeta/alpha stay out on count.
	top3 := rankedNames(t, st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?top=3", "", http.StatusOK)["labels"].([]any))
	if fmt.Sprint(top3) != fmt.Sprint([]string{"feat-" + tag, "bug-" + tag, "ui-" + tag}) {
		t.Errorf("top=3 = %v, want [feat bug ui]", top3)
	}
	// usage_count values on the ranked shape.
	first := top2[0].(map[string]any)
	if first["usage_count"].(float64) != 2 || first["id"].(string) != feat {
		t.Errorf("top label = %v, want feat with usage_count 2", first)
	}

	// Zero-usage tie inside top=N resolves by name: top=4 adds alpha (0 uses)
	// ahead of zeta.
	top4 := rankedNames(t, st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?top=4", "", http.StatusOK)["labels"].([]any))
	if fmt.Sprint(top4) != fmt.Sprint([]string{"feat-" + tag, "bug-" + tag, "ui-" + tag, "alpha-" + tag}) {
		t.Errorf("top=4 = %v, want [feat bug ui alpha]", top4)
	}

	// q: case-insensitive substring, filter-then-rank. "FE" (uppercase) matches
	// only feat-<tag> (no other name contains "fe").
	qOnly := rankedNames(t, st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?q=FE", "", http.StatusOK)["labels"].([]any))
	if fmt.Sprint(qOnly) != fmt.Sprint([]string{"feat-" + tag}) {
		t.Errorf("q=FE = %v, want [feat]", qOnly)
	}
	// q with no matches → [].
	qNone := st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?q=nomatch-"+tag, "", http.StatusOK)["labels"].([]any)
	if len(qNone) != 0 {
		t.Errorf("q=nomatch = %v, want []", qNone)
	}
	// combined: q matches the bug/ui/feat set ("a" matches feat/tag-free names? —
	// use a substring hitting two labels), then top truncates after ranking.
	qTop := rankedNames(t, st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?q="+tag+"&top=2", "", http.StatusOK)["labels"].([]any))
	if fmt.Sprint(qTop) != fmt.Sprint([]string{"feat-" + tag, "bug-" + tag}) {
		t.Errorf("q=<tag>&top=2 = %v, want [feat bug]", qTop)
	}

	// q is a LITERAL substring: LIKE metacharacters match themselves, so
	// ?q=% (every name contains zero-width? no — names don't contain %) must
	// return only labels whose name literally contains '%' — here none.
	pct := rankedNames(t, st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?q=%25", "", http.StatusOK)["labels"].([]any))
	if len(pct) != 0 {
		t.Errorf("q=%% = %v, want [] (no wildcard semantics)", pct)
	}
	// underscore is literal too: no label name contains "_bug-" + tag.
	us := rankedNames(t, st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?q=bug-_", "", http.StatusOK)["labels"].([]any))
	if len(us) != 0 {
		t.Errorf("q=bug-_ = %v, want [] (underscore is literal)", us)
	}

	// invalid top → 400.
	st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?top=0", "", http.StatusBadRequest)
	st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?top=abc", "", http.StatusBadRequest)
	st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels?top=-1", "", http.StatusBadRequest)

	// unknown workspace → 404 on the ranked path too.
	const missing = "00000000-0000-0000-0000-000000000000"
	st.do(t, "GET", "/api/workspaces/"+missing+"/labels?top=3", "", http.StatusNotFound)

	// empty workspace: ranked top over zero labels → [].
	fresh := seedBoard(t, st, "ranked-empty-"+tag)
	freshTop := st.do(t, "GET", "/api/workspaces/"+fresh.wsID+"/labels?top=5", "", http.StatusOK)["labels"].([]any)
	if len(freshTop) != 0 {
		t.Errorf("empty workspace top = %v, want []", freshTop)
	}
}

// rankedNames returns the label names of a ranked (usage_count) response.
func rankedNames(t *testing.T, raw []any) []string {
	t.Helper()
	names := make([]string, 0, len(raw))
	for _, l := range raw {
		m := l.(map[string]any)
		if m["usage_count"] == nil {
			t.Fatalf("ranked label %v missing usage_count", m)
		}
		names = append(names, m["name"].(string))
	}
	return names
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
