package main

import (
	"net/http"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

// 轻量初始化诊断（交付点 5，评审 §5 阶段 A / D7）。
//
// 只报告结果与建议，不自动执行重启、重枚举等修复动作；通话期间
// （call_active）修复动作一律视为被阻塞。检查复用已有数据来源：
// 卡/注册来自设备状态（modem 缓存或 USB-AT 状态），麦克风权限与
// 音频设备来自 SIP 音频快照（internal/sip 的既有逻辑），不另造
// 音频生命周期、不在诊断里预热或打开麦克风。

// diagnosticCheck 是单项检查结果。
type diagnosticCheck struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Status     string `json:"status"` // pass | warn | fail | unknown
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion,omitempty"`
}

// initDiagnosticsResponse 是 GET /api/diagnostics/init 的返回体。
type initDiagnosticsResponse struct {
	Checks         []diagnosticCheck `json:"checks"`
	Summary        string            `json:"summary"` // ok | attention | error | unknown
	CallActive     bool              `json:"call_active"`
	RepairsBlocked bool              `json:"repairs_blocked"`
	GeneratedAt    time.Time         `json:"generated_at"`
	Note           string            `json:"note"`
}

// diagnosticsInput 是纯函数 buildInitDiagnostics 的输入，便于单测
// 用模拟数据覆盖各检查分支，不调真实硬件。
type diagnosticsInput struct {
	HasDevice            bool
	SimKnown             bool
	SimInserted          bool
	RegKnown             bool
	RegStatus            int
	MicrophonePermission string
	AudioKnown           bool
	AudioAvailable       bool
	AudioDetail          string
	AudioInput           string
	AudioOutput          string
	CallActive           bool
}

func (a *app) registerDiagnosticsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/diagnostics/init", a.initDiagnostics)
}

func (a *app) initDiagnostics(w http.ResponseWriter, _ *http.Request) {
	in := diagnosticsInput{}
	if a.demo {
		in.HasDevice = true
		in.SimKnown = true
		in.SimInserted = true
		in.RegKnown = true
		in.RegStatus = 1
	} else if a.modem != nil {
		st := a.modem.GetFullStatus()
		in.HasDevice = true
		in.SimKnown = true
		in.SimInserted = st.SimInserted
		in.RegKnown = true
		in.RegStatus = st.RegStatus
	} else if a.usbAT != nil {
		if st, err := a.usbATStatus(); err == nil {
			in.HasDevice = true
			in.SimKnown = true
			in.SimInserted = st.SimInserted
			in.RegKnown = true
			in.RegStatus = st.RegStatus
		} else {
			in.HasDevice = a.currentUSBDevice() != nil
		}
	} else {
		in.HasDevice = a.currentUSBDevice() != nil
	}
	if a.sip != nil {
		st := a.sip.Status()
		in.AudioKnown = true
		in.MicrophonePermission = st.Audio.MicrophonePermission
		in.AudioAvailable = st.Audio.Available
		in.AudioDetail = st.Audio.Detail
		in.AudioInput = st.Audio.InputDevice
		in.AudioOutput = st.Audio.OutputDevice
		for _, call := range st.Calls {
			switch call.State {
			case sip.CallStateDialing, sip.CallStateRinging, sip.CallStateActive:
				in.CallActive = true
			}
		}
	}
	writeJSON(w, http.StatusOK, buildInitDiagnostics(in))
}

// buildInitDiagnostics 由输入拼装六项检查与总览。检查结果与修复动作
// 严格分开：Suggestion 只给文字建议，调用方不得据此自动修复。
func buildInitDiagnostics(in diagnosticsInput) initDiagnosticsResponse {
	checks := []diagnosticCheck{
		simReadyCheck(in),
		registrationCheck(in),
		microphonePermissionCheck(in),
		audioDevicesCheck(in),
		voiceRouteCheck(in),
	}
	// 可恢复原因：汇总第一项失败/告警检查的原因与建议；全部通过时记通过。
	checks = append(checks, recoverableCauseCheck(checks))

	summary := "ok"
	for _, c := range checks {
		if c.Status == "fail" {
			summary = "error"
			break
		}
	}
	if summary != "error" {
		for _, c := range checks {
			if c.Status == "warn" {
				summary = "attention"
			} else if c.Status == "unknown" && summary == "ok" {
				summary = "unknown"
			}
		}
	}
	return initDiagnosticsResponse{
		Checks:         checks,
		Summary:        summary,
		CallActive:     in.CallActive,
		RepairsBlocked: in.CallActive,
		GeneratedAt:    time.Now(),
		Note:           "本诊断只报告结果与建议，不自动执行重启、重枚举等修复动作；通话或 eSIM 写入期间请勿触发修复。",
	}
}

func simReadyCheck(in diagnosticsInput) diagnosticCheck {
	if !in.HasDevice {
		return diagnosticCheck{ID: "sim_ready", Label: "SIM 卡就绪", Status: "unknown",
			Detail: "未检测到 4G 模块", Suggestion: "检查 USB 连接，确认模块已上电并被系统识别。"}
	}
	if !in.SimKnown {
		return diagnosticCheck{ID: "sim_ready", Label: "SIM 卡就绪", Status: "unknown",
			Detail: "尚未读取到卡状态", Suggestion: "等待设备初始化完成，或刷新设备状态后重试。"}
	}
	if in.SimInserted {
		return diagnosticCheck{ID: "sim_ready", Label: "SIM 卡就绪", Status: "pass", Detail: "SIM 卡已插入并就绪"}
	}
	return diagnosticCheck{ID: "sim_ready", Label: "SIM 卡就绪", Status: "fail",
		Detail:     "SIM 卡未就绪（未插入或 PIN 锁定）",
		Suggestion: "检查 SIM 卡是否插好、触点是否清洁；eSIM 请确认 Profile 已启用。"}
}

