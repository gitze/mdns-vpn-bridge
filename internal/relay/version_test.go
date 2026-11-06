package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusExposesRunningVersion(t *testing.T) {
	for _, version := range []string{"9.8.7", "dev", ""} {
		t.Run(version, func(t *testing.T) {
			rec := httptest.NewRecorder()
			statusHandler(&counters{}, Status{Version: version}).ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "/_status", nil))
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := json.Unmarshal(body["version"], &got); err != nil {
				t.Fatalf("missing or invalid runtime version: %v", err)
			}
			if got != version {
				t.Fatalf("runtime version = %q, want %q", got, version)
			}
			for _, direction := range []string{"to_peer", "to_lan"} {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(body[direction], &fields); err != nil {
					t.Fatal(err)
				}
				if _, exists := fields["version"]; exists {
					t.Fatalf("version must not alter nested %s counters", direction)
				}
			}
		})
	}
}
