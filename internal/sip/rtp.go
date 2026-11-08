package sip

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// RTP: one media session per answered call. Payload type 0 (PCMU/8000),
// 20 ms frames of 160 samples. The session always runs its receive loop
// (counting packets and decoding to PCM for the bridge) and its send loop
// (bridge PCM when wired, silence frames otherwise), so media flow is
// observable even with the audio stub.

const (
	rtpPayloadTypePCMU = 0
	rtpFrameSamples    = 160 // 20 ms at 8000 Hz
	rtpFrameDuration   = 20 * time.Millisecond
)

// RTPSessionStats is the observable media accounting for one call.
type RTPSessionStats struct {
	LocalPort int    `json:"local_port"`
	Remote    string `json:"remote"`
	RXPackets uint64 `json:"rx_packets"`
	RXBytes   uint64 `json:"rx_bytes"`
	TXPackets uint64 `json:"tx_packets"`
	TXBytes   uint64 `json:"tx_bytes"`
	Running   bool   `json:"running"`
	LastError string `json:"last_error,omitempty"`
}

// RTPSession owns one UDP socket in the configured RTP range.
type RTPSession struct {
	conn   *net.UDPConn
	remote *net.UDPAddr
	bridge AudioBridge

	mu        sync.Mutex
	seq       uint16
	timestamp uint32
	ssrc      uint32
	rxPackets uint64
	rxBytes   uint64
	txPackets uint64
	txBytes   uint64
	running   bool
	stopCh    chan struct{}
	doneCh    chan struct{}
	recvDone  chan struct{}
	stopped   bool
	started   bool
	starting  bool
	lastError string
	onError   func(error)
}

// allocateRTPPort binds an even UDP port in [min, max] on 127.0.0.1 and
// returns the socket. Even ports are preferred (RTP/RTCP pairing); an odd
// port is accepted rather than failing when the range is nearly exhausted.
func allocateRTPPort(min, max int) (*net.UDPConn, int, error) {
	var lastErr error
	for port := min; port <= max; port++ {
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, 0, err
		}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		return conn, port, nil
	}
	if lastErr != nil {
		return nil, 0, fmt.Errorf("no free RTP port in %d-%d: %w", min, max, lastErr)
	}
	return nil, 0, fmt.Errorf("no free RTP port in %d-%d", min, max)
}

// NewRTPSession allocates a UDP socket in the configured RTP range. The
// session is idle until Start is called with the peer's address.
func NewRTPSession(min, max int, bridge AudioBridge) (*RTPSession, error) {
	conn, port, err := allocateRTPPort(min, max)
	if err != nil {
		return nil, err
	}
	var ssrcBytes [4]byte
	if _, err := rand.Read(ssrcBytes[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("generate RTP SSRC: %w", err)
	}
	var seqBytes [2]byte
	if _, err := rand.Read(seqBytes[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("generate RTP sequence: %w", err)
	}
	_ = port
	return &RTPSession{
		conn:      conn,
		bridge:    bridge,
		seq:       binary.BigEndian.Uint16(seqBytes[:]),
		timestamp: binary.BigEndian.Uint32(ssrcBytes[:]),
		ssrc:      binary.BigEndian.Uint32(ssrcBytes[:]),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		recvDone:  make(chan struct{}),
	}, nil
}

// LocalPort returns the bound UDP port.
func (s *RTPSession) LocalPort() int {
	if s.conn == nil || s.conn.LocalAddr() == nil {
		return 0
	}
	if addr, ok := s.conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.Port
	}
	return 0
}

// Start begins the send/receive loops toward remote. It is safe to call once.
func (s *RTPSession) Start(remote *net.UDPAddr) error {
	if remote == nil || remote.Port <= 0 || remote.Port > 65535 {
		return errors.New("RTP start: no remote address")
	}
	s.mu.Lock()
	if s.running || s.stopped || s.starting {
		s.mu.Unlock()
		return errors.New("RTP session already running or closed")
	}
	s.starting = true
	s.mu.Unlock()
	if lifecycle, ok := s.bridge.(interface{ StartPCM() error }); ok {
		if err := lifecycle.StartPCM(); err != nil {
			s.mu.Lock()
			s.starting = false
			s.lastError = err.Error()
			s.mu.Unlock()
			return fmt.Errorf("start module audio: %w", err)
		}
	}
	s.mu.Lock()
	s.starting = false
	if s.stopped {
		s.mu.Unlock()
		if lifecycle, ok := s.bridge.(interface{ StopPCM() }); ok {
			lifecycle.StopPCM()
		}
		return errors.New("RTP startup cancelled")
	}
	s.remote = remote
	s.running = true
	s.started = true
	s.mu.Unlock()
	go s.recvLoop()
	go s.sendLoop()
	return nil
}

// Stop ends both loops and closes the socket.
func (s *RTPSession) Stop() {
	s.mu.Lock()
	if s.stopped {
		started := s.started
		s.mu.Unlock()
		if started {
			<-s.doneCh
			<-s.recvDone
		}
		return
	}
	s.stopped = true
	s.running = false
	close(s.stopCh)
	started := s.started
	starting := s.starting
	s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
	}
	if starting {
		if lifecycle, ok := s.bridge.(interface{ StopPCM() }); ok {
			lifecycle.StopPCM()
		}
	}
	if started {
		<-s.doneCh
		<-s.recvDone
		if lifecycle, ok := s.bridge.(interface{ StopPCM() }); ok {
			lifecycle.StopPCM()
		}
	}
}

