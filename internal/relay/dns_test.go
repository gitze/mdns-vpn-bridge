package relay

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Wire-format builders
// ---------------------------------------------------------------------------

// dnsHeader builds a 12-byte header with the given section counts.
func dnsHeader(qd, an, ns, ar int) []byte {
	h := make([]byte, dnsHeaderLen)
	binary.BigEndian.PutUint16(h[4:6], uint16(qd))
	binary.BigEndian.PutUint16(h[6:8], uint16(an))
	binary.BigEndian.PutUint16(h[8:10], uint16(ns))
	binary.BigEndian.PutUint16(h[10:12], uint16(ar))
	return h
}

// encodeName encodes a dotted name as length-prefixed labels.
func encodeName(name string) []byte {
	var b []byte
	for _, l := range strings.Split(strings.Trim(name, "."), ".") {
		if l == "" {
			continue
		}
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

// queryFor builds a single-question query packet.
func queryFor(name string, qtype uint16) []byte {
	p := dnsHeader(1, 0, 0, 0)
	p = append(p, encodeName(name)...)
	p = binary.BigEndian.AppendUint16(p, qtype)
	p = binary.BigEndian.AppendUint16(p, 1) // IN
	return p
}

// rr encodes one resource record with a raw (possibly compressed) owner name
// and raw rdata.
func rr(owner []byte, typ uint16, rdata []byte) []byte {
	b := append([]byte(nil), owner...)
	b = binary.BigEndian.AppendUint16(b, typ)
	b = binary.BigEndian.AppendUint16(b, 1)   // class IN
	b = binary.BigEndian.AppendUint32(b, 120) // TTL
	b = binary.BigEndian.AppendUint16(b, uint16(len(rdata)))
	return append(b, rdata...)
}

// srvRdata encodes the 6-byte SRV prefix followed by the target name.
func srvRdata(target string) []byte {
	b := make([]byte, 6) // priority, weight, port
	return append(b, encodeName(target)...)
}

// soaRdata encodes mname + rname followed by the 20-byte numeric tail
// (serial, refresh, retry, expire, minimum).
func soaRdata(mname, rname string) []byte {
	b := append(encodeName(mname), encodeName(rname)...)
	return append(b, make([]byte, 20)...)
}

// ---------------------------------------------------------------------------
// Label matching
// ---------------------------------------------------------------------------

func TestContainsLabelSeq(t *testing.T) {
	cases := []struct {
		name, seq string
		want      bool
	}{
		{"_http._tcp.local", "_http", true},
		{"_http._tcp.local", "_http._tcp", true},
		{"_http._tcp.local", "_tcp", true},
		{"_https._tcp.local", "_http", false}, // label boundary, not substring
		{"_http-alt._tcp.local", "_http", false},
		{"x_http._tcp.local", "_http", false},
		{"_http._tcp.local", "_http._tcp.local", true},
		{"_http._tcp.local", "_tcp.local", true},
		{"local", "_http", false},
		{"", "_http", false},
		{"_http._tcp.local", "", false},
	}
	for _, c := range cases {
		got := containsLabelSeq(splitName(c.name), splitName(c.seq))
		if got != c.want {
			t.Errorf("containsLabelSeq(%q, %q) = %v, want %v", c.name, c.seq, got, c.want)
		}
	}
}

func TestSplitName(t *testing.T) {
	cases := map[string][]string{
		"_http":        {"_http"},
		"_HTTP._TCP.":  {"_http", "_tcp"},
		"._http.._tcp": {"_http", "_tcp"},
		"":             nil,
		".":            nil,
	}
	for in, want := range cases {
		got := splitName(in)
		if len(got) != len(want) {
			t.Fatalf("splitName(%q) = %#v, want %#v", in, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("splitName(%q) = %#v, want %#v", in, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Allowlist matching against real packet bytes
// ---------------------------------------------------------------------------

func TestServiceAllowedLabelBoundary(t *testing.T) {
	if ServiceAllowed(queryFor("_https._tcp.local", dnsTypePTR), []string{"_http"}) {
		t.Error("_http must not match _https")
	}
	if ServiceAllowed(queryFor("_http-alt._tcp.local", dnsTypePTR), []string{"_http"}) {
		t.Error("_http must not match _http-alt")
	}
	if !ServiceAllowed(queryFor("_http._tcp.local", dnsTypePTR), []string{"_http"}) {
		t.Error("_http must match _http._tcp.local")
	}
	if !ServiceAllowed(queryFor("_http._tcp.local", dnsTypePTR), []string{"_http._tcp"}) {
		t.Error("dotted entry _http._tcp must match")
	}
	if !ServiceAllowed(queryFor("_http._tcp.local", dnsTypePTR), []string{"_tcp"}) {
		t.Error("suffix label entry _tcp must match")
	}
}

func TestServiceAllowedCaseInsensitive(t *testing.T) {
	// Packet uses mixed case; allowlist uses lower case, and vice versa.
	if !ServiceAllowed(queryFor("_HTTP._TCP.local", dnsTypePTR), []string{"_http"}) {
		t.Error("matching must be case-insensitive (upper packet)")
	}
	if !ServiceAllowed(queryFor("_http._tcp.local", dnsTypePTR), []string{"_HTTP"}) {
		t.Error("matching must be case-insensitive (upper allowlist)")
	}
	if !ServiceAllowed(queryFor("_HtTp._tCp.local", dnsTypePTR), []string{"_hTtP._TcP"}) {
		t.Error("matching must be case-insensitive (mixed)")
	}
}

func TestServiceAllowedEnumerationAlwaysAllowed(t *testing.T) {
	enum := queryFor("_services._dns-sd._udp.local", dnsTypePTR)
	if !ServiceAllowed(enum, []string{"_http"}) {
		t.Error("service enumeration must survive an allowlist")
	}
	// Enumeration as a response record owner too.
	resp := append(dnsHeader(0, 1, 0, 0),
		rr(encodeName("_services._dns-sd._udp.local"), dnsTypePTR,
			encodeName("_http._tcp.local"))...)
	if !ServiceAllowed(resp, []string{"_ipp"}) {
		t.Error("enumeration response must survive an allowlist")
	}
}

// TestServiceAllowedPTRAndDependencies checks the packet-level allowlist: a
// combined mDNS response bundling PTR + SRV + TXT + A for an allowed service
// passes even though the A record alone mentions no service type.
func TestServiceAllowedPTRAndDependencies(t *testing.T) {
	svc := encodeName("_http._tcp.local")
	inst := encodeName("Printer._http._tcp.local")
	host := encodeName("printer.local")

	pkt := dnsHeader(0, 1, 0, 3)
	pkt = append(pkt, rr(svc, dnsTypePTR, inst)...)
	pkt = append(pkt, rr(inst, dnsTypeSRV, srvRdata("printer.local"))...)
	pkt = append(pkt, rr(inst, 16, []byte{4, 'p', 'a', 't', 'h'})...) // TXT
	pkt = append(pkt, rr(host, dnsTypeA, []byte{192, 168, 1, 9})...)

	if !ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("combined response for an allowed service must pass")
	}
	// A dependency packet that only carries the A record (no service name) is
	// dropped: a stateless packet-level allowlist cannot link it to the
	// service. This is the deliberate enumeration policy.
	aOnly := append(dnsHeader(0, 1, 0, 0), rr(host, dnsTypeA, []byte{192, 168, 1, 9})...)
	if ServiceAllowed(aOnly, []string{"_http"}) {
		t.Error("standalone A record without a service name must be dropped")
	}
}

// TestServiceAllowedIgnoresTXTText guards the original substring bug: a TXT
// record whose *text* contains "_http" must not satisfy the allowlist.
func TestServiceAllowedIgnoresTXTText(t *testing.T) {
	owner := encodeName("something.local")
	txt := append([]byte{6}, []byte("_http!")...)
	pkt := append(dnsHeader(0, 1, 0, 0), rr(owner, 16, txt)...)
	if ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("_http inside TXT text must not match")
	}
}

func TestServiceAllowedCompressedNames(t *testing.T) {
	// Two records share the owner name; the second uses a compression pointer
	// to the first owner's offset (12).
	svc := encodeName("_http._tcp.local")
	pkt := dnsHeader(0, 2, 0, 0)
	pkt = append(pkt, rr(svc, dnsTypePTR, encodeName("Printer._http._tcp.local"))...)
	// Pointer 0xC00C -> offset 12 (the start of the first owner name).
	pkt = append(pkt, rr([]byte{0xC0, 0x0C}, 16, []byte{0})...)

	if !ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("compressed owner names must be followed")
	}
}

func TestServiceAllowedPointerIntoInstance(t *testing.T) {
	// SRV owner is a pointer into the PTR rdata target so the service type
	// appears only in a compressed name.
	svc := encodeName("_http._tcp.local")
	pkt := dnsHeader(0, 2, 0, 0)
	first := rr(svc, dnsTypePTR, encodeName("Printer._http._tcp.local"))
	// Offset of the PTR rdata target: 12 + len(svc) + 10.
	targetOff := dnsHeaderLen + len(svc) + 10
	pkt = append(pkt, first...)
	pkt = append(pkt, rr([]byte{0xC0, byte(targetOff)}, dnsTypeSRV, srvRdata("printer.local"))...)

	if !ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("pointer into a service instance name must be followed")
	}
}

// ---------------------------------------------------------------------------
// Malformed / hostile packets: with an allowlist set they must be dropped.
// ---------------------------------------------------------------------------

func TestServiceAllowedMalformed(t *testing.T) {
	allow := []string{"_http"}

	cases := map[string][]byte{
		"empty":             {},
		"short header":      {0, 0, 0, 0, 0, 1},
		"header only":       dnsHeader(1, 0, 0, 0), // question claimed but absent
		"truncated label":   {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 5, '_', 'h'},
		"reserved bits":     {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0, 0, 0},
		"pointer oob":       {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 0xFF, 0, 0, 0, 0, 0},
		"pointer self loop": {0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 0x0C, 0, 0, 0, 0, 0},
		"label too long":    append(dnsHeader(1, 0, 0, 0), append([]byte{64}, make([]byte, 64)...)...),
	}

	for name, pkt := range cases {
		if ServiceAllowed(pkt, allow) {
			t.Errorf("%s: malformed packet must be dropped with an allowlist", name)
		}
	}
}

func TestServiceAllowedRDATABounds(t *testing.T) {
	// A record claims rdlength beyond the packet end.
	pkt := dnsHeader(0, 1, 0, 0)
	pkt = append(pkt, encodeName("_http._tcp.local")...)
	pkt = binary.BigEndian.AppendUint16(pkt, dnsTypePTR)
	pkt = binary.BigEndian.AppendUint16(pkt, 1)
	pkt = binary.BigEndian.AppendUint32(pkt, 120)
	pkt = binary.BigEndian.AppendUint16(pkt, 500) // rdlength, way past the end
	if ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("rdlength beyond the packet must be dropped")
	}
}

func TestServiceAllowedSRVShortRdata(t *testing.T) {
	// SRV with rdlength < 6 (the fixed prefix) is structurally invalid.
	pkt := dnsHeader(0, 1, 0, 0)
	pkt = append(pkt, rr(encodeName("_http._tcp.local"), dnsTypeSRV, []byte{0, 0, 0})...)
	if ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("SRV with a short rdata must be dropped")
	}
}

func TestServiceAllowedNoAllowlistIgnoresGarbage(t *testing.T) {
	// Without an allowlist no parsing happens; garbage still passes.
	if !ServiceAllowed([]byte{0xDE, 0xAD, 0xBE, 0xEF}, nil) {
		t.Error("empty allowlist must allow arbitrary bytes")
	}
}

// ---------------------------------------------------------------------------
// Parser bounds
// ---------------------------------------------------------------------------

func TestParseNamesPointerLoop(t *testing.T) {
	// Pointer that points to itself at offset 12.
	pkt := dnsHeader(1, 0, 0, 0)
	pkt = append(pkt, 0xC0, 0x0C)
	pkt = append(pkt, 0, 0, 0, 0) // qtype+qclass so the walk could proceed
	if _, err := parseNames(pkt); err == nil {
		t.Error("self-referential compression pointer must be rejected")
	}
}

func TestParseNamesTwoPointerLoop(t *testing.T) {
	// 12 -> 14 -> 12 : mutual pointer loop.
	pkt := dnsHeader(1, 0, 0, 0)
	pkt = append(pkt, 0xC0, 0x0E, 0xC0, 0x0C)
	pkt = append(pkt, 0, 0, 0, 0)
	if _, err := parseNames(pkt); err == nil {
		t.Error("mutual compression pointer loop must be rejected")
	}
}

func TestParseNamesCollectsTargets(t *testing.T) {
	svc := encodeName("_ipp._tcp.local")
	pkt := dnsHeader(0, 1, 0, 2)
	pkt = append(pkt, rr(svc, dnsTypePTR, encodeName("HP._ipp._tcp.local"))...)
	pkt = append(pkt, rr(encodeName("HP._ipp._tcp.local"), dnsTypeSRV, srvRdata("hp.local"))...)
	pkt = append(pkt, rr(encodeName("hp.local"), dnsTypeA, []byte{10, 0, 0, 5})...)

	names, err := parseNames(pkt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	flat := make([]string, 0, len(names))
	for _, n := range names {
		flat = append(flat, strings.Join(n, "."))
	}
	joined := strings.Join(flat, "|")
	for _, want := range []string{"_ipp._tcp.local", "hp._ipp._tcp.local", "hp.local"} {
		if !strings.Contains(joined, want) {
			t.Errorf("parsed names %q missing %q", joined, want)
		}
	}
}

// ---------------------------------------------------------------------------
// IsQuery header contract
// ---------------------------------------------------------------------------

// TestIsQueryRequiresFullHeader pins the contract that a query needs a full
// 12-byte header: the QR bit lives in the flags word, so a 3-byte packet with
// its low bit clear must not be classified as a query.
func TestIsQueryRequiresFullHeader(t *testing.T) {
	if IsQuery([]byte{0, 0, 0x00}) {
		t.Error("a 3-byte QR=0 packet must not count as a query")
	}
	if IsQuery([]byte{0, 0, 0x00, 0, 0}) {
		t.Error("a 5-byte QR=0 packet must not count as a query")
	}
	if !IsQuery(dnsHeader(1, 0, 0, 0)) {
		t.Error("a bare 12-byte query header must count as a query")
	}
	if !IsQuery(queryFor("_http._tcp.local", dnsTypePTR)) {
		t.Error("a full query packet must count as a query")
	}
	// A 12-byte header with QR=1 is a response, not a query.
	resp := dnsHeader(0, 0, 0, 0)
	resp[2] = 0x80
	if IsQuery(resp) {
		t.Error("a 12-byte QR=1 header must not count as a query")
	}
}

// ---------------------------------------------------------------------------
// RDATA name consumption: the encoded name must stay inside its own RDLENGTH.
// These are regressions for the bug where parseName's next offset was ignored,
// letting a name spill past RDLENGTH into the following record and letting an
// SOA omit its mandatory 20-byte numeric tail.
// ---------------------------------------------------------------------------

// TestServiceAllowedRejectsNameSpillPastRdlength: a PTR claims a 2-byte RDATA
// but actually carries a full uncompressed name. The name must not be read
// past RDLENGTH; the packet is dropped.
func TestServiceAllowedRejectsNameSpillPastRdlength(t *testing.T) {
	name := encodeName("_http._tcp.local")
	pkt := dnsHeader(0, 1, 0, 0)
	pkt = append(pkt, encodeName("_http._tcp.local")...)
	pkt = binary.BigEndian.AppendUint16(pkt, dnsTypePTR)
	pkt = binary.BigEndian.AppendUint16(pkt, 1)
	pkt = binary.BigEndian.AppendUint32(pkt, 120)
	pkt = binary.BigEndian.AppendUint16(pkt, 2) // lies: claims 2 RDATA bytes
	pkt = append(pkt, name...)                  // but the name is len(name) bytes

	if ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("a name spilling past RDLENGTH must be dropped")
	}
}

// TestServiceAllowedRejectsPTRTrailingRdata: the encoded name ends before
// RDLENGTH does, leaving unaccounted bytes inside the RDATA. The name must
// consume the RDATA exactly.
func TestServiceAllowedRejectsPTRTrailingRdata(t *testing.T) {
	rdata := append(encodeName("_http._tcp.local"), 0xDE, 0xAD)
	pkt := append(dnsHeader(0, 1, 0, 0),
		rr(encodeName("_http._tcp.local"), dnsTypePTR, rdata)...)
	if ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("a PTR with trailing RDATA bytes must be dropped")
	}
}

