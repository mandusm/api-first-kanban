package service

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestParseParentItemID pins the shared PATCH three-state decode: absent keeps,
// null clears, a string sets, and any other JSON type is a validation error.
func TestParseParentItemID(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		name    string
		raw     json.RawMessage
		wantSet bool
		wantID  *string
		wantErr bool
	}{
		{name: "absent keeps", raw: nil, wantSet: false, wantID: nil},
		{name: "null clears", raw: json.RawMessage(`null`), wantSet: true, wantID: nil},
		{name: "string sets", raw: json.RawMessage(`"` + id + `"`), wantSet: true, wantID: &id},
		{name: "non-string rejects", raw: json.RawMessage(`5`), wantErr: true},
		{name: "bool rejects", raw: json.RawMessage(`true`), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, got, err := ParseParentItemID(tc.raw)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("ParseParentItemID(%s) err = %v, want ErrInvalid", tc.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseParentItemID(%s) unexpected err: %v", tc.raw, err)
			}
			if set != tc.wantSet {
				t.Errorf("set = %v, want %v", set, tc.wantSet)
			}
			if (got == nil) != (tc.wantID == nil) || (got != nil && *got != *tc.wantID) {
				t.Errorf("id = %v, want %v", got, tc.wantID)
			}
		})
	}
}

// TestNormalizeAgent pins the shared blank/absent -> "human" default.
func TestNormalizeAgent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "human"},
		{"   ", "human"},
		{"human", "human"},
		{"worker", "worker"},
	} {
		if got := NormalizeAgent(tc.in); got != tc.want {
			t.Errorf("NormalizeAgent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	blank, name := "  ", "worker"
	if got := NormalizeAgentPtr(nil); got != "human" {
		t.Errorf("NormalizeAgentPtr(nil) = %q, want human", got)
	}
	if got := NormalizeAgentPtr(&blank); got != "human" {
		t.Errorf("NormalizeAgentPtr(blank) = %q, want human", got)
	}
	if got := NormalizeAgentPtr(&name); got != "worker" {
		t.Errorf("NormalizeAgentPtr(worker) = %q, want worker", got)
	}
}
