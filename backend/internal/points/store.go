package points

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

const InitialBalance = 1000

var ErrInsufficientBalance = errors.New("insufficient point balance")

type Kind string

const (
	Grant  Kind = "grant"
	Send   Kind = "send"
	Claim  Kind = "claim"
	Refund Kind = "refund"
)

type Entry struct {
	ID           int64     `db:"id" json:"id,string"`
	Kind         Kind      `db:"kind" json:"kind"`
	RefID        int64     `db:"ref_id" json:"ref_id,string"`
	Delta        int64     `db:"delta" json:"delta,string"`
	BalanceAfter int64     `db:"balance_after" json:"balance_after,string"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

type Store struct{ db *sqlx.DB }

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

// OpenAccount 使用注册事务初始化账户并记录发放流水。
func OpenAccount(ctx context.Context, tx *sqlx.Tx, userID int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO point_accounts (user_id, balance) VALUES (?, ?)`, userID, InitialBalance); err != nil {
		return err
	}
	return insertEntry(ctx, tx, userID, Grant, userID, InitialBalance, InitialBalance)
}

// Apply 使用同一条账户记录协调并发增减，流水记录本次更新后的余额。
func Apply(ctx context.Context, tx *sqlx.Tx, userID int64, kind Kind, refID, delta int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE point_accounts SET balance = balance + ? WHERE user_id = ? AND balance >= ?`, delta, userID, max(0, -delta))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		if delta < 0 {
			return ErrInsufficientBalance
		}
		return errors.New("point account missing")
	}
	var balance int64
	if err := tx.GetContext(ctx, &balance, `SELECT balance FROM point_accounts WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return insertEntry(ctx, tx, userID, kind, refID, delta, balance)
}

func insertEntry(ctx context.Context, tx *sqlx.Tx, userID int64, kind Kind, refID, delta, balance int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO point_ledger (user_id, kind, ref_id, delta, balance_after) VALUES (?, ?, ?, ?, ?)`,
		userID, kind, refID, delta, balance)
	return err
}

func (s *Store) Balance(ctx context.Context, userID int64) (int64, error) {
	var balance int64
	err := s.db.GetContext(ctx, &balance, `SELECT balance FROM point_accounts WHERE user_id = ?`, userID)
	return balance, err
}

func (s *Store) Ledger(ctx context.Context, userID, beforeID int64, limit int) ([]Entry, error) {
	query := `SELECT id, kind, ref_id, delta, balance_after, created_at FROM point_ledger WHERE user_id = ?`
	args := []any{userID}
	if beforeID != 0 {
		query += ` AND id < ?`
		args = append(args, beforeID)
	}
	entries := make([]Entry, 0, limit)
	err := s.db.SelectContext(ctx, &entries, query+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	return entries, err
}
