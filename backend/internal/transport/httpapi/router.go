package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/social"
	"github.com/c926694/im/backend/internal/user"
)

type handler struct {
	avatarDir  string
	imageSlots chan struct{}
	users      *user.Store
	chat       *chat.Service
	social     *social.Service
	hub        *realtime.Hub
	jwtSecret  []byte
	tickets    ticketStore
}

func NewRouter(users *user.Store, chatService *chat.Service, socialService *social.Service, hub *realtime.Hub,
	jwtSecret []byte, avatarDir string) *gin.Engine {
	h := &handler{avatarDir: avatarDir, imageSlots: make(chan struct{}, 4), users: users, chat: chatService,
		social: socialService, hub: hub, jwtSecret: jwtSecret}
	router := gin.New()
	router.GET("/avatars/:name", h.serveAvatar)
	// /ws 的查询参数里有票据，不能使用会打印原始请求的日志或 Recovery。
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		slog.Error("request panicked", "route", c.FullPath())
		reject(c, "internal_error")
	}))
	router.RedirectTrailingSlash = false
	router.NoRoute(func(c *gin.Context) {
		reject(c, "route_not_found")
	})
	router.NoMethod(func(c *gin.Context) {
		reject(c, "invalid_request")
	})
	router.GET("/healthz", func(c *gin.Context) {
		respondSuccess(c, gin.H{"status": "ok"})
	})

	api := router.Group("/api/v1")
	api.Use(func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h.registerAuthRoutes(api)
	protected := api.Group("", h.requireBearer)
	h.registerUserRoutes(protected)
	h.registerChatRoutes(protected)
	h.registerSocialRoutes(protected)
	h.registerRealtimeRoutes(router, protected)
	return router
}

func internalError(c *gin.Context, err error) {
	if errors.Is(err, context.Canceled) {
		// 浏览器取消请求后连接已经关闭，服务端无法再返回错误内容。
		return
	}
	slog.Error("request failed", "route", c.FullPath(), "error", err)
	reject(c, "internal_error")
}
