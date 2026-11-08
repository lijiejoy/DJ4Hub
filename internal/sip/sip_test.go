package sip

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUlawSilenceAndRoundtrip(t *testing.T) {
	if got := LinearToUlaw(0); got != 0xFF {
		t.Fatalf("silence encodes to %#x, want 0xFF", got)
	}
	if got := UlawToLinear(0xFF); got != 0 {
		t.Fatalf("0xFF decodes to %d, want 0", got)
	}
	// Roundtrip stays within one mu-law step for loud signals.
	for _, sample := range []int16{0, 100, -100, 1000, -1000, 8000, -8000, 16000, -16000, 30000} {
		back := UlawToLinear(LinearToUlaw(sample))
		diff := int(back) - int(sample)
		if diff < 0 {
			diff = -diff
		}
		if diff > 512 {
			t.Fatalf("roundtrip %d -> %d (diff %d)", sample, back, diff)
		}
	}
	// Known vector: 0xD5 is a commonly tabulated mu-law value for a mid sample.
	if got := UlawToLinear(0xD5); got == 0 {
		t.Fatalf("0xD5 decoded to 0, expected non-zero")
	}
}

func TestParseSIPMessage(t *testing.T) {
	raw := "INVITE sip:13800138000@127.0.0.1:5060 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:5061;branch=z9hG4bKabc\r\n" +
		"From: <sip:linphone@vohive.local>;tag=from1\r\n" +
		"To: <sip:13800138000@127.0.0.1:5060>\r\n" +
		"Call-ID: call1@test\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Contact: <sip:linphone@127.0.0.1:5061>\r\n" +
		"Content-Type: application/sdp\r\n" +
		"Content-Length: 4\r\n\r\n" +
		"v=0\r\n"
	msg, err := parseSIPMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.IsRequest || msg.Method != "INVITE" {
		t.Fatalf("method = %q isRequest=%v", msg.Method, msg.IsRequest)
	}
	if got := sipURIUser(msg.RequestURI); got != "13800138000" {
		t.Fatalf("uri user = %q", got)
	}
	if msg.callID() != "call1@test" || msg.cseqNum() != 1 {
		t.Fatalf("call-id/cseq = %q/%d", msg.callID(), msg.cseqNum())
	}
	if got := headerParam(msg.header("From"), "tag"); got != "from1" {
		t.Fatalf("from tag = %q", got)
	}
}

func TestParseSDP(t *testing.T) {
	body := "v=0\r\no=x 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0 8\r\na=rtpmap:0 PCMU/8000\r\n"
	media := parseSDP(body)
	if media.Port != 4000 || !media.HasPCMU || media.IP != "127.0.0.1" {
		t.Fatalf("parsed SDP = %+v", media)
	}
	if got := buildSDP(10002); !strings.Contains(got, "m=audio 10002 RTP/AVP 0") {
		t.Fatalf("built SDP missing m= line: %q", got)
	}
}

func TestDigestRoundtrip(t *testing.T) {
	srv := New(Config{Realm: "vohive.local", Username: "linphone", Password: "secret123"}, nil)
	nonce := srv.newNonce()
	resp := expectedDigestResponse("linphone", "vohive.local", "secret123", "REGISTER", "sip:vohive.local", nonce, "", "", "")
	msg := &sipMessage{Headers: map[string][]string{
		"authorization": {fmt.Sprintf(`Digest username="linphone", realm="vohive.local", nonce="%s", uri="sip:vohive.local", response="%s"`, nonce, resp)},
	}}
	if ok, user := srv.checkAuth(msg, "REGISTER"); !ok || user != "linphone" {
		t.Fatalf("valid digest rejected (ok=%v user=%q)", ok, user)
	}
	// Wrong password must fail.
	bad := &sipMessage{Headers: map[string][]string{
		"authorization": {fmt.Sprintf(`Digest username="linphone", realm="vohive.local", nonce="%s", uri="sip:vohive.local", response="deadbeef"`, nonce)},
	}}
	if ok, _ := srv.checkAuth(bad, "REGISTER"); ok {
		t.Fatalf("invalid digest accepted")
	}
	// Unknown nonce must fail.
	unknown := &sipMessage{Headers: map[string][]string{
		"authorization": {`Digest username="linphone", realm="vohive.local", nonce="nothere", uri="sip:vohive.local", response="deadbeef"`},
	}}
	if ok, _ := srv.checkAuth(unknown, "REGISTER"); ok {
		t.Fatalf("unknown nonce accepted")
	}
}

