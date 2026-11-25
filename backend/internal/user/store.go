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

type Profile struct {
	ID        int64  `db:"id" json:"id,string"`
	Username  string `db:"username" json:"username"`
	Nickname  string `db:"nickname" json:"nickname"`
	AvatarURL string `db:"avatar_url" json:"avatar_url"`
}

const selectProfile = `SELECT id, username, nickname, avatar_url FROM users`

func (s *Store) FindProfileByUsername(ctx context.Context, username string) (Profile, error) {
	var profile Profile
	err := s.db.GetContext(ctx, &profile, selectProfile+` WHERE username = ?`, username)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrUserNotFound
	}
	return profile, err
}

func (s *Store) ProfilesByIDs(ctx context.Context, ids []int64) (map[int64]Profile, error) {
	byID := make(map[int64]Profile, len(ids))
	if len(ids) == 0 {
		return byID, nil
	}
	query, args, err := sqlx.In(selectProfile+` WHERE id IN (?)`, ids)
	if err != nil {
		return nil, err
	}
	var profiles []Profile
	if err := s.db.SelectContext(ctx, &profiles, query, args...); err != nil {
		return nil, err
	}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	return byID, nil
}

func (s *Store) SetAvatar(ctx context.Context, id int64, avatarURL string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET avatar_url = ? WHERE id = ?`, avatarURL, id)
	return err
}

func (s *Store) Register(ctx context.Context, username, password, nickname string) (int64, error) {
	// Argon2id 接收完整密码，旧账号仍可使用已有的 bcrypt 哈希登录。
	hash, err := hashPassword(password)
	if err != nil {
		return 0, err
	}
	// 让唯一索引判定用户名冲突，避免并发注册时先查再写。
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO users (username, password_hash, nickname, point_balance)
		VALUES (?, ?, ?, ?)`, username, hash, nickname, InitialPointBalance)
	var dbErr *mysqldriver.MySQLError
	if errors.As(err, &dbErr) && dbErr.Number == 1062 {
		return 0, ErrUsernameTaken
	}
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	// 初始余额和首笔发放流水由注册事务一起提交。
	if err := insertPointEntry(ctx, tx, id, PointGrant, id, InitialPointBalance, InitialPointBalance); err != nil {
		return 0, err
	}
	return id, tx.Commit()
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
