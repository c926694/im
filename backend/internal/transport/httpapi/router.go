package httpapi

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/user"
)

type handler struct {
	users     *user.Store
	chat      *chat.Service
	friends   *friendship.Service
	hub       *realtime.Hub
	jwtSecret []byte
	tickets   ticketStore
}

func NewRouter(users *user.Store, chatService *chat.Service, friends *friendship.Service, hub *realtime.Hub, jwtSecret []byte) *gin.Engine {
	h := &handler{users: users, chat: chatService, friends: friends, hub: hub, jwtSecret: jwtSecret}
	router := gin.New()
	// /ws 的查询参数里有票据，不能使用会打印原始请求的日志或 Recovery。
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		slog.Error("request panicked", "route", c.FullPath())
		reject(c, http.StatusInternalServerError, "internal_error")
	}))
	router.RedirectTrailingSlash = false
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/ws", h.openWebSocket)

	api := router.Group("/api/v1")
	api.POST("/auth/register", h.register)
	api.POST("/auth/login", h.login)

	protected := api.Group("", h.requireBearer)
	protected.POST("/ws/tickets", h.issueWSTicket)
	protected.GET("/users/by-username", h.lookupUser)
	protected.GET("/friends", h.listFriends)
	protected.DELETE("/friends/:id", h.removeFriend)
	protected.GET("/friend-requests", h.listFriendRequests)
	protected.POST("/friend-requests", h.requestFriend)
	protected.PATCH("/friend-requests/:id", h.resolveFriendRequest)
	protected.POST("/conversations/direct", h.createDirect)
	protected.GET("/conversations", h.listConversations)
	protected.POST("/conversations/:id/messages", h.sendMessage)
	protected.GET("/conversations/:id/messages", h.listMessages)
	return router
}

func reject(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"error": code})
}

func internalError(c *gin.Context, err error) {
	slog.Error("request failed", "route", c.FullPath(), "error", err)
	reject(c, http.StatusInternalServerError, "internal_error")
}