func TestConfigFirstRunPassword(t *testing.T) {
	dir := t.TempDir()
	cfg, generated, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !generated {
		t.Fatalf("first run should generate a password")
	}
	if len(cfg.Password) != 20 {
		t.Fatalf("password length = %d", len(cfg.Password))
	}
	if cfg.Realm != "vohive.local" || cfg.Listen != "127.0.0.1:5060" || cfg.RTPMin != 10000 || cfg.RTPMax != 20000 {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	// Second load reuses the stored password and does not regenerate.
	cfg2, generated2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if generated2 || cfg2.Password != cfg.Password {
		t.Fatalf("second load regenerated or changed the password")
	}
	// Password file must be 0600 and the JSON must not contain it.
	if info, err := statMode(dir + "/sip-password"); err != nil || info != 0o600 {
		t.Fatalf("sip-password mode = %v err=%v", info, err)
	}
	raw := readFile(t, dir+"/sip-config.json")
	if strings.Contains(string(raw), cfg.Password) {
		t.Fatalf("password leaked into sip-config.json")
	}
}

// --- Live UDP flow tests (loopback, no modem) ---------------------------

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}

func startTestServer(t *testing.T) (*Server, int) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Listen = fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t))
	cfg.Password = "testpass123"
	srv := New(cfg, &QPCMVBridge{})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)
	addr := srv.conn.LocalAddr().(*net.UDPAddr)
	return srv, addr.Port
}

func sipClient(t *testing.T, port int) *net.UDPConn {
	t.Helper()
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func sendAndRecv(t *testing.T, conn *net.UDPConn, req string) string {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no reply to %q: %v", firstLine(req), err)
	}
	return string(buf[:n])
}

