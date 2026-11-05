package relay

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunRejectsNegativeDedupeTTL checks the early validation added to Run: a
// negative -dedupe TTL must be rejected by Run itself, not only by the CLI, so
// a programmatic caller cannot silently get the disabled loop-suppression
// behaviour. The check runs before any socket is opened, so no seams are needed.
func TestRunRejectsNegativeDedupeTTL(t *testing.T) {
	cfg := Config{
		LanIf:      "lo",
		Peer:       "127.0.0.1",
		DedupeTTL:  -time.Second,
		StatusAddr: "off",
		Logf:       func(string, ...any) {},
		// Keep the pre-fix fall-through fast: without the TTL check, Run would
		// otherwise retry the LAN lookup for the full default budget.
		retryAttempts: 1,
		retryDelay:    time.Millisecond,
	}
	err := Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run succeeded with a negative DedupeTTL")
	}
	if !strings.Contains(err.Error(), "dedupe") {
		t.Fatalf("Run err = %v; want a dedupe-TTL rejection", err)
	}
}
