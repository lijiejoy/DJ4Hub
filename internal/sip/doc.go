// Package sip implements a minimal, loopback-first SIP server (UDP 5060)
// with a PCMU/8000 RTP media path, bridging to a 4G module's circuit-switched
// voice via AT call control (ATD/ATA/ATH) and module USB audio.
//
// Design notes (2026-11-06, macmini, no module attached):
//   - Signalling is implemented with the standard library only (net, crypto/md5).
//     No external SIP stack is vendored, so the build gains no new dependency.
//   - The config shape follows xiaoguiday/vohive: sip.listen (UDP 5060),
//     realm "vohive.local", RTP 10000-20000, one local user (default
//     "linphone"). SMS via SIP MESSAGE is intentionally NOT implemented.
//   - The audio side follows mccding/CellBridge's verified shape (SIP 5060 +
//     RTP PCMU/8000) but NOT its Linux/NAS path: there is no MaVo, no ADB,
//     no hw:0,4 bridge here. Production macOS media uses the CoreAudio
//     bridge in usb_audio.go and native_audio_darwin.c. QPCMVBridge remains
//     a stub for demo mode and protocol tests.
//   - Nothing in this package makes a real call on its own: dial/answer/
//     hangup are injected callbacks owned by the caller (cmd/dj4ghub-macos),
//     so the package is fully testable with fakes and no module.
package sip

import "errors"

// ErrPCMNotWired is returned by the audio bridge when the module-side PCM
// path has not been wired yet. Callers must treat it as "audio bridge needs
// module / CoreAudio pump", not as a fatal error: SIP signalling and RTP
// packet flow keep working, and the RTP session degrades to silence frames
// plus packet counting.
var ErrPCMNotWired = errors.New("audio bridge: legacy/demo PCM path is not wired")

// ErrNoModem is returned by call-control callbacks when no module is present.
var ErrNoModem = errors.New("no modem attached")
