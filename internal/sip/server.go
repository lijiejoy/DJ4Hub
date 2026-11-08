package sip

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	nonceTTL           = 5 * time.Minute
	registerDefault    = 3600 // seconds
	dialConnectTimeout = 60 * time.Second
	answerTimeout      = 15 * time.Second
	inboundNoAnswer    = 30 * time.Second
	clipWait           = 800 * time.Millisecond
)

// CallState is the lifecycle state of one call, exposed via the HTTP API.
type CallState string

const (
	CallStateDialing CallState = "dialing"
	CallStateRinging CallState = "ringing"
	CallStateActive  CallState = "active"
	CallStateEnded   CallState = "ended"
	CallStateFailed  CallState = "failed"
	CallStateMissed  CallState = "missed"
)

// Registration is one SIP client's REGISTER binding.
type Registration struct {
	Username  string    `json:"username"`
	Contact   string    `json:"contact"`
	Source    string    `json:"source"`
	UserAgent string    `json:"user_agent,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Call is one voice call, either direction. The RTP session is internal.
type Call struct {
	ID          string    `json:"id"`
	Direction   string    `json:"direction"` // "inbound" | "outbound"
	Number      string    `json:"number"`
	State       CallState `json:"state"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ConnectedAt time.Time `json:"connected_at,omitempty"`
	Error       string    `json:"error,omitempty"`

	// endReason explains a terminal state (see CallEndReason). It stays
	// empty while the call is live and is never guessed: unknown signals
	// record EndReasonUnknown.
	endReason CallEndReason
	// moduleConnected is set only when the modem confirms the call is
	// connected (CONNECT URC, ATA success, or an active +CLCC entry).
	// It is independent of MediaReady below.
	moduleConnected bool
	// mediaReady is set once audio is actually flowing (RTP packets in
	// either direction for a SIP leg, forwarded PCM for a local call).
	mediaReady   bool
	mediaReadyAt time.Time
	// ringAt is when the call entered ringing; with StartedAt and
	// ConnectedAt it gives the segmented timings in PublicCall.Timings.
	ringAt time.Time
	// mediaCheck reports whether media is flowing right now. It is
	// evaluated outside the call lock (see public) because it reads the
	// RTP session and the audio bridge.
	mediaCheck func() bool

	mu            sync.Mutex
	callID        string // SIP Call-ID of the dialog
	fromTag       string
	toTag         string
	branch        string       // Via branch of the INVITE we sent (for ACK/CANCEL)
	signallingSrc *net.UDPAddr // SIP peer for this dialog
	remoteContact string
	inviteReq     *sipMessage // pending inbound INVITE from a SIP client
	inviteSDP     sdpMedia
	rtp           *RTPSession
	connectedCh   chan struct{}
	cancelCh      chan struct{}
	inviteSent    bool
	// established is set once the modem backs this call with a real
	// +CLCC entry or CPAS 4 (call in progress). A record that never got
	// there may still be setting up (CSFB / network paging), so a bare
	// CPAS 0 must not kill it on its own.
	established    bool
	pendingDialogs map[string]pendingDialog // outbound INVITEs awaiting a response
	answered       bool
	sipLeg         bool
	onState        func(PublicCall)
}

// pendingDialog is one outbound INVITE leg awaiting a response.
type pendingDialog struct {
	Addr    *net.UDPAddr
	Contact string
}

// RTPStats returns the call's media accounting, or nil when no RTP session.
func (c *Call) RTPStats() *RTPSessionStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rtp == nil {
		return nil
	}
	stats := c.rtp.Stats()
	return &stats
}

// CallTimings is the segmented timing record for one call, in
// milliseconds. Each segment is present only once its endpoints are known,
// so a report can take medians and worst cases per segment without mixing
// ring wait into talk time. The values are measured server-side; the native
// alert's first ring sound is logged separately (see call_alerts.go).
type CallTimings struct {
	DialToRingingMs    *int64 `json:"dial_to_ringing_ms,omitempty"`
	RingToAnswerMs     *int64 `json:"ring_to_answer_ms,omitempty"`
	AnswerToFirstMedia *int64 `json:"answer_to_first_media_ms,omitempty"`
	DialToConnectedMs  *int64 `json:"dial_to_connected_ms,omitempty"`
}

// PublicCall is the JSON view of a Call.
type PublicCall struct {
	ID          string           `json:"id"`
	Direction   string           `json:"direction"`
	Number      string           `json:"number"`
	State       CallState        `json:"state"`
	StartedAt   time.Time        `json:"started_at"`
	ConnectedAt time.Time        `json:"connected_at,omitempty"`
	Error       string           `json:"error,omitempty"`
	RTP         *RTPSessionStats `json:"rtp,omitempty"`
	LocalAudio  bool             `json:"local_audio"`
	// EndReason explains a terminal state; empty while the call is live.
	EndReason CallEndReason `json:"end_reason,omitempty"`
	// ModuleConnected: the modem confirmed the call is connected.
	ModuleConnected bool `json:"module_connected"`
	// MediaReady: audio is actually flowing (RTP packets, or forwarded
	// PCM on a local call). Independent of ModuleConnected.
	MediaReady   bool      `json:"media_ready"`
	MediaReadyAt time.Time `json:"media_ready_at,omitempty"`
	RingAt       time.Time `json:"ring_at,omitempty"`
	// DurationSeconds counts only the active segment: ConnectedAt to
	// UpdatedAt for a finished call, ConnectedAt to now for a live one.
	// Ring wait is never included; 0 before answer.
	DurationSeconds int64        `json:"duration_seconds"`
	Timings         *CallTimings `json:"timings,omitempty"`
}

func (c *Call) public() PublicCall {
	c.mu.Lock()
	snapshot := c.publicLocked()
	c.mu.Unlock()
	// Media readiness is observed outside the call lock: it reads the RTP
	// session and the audio bridge, which must never be probed while
	// holding c.mu. Status polling (~1 s cadence) is the observation
	// point, so MediaReadyAt has ~1 s granularity; that is the documented
	// precision of the answer-to-first-media segment.
	if !snapshot.MediaReady && c.mediaCheck != nil && c.mediaCheck() {
		c.mu.Lock()
		if !c.mediaReady {
			c.mediaReady = true
			c.mediaReadyAt = time.Now()
		}
		snapshot = c.publicLocked()
		c.mu.Unlock()
	}
	return snapshot
}

func msBetween(a, b time.Time) *int64 {
	if a.IsZero() || b.IsZero() {
		return nil
	}
	ms := b.Sub(a).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}