// TestServiceAllowedRejectsSOAMissingNumericTail: an SOA whose RDATA ends
// right after rname (no 20-byte numeric tail) is structurally invalid.
func TestServiceAllowedRejectsSOAMissingNumericTail(t *testing.T) {
	rdata := append(encodeName("ns._http._tcp.local"), encodeName("host._http._tcp.local")...)
	pkt := append(dnsHeader(0, 1, 0, 0),
		rr(encodeName("_http._tcp.local"), dnsTypeSOA, rdata)...)
	if ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("an SOA without its 20-byte numeric tail must be dropped")
	}
}

// TestServiceAllowedRejectsSOATruncatedTail: rname leaves fewer than 20 bytes
// for the numeric tail.
func TestServiceAllowedRejectsSOATruncatedTail(t *testing.T) {
	rdata := append(encodeName("ns._http._tcp.local"), encodeName("host._http._tcp.local")...)
	rdata = append(rdata, make([]byte, 19)...) // one byte short of the tail
	pkt := append(dnsHeader(0, 1, 0, 0),
		rr(encodeName("_http._tcp.local"), dnsTypeSOA, rdata)...)
	if ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("an SOA with a truncated numeric tail must be dropped")
	}
}

// TestServiceAllowedAcceptsValidSOA guards against over-rejection: a
// well-formed SOA (mname + rname + 20 numeric bytes) must pass.
func TestServiceAllowedAcceptsValidSOA(t *testing.T) {
	pkt := append(dnsHeader(0, 1, 0, 0),
		rr(encodeName("_http._tcp.local"), dnsTypeSOA,
			soaRdata("ns._http._tcp.local", "host._http._tcp.local"))...)
	if !ServiceAllowed(pkt, []string{"_http"}) {
		t.Error("a well-formed SOA must pass an allowlist it mentions")
	}
}

