package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/outbox"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/platform/rocketmq"
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
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	notifier := packetNotifier{chat: chats, redPackets: packets, users: users, hub: hub}
	events, err := rocketmq.Open(workerCtx, mqAddr, notifier.notify)
	if err != nil {
		return fmt.Errorf("start RocketMQ client: %w", err)
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); outbox.NewStore(db).Run(workerCtx, events) }()
	go func() { defer workers.Done(); packets.RunRefunds(workerCtx) }()
	defer func() {
		stopWorkers()
		workers.Wait()
		if err := events.Close(); err != nil {
			slog.Error("close RocketMQ client", "error", err)
		}
	}()
	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(users, chats, friendship.NewService(friendship.NewStore(db)), hub, []byte(secret), points.NewStore(db), packets),
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