// publicLocked builds the JSON view. Caller must hold c.mu.
func (c *Call) publicLocked() PublicCall {
	now := time.Now()
	duration := int64(0)
	if !c.ConnectedAt.IsZero() {
		end := now
		if IsTerminalState(c.State) {
			end = c.UpdatedAt
		}
		if d := end.Sub(c.ConnectedAt); d > 0 {
			duration = int64(d / time.Second)
		}
	}
	timings := &CallTimings{
		DialToRingingMs:    msBetween(c.StartedAt, c.ringAt),
		RingToAnswerMs:     msBetween(c.ringAt, c.ConnectedAt),
		AnswerToFirstMedia: msBetween(c.ConnectedAt, c.mediaReadyAt),
		DialToConnectedMs:  msBetween(c.StartedAt, c.ConnectedAt),
	}
	return PublicCall{
		ID: c.ID, Direction: c.Direction, Number: c.Number, State: c.State,
		StartedAt: c.StartedAt, ConnectedAt: c.ConnectedAt, Error: c.Error,
		LocalAudio: !c.sipLeg, EndReason: c.endReason,
		ModuleConnected: c.moduleConnected, MediaReady: c.mediaReady,
		MediaReadyAt: c.mediaReadyAt, RingAt: c.ringAt,
		DurationSeconds: duration, Timings: timings,
		RTP: func() *RTPSessionStats {
			if c.rtp == nil {
				return nil
			}
			s := c.rtp.Stats()
			return &s
		}(),
	}
}

// markModuleConnected records that the modem confirmed this call connected.
func (c *Call) markModuleConnected() {
	c.mu.Lock()
	c.moduleConnected = true
	c.mu.Unlock()
}

func (c *Call) setState(state CallState, err string) {
	c.setStateReason(state, "", err)
}

// setStateReason moves the call to state, recording reason for terminal
// states. An empty reason is inferred from the error text and call
// context; when nothing distinguishes the cause it stays unknown.
func (c *Call) setStateReason(state CallState, reason CallEndReason, err string) {
	c.mu.Lock()
	if IsTerminalState(c.State) && c.State != state {
		c.mu.Unlock()
		return
	}
	changed := c.State != state
	c.State = state
	c.UpdatedAt = time.Now()
	if state == CallStateRinging && c.ringAt.IsZero() {
		c.ringAt = c.UpdatedAt
	}
	if state == CallStateActive && c.ConnectedAt.IsZero() {
		c.ConnectedAt = c.UpdatedAt
	}
	if IsTerminalState(state) {
		if reason == "" {
			reason = InferEndReason(state, c.Direction, err)
		}
		c.endReason = reason
	}
	if err != "" {
		c.Error = err
	}
	notify := c.onState
	var snapshot PublicCall
	if changed && notify != nil {
		snapshot = c.publicLocked()
	}
	c.mu.Unlock()
	if changed && notify != nil {
		notify(snapshot)
	}
}

// AudioStatus is the JSON view of the audio bridge.
type AudioStatus struct {
	Mode                  string            `json:"mode"`
	Available             bool              `json:"available"`
	QPCMVEnabled          bool              `json:"qpcmv_enabled"`
	PCMWired              bool              `json:"pcm_wired"`
	Detail                string            `json:"detail"`
	Running               bool              `json:"running"`
	SampleRate            int               `json:"sample_rate,omitempty"`
	InputDevice           string            `json:"input_device,omitempty"`
	OutputDevice          string            `json:"output_device,omitempty"`
	InputUID              string            `json:"input_uid,omitempty"`
	OutputUID             string            `json:"output_uid,omitempty"`
	CapturedSamples       uint64            `json:"captured_samples"`
	PlayedSamples         uint64            `json:"played_samples"`
	InputNonzeroSamples   uint64            `json:"input_nonzero_samples"`
	OutputNonzeroSamples  uint64            `json:"output_nonzero_samples"`
	DroppedSamples        uint64            `json:"dropped_samples"`
	UnderrunSamples       uint64            `json:"underrun_samples"`
	CaptureQueuedSamples  uint64            `json:"capture_queued_samples"`
	PlaybackQueuedSamples uint64            `json:"playback_queued_samples"`
	LastError             string            `json:"last_error,omitempty"`
	MicrophonePermission  string            `json:"microphone_permission,omitempty"`
	Local                 *LocalAudioStatus `json:"local,omitempty"`
}

// Status is the JSON view returned by GET /api/sip/status.
type Status struct {
	Enabled       bool           `json:"enabled"`
	Listening     bool           `json:"listening"`
	ListenAddr    string         `json:"listen_addr"`
	ListenError   string         `json:"listen_error,omitempty"`
	Realm         string         `json:"realm"`
	Username      string         `json:"username"`
	HasPassword   bool           `json:"has_password"`
	Registrations []Registration `json:"registrations"`
	Calls         []PublicCall   `json:"calls"`
	RTPMin        int            `json:"rtp_min"`
	RTPMax        int            `json:"rtp_max"`
	Audio         AudioStatus    `json:"audio"`
}

// Server is the UDP SIP server. A nil Dial/Answer/Hangup means "no modem":
// INVITEs then fail gracefully with 503 and HTTP calls report the same.
type Server struct {
	cfg       Config
	cfgMu     sync.RWMutex
	audio     AudioBridge
	serverTag string

	// Call-control hooks (injected by the caller).
	Dial   func(number string) error
	Answer func() error
	Hangup func() error
	DTMF   func(digit string) error

	// ModemCallState, when set, reports the modem's own call state
	// (AT+CPAS + AT+CLCC). It is the authority for reconciling the tracked
	// call table: the USB-AT path has no CONNECT/NO CARRIER URC callbacks,
	// so without it a ringing/active record can outlive the real call
	// forever and block every later dial with "another call is already
	// active". CPAS is the authority for "is there any call at all";
	// CLCC entries with an empty number are this firmware's phantom
	// (idx 2 survives ATH while CPAS reads 0) and never describe a call.
	ModemCallState func() (ModemCallState, error)
	OnCallState    func(PublicCall)

	mu            sync.Mutex
	conn          *net.UDPConn
	running       bool
	listenErr     string
	registrations map[string]*Registration
	calls         map[string]*Call
	nonces        map[string]time.Time
	callSeq       int
	stopCh        chan struct{}
	wg            sync.WaitGroup
	pendingRing   *pendingRing
}

type pendingRing struct {
	number string
	timer  *time.Timer
	fired  bool
}

// CLCCEntry is one AT+CLCC line: index, direction, state, mode,
// multiparty, number, type. State: 0 active, 1 held, 2 dialing,
// 3 alerting, 4 incoming, 5 waiting.
type CLCCEntry struct {
	Index  int
	Dir    int
	State  int
	Number string
}

// ModemCallState is one AT+CPAS + AT+CLCC snapshot. CPAS -1 means the
// hook could not read it. ReleaseReason is the most recent call-release
// URC captured from the modem (NO CARRIER / BUSY / NO ANSWER), if any:
// the only witness to why a call the modem released actually ended.
type ModemCallState struct {
	CPAS          int
	CLCC          []CLCCEntry
	ReleaseReason string
}

// RealCalls returns the CLCC entries that describe a real call: only
// entries carrying a number count. The Baiwang phantom (idx 2, empty
// number, state 0 after ATH) is dropped here.
func (m ModemCallState) RealCalls() []CLCCEntry {
	out := make([]CLCCEntry, 0, len(m.CLCC))
	for _, e := range m.CLCC {
		if strings.TrimSpace(e.Number) != "" {
			out = append(out, e)
		}
	}
	return out
}

// New creates a Server. audio may be nil (treated as unavailable).
func New(cfg Config, audio AudioBridge) *Server {
	var tagBytes [8]byte
	_, _ = rand.Read(tagBytes[:])
	return &Server{
		cfg:           cfg,
		audio:         audio,
		serverTag:     "dj4hub-" + hex.EncodeToString(tagBytes[:]),
		registrations: map[string]*Registration{},
		calls:         map[string]*Call{},
		nonces:        map[string]time.Time{},
		stopCh:        make(chan struct{}),
	}
}

