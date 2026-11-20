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
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/realtime"
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

	// HTTP 发消息和 WebSocket 握手必须共用同一个 Hub。
	hub := realtime.NewHub()
	defer hub.Close()
	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(user.NewStore(db), chat.NewStore(db), hub, []byte(secret)),
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
