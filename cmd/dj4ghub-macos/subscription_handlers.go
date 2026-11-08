package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/modem"
	"github.com/WongLoki/DJ4Hub/internal/subscription"
)

// 订阅身份缓存与号码读取（交付点 5，评审 §3 / D4）。
//
// 注意：app.go 不在本交付点的文件范围内，缓存以包级单例保存；
// 后续重构可将其移到 app 结构体上，不改 API、存储格式与行为。
// 备注按 subscription_id 持久化到与 profile-notes 相同的配置目录，
// 与号码读取的 AT 查询无关。
var (
	sharedSubscriptionStore = subscription.NewStore()

	subscriptionRemarksMu     sync.Mutex
	subscriptionRemarksLoaded bool
	// 测试可覆盖备注文件路径，避免单元测试写真实用户配置目录。
	subscriptionRemarksPathOverride string

	subscriptionHookMu            sync.Mutex
	subscriptionHookICCID         string
	subscriptionHookFingerprint   string
	subscriptionRefreshInFlightMu sync.Mutex
	subscriptionRefreshInFlight   bool
)

func (a *app) registerSubscriptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/subscription", a.subscriptionStatus)
	mux.HandleFunc("POST /api/subscription/refresh", a.subscriptionRefresh)
	mux.HandleFunc("POST /api/subscription/remark", a.subscriptionRemark)
}

// subscriptionResponse 是 /api/subscription 系列接口的返回体：
// 内嵌 Identity 的全部字段，另加脱敏 ICCID、来源中文说明与是否过期。
type subscriptionResponse struct {
	subscription.Identity
	ICCIDMasked string `json:"iccid_masked"`
	NumberLabel string `json:"number_label"`
	Stale       bool   `json:"stale"`
}

func newSubscriptionResponse(id subscription.Identity, stale bool) subscriptionResponse {
	return subscriptionResponse{
		Identity:    id,
		ICCIDMasked: subscription.MaskICCID(id.ICCID),
		NumberLabel: id.NumberSource.Label(),
		Stale:       stale,
	}
}

// appSubscriptionReader 把 app 的两种设备路径适配成 subscription.Reader。
// 只读一个 CNUM（AT+CNUM，复用 internal/modem 的既有解析）；ICCID/IMEI
// 在串口路径上取 modem 缓存的 GetFullStatus，不另发 AT；USB-AT 路径
// 上复用 device_status.go 已有的解析助手。EID 本次留空：唯一来源
// esim.Manager.GetEIDs() 每次都会触发硬件 APDU 扫描，刷新路径不得在
// 通话或 eSIM 写入期间增加硬件压力；subscription_id 以 ICCID 为先，
// EID 缺失不影响身份稳定（见最终汇报）。
type appSubscriptionReader struct{ a *app }

func (r *appSubscriptionReader) ReadIdentity(ctx context.Context) (subscription.RawIdentity, error) {
	a := r.a
	if a.demo {
		return subscription.RawIdentity{
			ICCID: "89860123456789012345",
			IMEI:  "867400000000001",
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return subscription.RawIdentity{}, err
	}
	if a.modem != nil {
		st := a.modem.GetFullStatus()
		number, err := a.modem.QueryMSISDN()
		if err != nil {
			// 读不到号码不是身份错误：记 unknown，调用方仍可用 ICCID 识别卡。
			log.Printf("[subscription] CNUM read failed: %v", err)
			number = ""
		}
		return subscription.RawIdentity{
			ICCID:  st.ICCID,
			IMEI:   st.IMEI,
			Number: number,
		}, nil
	}
	if a.usbAT != nil {
		iccid := ""
		if resp, err := a.usbAT.Command("AT+QCCID", 3*time.Second); err == nil {
			iccid = parseUSBATPrefixed(resp, "+QCCID:")
		}
		imei := ""
		if resp, err := a.usbAT.Command("AT+CGSN", 3*time.Second); err == nil {
			imei = parseUSBATIMEI(resp)
		}
		number := ""
		if resp, err := a.usbAT.Command("AT+CNUM", 3*time.Second); err == nil {
			number = modem.ParseCNUM(resp)
		}
		return subscription.RawIdentity{ICCID: iccid, IMEI: imei, Number: number}, nil
	}
	return subscription.RawIdentity{}, errors.New("no modem or USB AT path available")
}

// parseUSBATIMEI 从 AT+CGSN 响应中提取第一行纯数字串号。
func parseUSBATIMEI(resp string) string {
	for _, line := range splitATLines(resp) {
		digits := strings.Map(func(ch rune) rune {
			if ch >= '0' && ch <= '9' {
				return ch
			}
			return -1
		}, line)
		if len(digits) >= 14 {
			return digits
		}
	}
	return ""
}

func (a *app) subscriptionStatus(w http.ResponseWriter, _ *http.Request) {
	ensureSubscriptionRemarksLoaded()
	id, ok := sharedSubscriptionStore.Current()
	if !ok {
		refreshed, err := a.refreshSubscription(10 * time.Second)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("subscription not available: %v", err))
			return
		}
		id = refreshed
	}
	writeJSON(w, http.StatusOK, newSubscriptionResponse(id, false))
}

