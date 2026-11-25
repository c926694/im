package httpapi

import (
	"bytes"
	_ "embed"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

//go:embed default.webp
var defaultAvatar []byte

const maxAvatarSize = 2 << 20

func (h *handler) me(c *gin.Context) {
	id := authenticated(c).userID
	profiles, err := h.users.ProfilesByIDs(c.Request.Context(), []int64{id})
	if err != nil {
		internalError(c, err)
		return
	}
	respondSuccess(c, profiles[id])
}

func (h *handler) uploadAvatar(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAvatarSize+4096)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		reject(c, "invalid_avatar")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "avatar" || part.FileName() == "" {
		reject(c, "invalid_avatar")
		return
	}
	data, err := io.ReadAll(io.LimitReader(part, maxAvatarSize+1))
	if err != nil || len(data) > maxAvatarSize {
		reject(c, "invalid_avatar")
		return
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width > 4096 || config.Height > 4096 {
		reject(c, "invalid_avatar")
		return
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		reject(c, "invalid_avatar")
		return
	}
	name := uuid.NewString() + "." + format
	if err := os.MkdirAll(h.avatarDir, 0755); err != nil {
		internalError(c, err)
		return
	}
	path := filepath.Join(h.avatarDir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		internalError(c, err)
		return
	}
	avatarURL := "/avatars/" + name
	// 保存访问路径后，用户资料接口可直接返回相同的 URL。
	if err := h.users.SetAvatar(c.Request.Context(), authenticated(c).userID, avatarURL); err != nil {
		_ = os.Remove(path)
		internalError(c, err)
		return
	}
	respondSuccess(c, gin.H{"avatar_url": avatarURL})
}

func (h *handler) serveAvatar(c *gin.Context) {
	name := c.Param("name")
	if name == "default.webp" {
		c.Data(http.StatusOK, "image/webp", defaultAvatar)
		return
	}
	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	id, err := uuid.Parse(base)
	if err != nil || id.String() != base || (ext != ".jpeg" && ext != ".png" && ext != ".webp") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(filepath.Join(h.avatarDir, name))
}
