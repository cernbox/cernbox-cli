package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// clockServer answers every request with the given status and Date, so a test
// can check that neither the status nor the absence of a body gets in the way.
func clockServer(t *testing.T, status int, date string) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD: reading a clock must not ask for a body", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want none: the clock has to be readable "+
				"before a credential is", got)
		}
		if date == "" {
			// Suppress the one net/http supplies for a response without one.
			w.Header()["Date"] = nil
		} else {
			w.Header().Set("Date", date)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)

	// No credential source at all: a request that tried to authenticate would
	// fail here rather than quietly succeed.
	c, err := New(ts.URL, WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestServerTime(t *testing.T) {
	want := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	// 401 and 404 are the interesting statuses: they are what an endpoint
	// answers when the credential is the problem, or when nothing is served at
	// the root, and the clock is readable in both cases.
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusNotFound} {
		got, err := clockServer(t, status, want.Format(http.TimeFormat)).ServerTime(context.Background())
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if !got.Equal(want) {
			t.Errorf("status %d: got %s, want %s", status, got, want)
		}
	}
}

func TestServerTimeWithoutADateHeader(t *testing.T) {
	_, err := clockServer(t, http.StatusOK, "").ServerTime(context.Background())
	if err == nil {
		t.Fatal("a response with no Date must be an error, not a zero time")
	}
	if !strings.Contains(err.Error(), "Date") {
		t.Errorf("error does not say what was missing: %v", err)
	}
}

func TestServerTimeWithAnUnparsableDate(t *testing.T) {
	_, err := clockServer(t, http.StatusOK, "the day before yesterday").ServerTime(context.Background())
	if err == nil {
		t.Fatal("an unparsable Date must be an error")
	}
}
