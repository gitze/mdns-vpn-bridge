package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatusHandlerServesJSON(t *testing.T) {
	c := &counters{}
	c.toPeerPkts.Store(42)
	c.toPeerBytes.Store(4096)
	c.toPeerDrop.Store(7)
	c.toLANPkts.Store(11)
	c.toLANBytes.Store(512)

	base := Status{
		LanIf: "ens18", LanIP: "10.0.0.2", Peer: "100.64.0.1", PeerPort: 53530,
		Lan2Peer: ModeQuery, Peer2LAN: ModeAll, Services: []string{"_http"},
		Started: time.Now().UTC().Add(-90 * time.Second).Format(time.RFC3339),
	}

	rec := httptest.NewRecorder()
	statusHandler(c, base).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}

	var got Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	if got.Peer != "100.64.0.1" || got.PeerPort != 53530 || got.Lan2Peer != ModeQuery {
		t.Errorf("unexpected identity fields: %+v", got)
	}
	if got.ToPeer.Packets != 42 || got.ToPeer.Bytes != 4096 || got.ToPeer.Dropped != 7 {
		t.Errorf("unexpected to_peer counters: %+v", got.ToPeer)
	}
	if got.ToLAN.Packets != 11 || got.ToLAN.Bytes != 512 {
		t.Errorf("unexpected to_lan counters: %+v", got.ToLAN)
	}
	if got.UptimeSeconds < 85 || got.UptimeSeconds > 200 {
		t.Errorf("uptime = %v, want ~90s", got.UptimeSeconds)
	}
}

func TestStatusHandlerRejectsWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	statusHandler(&counters{}, Status{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code = %d, want 405", rec.Code)
	}
}

func TestStatusUnknownPath(t *testing.T) {
	rec := httptest.NewRecorder()
	statusHandler(&counters{}, Status{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want 404", rec.Code)
	}
}

func TestSnapshotWithoutStartTime(t *testing.T) {
	// A missing/garbage Started value must not panic or produce nonsense.
	st := (&counters{}).snapshot(Status{Started: "not-a-time"})
	if st.UptimeSeconds < 0 {
		t.Fatalf("uptime = %v", st.UptimeSeconds)
	}
}

func TestStatusDedupeIsTopLevel(t *testing.T) {
	c := &counters{}
	c.dedupe.Store(123)
	c.toPeerPkts.Store(9)

	rec := httptest.NewRecorder()
	statusHandler(c, Status{Started: time.Now().UTC().Format(time.RFC3339)}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_status", nil))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	// The aggregate must sit at the top level so the existing UI regex keeps
	// matching; nesting it under to_peer/to_lan would break consumers.
	d, ok := raw["dedupe"]
	if !ok {
		t.Fatalf("top-level %q field missing: %s", "dedupe", rec.Body.String())
	}
	var got uint64
	if err := json.Unmarshal(d, &got); err != nil {
		t.Fatalf("dedupe not a number: %v", err)
	}
	if got != 123 {
		t.Fatalf("dedupe = %d, want 123", got)
	}
	for _, nested := range []string{"to_peer", "to_lan"} {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw[nested], &obj); err != nil {
			t.Fatalf("%s not an object: %v", nested, err)
		}
		if _, exists := obj["dedupe"]; exists {
			t.Errorf("dedupe must not be nested inside %s", nested)
		}
	}
}
