package user

import (
	"context"
	"database/sql"
	"errors"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

var (
	ErrUsernameTaken      = errors.New("username already exists")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserNotFound       = errors.New("user not found")
)

type Store struct {
	db *sqlx.DB
}

func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

func (s *Store) FindIDByUsername(ctx context.Context, username string) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id, `SELECT id FROM users WHERE username = ?`, username)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUserNotFound
	}
	return id, err
}

func (s *Store) NicknamesByIDs(ctx context.Context, ids []int64) (map[int64]string, error) {
	nicknames := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return nicknames, nil
	}
	query, args, err := sqlx.In(`SELECT id, nickname FROM users WHERE id IN (?)`, ids)
	if err != nil {
		return nil, err
	}
	var profiles []struct {
		ID       int64  `db:"id"`
		Nickname string `db:"nickname"`
	}
	if err := s.db.SelectContext(ctx, &profiles, query, args...); err != nil {
		return nil, err
	}
	for _, profile := range profiles {
		nicknames[profile.ID] = profile.Nickname
	}
	return nicknames, nil
}

func (s *Store) Register(ctx context.Context, username, password, nickname string) (int64, error) {
	// Argon2id 接收完整密码，旧账号仍可使用已有的 bcrypt 哈希登录。
	hash, err := hashPassword(password)
	if err != nil {
		return 0, err
	}
	// 让唯一索引判定用户名冲突，避免并发注册时先查再写。
	result, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, nickname) VALUES (?, ?, ?)`, username, hash, nickname)
	var dbErr *mysqldriver.MySQLError
	if errors.As(err, &dbErr) && dbErr.Number == 1062 {
		return 0, ErrUsernameTaken
	}
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) Login(ctx context.Context, username, password string) (int64, error) {
	var account struct {
		ID           int64  `db:"id"`
		PasswordHash string `db:"password_hash"`
	}
	err := s.db.GetContext(ctx, &account, `SELECT id, password_hash FROM users WHERE username = ?`, username)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalidCredentials
	}
	if err != nil {
		return 0, err
	}
	if !matchesPassword(account.PasswordHash, password) {
		return 0, ErrInvalidCredentials
	}
	return account.ID, nil
}
