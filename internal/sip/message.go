package sip

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// sipMessage is a parsed SIP request or response. Only the subset this
// server speaks is modelled: enough for REGISTER / INVITE / ACK / BYE /
// CANCEL / OPTIONS, plus the responses we generate.
type sipMessage struct {
	// Request fields.
	IsRequest  bool
	Method     string
	RequestURI string
	// Response fields.
	StatusCode int
	Reason     string
	// Shared.
	Headers map[string][]string // canonical lower-case key -> values
	Body    string
	Raw     string
}

// header returns the first value of a header (case-insensitive), or "".
func (m *sipMessage) header(name string) string {
	vals := m.Headers[strings.ToLower(name)]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// headerAll returns all values of a header.
func (m *sipMessage) headerAll(name string) []string {
	return m.Headers[strings.ToLower(name)]
}

// parseSIPMessage parses a raw SIP datagram. It tolerates folded header
// continuation lines and is deliberately lenient about unknown headers.
func parseSIPMessage(raw string) (*sipMessage, error) {
	// Normalise line endings, then split head/body on the blank line.
	normalised := strings.ReplaceAll(raw, "\r\n", "\n")
	head, body, _ := strings.Cut(normalised, "\n\n")
	lines := strings.Split(head, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, fmt.Errorf("empty SIP message")
	}
	msg := &sipMessage{Headers: map[string][]string{}}
	first := strings.TrimSpace(lines[0])
	if strings.HasPrefix(first, "SIP/2.0") {
		parts := strings.SplitN(first, " ", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("malformed SIP status line: %q", first)
		}
		code, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("malformed SIP status code: %q", first)
		}
		msg.IsRequest = false
		msg.StatusCode = code
		if len(parts) == 3 {
			msg.Reason = parts[2]
		}
	} else {
		parts := strings.SplitN(first, " ", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed SIP request line: %q", first)
		}
		msg.IsRequest = true
		msg.Method = strings.ToUpper(parts[0])
		msg.RequestURI = parts[1]
	}
	var lastKey string
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && lastKey != "" {
			// Continuation of the previous header.
			vals := msg.Headers[lastKey]
			vals[len(vals)-1] += " " + strings.TrimSpace(line)
			msg.Headers[lastKey] = vals
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		msg.Headers[key] = append(msg.Headers[key], strings.TrimSpace(value))
		lastKey = key
	}
	msg.Body = body
	msg.Raw = raw
	return msg, nil
}

