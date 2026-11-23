package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/notify"
	"github.com/c926694/im/backend/internal/outbox"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/points"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/redpacket"
	"github.com/c926694/im/backend/internal/transport/httpapi"
	"github.com/c926694/im/backend/internal/user"
)

func Run(ctx context.Context) error {
	dsn, secret := os.Getenv("IM_MYSQL_DSN"), os.Getenv("IM_JWT_SECRET")
	if dsn == "" {
		return errors.New("IM_MYSQL_DSN is required")
	}
	if len(secret) < 32 {
		return errors.New("IM_JWT_SECRET must be at least 32 bytes")
	}
	addr := os.Getenv("IM_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	db, err := mysql.Open(dsn)
	if err != nil {
		return errors.New("invalid IM_MYSQL_DSN")
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("connect to MySQL: %w", err)
	}
	if err := mysql.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate MySQL schema: %w", err)
	}

	if err := chat.MigrateImageFiles(ctx, db, chat.ImageDirectory()); err != nil {
		return fmt.Errorf("migrate chat images: %w", err)
	}

	// HTTP 发消息和 WebSocket 握手必须共用同一个 Hub。
	hub := realtime.NewHub()
	defer hub.Close()
	users := user.NewStore(db)
	chats := chat.NewService(chat.NewStore(db))
	packets := redpacket.NewService(redpacket.NewStore(db), chats)
	mqAddr := os.Getenv("IM_ROCKETMQ_NAMESRV")
	if mqAddr == "" {
		return errors.New("IM_ROCKETMQ_NAMESRV is required")
	}
	expiryEndpoint := os.Getenv("IM_ROCKETMQ_PROXY")
	if expiryEndpoint == "" {
		return errors.New("IM_ROCKETMQ_PROXY is required")
	}
	notifier := notify.New(chats, packets, users, hub)
	events := outbox.NewStore(db)
	chats.SetMessageCommittedListener(events.Wake)
	stopMessaging := (messagingTasks{
		nameServer: mqAddr, endpoint: expiryEndpoint, events: events, notifier: notifier, packets: packets,
	}).Start(ctx)
	defer stopMessaging()
	friends := friendship.NewService(friendship.NewStore(db))
	hub.SetPresenceListener(func(id int64) {
		queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		entries, err := friends.List(queryCtx, id, friendship.Accepted)
		if err != nil {
			if queryCtx.Err() == nil {
				slog.Warn("query presence audience", "error", err)
			}
			return
		}
		ids := make([]int64, 0, len(entries))
		for _, entry := range entries {
			ids = append(ids, entry.PeerID)
		}
		// 通知只触发重新查询，连接快速切换时客户端仍读取当前状态。
		if err := hub.PushPresence(ids); err != nil {
			slog.Warn("push presence", "error", err)
		}
	})
	router := http.NewServeMux()
	router.Handle("/", httpapi.NewRouter(users, chats, friends, hub, []byte(secret), points.NewStore(db), packets))
	router.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		probeCtx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := db.PingContext(probeCtx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("starting IM HTTP and WebSocket server", "addr", addr)
		serveErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		// HTTP Shutdown 不会关闭已升级的 WebSocket。
		hub.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
