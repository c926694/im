package friendship

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

type Status string

const (
	Pending  Status = "pending"
	Accepted Status = "accepted"
	Rejected Status = "rejected"
	Removed  Status = "removed"
)

type Store struct {
	db *sqlx.DB
}

func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

type Relationship struct {
	PeerID      int64     `db:"peer_id"`
	RequestID   string    `db:"request_id"`
	RequestedBy int64     `db:"requested_by"`
	Status      Status    `db:"status"`
	RequestedAt time.Time `db:"requested_at"`
}

const selectRelationship = `
	SELECT IF(user_low_id = ?, user_high_id, user_low_id) AS peer_id,
	       request_id, requested_by, status, requested_at
	FROM friendships`

func (s *Store) insertRequestIfAbsent(ctx context.Context, tx *sqlx.Tx, low, high int64, entry Relationship) error {
	// 如果用户对已存在，保留该行并与其他申请或状态更新互斥到事务结束。
	_, err := tx.ExecContext(ctx, `
		INSERT INTO friendships (user_low_id, user_high_id, request_id, requested_by, status, requested_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE user_low_id = friendships.user_low_id`,
		low, high, entry.RequestID, entry.RequestedBy, entry.Status, entry.RequestedAt, entry.RequestedAt)
	return err
}

func (s *Store) findRelationship(ctx context.Context, tx *sqlx.Tx, userID, low, high int64) (Relationship, error) {
	var relationship Relationship
	err := tx.GetContext(ctx, &relationship, selectRelationship+` WHERE user_low_id = ? AND user_high_id = ?`, userID, low, high)
	return relationship, err
}

func (s *Store) replaceRequest(ctx context.Context, tx *sqlx.Tx, low, high int64, entry Relationship) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE friendships SET request_id = ?, requested_by = ?, status = ?, requested_at = ?, updated_at = ?
		WHERE user_low_id = ? AND user_high_id = ?`,
		entry.RequestID, entry.RequestedBy, entry.Status, entry.RequestedAt, entry.RequestedAt, low, high)
	return err
}

func (s *Store) resolvePending(ctx context.Context, recipientID int64, requestID string, status Status) (int64, error) {
	// 只有当前申请的接收人能处理待办；重新申请后，旧 request_id 无法匹配。
	result, err := s.db.ExecContext(ctx, `
		UPDATE friendships SET status = ?, updated_at = CURRENT_TIMESTAMP(6)
		WHERE request_id = ? AND status = 'pending' AND requested_by <> ?
		  AND (user_low_id = ? OR user_high_id = ?)`, status, requestID, recipientID, recipientID, recipientID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) removeAccepted(ctx context.Context, userID, peerID int64) error {
	// 解除好友只改变关系状态，重复执行不会影响新的待处理申请。
	_, err := s.db.ExecContext(ctx, `
		UPDATE friendships SET status = 'removed', updated_at = CURRENT_TIMESTAMP(6)
		WHERE user_low_id = ? AND user_high_id = ? AND status = 'accepted'`, min(userID, peerID), max(userID, peerID))
	return err
}

func (s *Store) list(ctx context.Context, userID int64, status Status) ([]Relationship, error) {
	relationships := []Relationship{}
	err := s.db.SelectContext(ctx, &relationships, selectRelationship+`
		WHERE status = ? AND (user_low_id = ? OR user_high_id = ?) ORDER BY updated_at DESC, request_id DESC`,
		userID, status, userID, userID)
	return relationships, err
}

func AreFriends(ctx context.Context, queryer sqlx.QueryerContext, userID, peerID int64) (bool, error) {
	var accepted bool
	err := sqlx.GetContext(ctx, queryer, &accepted, `SELECT EXISTS (
		SELECT 1 FROM friendships WHERE user_low_id = ? AND user_high_id = ? AND status = 'accepted'
	)`, min(userID, peerID), max(userID, peerID))
	return accepted, err
}
