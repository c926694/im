package httpapi

import (
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/user"
)

type messageResponse realtime.MessageCreated

type conversationResponse struct {
	ID             int64     `json:"id,string"`
	PeerUserID     int64     `json:"peer_user_id,string"`
	PeerNickname   string    `json:"peer_nickname"`
	IsFriend       bool      `json:"is_friend"`
	LastSeq        int64     `json:"last_seq,string"`
	LastReadSeq    int64     `json:"last_read_seq,string"`
	LastActivityAt time.Time `json:"last_activity_at"`
	UnreadCount    int64     `json:"unread_count"`
}

func (h *handler) createDirect(c *gin.Context) {
	var form struct {
		PeerUsername string `json:"peer_username"`
	}
	if err := c.ShouldBindJSON(&form); err != nil {
		reject(c, "invalid_peer_username")
		return
	}
	username, ok := normalizeUsername(form.PeerUsername)
	if !ok {
		reject(c, "invalid_peer_username")
		return
	}
	ctx := c.Request.Context()
	// 用户名用于查找账号，单聊关系仍以用户 ID 保存。
	peerID, err := h.users.FindIDByUsername(ctx, username)
	if err != nil {
		chatError(c, err)
		return
	}
	// 创建和打开已有单聊用同一个入口；数据层保证同一对用户只建一个会话。
	id, err := h.chat.CreateDirect(ctx, authenticated(c).userID, peerID)
	if err != nil {
		chatError(c, err)
		return
	}
	respondSuccess(c, gin.H{"id": strconv.FormatInt(id, 10)})
}

func (h *handler) listConversations(c *gin.Context) {
	ctx := c.Request.Context()
	// 会话查询返回对方用户 ID，供后续批量查询昵称。
	userID := authenticated(c).userID
	conversations, err := h.chat.ListConversations(ctx, userID)
	if err != nil {
		internalError(c, err)
		return
	}
	peerIDs := make([]int64, 0, len(conversations))
	for _, conversation := range conversations {
		peerIDs = append(peerIDs, conversation.PeerUserID)
	}
	// 一次查询当前列表的昵称，避免逐个查询用户。
	profiles, err := h.users.ProfilesByIDs(ctx, peerIDs)
	if err != nil {
		internalError(c, err)
		return
	}
	accepted := make(map[int64]bool)
	if len(conversations) > 0 {
		// 好友状态只控制发送入口，历史会话仍保留在列表中。
		friends, err := h.friends.List(ctx, userID, friendship.Accepted)
		if err != nil {
			internalError(c, err)
			return
		}
		for _, friend := range friends {
			accepted[friend.PeerID] = true
		}
	}
	response := make([]conversationResponse, len(conversations))
	for i, conversation := range conversations {
		response[i] = conversationResponse{
			ID: conversation.ID, PeerUserID: conversation.PeerUserID, PeerNickname: profiles[conversation.PeerUserID].Nickname,
			IsFriend: accepted[conversation.PeerUserID],
			LastSeq:  conversation.LastSeq, LastReadSeq: conversation.LastReadSeq,
			LastActivityAt: conversation.LastActivityAt, UnreadCount: conversation.UnreadCount,
		}
	}
	respondSuccess(c, response)
}

func (h *handler) sendMessage(c *gin.Context) {
	conversationID, ok := conversationParam(c)
	if !ok {
		return
	}
	var form struct {
		ClientMsgID string `json:"client_msg_id"`
		Content     string `json:"content"`
	}
	if err := c.ShouldBindJSON(&form); err != nil {
		reject(c, "invalid_message")
		return
	}
	draft, err := chat.NewMessageDraft(form.ClientMsgID, form.Content)
	if err != nil {
		chatError(c, err)
		return
	}

	// 发送者取自 Bearer 身份；发送服务校验会话成员与好友资格。
	result, err := h.chat.SendMessage(c.Request.Context(), authenticated(c).userID, conversationID, draft)
	if err != nil {
		chatError(c, err)
		return
	}
	// 只推送首次写入的消息；在线通知失败也不能改掉已成功保存的结果。
	if result.Created {
		if err := h.hub.Push(result.RecipientID, realtime.MessageCreated(result.Message)); err != nil {
			slog.Warn("message saved but push failed", "message_id", result.Message.ID, "error", err)
		}
	}
	respondSuccess(c, messageResponse(result.Message))
}

func (h *handler) listMessages(c *gin.Context) {
	conversationID, ok := conversationParam(c)
	if !ok {
		return
	}
	page, ok := parseMessagePage(c.Request.URL.Query())
	if !ok {
		reject(c, "invalid_pagination")
		return
	}

	ctx, userID := c.Request.Context(), authenticated(c).userID
	var messages []chat.Message
	var err error
	// 传 after_seq=0 也表示正序补拉，不能把它当作没传游标。
	if page.sync {
		messages, err = h.chat.Sync(ctx, userID, conversationID, page.seq, page.limit)
	} else {
		messages, err = h.chat.History(ctx, userID, conversationID, page.seq, page.limit)
	}
	if err != nil {
		chatError(c, err)
		return
	}
	response := make([]messageResponse, len(messages))
	for i, message := range messages {
		response[i] = messageResponse(message)
	}
	respondSuccess(c, response)
}

// markRead 通过登录身份更新会话成员的已读序号。
func (h *handler) markRead(c *gin.Context) {
	conversationID, ok := conversationParam(c)
	if !ok {
		return
	}
	var form struct {
		Seq *int64 `json:"seq,string"`
	}
	if err := c.ShouldBindJSON(&form); err != nil || form.Seq == nil {
		reject(c, "invalid_read_seq")
		return
	}
	if err := h.chat.MarkRead(c.Request.Context(), authenticated(c).userID, conversationID, *form.Seq); err != nil {
		chatError(c, err)
		return
	}
	respondSuccess(c, nil)
}

type messagePage struct {
	seq   int64
	limit int
	sync  bool
}

func parseMessagePage(query url.Values) (messagePage, bool) {
	before, after := query.Has("before_seq"), query.Has("after_seq")
	if before && after {
		return messagePage{}, false
	}
	page := messagePage{sync: after}
	if before || after {
		value := query.Get("before_seq")
		if after {
			value = query.Get("after_seq")
		}
		seq, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seq < 0 {
			return messagePage{}, false
		}
		page.seq = seq
	}
	if query.Has("limit") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil {
			return messagePage{}, false
		}
		page.limit = limit
	}
	return page, true
}

func conversationParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		reject(c, "invalid_conversation_id")
		return 0, false
	}
	return id, true
}

func chatError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chat.ErrSelfConversation):
		reject(c, "self_conversation")
	case errors.Is(err, chat.ErrUserNotFound), errors.Is(err, user.ErrUserNotFound):
		reject(c, "user_not_found")
	case errors.Is(err, chat.ErrNotMember):
		reject(c, "not_conversation_member")
	case errors.Is(err, friendship.ErrNotFriends):
		reject(c, "not_friends")
	case errors.Is(err, chat.ErrInvalidClientMsgID):
		reject(c, "invalid_client_msg_id")
	case errors.Is(err, chat.ErrInvalidContent):
		reject(c, "invalid_content")
	case errors.Is(err, chat.ErrInvalidReadSeq):
		reject(c, "invalid_read_seq")
	case errors.Is(err, chat.ErrInvalidPageSize):
		reject(c, "invalid_pagination")
	default:
		internalError(c, err)
	}
}
