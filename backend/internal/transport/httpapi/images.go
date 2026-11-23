package httpapi

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/gin-gonic/gin"
)

const maxChatImageSize = 5 << 20

func (h *handler) sendImage(c *gin.Context) {
	conversationID, ok := conversationParam(c)
	if !ok {
		return
	}
	select {
	case h.imageSlots <- struct{}{}:
		defer func() { <-h.imageSlots }()
	default:
		reject(c, "image_busy")
		return
	}
	clientID, caption, attachment, err := parseChatImage(c)
	if err != nil {
		reject(c, "invalid_chat_image")
		return
	}
	ctx, senderID := c.Request.Context(), authenticated(c).userID
	profiles, err := h.users.ProfilesByIDs(ctx, []int64{senderID})
	if err != nil {
		internalError(c, err)
		return
	}
	result, err := h.chat.SendImage(ctx, senderID, conversationID, clientID, caption, attachment)
	if err != nil {
		chatError(c, err)
		return
	}
	respondSuccess(c, newMessageResponse(result.Message, profiles[senderID]))
}

func parseChatImage(c *gin.Context) (string, string, chat.Image, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatImageSize+16384)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return "", "", chat.Image{}, err
	}
	var content []byte
	var clientID, caption string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", chat.Image{}, err
		}
		switch part.FormName() {
		case "client_msg_id":
			data, err := io.ReadAll(io.LimitReader(part, 37))
			if err != nil {
				return "", "", chat.Image{}, err
			}
			clientID, err = chat.ParseClientMessageID(string(data))
			if err != nil {
				return "", "", chat.Image{}, err
			}
		case "content":
			data, err := io.ReadAll(io.LimitReader(part, 8001))
			if err != nil {
				return "", "", chat.Image{}, err
			}
			caption = string(data)
		case "image":
			if content != nil {
				return "", "", chat.Image{}, errors.New("multiple images")
			}
			content, err = io.ReadAll(io.LimitReader(part, maxChatImageSize+1))
			if err != nil {
				return "", "", chat.Image{}, err
			}
			if len(content) > maxChatImageSize {
				return "", "", chat.Image{}, errors.New("image exceeds size limit")
			}
		default:
			return "", "", chat.Image{}, errors.New("unexpected multipart field")
		}
	}
	if clientID == "" {
		return "", "", chat.Image{}, chat.ErrInvalidClientMsgID
	}
	if strings.TrimSpace(caption) == "" {
		caption = "[图片]"
	} else if err := chat.ValidateMessageContent(caption); err != nil {
		return "", "", chat.Image{}, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || config.Width > 4096 || config.Height > 4096 {
		return "", "", chat.Image{}, errors.New("invalid image format or dimensions")
	}
	if _, _, err := image.Decode(bytes.NewReader(content)); err != nil {
		return "", "", chat.Image{}, err
	}
	return clientID, caption, chat.Image{MIMEType: "image/" + format, Content: content}, nil
}

func (h *handler) readImage(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		reject(c, "invalid_request")
		return
	}
	attachment, err := h.chat.ReadImage(c.Request.Context(), authenticated(c).userID, id)
	if errors.Is(err, sql.ErrNoRows) {
		reject(c, "route_not_found")
		return
	}
	if err != nil {
		chatError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, attachment.MIMEType, attachment.Content)
}
