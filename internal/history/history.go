// Package history 是 DJ 4G Hub 的本地通信记录存储（交付点 2，评审 §5 阶段 B）。
//
// 设计要点（评审 docs/2026-11-08-phone-product-roadmap-review.md §3/§5B/§6）：
//
//   - 按卡身份保存：每条记录保存事件当时的订阅快照（subscription.Snapshot，
//     见 internal/subscription），绝不用“当前卡”反填历史；读不到归属的
//     记录记 subscription_id "unknown"，即“归属未知”。
//   - 增量写入：逐条落盘，不启动加载全部记录、不重写全表；读取一律数据库
//     分页（limit/offset），不提供全量返回接口。上游
//     612c2f6:cmd/dj4ghub-macos/history_sqlite.go 的做法（启动加载全部记录、
//     保存时重编码内存历史）刻意没有照搬；只复用了它的驱动选型
//     （glebarez/sqlite，经 modernc，无 cgo）、单连接、WAL + busy_timeout
//     与 0600 文件权限。
//   - 版本迁移：显式 SQL 迁移记录在 schema_migrations 表，带版本号；
//     当前版本见 CurrentSchemaVersion。
//   - 时间一律存 Unix 毫秒整数（INTEGER），不用 RFC3339Nano 文本：
//     RFC3339Nano 的变长小数位会让字典序 ≠ 时间序，排序不可靠。
//   - 驱动：纯 Go（glebarez/sqlite + modernc.org/sqlite），无 cgo；
//     CGO_ENABLED=0 与 GOARCH=arm64 构建均已验证（见包外交付汇报）。
//
// 回退说明：
//   - 删除数据库文件（默认位置见 DefaultPath，含 -wal/-shm 附属文件）
//     即回到无历史状态；服务重启后重建空库。v1 只有建表迁移，无破坏性
//     变更，不提供向下迁移。
//   - 正在写入的数据库绝不直接放同步目录；备份/恢复是后续交付点，
//     本包只保证单文件 + WAL 的一致性。
//   - 磁盘满/只读时 Open 失败，调用方必须保留模块数据、不清理，
//     并通过状态接口显示故障（见 cmd/dj4ghub-macos/history_handlers.go）。
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// CurrentSchemaVersion 是当前数据库结构版本。每次结构变更加一个迁移，
// 递增该值；旧库按 schema_migrations 表逐个应用缺失的迁移。
const CurrentSchemaVersion = 3

// DefaultDBFileName 是数据库文件名，与上游历史实现保持同一文件名，
// 便于运维定位；本包不读取上游的 communication_record 旧表。
const DefaultDBFileName = "communication-history.sqlite"

// Store 是 SQLite 通信记录库。底层 *gorm.DB 只用作 glebarez/sqlite
// 驱动的宿主；所有读写都用显式 SQL，不用 AutoMigrate，迁移可审计。
type Store struct {
	db     *gorm.DB
	sqlDB  *sql.DB
	path   string
	mu     sync.Mutex
	closed bool
}

// DefaultPath 返回数据库文件位置：与现有配置同目录
// （os.UserConfigDir()/DJ 4G Hub/，即 ~/Library/Application Support/DJ 4G Hub/）。
// 路径由调用方统一控制，不写死绝对路径。
func DefaultPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate history database directory: %w", err)
	}
	return filepath.Join(configDir, "DJ 4G Hub", DefaultDBFileName), nil
}

