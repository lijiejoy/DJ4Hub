package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Contact 是本地轻量联系人（交付点 3，评审 §5C）：姓名 + 多号码 + 收藏。
// 只做“从通话记录保存姓名、按号码识别来电”这一最小闭环；系统通讯录
// 导入、批量导入不在本交付点做。原始号码保留展示与追溯；Normalized
// 只用于比较与去重，地区不明确时不盲目补国家码（评审 §3）。
// 联系人与通话/短信共用同一 history SQLite 库（迁移 v2），
// 备份与保留口径与通信记录一致。
type Contact struct {
	ID         int64
	Name       string
	Numbers    []string // 原始号码（展示用）
	Normalized []string // 标准化号码（比较与去重用）
	Favorite   bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type contactRow struct {
	ID             int64  `gorm:"column:id"`
	Name           string `gorm:"column:name"`
	NumbersJSON    string `gorm:"column:numbers_json"`
	NormalizedJSON string `gorm:"column:normalized_json"`
	Favorite       int64  `gorm:"column:favorite"`
	CreatedAt      int64  `gorm:"column:created_at"`
	UpdatedAt      int64  `gorm:"column:updated_at"`
}

func (r contactRow) toContact() Contact {
	c := Contact{
		ID: r.ID, Name: r.Name, Favorite: r.Favorite != 0,
		CreatedAt: fromMillisValue(r.CreatedAt), UpdatedAt: fromMillisValue(r.UpdatedAt),
	}
	_ = json.Unmarshal([]byte(r.NumbersJSON), &c.Numbers)
	_ = json.Unmarshal([]byte(r.NormalizedJSON), &c.Normalized)
	if c.Numbers == nil {
		c.Numbers = []string{}
	}
	if c.Normalized == nil {
		c.Normalized = []string{}
	}
	return c
}

func contactNumbersJSON(numbers []string) string {
	if len(numbers) == 0 {
		return "[]"
	}
	b, err := json.Marshal(numbers)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// GetContact 按 id 取一位联系人；不存在返回 ErrNotFound。
func (s *Store) GetContact(ctx context.Context, id int64) (Contact, error) {
	var row contactRow
	err := s.db.WithContext(ctx).Table("contacts").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Contact{}, ErrNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("read contact: %w", err)
	}
	return row.toContact(), nil
}

// ListContacts 按收藏优先、姓名排序返回全部联系人。
// 轻量联系人规模很小，全量返回是刻意的；通话/短信仍走分页。
func (s *Store) ListContacts(ctx context.Context) ([]Contact, error) {
	var rows []contactRow
	if err := s.db.WithContext(ctx).Table("contacts").
		Order("favorite DESC, name ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	out := make([]Contact, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toContact())
	}
	return out, nil
}

// SaveContact 按号码保存联系人：该号码已在某位联系人名下时只改名，
// 不产生第二条同号联系人；否则新建一位联系人。
func (s *Store) SaveContact(ctx context.Context, name, rawNumber string) (Contact, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Contact{}, errors.New("save contact: empty name")
	}
	if len(name) > 100 {
		return Contact{}, errors.New("save contact: name too long")
	}
	norm := NormalizeNumber(rawNumber)
	if norm == "" {
		return Contact{}, errors.New("save contact: empty number")
	}
	contacts, err := s.ListContacts(ctx)
	if err != nil {
		return Contact{}, err
	}
	for _, c := range contacts {
		if !slices.Contains(c.Normalized, norm) {
			continue
		}
		if c.Name == name {
			return c, nil
		}
		now := unixMillisNow()
		if err := s.db.WithContext(ctx).Table("contacts").Where("id = ?", c.ID).
			Updates(map[string]any{"name": name, "updated_at": now}).Error; err != nil {
			return Contact{}, fmt.Errorf("update contact: %w", err)
		}
		c.Name = name
		c.UpdatedAt = fromMillisValue(now)
		return c, nil
	}
	now := unixMillisNow()
	row := contactRow{
		Name: name, NumbersJSON: contactNumbersJSON([]string{strings.TrimSpace(rawNumber)}),
		NormalizedJSON: contactNumbersJSON([]string{norm}),
		CreatedAt:      now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Table("contacts").Create(&row).Error; err != nil {
		return Contact{}, fmt.Errorf("insert contact: %w", err)
	}
	return row.toContact(), nil
}

// AddContactNumber 给一位联系人追加一个号码（多号码）。
// 该号码已属于另一位联系人时拒绝，避免两人共用一号。
func (s *Store) AddContactNumber(ctx context.Context, id int64, rawNumber string) (Contact, error) {
	norm := NormalizeNumber(rawNumber)
	if norm == "" {
		return Contact{}, errors.New("add contact number: empty number")
	}
	contacts, err := s.ListContacts(ctx)
	if err != nil {
		return Contact{}, err
	}
	var target *Contact
	for i := range contacts {
		if contacts[i].ID == id {
			target = &contacts[i]
			continue
		}
		if slices.Contains(contacts[i].Normalized, norm) {
			return Contact{}, fmt.Errorf("add contact number: number already belongs to contact %d", contacts[i].ID)
		}
	}
	if target == nil {
		return Contact{}, ErrNotFound
	}
	if slices.Contains(target.Normalized, norm) {
		return *target, nil
	}
	target.Numbers = append(target.Numbers, strings.TrimSpace(rawNumber))
	target.Normalized = append(target.Normalized, norm)
	now := unixMillisNow()
	if err := s.db.WithContext(ctx).Table("contacts").Where("id = ?", id).
		Updates(map[string]any{
			"numbers_json":    contactNumbersJSON(target.Numbers),
			"normalized_json": contactNumbersJSON(target.Normalized),
			"updated_at":      now,
		}).Error; err != nil {
		return Contact{}, fmt.Errorf("update contact numbers: %w", err)
	}
	target.UpdatedAt = fromMillisValue(now)
	return *target, nil
}

// UpdateContact 改名与收藏开关；name 为空表示只改收藏。
func (s *Store) UpdateContact(ctx context.Context, id int64, name string, favorite bool) (Contact, error) {
	name = strings.TrimSpace(name)
	if len(name) > 100 {
		return Contact{}, errors.New("update contact: name too long")
	}
	current, err := s.GetContact(ctx, id)
	if err != nil {
		return Contact{}, err
	}
	if name == "" {
		name = current.Name
	}
	now := unixMillisNow()
	if err := s.db.WithContext(ctx).Table("contacts").Where("id = ?", id).
		Updates(map[string]any{"name": name, "favorite": boolToInt(favorite), "updated_at": now}).Error; err != nil {
		return Contact{}, fmt.Errorf("update contact: %w", err)
	}
	current.Name = name
	current.Favorite = favorite
	current.UpdatedAt = fromMillisValue(now)
	return current, nil
}

// DeleteContact 删除一位联系人；不存在返回 deleted=false。
func (s *Store) DeleteContact(ctx context.Context, id int64) (bool, error) {
	res := s.db.WithContext(ctx).Table("contacts").Where("id = ?", id).Delete(nil)
	if res.Error != nil {
		return false, fmt.Errorf("delete contact: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// GetCall 按稳定通话 ID 取一条通话记录；不存在返回 ErrNotFound。
func (s *Store) GetCall(ctx context.Context, callID string) (CallRecord, error) {
	var row callRow
	err := s.db.WithContext(ctx).Table("calls").Where("call_id = ?", callID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CallRecord{}, ErrNotFound
	}
	if err != nil {
		return CallRecord{}, fmt.Errorf("read call: %w", err)
	}
	return row.toRecord(), nil
}

// CountMissedUnseen 返回未接总数与其中未读（用户尚未在最近通话里
// 看到）的数量；subscriptionID 为空表示全部卡。
func (s *Store) CountMissedUnseen(ctx context.Context, subscriptionID string) (unseen, total int64, err error) {
	where := "missed = 1"
	var args []any
	if subscriptionID != "" {
		where += " AND subscription_id = ?"
		args = append(args, subscriptionID)
	}
	if err := s.db.WithContext(ctx).Table("calls").Where(where, args...).Count(&total).Error; err != nil {
		return 0, 0, fmt.Errorf("count missed calls: %w", err)
	}
	if err := s.db.WithContext(ctx).Table("calls").Where(where+" AND seen = 0", args...).Count(&unseen).Error; err != nil {
		return 0, 0, fmt.Errorf("count unseen missed calls: %w", err)
	}
	return unseen, total, nil
}

// MarkMissedSeen 把未读未接全部标记为已读（用户已在最近通话里看到），
// 返回本次标记的条数；subscriptionID 为空表示全部卡。
func (s *Store) MarkMissedSeen(ctx context.Context, subscriptionID string) (int64, error) {
	where := "missed = 1 AND seen = 0"
	var args []any
	if subscriptionID != "" {
		where += " AND subscription_id = ?"
		args = append(args, subscriptionID)
	}
	res := s.db.WithContext(ctx).Table("calls").Where(where, args...).
		Updates(map[string]any{"seen": 1, "updated_at": unixMillisNow()})
	if res.Error != nil {
		return 0, fmt.Errorf("mark missed seen: %w", res.Error)
	}
	return res.RowsAffected, nil
}