// TestServiceAllowedAcceptsExactLengths guards the exact-length rule against
// over-rejection for every name-valued RDATA type.
func TestServiceAllowedAcceptsExactLengths(t *testing.T) {
	owner := encodeName("_http._tcp.local")
	cases := map[string][]byte{
		"PTR":   rr(owner, dnsTypePTR, encodeName("Printer._http._tcp.local")),
		"CNAME": rr(owner, dnsTypeCNAME, encodeName("alias._http._tcp.local")),
		"NS":    rr(owner, dnsTypeNS, encodeName("ns._http._tcp.local")),
		"DNAME": rr(owner, dnsTypeDNAME, encodeName("dn._http._tcp.local")),
		"SRV":   rr(owner, dnsTypeSRV, srvRdata("host._http._tcp.local")),
		"MX":    rr(owner, dnsTypeMX, append([]byte{0, 10}, encodeName("mx._http._tcp.local")...)),
	}
	for name, rec := range cases {
		pkt := append(dnsHeader(0, 1, 0, 0), rec...)
		if !ServiceAllowed(pkt, []string{"_http"}) {
			t.Errorf("%s with an exact-length RDATA must pass", name)
		}
	}
}

// TestParseNamesRejectsNameOver255Bytes: 63+63+63+62 label bytes are 255 bytes
// of labels, but the root label pushes the wire name to 256 — over the
// 255-byte limit once the terminating zero byte is counted.
func TestParseNamesRejectsNameOver255Bytes(t *testing.T) {
	name := make([]byte, 0, 257)
	for _, l := range []int{63, 63, 63, 62} {
		name = append(name, byte(l))
		name = append(name, bytes.Repeat([]byte{'a'}, l)...)
	}
	name = append(name, 0) // root terminator

	pkt := append(dnsHeader(1, 0, 0, 0), name...)
	pkt = binary.BigEndian.AppendUint16(pkt, dnsTypePTR)
	pkt = binary.BigEndian.AppendUint16(pkt, 1)
	if _, err := parseNames(pkt); err == nil {
		t.Error("a name longer than 255 wire bytes must be rejected")
	}
}