func firstLine(s string) string {
	if i := strings.Index(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func digestHeader(t *testing.T, srv *Server, username, method, uri string) string {
	t.Helper()
	// Ask for a challenge first is the client flow; here we mint a nonce the
	// same way the server does so the test stays deterministic.
	nonce := srv.newNonce()
	resp := expectedDigestResponse(username, "vohive.local", "testpass123", method, uri, nonce, "", "", "")
	return fmt.Sprintf(`Digest username="%s", realm="vohive.local", nonce="%s", uri="%s", response="%s"`,
		username, nonce, uri, resp)
}

func TestRegisterFlow(t *testing.T) {
	srv, port := startTestServer(t)
	conn := sipClient(t, port)
	callID := "reg-test-1"
	base := "REGISTER sip:vohive.local SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bKreg1\r\n" +
		"From: <sip:linphone@vohive.local>;tag=t1\r\n" +
		"To: <sip:linphone@vohive.local>\r\n" +
		"Call-ID: " + callID + "\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Contact: <sip:linphone@127.0.0.1:5061>;expires=3600\r\n" +
		"Content-Length: 0\r\n\r\n"
	// No auth -> 401 with a Digest challenge.
	reply := sendAndRecv(t, conn, base)
	if !strings.HasPrefix(reply, "SIP/2.0 401") {
		t.Fatalf("expected 401, got: %s", firstLine(reply))
	}
	if !strings.Contains(reply, `realm="vohive.local"`) {
		t.Fatalf("challenge missing realm: %s", reply)
	}
	// With valid digest -> 200, registered.
	auth := digestHeader(t, srv, "linphone", "REGISTER", "sip:vohive.local")
	reply = sendAndRecv(t, conn, strings.Replace(base, "CSeq: 1 REGISTER", "CSeq: 2 REGISTER\r\nAuthorization: "+auth, 1))
	if !strings.HasPrefix(reply, "SIP/2.0 200") {
		t.Fatalf("expected 200, got: %s", reply)
	}
	st := srv.Status()
	if len(st.Registrations) != 1 || st.Registrations[0].Username != "linphone" {
		t.Fatalf("registrations = %+v", st.Registrations)
	}
}

func TestInviteWithoutModemFailsGracefully(t *testing.T) {
	srv, port := startTestServer(t)
	conn := sipClient(t, port)
	// Register first (INVITE requires auth).
	auth := digestHeader(t, srv, "linphone", "REGISTER", "sip:vohive.local")
	reg := "REGISTER sip:vohive.local SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bKreg2\r\n" +
		"From: <sip:linphone@vohive.local>;tag=t2\r\n" +
		"To: <sip:linphone@vohive.local>\r\n" +
		"Call-ID: reg-test-2\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Authorization: " + auth + "\r\n" +
		"Contact: <sip:linphone@127.0.0.1:5061>;expires=3600\r\n" +
		"Content-Length: 0\r\n\r\n"
	if reply := sendAndRecv(t, conn, reg); !strings.HasPrefix(reply, "SIP/2.0 200") {
		t.Fatalf("register failed: %s", firstLine(reply))
	}
	// INVITE to a number with no modem attached: 100 Trying, then 503.
	// No crash, no real call.
	inviteAuth := digestHeader(t, srv, "linphone", "INVITE", "sip:13800138000@127.0.0.1")
	sdp := "v=0\r\no=x 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	invite := "INVITE sip:13800138000@127.0.0.1 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bKinv1\r\n" +
		"From: <sip:linphone@vohive.local>;tag=t3\r\n" +
		"To: <sip:13800138000@127.0.0.1>\r\n" +
		"Call-ID: inv-test-1\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Authorization: " + inviteAuth + "\r\n" +
		"Contact: <sip:linphone@127.0.0.1:5061>\r\n" +
		"Content-Type: application/sdp\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n", len(sdp)) + sdp
	reply := sendAndRecv(t, conn, invite)
	if !strings.HasPrefix(reply, "SIP/2.0 100") {
		t.Fatalf("expected 100 Trying, got: %s", firstLine(reply))
	}
	// Second reply (503) arrives on the same socket.
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no 503 after Trying: %v", err)
	}
	if !strings.HasPrefix(string(buf[:n]), "SIP/2.0 503") {
		t.Fatalf("expected 503, got: %s", firstLine(string(buf[:n])))
	}
	if !srv.Status().Listening {
		t.Fatalf("server stopped listening after failed INVITE")
	}
	calls := srv.Calls()
	if len(calls) != 1 || calls[0].State != CallStateFailed {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestOptionsAndMessage(t *testing.T) {
	_, port := startTestServer(t)
	conn := sipClient(t, port)
	opts := "OPTIONS sip:vohive.local SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bKopt1\r\n" +
		"From: <sip:linphone@vohive.local>;tag=t4\r\n" +
		"To: <sip:vohive.local>\r\n" +
		"Call-ID: opt-test-1\r\n" +
		"CSeq: 1 OPTIONS\r\n" +
		"Content-Length: 0\r\n\r\n"
	if reply := sendAndRecv(t, conn, opts); !strings.HasPrefix(reply, "SIP/2.0 200") {
		t.Fatalf("OPTIONS expected 200, got: %s", firstLine(reply))
	}
	msg := strings.Replace(opts, "OPTIONS", "MESSAGE", 1)
	msg = strings.Replace(msg, "opt-test-1", "msg-test-1", 1)
	if reply := sendAndRecv(t, conn, msg); !strings.HasPrefix(reply, "SIP/2.0 405") {
		t.Fatalf("MESSAGE expected 405, got: %s", firstLine(reply))
	}
}

func TestRTPAllocateAndSilence(t *testing.T) {
	// Allocate a session, start it toward a loopback listener, and verify
	// silence RTP frames (PT 0) arrive with incrementing sequence numbers.
	recv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	remote := recv.LocalAddr().(*net.UDPAddr)
	sess, err := NewRTPSession(20000, 20100, &QPCMVBridge{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	if err := sess.Start(remote); err != nil {
		t.Fatal(err)
	}
	_ = recv.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	var seqs []int
	for len(seqs) < 3 {
		n, err := recv.Read(buf)
		if err != nil {
			t.Fatalf("no RTP frames: %v", err)
		}
		if n < 12 || buf[0] != 0x80 || buf[1] != 0 {
			t.Fatalf("bad RTP header: %v", buf[:n])
		}
		seq := int(buf[2])<<8 | int(buf[3])
		seqs = append(seqs, seq)
		if n != 12+160 {
			t.Fatalf("RTP packet size = %d, want %d", n, 12+160)
		}
		for _, b := range buf[12:n] {
			if b != 0xFF {
				t.Fatalf("non-silence payload byte %#x (stub bridge must send silence)", b)
			}
		}
	}
	if seqs[1] != seqs[0]+1 || seqs[2] != seqs[1]+1 {
		t.Fatalf("sequence not incrementing: %v", seqs)
	}
	stats := sess.Stats()
	if stats.TXPackets < 3 || !stats.Running {
		t.Fatalf("stats = %+v", stats)
	}
	// Send one RTP packet back; the session must count it.
	pkt := make([]byte, 12+160)
	pkt[0] = 0x80
	pkt[1] = 0
	for i := range pkt[12:] {
		pkt[12+i] = 0xFF
	}
	local := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: sess.LocalPort()}
	if _, err := recv.WriteToUDP(pkt, local); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Stats().RXPackets >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sess.Stats().RXPackets < 1 {
		t.Fatalf("inbound RTP packet not counted: %+v", sess.Stats())
	}
}

func TestAudioBridgeStub(t *testing.T) {
	b := &QPCMVBridge{}
	if b.Available() {
		t.Fatalf("bridge without modem must be unavailable")
	}
	if _, err := b.ReadPCM(160); err != ErrPCMNotWired {
		t.Fatalf("ReadPCM err = %v, want ErrPCMNotWired", err)
	}
	if err := b.WritePCM(make([]int16, 160)); err != ErrPCMNotWired {
		t.Fatalf("WritePCM err = %v, want ErrPCMNotWired", err)
	}
	if !strings.Contains(b.Status(), "stub") {
		t.Fatalf("status must say stub: %q", b.Status())
	}
}

func TestMD5Helper(t *testing.T) {
	sum := md5.Sum([]byte("test"))
	if hex.EncodeToString(sum[:]) != md5Hex("test") {
		t.Fatalf("md5Hex mismatch")
	}
}

func statMode(path string) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// --- CPAS/CLCC reconciliation (stuck-call fix, 2026-11-06) ---------------

func TestReconcileClearsStaleCallWhenCPASZero(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	// Modem: no call (CPAS 0) and only the firmware phantom in CLCC
	// (idx 2, state active, empty number).
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0, CLCC: []CLCCEntry{{Index: 2, Dir: 1, State: 0, Number: ""}}}, nil
	}
	id1, err := srv.DialFromAPI("12025550142")
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	// The record is now ringing. A second dial must succeed: reconcile
	// sees CPAS 0, ends the stale record, and the busy check passes.
	id2, err := srv.DialFromAPI("12025550142")
	if err != nil {
		t.Fatalf("second dial after reconcile should succeed, got: %v", err)
	}
	if id2 == id1 {
		t.Fatalf("second dial reused the stale call id %s", id1)
	}
	if got := srv.Status().Calls; len(got) != 2 {
		t.Fatalf("expected 2 call records (1 ended + 1 live), got %d", len(got))
	}
}

func TestReconcileBlocksDialWithModemReason(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	// Modem: a real call is dialling (CPAS 4, CLCC entry with a number).
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Index: 1, Dir: 1, State: 2, Number: "12025550142"}}}, nil
	}
	if _, err := srv.DialFromAPI("12025550142"); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	_, err := srv.DialFromAPI("13800138000")
	if err == nil {
		t.Fatal("second dial while modem is busy should fail")
	}
	if !strings.Contains(err.Error(), "CPAS 4") {
		t.Fatalf("busy error should name the modem state, got: %v", err)
	}
}

