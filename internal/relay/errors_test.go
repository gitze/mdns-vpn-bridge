package relay

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestErrorLimiterRateLimitsAndAggregates checks the per-direction error log
// limiter deterministically with an injected clock: the first error is logged,
// further errors inside the window are suppressed and aggregated, and the next
// window reports how many were suppressed.
func TestErrorLimiterRateLimitsAndAggregates(t *testing.T) {
	l := &errorLimiter{every: 10 * time.Second}
	t0 := time.Unix(1000, 0)

	if line, ok := l.record(t0, errors.New("boom")); !ok || line != "boom" {
		t.Fatalf("first record = %q, %v; want \"boom\", true", line, ok)
	}
	if line, ok := l.record(t0.Add(time.Second), errors.New("boom")); ok {
		t.Fatalf("record inside window = %q, %v; want suppressed", line, ok)
	}
	if line, ok := l.record(t0.Add(2*time.Second), errors.New("boom")); ok {
		t.Fatalf("record inside window = %q, %v; want suppressed", line, ok)
	}
	line, ok := l.record(t0.Add(10*time.Second), errors.New("boom"))
	if !ok || line != "boom (3 errors since last report)" {
		t.Fatalf("window boundary record = %q, %v; want aggregated line", line, ok)
	}
	// Nothing pending after the last emitted line.
	if line, ok := l.flush(); ok {
		t.Fatalf("flush with nothing pending = %q, %v; want empty", line, ok)
	}
}

// TestErrorLimiterFlushOnStop checks that errors accumulated just before
// shutdown are still summarised even though no line was due yet.
func TestErrorLimiterFlushOnStop(t *testing.T) {
	l := &errorLimiter{every: time.Hour}
	now := time.Unix(2000, 0)
	// first error is emitted, the next two are suppressed and aggregated
	l.record(now, errors.New("x"))
	l.record(now.Add(time.Second), nil2("x"))
	l.record(now.Add(2*time.Second), nil2("x"))

	line, ok := l.flush()
	if !ok {
		t.Fatal("flush returned nothing although errors were pending")
	}
	if line != "x (2 errors since last report)" {
		t.Fatalf("flush line = %q; want 2 suppressed errors", line)
	}
	if line, ok := l.flush(); ok {
		t.Fatalf("second flush = %q, %v; want empty", line, ok)
	}
}

// nil2 is a tiny helper so the aggregation test uses fresh error values with
// the same message; errors.Is/== identity must not matter to the limiter.
func nil2(s string) error { return errors.New(s) }

// TestStatusErrorsIsTopLevel verifies the additive top-level "errors" counter:
// existing consumers keep working, so the aggregate must not be nested under
// to_peer/to_lan.
func TestStatusErrorsIsTopLevel(t *testing.T) {
	c := &counters{}
	c.errors.Store(77)
	c.toPeerPkts.Store(9)

	rec := httptest.NewRecorder()
	statusHandler(c, Status{Started: time.Now().UTC().Format(time.RFC3339)}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_status", nil))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	e, ok := raw["errors"]
	if !ok {
		t.Fatalf("top-level %q field missing: %s", "errors", rec.Body.String())
	}
	var got uint64
	if err := json.Unmarshal(e, &got); err != nil {
		t.Fatalf("errors not a number: %v", err)
	}
	if got != 77 {
		t.Fatalf("errors = %d, want 77", got)
	}
	for _, nested := range []string{"to_peer", "to_lan"} {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw[nested], &obj); err != nil {
			t.Fatalf("%s not an object: %v", nested, err)
		}
		if _, exists := obj["errors"]; exists {
			t.Errorf("errors must not be nested inside %s", nested)
		}
	}
}
