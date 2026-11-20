package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/c926694/im/backend/internal/user"
)

const (
	accessTTL    = time.Hour
	principalKey = "auth.principal"
)

type principal struct {
	userID    int64
	expiresAt time.Time
}

type credentials struct {
	Username string  `json:"username"`
	Password *string `json:"password" binding:"required"`
}

func (form *credentials) normalize() bool {
	username, ok := normalizeUsername(form.Username)
	form.Username = username
	return ok
}

func normalizeUsername(value string) (string, bool) {
	username := strings.TrimSpace(value)
	if len(username) == 0 || len(username) > 32 {
		return "", false
	}
	for _, r := range username {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '_') {
			return "", false
		}
	}
	return strings.ToLower(username), true
}

func (h *handler) register(c *gin.Context) {
	var form struct {
		credentials
		Nickname string `json:"nickname"`
	}
	if err := c.ShouldBindJSON(&form); err != nil || !form.credentials.normalize() {
		reject(c, http.StatusBadRequest, "invalid_registration")
		return
	}
	nickname := strings.TrimSpace(form.Nickname)
	if nickname == "" || utf8.RuneCountInString(nickname) > 64 {
		reject(c, http.StatusBadRequest, "invalid_registration")
		return
	}
	userID, err := h.users.Register(c.Request.Context(), form.Username, *form.Password, nickname)
	if errors.Is(err, user.ErrUsernameTaken) {
		reject(c, http.StatusConflict, "username_taken")
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user_id": strconv.FormatInt(userID, 10)})
}

func (h *handler) login(c *gin.Context) {
	var form credentials
	if err := c.ShouldBindJSON(&form); err != nil || !form.normalize() {
		reject(c, http.StatusBadRequest, "invalid_login")
		return
	}
	// 先验证数据库里的密码哈希，再签发带用户 ID 的访问凭证。
	userID, err := h.users.Login(c.Request.Context(), form.Username, *form.Password)
	if errors.Is(err, user.ErrInvalidCredentials) {
		reject(c, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	subject := strconv.FormatInt(userID, 10)
	now := time.Now()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(accessTTL)),
	}).SignedString(h.jwtSecret)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"user_id":      subject,
		"access_token": token,
		"expires_in":   int(accessTTL / time.Second),
	})
}

func (h *handler) requireBearer(c *gin.Context) {
	scheme, value, _ := strings.Cut(c.GetHeader("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || value == "" {
		reject(c, http.StatusUnauthorized, "invalid_token")
		return
	}
	claims := jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(value, &claims, func(*jwt.Token) (any, error) {
		return h.jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		reject(c, http.StatusUnauthorized, "invalid_token")
		return
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || userID <= 0 {
		reject(c, http.StatusUnauthorized, "invalid_token")
		return
	}
	c.Set(principalKey, principal{userID: userID, expiresAt: claims.ExpiresAt.Time})
	c.Next()
}

func authenticated(c *gin.Context) principal {
	return c.MustGet(principalKey).(principal)
}
