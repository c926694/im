package mysql

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

func copyPointBalances(ctx context.Context, conn *sqlx.Conn) error {
	const batchSize = 500
	var afterID int64
	for {
		var accounts []struct {
			UserID  int64 `db:"user_id"`
			Balance int64 `db:"balance"`
		}
		if err := conn.SelectContext(ctx, &accounts, `SELECT user_id, balance FROM point_accounts
			WHERE user_id > ? ORDER BY user_id LIMIT ?`, afterID, batchSize); err != nil {
			return err
		}
		if len(accounts) == 0 {
			break
		}
		// 按已查询的用户 ID 批量更新，余额为零的账户也会从 NULL 转为零。
		var statement strings.Builder
		statement.WriteString("UPDATE users SET point_balance = CASE id")
		args := make([]any, 0, len(accounts)*3)
		for _, account := range accounts {
			statement.WriteString(" WHEN ? THEN ?")
			args = append(args, account.UserID, account.Balance)
		}
		statement.WriteString(" END WHERE id IN (")
		statement.WriteString(strings.Repeat("?,", len(accounts)-1))
		statement.WriteString("?)")
		for _, account := range accounts {
			args = append(args, account.UserID)
		}
		result, err := conn.ExecContext(ctx, statement.String(), args...)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated != int64(len(accounts)) {
			return fmt.Errorf("updated %d of %d users in point balance batch", updated, len(accounts))
		}
		afterID = accounts[len(accounts)-1].UserID
	}
	var missing int64
	if err := conn.GetContext(ctx, &missing, `SELECT COUNT(*) FROM users WHERE point_balance IS NULL`); err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("%d users are missing a point balance", missing)
	}
	return nil
}
