package subscription

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// RawIdentity 是从模块读到的原始身份字段。Number 为 CNUM 自动读取结果，
// 可空；EID 由调用方在有现成 eUICC 信息时填入，本包不主动触发硬件扫描。
type RawIdentity struct {
	ICCID  string
	IMEI   string
	EID    string
	Number string
}

// Reader 是订阅身份的读取来源。实现方只在连接建立、换卡/Profile 切换
// 或用户手动刷新时调用它，不得各自频繁查询模块（评审 §6 第 5 点）。
type Reader interface {
	ReadIdentity(ctx context.Context) (RawIdentity, error)
}

// ErrBusy 表示已有一次刷新正在进行，调用方应直接使用缓存身份。
var ErrBusy = errors.New("subscription refresh already in progress")

// ErrNoIdentity 表示尚未成功读取过任何订阅身份。
var ErrNoIdentity = errors.New("no subscription identity has been read yet")

// Store 缓存当前订阅身份与用户备注号码。
//
// 缓存失效规则：
//   - 连接代次/卡身份变化（ICCID 或 USB 设备指纹变化）时由调用方调
//     Invalidate()，下一次读取重新走 Reader；
//   - 用户手动刷新直接调 Refresh()；
//   - 备注按 subscription_id 索引，换卡后旧卡的备注不串到新卡上，
//     回到旧卡时备注仍在。
//
// 备注号码的优先级高于自动读取，但清除备注后回落到自动读取的原始号码。
type Store struct {
	mu         sync.Mutex
	current    *Identity
	remarks    map[string]string
	generation uint64
	refreshed  bool
	refreshing bool
}

// NewStore 新建空缓存。备注通过 SetRemarks 载入（持久化由调用方负责）。
func NewStore() *Store {
	return &Store{remarks: make(map[string]string)}
}

// SetRemarks 载入按 subscription_id 索引的备注号码（启动时从磁盘恢复）。
func (s *Store) SetRemarks(remarks map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]string, len(remarks))
	for id, number := range remarks {
		if id == "" || number == "" {
			continue
		}
		if norm, ok := NormalizeRemarkNumber(number); ok {
			next[id] = norm
		}
	}
	s.remarks = next
	// 已缓存的当前身份按新载入的备注重新解析有效号码。
	if s.current != nil {
		s.applyRemarkLocked(s.current)
	}
}

// Remarks 返回备注号码的副本（调用方持久化用）。
func (s *Store) Remarks() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.remarks))
	for id, number := range s.remarks {
		out[id] = number
	}
	return out
}

// Current 返回缓存的当前身份。
func (s *Store) Current() (Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return Identity{}, false
	}
	return *s.current, true
}

// Snapshot 返回当前身份的事件快照；无缓存身份时返回 false，调用方
// 应把该事件记为归属未知，而不是用空身份或当前卡事后反填。
func (s *Store) Snapshot() (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return Snapshot{}, false
	}
	return s.current.Snapshot(), true
}

// Invalidate 使缓存失效：清除当前身份并递增代次。下一次读取重新走
// Reader；进行中的刷新不受影响（它完成后会写入新身份）。
func (s *Store) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = nil
	s.refreshed = false
	s.generation++
}

// Refresh 重新读取身份并更新缓存。订阅引用变化时递增代次；备注按新
// subscription_id 重新解析。已有刷新进行中时直接返回缓存身份，
// 无缓存时返回 ErrBusy，调用方不得因此反复查询模块。
func (s *Store) Refresh(ctx context.Context, reader Reader) (Identity, error) {
	s.mu.Lock()
	if s.refreshing {
		cur := s.current
		s.mu.Unlock()
		if cur != nil {
			return *cur, nil
		}
		return Identity{}, ErrBusy
	}
	s.refreshing = true
	s.mu.Unlock()

	raw, err := reader.ReadIdentity(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = false
	if err != nil {
		return Identity{}, err
	}
	id := DeriveSubscriptionID(raw.ICCID, raw.EID, "")
	if s.current == nil || s.current.SubscriptionID != id {
		s.generation++
	}
	next := &Identity{
		SubscriptionID: id,
		ICCID:          raw.ICCID,
		IMEI:           raw.IMEI,
		EID:            raw.EID,
		UpdatedAt:      time.Now(),
		Generation:     s.generation,
		autoNumber:     raw.Number,
	}
	s.applyRemarkLocked(next)
	s.current = next
	s.refreshed = true
	return *next, nil
}

// SetRemark 设置或更正当前订阅的备注号码；空字符串表示清除备注，
// 清除后有效号码回落到 CNUM 自动读取值（或未知）。备注按当前
// subscription_id 保存，换卡不串号。
func (s *Store) SetRemark(number string) (Identity, error) {
	number = strings.TrimSpace(number)
	if number != "" {
		norm, ok := NormalizeRemarkNumber(number)
		if !ok {
			return Identity{}, errors.New("备注号码格式无效：请输入 6–15 位数字，可带 + 前缀")
		}
		number = norm
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return Identity{}, ErrNoIdentity
	}
	if number == "" {
		delete(s.remarks, s.current.SubscriptionID)
	} else {
		s.remarks[s.current.SubscriptionID] = number
	}
	s.applyRemarkLocked(s.current)
	s.current.UpdatedAt = time.Now()
	return *s.current, nil
}

// applyRemarkLocked 按“备注 > 自动读取 > 未知”解析有效号码与来源。
// 调用方须持有 s.mu。
func (s *Store) applyRemarkLocked(id *Identity) {
	if remark, ok := s.remarks[id.SubscriptionID]; ok && remark != "" {
		id.Number = remark
		id.NumberSource = NumberSourceRemark
		return
	}
	if id.autoNumber != "" {
		id.Number = id.autoNumber
		id.NumberSource = NumberSourceAuto
		return
	}
	id.Number = ""
	id.NumberSource = NumberSourceUnknown
}