// buildResponse renders a SIP response to req. Extra headers are appended
// after the standard Via/From/To/Call-ID/CSeq set. toTag, when non-empty, is
// added to the To header (dialog creation).
func buildResponse(req *sipMessage, code int, reason string, toTag string, extra map[string]string, body string, contentType string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", code, reason)
	for _, via := range req.headerAll("Via") {
		fmt.Fprintf(&b, "Via: %s\r\n", via)
	}
	if from := req.header("From"); from != "" {
		fmt.Fprintf(&b, "From: %s\r\n", from)
	}
	if to := req.header("To"); to != "" {
		if toTag != "" && !strings.Contains(to, "tag=") {
			to += ";tag=" + toTag
		}
		fmt.Fprintf(&b, "To: %s\r\n", to)
	}
	if callID := req.header("Call-ID"); callID != "" {
		fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	}
	if cseq := req.header("CSeq"); cseq != "" {
		fmt.Fprintf(&b, "CSeq: %s\r\n", cseq)
	}
	if contact := req.header("Contact"); contact != "" && (code == 200) {
		// Echo the client's Contact only on 200 to REGISTER/INVITE handled
		// by the caller via extra headers; do not echo blindly here.
		_ = contact
	}
	for k, v := range extra {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	if contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\r\n", contentType)
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.WriteString(body)
	return b.String()
}

// buildRequest renders a SIP request.
func buildRequest(method, requestURI string, headers map[string]string, body string, contentType string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", method, requestURI)
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	if contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\r\n", contentType)
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.WriteString(body)
	return b.String()
}

// --- SIP header helpers -------------------------------------------------

// sipURIUser extracts the user part of a sip: URI ("sip:+86138...@host"
// -> "+86138..."). Returns "" when there is no user part.
func sipURIUser(uri string) string {
	uri = strings.TrimSpace(uri)
	uri = strings.Trim(uri, "<>")
	if i := strings.Index(uri, ":"); i >= 0 {
		uri = uri[i+1:]
	}
	if i := strings.Index(uri, "@"); i >= 0 {
		uri = uri[:i]
	}
	if i := strings.Index(uri, ";"); i >= 0 {
		uri = uri[:i]
	}
	return strings.TrimSpace(uri)
}

// headerParam extracts a ;param=value from a header value (e.g. tag, expires).
func headerParam(headerValue, param string) string {
	for _, part := range strings.Split(headerValue, ";") {
		part = strings.TrimSpace(part)
		if k, v, ok := strings.Cut(part, "="); ok && strings.EqualFold(strings.TrimSpace(k), param) {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// callIDOf returns the Call-ID of a message.
func (m *sipMessage) callID() string { return m.header("Call-ID") }

// cseqNum returns the numeric part of CSeq.
func (m *sipMessage) cseqNum() int {
	fields := strings.Fields(m.header("CSeq"))
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(fields[0])
	return n
}

// --- SDP helpers (PCMU/8000 only) --------------------------------------

// sdpMedia describes the audio m= line a peer offered or answered.
type sdpMedia struct {
	IP       string
	Port     int
	Payloads []int
	HasPCMU  bool
}

// parseSDP extracts the first audio media section from an SDP body.
func parseSDP(body string) sdpMedia {
	var media sdpMedia
	var sessionIP string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "c=IN IP4 ") {
			ip := strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
			if media.IP == "" {
				// First c= line is session-level; a media-level one overrides it.
				sessionIP = ip
				media.IP = ip
			} else {
				media.IP = ip
			}
		}
		if strings.HasPrefix(line, "m=audio ") {
			fields := strings.Fields(strings.TrimPrefix(line, "m=audio "))
			if len(fields) >= 1 {
				media.Port, _ = strconv.Atoi(fields[0])
			}
			for _, p := range fields[1:] {
				n, err := strconv.Atoi(p)
				if err != nil {
					continue
				}
				media.Payloads = append(media.Payloads, n)
				if n == 0 {
					media.HasPCMU = true
				}
			}
		}
	}
	if media.IP == "" {
		media.IP = sessionIP
	}
	return media
}

// buildSDP renders a minimal PCMU/8000 offer/answer for our RTP port.
func buildSDP(rtpPort int) string {
	return fmt.Sprintf("v=0\r\n"+
		"o=dj4hub 1 1 IN IP4 127.0.0.1\r\n"+
		"s=DJ4Hub call\r\n"+
		"c=IN IP4 127.0.0.1\r\n"+
		"t=0 0\r\n"+
		"m=audio %d RTP/AVP 0\r\n"+
		"a=rtpmap:0 PCMU/8000\r\n"+
		"a=ptime:20\r\n"+
		"a=sendrecv\r\n", rtpPort)
}

// remoteAddrForSDP resolves the RTP peer address from parsed SDP. When the
// SDP carries 0.0.0.0 or no address, the SIP signalling source is used.
func remoteAddrForSDP(media sdpMedia, signallingSrc *net.UDPAddr) *net.UDPAddr {
	ip := net.ParseIP(media.IP)
	if ip == nil || ip.IsUnspecified() || media.Port <= 0 {
		if signallingSrc == nil {
			return nil
		}
		return &net.UDPAddr{IP: signallingSrc.IP, Port: media.Port}
	}
	return &net.UDPAddr{IP: ip, Port: media.Port}
}