// Open 打开（不存在则创建）数据库文件并应用迁移、标记中断通话。
// path 为 ":memory:" 时打开不落盘的内存库（演示模式与测试用）。
// 打开失败时返回包装过的错误，调用方不得继续假装记录已持久化。
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create history database directory: %w", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create history database file: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("create history database file: %w", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("restrict history database file: %w", err)
		}
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open history database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("open history database: %w", err)
	}
	// 单连接：SQLite 连接级 pragma（WAL、busy_timeout）确定可用，
	// 写入串行化，避免多连接下的 database is locked。
	sqlDB.SetMaxOpenConns(1)
	s := &Store{db: db, sqlDB: sqlDB, path: path}
	// 连接级 pragma（与上游已验证的取值为同一组）。
	for _, stmt := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("configure history database (%s): %w", stmt, err)
		}
	}
	if err := s.migrate(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	// 重启恢复：上次未正常结束的通话没有结束时间与时长，标记为中断/
	// 结果未知，绝不凭空补结束时间或时长（评审 §5B）。
	if err := s.markInterruptedCalls(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return s, nil
}

// Path 返回数据库文件路径（":memory:" 表示内存库）。
func (s *Store) Path() string { return s.path }

// SchemaVersion 返回已应用的最高迁移版本。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	if err := s.db.WithContext(ctx).Raw(
		"SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil {
		return 0, fmt.Errorf("read history schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// Close 关闭数据库连接。重复关闭返回 nil。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.sqlDB == nil {
		return nil
	}
	return s.sqlDB.Close()
}

// migration 是一次带版本号的结构变更。
type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		name:    "v1-initial-tables",
		sql: `
CREATE TABLE IF NOT EXISTS sms_messages (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  direction         TEXT NOT NULL CHECK (direction IN ('in', 'out')),
  peer_raw          TEXT NOT NULL DEFAULT '',
  peer_normalized   TEXT NOT NULL DEFAULT '',
  body              TEXT NOT NULL DEFAULT '',
  subscription_id   TEXT NOT NULL DEFAULT 'unknown',
  iccid             TEXT NOT NULL DEFAULT '',
  imei              TEXT NOT NULL DEFAULT '',
  eid               TEXT NOT NULL DEFAULT '',
  own_number        TEXT NOT NULL DEFAULT '',
  number_source     TEXT NOT NULL DEFAULT 'unknown',
  snapshot_at       INTEGER,
  concat_ref        INTEGER,
  concat_ref_bits   INTEGER,
  concat_total      INTEGER,
  concat_segments   INTEGER,
  concat_complete   INTEGER NOT NULL DEFAULT 1,
  dedup_key         TEXT NOT NULL,
  outbox_id         INTEGER,
  is_read           INTEGER NOT NULL DEFAULT 0,
  received_at       INTEGER NOT NULL,
  created_at        INTEGER NOT NULL,
  UNIQUE (dedup_key)
);
CREATE INDEX IF NOT EXISTS idx_sms_subscription_received
  ON sms_messages (subscription_id, received_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_sms_peer_received
  ON sms_messages (peer_normalized, received_at DESC, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sms_outbox_id
  ON sms_messages (outbox_id) WHERE outbox_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS calls (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  call_id           TEXT NOT NULL,
  direction         TEXT NOT NULL CHECK (direction IN ('inbound', 'outbound')),
  number_raw        TEXT NOT NULL DEFAULT '',
  number_normalized TEXT NOT NULL DEFAULT '',
  subscription_id   TEXT NOT NULL DEFAULT 'unknown',
  iccid             TEXT NOT NULL DEFAULT '',
  imei              TEXT NOT NULL DEFAULT '',
  eid               TEXT NOT NULL DEFAULT '',
  own_number        TEXT NOT NULL DEFAULT '',
  number_source     TEXT NOT NULL DEFAULT 'unknown',
  snapshot_at       INTEGER,
  started_at        INTEGER NOT NULL,
  connected_at      INTEGER,
  ended_at          INTEGER,
  duration_seconds  INTEGER,
  end_reason        TEXT NOT NULL DEFAULT '',
  end_state         TEXT NOT NULL DEFAULT '',
  missed            INTEGER NOT NULL DEFAULT 0,
  status            TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'ended', 'interrupted')),
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  UNIQUE (call_id)
);
CREATE INDEX IF NOT EXISTS idx_calls_subscription_started
  ON calls (subscription_id, started_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS outbox (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  peer_raw          TEXT NOT NULL DEFAULT '',
  peer_normalized   TEXT NOT NULL DEFAULT '',
  body              TEXT NOT NULL DEFAULT '',
  segments          INTEGER NOT NULL DEFAULT 0,
  message_refs      TEXT NOT NULL DEFAULT '',
  subscription_id   TEXT NOT NULL DEFAULT 'unknown',
  iccid             TEXT NOT NULL DEFAULT '',
  imei              TEXT NOT NULL DEFAULT '',
  eid               TEXT NOT NULL DEFAULT '',
  own_number        TEXT NOT NULL DEFAULT '',
  number_source     TEXT NOT NULL DEFAULT 'unknown',
  snapshot_at       INTEGER,
  status            TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'submitted', 'delivered', 'failed', 'uncertain')),
  error_text        TEXT NOT NULL DEFAULT '',
  sms_message_id    INTEGER,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_outbox_peer_created
  ON outbox (peer_normalized, created_at DESC, id DESC);
`,
	},
	{
		// 交付点 4：短信草稿表（sms_drafts）。同一号码永远只有一条
		// 最新草稿；草稿只存本地，不上传、不自动发送。
		version: 2,
		name:    "v2-sms-drafts",
		sql: `
CREATE TABLE IF NOT EXISTS sms_drafts (
  peer_normalized TEXT PRIMARY KEY,
  peer_raw        TEXT NOT NULL DEFAULT '',
  body            TEXT NOT NULL DEFAULT '',
  updated_at      INTEGER NOT NULL
);
`,
	},
	{
		// 交付点 3：calls.seen 列（未接已读角标）+ contacts 表（本地轻量联系人）。
		version: 3,
		name:    "v3-calls-seen-and-contacts",
		sql: `
ALTER TABLE calls ADD COLUMN seen INTEGER NOT NULL DEFAULT 1;
CREATE TABLE IF NOT EXISTS contacts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT NOT NULL DEFAULT '',
  numbers_json    TEXT NOT NULL DEFAULT '[]',
  normalized_json TEXT NOT NULL DEFAULT '[]',
  favorite        INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_contacts_name
  ON contacts (name, id);
`,
	},
}