// TestParseNamesAcceptsNameAt255Bytes: the same name one label byte shorter is
// exactly 255 wire bytes including the root and must be accepted.
func TestParseNamesAcceptsNameAt255Bytes(t *testing.T) {
	name := make([]byte, 0, 256)
	for _, l := range []int{63, 63, 63, 61} {
		name = append(name, byte(l))
		name = append(name, bytes.Repeat([]byte{'a'}, l)...)
	}
	name = append(name, 0) // root terminator -> 255 bytes

	pkt := append(dnsHeader(1, 0, 0, 0), name...)
	pkt = binary.BigEndian.AppendUint16(pkt, dnsTypePTR)
	pkt = binary.BigEndian.AppendUint16(pkt, 1)
	if _, err := parseNames(pkt); err != nil {
		t.Errorf("a 255-byte name must be accepted, got %v", err)
	}
}

// TestParseNamesRejectsTrailingBytes: a packet with bytes after the last
// record is not a well-formed message.
func TestParseNamesRejectsTrailingBytes(t *testing.T) {
	pkt := append(queryFor("_http._tcp.local", dnsTypePTR), 0xDE, 0xAD)
	if _, err := parseNames(pkt); err == nil {
		t.Error("trailing bytes after the last record must be rejected")
	}
}

// ---------------------------------------------------------------------------
// Fuzzing
// ---------------------------------------------------------------------------

