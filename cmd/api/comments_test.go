package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestCommentsREST covers list/create for item comments: ascending order,
// wrapped {comment} responses, author_type agent/human, and validation.
func TestCommentsREST(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	iid := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"c-%s","agent":"test"}`, tag), http.StatusCreated)["item"].(map[string]any)["id"].(string)

	if got := st.do(t, "GET", "/api/items/"+iid+"/comments", "", http.StatusOK)["comments"].([]any); len(got) != 0 {
		t.Errorf("fresh item comments = %v, want []", got)
	}

	want := []string{"first", "second", "third"}
	for i, body := range want {
		out := st.do(t, "POST", "/api/items/"+iid+"/comments",
			fmt.Sprintf(`{"body":%q,"agent":"agent-%d"}`, body, i), http.StatusCreated)
		c, ok := out["comment"].(map[string]any)
		if !ok {
			t.Fatalf("response missing wrapped comment: %v", out)
		}
		if c["body"] != body || c["author_type"] != "agent" || c["author_name"] != fmt.Sprintf("agent-%d", i) {
			t.Errorf("comment = %v, want body %q by agent-%d", c, body, i)
		}
		// Distinct created_at so the ascending order below is deterministic.
		time.Sleep(2 * time.Millisecond)
	}
	// Absent agent -> author_type/name "human".
	human := st.do(t, "POST", "/api/items/"+iid+"/comments", `{"body":"human note"}`, http.StatusCreated)["comment"].(map[string]any)
	if human["author_type"] != "human" || human["author_name"] != "human" {
		t.Errorf("human comment = %v, want author human/human", human)
	}
	want = append(want, "human note")

	list := st.do(t, "GET", "/api/items/"+iid+"/comments", "", http.StatusOK)["comments"].([]any)
	if len(list) != len(want) {
		t.Fatalf("comments = %d, want %d: %v", len(list), len(want), list)
	}
	var prev string
	for i, raw := range list {
		c := raw.(map[string]any)
		if c["body"] != want[i] {
			t.Errorf("comments[%d] body = %v, want %q (ascending by created_at)", i, c["body"], want[i])
		}
		at, err := time.Parse(time.RFC3339Nano, c["created_at"].(string))
		if err != nil {
			t.Fatalf("parse created_at %v: %v", c["created_at"], err)
		}
		if i > 0 {
			p, _ := time.Parse(time.RFC3339Nano, prev)
			if at.Before(p) {
				t.Errorf("comments[%d] created_at %s < previous %s; want ascending", i, at, p)
			}
		}
		prev = c["created_at"].(string)
	}

	// validation: blank body -> 400; unknown item -> 404.
	st.do(t, "POST", "/api/items/"+iid+"/comments", `{"body":"   "}`, http.StatusBadRequest)
	const zero = "00000000-0000-0000-0000-000000000000"
	st.do(t, "GET", "/api/items/"+zero+"/comments", "", http.StatusNotFound)
	st.do(t, "POST", "/api/items/"+zero+"/comments", `{"body":"x"}`, http.StatusNotFound)
}
