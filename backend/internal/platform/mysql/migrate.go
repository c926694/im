package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c926694/im/backend/migrations"
	"github.com/jmoiron/sqlx"
)

const migrationLockName = "im_schema_migrations"

const createMigrationTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version CHAR(6) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '迁移版本号',
    name VARCHAR(255) NOT NULL COMMENT '迁移文件名',
    dirty BOOLEAN NOT NULL COMMENT '是否存在未完成的迁移',
    applied_at DATETIME(6) NULL COMMENT '迁移成功时间（UTC）',
    PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='数据库结构迁移记录'`

type migrationFile struct {
	version string
	name    string
	content string
}

// Migrate 按版本执行尚未记录的嵌入式 SQL 迁移。
func Migrate(ctx context.Context, db *sqlx.DB) (returnErr error) {
	conn, err := db.Connx(ctx)
	if err != nil {
		return fmt.Errorf("reserve MySQL connection for migrations: %w", err)
	}
	locked := false
	defer func() {
		if locked {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released sql.NullInt64
			if err := conn.QueryRowxContext(releaseCtx, `SELECT RELEASE_LOCK(?)`, migrationLockName).Scan(&released); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("release migration lock: %w", err))
			} else if !released.Valid || released.Int64 != 1 {
				returnErr = errors.Join(returnErr, errors.New("MySQL migration lock was not released"))
			}
		}
		if err := conn.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close MySQL migration connection: %w", err))
		}
	}()

	var acquired sql.NullInt64
	if err := conn.QueryRowxContext(ctx, `SELECT GET_LOCK(?, 30)`, migrationLockName).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire MySQL migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return errors.New("timed out waiting for MySQL migration lock")
	}
	locked = true

	// 同一个连接持有 advisory lock，保证多个后端实例不会同时执行迁移。
	if _, err := conn.ExecContext(ctx, createMigrationTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	// 旧数据库没有迁移记录表，需要按现有表和字段记录可识别的迁移版本。
	if err := adoptExistingSchema(ctx, conn); err != nil {
		return err
	}
	// MySQL DDL 会自动提交，dirty 标记用于阻止部分执行后被自动重跑。
	if err := rejectDirtyMigration(ctx, conn); err != nil {
		return err
	}
	files, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		var exists bool
		if err := conn.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)`, file.version); err != nil {
			return fmt.Errorf("check migration %s: %w", file.version, err)
		}
		if exists {
			continue
		}
		if err := applyMigration(ctx, conn, file); err != nil {
			return err
		}
	}
	return nil
}

// migrationFiles 校验版本编号并按文件名顺序读取嵌入式 SQL。
func migrationFiles() ([]migrationFile, error) {
	paths, err := fs.Glob(migrations.Files, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(paths)
	files := make([]migrationFile, 0, len(paths))
	versions := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		version, description, ok := strings.Cut(path, "_")
		if !ok || len(version) != 6 || description == "" {
			return nil, fmt.Errorf("invalid migration filename %q", path)
		}
		if _, err := strconv.ParseUint(version, 10, 32); err != nil {
			return nil, fmt.Errorf("invalid migration version in filename %q: %w", path, err)
		}
		if _, exists := versions[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %q", version)
		}
		versions[version] = struct{}{}
		content, err := fs.ReadFile(migrations.Files, path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", path, err)
		}
		files = append(files, migrationFile{version: version, name: path, content: string(content)})
	}
	return files, nil
}

// applyMigration 先记录未完成状态，再执行 SQL；MySQL DDL 自动提交，中断后需要先人工检查。
func applyMigration(ctx context.Context, conn *sqlx.Conn, file migrationFile) error {
	if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, dirty) VALUES (?, ?, TRUE)`, file.version, file.name); err != nil {
		return fmt.Errorf("record migration %s: %w", file.version, err)
	}
	if _, err := conn.ExecContext(ctx, file.content); err != nil {
		return fmt.Errorf("execute migration %s: %w", file.name, err)
	}
	if file.version == "000012" {
		// 逐批复制余额，全部核对通过后才能继续执行删除旧账户表的迁移。
		if err := copyPointBalances(ctx, conn); err != nil {
			return fmt.Errorf("copy point balances: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE schema_migrations SET dirty = FALSE, applied_at = UTC_TIMESTAMP(6) WHERE version = ?`, file.version); err != nil {
		return fmt.Errorf("complete migration %s: %w", file.version, err)
	}
	return nil
}