func (a *app) subscriptionRefresh(w http.ResponseWriter, _ *http.Request) {
	ensureSubscriptionRemarksLoaded()
	id, err := a.refreshSubscription(15 * time.Second)
	if err != nil {
		if current, ok := sharedSubscriptionStore.Current(); ok {
			// 刷新失败时返回上次身份并标记过期，不谎报新读数。
			writeJSON(w, http.StatusOK, newSubscriptionResponse(current, true))
			return
		}
		writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("subscription refresh failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, newSubscriptionResponse(id, false))
}

func (a *app) subscriptionRemark(w http.ResponseWriter, r *http.Request) {
	ensureSubscriptionRemarksLoaded()
	var body struct {
		Number string `json:"number"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if _, ok := sharedSubscriptionStore.Current(); !ok {
		if _, err := a.refreshSubscription(10 * time.Second); err != nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("subscription not available: %v", err))
			return
		}
	}
	id, err := sharedSubscriptionStore.SetRemark(body.Number)
	if err != nil {
		if errors.Is(err, subscription.ErrNoIdentity) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := saveSubscriptionRemarks(); err != nil {
		// 备注已在内存生效，持久化失败只记日志，不把写盘失败伪装成备注失败。
		log.Printf("[subscription] save remarks failed: %v", err)
	}
	writeJSON(w, http.StatusOK, newSubscriptionResponse(id, false))
}

// refreshSubscription 用带超时的上下文走一次读取；已有刷新进行中时
// Store 直接返回缓存身份，不重复查询模块。
func (a *app) refreshSubscription(timeout time.Duration) (subscription.Identity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return sharedSubscriptionStore.Refresh(ctx, &appSubscriptionReader{a: a})
}

// noteDeviceStatusForSubscription 由 device_status.go 的 status() 在每次
// 成功读取设备状态后调用：ICCID 或 USB 设备指纹变化（换卡/换模块/重枚举）
// 时使缓存失效并在后台刷新一次；首次见到 ICCID 时同样触发一次，覆盖
// “连接建立时读取并缓存”的需求，而无需改 main.go 的启动流程。
// 本函数只做比较与触发，不阻塞状态接口；后台刷新失败只记日志。
func (a *app) noteDeviceStatusForSubscription(iccid, fingerprint string) {
	subscriptionHookMu.Lock()
	changed := subscriptionHookICCID != "" && iccid != "" && iccid != subscriptionHookICCID
	fpChanged := subscriptionHookFingerprint != "" && fingerprint != "" &&
		fingerprint != subscriptionHookFingerprint
	first := subscriptionHookICCID == "" && iccid != ""
	if iccid != "" {
		subscriptionHookICCID = iccid
	}
	if fingerprint != "" {
		subscriptionHookFingerprint = fingerprint
	}
	subscriptionHookMu.Unlock()
	if !changed && !fpChanged && !first {
		return
	}
	sharedSubscriptionStore.Invalidate()
	go a.refreshSubscriptionBestEffort()
}

func (a *app) refreshSubscriptionBestEffort() {
	subscriptionRefreshInFlightMu.Lock()
	if subscriptionRefreshInFlight {
		subscriptionRefreshInFlightMu.Unlock()
		return
	}
	subscriptionRefreshInFlight = true
	subscriptionRefreshInFlightMu.Unlock()
	defer func() {
		subscriptionRefreshInFlightMu.Lock()
		subscriptionRefreshInFlight = false
		subscriptionRefreshInFlightMu.Unlock()
	}()
	if _, err := a.refreshSubscription(15 * time.Second); err != nil {
		log.Printf("[subscription] background refresh failed: %v", err)
	}
}

// 备注持久化：与 profile-notes 相同的目录与写盘方式
// （os.UserConfigDir()/DJ 4G Hub/，临时文件 + 重命名，0600）。
func subscriptionRemarksPath() (string, error) {
	if subscriptionRemarksPathOverride != "" {
		return subscriptionRemarksPathOverride, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate subscription remarks directory: %w", err)
	}
	return filepath.Join(configDir, "DJ 4G Hub", "subscription-remarks.json"), nil
}

func ensureSubscriptionRemarksLoaded() {
	subscriptionRemarksMu.Lock()
	defer subscriptionRemarksMu.Unlock()
	if subscriptionRemarksLoaded {
		return
	}
	subscriptionRemarksLoaded = true
	path, err := subscriptionRemarksPath()
	if err != nil {
		log.Printf("[subscription] remarks path unavailable: %v", err)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[subscription] read remarks failed: %v", err)
		}
		return
	}
	var remarks map[string]string
	if len(data) > 0 {
		if err := json.Unmarshal(data, &remarks); err != nil {
			log.Printf("[subscription] parse remarks failed: %v", err)
			return
		}
	}
	sharedSubscriptionStore.SetRemarks(remarks)
}

func saveSubscriptionRemarks() error {
	path, err := subscriptionRemarksPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(sharedSubscriptionStore.Remarks())
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