// Config returns a copy of the current config (password included; callers
// must never serialise it).
func (s *Server) Config() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// Start binds the UDP listener and serves. When cfg.Enabled is false it
// records that and returns nil. A bind failure is stored as listenErr and
// returned; the caller should keep its HTTP server up regardless.
func (s *Server) Start() error {
	cfg := s.Config()
	if err := cfg.Validate(); err != nil {
		s.mu.Lock()
		s.listenErr = err.Error()
		s.mu.Unlock()
		return err
	}
	if !cfg.Enabled {
		s.mu.Lock()
		s.running = false
		s.listenErr = ""
		s.mu.Unlock()
		log.Printf("[sip] disabled by config")
		return nil
	}
	addr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		s.mu.Lock()
		s.listenErr = err.Error()
		s.mu.Unlock()
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		s.mu.Lock()
		s.listenErr = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("SIP listen %s: %w", cfg.Listen, err)
	}
	s.mu.Lock()
	s.conn = conn
	s.running = true
	s.listenErr = ""
	s.stopCh = make(chan struct{})
	s.mu.Unlock()
	log.Printf("[sip] listening on %s (realm %s, user %s, RTP %d-%d)", cfg.Listen, cfg.Realm, cfg.Username, cfg.RTPMin, cfg.RTPMax)
	s.wg.Add(1)
	go s.readLoop()
	s.wg.Add(1)
	go s.pruneLoop()
	s.wg.Add(1)
	go s.reconcileLoop()
	return nil
}

// Stop closes the listener and ends all calls (RTP sessions included).
func (s *Server) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	conn := s.conn
	s.conn = nil
	calls := make([]*Call, 0, len(s.calls))
	for _, c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	s.wg.Wait()
	for _, c := range calls {
		s.endCall(c, CallStateEnded, "")
	}
	log.Printf("[sip] stopped")
}

// UpdateConfig applies a new config. Changing the listen address or realm
// restarts the listener; other changes apply live.
func (s *Server) UpdateConfig(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	old := s.Config()
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
	if old.Listen != cfg.Listen || old.Enabled != cfg.Enabled {
		s.Stop()
		return s.Start()
	}
	return nil
}

// Status returns the current server snapshot for the HTTP API.
func (s *Server) Status() Status {
	cfg := s.Config()
	s.mu.Lock()
	st := Status{
		Enabled:     cfg.Enabled,
		Listening:   s.running,
		ListenAddr:  cfg.Listen,
		ListenError: s.listenErr,
		Realm:       cfg.Realm,
		Username:    cfg.Username,
		HasPassword: cfg.Password != "",
		RTPMin:      cfg.RTPMin,
		RTPMax:      cfg.RTPMax,
	}
	now := time.Now()
	for _, r := range s.registrations {
		if r.ExpiresAt.After(now) {
			st.Registrations = append(st.Registrations, *r)
		}
	}
	for _, c := range s.calls {
		st.Calls = append(st.Calls, c.public())
	}
	s.mu.Unlock()
	if audio, ok := s.audio.(interface{ AudioSnapshot() AudioStatus }); ok {
		st.Audio = audio.AudioSnapshot()
	} else if s.audio != nil {
		st.Audio = AudioStatus{
			Mode: "qpcmv-stub", Available: s.audio.Available(),
			QPCMVEnabled: s.audio.QPCMVEnabled(), PCMWired: false,
			Detail: s.audio.Status(),
		}
	} else {
		st.Audio = AudioStatus{Mode: "qpcmv-stub", Available: false, Detail: "no audio bridge configured (stub)"}
	}
	return st
}

func (s *Server) startCallMedia(call *Call, rtp *RTPSession, remote *net.UDPAddr) error {
	rtp.mu.Lock()
	rtp.onError = func(err error) {
		s.endCall(call, CallStateFailed, "audio media failed: "+err.Error())
		if s.Hangup != nil {
			_ = s.Hangup()
		}
	}
	rtp.mu.Unlock()
	if err := rtp.Start(remote); err != nil {
		s.endCall(call, CallStateFailed, "audio media failed: "+err.Error())
		if s.Hangup != nil {
			_ = s.Hangup()
		}
		return err
	}
	return nil
}

// Calls returns public views of all tracked calls.
func (s *Server) Calls() []PublicCall { return s.Status().Calls }

// mediaReadyFor reports whether a call's audio is actually flowing right
// now. For a SIP leg that means the RTP session is running and frames have
// moved in either direction (the send loop emits a 20 ms frame once
// running, so TX alone proves the pump is ticking; RX proves peer media
// arrived). For a local call it means the bridge forwarded PCM in either
// direction. A call with no media session yet is never ready.
func (s *Server) mediaReadyFor(call *Call) bool {
	call.mu.Lock()
	rtp := call.rtp
	sipLeg := call.sipLeg
	id := call.ID
	call.mu.Unlock()
	if rtp != nil {
		st := rtp.Stats()
		return st.Running && (st.RXPackets+st.TXPackets > 0)
	}
	if sipLeg {
		return false
	}
	if flow, ok := s.audio.(interface{ LocalMediaFlowing(string) bool }); ok {
		return flow.LocalMediaFlowing(id)
	}
	return false
}

// --- Modem event entry points (called by the app's modem callbacks) ------

// NoteRing records a RING URC. The number usually follows in a +CLIP URC
// within ~1 s, so the outbound INVITE waits briefly (clipWait) for it.
func (s *Server) NoteRing() {
	s.mu.Lock()
	if s.pendingRing != nil && !s.pendingRing.fired {
		s.mu.Unlock()
		return // already waiting for CLIP
	}
	pr := &pendingRing{}
	pr.timer = time.AfterFunc(clipWait, func() { s.firePendingRing() })
	s.pendingRing = pr
	s.mu.Unlock()
}

// NoteClip records a +CLIP URC (incoming caller number). When a RING is
// already pending, the INVITE goes out immediately with the real number.
func (s *Server) NoteClip(number string) {
	number = strings.TrimSpace(number)
	s.mu.Lock()
	if s.pendingRing != nil && !s.pendingRing.fired {
		s.pendingRing.number = number
		s.pendingRing.timer.Stop()
		s.pendingRing.fired = true
		s.mu.Unlock()
		s.IncomingCall(number)
		return
	}
	s.mu.Unlock()
	// No RING seen (CLIP arrived first, or re-delivery): treat as a call.
	s.IncomingCall(number)
}

func (s *Server) firePendingRing() {
	s.mu.Lock()
	if s.pendingRing == nil {
		s.mu.Unlock()
		return
	}
	number := s.pendingRing.number
	s.pendingRing.fired = true
	s.pendingRing = nil
	s.mu.Unlock()
	s.IncomingCall(number)
}

