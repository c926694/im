package social

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

type Store struct {
	db *sqlx.DB
}

func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
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

func (s *Store) resolvePending(ctx context.Context, recipientID int64, requestID string, status FriendshipStatus) (int64, error) {
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

func (s *Store) list(ctx context.Context, userID int64, status FriendshipStatus) ([]Relationship, error) {
	relationships := []Relationship{}
	err := s.db.SelectContext(ctx, &relationships, selectRelationship+`
		WHERE status = ? AND (user_low_id = ? OR user_high_id = ?) ORDER BY updated_at DESC, request_id DESC`,
		userID, status, userID, userID)
	return relationships, err
}

func (s *Store) AreFriends(ctx context.Context, userID, peerID int64) (bool, error) {
	return areFriends(ctx, s.db, userID, peerID)
}

// AreFriendsTx 复用发送消息的事务，好友资格按普通查询读取。
func (s *Store) AreFriendsTx(ctx context.Context, tx *sqlx.Tx, userID, peerID int64) (bool, error) {
	return areFriends(ctx, tx, userID, peerID)
}

func areFriends(ctx context.Context, queryer sqlx.QueryerContext, userID, peerID int64) (bool, error) {
	var accepted bool
	err := sqlx.GetContext(ctx, queryer, &accepted, `SELECT EXISTS (
		SELECT 1 FROM friendships WHERE user_low_id = ? AND user_high_id = ? AND status = 'accepted'
	)`, min(userID, peerID), max(userID, peerID))
	return accepted, err
}

// countFriends 一次检查本次受邀用户的好友关系。
func (s *Store) countFriends(ctx context.Context, tx *sqlx.Tx, actor int64, ids []int64) (int, error) {
	query, args, err := sqlx.In(`SELECT COUNT(*) FROM friendships WHERE status = 'accepted'
		AND ((user_low_id = ? AND user_high_id IN (?)) OR (user_high_id = ? AND user_low_id IN (?)))`, actor, ids, actor, ids)
	if err != nil {
		return 0, err
	}
	var count int
	err = tx.GetContext(ctx, &count, query, args...)
	return count, err
}

const selectPacket = `SELECT id, sender_id, recipient_id, points_per_share, share_count, remaining, status, expires_at FROM red_packets WHERE id = ?`

// beginWrite 的普通查询读取最新已提交记录，领取重试在取得红包行锁后读取原结果。
func (s *Store) beginWrite(ctx context.Context) (*sqlx.Tx, error) {
	return s.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func (s *Store) snapshot(ctx context.Context) (*sqlx.Tx, error) {
	return s.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

func insertPacket(ctx context.Context, tx *sqlx.Tx, packet Packet) (int64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO red_packets (sender_id, recipient_id, points_per_share, share_count, remaining, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, packet.SenderID, packet.RecipientID, packet.PointsPerShare, packet.ShareCount, packet.Remaining, packet.ExpiresAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func getPacket(ctx context.Context, tx *sqlx.Tx, id int64, lock bool) (Packet, error) {
	query := selectPacket
	if lock {
		query += ` FOR UPDATE`
	}
	var packet Packet
	err := tx.GetContext(ctx, &packet, query, id)
	return packet, err
}

func getClaim(ctx context.Context, tx *sqlx.Tx, packetID, userID int64) (Claim, error) {
	var claim Claim
	err := tx.GetContext(ctx, &claim, `SELECT points, created_at FROM red_packet_claims WHERE red_packet_id = ? AND user_id = ?`, packetID, userID)
	return claim, err
}

func insertClaim(ctx context.Context, tx *sqlx.Tx, packetID, userID int64, claim Claim) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO red_packet_claims (red_packet_id, user_id, points, created_at) VALUES (?, ?, ?, ?)`, packetID, userID, claim.Points, claim.CreatedAt)
	return err
}

func updatePacket(ctx context.Context, tx *sqlx.Tx, id int64, remaining int, status RedPacketStatus) error {
	_, err := tx.ExecContext(ctx, `UPDATE red_packets SET remaining = ?, status = ? WHERE id = ?`, remaining, status, id)
	return err
}

func databaseTime(ctx context.Context, tx *sqlx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.GetContext(ctx, &now, `SELECT UTC_TIMESTAMP(6)`)
	return now, err
}

func (s *Store) statuses(ctx context.Context, packetIDs []int64) (map[int64]RedPacketStatus, error) {
	statuses := make(map[int64]RedPacketStatus, len(packetIDs))
	if len(packetIDs) == 0 {
		return statuses, nil
	}
	query, args, err := sqlx.In(`SELECT id, status FROM red_packets WHERE id IN (?)`, packetIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID     int64           `db:"id"`
		Status RedPacketStatus `db:"status"`
	}
	err = s.db.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		statuses[row.ID] = row.Status
	}
	return statuses, nil
}
