package httpapi

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/social"
	"github.com/c926694/im/backend/internal/user"
)

type groupMemberResponse struct {
	user.Profile
	Role chat.Role `json:"role"`
}

type groupResponse struct {
	ID      int64                 `json:"id,string"`
	Name    string                `json:"name"`
	Members []groupMemberResponse `json:"members"`
}

func (h *handler) createGroup(c *gin.Context) {
	var form struct {
		Name      string   `json:"name"`
		MemberIDs []string `json:"member_ids"`
	}
	if err := c.ShouldBindJSON(&form); err != nil {
		reject(c, "invalid_group")
		return
	}
	actor := authenticated(c).userID
	name, validName := groupName(form.Name)
	ids, validIDs := inviteIDs(form.MemberIDs, actor)
	if !validName || !validIDs {
		reject(c, "invalid_group")
		return
	}
	change, err := h.social.CreateGroup(c.Request.Context(), actor, name, ids)
	h.respondGroupChange(c, change, err)
}

func (h *handler) groupDetails(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	details, err := h.social.GroupDetails(ctx, authenticated(c).userID, id)
	if err != nil {
		groupError(c, err)
		return
	}
	// 群成员查询返回用户 ID，再一次读取成员资料。
	ids := make([]int64, 0, len(details.Members))
	for _, member := range details.Members {
		ids = append(ids, member.UserID)
	}
	profiles, err := h.users.ProfilesByIDs(ctx, ids)
	if err != nil {
		internalError(c, err)
		return
	}
	response := groupResponse{ID: id, Name: details.Conversation.Name, Members: make([]groupMemberResponse, 0, len(ids))}
	for _, member := range details.Members {
		response.Members = append(response.Members, groupMemberResponse{Profile: profiles[member.UserID], Role: member.Role})
	}
	respondSuccess(c, response)
}

func (h *handler) renameGroup(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	var form struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&form); err != nil {
		reject(c, "invalid_group")
		return
	}
	name, ok := groupName(form.Name)
	if !ok {
		reject(c, "invalid_group")
		return
	}
	change, err := h.social.RenameGroup(c.Request.Context(), authenticated(c).userID, id, name)
	h.respondGroupChange(c, change, err)
}

func (h *handler) inviteGroupMembers(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	var form struct {
		MemberIDs []string `json:"member_ids"`
	}
	if err := c.ShouldBindJSON(&form); err != nil {
		reject(c, "invalid_group")
		return
	}
	actor := authenticated(c).userID
	ids, ok := inviteIDs(form.MemberIDs, actor)
	if !ok || len(ids) == 0 {
		reject(c, "invalid_group")
		return
	}
	change, err := h.social.InviteMembers(c.Request.Context(), actor, id, ids)
	h.respondGroupChange(c, change, err)
}

func (h *handler) removeGroupMember(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	target, ok := groupTarget(c)
	if !ok {
		return
	}
	change, err := h.social.RemoveGroupMember(c.Request.Context(), authenticated(c).userID, id, target)
	h.respondGroupChange(c, change, err)
}

func (h *handler) setGroupRole(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	target, ok := groupTarget(c)
	if !ok {
		return
	}
	var form struct {
		Role chat.Role `json:"role"`
	}
	if err := c.ShouldBindJSON(&form); err != nil || (form.Role != chat.Admin && form.Role != chat.Normal) {
		reject(c, "invalid_group")
		return
	}
	change, err := h.social.SetGroupRole(c.Request.Context(), authenticated(c).userID, id, target, form.Role)
	h.respondGroupChange(c, change, err)
}

func (h *handler) transferGroup(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	var form struct {
		UserID int64 `json:"user_id,string"`
	}
	if err := c.ShouldBindJSON(&form); err != nil || form.UserID <= 0 {
		reject(c, "invalid_group")
		return
	}
	change, err := h.social.TransferGroup(c.Request.Context(), authenticated(c).userID, id, form.UserID)
	h.respondGroupChange(c, change, err)
}

func (h *handler) leaveGroup(c *gin.Context) {
	id, ok := conversationParam(c)
	if !ok {
		return
	}
	change, err := h.social.LeaveGroup(c.Request.Context(), authenticated(c).userID, id)
	h.respondGroupChange(c, change, err)
}

// respondGroupChange 在事务提交后通知受影响的成员重新查询会话。
func (h *handler) respondGroupChange(c *gin.Context, change social.GroupChange, err error) {
	if err != nil {
		groupError(c, err)
		return
	}
	if err := h.hub.PushConversationUpdated(change.Recipients, realtime.ConversationUpdated{ConversationID: change.ID}); err != nil {
		slog.Warn("group change saved but push failed", "conversation_id", change.ID, "error", err)
	}
	respondSuccess(c, gin.H{"id": strconv.FormatInt(change.ID, 10)})
}

func groupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, social.ErrGroupUnavailable):
		reject(c, "group_unavailable")
	case errors.Is(err, social.ErrGroupPermission):
		reject(c, "group_permission")
	case errors.Is(err, social.ErrGroupMemberMissing):
		reject(c, "group_member_missing")
	case errors.Is(err, social.ErrGroupLimit):
		reject(c, "group_limit")
	case errors.Is(err, social.ErrGroupInviteFriends):
		reject(c, "group_invite_friends")
	case errors.Is(err, social.ErrOwnerMustTransfer):
		reject(c, "owner_must_transfer")
	default:
		chatError(c, err)
	}
}

func groupName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	return name, name != "" && utf8.RuneCountInString(name) <= 64
}

func groupTarget(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || id <= 0 {
		reject(c, "invalid_group")
		return 0, false
	}
	return id, true
}

// inviteIDs 将外部字符串转换为唯一的用户 ID，并排除操作者自己。
func inviteIDs(values []string, actor int64) ([]int64, bool) {
	ids := make([]int64, 0, len(values))
	seen := map[int64]bool{actor: true}
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, false
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, true
}
