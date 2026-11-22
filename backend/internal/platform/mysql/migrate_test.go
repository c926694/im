package mysql

import (
	"context"
	"os"
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
		if err := db.GetContext(ctx, &balance, `SELECT balance FROM point_accounts WHERE user_id = (SELECT id FROM users WHERE username = ?)`, "migration_legacy"); err != nil {
			t.Fatalf("read migrated legacy account balance: %v", err)
		}
		if balance != 1000 {
			t.Fatalf("expected legacy account to receive 1000 points, got %d", balance)
		}
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
