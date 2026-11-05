package relay

import (
	"encoding/binary"
	"strings"
)

// Mode selects what is forwarded over the unicast link.
type Mode string

const (
	// ModeQuery forwards only DNS queries (QR=0). Use on the side whose own
	// announcements should not cross the link — typically the side with the
	// chatty LAN or a metered uplink.
	ModeQuery Mode = "query"
	// ModeAll forwards queries and responses.
	ModeAll Mode = "all"
)

// ParseMode validates a Mode string.
func ParseMode(s string) (Mode, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(s)))
	if m != ModeQuery && m != ModeAll {
		return "", &ModeError{s}
	}
	return m, nil
}

// ModeError reports an invalid mode value.
type ModeError struct{ Value string }

func (e *ModeError) Error() string {
	return "mode must be \"query\" or \"all\", got " + e.Value
}

// IsQuery reports whether an mDNS/DNS packet is a query (QR bit = 0).
// Queries must always be relayed: the peer's devices can only answer what
// they have heard.
//
// A full 12-byte header is required: the QR bit lives in the flags word, so a
// packet shorter than dnsHeaderLen cannot be a well-formed query and must not
// be classified as one.
func IsQuery(pkt []byte) bool {
	return len(pkt) >= dnsHeaderLen && pkt[2]&0x80 == 0
}

// DNS wire constants for the minimal parser. The parser only needs the header,
// the question section and the names embedded in resource records; it never
// interprets opaque RDATA (TXT text, A/AAAA addresses, ...).
const (
	dnsHeaderLen = 12

	// Structural bounds. A hostile or corrupt packet must not make the parser
	// spin, recurse or allocate without limit; every loop below is capped.
	maxNameLabels = 128 // labels in one name (after following pointers)
	maxNameJumps  = 128 // compression-pointer hops while resolving one name
	maxNameLen    = 255 // uncompressed wire length of one name

	dnsTypeA     = 1
	dnsTypeNS    = 2
	dnsTypeCNAME = 5
	dnsTypeSOA   = 6
	dnsTypePTR   = 12
	dnsTypeMX    = 15
	dnsTypeSRV   = 33
	dnsTypeDNAME = 39
)

// enumServiceLabels is the label sequence of the mDNS service-enumeration
// name "_services._dns-sd._udp.local". With an allowlist set this query must
// keep working, otherwise browsers can no longer discover which service types
// exist across the link.
var enumServiceLabels = []string{"_services", "_dns-sd", "_udp"}

// dnsError describes a structural violation in a packet.
type dnsError string

func (e dnsError) Error() string { return string(e) }

const (
	errShortPacket = dnsError("dns: packet shorter than header")
	errTruncated   = dnsError("dns: truncated field")
	errBadLabel    = dnsError("dns: invalid label")
	errBadPointer  = dnsError("dns: compression pointer out of range or looping")
	errBadName     = dnsError("dns: name too long or too many labels")
	errRdata       = dnsError("dns: rdata out of bounds")
	errTrailing    = dnsError("dns: trailing bytes after the last record")
)

