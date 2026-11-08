//go:build darwin && cgo

package main

/*
#cgo pkg-config: libusb-1.0
#include <stdlib.h>
#include <libusb.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	djiUSBVendorID  = 0x2ca3
	djiUSBProductID = 0x4006
)

type usbAT struct {
	modemCallEvents
	ctx         *C.libusb_context
	handle      *C.libusb_device_handle
	iface       int
	endpointIn  byte
	endpointOut byte
	mu          sync.Mutex
	// urcs is the last few modem URC lines seen while draining the bulk
	// interface between commands (NO CARRIER, BUSY, NO ANSWER, RING,
	// +CLCC, +CMTI, +CDS, +CUSD, CME/CMS errors), oldest first, capped
	// at 20, with a capture timestamp each (urcAt, parallel to urcs).
	// The drain used to throw these away, so the service could never
	// learn why the modem released a call; the SIP reconciler reads
	// them back as the release reason. Guarded by urcMu, never taken
	// while holding mu.
	urcMu sync.Mutex
	urcs  []string
	urcAt []time.Time
}

type usbATCandidate struct {
	iface       int
	endpointIn  byte
	endpointOut byte
}

func openDJIUSBAT() (*usbAT, error) {
	var ctx *C.libusb_context
	if rc := C.libusb_init(&ctx); rc != 0 {
		return nil, fmt.Errorf("libusb init: %s", usbErrorName(rc))
	}
	handle := C.libusb_open_device_with_vid_pid(ctx, djiUSBVendorID, djiUSBProductID)
	if handle == nil {
		C.libusb_exit(ctx)
		return nil, errors.New("DJI USB AT device 2ca3:4006 not found")
	}
	candidates, err := usbATCandidates(handle)
	if err != nil {
		C.libusb_close(handle)
		C.libusb_exit(ctx)
		return nil, err
	}
	var lastErr error
	for _, candidate := range candidates {
		if rc := C.libusb_claim_interface(handle, C.int(candidate.iface)); rc != 0 {
			lastErr = fmt.Errorf("claim USB AT interface %d: %s", candidate.iface, usbErrorName(rc))
			continue
		}
		dev := &usbAT{
			ctx:         ctx,
			handle:      handle,
			iface:       candidate.iface,
			endpointIn:  candidate.endpointIn,
			endpointOut: candidate.endpointOut,
		}
		if response, err := dev.Command("AT", 900*time.Millisecond); err == nil && atProbeSucceeded(response) {
			return dev, nil
		} else {
			if err == nil {
				err = fmt.Errorf("unexpected AT probe response %q", response)
			}
			lastErr = fmt.Errorf("probe USB AT interface %d out 0x%02x in 0x%02x: %w",
				candidate.iface, candidate.endpointOut, candidate.endpointIn, err)
		}
		C.libusb_release_interface(handle, C.int(candidate.iface))
	}
	C.libusb_close(handle)
	C.libusb_exit(ctx)
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no USB bulk interface candidates found for DJI AT bridge")
}

func usbATCandidates(handle *C.libusb_device_handle) ([]usbATCandidate, error) {
	dev := C.libusb_get_device(handle)
	if dev == nil {
		return nil, errors.New("libusb device handle has no device")
	}
	var config *C.struct_libusb_config_descriptor
	if rc := C.libusb_get_active_config_descriptor(dev, &config); rc != 0 {
		return nil, fmt.Errorf("get active USB config descriptor: %s", usbErrorName(rc))
	}
	defer C.libusb_free_config_descriptor(config)

	var candidates []usbATCandidate
	interfaces := unsafe.Slice(config._interface, int(config.bNumInterfaces))
	for _, intf := range interfaces {
		altsettings := unsafe.Slice(intf.altsetting, int(intf.num_altsetting))
		for _, alt := range altsettings {
			var endpointIn, endpointOut byte
			endpoints := unsafe.Slice(alt.endpoint, int(alt.bNumEndpoints))
			for _, ep := range endpoints {
				attrs := byte(ep.bmAttributes) & byte(C.LIBUSB_TRANSFER_TYPE_MASK)
				if attrs != byte(C.LIBUSB_TRANSFER_TYPE_BULK) {
					continue
				}
				addr := byte(ep.bEndpointAddress)
				if addr&byte(C.LIBUSB_ENDPOINT_IN) != 0 {
					endpointIn = addr
				} else {
					endpointOut = addr
				}
			}
			if endpointIn != 0 && endpointOut != 0 {
				candidates = append(candidates, usbATCandidate{
					iface:       int(alt.bInterfaceNumber),
					endpointIn:  endpointIn,
					endpointOut: endpointOut,
				})
			}
		}
	}
	return candidates, nil
}

func (u *usbAT) Close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return
	}
	C.libusb_release_interface(u.handle, C.int(u.iface))
	C.libusb_close(u.handle)
	C.libusb_exit(u.ctx)
	u.handle = nil
	u.ctx = nil
}

func (u *usbAT) Command(cmd string, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	payload := []byte(cmd + "\r")
	if err := u.bulkWriteLocked(u.endpointOut, payload, timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var chunks []string
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return strings.Join(chunks, ""), err
		}
		if len(data) == 0 {
			continue
		}
		u.feedCallBytes(string(data))
		chunks = append(chunks, string(data))
		joined := strings.Join(chunks, "")
		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}
	if len(chunks) == 0 {
		return "", errors.New("USB AT command timed out without response")
	}
	return normalizeATResponse(strings.Join(chunks, "")), nil
}

// CommandWithPrompt executes an AT command that enters an interactive input
// state, then submits followUp after the modem returns its ">" prompt.
func (u *usbAT) CommandWithPrompt(cmd string, followUp []byte, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if len(followUp) == 0 {
		return "", errors.New("interactive AT follow-up is empty")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	if err := u.bulkWriteLocked(u.endpointOut, []byte(cmd+"\r"), timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var response strings.Builder
	promptReceived := false
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return normalizeATResponse(response.String()), err
		}
		if len(data) == 0 {
			continue
		}
		u.feedCallBytes(string(data))
		response.Write(data)
		joined := response.String()

		if !promptReceived {
			if atResponseIsError(joined) {
				return normalizeATResponse(joined), nil
			}
			if !atResponseHasPrompt(joined) {
				continue
			}
			if err := u.bulkWriteLocked(u.endpointOut, followUp, time.Until(deadline)); err != nil {
				return normalizeATResponse(joined), err
			}
			promptReceived = true
			continue
		}

		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}

	if promptReceived {
		// ESC cancels a pending message editor on modems that still accept input.
		_ = u.bulkWriteLocked(u.endpointOut, []byte{0x1b}, 300*time.Millisecond)
	}
	if response.Len() == 0 {
		return "", errors.New("USB interactive AT command timed out without response")
	}
	return normalizeATResponse(response.String()), errors.New("USB interactive AT command timed out before completion")
}

// WaitForCUSD waits up to timeout for a "+CUSD:" URC line on the bulk
// interface and returns the raw line. The USB-AT bridge has no background
// URC reader, so a USSD wait must read the interface directly, holding the
// bridge lock for the whole wait so no other command can drain the line in
// between. Callers parse the line with modem.ParseCUSDLine.
func (u *usbAT) WaitForCUSD(timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}
	deadline := time.Now().Add(timeout)
	var buf strings.Builder
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return "", err
		}
		if len(data) == 0 {
			continue
		}
		u.feedCallBytes(string(data))
		buf.Write(data)
		for _, line := range strings.Split(buf.String(), "\n") {
			if idx := strings.Index(line, "+CUSD:"); idx >= 0 {
				return strings.TrimSpace(line[idx:]), nil
			}
		}
	}
	return "", errors.New("timed out waiting for +CUSD response")
}

var errUSBTimeout = errors.New("usb timeout")

func (u *usbAT) drainLocked() {
	for {
		data, err := u.bulkReadLocked(u.endpointIn, 80*time.Millisecond)
		if err != nil {
			return
		}
		u.stashURCs(string(data))
		u.feedCallBytes(string(data))
	}
}

// stashURCs keeps modem URC lines from a drained chunk, oldest first,
// capped at 20. Anything else (echoes, OK, signal reports) is dropped.
func (u *usbAT) stashURCs(chunk string) {
	for _, line := range strings.Split(chunk, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\r"))
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if !(strings.Contains(upper, "NO CARRIER") || strings.Contains(upper, "BUSY") ||
			strings.Contains(upper, "NO ANSWER") || strings.Contains(upper, "NO DIALTONE") ||
			strings.HasPrefix(upper, "RING") || strings.HasPrefix(upper, "+CLCC:") ||
			strings.HasPrefix(upper, "+CMTI:") || strings.HasPrefix(upper, "+CDS:") ||
			strings.HasPrefix(upper, "+CUSD:") || strings.Contains(upper, "CME ERROR") ||
			strings.Contains(upper, "CMS ERROR")) {
			continue
		}
		now := time.Now()
		u.urcMu.Lock()
		u.urcs = append(u.urcs, now.Format("15:04:05")+" "+line)
		u.urcAt = append(u.urcAt, now)
		if len(u.urcs) > 20 {
			u.urcs = append([]string{}, u.urcs[len(u.urcs)-20:]...)
			u.urcAt = append([]time.Time{}, u.urcAt[len(u.urcAt)-20:]...)
		}
		u.urcMu.Unlock()
	}
}

// urcFreshness is how long a captured URC stays attributable to a call.
// A genuine release URC is read by the reconciler within ~10s of being
// captured (polls run every ~5s while a call is live); an older one
// belongs to a previous call and must never fail a fresh dial.
const urcFreshness = 30 * time.Second

// RecentURCs returns the captured URC lines from the last 30 seconds,
// oldest first. Older lines are dropped: they describe a previous call,
// not the live one.
func (u *usbAT) RecentURCs() []string {
	if u == nil {
		return nil
	}
	u.urcMu.Lock()
	defer u.urcMu.Unlock()
	cutoff := time.Now().Add(-urcFreshness)
	var out []string
	for i, line := range u.urcs {
		if i < len(u.urcAt) && u.urcAt[i].After(cutoff) {
			out = append(out, line)
		}
	}
	return out
}

func (u *usbAT) Description() string {
	if u == nil {
		return "USB AT"
	}
	return fmt.Sprintf("USB AT · 2ca3:4006 interface %d out 0x%02x in 0x%02x",
		u.iface, u.endpointOut, u.endpointIn)
}

func (u *usbAT) bulkWriteLocked(endpoint byte, payload []byte, timeout time.Duration) error {
	var transferred C.int
	ptr := unsafe.Pointer(&payload[0])
	rc := C.libusb_bulk_transfer(
		u.handle,
		C.uchar(endpoint),
		(*C.uchar)(ptr),
		C.int(len(payload)),
		&transferred,
		C.uint(boundedTimeoutMillis(timeout)),
	)
	if rc != 0 {
		return fmt.Errorf("USB bulk write: %s", usbErrorName(rc))
	}
	if int(transferred) != len(payload) {
		return fmt.Errorf("USB bulk write short transfer: %d/%d", int(transferred), len(payload))
	}
	return nil
}

func (u *usbAT) bulkReadLocked(endpoint byte, timeout time.Duration) ([]byte, error) {
	buf := make([]byte, 512)
	var transferred C.int
	rc := C.libusb_bulk_transfer(
		u.handle,
		C.uchar(endpoint),
		(*C.uchar)(unsafe.Pointer(&buf[0])),
		C.int(len(buf)),
		&transferred,
		C.uint(boundedTimeoutMillis(timeout)),
	)
	if rc == C.LIBUSB_ERROR_TIMEOUT {
		return nil, errUSBTimeout
	}
	if rc != 0 {
		return nil, fmt.Errorf("USB bulk read: %s", usbErrorName(rc))
	}
	return buf[:int(transferred)], nil
}

// libusb treats a zero timeout as "wait forever" (an infinite transfer), so
// sub-millisecond durations must be rounded up rather than truncated to 0.
// Without this the AT bridge can block forever inside libusb_bulk_transfer.
func boundedTimeoutMillis(timeout time.Duration) int64 {
	ms := timeout.Milliseconds()
	if ms <= 0 {
		return 1
	}
	return ms
}

func usbErrorName(rc C.int) string {
	return C.GoString(C.libusb_error_name(rc))
}
