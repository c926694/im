package httpapi

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/notify"
	"github.com/c926694/im/backend/internal/outbox"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/social"
	"github.com/c926694/im/backend/internal/user"
	"github.com/jmoiron/sqlx"
)

type directNotificationTransport struct{ notify.Notifier }

func (n directNotificationTransport) Publish(ctx context.Context, event outbox.Event) error {
	return n.Deliver(ctx, event)
}

// startNotifications 使用实际的 Outbox 扫描、成员查询和 WebSocket 推送。
func startNotifications(t *testing.T, db *sqlx.DB, chats *chat.Service, socialService *social.Service, hub *realtime.Hub) {
	t.Helper()
	events := outbox.NewStore(db)
	chats.SetMessageCommittedListener(events.Wake)
	notifier := notify.New(chats, socialService, user.NewStore(db), hub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		events.Run(ctx, directNotificationTransport{notifier}, 1)
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func waitMessagePublished(t *testing.T, db *sqlx.DB, id int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var published sql.NullTime
		err := db.Get(&published, `SELECT published_at FROM outbox_events
			WHERE kind = 'message_created' AND message_id = ?`, id)
		if err != nil {
			t.Fatal(err)
		}
		if published.Valid {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("message %d still pending", id)
}
