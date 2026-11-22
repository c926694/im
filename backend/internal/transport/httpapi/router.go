package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/points"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/redpacket"
	"github.com/c926694/im/backend/internal/user"
)

type handler struct {
	points     *points.Store
	redPackets *redpacket.Service
	users      *user.Store
	chat       *chat.Service
	friends    *friendship.Service
	hub        *realtime.Hub
	jwtSecret  []byte
	tickets    ticketStore
}

func NewRouter(users *user.Store, chatService *chat.Service, friends *friendship.Service, hub *realtime.Hub, jwtSecret []byte, pointStore *points.Store, redPackets *redpacket.Service) *gin.Engine {
	h := &handler{users: users, chat: chatService, friends: friends, hub: hub, jwtSecret: jwtSecret, points: pointStore, redPackets: redPackets}
	router := gin.New()
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
	protected.POST("/conversations/:id/read", h.markRead)
	protected.GET("/points/balance", h.pointBalance)
	protected.GET("/points/ledger", h.pointLedger)
	protected.POST("/conversations/:id/red-packets", h.sendPacket)
	protected.GET("/red-packets/:id", h.packetDetails)
	protected.POST("/red-packets/:id/claim", h.claimPacket)
	protected.POST("/groups", h.createGroup)
	protected.GET("/groups/:id", h.groupDetails)
	protected.PATCH("/groups/:id", h.renameGroup)
	protected.POST("/groups/:id/members", h.inviteGroupMembers)
	protected.DELETE("/groups/:id/members/:user_id", h.removeGroupMember)
	protected.PATCH("/groups/:id/members/:user_id", h.setGroupRole)
	protected.POST("/groups/:id/transfer", h.transferGroup)
	protected.POST("/groups/:id/leave", h.leaveGroup)
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