func registrationCheck(in diagnosticsInput) diagnosticCheck {
	if !in.RegKnown {
		return diagnosticCheck{ID: "registration", Label: "网络注册", Status: "unknown",
			Detail: "尚未读取到注册状态", Suggestion: "等待设备初始化完成，或刷新设备状态后重试。"}
	}
	switch in.RegStatus {
	case 1:
		return diagnosticCheck{ID: "registration", Label: "网络注册", Status: "pass", Detail: "已注册（归属网络）"}
	case 5:
		return diagnosticCheck{ID: "registration", Label: "网络注册", Status: "pass", Detail: "已注册（漫游）"}
	case 2:
		return diagnosticCheck{ID: "registration", Label: "网络注册", Status: "warn",
			Detail: "正在搜索网络", Suggestion: "等待搜网完成；长时间无服务请检查信号与运营商选择。"}
	case 3:
		return diagnosticCheck{ID: "registration", Label: "网络注册", Status: "fail",
			Detail: "注册被拒绝", Suggestion: "确认 SIM 未欠费停机、未被运营商限制；换位置或重启模块前请先确认无通话在进行。"}
	default:
		return diagnosticCheck{ID: "registration", Label: "网络注册", Status: "fail",
			Detail: "未注册", Suggestion: "检查信号强度与运营商选择；确认 SIM 已激活且在覆盖区内。"}
	}
}

func microphonePermissionCheck(in diagnosticsInput) diagnosticCheck {
	switch in.MicrophonePermission {
	case "authorized":
		return diagnosticCheck{ID: "microphone_permission", Label: "麦克风权限", Status: "pass", Detail: "已授权"}
	case "":
		return diagnosticCheck{ID: "microphone_permission", Label: "麦克风权限", Status: "unknown",
			Detail: "SIP 音频未就绪，无法读取权限状态", Suggestion: "确认 SIP 服务已启动后重试。"}
	default:
		return diagnosticCheck{ID: "microphone_permission", Label: "麦克风权限", Status: "fail",
			Detail:     "麦克风权限为 " + in.MicrophonePermission,
			Suggestion: "在“系统设置 > 隐私与安全 > 麦克风”中允许 DJ 4G Hub。"}
	}
}

func audioDevicesCheck(in diagnosticsInput) diagnosticCheck {
	if !in.AudioKnown {
		return diagnosticCheck{ID: "audio_devices", Label: "音频设备", Status: "unknown",
			Detail: "SIP 音频未就绪", Suggestion: "确认 SIP 服务已启动后重试。"}
	}
	if in.AudioAvailable {
		detail := "模块 USB 音频可用"
		if in.AudioInput != "" || in.AudioOutput != "" {
			detail += "：" + in.AudioInput + " / " + in.AudioOutput
		}
		return diagnosticCheck{ID: "audio_devices", Label: "音频设备", Status: "pass", Detail: detail}
	}
	detail := in.AudioDetail
	if detail == "" {
		detail = "音频桥未就绪"
	}
	return diagnosticCheck{ID: "audio_devices", Label: "音频设备", Status: "fail",
		Detail: detail, Suggestion: "检查模块 USB 音频是否已枚举；重新插拔模块（通话期间请勿操作）。"}
}

func voiceRouteCheck(in diagnosticsInput) diagnosticCheck {
	if !in.AudioKnown {
		return diagnosticCheck{ID: "voice_route", Label: "语音路由", Status: "unknown",
			Detail: "SIP 音频未就绪，无法判断语音路由", Suggestion: "确认 SIP 服务已启动后重试。"}
	}
	if !in.AudioAvailable {
		return diagnosticCheck{ID: "voice_route", Label: "语音路由", Status: "fail",
			Detail:     "语音路由不可用：模块 USB 音频未就绪",
			Suggestion: "先解决音频设备问题；不要在通话中切换音频路由。"}
	}
	if in.MicrophonePermission != "" && in.MicrophonePermission != "authorized" {
		return diagnosticCheck{ID: "voice_route", Label: "语音路由", Status: "warn",
			Detail:     "语音路由受限：麦克风未授权，模块音频就绪但本机拾音不可用",
			Suggestion: "在“系统设置 > 隐私与安全 > 麦克风”中允许 DJ 4G Hub。"}
	}
	return diagnosticCheck{ID: "voice_route", Label: "语音路由", Status: "pass", Detail: "语音路由：模块 USB 音频"}
}

func recoverableCauseCheck(checks []diagnosticCheck) diagnosticCheck {
	for _, c := range checks {
		if c.Status == "fail" {
			detail := "可恢复原因：" + c.Label + "——" + c.Detail
			return diagnosticCheck{ID: "recoverable_cause", Label: "可恢复原因", Status: "fail",
				Detail: detail, Suggestion: c.Suggestion}
		}
	}
	for _, c := range checks {
		if c.Status == "warn" {
			return diagnosticCheck{ID: "recoverable_cause", Label: "可恢复原因", Status: "warn",
				Detail: "待观察：" + c.Label + "——" + c.Detail, Suggestion: c.Suggestion}
		}
	}
	for _, c := range checks {
		if c.Status == "unknown" {
			return diagnosticCheck{ID: "recoverable_cause", Label: "可恢复原因", Status: "unknown",
				Detail: "尚有未确认项：" + c.Label + "——" + c.Detail, Suggestion: c.Suggestion}
		}
	}
	return diagnosticCheck{ID: "recoverable_cause", Label: "可恢复原因", Status: "pass", Detail: "未发现需要恢复的问题"}
}
