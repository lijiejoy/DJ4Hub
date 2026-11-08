package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/history"
	"github.com/WongLoki/DJ4Hub/internal/sip"
	"github.com/WongLoki/DJ4Hub/internal/subscription"
)

// 通信历史存储与 API（交付点 2，评审 §5 阶段 B）。
//
// 接线方式（app.go 不在本交付点的文件范围内，沿用交付点 5 的先例，
// 以包级单例保存存储实例；后续重构可将其移到 app 结构体上，
// 不改 API、存储格式与行为）：
//
//   - historyStore() 懒打开：首次短信落盘或首次历史 API 访问时打开，
//     打开失败不阻塞内存短信缓存与网页，只记故障并阻止模块清理。
//     演示模式用 :memory: 内存库，不污染真实历史。
//   - 通话事件经 internal/sip 的 HistorySink 挂接点写入：
//     ensureHistoryHook() 在存储打开时把回调装到 a.sip 上；
//     USB-AT 模式下短信轮询约 1.2 秒后首次触发，modem 模式下由启动时
//     的 CheckAllSMS 回调触发；窗口内开始且结束的通话可能漏记，
//     /api/history/status 暴露 hook_installed 供核查。
//   - 短信发送不经过本存储自动补发：发件箱只记录尝试结果。

var (
	historyStoreMu       sync.Mutex
	historyStoreInstance *history.Store

	// 测试可覆盖数据库路径，避免单元测试写真实用户配置目录。
	historyPathOverride string

	historyLastErrMu sync.Mutex
	historyLastErr   error
)

// historyStore 返回共享的历史存储，首次调用时懒打开。
// 打开失败返回错误，调用方必须把该失败当作“未持久化”处理。
func (a *app) historyStore() (*history.Store, error) {
	historyStoreMu.Lock()
	defer historyStoreMu.Unlock()
	if historyStoreInstance != nil {
		// 存储是共享的，挂接是每个 app 一份：已打开时也要给当前
		// app 装回调（测试里换 app 的情形同样适用）。
		a.ensureHistoryHook()
		return historyStoreInstance, nil
	}
	path := historyPathOverride
	if path == "" {
		if a.demo {
			path = ":memory:"
		} else {
			p, err := history.DefaultPath()
			if err != nil {
				setHistoryLastError(err)
				return nil, err
			}
			path = p
		}
	}
	st, err := history.Open(path)
	if err != nil {
		setHistoryLastError(err)
		return nil, err
	}
	historyStoreInstance = st
	setHistoryLastError(nil)
	a.ensureHistoryHook()
	return st, nil
}

// historyStorePath 返回当前（或将要）使用的数据库路径，供状态接口使用。
func historyStorePath(a *app) string {
	if historyPathOverride != "" {
		return historyPathOverride
	}
	if a.demo {
		return ":memory:"
	}
	if p, err := history.DefaultPath(); err == nil {
		return p
	}
	return ""
}

// ensureHistoryHook 把通话历史回调装到 SIP 服务上（幂等）。
// 挂接点在 internal/sip/server.go（HistorySink）；回调在每次通话
// 状态事件时触发，不另起 CLCC 轮询监视器。
func (a *app) ensureHistoryHook() {
	if a.sip == nil || a.sip.HistorySink != nil {
		return
	}
	a.sip.HistorySink = func(call sip.PublicCall) {
		a.recordCallHistory(call)
	}
}

func setHistoryLastError(err error) {
	historyLastErrMu.Lock()
	defer historyLastErrMu.Unlock()
	historyLastErr = err
}

func getHistoryLastError() error {
	historyLastErrMu.Lock()
	defer historyLastErrMu.Unlock()
	return historyLastErr
}

func historyLastErrorString() string {
	if err := getHistoryLastError(); err != nil {
		return err.Error()
	}
	return ""
}

// currentHistorySnapshot 取当前订阅身份的事件快照；
// 无缓存身份时返回“归属未知”快照，不用空身份或当前卡事后反填。
func currentHistorySnapshot() subscription.Snapshot {
	if snap, ok := sharedSubscriptionStore.Snapshot(); ok {
		return snap
	}
	return history.UnknownSnapshot()
}

