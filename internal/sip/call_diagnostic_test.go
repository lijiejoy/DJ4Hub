package sip

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInboundAnswerKeepsAdvertisedRTPPort(t *testing.T) {
	server, port := startTestServer(t)
	server.audio = &pcmTestBridge{received: make(chan []int16, 10)}
	server.Answer = func() error { return nil }
	client := sipClient(t, port)
	server.mu.Lock()
	server.registrations["test"] = &Registration{
		Username: "test", Source: client.LocalAddr().String(),
		Contact:   "<sip:test@" + client.LocalAddr().String() + ">",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	server.mu.Unlock()
	server.IncomingCall("12345")
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8192)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := parseSIPMessage(string(buf[:n]))
	if err != nil {
		t.Fatal(err)
	}
	offeredPort := parseSDP(invite.Body).Port
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	raw := buildResponse(invite, 200, "OK", "test-peer", map[string]string{
		"Contact": "<sip:test@" + client.LocalAddr().String() + ">",
	}, buildSDP(peer.LocalAddr().(*net.UDPAddr).Port), "application/sdp")
	answer, err := parseSIPMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	call := server.findCallByCallID(invite.callID())
	if call == nil {
		t.Fatal("incoming call was not tracked")
	}
	server.handleInviteAccepted(call, answer, client.LocalAddr().(*net.UDPAddr))
	stats := call.RTPStats()
	if stats == nil || stats.LocalPort != offeredPort || !stats.Running {
		t.Fatalf("answer changed advertised RTP endpoint: offered=%d stats=%+v", offeredPort, stats)
	}
}

func TestCallDiagnosticCompletesAuthenticatedCallAndMedia(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:" + strconv.Itoa(freeUDPPort(t))
	cfg.Password = "local-test-password"
	bridge := &pcmTestBridge{received: make(chan []int16, 200)}
	server := New(cfg, bridge)
	server.Dial = func(number string) error {
		if number != "12345" {
			t.Errorf("unexpected test number %s", number)
		}
		go func() {
			time.Sleep(30 * time.Millisecond)
			server.ModemConnected()
		}()
		return nil
	}
	hangup := make(chan struct{}, 1)
	server.Hangup = func() error {
		select {
		case hangup <- struct{}{}:
		default:
		}
		return nil
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := RunCallDiagnostic(ctx, cfg, "12345", time.Second, &output); err != nil {
		t.Fatalf("diagnostic failed: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "SIP 200") || !strings.Contains(output.String(), "audible samples=") {
		t.Fatal(output.String())
	}
	select {
	case <-hangup:
	case <-time.After(time.Second):
		t.Fatal("diagnostic did not hang up the completed test call")
	}
	nonzero := false
	for len(bridge.received) > 0 {
		for _, sample := range <-bridge.received {
			if sample > 64 || sample < -64 {
				nonzero = true
			}
		}
	}
	if !nonzero {
		t.Fatal("test tone did not reach module-side PCM endpoint")
	}
}
