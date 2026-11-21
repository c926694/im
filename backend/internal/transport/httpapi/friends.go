package httpapi

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/user"
)

type friendRequestResponse struct {
	ID        string       `json:"id"`
	Peer      user.Profile `json:"peer"`
	Direction string       `json:"direction"`
}

func (h *handler) lookupUser(c *gin.Context) {
	username, ok := normalizeUsername(c.Query("username"))
	if !ok {
		reject(c, http.StatusBadRequest, "invalid_peer_username")
		return
	}
	profile, err := h.users.FindProfileByUsername(c.Request.Context(), username)
	if err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, profile)
}

func (h *handler) requestFriend(c *gin.Context) {
	var form struct {
		PeerUsername string `json:"peer_username"`
	}
	if err := c.ShouldBindJSON(&form); err != nil {
		reject(c, http.StatusBadRequest, "invalid_peer_username")
		return
	}
	username, ok := normalizeUsername(form.PeerUsername)
	if !ok {
		reject(c, http.StatusBadRequest, "invalid_peer_username")
		return
	}
	ctx := c.Request.Context()
	// 关系表只保存用户 ID；完整用户名用于确定申请接收人。
	peer, err := h.users.FindProfileByUsername(ctx, username)
	if err != nil {
		friendError(c, err)
		return
	}
	// 申请人来自登录身份，重复申请沿用已有的申请标识。
	entry, err := h.friends.Request(ctx, authenticated(c).userID, peer.ID)
	if err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, friendRequestResponse{ID: entry.RequestID, Peer: peer, Direction: "outgoing"})
}

func (h *handler) listFriends(c *gin.Context) {
	entries, profiles, err := h.friendProfiles(c, friendship.Accepted)
	if err != nil {
		internalError(c, err)
		return
	}
	response := make([]user.Profile, 0, len(entries))
	for _, entry := range entries {
		response = append(response, profiles[entry.PeerID])
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) listFriendRequests(c *gin.Context) {
	entries, profiles, err := h.friendProfiles(c, friendship.Pending)
	if err != nil {
		internalError(c, err)
		return
	}
	userID := authenticated(c).userID
	response := make([]friendRequestResponse, 0, len(entries))
	for _, entry := range entries {
		direction := "incoming"
		if entry.RequestedBy == userID {
			direction = "outgoing"
		}
		response = append(response, friendRequestResponse{ID: entry.RequestID, Peer: profiles[entry.PeerID], Direction: direction})
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) friendProfiles(c *gin.Context, status friendship.Status) ([]friendship.Relationship, map[int64]user.Profile, error) {
	ctx := c.Request.Context()
	// 先查询当前用户的关系，再按关系中的用户 ID 批量查询昵称和用户名。
	entries, err := h.friends.List(ctx, authenticated(c).userID, status)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.PeerID)
	}
	profiles, err := h.users.ProfilesByIDs(ctx, ids)
	return entries, profiles, err
}

func (h *handler) resolveFriendRequest(c *gin.Context) {
	var form struct {
		Status friendship.Status `json:"status"`
	}
	if err := c.ShouldBindJSON(&form); err != nil || (form.Status != friendship.Accepted && form.Status != friendship.Rejected) {
		reject(c, http.StatusBadRequest, "invalid_friend_decision")
		return
	}
	id := c.Param("id")
	if _, err := hex.DecodeString(id); len(id) != 32 || err != nil {
		reject(c, http.StatusBadRequest, "invalid_friend_request_id")
		return
	}
	// 按申请标识处理当前用户收到的待办，防止旧申请改变新关系。
	if err := h.friends.Resolve(c.Request.Context(), authenticated(c).userID, id, form.Status); err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": form.Status})
}

func (h *handler) removeFriend(c *gin.Context) {
	peerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || peerID <= 0 {
		reject(c, http.StatusBadRequest, "invalid_friend_id")
		return
	}
	// 解除好友只修改关系，旧会话和消息继续可查。
	if err := h.friends.Remove(c.Request.Context(), authenticated(c).userID, peerID); err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": friendship.Removed})
}

func friendError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, user.ErrUserNotFound):
		reject(c, http.StatusNotFound, "user_not_found")
	case errors.Is(err, friendship.ErrSelfRequest):
		reject(c, http.StatusBadRequest, "self_friend_request")
	case errors.Is(err, friendship.ErrIncomingRequest):
		reject(c, http.StatusConflict, "incoming_friend_request")
	case errors.Is(err, friendship.ErrAlreadyFriends):
		reject(c, http.StatusConflict, "already_friends")
	case errors.Is(err, friendship.ErrRequestUnavailable):
		reject(c, http.StatusNotFound, "friend_request_unavailable")
	default:
		internalError(c, err)
	}
}
