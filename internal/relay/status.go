package relay

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// Counter holds one direction's packet statistics.
type Counter struct {
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
	Dropped uint64 `json:"dropped"`
}

// Status is the machine-readable state served on the status endpoint and
// printed by the "status" subcommand.
type Status struct {
	// Version comes from the running process, independent of installed package metadata.
	Version       string   `json:"version"`
	LanIf         string   `json:"lan_if"`
	LanIP         string   `json:"lan_ip"`
	Peer          string   `json:"peer"`
	PeerPort      int      `json:"peer_port"`
	Lan2Peer      Mode     `json:"lan2peer"`
	Peer2LAN      Mode     `json:"peer2lan"`
	Services      []string `json:"services,omitempty"`
	Started       string   `json:"started"`
	UptimeSeconds float64  `json:"uptime_seconds"`
	ToPeer        Counter  `json:"to_peer"`
	ToLAN         Counter  `json:"to_lan"`
	// Dedupe is the number of packets suppressed as duplicates in either
	// direction. It is a top-level field (not nested in to_peer/to_lan) so
	// existing consumers keep working; the aggregate is what the UI shows.
	Dedupe uint64 `json:"dedupe"`
	// Errors is the number of write failures (peer link or LAN multicast) in
	// either direction. Top-level and additive so the existing UI keeps
	// working; the aggregate is what the UI shows.
	Errors uint64 `json:"errors"`
}

// counters is the live statistics block of one relay instance.
type counters struct {
	toPeerPkts, toPeerBytes, toPeerDrop atomic.Uint64
	toLANPkts, toLANBytes, toLANDrop    atomic.Uint64
	dedupe                              atomic.Uint64
	errors                              atomic.Uint64
}

func (c *counters) snapshot(base Status) Status {
	base.ToPeer = Counter{
		Packets: c.toPeerPkts.Load(),
		Bytes:   c.toPeerBytes.Load(),
		Dropped: c.toPeerDrop.Load(),
	}
	base.ToLAN = Counter{
		Packets: c.toLANPkts.Load(),
		Bytes:   c.toLANBytes.Load(),
		Dropped: c.toLANDrop.Load(),
	}
	base.UptimeSeconds = time.Since(parseStarted(base.Started)).Seconds()
	base.Dedupe = c.dedupe.Load()
	base.Errors = c.errors.Load()
	return base
}

func parseStarted(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now()
}

// statusHandler serves GET /_status as JSON.
func statusHandler(c *counters, base Status) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/_status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(c.snapshot(base))
	})
	return mux
}