// IncomingCall sends a SIP INVITE to every registered client for a modem
// inbound call. Without a registered client it remains available to the web UI.
func (s *Server) IncomingCall(number string) {
	if number == "" {
		number = "unknown"
	}
	s.mu.Lock()
	for _, c := range s.calls {
		c.mu.Lock()
		if c.Direction == "inbound" && (c.State == CallStateRinging || c.State == CallStateActive) {
			// Already handling this inbound call; just fix up the number.
			changed := false
			if c.Number == "unknown" && number != "unknown" {
				c.Number = number
				changed = true
			}
			snapshot := c.publicLocked()
			callback := c.onState
			c.mu.Unlock()
			s.mu.Unlock()
			if changed && callback != nil {
				callback(snapshot)
			}
			return
		}
		c.mu.Unlock()
	}
	call := s.newCallLocked("inbound", number)
	registrations := make([]*Registration, 0, len(s.registrations))
	now := time.Now()
	for _, r := range s.registrations {
		if r.ExpiresAt.After(now) {
			cp := *r
			registrations = append(registrations, &cp)
		}
	}
	s.mu.Unlock()

	// A registered SIP client owns media preparation; local web calls prepare
	// while ringing. AnswerCall can explicitly claim local audio instead.
	call.mu.Lock()
	call.sipLeg = len(registrations) > 0
	call.mu.Unlock()
	call.setState(CallStateRinging, "")
	call.mu.Lock()
	call.inviteSent = true
	call.pendingDialogs = map[string]pendingDialog{}
	call.mu.Unlock()
	for _, reg := range registrations {
		s.sendInviteToClient(call, reg)
	}
	// No answer from any client within the window -> miss the call.
	time.AfterFunc(inboundNoAnswer, func() {
		call.mu.Lock()
		pending := len(call.pendingDialogs)
		state := call.State
		call.mu.Unlock()
		if state == CallStateRinging && (pending > 0 || len(registrations) == 0) {
			s.cancelPendingDialogs(call)
			call.setStateReason(CallStateMissed, EndReasonNoAnswer, "no SIP client answered")
			if s.Hangup != nil {
				_ = s.Hangup()
			}
		}
	})
}

// ModemConnected reports the modem's CONNECT URC (outbound peer answered).
func (s *Server) ModemConnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.Direction == "outbound" && (c.State == CallStateDialing || c.State == CallStateRinging) {
			c.markModuleConnected()
			select {
			case <-c.connectedCh:
			default:
				close(c.connectedCh)
			}
			return
		}
	}
}

// ModemHungUp reports the modem's NO CARRIER URC (remote hangup). An
// active call ends as a remote hangup; a call that never connected ends
// as a remote cancel — the two must not be conflated.
func (s *Server) ModemHungUp() {
	s.mu.Lock()
	calls := make([]*Call, 0, len(s.calls))
	for _, c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	for _, c := range calls {
		if c.State == CallStateActive || c.State == CallStateRinging || c.State == CallStateDialing {
			reason := EndReasonRemoteCancel
			if c.State == CallStateActive {
				reason = EndReasonRemoteHangup
			}
			s.endCallReason(c, CallStateEnded, reason, "remote hangup (NO CARRIER)")
		}
	}
}

// liveCallsLocked returns the tracked calls that are not yet terminal.
// Caller must hold s.mu.
func (s *Server) liveCallsLocked() []*Call {
	out := make([]*Call, 0, len(s.calls))
	for _, c := range s.calls {
		if c.State == CallStateDialing || c.State == CallStateRinging || c.State == CallStateActive {
			out = append(out, c)
		}
	}
	return out
}

// ReconcileWithModem grounds the tracked call table in the modem's own
// AT+CPAS/AT+CLCC readings and returns the snapshot. With no hook it is a
// no-op returning a zero snapshot (CPAS -1). Rules:
//   - A real CLCC entry, or CPAS 4 (call in progress), marks a live record
//     established: the modem really is handling a call, even when CLCC
//     lags the dial by seconds.
//   - CPAS 0 and no real CLCC entry:
//   - an established record has genuinely ended (remote hangup): end it
//     as Ended, with the modem's release URC as the reason when one was
//     captured (NO CARRIER / BUSY / NO ANSWER).
//   - an unestablished dialling/ringing record is left alone. This modem
//     can report CPAS 0 with an empty CLCC for many seconds while a
//     mobile-originated call is still being set up (CSFB / network
//     paging): the first reconcile tick used to kill such calls ~15s
//     after ATD with "modem reports no active call" while the far end
//     was still ringing. Only a captured release URC ends an
//     unestablished call early (as Failed, with the URC text); a call
//     that never establishes fails at the 60s dial timeout.
//   - CPAS 4 with no real CLCC entry: the modem says a call is in
//     progress. Records are marked established and left alone.
func (s *Server) ReconcileWithModem() (ModemCallState, error) {
	return s.reconcileWithModem(false)
}

// ReconcileBeforeDial is the pre-dial variant used before starting a new
// call: when the modem reports CPAS 0 with no real CLCC entry, every live
// record is stale by definition (the user is starting a fresh call) and
// is ended immediately, so a dead record can never block a new dial.
// Established calls (real CLCC / CPAS 4) are left alone and still block.
func (s *Server) ReconcileBeforeDial() (ModemCallState, error) {
	return s.reconcileWithModem(true)
}

// releaseReason phrases why an established call ended, naming the
// modem's own release URC when one was captured.
func releaseReason(ms ModemCallState) string {
	if ms.ReleaseReason != "" {
		return "remote ended the call (" + ms.ReleaseReason + ")"
	}
	return "remote ended the call (AT+CPAS 0)"
}

// reasonForModemRelease maps the modem's release URC to an end reason for
// one call. A captured BUSY / NO ANSWER URC names the cause directly; a
// bare CPAS 0 falls back to remote hangup for an active call and remote
// cancel for one that never connected.
func reasonForModemRelease(ms ModemCallState, wasActive bool) CallEndReason {
	if reason, ok := MapReleaseURC(ms.ReleaseReason, wasActive); ok {
		return reason
	}
	if wasActive {
		return EndReasonRemoteHangup
	}
	return EndReasonRemoteCancel
}

