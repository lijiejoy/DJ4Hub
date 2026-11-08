package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WongLoki/DJ4Hub/internal/history"
	"github.com/WongLoki/DJ4Hub/internal/subscription"
)

// 短信会话、搜索、草稿与导出（交付点 4，评审 §5 阶段 C / §8 交付点 4）。
//
// 复用，不另起一套：
//   - 短信列表复用 GET /api/history/sms（ListSMS）；本文件只在它上面做
//     会话分组（服务端 GROUP BY，分页作用于会话）、搜索、未读标记。
//   - 发送复用 POST /api/sms/send；发送状态只读发件箱
//     （queued/submitted/delivered/failed/uncertain），不新建状态机；
//     uncertain 的行永远不自动补发（交付点 2 口径）。
//   - 草稿存 history 库 sms_drafts 表（迁移 v2），只存本地，
//     不上传、不自动发送；同一号码永远只有一条最新草稿。
//   - 导出由服务端流式生成文件（JSON 或 CSV），分页取数，
//     绝不把全量短信塞进前端内存。

func (a *app) registerConversationsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/conversations", a.listConversations)
	mux.HandleFunc("GET /api/conversations/search", a.searchConversations)
	mux.HandleFunc("GET /api/conversations/thread", a.conversationThread)
	mux.HandleFunc("GET /api/conversations/outbox", a.listConversationOutbox)
	mux.HandleFunc("POST /api/conversations/read", a.markConversationRead)
	mux.HandleFunc("GET /api/conversations/drafts", a.listDrafts)
	mux.HandleFunc("PUT /api/conversations/drafts", a.saveDraft)
	mux.HandleFunc("DELETE /api/conversations/drafts", a.deleteDraft)
	mux.HandleFunc("GET /api/conversations/export", a.exportConversations)
}

// outboxStatusLabel 是发件箱状态的中文展示；uncertain 明确标为
// “结果不确定”，绝不显示成已送达或已读。
func outboxStatusLabel(status string) string {
	switch status {
	case history.OutboxQueued:
		return "已排队"
	case history.OutboxSubmitted:
		return "已提交"
	case history.OutboxDelivered:
		return "已送达"
	case history.OutboxFailed:
		return "发送失败"
	case history.OutboxUncertain:
		return "结果不确定"
	default:
		return ""
	}
}

type conversationJSON struct {
	Peer                  string    `json:"peer"`
	PeerNormalized        string    `json:"peer_normalized"`
	SubscriptionID        string    `json:"subscription_id"`
	ICCIDMasked           string    `json:"iccid_masked"`
	OwnNumber             string    `json:"own_number,omitempty"`
	NumberSource          string    `json:"number_source"`
	AttributionUnknown    bool      `json:"attribution_unknown"`
	LastBody              string    `json:"last_body"`
	LastDirection         string    `json:"last_direction"`
	LastAt                time.Time `json:"last_at"`
	LastOutboxStatus      string    `json:"last_outbox_status,omitempty"`
	LastOutboxStatusLabel string    `json:"last_outbox_status_label,omitempty"`
	UnreadCount           int64     `json:"unread_count"`
	TotalCount            int64     `json:"total_count"`
}

func toConversationJSON(c history.Conversation) conversationJSON {
	out := conversationJSON{
		Peer: c.PeerRaw, PeerNormalized: c.PeerNormalized,
		SubscriptionID:     c.SubscriptionID,
		ICCIDMasked:        subscription.MaskICCID(c.ICCID),
		OwnNumber:          c.OwnNumber,
		NumberSource:       c.NumberSource,
		AttributionUnknown: c.SubscriptionID == history.UnknownSubscriptionID,
		LastBody:           c.LastBody,
		LastDirection:      c.LastDirection,
		LastAt:             c.LastAt,
		LastOutboxStatus:   c.LastOutboxStatus,
		UnreadCount:        c.UnreadCount,
		TotalCount:         c.TotalCount,
	}
	if c.LastOutboxStatus != "" {
		out.LastOutboxStatusLabel = outboxStatusLabel(c.LastOutboxStatus)
	}
	return out
}