func TestReconcilePromotesRingingToActiveOnRealCLCC(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Index: 1, Dir: 1, State: 2, Number: "12025550142"}}}, nil
	}
	if _, err := srv.DialFromAPI("12025550142"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Peer answers: CLCC now shows the real entry active.
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Index: 1, Dir: 1, State: 0, Number: "12025550142"}}}, nil
	}
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	calls := srv.Status().Calls
	if len(calls) != 1 || calls[0].State != CallStateActive {
		t.Fatalf("expected 1 active call, got %+v", calls)
	}
}

func TestReconcileExpiresStaleDiallingCall(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	hungUp := false
	srv.Hangup = func() error { hungUp = true; return nil }
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: nil}, nil
	}
	if _, err := srv.DialFromAPI("12025550142"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Backdate the record past the 60s connect timeout with no real
	// CLCC entry backing it: reconcile must fail it and hang up.
	srv.mu.Lock()
	for _, c := range srv.calls {
		c.StartedAt = time.Now().Add(-61 * time.Second)
	}
	srv.mu.Unlock()
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	calls := srv.Status().Calls
	if len(calls) != 1 || calls[0].State != CallStateFailed {
		t.Fatalf("expected 1 failed call, got %+v", calls)
	}
	if !hungUp {
		t.Fatal("reconcile should have hung up the modem after expiring the call")
	}
}

