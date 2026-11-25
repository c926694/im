package mysql

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

func TestMigrateAppliesEmbeddedSQLOnce(t *testing.T) {
	db := openMigrationTestDatabase(t, "IM_MIGRATION_TEST_DSN", "im_migration_test")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	assertMigrationHistory(t, ctx, db)

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("run migrations a second time: %v", err)
	}
	assertMigrationHistory(t, ctx, db)
}

func TestMigrateContinuesLegacySchema(t *testing.T) {
	db := openMigrationTestDatabase(t, "IM_MIGRATION_UPGRADE_TEST_DSN", "im_migration_upgrade_test")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var baseTables int
	if err := db.GetContext(ctx, &baseTables, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
		AND table_name IN ('users', 'conversations', 'conversation_members', 'messages')`); err != nil {
		t.Fatal(err)
	}
	seededLegacyUser := baseTables == 0
	if baseTables == 0 {
		files, err := migrationFiles()
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.version > "000003" {
				break
			}
			if _, err := db.ExecContext(ctx, file.content); err != nil {
				t.Fatalf("prepare legacy schema with %s: %v", file.name, err)
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, nickname) VALUES (?, ?, ?)`, "migration_legacy", "test-hash", "迁移测试账号"); err != nil {
			t.Fatalf("seed legacy user: %v", err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("continue legacy schema migrations: %v", err)
	}
	assertMigrationHistory(t, ctx, db)
	if seededLegacyUser {
		var balance int64
		if err := db.GetContext(ctx, &balance, `SELECT point_balance FROM users WHERE username = ?`, "migration_legacy"); err != nil {
			t.Fatalf("read migrated legacy account balance: %v", err)
		}
		if balance != 1000 {
			t.Fatalf("expected legacy account to receive 1000 points, got %d", balance)
		}
	}
}

func TestMigrateSchedulesOpenPackets(t *testing.T) {
	db := openMigrationTestDatabase(t, "IM_EXPIRY_UPGRADE_TEST_DSN", "im_expiry_upgrade_test")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	applyMigrationsThrough(t, ctx, db, "000010")
	_, err := db.ExecContext(ctx, `INSERT INTO red_packets (sender_id, points_per_share, share_count, remaining, status, expires_at)
		VALUES (1, 10, 1, 1, 'open', UTC_TIMESTAMP(6) - INTERVAL 5 SECOND),
		(1, 10, 1, 1, 'open', UTC_TIMESTAMP(6) + INTERVAL 12 HOUR),
		(1, 10, 1, 0, 'claimed', UTC_TIMESTAMP(6))`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var ids []int64
	if err := db.SelectContext(ctx, &ids, `SELECT red_packet_id FROM outbox_events WHERE kind = 'packet_expiry' AND deliver_at IS NOT NULL ORDER BY red_packet_id`); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("expiry tasks: %v", ids)
	}
}

func TestMigrateMovesPointBalancesIntoUsers(t *testing.T) {
	db := openMigrationTestDatabase(t, "IM_POINTS_UPGRADE_TEST_DSN", "im_points_upgrade_test")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	applyMigrationsThrough(t, ctx, db, "000011")

	// 使用迁移前的表结构构造一次已完成的红包交易，覆盖非默认余额和零余额。
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, nickname)
		VALUES (11, 'points_sender', 'test-hash', '发红包用户'), (42, 'points_receiver', 'test-hash', '领红包用户');
		INSERT INTO point_accounts (user_id, balance) VALUES (11, 0), (42, 2000);
		INSERT INTO red_packets (id, sender_id, recipient_id, points_per_share, share_count, remaining, status, expires_at)
		VALUES (15, 11, 42, 1000, 1, 0, 'claimed', UTC_TIMESTAMP(6));
		INSERT INTO red_packet_claims (red_packet_id, user_id, points) VALUES (15, 42, 1000);
		INSERT INTO point_ledger (user_id, kind, ref_id, delta, balance_after) VALUES
			(11, 'grant', 11, 1000, 1000), (42, 'grant', 42, 1000, 1000),
			(11, 'send', 15, -1000, 0), (42, 'claim', 15, 1000, 2000)`); err != nil {
		t.Fatal(err)
	}
	type ledgerRow struct {
		ID           int64     `db:"id"`
		UserID       int64     `db:"user_id"`
		Kind         string    `db:"kind"`
		RefID        int64     `db:"ref_id"`
		Delta        int64     `db:"delta"`
		BalanceAfter int64     `db:"balance_after"`
		CreatedAt    time.Time `db:"created_at"`
	}
	const ledgerSQL = `SELECT id, user_id, kind, ref_id, delta, balance_after, created_at FROM point_ledger ORDER BY id`
	var before, after []ledgerRow
	if err := db.SelectContext(ctx, &before, ledgerSQL); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	assertMigrationHistory(t, ctx, db)
	var balances []int64
	if err := db.SelectContext(ctx, &balances, `SELECT point_balance FROM users ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(balances, []int64{0, 2000}) {
		t.Fatalf("迁移后的积分余额不一致：%v", balances)
	}
	if err := db.SelectContext(ctx, &after, ledgerSQL); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatalf("迁移后的流水发生变化：迁移前 %v，迁移后 %v", before, after)
	}
	var legacyExists bool
	if err := db.GetContext(ctx, &legacyExists, `SELECT EXISTS(SELECT 1 FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = 'point_accounts')`); err != nil {
		t.Fatal(err)
	}
	if legacyExists {
		t.Fatal("旧积分账户表仍然存在")
	}
}

func applyMigrationsThrough(t *testing.T, ctx context.Context, db *sqlx.DB, version string) {
	t.Helper()
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, createMigrationTable); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.version > version {
			break
		}
		if err := applyMigration(ctx, conn, file); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateStoresAvatarURLs(t *testing.T) {
	db := openMigrationTestDatabase(t, "IM_AVATAR_UPGRADE_TEST_DSN", "im_avatar_upgrade_test")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	applyMigrationsThrough(t, ctx, db, "000013")

	const filename = "ba8c0bef-5de6-4ae8-94d0-711ea808ea31.webp"
	if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, nickname, avatar_name)
		VALUES ('avatar_default', 'test-hash', '默认头像', ''), ('avatar_uploaded', 'test-hash', '上传头像', ?)`, filename); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	assertMigrationHistory(t, ctx, db)
	var urls []string
	if err := db.SelectContext(ctx, &urls, `SELECT avatar_url FROM users ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(urls, []string{"/avatars/default.webp", "/avatars/" + filename}) {
		t.Fatalf("迁移后的头像 URL 不一致：%v", urls)
	}
}

func openMigrationTestDatabase(t *testing.T, envName, expectedDBName string) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv(envName)
	if dsn == "" {
		t.Skipf("set %s to a dedicated MySQL migration test database", envName)
	}
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.DBName != expectedDBName {
		t.Fatalf("%s requires database %s, got %q", envName, expectedDBName, config.DBName)
	}

	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func assertMigrationHistory(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	var applied, dirty int
	if err := db.QueryRowxContext(ctx, `SELECT COUNT(*), COALESCE(SUM(dirty), 0) FROM schema_migrations`).Scan(&applied, &dirty); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Fatalf("expected %d applied migrations, got %d", len(files), applied)
	}
	if dirty != 0 {
		t.Fatalf("expected no incomplete migrations, got %d", dirty)
	}
}
