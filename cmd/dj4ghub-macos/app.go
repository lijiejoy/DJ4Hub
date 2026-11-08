package main

import (
	"context"
	"sync"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/esim"
	"github.com/WongLoki/DJ4Hub/internal/modem"
	"github.com/WongLoki/DJ4Hub/internal/sip"
	"github.com/WongLoki/DJ4Hub/pkg/smscodec"
)

type app struct {
	background        context.Context
	modem             *modem.Manager
	esimMu            sync.RWMutex
	esim              *esim.Manager
	esimSwitchAllowed bool
	usbAT             *usbAT
	port              string
	demo              bool
	discoveryError    string
	usbDevice         *usbDeviceStatus
	usbATBackoffUntil time.Time
	usbATBackoffErr   string

	smsMu           sync.RWMutex
	sms             []receivedSMS
	smsSendMu       sync.Mutex
	smsPollMu       sync.Mutex
	smsCardIdentity string
	smsReassembler  *smscodec.Reassembler

	smsPollInterval  time.Duration
	smsAutoCleanupME bool
	smsLastPoll      time.Time
	smsLastPollError string

	profileNotesMu     sync.Mutex
	profileNotes       map[string]profileNote
	profileNotesLoaded bool
	profileNotesPath   string

	moduleNotesMu sync.Mutex

	trafficMu        sync.Mutex
	trafficBaselines map[string]networkByteCounters

	// 上游通信历史（612c2f6 合并引入）：与本地 SQLite 历史（internal/history）并存，
	// 统一方案待步骤 08 决定，此处仅保证两套系统各自可用。
	alertsStarted time.Time
	historyMu     sync.Mutex
	history       *communicationHistory

	// 上游模块音频（45622dc 合并引入）：module_audio.go 的会话锁与当前会话；
	// 与 SIP 音频桥（sipAudio）并存，统一方案待步骤 08 决定。
	audioMu      sync.Mutex
	audioSession *moduleAudioSession

	sip               *sip.Server
	sipDir            string
	sipAudio          *sip.CallAudioBridge
	sipCallPollCancel context.CancelFunc
	sipAlerts         *localCallAlerts
	sipPanel          *nativeCallPanel

	// USSD 会话（Item 2，见 ussd_handlers.go）
	ussdMu      sync.Mutex
	ussdSession *ussdSession

	// SMS 送达报告（Item 2，内存存储，与短信列表同模式，上限 500）
	deliveryMu  sync.RWMutex
	delivery    []smsDeliveryRecord
	deliverySeq int

	// 运营商扫描异步状态（Item 3，见 radio_handlers.go）
	scanMu       sync.Mutex
	operatorScan *operatorScanState

	// 小区锁定 / 运营商偏好持久化（Item 3，按 ICCID 索引）
	radioPrefsMu sync.Mutex

	// 定时任务（Item 4，见 tasks_handlers.go）
	tasksMu sync.Mutex
	tasks   []*scheduledTask

	// 通知 / Bot 推送（Batch 1，见 notify_handlers.go）
	notifyMu       sync.Mutex
	notify         *notifySettings
	notifyStatusMu sync.Mutex
	notifyStatus   map[string]notifyChannelStatus
	notifyLogMu    sync.Mutex
	notifyLog      []notifyLogEntry
	notifyRingMu   sync.Mutex
	notifyRingLast map[string]time.Time
	botMu          sync.Mutex
	botOffset      int64
	botPendingMu   sync.Mutex
	botPending     *pendingBotAction
}