// recordCallHistory 把一次 SIP 通话状态事件写入历史。
// 快照在首次见到该通话时冻结；终结事件带结束时间、时长与结束原因；
// 未接只认 state == missed，拒接/取消/忙线不混入未接。
// 本函数绝不抛错、不阻塞 SIP 事件循环：失败只记故障。
func (a *app) recordCallHistory(call sip.PublicCall) {
	store, err := a.historyStore()
	if err != nil {
		return
	}
	rec := history.CallRecord{
		CallID:          call.ID,
		Direction:       call.Direction,
		NumberRaw:       call.Number,
		Snapshot:        currentHistorySnapshot(),
		StartedAt:       call.StartedAt,
		EndReason:       string(call.EndReason),
		DurationSeconds: nil,
	}
	if !call.ConnectedAt.IsZero() {
		connected := call.ConnectedAt
		rec.ConnectedAt = &connected
		duration := call.DurationSeconds // 服务端只计接通段，振铃不计入。
		rec.DurationSeconds = &duration
	}
	if sip.IsTerminalState(call.State) {
		now := time.Now()
		rec.EndedAt = &now
		rec.EndState = string(call.State)
		rec.Missed = call.State == sip.CallStateMissed
		if rec.EndReason == "" {
			rec.EndReason = string(sip.EndReasonUnknown)
		}
	}
	if err := store.UpsertCall(context.Background(), rec); err != nil {
		setHistoryLastError(err)
	}
}

// persistReceivedSMS 把新收到的短信逐条落盘（增量写入）。
// 解析失败的 PDU 占位条目（Sender 为 "PDU"）不落盘：它没有真实
// 发送方与正文，落盘只会污染历史；但它会让本函数返回错误，
// 从而阻止本轮模块清理——未确认内容绝不随清理丢失。
// 返回 nil 表示本轮全部应保存的短信均已落盘。
func (a *app) persistReceivedSMS(messages []receivedSMS) error {
	if len(messages) == 0 {
		return nil
	}
	store, err := a.historyStore()
	if err != nil {
		return err
	}
	snap := currentHistorySnapshot()
	var firstErr error
	for _, m := range messages {
		if strings.TrimSpace(m.Sender) == "PDU" {
			if firstErr == nil {
				firstErr = errors.New("unparseable SMS PDU kept on module; history persist incomplete")
			}
			continue
		}
		var concatMeta *history.ConcatMeta
		if m.Concat != nil && m.Concat.IsConcat {
			concatMeta = &history.ConcatMeta{Ref: m.Concat.Ref, RefBits: m.Concat.RefBits, Total: m.Concat.Total, Segments: m.Concat.Total, Complete: true}
		}
		if _, err := store.SaveSMS(context.Background(), history.SMSMessage{
			Direction:  "in",
			PeerRaw:    m.Sender,
			Body:       m.Content,
			Snapshot:   snap,
			Concat:     concatMeta,
			ReceivedAt: m.Timestamp,
		}); err != nil {
			setHistoryLastError(err)
			return err
		}
	}
	setHistoryLastError(firstErr)
	return firstErr
}

// recordOutboxAttempt 记一次发送尝试与对应的发出短信行。
// 状态映射：发送成功记 submitted；发送出错一律记 uncertain——
// 当前两个发送通道都不报告失败发生在提交前还是提交后，
// 无法证明未发送时不能记 failed，更不能自动补发。
// failed 状态保留给未来能证明未发送的通道。
func (a *app) recordOutboxAttempt(phone, message string, segments int, mrs []int, sendErr error) {
	store, err := a.historyStore()
	if err != nil {
		return
	}
	status := history.OutboxSubmitted
	errText := ""
	if sendErr != nil {
		status = history.OutboxUncertain
		errText = sendErr.Error()
	}
	if _, _, err := store.RecordOutbound(context.Background(), history.OutboxAttempt{
		PeerRaw:     phone,
		Body:        message,
		Segments:    segments,
		MessageRefs: mrs,
		Snapshot:    currentHistorySnapshot(),
		Status:      status,
		ErrorText:   errText,
	}); err != nil {
		setHistoryLastError(err)
	}
}

