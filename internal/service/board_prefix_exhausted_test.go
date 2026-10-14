package service

import (
	"errors"
	"strings"
	"testing"
)

// TestErrBoardPrefixExhaustedMessage pins the exhaustion 409's wording: it
// must surface the real cause (1000-candidate space exhausted, stale test
// boards) and the remedy, instead of the old opaque
// "could not allocate a unique key prefix for board".
func TestErrBoardPrefixExhaustedMessage(t *testing.T) {
	err := errBoardPrefixExhausted("PB")
	if err == nil {
		t.Fatal("errBoardPrefixExhausted(\"PB\") = nil, want error")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want a conflict ServiceError", err)
	}
	msg := svcErr.Error()
	for _, want := range []string{"1000", "reset-test-db", "stale boards"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "could not allocate a unique key prefix for board") {
		t.Errorf("message still uses the old opaque wording: %q", msg)
	}
}