func (s *Server) reconcileWithModem(beforeDial bool) (ModemCallState, error) {
	if s.ModemCallState == nil {
		return ModemCallState{CPAS: -1}, nil
	}
	ms, err := s.ModemCallState()
	if err != nil {
		return ms, err
	}
	s.mu.Lock()
	live := s.liveCallsLocked()
	s.mu.Unlock()
	if len(live) == 0 {
		return ms, nil
	}
	real := ms.RealCalls()
	if len(real) > 0 || ms.CPAS == 4 || ms.CPAS == 3 {
		for _, c := range live {
			c.mu.Lock()
			c.established = true
			c.mu.Unlock()
		}
	}
	if len(real) > 0 {
		for _, e := range real {
			if e.State != 0 {
				continue
			}
			for _, c := range live {
				if c.Direction != "outbound" {
					continue
				}
				if e.Number != "" && c.Number != "" && e.Number != c.Number {
					continue
				}
				c.mu.Lock()
				hasDialog := c.callID != ""
				c.mu.Unlock()
				if hasDialog {
					s.ModemConnected()
				} else {
					// A real +CLCC active entry is the modem confirming
					// the call connected.
					c.markModuleConnected()
					c.setState(CallStateActive, "")
				}
			}
		}
		return ms, nil
	}
	if ms.CPAS == 0 && len(real) == 0 {
		ended := 0
		for _, c := range live {
			c.mu.Lock()
			est := c.established
			wasActive := c.State == CallStateActive
			c.mu.Unlock()
			switch {
			case c.Direction == "inbound":
				s.endCallReason(c, CallStateEnded, reasonForModemRelease(ms, wasActive), releaseReason(ms))
				ended++
			case est:
				s.endCallReason(c, CallStateEnded, reasonForModemRelease(ms, wasActive), releaseReason(ms))
				ended++
			case beforeDial:
				// Stale-record cleanup before a fresh dial: the
				// record never connected, so its end cause is
				// unknown rather than a hangup.
				s.endCallReason(c, CallStateEnded, EndReasonUnknown, "modem reports no active call (AT+CPAS 0; +CLCC phantom ignored)")
				ended++
			case ms.ReleaseReason != "":
				s.endCallReason(c, CallStateFailed, reasonForModemRelease(ms, wasActive), "call released by modem/network: "+ms.ReleaseReason)
				ended++
			default:
				// Still setting up: leave the record alone. The
				// 60s dial timeout below fails it if the modem
				// never backs it with a real +CLCC entry.
			}
		}
		if ended > 0 {
			log.Printf("[sip] reconcile: resolved %d call record(s), modem CPAS 0", ended)
		}
		// Fall through to the dial-timeout expiry check below.
	}
	now := time.Now()
	expired := false
	for _, c := range live {
		if (c.State == CallStateDialing || c.State == CallStateRinging) && now.Sub(c.StartedAt) > dialConnectTimeout {
			c.setStateReason(CallStateFailed, EndReasonNoAnswer, "no answer from modem (dial timeout, no matching +CLCC entry)")
			expired = true
		}
	}
	if expired {
		log.Printf("[sip] reconcile: expired dialling call(s) with no matching +CLCC entry")
		if s.Hangup != nil {
			_ = s.Hangup()
		}
	}
	return ms, nil
}

// reconcileLoop polls the modem while a call is live and ends records the
// modem no longer backs. It issues no AT commands when no call is tracked,
// so an idle server costs the modem nothing.
func (s *Server) reconcileLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			if s.ModemCallState == nil {
				continue
			}
			s.mu.Lock()
			live := len(s.liveCallsLocked()) > 0
			s.mu.Unlock()
			if !live {
				continue
			}
			if _, err := s.ReconcileWithModem(); err != nil {
				log.Printf("[sip] CPAS reconcile failed: %v", err)
			}
		}
	}
}

// --- HTTP-initiated call control ----------------------------------------

// DialFromAPI dials number via the modem (HTTP POST /api/sip/call). It
// tracks the call in the same table as SIP-leg calls; when a SIP client is
// registered the call is visible to it, and HangupCall can end it.
func (s *Server) DialFromAPI(number string) (string, error) {
	if err := validateNumber(number); err != nil {
		return "", err
	}
	// Ground the busy check in the modem first: a stale record (e.g. a
	// call the modem already ended, or the +CLCC phantom) must not block
	// a new dial. The pre-dial reconcile ends such records when CPAS
	// reads 0; the background loop never kills a young dial on its own.
	ms, err := s.ReconcileBeforeDial()
	if err != nil {
		log.Printf("[sip] CPAS reconcile failed before dial: %v", err)
	}
	s.mu.Lock()
	for _, c := range s.calls {
		if c.State == CallStateDialing || c.State == CallStateRinging || c.State == CallStateActive {
			state := c.State
			s.mu.Unlock()
			if ms.CPAS >= 0 {
				return "", fmt.Errorf("another call is already %s (modem reports AT+CPAS %d)", state, ms.CPAS)
			}
			return "", fmt.Errorf("another call is already %s", state)
		}
	}
	call := s.newCallLocked("outbound", number)
	call.setState(CallStateDialing, "")
	s.mu.Unlock()
	if s.OnCallState != nil {
		s.OnCallState(call.public())
	}
	if s.Dial == nil {
		call.setStateReason(CallStateFailed, EndReasonNoNetwork, ErrNoModem.Error())
		return call.ID, ErrNoModem
	}
	dialStart := time.Now()
	if err := s.Dial(number); err != nil {
		call.setState(CallStateFailed, err.Error())
		return call.ID, err
	}
	log.Printf("[sip] ATD %s accepted in %s", number, time.Since(dialStart).Round(time.Millisecond))
	call.setState(CallStateRinging, "")
	// If CONNECT never arrives, the record is resolved by the CPAS
	// reconciler (5s poll while a call is live): CPAS 0 ends it, a real
	// +CLCC active entry promotes it, and a 60s dial timeout fails it.
	return call.ID, nil
}

// AnswerCall answers the current inbound call via the modem (ATA) and, when
// a SIP leg exists, marks it active.
func (s *Server) AnswerCall(id string) error {
	call, err := s.findCall(id)
	if err != nil {
		return err
	}
	call.mu.Lock()
	dir, state := call.Direction, call.State
	call.mu.Unlock()
	if dir != "inbound" || state != CallStateRinging {
		return fmt.Errorf("no incoming call to answer (call %s is %s %s)", call.ID, dir, state)
	}
	if s.Answer == nil {
		return ErrNoModem
	}
	if err := s.Answer(); err != nil {
		return err
	}
	s.cancelPendingDialogs(call)
	call.mu.Lock()
	call.sipLeg = false
	call.mu.Unlock()
	// ATA accepted: the modem confirmed the call connected.
	call.markModuleConnected()
	call.setState(CallStateActive, "")
	return nil
}

// HangupCall ends a call: BYE to the SIP peer when a dialog exists, ATH to
// the modem, RTP torn down. With an empty id it ends the current call.
// The end reason names the local action: rejecting a ringing inbound call
// is not the same as hanging up an active one or cancelling an outbound
// dial.
func (s *Server) HangupCall(id string) error {
	call, err := s.findCall(id)
	if err != nil {
		return err
	}
	call.mu.Lock()
	dir, state := call.Direction, call.State
	call.mu.Unlock()
	reason := EndReasonLocalHangup
	switch {
	case dir == "inbound" && state == CallStateRinging:
		reason = EndReasonLocalReject
	case state == CallStateDialing || state == CallStateRinging:
		reason = EndReasonLocalCancel
	}
	s.endCallReason(call, CallStateEnded, reason, "")
	if s.Hangup != nil {
		_ = s.Hangup()
	}
	return nil
}

func (s *Server) findCall(id string) (*Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		for _, c := range s.calls {
			if c.State == CallStateDialing || c.State == CallStateRinging || c.State == CallStateActive {
				return c, nil
			}
		}
		return nil, fmt.Errorf("no active call")
	}
	c, ok := s.calls[id]
	if !ok {
		return nil, fmt.Errorf("unknown call %q", id)
	}
	return c, nil
}

// endCall tears down the SIP leg (BYE when a dialog exists, 487 when an
// outbound INVITE is still pending) and RTP. The end reason is inferred
// from the error text; use endCallReason to name it explicitly.
func (s *Server) endCall(c *Call, state CallState, errMsg string) {
	s.endCallReason(c, state, "", errMsg)
}

