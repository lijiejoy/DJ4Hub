package sip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"time"
)

// CheckLocalAudio checks real Mac callbacks with silent playback and no recording.
func CheckLocalAudio(ctx context.Context, out io.Writer) error {
	endpoint, devices, err := openLocalAudio("", "")
	if err != nil {
		return err
	}
	defer endpoint.Close()
	fmt.Fprintf(out, "Mac microphone: %s; speakers: %s\n", devices.InputName, devices.OutputName)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < 100; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if _, err := endpoint.ReadPCM(160); err != nil {
			return err
		}
		if err := endpoint.WritePCM(make([]int16, 160)); err != nil {
			return err
		}
	}
	counts := endpoint.Counters()
	fmt.Fprintf(out, "Mac captured=%d microphone_nonzero=%d played=%d dropped=%d underruns=%d (audio not saved)\n",
		counts.Captured, counts.InputNonzero, counts.Played, counts.Dropped, counts.Underruns)
	if counts.Captured == 0 || counts.Played == 0 {
		return errors.New("Mac audio callbacks did not transfer samples")
	}
	return nil
}

// CheckUSBAudio starts only the module streams, plays silence, and checks
// that both hardware callbacks run. It does not dial or save audio.
func CheckUSBAudio(ctx context.Context, out io.Writer) error {
	bridge := NewUSBAudioBridge().(*usbAudioBridge)
	fmt.Fprintln(out, bridge.Status())
	if err := bridge.StartPCM(); err != nil {
		return err
	}
	defer bridge.StopPCM()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < 100; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if err := bridge.WritePCM(make([]int16, 160)); err != nil {
			return err
		}
		if _, err := bridge.ReadPCM(160); err != nil {
			return err
		}
	}
	status := bridge.AudioSnapshot()
	fmt.Fprintf(out, "capture samples=%d, playback samples=%d, dropped=%d, underruns=%d\n",
		status.CapturedSamples, status.PlayedSamples, status.DroppedSamples, status.UnderrunSamples)
	if status.CapturedSamples == 0 || status.PlayedSamples == 0 {
		return errors.New("USB audio callbacks are silent or inactive; check macOS microphone permission")
	}
	return nil
}

// RunCallDiagnostic calls only the explicitly supplied number through an
// already running local SIP server. It sends a quiet intermittent test tone,
// counts received non-silent samples without saving them, and hangs up.
func RunCallDiagnostic(ctx context.Context, cfg Config, number string, duration time.Duration, out io.Writer) error {
	if err := validateNumber(number); err != nil {
		return err
	}
	server, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		return err
	}
	if !server.IP.IsLoopback() {
		return errors.New("call diagnostic requires a local SIP server")
	}
	client, err := net.DialUDP("udp", nil, server)
	if err != nil {
		return err
	}
	defer client.Close()
	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return err
	}
	defer media.Close()
	port := media.LocalAddr().(*net.UDPAddr).Port
	uri := "sip:" + number + "@" + cfg.Listen
	callID := randomHex(12) + "@dj4hub-diagnostic"
	headers := map[string]string{
		"Via":  "SIP/2.0/UDP " + client.LocalAddr().String() + ";branch=z9hG4bK" + randomHex(10),
		"From": fmt.Sprintf("<sip:%s@%s>;tag=%s", cfg.Username, cfg.Realm, randomHex(6)),
		"To":   "<" + uri + ">", "Call-ID": callID, "CSeq": "1 INVITE",
		"Contact":      "<sip:diagnostic@" + client.LocalAddr().String() + ">",
		"Max-Forwards": "70",
	}
	send := func(method, body string) error {
		contentType := ""
		if body != "" {
			contentType = "application/sdp"
		}
		_, err := client.Write([]byte(buildRequest(method, uri, headers, body, contentType)))
		return err
	}
	buf := make([]byte, 8192)
	read := func() (*sipMessage, error) {
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, err := client.Read(buf)
			if e, ok := err.(net.Error); ok && e.Timeout() {
				continue
			}
			if err != nil {
				return nil, err
			}
			msg, err := parseSIPMessage(string(buf[:n]))
			if err != nil {
				return nil, err
			}
			if msg.callID() == callID {
				return msg, nil
			}
		}
	}
	if err := send("INVITE", buildSDP(port)); err != nil {
		return err
	}
	challenge, err := read()
	if err != nil {
		return err
	}
	if challenge.StatusCode != 401 {
		return fmt.Errorf("expected SIP authentication challenge, got %d", challenge.StatusCode)
	}
	headers["To"] = challenge.header("To")
	headers["CSeq"] = "1 ACK"
	if err := send("ACK", ""); err != nil {
		return err
	}
	params := parseAuthParams(challenge.header("WWW-Authenticate"))
	response := expectedDigestResponse(cfg.Username, cfg.Realm, cfg.Password, "INVITE", uri, params["nonce"], "auth", "00000001", "diagnostic")
	headers["Authorization"] = fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", qop=auth, nc=00000001, cnonce="diagnostic"`,
		cfg.Username, cfg.Realm, params["nonce"], uri, response)
	headers["Via"] = "SIP/2.0/UDP " + client.LocalAddr().String() + ";branch=z9hG4bK" + randomHex(10)
	headers["To"] = "<" + uri + ">"
	headers["CSeq"] = "2 INVITE"
	if err := send("INVITE", buildSDP(port)); err != nil {
		return err
	}
	answered := false
	defer func() {
		if answered {
			headers["CSeq"] = "3 BYE"
			_ = send("BYE", "")
		} else {
			headers["CSeq"] = "2 CANCEL"
			_ = send("CANCEL", "")
		}
	}()
	fmt.Fprintf(out, "Calling %s; please answer. No call audio is saved.\n", number)
	for {
		msg, err := read()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "SIP %d %s\n", msg.StatusCode, msg.Reason)
		if msg.StatusCode >= 300 {
			return fmt.Errorf("call rejected: SIP %d %s", msg.StatusCode, msg.Reason)
		}
		if msg.StatusCode != 200 {
			continue
		}
		headers["To"] = msg.header("To")
		headers["CSeq"] = "2 ACK"
		if err := send("ACK", ""); err != nil {
			return err
		}
		answered = true
		remote := remoteAddrForSDP(parseSDP(msg.Body), server)
		if remote == nil || remote.Port <= 0 {
			return errors.New("server did not negotiate a valid RTP endpoint")
		}
		fmt.Fprintln(out, "Connected: sending a quiet 440 Hz intermittent tone; speak into the phone to test the return path.")
		return diagnosticMedia(ctx, client, media, remote, callID, duration, out)
	}
}