func TestReconcileDoesNotKillYoungDialOnCPASZero(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	// Modem: CPAS 0, no CLCC at all — the state a mobile-originated
	// call can report for seconds while it is still being set up
	// (CSFB / network paging).
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0, CLCC: nil}, nil
	}
	if _, err := srv.DialFromAPI("12025550142"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Two reconcile polls must leave the young dial ringing.
	for i := 0; i < 2; i++ {
		if _, err := srv.ReconcileWithModem(); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	calls := srv.Status().Calls
	if len(calls) != 1 || calls[0].State != CallStateRinging {
		t.Fatalf("young dial must still be ringing, got %+v", calls)
	}
	// Past the 60s dial timeout with no CLCC backing it, it must fail.
	srv.mu.Lock()
	for _, c := range srv.calls {
		c.StartedAt = time.Now().Add(-61 * time.Second)
	}
	srv.mu.Unlock()
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	calls = srv.Status().Calls
	if len(calls) != 1 || calls[0].State != CallStateFailed {
		t.Fatalf("expected 1 failed call after timeout, got %+v", calls)
	}
}

func TestReconcileEndsEstablishedCallOnCPASZero(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Index: 1, Dir: 1, State: 2, Number: "12025550142"}}}, nil
	}
	if _, err := srv.DialFromAPI("12025550142"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Peer answers, then hangs up: CPAS 0, no CLCC, NO CARRIER seen.
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Index: 1, Dir: 1, State: 0, Number: "12025550142"}}}, nil
	}
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0, CLCC: nil, ReleaseReason: "22:51:05 NO CARRIER"}, nil
	}
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	calls := srv.Status().Calls
	if len(calls) != 1 || calls[0].State != CallStateEnded {
		t.Fatalf("expected 1 ended call, got %+v", calls)
	}
	if !strings.Contains(calls[0].Error, "NO CARRIER") {
		t.Fatalf("end reason should name the release URC, got %q", calls[0].Error)
	}
}

func TestReconcileFailsUnestablishedDialOnReleaseURC(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0, CLCC: nil}, nil
	}
	if _, err := srv.DialFromAPI("12025550142"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Modem releases the unestablished dial with BUSY: reconcile must
	// fail it promptly with the real reason.
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0, CLCC: nil, ReleaseReason: "22:51:05 BUSY"}, nil
	}
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	calls := srv.Status().Calls
	if len(calls) != 1 || calls[0].State != CallStateFailed {
		t.Fatalf("expected 1 failed call, got %+v", calls)
	}
	if !strings.Contains(calls[0].Error, "BUSY") {
		t.Fatalf("failure should name BUSY, got %q", calls[0].Error)
	}
}

func TestAnswerCallRefusesOutboundCall(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(number string) error { return nil }
	if _, err := srv.DialFromAPI("12025550142"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := srv.AnswerCall(""); err == nil {
		t.Fatal("answering an outbound call should fail")
	} else if !strings.Contains(err.Error(), "no incoming call") {
		t.Fatalf("expected no incoming call error, got: %v", err)
	}
}