// endCallReason is endCall with an explicit end reason. The same call
// record is updated in place — ending a call never creates a second
// history entry, and the stable call ID is what the later (persistent)
// history will key on.
func (s *Server) endCallReason(c *Call, state CallState, reason CallEndReason, errMsg string) {
	c.mu.Lock()
	rtp := c.rtp
	c.rtp = nil
	src := c.signallingSrc
	callID := c.callID
	fromTag := c.fromTag
	toTag := c.toTag
	contact := c.remoteContact
	invite := c.inviteReq
	inviteSrc := c.signallingSrc
	prevState := c.State
	cancel := c.cancelCh
	c.mu.Unlock()
	if cancel != nil {
		select {
		case <-cancel:
		default:
			close(cancel)
		}
	}
	if rtp != nil {
		rtp.Stop()
	}
	// A still-pending outbound INVITE (client -> server) gets 487 so the
	// client stops retransmitting.
	if invite != nil && (prevState == CallStateDialing || prevState == CallStateRinging) {
		s.reply(inviteSrc, invite, 487, "Request Terminated", nil, "", "")
	}
	// BYE the SIP peer when this call owns a confirmed dialog.
	if src != nil && callID != "" && contact != "" && prevState == CallStateActive {
		s.sendBye(src, callID, fromTag, toTag, contact)
	}
	c.setStateReason(state, reason, errMsg)
}

// --- internals ----------------------------------------------------------

func (s *Server) newCallLocked(direction, number string) *Call {
	s.callSeq++
	call := &Call{
		ID: fmt.Sprintf("call-%d", s.callSeq), Direction: direction, Number: number,
		State: CallStateDialing, StartedAt: time.Now(), UpdatedAt: time.Now(),
		connectedCh: make(chan struct{}), cancelCh: make(chan struct{}),
		pendingDialogs: map[string]pendingDialog{},
		onState:        s.OnCallState,
	}
	call.mediaCheck = func() bool { return s.mediaReadyFor(call) }
	s.calls[call.ID] = call
	return call
}

func validateNumber(number string) error {
	digits := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '+' {
			return r
		}
		return -1
	}, number)
	digits = strings.TrimPrefix(digits, "+")
	if len(digits) < 3 || len(digits) > 15 {
		return fmt.Errorf("invalid phone number %q", number)
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *Server) pruneLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.pruneNonces()
			s.mu.Lock()
			now := time.Now()
			for user, r := range s.registrations {
				if r.ExpiresAt.Before(now) {
					delete(s.registrations, user)
				}
			}
			// Drop ended/failed/missed calls older than 10 minutes.
			for id, c := range s.calls {
				if (c.State == CallStateEnded || c.State == CallStateFailed || c.State == CallStateMissed) &&
					time.Since(c.UpdatedAt) > 10*time.Minute {
					delete(s.calls, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, 65535)
	for {
		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn == nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-s.stopCh:
					return
				default:
					continue
				}
			}
			select {
			case <-s.stopCh:
				return
			default:
				continue
			}
		}
		msg, err := parseSIPMessage(string(buf[:n]))
		if err != nil {
			log.Printf("[sip] unparsable datagram from %s: %v", src, err)
			continue
		}
		s.dispatch(msg, src)
	}
}

func (s *Server) send(dst *net.UDPAddr, text string) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil || dst == nil {
		return
	}
	_, _ = conn.WriteToUDP([]byte(text), dst)
}

func (s *Server) reply(src *net.UDPAddr, req *sipMessage, code int, reason string, extra map[string]string, body, contentType string) {
	s.send(src, buildResponse(req, code, reason, s.serverTag, extra, body, contentType))
}

// dispatch routes one parsed SIP message.
func (s *Server) dispatch(msg *sipMessage, src *net.UDPAddr) {
	if !msg.IsRequest {
		s.handleResponse(msg, src)
		return
	}
	switch msg.Method {
	case "REGISTER":
		s.handleRegister(msg, src)
	case "INVITE":
		s.handleInvite(msg, src)
	case "ACK":
		s.handleACK(msg, src)
	case "BYE":
		s.handleBye(msg, src)
	case "CANCEL":
		s.handleCancel(msg, src)
	case "OPTIONS":
		s.reply(src, msg, 200, "OK", map[string]string{
			"Allow": "INVITE, ACK, BYE, CANCEL, OPTIONS, REGISTER",
		}, "", "")
	case "MESSAGE":
		// SMS via SIP MESSAGE is explicitly out of scope for this item.
		s.reply(src, msg, 405, "Method Not Allowed", map[string]string{
			"Allow": "INVITE, ACK, BYE, CANCEL, OPTIONS, REGISTER",
		}, "", "")
	default:
		s.reply(src, msg, 501, "Not Implemented", nil, "", "")
	}
}

// --- REGISTER -----------------------------------------------------------

func (s *Server) handleRegister(msg *sipMessage, src *net.UDPAddr) {
	if ok, _ := s.checkAuth(msg, "REGISTER"); !ok {
		s.reply(src, msg, 401, "Unauthorized", map[string]string{
			"WWW-Authenticate": s.challengeWWWAuthenticate(),
		}, "", "")
		return
	}
	contact := msg.header("Contact")
	if contact == "" || contact == "*" {
		// REGISTER with Contact: * and Expires: 0 unregisters.
		if msg.header("Expires") == "0" || headerParam(contact, "expires") == "0" {
			s.mu.Lock()
			delete(s.registrations, s.Config().Username)
			s.mu.Unlock()
			s.reply(src, msg, 200, "OK", nil, "", "")
			return
		}
		s.reply(src, msg, 400, "Bad Request", nil, "", "")
		return
	}
	expires := registerDefault
	if e := msg.header("Expires"); e != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(e)); err == nil && n >= 0 {
			expires = n
		}
	}
	if e := headerParam(contact, "expires"); e != "" {
		if n, err := strconv.Atoi(e); err == nil && n >= 0 {
			expires = n
		}
	}
	if expires == 0 {
		s.mu.Lock()
		delete(s.registrations, s.Config().Username)
		s.mu.Unlock()
		s.reply(src, msg, 200, "OK", nil, "", "")
		return
	}
	reg := &Registration{
		Username: s.Config().Username, Contact: contact,
		Source: src.String(), UserAgent: msg.header("User-Agent"),
		ExpiresAt: time.Now().Add(time.Duration(expires) * time.Second),
	}
	s.mu.Lock()
	s.registrations[s.Config().Username] = reg
	s.mu.Unlock()
	log.Printf("[sip] REGISTER %s from %s (expires %ds)", s.Config().Username, src, expires)
	s.reply(src, msg, 200, "OK", map[string]string{
		"Contact": fmt.Sprintf("%s;expires=%d", contact, expires),
		"Expires": strconv.Itoa(expires),
	}, "", "")
}

// --- INVITE from a SIP client (outbound call via the modem) --------------