func diagnosticMedia(ctx context.Context, signalling, media *net.UDPConn, remote *net.UDPAddr, callID string, duration time.Duration, out io.Writer) error {
	if duration < time.Second || duration > time.Minute {
		return errors.New("test duration must be between 1 and 60 seconds")
	}
	start := time.Now()
	var packets, nonzero, samples uint64
	var peak int
	var frame uint32
	next := start
	buf := make([]byte, 2048)
	for time.Since(start) < duration {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !time.Now().Before(next) {
			packet := make([]byte, 172)
			packet[0] = 0x80
			binary.BigEndian.PutUint16(packet[2:4], uint16(frame))
			binary.BigEndian.PutUint32(packet[4:8], frame*160)
			binary.BigEndian.PutUint32(packet[8:12], 0x444a3448)
			for i := 0; i < 160; i++ {
				sample := int16(0)
				if (frame/50)%2 == 0 {
					sample = int16(1800 * math.Sin(2*math.Pi*440*float64(frame*160+uint32(i))/8000))
				}
				packet[12+i] = LinearToUlaw(sample)
			}
			if _, err := media.WriteToUDP(packet, remote); err != nil {
				return err
			}
			frame++
			next = start.Add(time.Duration(frame) * 20 * time.Millisecond)
		}
		_ = media.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		n, source, err := media.ReadFromUDP(buf)
		if err == nil && source.Port == remote.Port && source.IP.Equal(remote.IP) && n >= 12 && buf[1]&0x7f == 0 {
			packets++
			for _, sample := range UlawToPCM(buf[12:n]) {
				samples++
				v := int(sample)
				if v < 0 {
					v = -v
				}
				if v > peak {
					peak = v
				}
				if v > 64 {
					nonzero++
				}
			}
		}
		_ = signalling.SetReadDeadline(time.Now())
		n, err = signalling.Read(buf)
		if err == nil {
			msg, parseErr := parseSIPMessage(string(buf[:n]))
			if parseErr == nil && msg.callID() == callID && msg.Method == "BYE" {
				_, _ = signalling.Write([]byte(buildResponse(msg, 200, "OK", "", nil, "", "")))
				break
			}
		}
	}
	fmt.Fprintf(out, "RTP sent=%d received=%d; received samples=%d audible samples=%d peak=%d (audio not saved)\n", frame, packets, samples, nonzero, peak)
	if packets == 0 {
		return errors.New("no RTP received from server")
	}
	if nonzero == 0 {
		return errors.New("RTP received, but module capture contained no audible samples")
	}
	return nil
}
