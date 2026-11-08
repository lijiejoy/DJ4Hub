package sip

import (
	"errors"
	"strings"
)

func (s *Server) SendDTMF(id, digit string) error {
	if id == "" || len(digit) != 1 || !strings.Contains("0123456789*#", digit) {
		return errors.New("DTMF requires a call ID and one digit (0–9, * or #)")
	}
	call, err := s.findCall(id)
	if err != nil {
		return err
	}
	if call.public().State != CallStateActive {
		return errors.New("DTMF requires a connected call")
	}
	if s.DTMF == nil {
		return errors.New("modem DTMF is unavailable")
	}
	return s.DTMF(digit)
}

func (b *CallAudioBridge) SetLocalControls(id string, microphoneMuted, speakerMuted bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id == "" || id != b.callID || b.closed || b.ended[id] || b.cancel == nil {
		return errors.New("local call is no longer available")
	}
	b.local.MicrophoneMuted = microphoneMuted
	b.local.SpeakerMuted = speakerMuted
	return nil
}
