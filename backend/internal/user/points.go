package user

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

const InitialPointBalance = 1000

var ErrInsufficientBalance = errors.New("insufficient point balance")

type PointKind string

const (
	PointGrant  PointKind = "grant"
	PointSend   PointKind = "send"
	PointClaim  PointKind = "claim"
	PointRefund PointKind = "refund"
)

type PointEntry struct {
	ID           int64     `db:"id" json:"id,string"`
	Kind         PointKind `db:"kind" json:"kind"`
	RefID        int64     `db:"ref_id" json:"ref_id,string"`
	Delta        int64     `db:"delta" json:"delta,string"`
	BalanceAfter int64     `db:"balance_after" json:"balance_after,string"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

// ApplyPointChange 由调用方传入事务，余额更新和流水写入一起提交。
func ApplyPointChange(ctx context.Context, tx *sqlx.Tx, userID int64, kind PointKind, refID, delta int64) error {
	// 在更新语句中检查可用余额，并发扣减不会把余额扣成负数。
	result, err := tx.ExecContext(ctx, `UPDATE users SET point_balance = point_balance + ?
		WHERE id = ? AND point_balance >= ?`, delta, userID, max(0, -delta))
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
		return ErrUserNotFound
	}
	var balance int64
	if err := tx.GetContext(ctx, &balance, `SELECT point_balance FROM users WHERE id = ?`, userID); err != nil {
		return err
	}
	return insertPointEntry(ctx, tx, userID, kind, refID, delta, balance)
}

func insertPointEntry(ctx context.Context, tx *sqlx.Tx, userID int64, kind PointKind, refID, delta, balance int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO point_ledger (user_id, kind, ref_id, delta, balance_after) VALUES (?, ?, ?, ?, ?)`,
		userID, kind, refID, delta, balance)
	return err
}

func (s *Store) PointBalance(ctx context.Context, userID int64) (int64, error) {
	var balance int64
	err := s.db.GetContext(ctx, &balance, `SELECT point_balance FROM users WHERE id = ?`, userID)
	return balance, err
}

func (s *Store) PointLedger(ctx context.Context, userID, beforeID int64, limit int) ([]PointEntry, error) {
	query := `SELECT id, kind, ref_id, delta, balance_after, created_at FROM point_ledger WHERE user_id = ?`
	args := []any{userID}
	if beforeID != 0 {
		query += ` AND id < ?`
		args = append(args, beforeID)
	}
	entries := make([]PointEntry, 0, limit)
	err := s.db.SelectContext(ctx, &entries, query+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	return entries, err
}
