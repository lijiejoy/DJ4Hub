package sip

// AudioBridge is the module-side PCM endpoint of a call.
//
// The SIP/RTP side of this package speaks PCMU/8000. The module side speaks
// 8 kHz mono PCM through its USB Audio Class interface. Production macOS
// calls use CoreAudio, with an additional module-side route on QDC507.
// QPCMVBridge is retained only as a legacy adapter and for demo/tests;
// its PCM methods return ErrPCMNotWired.
//
// Hardware observation (2026-11-07, QDC507GLEFM21): the connected QDC507
// exposes USB audio interfaces 6-8 and macOS detects 8 kHz mono input and
// output. AT+QPCMV=? advertises support, although AT+QPCMV? returns ERROR.
// A rejected read query alone cannot establish command or UAC support.
type AudioBridge interface {
	// Available reports whether any module audio path is present at all.
	Available() bool
	// Status returns a short human-readable description of the bridge state.
	Status() string
	// QPCMVEnabled reports the module's AT+QPCMV state (false when unknown).
	QPCMVEnabled() bool
	// Enable starts the implementation's module audio path.
	Enable() error
	// Disable stops the implementation's host audio path.
	Disable() error
	// WritePCM consumes 8 kHz 16-bit mono PCM from the SIP side (toward the
	// module's earpiece). Returns ErrPCMNotWired until the pump exists.
	WritePCM(pcm []int16) error
	// ReadPCM returns up to n samples of 8 kHz PCM from the module
	// (toward the SIP side). Returns ErrPCMNotWired until the pump exists.
	ReadPCM(n int) ([]int16, error)
}

// ModemAudio is the minimal modem surface the QPCMV bridge needs. It is
// satisfied by *modem.Manager and by the app-level adapter in
// cmd/dj4ghub-macos (which also covers the USB-AT path where there is no
// Manager). Keeping this interface in the sip package avoids an import of
// the whole modem stack from the audio stub.
type ModemAudio interface {
	EnableUSBAudio() error
	DisableUSBAudio() error
	QueryUSBAudioMode() (bool, int, error)
	// GetQPCMVChan returns the +QPCMV flow-control channel (0=busy, 1=ready),
	// or nil when the modem does not expose one.
	GetQPCMVChan() <-chan int
}

// QPCMVBridge is the legacy AT+QPCMV adapter with stubbed PCM transport.
type QPCMVBridge struct {
	Modem ModemAudio // nil when no module is attached
}

// Available reports modem attachment, not whether PCM is implemented.
func (b *QPCMVBridge) Available() bool {
	if b.Modem == nil {
		return false
	}
	if modem, ok := b.Modem.(interface{ Available() bool }); ok {
		return modem.Available()
	}
	return true
}

// Status describes the bridge honestly: QPCMV state when known, and the
// fact that the PCM pump is not wired.
func (b *QPCMVBridge) Status() string {
	if !b.Available() {
		return "no modem attached; audio bridge unavailable (stub)"
	}
	enabled, mode, err := b.Modem.QueryUSBAudioMode()
	if err != nil {
		return "modem attached; +QPCMV state unknown (" + err.Error() + "); PCM pump not wired (stub)"
	}
	if enabled {
		return "modem attached; +QPCMV enabled (mode " + itoa(mode) + ", UAC on); PCM pump not wired (stub)"
	}
	return "modem attached; +QPCMV disabled; PCM pump not wired (stub)"
}

// QPCMVEnabled reports the module's UAC audio state.
func (b *QPCMVBridge) QPCMVEnabled() bool {
	if !b.Available() {
		return false
	}
	enabled, _, err := b.Modem.QueryUSBAudioMode()
	return err == nil && enabled
}

// Enable turns on the module's UAC audio mode.
func (b *QPCMVBridge) Enable() error {
	if !b.Available() {
		return ErrNoModem
	}
	return b.Modem.EnableUSBAudio()
}

// Disable turns off the module's UAC audio mode.
func (b *QPCMVBridge) Disable() error {
	if !b.Available() {
		return ErrNoModem
	}
	return b.Modem.DisableUSBAudio()
}

// WritePCM is not wired yet: there is no CoreAudio pump to deliver PCM to
// the module's UAC interface.
func (b *QPCMVBridge) WritePCM(_ []int16) error { return ErrPCMNotWired }

// ReadPCM is not wired yet: there is no CoreAudio pump to capture PCM from
// the module's UAC interface.
func (b *QPCMVBridge) ReadPCM(_ int) ([]int16, error) { return nil, ErrPCMNotWired }

// QPCMVReadyChan exposes the module's +QPCMV flow-control channel when the
// modem provides one, so a future PCM pump can pace itself on it.
func (b *QPCMVBridge) QPCMVReadyChan() <-chan int {
	if b.Modem == nil {
		return nil
	}
	return b.Modem.GetQPCMVChan()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