// asciiLower returns s with ASCII 'A'..'Z' folded to 'a'..'z'. Every other
// byte — non-ASCII letters, invalid UTF-8, punctuation — is copied unchanged.
// DNS name comparison is ASCII-case-insensitive; strings.ToLower would instead
// fold Unicode (U+212A KELVIN SIGN -> 'k', U+FFFD for invalid bytes) and let a
// label match an allowlist entry it must not (R4).
func asciiLower(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// parseName reads one domain name starting at off. It returns the name as
// lowercased labels (the root label is omitted), the offset just past the name
// at its original position (a trailing compression pointer counts as two
// bytes; its target is not followed for this offset), and an error for any
// structural violation.
func parseName(msg []byte, off int) (labels []string, next int, err error) {
	next = -1
	hops := 0
	labelCount := 0
	nameLen := 0
	for {
		if off < 0 || off >= len(msg) {
			return nil, 0, errTruncated
		}
		b := msg[off]
		switch {
		case b == 0: // end of name
			off++
			// The root label (the terminating zero byte) is part of the wire
			// form and counts toward the 255-byte uncompressed limit.
			nameLen++
			if nameLen > maxNameLen {
				return nil, 0, errBadName
			}
			if next < 0 {
				next = off
			}
			return labels, next, nil
		case b&0xC0 == 0xC0: // compression pointer (11xxxxxx xxxxxxxx)
			if off+2 > len(msg) {
				return nil, 0, errTruncated
			}
			ptr := int(b&0x3F)<<8 | int(msg[off+1])
			if next < 0 {
				next = off + 2
			}
			hops++
			if hops > maxNameJumps {
				return nil, 0, errBadPointer
			}
			if ptr >= len(msg) {
				return nil, 0, errBadPointer
			}
			off = ptr
		case b&0xC0 != 0: // 0x40/0x80 are reserved and never valid here
			return nil, 0, errBadLabel
		default: // ordinary label
			l := int(b)
			if l == 0 || l > 63 {
				return nil, 0, errBadLabel
			}
			if off+1+l > len(msg) {
				return nil, 0, errTruncated
			}
			labelCount++
			if labelCount > maxNameLabels {
				return nil, 0, errBadName
			}
			nameLen += l + 1
			if nameLen > maxNameLen {
				return nil, 0, errBadName
			}
			labels = append(labels, asciiLower(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

// parseNames walks the header, question section and every resource record and
// collects each domain name that appears as a question name, a record owner
// name, or a name-valued RDATA:
//
//   - NS / CNAME / PTR / DNAME target
//   - SRV target (after the 6-byte priority/weight/port prefix)
//   - MX exchange (after the 2-byte preference)
//   - SOA mname and rname
//
// This is what makes the allowlist packet-level: a combined mDNS response
// carries the PTR (service type -> instance), the SRV/TXT records owned by the
// instance, and the A/AAAA record of the SRV target all in one packet. The
// instance name embeds the service type, so a single allowed service keeps the
// whole packet (and thus its dependent records) on the wire. Opaque RDATA such
// as TXT text is deliberately not searched.
func parseNames(msg []byte) ([][]string, error) {
	if len(msg) < dnsHeaderLen {
		return nil, errShortPacket
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	ns := int(binary.BigEndian.Uint16(msg[8:10]))
	ar := int(binary.BigEndian.Uint16(msg[10:12]))

	off := dnsHeaderLen
	var names [][]string

	for i := 0; i < qd; i++ {
		labels, next, err := parseName(msg, off)
		if err != nil {
			return nil, err
		}
		names = append(names, labels)
		off = next
		if off+4 > len(msg) { // qtype + qclass
			return nil, errTruncated
		}
		off += 4
	}

	for i, total := 0, an+ns+ar; i < total; i++ {
		labels, next, err := parseName(msg, off)
		if err != nil {
			return nil, err
		}
		names = append(names, labels)
		off = next
		if off+10 > len(msg) { // type(2)+class(2)+ttl(4)+rdlength(2)
			return nil, errTruncated
		}
		typ := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		rdata := off + 10
		if rdata+rdlen > len(msg) {
			return nil, errRdata
		}
		rend := rdata + rdlen // one past the last RDATA byte
		switch typ {
		case dnsTypeNS, dnsTypeCNAME, dnsTypePTR, dnsTypeDNAME:
			// The RDATA is exactly one domain name; the encoded name must
			// consume it byte for byte. parseName's next offset is the end of
			// the name at its original position, so any spill past RDLENGTH
			// into the following record (or any trailing junk) fails here.
			target, next, err := parseName(msg, rdata)
			if err != nil {
				return nil, err
			}
			if next != rend {
				return nil, errRdata
			}
			names = append(names, target)
		case dnsTypeSRV:
			// 6-byte priority/weight/port prefix, then exactly one name.
			if rdlen < 7 { // 6-byte prefix + at least the root label
				return nil, errRdata
			}
			target, next, err := parseName(msg, rdata+6)
			if err != nil {
				return nil, err
			}
			if next != rend {
				return nil, errRdata
			}
			names = append(names, target)
		case dnsTypeMX:
			// 2-byte preference prefix, then exactly one name.
			if rdlen < 3 { // 2-byte prefix + at least the root label
				return nil, errRdata
			}
			target, next, err := parseName(msg, rdata+2)
			if err != nil {
				return nil, err
			}
			if next != rend {
				return nil, errRdata
			}
			names = append(names, target)
		case dnsTypeSOA:
			// mname + rname, then exactly 20 numeric bytes
			// (serial, refresh, retry, expire, minimum).
			if rdlen < 22 { // two root labels + 20 numeric bytes
				return nil, errRdata
			}
			mname, n1, err := parseName(msg, rdata)
			if err != nil {
				return nil, err
			}
			names = append(names, mname)
			if n1 > rend-20 { // rname must leave room for the numeric tail
				return nil, errRdata
			}
			rname, n2, err := parseName(msg, n1)
			if err != nil {
				return nil, err
			}
			names = append(names, rname)
			if n2 != rend-20 {
				return nil, errRdata
			}
		}
		off = rend
	}
	// A well-formed DNS message ends exactly at the last field; bytes after it
	// are unaccounted for and must not be silently accepted.
	if off != len(msg) {
		return nil, errTrailing
	}
	return names, nil
}

// ServiceAllowed reports whether a packet mentions one of the allowed service
// types. An empty allowlist permits everything (no parsing happens at all).
//
// With an allowlist set, the packet is parsed and every domain name it carries
// is compared against each entry. An entry matches only at a label boundary and
// case-insensitively: "_http" matches "_http._tcp.local" but never
// "_https._tcp.local" or a "_http" substring inside TXT text. Entries may name
// a label sequence with dots ("_http._tcp").
//
// The service-enumeration name _services._dns-sd._udp is always allowed while
// an allowlist is set. A packet that cannot be parsed structurally is rejected
// (and the caller counts it as a drop).
func ServiceAllowed(pkt []byte, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	names, err := parseNames(pkt)
	if err != nil {
		return false
	}
	for _, labels := range names {
		if containsLabelSeq(labels, enumServiceLabels) {
			return true
		}
	}
	for _, entry := range allow {
		seq := splitName(entry)
		if len(seq) == 0 {
			continue
		}
		for _, labels := range names {
			if containsLabelSeq(labels, seq) {
				return true
			}
		}
	}
	return false
}

// containsLabelSeq reports whether seq occurs as a contiguous run of whole
// labels inside name. Both are expected lowercased.
func containsLabelSeq(name, seq []string) bool {
	if len(seq) == 0 || len(seq) > len(name) {
		return false
	}
	for i := 0; i+len(seq) <= len(name); i++ {
		match := true
		for j := range seq {
			if name[i+j] != seq[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// splitName turns an allowlist entry into lowercased labels, tolerating a
// trailing dot and repeated dots ("_http._tcp." -> ["_http","_tcp"]).
func splitName(s string) []string {
	s = strings.Trim(s, ".")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, asciiLower([]byte(p)))
		}
	}
	return out
}

// ParseServices splits a comma separated allowlist ("_http, _esphomelib")
// and normalises each entry. Empty input yields nil (= allow all).
func ParseServices(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
