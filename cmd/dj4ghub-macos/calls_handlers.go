package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/history"
)

// 交付点 3：最近通话动作接口。
//
// 列表复用 GET /api/history/calls（交付点 2），本文件不另建通话列表
// 接口，也不另起 CLCC 轮询或独立通话监视器；通话数据只读 history store。
// 回拨复用 POST /api/sip/call 的拨号链路，发短信复用 POST /api/sms/send
// 的发送路径：两个动作都先解析出号码，再把请求体重建成原接口的形状
// 后直接调原 handler，不建第二条拨号/发送链路，也不碰 internal/sip
// 的通话控制链。
// 联系人是本仓库此前没有的存储：按评审 §3 做最小本地实现——姓名 +
// 多号码 + 收藏，存在同一 history SQLite 库的 contacts 表（迁移 v2），
// 与通话/短信共用备份与保留口径；系统通讯录导入、批量导入不在本
// 交付点做（评审 §5C 联系人后置）。
func (a *app) registerCallsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/calls/missed-count", a.callsMissedCount)
	mux.HandleFunc("POST /api/calls/missed/seen", a.callsMarkMissedSeen)
	mux.HandleFunc("POST /api/calls/redial", a.callsRedial)
	mux.HandleFunc("POST /api/calls/sms", a.callsSMS)
	mux.HandleFunc("POST /api/calls/save-contact", a.callsSaveContact)
	mux.HandleFunc("GET /api/contacts", a.listContacts)
	mux.HandleFunc("POST /api/contacts", a.saveContact)
	mux.HandleFunc("DELETE /api/contacts", a.deleteContact)
}

func (a *app) callsStore(w http.ResponseWriter) (*history.Store, bool) {
	store, err := a.historyStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return nil, false
	}
	return store, true
}

// resolveCallNumber 从 {call_id} 或直接给的号码解析出原始号码。
// call_id 优先：号码以历史记录为准，不信任调用方随手填的号码。
// 返回的 int 是出错时应写的 HTTP 状态码。
func (a *app) resolveCallNumber(r *http.Request, callID, number string) (string, int, error) {
	number = strings.TrimSpace(number)
	if number != "" {
		if len(number) > 32 {
			return "", http.StatusBadRequest, errors.New("invalid number")
		}
		return number, 0, nil
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return "", http.StatusBadRequest, errors.New("call_id or number is required")
	}
	store, err := a.historyStore()
	if err != nil {
		return "", http.StatusServiceUnavailable, err
	}
	rec, err := store.GetCall(r.Context(), callID)
	if err != nil {
		if errors.Is(err, history.ErrNotFound) {
			return "", http.StatusNotFound, errors.New("call not found")
		}
		return "", http.StatusServiceUnavailable, err
	}
	if strings.TrimSpace(rec.NumberRaw) == "" {
		return "", http.StatusBadRequest, errors.New("call record has no number")
	}
	return rec.NumberRaw, 0, nil
}

// forwardToExisting 把已解析的参数重建成原接口的请求体后调原 handler。
// 原 handler 仍做全部校验与实际工作（拨号、发送、演示模式、落盘）。
func forwardToExisting(w http.ResponseWriter, r *http.Request, body any, handler func(http.ResponseWriter, *http.Request)) {
	payload, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req := r.Clone(r.Context())
	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))
	handler(w, req)
}

// callsMissedCount 返回未接角标数据：未读未接数与未接总数。
// 可带 ?subscription_id= 按卡筛选；不带表示全部卡。
func (a *app) callsMissedCount(w http.ResponseWriter, r *http.Request) {
	store, ok := a.callsStore(w)
	if !ok {
		return
	}
	subscriptionID := historySubscriptionFilter(r)
	unseen, total, err := store.CountMissedUnseen(r.Context(), subscriptionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"missed_unseen":   unseen,
		"missed_total":    total,
		"subscription_id": subscriptionID,
	})
}

// callsMarkMissedSeen 把未读未接标记为已读（用户已在最近通话里看到）。
// body 可空；{subscription_id} 只清该卡的。
func (a *app) callsMarkMissedSeen(w http.ResponseWriter, r *http.Request) {
	store, ok := a.callsStore(w)
	if !ok {
		return
	}
	var body struct {
		SubscriptionID string `json:"subscription_id"`
	}
	// 空 body 也合法：解析失败只当作“全部卡”。
	_ = json.NewDecoder(r.Body).Decode(&body)
	marked, err := store.MarkMissedSeen(r.Context(), strings.TrimSpace(body.SubscriptionID))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "marked": marked})
}

