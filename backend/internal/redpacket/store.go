package redpacket

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

type Status string

const (
	Open     Status = "open"
	Claimed  Status = "claimed"
	Refunded Status = "refunded"
	Expired  Status = "expired"
)

type Packet struct {
	ID             int64     `db:"id" json:"id,string"`
	SenderID       int64     `db:"sender_id" json:"sender_id,string"`
	RecipientID    *int64    `db:"recipient_id" json:"-"`
	PointsPerShare int64     `db:"points_per_share" json:"points_per_share,string"`
	ShareCount     int       `db:"share_count" json:"share_count"`
	Remaining      int       `db:"remaining" json:"remaining"`
	Status         Status    `db:"status" json:"status"`
	ExpiresAt      time.Time `db:"expires_at" json:"expires_at"`
}

type Claim struct {
	Points    int64     `db:"points" json:"points,string"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type Store struct{ db *sqlx.DB }

const selectPacket = `SELECT id, sender_id, recipient_id, points_per_share, share_count, remaining, status, expires_at FROM red_packets WHERE id = ?`

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

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

func updatePacket(ctx context.Context, tx *sqlx.Tx, id int64, remaining int, status Status) error {
	_, err := tx.ExecContext(ctx, `UPDATE red_packets SET remaining = ?, status = ? WHERE id = ?`, remaining, status, id)
	return err
}

func databaseTime(ctx context.Context, tx *sqlx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.GetContext(ctx, &now, `SELECT UTC_TIMESTAMP(6)`)
	return now, err
}

func (s *Store) statuses(ctx context.Context, packetIDs []int64) (map[int64]Status, error) {
	statuses := make(map[int64]Status, len(packetIDs))
	if len(packetIDs) == 0 {
		return statuses, nil
	}
	query, args, err := sqlx.In(`SELECT id, status FROM red_packets WHERE id IN (?)`, packetIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID     int64  `db:"id"`
		Status Status `db:"status"`
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