func (a *app) listConversations(w http.ResponseWriter, r *http.Request) {
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
	q := history.ConversationQuery{
		SubscriptionID: historySubscriptionFilter(r),
		Limit:          limit, Offset: offset,
	}
	if strings.TrimSpace(r.URL.Query().Get("unread_only")) == "true" {
		q.UnreadOnly = true
	}
	rows, total, err := store.ListConversations(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	items := make([]conversationJSON, 0, len(rows))
	for _, c := range rows {
		items = append(items, toConversationJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

// searchConversations 按号码或正文搜索短信（服务端 LIKE，分页返回）。
// q 为空返回 400；通配符只按字面匹配（存储层转义）。
func (a *app) searchConversations(w http.ResponseWriter, r *http.Request) {
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	if utf8.RuneCountInString(q) > 200 {
		writeError(w, http.StatusBadRequest, "q must be at most 200 characters")
		return
	}
	limit, offset, err := parseHistoryPagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	smsQuery := history.SMSQuery{
		SubscriptionID: historySubscriptionFilter(r),
		Search:         q,
		Limit:          limit, Offset: offset,
	}
	switch dir := strings.TrimSpace(r.URL.Query().Get("direction")); dir {
	case "", "in", "out":
		smsQuery.Direction = dir
	default:
		writeError(w, http.StatusBadRequest, "direction must be in or out")
		return
	}
	rows, total, err := store.ListSMS(r.Context(), smsQuery)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	items := make([]historySMSJSON, 0, len(rows))
	for _, msg := range rows {
		items = append(items, toHistorySMSJSON(msg))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset, "q": q,
	})
}

type threadSMSJSON struct {
	historySMSJSON
	OutboxStatus      string `json:"outbox_status,omitempty"`
	OutboxStatusLabel string `json:"outbox_status_label,omitempty"`
}

// conversationThread 返回一个会话内的短信（倒序分页），
// 发出的短信附带发件箱状态（与发件箱一致的唯一状态来源）。
func (a *app) conversationThread(w http.ResponseWriter, r *http.Request) {
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	peer := history.NormalizeNumber(r.URL.Query().Get("peer"))
	if peer == "" {
		writeError(w, http.StatusBadRequest, "peer is required")
		return
	}
	limit, offset, err := parseHistoryPagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := history.SMSQuery{
		SubscriptionID: historySubscriptionFilter(r),
		PeerNormalized: peer,
		Limit:          limit, Offset: offset,
	}
	rows, total, err := store.ListSMS(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	items, err := a.enrichThreadSMS(r.Context(), store, rows)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
		"peer": rowsPeerRaw(rows), "peer_normalized": peer,
	})
}

func rowsPeerRaw(rows []history.SMSMessage) string {
	for _, m := range rows {
		if m.PeerRaw != "" {
			return m.PeerRaw
		}
	}
	return ""
}

// enrichThreadSMS 给发出的短信补上发件箱状态；收件不带状态。
func (a *app) enrichThreadSMS(ctx context.Context, store *history.Store, rows []history.SMSMessage) ([]threadSMSJSON, error) {
	var ids []int64
	for _, m := range rows {
		if m.Direction == "out" && m.OutboxID != nil {
			ids = append(ids, *m.OutboxID)
		}
	}
	statuses, err := store.OutboxStatusByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]threadSMSJSON, 0, len(rows))
	for _, m := range rows {
		item := threadSMSJSON{historySMSJSON: toHistorySMSJSON(m)}
		if m.Direction == "out" && m.OutboxID != nil {
			item.OutboxStatus = statuses[*m.OutboxID]
			item.OutboxStatusLabel = outboxStatusLabel(item.OutboxStatus)
		}
		items = append(items, item)
	}
	return items, nil
}