// migrate 在事务里按版本号顺序应用缺失的迁移并记录到
// schema_migrations；已应用的迁移不再重复执行。
func (s *Store) migrate(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at INTEGER NOT NULL
)`).Error; err != nil {
		return fmt.Errorf("create history migration table: %w", err)
	}
	for _, m := range migrations {
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var count int64
			if err := tx.Raw(
				"SELECT COUNT(*) FROM schema_migrations WHERE version = ?",
				m.version).Scan(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			if err := tx.Exec(m.sql).Error; err != nil {
				return err
			}
			return tx.Exec(
				"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
				m.version, m.name, unixMillisNow()).Error
		}); err != nil {
			return fmt.Errorf("apply history migration %d (%s): %w", m.version, m.name, err)
		}
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if version != CurrentSchemaVersion {
		return fmt.Errorf("history schema version is %d, want %d", version, CurrentSchemaVersion)
	}
	return nil
}

// markInterruptedCalls 把上次运行遗留的未结束通话标记为中断。
// 只改 status 与空的 end_reason；ended_at 与 duration_seconds 保持
// NULL，不补任何凭空的时间或时长。
func (s *Store) markInterruptedCalls(ctx context.Context) error {
	err := s.db.WithContext(ctx).Exec(`
UPDATE calls
SET status = 'interrupted',
    end_reason = CASE WHEN end_reason = '' THEN 'unknown' ELSE end_reason END,
    updated_at = ?
WHERE ended_at IS NULL AND status = 'active'`, unixMillisNow()).Error
	if err != nil {
		return fmt.Errorf("mark interrupted history calls: %w", err)
	}
	return nil
}

// ErrNotFound 表示要更新的记录不存在。
var ErrNotFound = errors.New("history: record not found")
