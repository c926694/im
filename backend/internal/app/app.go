package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/config"
	"github.com/c926694/im/backend/internal/notify"
	"github.com/c926694/im/backend/internal/outbox"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/platform/rocketmq"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/social"
	"github.com/c926694/im/backend/internal/transport/httpapi"
	"github.com/c926694/im/backend/internal/user"
)

func Run(ctx context.Context, cfg config.Config) error {
	gin.SetMode(cfg.HTTP.Mode)
	if err := rocketmq.ConfigureProxyLogger(); err != nil {
		return fmt.Errorf("configure RocketMQ logger: %w", err)
	}

	db, err := mysql.Open(cfg.MySQL.DSN)
	if err != nil {
		return errors.New("invalid mysql.dsn")
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

	if err := chat.MigrateImageFiles(ctx, db, cfg.Storage.ChatImageDir); err != nil {
		return fmt.Errorf("migrate chat images: %w", err)
	}

	// HTTP 发消息和 WebSocket 握手必须共用同一个 Hub。
	hub := realtime.NewHub()
	defer hub.Close()
	users := user.NewStore(db)
	socialStore := social.NewStore(db)
	chatStore := chat.NewStore(db)
	chats := chat.NewService(chatStore, socialStore, cfg.Storage.ChatImageDir)
	socialService := social.NewService(socialStore, chats, chatStore)
	notifier := notify.New(chats, socialService, users, hub)
	events := outbox.NewStore(db)
	chats.SetMessageCommittedListener(events.Wake)
	stopMessaging := (messagingTasks{
		nameServer: cfg.RocketMQ.NameServer, endpoint: cfg.RocketMQ.Proxy, events: events, notifier: notifier, packets: socialService,
	}).Start(ctx)
	defer stopMessaging()
	hub.SetPresenceListener(func(id int64) {
		queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		entries, err := socialService.List(queryCtx, id, social.Accepted)
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
	router.Handle("/", httpapi.NewRouter(users, chats, socialService, hub, []byte(cfg.Auth.JWTSecret), cfg.Storage.AvatarDir))
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
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("starting IM HTTP and WebSocket server", "addr", server.Addr)
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
