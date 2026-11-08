// Package subscription 定义 DJ 4G Hub 的订阅（SIM 卡 / Profile）身份模型。
//
// 设计依据：docs/2026-11-08-phone-product-roadmap-review.md §3 ——
// 卡片身份必须先于记录筛选。ICCID 识别订阅/Profile，IMEI 识别模块，
// EID 识别 eUICC，三层不混用；手机号是可空、可更正的属性，绝不作主键。
package subscription

import (
	"strings"
	"time"
)

// NumberSource 说明本机号码的来源。备注号码绝不显示成运营商已验证的结果。
type NumberSource string

const (
	// NumberSourceAuto 是 SIM 自动读取（AT+CNUM）的号码。
	NumberSourceAuto NumberSource = "auto"
	// NumberSourceRemark 是用户手动备注的号码。
	NumberSourceRemark NumberSource = "remark"
	// NumberSourceUnknown 表示号码未知（读不到且无备注）。
	NumberSourceUnknown NumberSource = "unknown"
)

// Label 返回面向用户的来源说明。
func (s NumberSource) Label() string {
	switch s {
	case NumberSourceAuto:
		return "运营商自动读取"
	case NumberSourceRemark:
		return "用户备注"
	default:
		return "未知"
	}
}

// Identity 是当前活动订阅的身份。SubscriptionID 是内部稳定的引用，
// 手机号只是其中的可空属性，不参与身份识别。
type Identity struct {
	SubscriptionID string       `json:"subscription_id"`
	ICCID          string       `json:"iccid"`
	IMEI           string       `json:"imei"`
	EID            string       `json:"eid"`
	Number         string       `json:"number"`
	NumberSource   NumberSource `json:"number_source"`
	UpdatedAt      time.Time    `json:"updated_at"`
	Generation     uint64       `json:"generation"`

	// autoNumber 是 CNUM 自动读取的原始号码（可空），不参与 JSON 序列化。
	// 备注被清除后，有效号码回落到它；它与备注分开保存，避免备注覆盖原始读数。
	autoNumber string
}

// AutoNumber 返回 CNUM 自动读取的原始号码（可空）。
func (id Identity) AutoNumber() string { return id.autoNumber }

// Snapshot 是事件发生当时保存的卡身份快照，供后续交付点（通话/短信记录）
// 在每条记录上保存。历史查询时用快照，绝不用“当前卡”反填全部记录；
// 读不到原始归属的旧记录保持归属未知。
type Snapshot struct {
	SubscriptionID string       `json:"subscription_id"`
	ICCID          string       `json:"iccid"`
	IMEI           string       `json:"imei"`
	EID            string       `json:"eid"`
	Number         string       `json:"number"`
	NumberSource   NumberSource `json:"number_source"`
	CapturedAt     time.Time    `json:"captured_at"`
}

// Snapshot 生成当前身份在这一时刻的快照。
func (id Identity) Snapshot() Snapshot {
	return Snapshot{
		SubscriptionID: id.SubscriptionID,
		ICCID:          id.ICCID,
		IMEI:           id.IMEI,
		EID:            id.EID,
		Number:         id.Number,
		NumberSource:   id.NumberSource,
		CapturedAt:     time.Now(),
	}
}

// DeriveSubscriptionID 推导内部稳定的订阅引用。ICCID 优先；无 ICCID 时
// 用 EID + Profile 标识；两者皆无时返回 "unknown"，调用方不得用手机号
// 或空字符串代替它作主键。
func DeriveSubscriptionID(iccid, eid, profileID string) string {
	if iccid = strings.TrimSpace(iccid); iccid != "" {
		return "iccid:" + iccid
	}
	if eid = strings.TrimSpace(eid); eid != "" {
		if profileID = strings.TrimSpace(profileID); profileID != "" {
			return "eicc:" + eid + ":" + profileID
		}
		return "eicc:" + eid
	}
	return "unknown"
}

// MaskICCID 脱敏 ICCID，只保留末 4 位；短于等于 8 位时全部打码。
func MaskICCID(iccid string) string {
	text := strings.TrimSpace(iccid)
	if text == "" {
		return ""
	}
	if len(text) <= 8 {
		return strings.Repeat("•", len(text))
	}
	return text[:4] + " " + strings.Repeat("•", len(text)-8) + " " + text[len(text)-4:]
}

// MaskNumber 脱敏号码，保留前 4 位与末 4 位数字，中间数字打码，
// 非数字字符（+、空格等）原样保留；数字不足 8 位时不打码。
func MaskNumber(number string) string {
	text := strings.TrimSpace(number)
	digits := 0
	for _, ch := range text {
		if ch >= '0' && ch <= '9' {
			digits++
		}
	}
	if digits <= 8 {
		return text
	}
	var out strings.Builder
	seen := 0
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			out.WriteRune(ch)
			continue
		}
		seen++
		if seen > 4 && seen <= digits-4 {
			out.WriteByte('*')
		} else {
			out.WriteRune(ch)
		}
	}
	return out.String()
}

// NormalizeRemarkNumber 规范化用户备注的号码：去空格、横线与括号，
// 保留可选的 + 前缀；其余须为数字且为 6–15 位。地区不明确时不盲目补
// 国家码，只做去噪与格式校验。
func NormalizeRemarkNumber(input string) (string, bool) {
	s := strings.TrimSpace(input)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "(", "")
	s = strings.ReplaceAll(s, ")", "")
	if s == "" {
		return "", false
	}
	digits := s
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	}
	if len(digits) < 6 || len(digits) > 15 {
		return "", false
	}
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	return s, true
}