// --- HTTP API（供交付点 3、4 的界面使用；本次不做界面） -----------

func (a *app) registerHistoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/history/calls", a.listHistoryCalls)
	mux.HandleFunc("GET /api/history/sms", a.listHistorySMS)
	mux.HandleFunc("GET /api/history/status", a.historyStatus)
}

// parseHistoryPagination 解析 limit/offset：默认 50，上限 100，
// 非法值返回 400。存储层还会再钳制一次，绝无全量返回。
func parseHistoryPagination(r *http.Request) (limit, offset int, err error) {
	limit = 50
	offset = 0
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 || n > 100 {
			return 0, 0, fmt.Errorf("limit must be an integer between 1 and 100")
		}
		limit = n
	}
	if v := strings.TrimSpace(r.URL.Query().Get("offset")); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
}

func historySubscriptionFilter(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("subscription_id"))
}

type historyCallJSON struct {
	ID                 string     `json:"id"`
	Direction          string     `json:"direction"`
	Number             string     `json:"number"`
	NumberNormalized   string     `json:"number_normalized"`
	SubscriptionID     string     `json:"subscription_id"`
	ICCIDMasked        string     `json:"iccid_masked"`
	OwnNumber          string     `json:"own_number,omitempty"`
	NumberSource       string     `json:"number_source"`
	AttributionUnknown bool       `json:"attribution_unknown"`
	StartedAt          time.Time  `json:"started_at"`
	ConnectedAt        *time.Time `json:"connected_at,omitempty"`
	EndedAt            *time.Time `json:"ended_at,omitempty"`
	DurationSeconds    *int64     `json:"duration_seconds,omitempty"`
	EndReason          string     `json:"end_reason,omitempty"`
	EndReasonLabel     string     `json:"end_reason_label,omitempty"`
	EndState           string     `json:"end_state,omitempty"`
	Missed             bool       `json:"missed"`
	Status             string     `json:"status"`
}

func toHistoryCallJSON(rec history.CallRecord) historyCallJSON {
	out := historyCallJSON{
		ID: rec.CallID, Direction: rec.Direction,
		Number: rec.NumberRaw, NumberNormalized: rec.NumberNormalized,
		SubscriptionID:     rec.Snapshot.SubscriptionID,
		ICCIDMasked:        subscription.MaskICCID(rec.Snapshot.ICCID),
		OwnNumber:          rec.Snapshot.Number,
		NumberSource:       string(rec.Snapshot.NumberSource),
		AttributionUnknown: rec.Snapshot.SubscriptionID == history.UnknownSubscriptionID,
		StartedAt:          rec.StartedAt,
		ConnectedAt:        rec.ConnectedAt,
		EndedAt:            rec.EndedAt,
		DurationSeconds:    rec.DurationSeconds,
		EndReason:          rec.EndReason,
		EndState:           rec.EndState,
		Missed:             rec.Missed,
		Status:             rec.Status,
	}
	if rec.EndReason != "" {
		out.EndReasonLabel = sip.EndReasonLabel(sip.CallEndReason(rec.EndReason))
	}
	return out
}

type historySMSJSON struct {
	ID                 int64     `json:"id"`
	Direction          string    `json:"direction"`
	Peer               string    `json:"peer"`
	PeerNormalized     string    `json:"peer_normalized"`
	Body               string    `json:"body"`
	SubscriptionID     string    `json:"subscription_id"`
	ICCIDMasked        string    `json:"iccid_masked"`
	OwnNumber          string    `json:"own_number,omitempty"`
	NumberSource       string    `json:"number_source"`
	AttributionUnknown bool      `json:"attribution_unknown"`
	ConcatRef          *int      `json:"concat_ref,omitempty"`
	ConcatTotal        *int      `json:"concat_total,omitempty"`
	ConcatSegments     *int      `json:"concat_segments,omitempty"`
	ConcatComplete     bool      `json:"concat_complete"`
	IsRead             bool      `json:"is_read"`
	ReceivedAt         time.Time `json:"received_at"`
	OutboxID           *int64    `json:"outbox_id,omitempty"`
}

