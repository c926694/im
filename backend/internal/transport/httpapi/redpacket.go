package httpapi

import (
	"errors"
	"math"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/points"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/redpacket"
)

func (h *handler) pointBalance(c *gin.Context) {
	balance, err := h.points.Balance(c.Request.Context(), authenticated(c).userID)
	if err != nil {
		internalError(c, err)
		return
	}
	respondSuccess(c, gin.H{"balance": strconv.FormatInt(balance, 10)})
}

func (h *handler) pointLedger(c *gin.Context) {
	query := c.Request.URL.Query()
	var beforeID int64
	var err error
	if query.Has("before_id") {
		beforeID, err = strconv.ParseInt(query.Get("before_id"), 10, 64)
		if err != nil || beforeID <= 0 {
			reject(c, "invalid_pagination")
			return
		}
	}
	limit := 50
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			reject(c, "invalid_pagination")
			return
		}
	}
	entries, err := h.points.Ledger(c.Request.Context(), authenticated(c).userID, beforeID, limit)
	if err != nil {
		internalError(c, err)
		return
	}
	respondSuccess(c, entries)
}

func (h *handler) sendPacket(c *gin.Context) {
	conversationID, ok := conversationParam(c)
	if !ok {
		return
	}
	var form struct {
		ClientMsgID    string `json:"client_msg_id"`
		PointsPerShare int64  `json:"points_per_share,string"`
		ShareCount     int    `json:"share_count"`
	}
	if err := c.ShouldBindJSON(&form); err != nil || form.ShareCount < 1 || form.ShareCount > chat.MaxGroupMembers-1 ||
		form.PointsPerShare <= 0 || form.PointsPerShare > math.MaxInt64/int64(form.ShareCount) {
		reject(c, "invalid_red_packet")
		return
	}
	clientID, err := chat.ParseClientMessageID(form.ClientMsgID)
	if err != nil {
		chatError(c, err)
		return
	}
	ctx, userID := c.Request.Context(), authenticated(c).userID
	profiles, err := h.users.ProfilesByIDs(ctx, []int64{userID})
	if err != nil {
		internalError(c, err)
		return
	}
	message, err := h.redPackets.Send(ctx, userID, conversationID, redpacket.Draft{
		ClientMsgID: clientID, PointsPerShare: form.PointsPerShare, ShareCount: form.ShareCount})
	if err != nil {
		packetError(c, err)
		return
	}
	statuses, err := h.redPackets.Statuses(ctx, []int64{*message.RedPacketID})
	if err != nil {
		internalError(c, err)
		return
	}
	status, ok := statuses[*message.RedPacketID]
	if !ok {
		internalError(c, errors.New("red packet status is missing"))
		return
	}
	messageResponse := realtime.NewMessageCreated(message, profiles[userID].Nickname, profiles[userID].AvatarURL)
	messageResponse.RedPacketStatus = string(status)
	respondSuccess(c, gin.H{"red_packet_id": strconv.FormatInt(*message.RedPacketID, 10), "message": messageResponse})
}

func (h *handler) packetDetails(c *gin.Context) {
	id, ok := packetParam(c)
	if !ok {
		return
	}
	details, err := h.redPackets.Details(c.Request.Context(), id, authenticated(c).userID)
	if err != nil {
		packetError(c, err)
		return
	}
	respondSuccess(c, details)
}

func (h *handler) claimPacket(c *gin.Context) {
	id, ok := packetParam(c)
	if !ok {
		return
	}
	claim, err := h.redPackets.Claim(c.Request.Context(), id, authenticated(c).userID)
	if err != nil {
		packetError(c, err)
		return
	}
	respondSuccess(c, claim)
}

func packetParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		reject(c, "invalid_red_packet")
		return 0, false
	}
	return id, true
}

func packetError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, redpacket.ErrNotFound):
		reject(c, "red_packet_not_found")
	case errors.Is(err, redpacket.ErrInvalidShares):
		reject(c, "invalid_red_packet_shares")
	case errors.Is(err, redpacket.ErrIneligible):
		reject(c, "red_packet_ineligible")
	case errors.Is(err, redpacket.ErrClaimed):
		reject(c, "red_packet_claimed")
	case errors.Is(err, redpacket.ErrExpired):
		reject(c, "red_packet_expired")
	case errors.Is(err, points.ErrInsufficientBalance):
		reject(c, "insufficient_points")
	default:
		chatError(c, err)
	}
}
