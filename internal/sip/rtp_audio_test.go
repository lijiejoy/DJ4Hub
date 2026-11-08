package sip

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestRTPPayloadHandlesHeadersAndRejectsOtherCodecs(t *testing.T) {
	// V2 + padding + extension + one CSRC, one extension word, two payload
	// bytes and two padding bytes.
	packet := make([]byte, 28)
	packet[0] = 0xb1
	binary.BigEndian.PutUint16(packet[18:20], 1)
	packet[24], packet[25], packet[27] = 0x80, 0x00, 2
	payload, ok := pcmuPayload(packet)
	if !ok || len(payload) != 2 || payload[0] != 0x80 || payload[1] != 0 {
		t.Fatalf("RTP headers leaked into audio: payload=%x ok=%v", payload, ok)
	}
	packet[1] = 101 // telephone-event, not audio
	if _, ok := pcmuPayload(packet); ok {
		t.Fatal("DTMF payload accepted as PCM audio")
	}
	packet[1] = 0
	packet[27] = 255
	if _, ok := pcmuPayload(packet); ok {
		t.Fatal("invalid padding accepted")
	}
	if _, ok := pcmuPayload(packet[:19]); ok {
		t.Fatal("truncated extension accepted")
	}
}

type lifecycleTestBridge struct {
	pcmTestBridge
	startErr error
	starts   int
	stops    int
}

func (b *lifecycleTestBridge) StartPCM() error {
	b.starts++
	return b.startErr
}

func (b *lifecycleTestBridge) StopPCM() { b.stops++ }

func TestRTPRejectsFailedAudioStart(t *testing.T) {
	bridge := &lifecycleTestBridge{startErr: errors.New("USB audio device unavailable")}
	session, err := NewRTPSession(21000, 21100, bridge)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Stop()
	err = session.Start(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9})
	if err == nil || session.Stats().Running {
		t.Fatal("RTP must report audio startup failure instead of running with silence")
	}
}

func TestRTPStopsAudioExactlyOnce(t *testing.T) {
	bridge := &lifecycleTestBridge{}
	session, err := NewRTPSession(21000, 21100, bridge)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Stop()
	if err := session.Start(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9}); err != nil {
		t.Fatal(err)
	}
	session.Stop()
	session.Stop()
	if bridge.starts != 1 || bridge.stops != 1 {
		t.Fatalf("audio lifecycle starts=%d stops=%d, want 1 each", bridge.starts, bridge.stops)
	}
	if err := session.Start(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9}); err == nil {
		t.Fatal("closed RTP session must not restart")
	}
}

type pcmTestBridge struct {
	received chan []int16
}

func (b *pcmTestBridge) Available() bool    { return true }
func (b *pcmTestBridge) Status() string     { return "test PCM endpoint" }
func (b *pcmTestBridge) QPCMVEnabled() bool { return false }
func (b *pcmTestBridge) Enable() error      { return nil }
func (b *pcmTestBridge) Disable() error     { return nil }
func (b *pcmTestBridge) ReadPCM(n int) ([]int16, error) {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = 32124 // Known G.711 mu-law vector: 0x80.
	}
	return pcm, nil
}
func (b *pcmTestBridge) WritePCM(pcm []int16) error {
	select {
	case b.received <- append([]int16(nil), pcm...):
	default:
	}
	return nil
}

func TestRTPTransfersNonSilentPCMInBothDirections(t *testing.T) {
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	bridge := &pcmTestBridge{received: make(chan []int16, 1)}
	session, err := NewRTPSession(21000, 21100, bridge)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Stop()
	if err := session.Start(peer.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}

	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 2048)
	n, source, err := peer.ReadFromUDP(packet)
	if err != nil {
		t.Fatal(err)
	}
	if n != 172 || packet[0] != 0x80 || packet[1] != 0 {
		t.Fatalf("unexpected PCMU RTP frame: length=%d header=%x", n, packet[:2])
	}
	for i, sample := range packet[12:n] {
		if sample != 0x80 {
			t.Fatalf("outgoing sample %d = %#x, want non-silent PCMU 0x80", i, sample)
		}
	}

	// Known G.711 mu-law vector: 0x00 decodes to -32124.
	for i := 12; i < n; i++ {
		packet[i] = 0x00
	}
	if _, err := peer.WriteToUDP(packet[:n], source); err != nil {
		t.Fatal(err)
	}
	select {
	case pcm := <-bridge.received:
		if len(pcm) != 160 {
			t.Fatalf("received %d samples, want 160", len(pcm))
		}
		for i, sample := range pcm {
			if sample != -32124 {
				t.Fatalf("incoming sample %d = %d, want -32124", i, sample)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("received RTP audio was not delivered to the PCM bridge")
	}
}
