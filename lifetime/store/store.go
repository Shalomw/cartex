// Package store 是 ARTEX lifetime profile 的 PostgreSQL 持久化层。
//
// 设计约束：
//   - 只依赖标准库与 github.com/jackc/pgx/v5/stdlib（database/sql 驱动 "pgx"）；
//   - 所有写方法显式接受 context，使用参数化 SQL；JSON 列用 json.Marshal；
//   - 不使用 ORM；
//   - 只操作 lifetime_ 前缀的表，不改动 ARTEX 现有表。
//
// 表与公开 API 速查见同目录 README.md 与 schema.sql。
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 驱动 "pgx"
)

//go:embed schema.sql
var schemaFS embed.FS

// advisoryLockKey 是 schema 应用期间的 PostgreSQL advisory lock key。
// 多个进程/测试同时打开同一个库时，保证建表语句串行执行。
const advisoryLockKey int64 = 7337742001

// Store 封装一个 pgx database/sql 连接池。
type Store struct {
	db *sql.DB
}

// 哨兵错误，便于 service 层区分重试/失效语义。
var (
	// ErrNotFound 表示按主键/自然键查询的目标不存在。
	ErrNotFound = errors.New("lifetime/store: not found")
	// ErrLeaseLost 表示 claim 已丢失：claim_id/session 不匹配、租约已过期或项已完成。
	ErrLeaseLost = errors.New("lifetime/store: lease lost")
	// ErrAreaOwned 表示 area 已被其他未过期 session 持有（R-6.33）。
	ErrAreaOwned = errors.New("lifetime/store: area owned by another session")
)

// Open 打开 PostgreSQL 连接池，Ping 后在单连接上持有 advisory lock 并
// 幂等应用 embedded schema.sql，随后释放锁。
func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("lifetime/store: empty DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: open: %w", err)
	}
	// 连接池上限保持保守；服务层可继续通过 DB() 调整。
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lifetime/store: ping: %w", err)
	}

	schemaSQL, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lifetime/store: read schema: %w", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lifetime/store: acquire schema connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lifetime/store: advisory lock: %w", err)
	}
	unlock := func() {
		// 即使调用方 ctx 已取消也要尝试解锁；连接 Close 也会释放会话级锁。
		unlockCtx := context.WithoutCancel(ctx)
		_, _ = conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
	}
	defer unlock()

	// 无参数 Exec 走 simple protocol，允许一次执行多条语句；
	// PostgreSQL 会把它包在隐式事务里，任一语句失败则整体回滚。
	if _, err := conn.ExecContext(ctx, string(schemaSQL)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lifetime/store: apply schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close 关闭底层连接池。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB 暴露底层 *sql.DB，供测试与服务层只读查询/事务使用。
func (s *Store) DB() *sql.DB { return s.db }

// querier 是 *sql.DB 与 *sql.Tx 的公共子集，便于同一段 SQL 在事务内外复用。
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// withTx 在单个事务中执行 fn；fn 返回错误则回滚。
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lifetime/store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lifetime/store: commit: %w", err)
	}
	return nil
}

// marshalJSON 把任意 payload 编码为 jsonb 参数（pgx 将 []byte 视为原始 JSON）。
func marshalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: marshal json: %w", err)
	}
	return b, nil
}

// nowUTC 是所有平台写入使用的时钟。
func nowUTC() time.Time { return time.Now().UTC() }

// nullTime 把零值 time.Time 映射为 SQL NULL。
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// scanNullTime 把可能为 NULL 的时间列转为零值 time.Time。
func scanNullTime(n sql.NullTime) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return n.Time.UTC()
}

// rowsErr 统一处理 rows.Close/rows.Err 的组合。
func rowsErr(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		return fmt.Errorf("lifetime/store: rows: %w", err)
	}
	return nil
}

// scanJSONRows 逐行读取一个 jsonb 列并交给 fn 反序列化。
func scanJSONRows(ctx context.Context, db *sql.DB, query string, args []any, fn func([]byte) error) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if err := fn(raw); err != nil {
			return err
		}
	}
	return rowsErr(rows)
}

// scopeID 把确定性 ID（coverage unit_id / work package_id）限定到 task。
// 这些 ID 由 family×pair×form 等内容哈希生成，同一份源码在多个 task 下
// 会产生相同 ID；表主键是全局的，因此写入前必须加 task 前缀，避免第二个
// task 的 upsert 命中第一行而把数据留在旧 task。已带前缀的 ID 原样返回。
func scopeID(taskID, id string) string {
	if taskID == "" || id == "" {
		return id
	}
	prefix := taskID + "@"
	if strings.HasPrefix(id, prefix) {
		return id
	}
	return prefix + id
}