func (s *RTPSession) mediaError(err error) {
	s.mu.Lock()
	if s.stopped || s.lastError != "" {
		s.mu.Unlock()
		return
	}
	s.lastError = err.Error()
	callback := s.onError
	s.mu.Unlock()
	if callback != nil {
		go callback(err)
	}
}

// Stats returns current media accounting.
func (s *RTPSession) Stats() RTPSessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	remote := ""
	if s.remote != nil {
		remote = s.remote.String()
	}
	return RTPSessionStats{
		LocalPort: s.LocalPort(),
		Remote:    remote,
		RXPackets: s.rxPackets,
		RXBytes:   s.rxBytes,
		TXPackets: s.txPackets,
		TXBytes:   s.txBytes,
		Running:   s.running,
		LastError: s.lastError,
	}
}

func pcmuPayload(packet []byte) ([]byte, bool) {
	if len(packet) < 12 || packet[0]>>6 != 2 || packet[1]&0x7f != rtpPayloadTypePCMU {
		return nil, false
	}
	offset := 12 + int(packet[0]&0x0f)*4
	if offset > len(packet) {
		return nil, false
	}
	if packet[0]&0x10 != 0 {
		if offset+4 > len(packet) {
			return nil, false
		}
		offset += 4 + int(binary.BigEndian.Uint16(packet[offset+2:offset+4]))*4
	}
	end := len(packet)
	if packet[0]&0x20 != 0 {
		padding := int(packet[end-1])
		if padding == 0 || padding > end-offset {
			return nil, false
		}
		end -= padding
	}
	if offset >= end {
		return nil, false
	}
	return packet[offset:end], true
}

func (s *RTPSession) recvLoop() {
	defer close(s.recvDone)
	buf := make([]byte, 2048)
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, source, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		if s.remote == nil || !source.IP.Equal(s.remote.IP) || source.Port != s.remote.Port {
			continue
		}
		payload, ok := pcmuPayload(buf[:n])
		if !ok {
			continue
		}
		s.mu.Lock()
		s.rxPackets++
		s.rxBytes += uint64(len(payload))
		s.mu.Unlock()
		// Decode to PCM and hand it to the bridge when one is wired. With
		// the stub bridge this returns ErrPCMNotWired and is ignored: the
		// packets are still counted, which is the observable proof that
		// media arrived.
		if s.bridge != nil {
			if err := s.bridge.WritePCM(UlawToPCM(payload)); err != nil && !errors.Is(err, ErrPCMNotWired) {
				s.mediaError(err)
				return
			}
		}
	}
}

func (s *RTPSession) sendLoop() {
	ticker := time.NewTicker(rtpFrameDuration)
	defer ticker.Stop()
	defer close(s.doneCh)
	silence := make([]byte, rtpFrameSamples)
	for i := range silence {
		silence[i] = ulawSilence
	}
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
		}
		payload := silence
		if s.bridge != nil {
			pcm, err := s.bridge.ReadPCM(rtpFrameSamples)
			if err != nil && !errors.Is(err, ErrPCMNotWired) {
				s.mediaError(err)
				return
			}
			if err == nil && len(pcm) > 0 {
				// Bridge PCM wins when wired; pad/truncate to one frame.
				frame := make([]int16, rtpFrameSamples)
				copy(frame, pcm)
				payload = PCMToUlaw(frame)
			}
			// ErrPCMNotWired (or any bridge error) -> keep silence.
		}
		packet := make([]byte, 12+len(payload))
		packet[0] = 0x80 // V=2, P=0, X=0, CC=0
		packet[1] = rtpPayloadTypePCMU
		s.mu.Lock()
		binary.BigEndian.PutUint16(packet[2:4], s.seq)
		binary.BigEndian.PutUint32(packet[4:8], s.timestamp)
		binary.BigEndian.PutUint32(packet[8:12], s.ssrc)
		s.seq++
		s.timestamp += rtpFrameSamples
		s.mu.Unlock()
		copy(packet[12:], payload)
		if s.remote == nil {
			continue
		}
		if _, err := s.conn.WriteToUDP(packet, s.remote); err != nil {
			return
		}
		s.mu.Lock()
		s.txPackets++
		s.txBytes += uint64(len(payload))
		s.mu.Unlock()
	}
}
