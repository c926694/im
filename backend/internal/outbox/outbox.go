package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/sync/errgroup"
)

type Kind string

const (
	Created        Kind = "created"
	Claimed        Kind = "claimed"
	Refunded       Kind = "refunded"
	MessageCreated Kind = "message_created"
)

const batchSize = 50

type Event struct {
	ID        int64  `db:"id" json:"id,string"`
	Kind      Kind   `db:"kind" json:"kind"`
	PacketID  int64  `db:"red_packet_id" json:"red_packet_id,string"`
	MessageID *int64 `db:"message_id" json:"message_id,string,omitempty"`
	Attempts  int    `db:"attempts" json:"-"`
}

type Publisher interface {
	Publish(context.Context, Event) error
}

type Store struct {
	db   *sqlx.DB
	wake chan struct{}
}

func NewStore(db *sqlx.DB) *Store { return &Store{db: db, wake: make(chan struct{}, 1)} }

// Wake 合并重复唤醒；进程重启时仍通过数据库扫描发现待发送事件。
func (s *Store) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Add 与业务更新使用同一个事务，发布器只能读取已经提交的事件。
func Add(ctx context.Context, tx *sqlx.Tx, event Event) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO outbox_events (kind, red_packet_id, message_id) VALUES (?, NULLIF(?, 0), ?)`,
		event.Kind, event.PacketID, event.MessageID)
	return err
}

func (s *Store) pending(ctx context.Context) ([]Event, error) {
	var events []Event
	err := s.db.SelectContext(ctx, &events, `SELECT id, kind, COALESCE(red_packet_id, 0) AS red_packet_id, message_id, attempts FROM outbox_events
		WHERE published_at IS NULL AND next_attempt_at <= UTC_TIMESTAMP(6) ORDER BY next_attempt_at, id LIMIT ?`, batchSize)
	return events, err
}

// Run 由单实例服务中的一个发布循环调用。发布成功但更新时间失败时允许再次发布。
func (s *Store) Run(ctx context.Context, publisher Publisher, parallelism int) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		count, err := s.publishBatch(ctx, publisher, parallelism)
		if err != nil && ctx.Err() == nil {
			slog.Error("publish outbox batch", "error", err)
		}
		if err == nil && count == batchSize {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *Store) publishBatch(ctx context.Context, publisher Publisher, parallelism int) (int, error) {
	events, err := s.pending(ctx)
	if err != nil {
		return 0, err
	}
	var group errgroup.Group
	group.SetLimit(parallelism)
	for _, event := range events {
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error { return s.publishOne(ctx, publisher, event) })
	}
	return len(events), group.Wait()
}

func (s *Store) publishOne(ctx context.Context, publisher Publisher, event Event) error {
	publishCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err := publisher.Publish(publishCtx, event)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		slog.Warn("outbox event publish failed", "event_id", event.ID, "error", err)
		seconds := min(60, 1<<min(event.Attempts, 6))
		_, err = s.db.ExecContext(ctx, `UPDATE outbox_events SET attempts = attempts + 1,
			next_attempt_at = TIMESTAMPADD(SECOND, ?, UTC_TIMESTAMP(6)) WHERE id = ?`, seconds, event.ID)
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE outbox_events SET published_at = UTC_TIMESTAMP(6) WHERE id = ?`, event.ID)
	return err
}