// callsRedial 从一条通话记录回拨：先按 call_id 查出号码，
// 再转调已有的 POST /api/sip/call。无 SIP 服务时原 handler 返回 503，
// 绝不伪造拨号成功。
func (a *app) callsRedial(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CallID string `json:"call_id"`
		Number string `json:"number"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	number, status, err := a.resolveCallNumber(r, body.CallID, body.Number)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	forwardToExisting(w, r, map[string]any{"number": number}, a.sipCall)
}

// callsSMS 从一条通话记录发短信：先按 call_id 查出号码，
// 再转调已有的 POST /api/sms/send（含演示模式与发件箱落盘）。
func (a *app) callsSMS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CallID  string `json:"call_id"`
		Number  string `json:"number"`
		Phone   string `json:"phone"`
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	number := strings.TrimSpace(body.Number)
	if number == "" {
		number = strings.TrimSpace(body.Phone)
	}
	number, status, err := a.resolveCallNumber(r, body.CallID, number)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	forwardToExisting(w, r, map[string]any{"phone": number, "message": body.Message}, a.sendSMS)
}

// callsSaveContact 从一条通话记录保存联系人：按 call_id 查出号码后
// 按号码保存（同号只改名，不产生重复联系人）。
func (a *app) callsSaveContact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CallID string `json:"call_id"`
		Number string `json:"number"`
		Name   string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	number, status, err := a.resolveCallNumber(r, body.CallID, body.Number)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	store, ok := a.callsStore(w)
	if !ok {
		return
	}
	contact, err := store.SaveContact(r.Context(), body.Name, number)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toContactJSON(contact))
}

type contactJSON struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Numbers    []string  `json:"numbers"`
	Normalized []string  `json:"normalized"`
	Favorite   bool      `json:"favorite"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toContactJSON(c history.Contact) contactJSON {
	return contactJSON{
		ID: c.ID, Name: c.Name, Numbers: c.Numbers, Normalized: c.Normalized,
		Favorite: c.Favorite, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func toContactJSONList(contacts []history.Contact) []contactJSON {
	out := make([]contactJSON, 0, len(contacts))
	for _, c := range contacts {
		out = append(out, toContactJSON(c))
	}
	return out
}

func (a *app) listContacts(w http.ResponseWriter, r *http.Request) {
	store, ok := a.callsStore(w)
	if !ok {
		return
	}
	contacts, err := store.ListContacts(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": toContactJSONList(contacts)})
}

// saveContact 保存联系人：
//   - {name, number}：按号码新建或改名；
//   - {id, number}：给该联系人追加一个号码（多号码）；
//   - {id, name?, favorite?}：改名与收藏开关。
func (a *app) saveContact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Number   string `json:"number"`
		Favorite *bool  `json:"favorite"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	store, ok := a.callsStore(w)
	if !ok {
		return
	}
	ctx := r.Context()
	if body.ID > 0 {
		if strings.TrimSpace(body.Number) != "" {
			contact, err := store.AddContactNumber(ctx, body.ID, body.Number)
			if err != nil {
				writeContactError(w, err)
				return
			}
			if strings.TrimSpace(body.Name) != "" || body.Favorite != nil {
				contact, err = store.UpdateContact(ctx, body.ID, body.Name, favoriteOr(contact.Favorite, body.Favorite))
				if err != nil {
					writeContactError(w, err)
					return
				}
			}
			writeJSON(w, http.StatusOK, toContactJSON(contact))
			return
		}
		current, err := store.GetContact(ctx, body.ID)
		if err != nil {
			writeContactError(w, err)
			return
		}
		contact, err := store.UpdateContact(ctx, body.ID, body.Name, favoriteOr(current.Favorite, body.Favorite))
		if err != nil {
			writeContactError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toContactJSON(contact))
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Number) == "" {
		writeError(w, http.StatusBadRequest, "name and number are required")
		return
	}
	contact, err := store.SaveContact(ctx, body.Name, body.Number)
	if err != nil {
		writeContactError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toContactJSON(contact))
}

func favoriteOr(current bool, override *bool) bool {
	if override == nil {
		return current
	}
	return *override
}

func writeContactError(w http.ResponseWriter, err error) {
	if errors.Is(err, history.ErrNotFound) {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (a *app) deleteContact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ID <= 0 {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	store, ok := a.callsStore(w)
	if !ok {
		return
	}
	deleted, err := store.DeleteContact(r.Context(), body.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "history unavailable: "+err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
}