// maxFuzzPacket bounds the input the fuzz target will parse. parseNames never
// allocates per input byte, but capping keeps a single fuzz input small so the
// engine cannot drive memory or runtime up with a huge slice.
const maxFuzzPacket = 4096

// FuzzServiceAllowed drives the packet parser and the allowlist decision with
// arbitrary bytes. It must never panic, hang or allocate without bound. With
// an empty allowlist no parsing happens at all (the caller's min-12 length
// contract in Run is the only gate), so that path must always return true.
func FuzzServiceAllowed(f *testing.F) {
	// Valid seeds: a PTR query and a combined PTR + SRV response whose second
	// owner name is a compression pointer into the first.
	f.Add(queryFor("_http._tcp.local", dnsTypePTR))
	svc := encodeName("_http._tcp.local")
	combined := dnsHeader(0, 2, 0, 0)
	combined = append(combined, rr(svc, dnsTypePTR, encodeName("Printer._http._tcp.local"))...)
	combined = append(combined, rr([]byte{0xC0, 0x0C}, dnsTypeSRV, srvRdata("printer.local"))...)
	f.Add(combined)
	f.Add(append(dnsHeader(0, 1, 0, 0),
		rr(svc, dnsTypeSOA, soaRdata("ns._http._tcp.local", "host._http._tcp.local"))...))

	// Malformed seeds: empty, short header, reserved label bits, out-of-range
	// and self-referential compression pointers, and trailing junk.
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 1})
	f.Add([]byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 0xFF, 0, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 0x0C, 0, 0, 0, 0, 0})
	f.Add(append(queryFor("_http._tcp.local", dnsTypePTR), 0xDE, 0xAD))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzPacket {
			data = data[:maxFuzzPacket]
		}
		// Empty allowlist: no parsing, always allowed.
		if !ServiceAllowed(data, nil) {
			t.Fatal("empty allowlist must allow any bytes")
		}
		// With an allowlist the parser runs on untrusted input.
		_ = ServiceAllowed(data, []string{"_http", "_ipp", "_services._dns-sd._udp"})
		_, _ = parseNames(data)
	})
}

