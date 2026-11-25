package httpapi

import "github.com/gin-gonic/gin"

func (h *handler) registerAuthRoutes(api *gin.RouterGroup) {
	auth := api.Group("/auth")
	auth.POST("/register", h.register)
	auth.POST("/login", h.login)
}

func (h *handler) registerUserRoutes(api *gin.RouterGroup) {
	me := api.Group("/me")
	me.GET("", h.me)
	me.PUT("/avatar", h.uploadAvatar)
	api.Group("/users").GET("/by-username", h.lookupUser)

	points := api.Group("/points")
	points.GET("/balance", h.pointBalance)
	points.GET("/ledger", h.pointLedger)
}

func (h *handler) registerChatRoutes(api *gin.RouterGroup) {
	conversations := api.Group("/conversations")
	conversations.GET("", h.listConversations)
	conversations.GET("/versions", h.listConversationVersions)
	conversations.POST("/direct", h.createDirect)

	conversation := conversations.Group("/:id")
	conversation.POST("/messages", h.sendMessage)
	conversation.GET("/messages", h.listMessages)
	conversation.GET("/sync-state", h.getSyncState)
	conversation.POST("/read", h.markRead)
	api.Group("/messages").GET("/:id/image", h.readImage)
}

func (h *handler) registerSocialRoutes(api *gin.RouterGroup) {
	friends := api.Group("/friends")
	friends.GET("", h.listFriends)
	friends.DELETE("/:id", h.removeFriend)

	requests := api.Group("/friend-requests")
	requests.GET("", h.listFriendRequests)
	requests.POST("", h.requestFriend)
	requests.PUT("/:id", h.resolveFriendRequest)

	groups := api.Group("/groups")
	groups.POST("", h.createGroup)
	group := groups.Group("/:id")
	group.GET("", h.groupDetails)
	group.PUT("", h.renameGroup)
	group.POST("/members", h.inviteGroupMembers)
	group.DELETE("/members/:user_id", h.removeGroupMember)
	group.PUT("/members/:user_id", h.setGroupRole)
	group.POST("/transfer", h.transferGroup)
	group.POST("/leave", h.leaveGroup)

	// 发红包沿用会话地址，领取和详情使用红包 ID。
	api.POST("/conversations/:id/red-packets", h.sendPacket)
	packet := api.Group("/red-packets/:id")
	packet.GET("", h.packetDetails)
	packet.POST("/claim", h.claimPacket)
}

func (h *handler) registerRealtimeRoutes(router *gin.Engine, api *gin.RouterGroup) {
	api.Group("/ws").POST("/tickets", h.issueWSTicket)
	// 握手在根路由注册，使用一次性票据鉴权。
	router.GET("/ws", h.openWebSocket)
}
