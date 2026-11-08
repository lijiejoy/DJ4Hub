package sip

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// digestAuth implements the RFC 2617 / RFC 3261 Digest challenge used for
// SIP REGISTER and INVITE. Only the MD5 algorithm is supported (the same
// level Linphone negotiates against vohive's server); qop=auth is supported
// when the client sends it, plain (no-qop) responses are accepted too.

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// newNonce returns a random, server-tracked nonce valid for nonceTTL.
func (s *Server) newNonce() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failure is fatal for auth material; fall back to time-based
		// value rather than reusing a constant.
		return fmt.Sprintf("%x.%d", time.Now().UnixNano(), time.Now().Unix())
	}
	nonce := hex.EncodeToString(raw[:])
	s.mu.Lock()
	s.nonces[nonce] = time.Now().Add(nonceTTL)
	s.mu.Unlock()
	return nonce
}

func (s *Server) validNonce(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.nonces[nonce]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.nonces, nonce)
		return false
	}
	return true
}

func (s *Server) pruneNonces() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for nonce, exp := range s.nonces {
		if now.After(exp) {
			delete(s.nonces, nonce)
		}
	}
}

// expectedDigestResponse computes the response a client must send for the
// given method/uri/credentials.
func expectedDigestResponse(username, realm, password, method, uri, nonce, qop, nc, cnonce string) string {
	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)
	if qop == "auth" {
		return md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	}
	return md5Hex(ha1 + ":" + nonce + ":" + ha2)
}

// checkAuth verifies the Authorization header of msg for method. It returns
// (ok, username). A missing or invalid header means the caller must be
// challenged with 401 (challengeWWWAuthenticate builds the header).
func (s *Server) checkAuth(msg *sipMessage, method string) (bool, string) {
	cfg := s.Config()
	auth := msg.header("Authorization")
	if auth == "" {
		return false, ""
	}
	params := parseAuthParams(auth)
	username := params["username"]
	if username == "" || username != cfg.Username {
		return false, ""
	}
	nonce := params["nonce"]
	if nonce == "" || !s.validNonce(nonce) {
		return false, ""
	}
	if params["realm"] != cfg.Realm {
		return false, ""
	}
	uri := params["uri"]
	if uri == "" {
		return false, ""
	}
	want := expectedDigestResponse(username, cfg.Realm, cfg.Password, method, uri, nonce, params["qop"], params["nc"], params["cnonce"])
	if !strings.EqualFold(want, params["response"]) {
		return false, ""
	}
	return true, username
}

// challengeWWWAuthenticate renders the Digest challenge for a 401 response.
func (s *Server) challengeWWWAuthenticate() string {
	return fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5, qop="auth"`, s.Config().Realm, s.newNonce())
}

// parseAuthParams parses `Digest k="v", k2=v2` style header values.
func parseAuthParams(header string) map[string]string {
	out := map[string]string{}
	rest := strings.TrimSpace(header)
	if i := strings.Index(rest, " "); i >= 0 {
		rest = rest[i+1:]
	}
	for _, part := range splitAuthParams(rest) {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		val = strings.Trim(val, `"`)
		out[key] = val
	}
	return out
}

// splitAuthParams splits on commas that are not inside double quotes.
func splitAuthParams(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			cur.WriteRune(r)
		case r == ',' && !inQuotes:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}
