package httpapi

import (
	"errors"
	"log/slog"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/redpacket"
	"github.com/c926694/im/backend/internal/user"
)

type messageResponse realtime.MessageCreated

func newMessageResponse(message chat.Message, profile user.Profile) messageResponse {
	return messageResponse(realtime.NewMessageCreated(message, profile.Nickname, profile.AvatarURL))
}

type conversationResponse struct {
	ID             int64     `json:"id,string"`
	Kind           chat.Kind `json:"kind"`
	GroupName      string    `json:"group_name,omitempty"`
	JoinedSeq      int64     `json:"joined_seq,string"`
	PeerUserID     int64     `json:"peer_user_id,string,omitempty"`
	PeerAvatarURL  string    `json:"peer_avatar_url,omitempty"`
	PeerOnline     *bool     `json:"peer_online,omitempty"`
	PeerNickname   string    `json:"peer_nickname,omitempty"`
	IsFriend       bool      `json:"is_friend"`
	LastSeq        int64     `json:"last_seq,string"`
	LastReadSeq    int64     `json:"last_read_seq,string"`
	LastActivityAt time.Time `json:"last_activity_at"`
	LastMessage    string    `json:"last_message"`
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
	var ids []int64
	if raw, present := c.GetQuery("ids"); present {
		items := strings.Split(raw, ",")
		if len(items) > 50 {
			reject(c, "invalid_request")
			return
		}
		for _, item := range items {
			id, err := strconv.ParseInt(item, 10, 64)
			if err != nil || id <= 0 {
				reject(c, "invalid_request")
				return
			}
			ids = append(ids, id)
		}
	}
	conversations, err := h.chat.ListConversations(ctx, userID, ids...)
	if err != nil {
		internalError(c, err)
		return
	}
	peerIDs := make([]int64, 0, len(conversations))
	for _, conversation := range conversations {
		if conversation.Kind == chat.Direct {
			peerIDs = append(peerIDs, conversation.PeerUserID)
		}
	}
	// 一次查询当前列表的昵称，避免逐个查询用户。
	profiles, err := h.users.ProfilesByIDs(ctx, peerIDs)
	if err != nil {
		internalError(c, err)
		return
	}
	accepted := make(map[int64]bool)
	if len(peerIDs) > 0 {
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
		var online *bool
		if accepted[conversation.PeerUserID] {
			value := h.hub.Online(conversation.PeerUserID)
			online = &value
		}
		response[i] = conversationResponse{
			PeerOnline: online, PeerAvatarURL: profiles[conversation.PeerUserID].AvatarURL,
			ID: conversation.ID, PeerUserID: conversation.PeerUserID, PeerNickname: profiles[conversation.PeerUserID].Nickname,
			Kind: conversation.Kind, GroupName: conversation.GroupName, JoinedSeq: conversation.JoinedSeq,
			IsFriend: accepted[conversation.PeerUserID],
			LastSeq:  conversation.LastSeq, LastReadSeq: conversation.LastReadSeq,
			LastActivityAt: conversation.LastActivityAt, LastMessage: conversation.LastMessage,
			UnreadCount: conversation.UnreadCount,
		}
	}
	respondSuccess(c, response)
}

type conversationVersion struct {
	ID          int64 `json:"id,string"`
	JoinedSeq   int64 `json:"joined_seq,string"`
	LastSeq     int64 `json:"last_seq,string"`
	LastReadSeq int64 `json:"last_read_seq,string"`
}

func (h *handler) listConversationVersions(c *gin.Context) {
	versions, err := h.chat.ListConversationVersions(c.Request.Context(), authenticated(c).userID)
	if err != nil {
		internalError(c, err)
		return
	}
	result := make([]conversationVersion, 0, len(versions))
	for _, item := range versions {
		result = append(result, conversationVersion{item.ID, item.JoinedSeq, item.LastSeq, item.LastReadSeq})
	}
	respondSuccess(c, result)
}

func (h *handler) sendMessage(c *gin.Context) {
	mediaType, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaType == "multipart/form-data" {
		h.sendImage(c)
		return
	}
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

	ctx, senderID := c.Request.Context(), authenticated(c).userID
	// 在写入前取得昵称，提交成功后的推送不需要再访问数据库。
	profiles, err := h.users.ProfilesByIDs(ctx, []int64{senderID})
	if err != nil {
		internalError(c, err)
		return
	}
	// 发送者取自 Bearer 凭证，Service 按会话类型判定发送资格。
	result, err := h.chat.SendMessage(ctx, senderID, conversationID, draft)
	if err != nil {
		chatError(c, err)
		return
	}
	respondSuccess(c, newMessageResponse(result.Message, profiles[senderID]))
}

func (h *handler) getSyncState(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	seq, err := h.chat.LastVisibleSeq(c.Request.Context(), authenticated(c).userID, id)
	if err != nil {
		chatError(c, err)
		return
	}
	respondSuccess(c, gin.H{"last_seq": strconv.FormatInt(seq, 10)})
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
	senderIDs := make([]int64, 0, len(messages))
	for _, message := range messages {
		senderIDs = append(senderIDs, message.SenderID)
	}
	profiles, err := h.users.ProfilesByIDs(ctx, senderIDs)
	if err != nil {
		internalError(c, err)
		return
	}
	packetIDs := make([]int64, 0, len(messages))
	for _, message := range messages {
		if message.RedPacketID != nil {
			packetIDs = append(packetIDs, *message.RedPacketID)
		}
	}
	packetStatuses := make(map[int64]redpacket.Status)
	if len(packetIDs) > 0 {
		packetStatuses, err = h.redPackets.Statuses(ctx, packetIDs)
		if err != nil {
			internalError(c, err)
			return
		}
	}
	for i, message := range messages {
		response[i] = newMessageResponse(message, profiles[message.SenderID])
		if message.RedPacketID != nil {
			status, ok := packetStatuses[*message.RedPacketID]
			if !ok {
				internalError(c, errors.New("red packet status is missing"))
				return
			}
			response[i].RedPacketStatus = string(status)
		}
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
	userID := authenticated(c).userID
	if err := h.chat.MarkRead(c.Request.Context(), userID, conversationID, *form.Seq); err != nil {
		chatError(c, err)
		return
	}
	if err := h.hub.PushConversationRead(userID, realtime.ConversationRead{ConversationID: conversationID, Seq: *form.Seq}); err != nil {
		slog.Warn("push read progress", "error", err)
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
	case errors.Is(err, chat.ErrGroupUnavailable):
		reject(c, "group_unavailable")
	case errors.Is(err, chat.ErrGroupPermission):
		reject(c, "group_permission")
	case errors.Is(err, chat.ErrGroupMemberMissing):
		reject(c, "group_member_missing")
	case errors.Is(err, chat.ErrGroupLimit):
		reject(c, "group_limit")
	case errors.Is(err, chat.ErrGroupInviteFriends):
		reject(c, "group_invite_friends")
	case errors.Is(err, chat.ErrOwnerMustTransfer):
		reject(c, "owner_must_transfer")
	case errors.Is(err, chat.ErrMessageConflict):
		reject(c, "message_conflict")
	case errors.Is(err, chat.ErrPreviousMembershipMessage):
		reject(c, "previous_membership_message")
	default:
		internalError(c, err)
	}
}
