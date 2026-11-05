package relay

import "testing"

// queryHeader is a minimal DNS packet with QR=0 (flags byte 0x0100 -> byte[2] top bit clear).
var query = []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

// responseHeader is a DNS packet with QR=1.
var response = []byte{0x00, 0x00, 0x81, 0x80, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

func TestIsQuery(t *testing.T) {
	if !IsQuery(query) {
		t.Error("QR=0 must be a query")
	}
	if IsQuery(response) {
		t.Error("QR=1 must be a response")
	}
	if IsQuery([]byte{0, 0}) {
		t.Error("truncated packet must not count as query")
	}
	if IsQuery(nil) {
		t.Error("empty packet must not count as query")
	}
}

func TestServiceAllowed(t *testing.T) {
	// A well-formed query for _http._tcp.local.
	pkt := queryFor("_http._tcp.local", dnsTypePTR)

	if !ServiceAllowed(pkt, nil) {
		t.Error("empty allowlist must allow everything")
	}
	if !ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("packet mentioning _http must pass an allowlist containing _http")
	}
	if ServiceAllowed(pkt, []string{"_ipp", "_esphomelib"}) {
		t.Error("packet without a listed type must be dropped")
	}
	if !ServiceAllowed([]byte{0, 0, 0x84, 0}, nil) {
		t.Error("empty allowlist must allow even tiny packets")
	}
}

func TestParseServices(t *testing.T) {
	got := ParseServices(" _http, _esphomelib ,,")
	if len(got) != 2 || got[0] != "_http" || got[1] != "_esphomelib" {
		t.Fatalf("unexpected result: %#v", got)
	}
	if ParseServices("") != nil {
		t.Error("empty input must yield nil (allow all)")
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{
		"query": ModeQuery, "ALL": ModeAll, " all ": ModeAll,
	} {
		m, err := ParseMode(in)
		if err != nil || m != want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v", in, m, err, want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Error("invalid mode must fail")
	}
}