func (s *Server) handleInvite(msg *sipMessage, src *net.UDPAddr) {
	// In-dialog re-INVITE on an active call: answer with current SDP.
	if existing := s.findCallByCallID(msg.callID()); existing != nil {
		existing.mu.Lock()
		active := existing.State == CallStateActive
		rtp := existing.rtp
		existing.mu.Unlock()
		if active && rtp != nil {
			s.reply(src, msg, 200, "OK", map[string]string{
				"Contact": fmt.Sprintf("<sip:%s@%s>", s.Config().Username, s.Config().Listen),
			}, buildSDP(rtp.LocalPort()), "application/sdp")
		} else {
			s.reply(src, msg, 486, "Busy Here", nil, "", "")
		}
		return
	}
	if ok, _ := s.checkAuth(msg, "INVITE"); !ok {
		s.reply(src, msg, 401, "Unauthorized", map[string]string{
			"WWW-Authenticate": s.challengeWWWAuthenticate(),
		}, "", "")
		return
	}
	number := sipURIUser(msg.RequestURI)
	if err := validateNumber(number); err != nil {
		s.reply(src, msg, 484, "Address Incomplete", nil, "", "")
		return
	}
	media := parseSDP(msg.Body)
	if !media.HasPCMU {
		s.reply(src, msg, 488, "Not Acceptable Here", nil, "", "")
		return
	}
	if _, err := s.ReconcileBeforeDial(); err != nil {
		log.Printf("[sip] CPAS reconcile failed before INVITE: %v", err)
	}
	s.mu.Lock()
	for _, c := range s.calls {
		if c.State == CallStateDialing || c.State == CallStateRinging || c.State == CallStateActive {
			s.mu.Unlock()
			s.reply(src, msg, 486, "Busy Here", nil, "", "")
			return
		}
	}
	call := s.newCallLocked("outbound", number)
	call.mu.Lock()
	call.sipLeg = true
	call.callID = msg.callID()
	call.fromTag = headerParam(msg.header("From"), "tag")
	call.signallingSrc = src
	call.remoteContact = msg.header("Contact")
	call.inviteReq = msg
	call.inviteSDP = media
	call.mu.Unlock()
	call.setState(CallStateDialing, "")
	s.mu.Unlock()

	s.reply(src, msg, 100, "Trying", nil, "", "")
	go s.runOutboundDial(call, number, src, msg)
}

func (s *Server) runOutboundDial(call *Call, number string, src *net.UDPAddr, invite *sipMessage) {
	if s.Dial == nil {
		call.setState(CallStateFailed, ErrNoModem.Error())
		s.reply(src, invite, 503, "Service Unavailable", nil, "", "")
		return
	}
	if err := s.Dial(number); err != nil {
		call.setState(CallStateFailed, err.Error())
		s.reply(src, invite, 503, "Service Unavailable", nil, "", "")
		return
	}
	call.setState(CallStateRinging, "")
	s.reply(src, invite, 180, "Ringing", nil, "", "")
	select {
	case <-call.connectedCh:
		// Peer answered: offer our RTP port.
		rtp, err := NewRTPSession(s.Config().RTPMin, s.Config().RTPMax, s.audio)
		if err != nil {
			call.setState(CallStateFailed, "RTP allocation failed: "+err.Error())
			s.reply(src, invite, 503, "Service Unavailable", nil, "", "")
			if s.Hangup != nil {
				_ = s.Hangup()
			}
			return
		}
		call.mu.Lock()
		call.rtp = rtp
		call.mu.Unlock()
		s.reply(src, invite, 200, "OK", map[string]string{
			"Contact": fmt.Sprintf("<sip:%s@%s>", s.Config().Username, s.Config().Listen),
		}, buildSDP(rtp.LocalPort()), "application/sdp")
		// Wait for ACK; without it the call never confirms.
		time.AfterFunc(answerTimeout, func() {
			call.mu.Lock()
			state := call.State
			answered := call.answered
			call.mu.Unlock()
			if state == CallStateRinging && !answered {
				s.endCall(call, CallStateEnded, "no ACK from SIP client")
				if s.Hangup != nil {
					_ = s.Hangup()
				}
			}
		})
	case <-call.cancelCh:
		return
	case <-time.After(dialConnectTimeout):
		call.setStateReason(CallStateFailed, EndReasonNoAnswer, "no answer from modem (CONNECT timeout)")
		s.reply(src, invite, 480, "Temporarily Unavailable", nil, "", "")
		if s.Hangup != nil {
			_ = s.Hangup()
		}
	}
}

// --- ACK / BYE / CANCEL --------------------------------------------------

func (s *Server) handleACK(msg *sipMessage, src *net.UDPAddr) {
	call := s.findCallByCallID(msg.callID())
	if call == nil {
		return
	}
	call.mu.Lock()
	if call.State != CallStateRinging || call.answered {
		call.mu.Unlock()
		return
	}
	call.answered = true
	rtp := call.rtp
	media := call.inviteSDP
	call.mu.Unlock()
	call.setState(CallStateActive, "")
	if rtp != nil {
		remote := remoteAddrForSDP(media, src)
		if err := s.startCallMedia(call, rtp, remote); err != nil {
			log.Printf("[sip] RTP start failed: %v", err)
			return
		}
	}
	log.Printf("[sip] call %s active (outbound to %s)", call.ID, call.Number)
}

func (s *Server) handleBye(msg *sipMessage, src *net.UDPAddr) {
	call := s.findCallByCallID(msg.callID())
	if call == nil {
		s.reply(src, msg, 481, "Call/Transaction Does Not Exist", nil, "", "")
		return
	}
	s.reply(src, msg, 200, "OK", nil, "", "")
	s.endCallReason(call, CallStateEnded, EndReasonRemoteHangup, "BYE from SIP client")
	if s.Hangup != nil {
		_ = s.Hangup()
	}
}

func (s *Server) handleCancel(msg *sipMessage, src *net.UDPAddr) {
	call := s.findCallByCallID(msg.callID())
	s.reply(src, msg, 200, "OK", nil, "", "")
	if call == nil {
		return
	}
	call.mu.Lock()
	invite := call.inviteReq
	inviteSrc := call.signallingSrc
	call.mu.Unlock()
	if invite != nil && (call.State == CallStateDialing || call.State == CallStateRinging) {
		s.reply(inviteSrc, invite, 487, "Request Terminated", nil, "", "")
	}
	s.endCallReason(call, CallStateEnded, EndReasonRemoteCancel, "CANCEL from SIP client")
	if s.Hangup != nil {
		_ = s.Hangup()
	}
}

func (s *Server) findCallByCallID(callID string) *Call {
	if callID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		c.mu.Lock()
		match := c.callID == callID
		c.mu.Unlock()
		if match {
			return c
		}
		// Inbound calls we placed: the dialog Call-ID is the key of
		// pendingDialogs.
		c.mu.Lock()
		_, pending := c.pendingDialogs[callID]
		c.mu.Unlock()
		if pending {
			return c
		}
	}
	return nil
}

// --- Responses to INVITEs we sent (inbound modem calls) -----------------

func (s *Server) handleResponse(msg *sipMessage, src *net.UDPAddr) {
	call := s.findCallByCallID(msg.callID())
	if call == nil {
		return
	}
	switch {
	case msg.StatusCode == 180 || msg.StatusCode == 183:
		call.setState(CallStateRinging, "")
	case msg.StatusCode >= 200 && msg.StatusCode < 300:
		s.handleInviteAccepted(call, msg, src)
	case msg.StatusCode >= 400:
		call.mu.Lock()
		delete(call.pendingDialogs, msg.callID())
		remaining := len(call.pendingDialogs)
		state := call.State
		call.mu.Unlock()
		if remaining == 0 && state == CallStateRinging {
			reason, _ := MapSIPRejectReason(msg.StatusCode)
			call.setStateReason(CallStateFailed, reason, fmt.Sprintf("SIP client rejected (%d %s)", msg.StatusCode, msg.Reason))
			if s.Hangup != nil {
				_ = s.Hangup()
			}
		}
	}
}