// rejectDirtyMigration 阻止重复执行上次中断的 DDL。
func rejectDirtyMigration(ctx context.Context, conn *sqlx.Conn) error {
	var version string
	err := conn.GetContext(ctx, &version, `SELECT version FROM schema_migrations WHERE dirty = TRUE ORDER BY version LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check incomplete migrations: %w", err)
	}
	return fmt.Errorf("migration %s is incomplete; inspect the database before restarting", version)
}

// adoptExistingSchema 将旧数据库中能够识别的结构记入版本表，避免重复建表。
func adoptExistingSchema(ctx context.Context, conn *sqlx.Conn) error {
	var recorded int
	if err := conn.GetContext(ctx, &recorded, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	if recorded > 0 {
		return nil
	}
	var baseTables int
	if err := conn.GetContext(ctx, &baseTables, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
		AND table_name IN ('users', 'conversations', 'conversation_members', 'messages')`); err != nil {
		return fmt.Errorf("inspect existing base tables: %w", err)
	}
	if baseTables == 0 {
		return nil
	}
	if baseTables != 4 {
		return fmt.Errorf("existing database has an incomplete IM schema: found %d of 4 base tables", baseTables)
	}

	versions := []migrationFile{{version: "000001", name: "000001_direct_chat.up.sql"}}
	commentsApplied, err := hasColumnComment(ctx, conn, "users", "username", "唯一用户名，用于登录和查找聊天对象")
	if err != nil {
		return err
	}
	if commentsApplied {
		versions = append(versions, migrationFile{version: "000002", name: "000002_direct_chat_comments.up.sql"})
	}

	friendships, err := hasTable(ctx, conn, "friendships")
	if err != nil {
		return err
	}
	if friendships {
		versions = append(versions, migrationFile{version: "000003", name: "000003_friendships.up.sql"})
	}

	groupMarkers, err := countColumns(ctx, conn, map[string][]string{
		"conversations":        {"kind", "group_name", "closed_at"},
		"conversation_members": {"role", "joined_seq", "joined_at", "left_at"},
	})
	if err != nil {
		return err
	}
	if groupMarkers != 0 && groupMarkers != 7 {
		return fmt.Errorf("existing database has an incomplete group-chat migration: found %d of 7 schema markers", groupMarkers)
	}
	if groupMarkers == 7 {
		versions = append(versions, migrationFile{version: "000004", name: "000004_group_chat.up.sql"})
	}

	packetTables, err := countTables(ctx, conn, []string{"point_accounts", "point_ledger", "red_packets", "red_packet_claims", "outbox_events"})
	if err != nil {
		return err
	}
	packetColumns, err := countColumns(ctx, conn, map[string][]string{"messages": {"kind", "red_packet_id"}})
	if err != nil {
		return err
	}
	packetMarkers := packetTables + packetColumns
	if packetMarkers != 0 && packetMarkers != 7 {
		return fmt.Errorf("existing database has an incomplete red-packet migration: found %d of 7 schema markers", packetMarkers)
	}
	if packetMarkers == 7 {
		versions = append(versions, migrationFile{version: "000005", name: "000005_red_packets.up.sql"})
	}

	for _, file := range versions {
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, dirty, applied_at)
			VALUES (?, ?, FALSE, UTC_TIMESTAMP(6))`, file.version, file.name); err != nil {
			return fmt.Errorf("record existing migration %s: %w", file.version, err)
		}
	}
	return nil
}

func hasTable(ctx context.Context, conn *sqlx.Conn, table string) (bool, error) {
	var exists bool
	if err := conn.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' AND table_name = ?)`, table); err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	return exists, nil
}

func countTables(ctx context.Context, conn *sqlx.Conn, tables []string) (int, error) {
	query, args, err := sqlx.In(`SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' AND table_name IN (?)`, tables)
	if err != nil {
		return 0, fmt.Errorf("build table inspection query: %w", err)
	}
	query = conn.Rebind(query)
	var count int
	if err := conn.GetContext(ctx, &count, query, args...); err != nil {
		return 0, fmt.Errorf("inspect existing tables: %w", err)
	}
	return count, nil
}

func countColumns(ctx context.Context, conn *sqlx.Conn, tables map[string][]string) (int, error) {
	count := 0
	for table, columns := range tables {
		query, args, err := sqlx.In(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? AND column_name IN (?)`, table, columns)
		if err != nil {
			return 0, fmt.Errorf("build column inspection query: %w", err)
		}
		query = conn.Rebind(query)
		var tableCount int
		if err := conn.GetContext(ctx, &tableCount, query, args...); err != nil {
			return 0, fmt.Errorf("inspect columns in %s: %w", table, err)
		}
		count += tableCount
	}
	return count, nil
}

func hasColumnComment(ctx context.Context, conn *sqlx.Conn, table, column, comment string) (bool, error) {
	var exists bool
	if err := conn.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? AND column_comment = ?)`, table, column, comment); err != nil {
		return false, fmt.Errorf("inspect comment for %s.%s: %w", table, column, err)
	}
	return exists, nil
}