type outboxAttemptJSON struct {
	ID             int64     `json:"id"`
	Peer           string    `json:"peer"`
	PeerNormalized string    `json:"peer_normalized"`
	Body           string    `json:"body"`
	Segments       int       `json:"segments"`
	MessageRefs    []int     `json:"message_refs"`
	SubscriptionID string    `json:"subscription_id"`
	Status         string    `json:"status"`
	StatusLabel    string    `json:"status_label"`
	ErrorText      string    `json:"error_text,omitempty"`
	SMSMessageID   *int64    `json:"sms_message_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toOutboxAttemptJSON(at history.OutboxAttempt) outboxAttemptJSON {
	return outboxAttemptJSON{
		ID: at.ID, Peer: at.PeerRaw, PeerNormalized: at.PeerNormalized,
		Body: at.Body, Segments: at.Segments, MessageRefs: at.MessageRefs,
		SubscriptionID: at.Snapshot.SubscriptionID,
		Status:         at.Status, StatusLabel: outboxStatusLabel(at.Status),
		ErrorText: at.ErrorText, SMSMessageID: at.SMSMessageID,
		CreatedAt: at.CreatedAt, UpdatedAt: at.UpdatedAt,
	}
}

// listConversationOutbox 返回发件箱尝试（送达状态的唯一来源），可按会话筛选。
func (a *app) listConversationOutbox(w http.ResponseWriter, r *http.Request) {
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
	q := history.OutboxQuery{
		SubscriptionID: historySubscriptionFilter(r),
		Limit:          limit, Offset: offset,
	}
	if peer := strings.TrimSpace(r.URL.Query().Get("peer")); peer != "" {
		q.PeerNormalized = history.NormalizeNumber(peer)
	}
	switch status := strings.TrimSpace(r.URL.Query().Get("status")); status {
	case "":
	case history.OutboxQueued, history.OutboxSubmitted, history.OutboxDelivered,
		history.OutboxFailed, history.OutboxUncertain:
		q.Status = status
	default:
		writeError(w, http.StatusBadRequest, "status must be queued, submitted, delivered, failed or uncertain")
		return
	}
	rows, total, err := store.ListOutbox(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	items := make([]outboxAttemptJSON, 0, len(rows))
	for _, at := range rows {
		items = append(items, toOutboxAttemptJSON(at))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

// markConversationRead 把一个会话或指定 id 的未读收件标为已读。
// 请求体：{"peer": "10086"} 或 {"ids": [1, 2]}；peer 与 ids 可同时给。
func (a *app) markConversationRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Peer string  `json:"peer"`
		IDs  []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	peer := history.NormalizeNumber(body.Peer)
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	marked, err := store.MarkSMSRead(r.Context(), peer, body.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marked": marked})
}

// --- 草稿 ---------------------------------------------------------------

type draftJSON struct {
	Peer           string    `json:"peer"`
	PeerNormalized string    `json:"peer_normalized"`
	Body           string    `json:"body"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toDraftJSON(d history.SMSDraft) draftJSON {
	return draftJSON{
		Peer: d.PeerRaw, PeerNormalized: d.PeerNormalized,
		Body: d.Body, UpdatedAt: d.UpdatedAt,
	}
}

func (a *app) listDrafts(w http.ResponseWriter, r *http.Request) {
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	rows, err := store.ListDrafts(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	items := make([]draftJSON, 0, len(rows))
	for _, d := range rows {
		items = append(items, toDraftJSON(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// saveDraft 保存某号码的草稿；正文为空（或只含空白）时删除该号码的草稿。
// 草稿只存本地，不上传、不自动发送；正文上限 1000 字，与发送框一致。
func (a *app) saveDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Peer string `json:"peer"`
		Body string `json:"body"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	peer := history.NormalizeNumber(body.Peer)
	if peer == "" {
		writeError(w, http.StatusBadRequest, "peer is required")
		return
	}
	if utf8.RuneCountInString(body.Body) > 1000 {
		writeError(w, http.StatusBadRequest, "body must be at most 1000 characters")
		return
	}
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		if err := store.DeleteDraft(r.Context(), peer); err != nil {
			writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "peer_normalized": peer})
		return
	}
	if err := store.SaveDraft(r.Context(), history.SMSDraft{
		PeerRaw: body.Peer, PeerNormalized: peer, Body: body.Body,
	}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	draft, _, err := store.GetDraft(r.Context(), peer)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toDraftJSON(draft))
}

// deleteDraft 丢弃某号码的草稿（?peer=…）。
func (a *app) deleteDraft(w http.ResponseWriter, r *http.Request) {
	peer := history.NormalizeNumber(r.URL.Query().Get("peer"))
	if peer == "" {
		writeError(w, http.StatusBadRequest, "peer is required")
		return
	}
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	if err := store.DeleteDraft(r.Context(), peer); err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "peer_normalized": peer})
}

// --- 导出 ---------------------------------------------------------------

// exportMaxRows 是单次导出上限：服务端分页取数，绝不无界；
// 超出时 truncated=true，只导出前 exportMaxRows 条（按时间倒序）。
const exportMaxRows = 10000

const exportPageSize = 100

// exportConversations 以文件下载形式导出短信：?format=json|csv（默认 json），
// 可用 peer 只导一个会话、subscription_id 按卡筛选。
// JSON 与 CSV 均为服务端流式生成，分页取数，不进前端内存。
func (a *app) exportConversations(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, "format must be json or csv")
		return
	}
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	baseQuery := history.SMSQuery{
		SubscriptionID: historySubscriptionFilter(r),
	}
	peer := ""
	if p := strings.TrimSpace(r.URL.Query().Get("peer")); p != "" {
		peer = history.NormalizeNumber(p)
		baseQuery.PeerNormalized = peer
	}
	// 先取总数，决定是否截断；再分页流式写出。
	_, total, err := store.ListSMS(r.Context(), history.SMSQuery{
		SubscriptionID: baseQuery.SubscriptionID,
		PeerNormalized: baseQuery.PeerNormalized,
		Limit:          1,
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	truncated := total > exportMaxRows
	stamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("sms-export-%s.%s", stamp, format)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	if format == "csv" {
		a.writeExportCSV(w, r, store, baseQuery, total, truncated)
		return
	}
	a.writeExportJSON(w, r, store, baseQuery, total, truncated)
}

func (a *app) writeExportJSON(w http.ResponseWriter, r *http.Request, store *history.Store, base history.SMSQuery, total int64, truncated bool) {
	enc := json.NewEncoder(w)
	// 手写外层结构，items 逐条流式编码，不在内存里攒全量。
	header := map[string]any{
		"exported_at":     time.Now().UTC(),
		"peer_normalized": base.PeerNormalized,
		"subscription_id": base.SubscriptionID,
		"total":           total,
		"truncated":       truncated,
		"format":          "json",
	}
	headerBytes, _ := json.Marshal(header)
	// 去掉尾部的 }，随后写 "items":[...] 再补 }。
	_, _ = w.Write(headerBytes[:len(headerBytes)-1])
	_, _ = w.Write([]byte(`,"items":[`))
	exported := int64(0)
	first := true
	for offset := 0; exported < total && exported < exportMaxRows; offset += exportPageSize {
		q := base
		q.Limit = exportPageSize
		q.Offset = offset
		rows, _, err := store.ListSMS(r.Context(), q)
		if err != nil {
			// 已开始写响应，只能截断并标注失败，绝不静默假装完整。
			msg, _ := json.Marshal("export interrupted: " + err.Error())
			_, _ = w.Write([]byte(`],"error":` + string(msg) + `}`))
			return
		}
		if len(rows) == 0 {
			break
		}
		items, err := a.enrichThreadSMS(r.Context(), store, rows)
		if err != nil {
			msg, _ := json.Marshal("export interrupted: " + err.Error())
			_, _ = w.Write([]byte(`],"error":` + string(msg) + `}`))
			return
		}
		for _, item := range items {
			if exported >= exportMaxRows {
				break
			}
			if !first {
				_, _ = w.Write([]byte(","))
			}
			first = false
			if err := enc.Encode(item); err != nil {
				return
			}
			exported++
		}
		if len(rows) < exportPageSize {
			break
		}
	}
	_, _ = w.Write([]byte(`]}`))
}

func (a *app) writeExportCSV(w http.ResponseWriter, r *http.Request, store *history.Store, base history.SMSQuery, total int64, truncated bool) {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"id", "direction", "peer", "peer_normalized", "body", "subscription_id", "iccid_masked", "own_number", "received_at", "is_read", "outbox_status", "truncated_note"})
	exported := int64(0)
	for offset := 0; exported < total && exported < exportMaxRows; offset += exportPageSize {
		q := base
		q.Limit = exportPageSize
		q.Offset = offset
		rows, _, err := store.ListSMS(r.Context(), q)
		if err != nil {
			return
		}
		if len(rows) == 0 {
			break
		}
		items, err := a.enrichThreadSMS(r.Context(), store, rows)
		if err != nil {
			return
		}
		for _, item := range items {
			if exported >= exportMaxRows {
				break
			}
			note := ""
			if truncated && exported == exportMaxRows-1 {
				note = "truncated at 10000 rows"
			}
			_ = cw.Write([]string{
				strconv.FormatInt(item.ID, 10),
				item.Direction,
				item.Peer,
				item.PeerNormalized,
				item.Body,
				item.SubscriptionID,
				item.ICCIDMasked,
				item.OwnNumber,
				item.ReceivedAt.Format(time.RFC3339),
				strconv.FormatBool(item.IsRead),
				item.OutboxStatus,
				note,
			})
			exported++
		}
		if len(rows) < exportPageSize {
			break
		}
	}
}
