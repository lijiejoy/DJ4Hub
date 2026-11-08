package sip

import "strings"

// CallEndReason explains why a call reached a terminal state. It is kept
// separate from CallState on purpose: "ended" alone cannot tell a rejected
// call from a remote hangup, and "missed" must not absorb busy, cancelled
// or media-failed calls. When the signals do not distinguish the cause,
// the reason is EndReasonUnknown — never a guess.
type CallEndReason string

const (
	// EndReasonUnknown: the available signals do not say why the call ended.
	EndReasonUnknown CallEndReason = "unknown"
	// EndReasonLocalHangup: the user hung up an active call.
	EndReasonLocalHangup CallEndReason = "local_hangup"
	// EndReasonRemoteHangup: the remote party hung up an active call.
	EndReasonRemoteHangup CallEndReason = "remote_hangup"
	// EndReasonLocalReject: the user rejected an incoming call.
	EndReasonLocalReject CallEndReason = "local_reject"
	// EndReasonRemoteCancel: the caller hung up before the call was
	// answered (or the SIP peer cancelled the INVITE).
	EndReasonRemoteCancel CallEndReason = "remote_cancel"
	// EndReasonNoAnswer: nobody answered before the timeout.
	EndReasonNoAnswer CallEndReason = "no_answer"
	// EndReasonNoNetwork: no modem / no service / no dial tone.
	EndReasonNoNetwork CallEndReason = "no_network"
	// EndReasonBusy: the remote party is busy.
	EndReasonBusy CallEndReason = "busy"
	// EndReasonMediaFailed: RTP / audio media could not start or failed.
	EndReasonMediaFailed CallEndReason = "media_failed"
	// EndReasonDeviceDisconnected: the audio device went away mid-call.
	EndReasonDeviceDisconnected CallEndReason = "device_disconnected"
	// EndReasonLocalCancel: the user cancelled an outbound call before
	// it was answered.
	EndReasonLocalCancel CallEndReason = "local_cancel"
)

// EndReasonLabel is the user-facing (Chinese) label for an end reason.
func EndReasonLabel(reason CallEndReason) string {
	switch reason {
	case EndReasonLocalHangup:
		return "本机挂断"
	case EndReasonRemoteHangup:
		return "对方挂断"
	case EndReasonLocalReject:
		return "主动拒接"
	case EndReasonRemoteCancel:
		return "对方取消"
	case EndReasonNoAnswer:
		return "未接超时"
	case EndReasonNoNetwork:
		return "无网络"
	case EndReasonBusy:
		return "忙线"
	case EndReasonMediaFailed:
		return "媒体失败"
	case EndReasonDeviceDisconnected:
		return "设备断开"
	case EndReasonLocalCancel:
		return "本机取消"
	default:
		return "原因未知"
	}
}

// IsTerminalState reports whether a call state is final.
func IsTerminalState(state CallState) bool {
	return state == CallStateEnded || state == CallStateFailed || state == CallStateMissed
}

// MapSIPRejectReason maps a SIP failure response code to an end reason.
// Only codes with an unambiguous meaning map; anything else is unknown.
func MapSIPRejectReason(code int) (CallEndReason, bool) {
	switch code {
	case 486, 600:
		return EndReasonBusy, true
	case 487, 603:
		return EndReasonRemoteCancel, true
	case 408, 480:
		return EndReasonNoAnswer, true
	default:
		return EndReasonUnknown, false
	}
}

// MapReleaseURC maps a modem call-release URC to an end reason. NO CARRIER
// is deliberately state-dependent: after answer it is a remote hangup,
// before answer it is the other side cancelling.
func MapReleaseURC(urc string, wasActive bool) (CallEndReason, bool) {
	upper := strings.ToUpper(urc)
	switch {
	case strings.Contains(upper, "BUSY"):
		return EndReasonBusy, true
	case strings.Contains(upper, "NO ANSWER"):
		return EndReasonNoAnswer, true
	case strings.Contains(upper, "NO DIALTONE"):
		return EndReasonNoNetwork, true
	case strings.Contains(upper, "NO CARRIER"):
		if wasActive {
			return EndReasonRemoteHangup, true
		}
		return EndReasonRemoteCancel, true
	default:
		return EndReasonUnknown, false
	}
}

// InferEndReason derives a reason from a terminal transition's error text
// and call context. It is the fallback for paths that do not name a reason
// explicitly; unknown stays unknown rather than guessing.
func InferEndReason(state CallState, direction string, errText string) CallEndReason {
	upper := strings.ToUpper(errText)
	switch {
	case strings.Contains(upper, "BUSY"):
		return EndReasonBusy
	case strings.Contains(upper, "NO ANSWER"):
		return EndReasonNoAnswer
	case strings.Contains(upper, "NO DIALTONE"):
		return EndReasonNoNetwork
	case strings.Contains(upper, "NO MODEM"), strings.Contains(upper, "NO MODEM ATTACHED"):
		return EndReasonNoNetwork
	case strings.Contains(upper, "MEDIA"), strings.Contains(upper, "RTP"), strings.Contains(upper, "PCMU"):
		return EndReasonMediaFailed
	case strings.Contains(upper, "DISCONNECTED"), strings.Contains(upper, "DEVICE DISCONNECTED"):
		return EndReasonDeviceDisconnected
	case strings.Contains(upper, "DIAL TIMEOUT"), strings.Contains(upper, "NO SIP CLIENT ANSWERED"),
		strings.Contains(upper, "CONNECT TIMEOUT"):
		return EndReasonNoAnswer
	case strings.Contains(upper, "NO CARRIER"):
		if state == CallStateActive {
			return EndReasonRemoteHangup
		}
		return EndReasonRemoteCancel
	default:
		return EndReasonUnknown
	}
}