func toHistorySMSJSON(msg history.SMSMessage) historySMSJSON {
	out := historySMSJSON{
		ID: msg.ID, Direction: msg.Direction,
		Peer: msg.PeerRaw, PeerNormalized: msg.PeerNormalized,
		Body:               msg.Body,
		SubscriptionID:     msg.Snapshot.SubscriptionID,
		ICCIDMasked:        subscription.MaskICCID(msg.Snapshot.ICCID),
		OwnNumber:          msg.Snapshot.Number,
		NumberSource:       string(msg.Snapshot.NumberSource),
		AttributionUnknown: msg.Snapshot.SubscriptionID == history.UnknownSubscriptionID,
		ConcatComplete:     true,
		IsRead:             msg.IsRead,
		ReceivedAt:         msg.ReceivedAt,
		OutboxID:           msg.OutboxID,
	}
	if msg.Concat != nil {
		ref, total, segments := msg.Concat.Ref, msg.Concat.Total, msg.Concat.Segments
		out.ConcatRef, out.ConcatTotal, out.ConcatSegments = &ref, &total, &segments
		out.ConcatComplete = msg.Concat.Complete
	}
	return out
}

func (a *app) listHistoryCalls(w http.ResponseWriter, r *http.Request) {
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	limit, offset, err := parseHistoryPagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := history.CallQuery{
		SubscriptionID: historySubscriptionFilter(r),
		Limit:          limit, Offset: offset,
	}
	switch dir := strings.TrimSpace(r.URL.Query().Get("direction")); dir {
	case "", "inbound", "outbound":
		q.Direction = dir
	default:
		writeError(w, http.StatusBadRequest, "direction must be inbound or outbound")
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("missed")) == "true" {
		q.MissedOnly = true
	}
	rows, total, err := store.ListCalls(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	items := make([]historyCallJSON, 0, len(rows))
	for _, rec := range rows {
		items = append(items, toHistoryCallJSON(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func (a *app) listHistorySMS(w http.ResponseWriter, r *http.Request) {
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	limit, offset, err := parseHistoryPagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := history.SMSQuery{
		SubscriptionID: historySubscriptionFilter(r),
		Limit:          limit, Offset: offset,
	}
	switch dir := strings.TrimSpace(r.URL.Query().Get("direction")); dir {
	case "", "in", "out":
		q.Direction = dir
	default:
		writeError(w, http.StatusBadRequest, "direction must be in or out")
		return
	}
	if peer := strings.TrimSpace(r.URL.Query().Get("peer")); peer != "" {
		q.PeerNormalized = history.NormalizeNumber(peer)
	}
	rows, total, err := store.ListSMS(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	items := make([]historySMSJSON, 0, len(rows))
	for _, msg := range rows {
		items = append(items, toHistorySMSJSON(msg))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func (a *app) historyStatus(w http.ResponseWriter, _ *http.Request) {
	store, err := a.historyStore()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false, "path": historyStorePath(a),
			"last_error": err.Error(),
		})
		return
	}
	sms, calls, outbox, countErr := store.Counts(context.Background())
	version, versionErr := store.SchemaVersion(context.Background())
	hookInstalled := a.sip != nil && a.sip.HistorySink != nil
	resp := map[string]any{
		"available":      true,
		"path":           store.Path(),
		"schema_version": version,
		"counts": map[string]any{
			"sms": sms, "calls": calls, "outbox": outbox,
		},
		"hook_installed": hookInstalled,
		"last_error":     historyLastErrorString(),
	}
	if countErr != nil || versionErr != nil {
		log.Printf("history status degraded: count=%v version=%v", countErr, versionErr)
	}
	writeJSON(w, http.StatusOK, resp)
}