// ---------------------------------------------------------------------------
// ASCII-only case folding (R4)
// ---------------------------------------------------------------------------

// TestAsciiLower pins the byte-for-byte contract of asciiLower: only A-Z fold;
// non-ASCII letters and invalid UTF-8 are copied unchanged.
func TestAsciiLower(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ABC", "abc"},
		{"_HTTP._TCP", "_http._tcp"},
		{"MiXeD-123", "mixed-123"},
		{"\u212a", "\u212a"},     // KELVIN SIGN unchanged (strings.ToLower -> "k")
		{"k", "k"},               // plain ASCII k unchanged
		{"\xff\x80", "\xff\x80"}, // invalid UTF-8 unchanged
		{"", ""},
	}
	for _, c := range cases {
		if got := asciiLower([]byte(c.in)); got != c.want {
			t.Errorf("asciiLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestServiceAllowedAsciiCaseFolding pins R4: only ASCII A-Z may fold. A label
// containing the Kelvin sign (U+212A), which strings.ToLower folds to 'k', must
// not match an allowlist entry "k"; the exact raw bytes stay matchable.
func TestServiceAllowedAsciiCaseFolding(t *testing.T) {
	kelvin := queryFor("\u212a._tcp.local", dnsTypePTR)
	if ServiceAllowed(kelvin, []string{"k"}) {
		t.Error("U+212A must not fold to ASCII k")
	}
	if !ServiceAllowed(kelvin, []string{"_tcp"}) {
		t.Error("_tcp must still match the rest of the name")
	}
	if !ServiceAllowed(kelvin, []string{"\u212a"}) {
		t.Error("an allowlist entry with the exact raw bytes must match")
	}
}

// TestServiceAllowedInvalidUTF8Unchanged pins R4: invalid UTF-8 label bytes are
// compared verbatim, never folded to a replacement character or a letter.
func TestServiceAllowedInvalidUTF8Unchanged(t *testing.T) {
	raw := queryFor("\xff\x80._tcp.local", dnsTypePTR)
	if !ServiceAllowed(raw, []string{"_tcp"}) {
		t.Error("_tcp must still match the rest of the name")
	}
	if ServiceAllowed(raw, []string{"k"}) {
		t.Error("invalid UTF-8 bytes must not match an unrelated entry")
	}
	if !ServiceAllowed(raw, []string{"\xff\x80"}) {
		t.Error("an allowlist entry with the exact raw bytes must match")
	}
}