func (s *Server) handleInviteAccepted(call *Call, msg *sipMessage, src *net.UDPAddr) {
	call.mu.Lock()
	if call.State == CallStateActive || call.answered {
		call.mu.Unlock()
		return
	}
	call.answered = true
	call.sipLeg = true
	call.toTag = headerParam(msg.header("To"), "tag")
	call.signallingSrc = src
	call.remoteContact = msg.header("Contact")
	call.callID = msg.callID()
	pending := call.pendingDialogs
	call.pendingDialogs = map[string]pendingDialog{}
	branch := call.branch
	fromTag := call.fromTag
	call.mu.Unlock()

	// ACK the winner, CANCEL the rest.
	ack := buildRequest("ACK", call.remoteContact, map[string]string{
		"Via":     fmt.Sprintf("SIP/2.0/UDP %s;branch=%s", s.Config().Listen, branch),
		"From":    fmt.Sprintf("<sip:%s@%s>;tag=%s", call.Number, s.Config().Realm, fromTag),
		"To":      msg.header("To"),
		"Call-ID": msg.callID(),
		"CSeq":    fmt.Sprintf("%d ACK", msg.cseqNum()),
	}, "", "")
	s.send(src, ack)
	for dialogID, dlg := range pending {
		if dialogID == msg.callID() {
			continue
		}
		s.sendCancel(dlg.Addr, dialogID, dlg.Contact, call)
	}
	media := parseSDP(msg.Body)
	if !media.HasPCMU {
		call.setState(CallStateFailed, "SIP client answered without PCMU")
		if s.Hangup != nil {
			_ = s.Hangup()
		}
		return
	}
	call.mu.Lock()
	rtp := call.rtp
	call.mu.Unlock()
	var err error
	if rtp == nil {
		rtp, err = NewRTPSession(s.Config().RTPMin, s.Config().RTPMax, s.audio)
	}
	if err != nil {
		call.setState(CallStateFailed, "RTP allocation failed: "+err.Error())
		if s.Hangup != nil {
			_ = s.Hangup()
		}
		return
	}
	call.mu.Lock()
	call.rtp = rtp
	call.mu.Unlock()
	if s.Answer == nil {
		call.setState(CallStateFailed, ErrNoModem.Error()+" (ATA unavailable)")
		rtp.Stop()
		if s.Hangup != nil {
			_ = s.Hangup()
		}
		return
	}
	if err := s.Answer(); err != nil {
		call.setState(CallStateFailed, "ATA failed: "+err.Error())
		rtp.Stop()
		return
	}
	// ATA accepted: the modem confirmed the call connected.
	call.markModuleConnected()
	remote := remoteAddrForSDP(media, src)
	if err := s.startCallMedia(call, rtp, remote); err != nil {
		log.Printf("[sip] RTP start failed: %v", err)
		return
	}
	call.setState(CallStateActive, "")
	log.Printf("[sip] call %s active (inbound from %s)", call.ID, call.Number)
}

// --- Outbound requests (INVITE / CANCEL / BYE to a client) ---------------

func (s *Server) sendInviteToClient(call *Call, reg *Registration) {
	cfg := s.Config()
	callID := randomHex(16) + "@dj4hub"
	branch := "z9hG4bK" + randomHex(12)
	fromTag := "dj4hub-" + randomHex(6)
	dst, err := net.ResolveUDPAddr("udp", reg.Source)
	if err != nil {
		return
	}
	call.mu.Lock()
	// One RTP session per call, shared by all invite legs.
	rtp := call.rtp
	if rtp == nil {
		call.mu.Unlock()
		var err error
		rtp, err = NewRTPSession(cfg.RTPMin, cfg.RTPMax, s.audio)
		if err != nil {
			call.setState(CallStateFailed, "RTP allocation failed: "+err.Error())
			return
		}
		call.mu.Lock()
		call.rtp = rtp
	}
	call.fromTag = fromTag
	call.branch = branch
	call.pendingDialogs[callID] = pendingDialog{Addr: dst, Contact: reg.Contact}
	port := rtp.LocalPort()
	call.mu.Unlock()
	invite := buildRequest("INVITE", reg.Contact, map[string]string{
		"Via":          fmt.Sprintf("SIP/2.0/UDP %s;branch=%s", cfg.Listen, branch),
		"From":         fmt.Sprintf("<sip:%s@%s>;tag=%s", call.Number, cfg.Realm, fromTag),
		"To":           reg.Contact,
		"Call-ID":      callID,
		"CSeq":         "1 INVITE",
		"Contact":      fmt.Sprintf("<sip:%s@%s>", cfg.Username, cfg.Listen),
		"Max-Forwards": "70",
	}, buildSDP(port), "application/sdp")
	s.send(dst, invite)
}

func (s *Server) sendCancel(dst *net.UDPAddr, callID, contact string, call *Call) {
	cfg := s.Config()
	call.mu.Lock()
	fromTag := call.fromTag
	branch := call.branch
	call.mu.Unlock()
	if branch == "" {
		branch = branchFor(callID)
	}
	cancel := buildRequest("CANCEL", contact, map[string]string{
		"Via":     fmt.Sprintf("SIP/2.0/UDP %s;branch=%s", cfg.Listen, branch),
		"From":    fmt.Sprintf("<sip:%s@%s>;tag=%s", call.Number, cfg.Realm, fromTag),
		"To":      contact,
		"Call-ID": callID,
		"CSeq":    "1 CANCEL",
	}, "", "")
	s.send(dst, cancel)
}

func (s *Server) cancelPendingDialogs(call *Call) {
	call.mu.Lock()
	pending := make(map[string]pendingDialog, len(call.pendingDialogs))
	for k, v := range call.pendingDialogs {
		pending[k] = v
	}
	call.pendingDialogs = map[string]pendingDialog{}
	call.mu.Unlock()
	for dialogID, dlg := range pending {
		s.sendCancel(dlg.Addr, dialogID, dlg.Contact, call)
	}
}

func (s *Server) sendBye(dst *net.UDPAddr, callID, fromTag, toTag, contact string) {
	if dst == nil || callID == "" || contact == "" {
		return
	}
	cfg := s.Config()
	to := contact
	if toTag != "" && !strings.Contains(to, "tag=") {
		to += ";tag=" + toTag
	}
	bye := buildRequest("BYE", contact, map[string]string{
		"Via":     fmt.Sprintf("SIP/2.0/UDP %s;branch=%s", cfg.Listen, branchFor(callID)),
		"From":    fmt.Sprintf("<sip:%s@%s>;tag=%s", cfg.Username, cfg.Realm, fromTag),
		"To":      to,
		"Call-ID": callID,
		"CSeq":    "2 BYE",
	}, "", "")
	s.send(dst, bye)
}

// branchFor derives a stable branch token from a Call-ID (used for ACK/BYE/
// CANCEL on dialogs we created, where the original branch is not stored).
func branchFor(callID string) string {
	return "z9hG4bK" + md5Hex(callID)[:12]
}
