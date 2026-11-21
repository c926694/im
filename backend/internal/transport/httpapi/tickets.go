package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const ticketTTL = 30 * time.Second

type ticketStore struct {
	mu      sync.Mutex
	pending map[string]principal
}

func (s *ticketStore) reserve(value string, ticket principal, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[string]principal)
	}
	// 签发时顺带清理未使用的过期票据，首版不启动后台任务。
	for value, pending := range s.pending {
		if !now.Before(pending.expiresAt) {
			delete(s.pending, value)
		}
	}
	s.pending[value] = ticket
}

func (s *ticketStore) consume(value string, now time.Time) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.pending[value]
	delete(s.pending, value)
	return ticket.userID, ok && now.Before(ticket.expiresAt)
}

func (h *handler) issueWSTicket(c *gin.Context) {
	identity := authenticated(c)
	now := time.Now().UTC()
	expiresAt := now.Add(ticketTTL)
	if identity.expiresAt.Before(expiresAt) {
		expiresAt = identity.expiresAt.UTC()
	}
	if !now.Before(expiresAt) {
		reject(c, "invalid_token")
		return
	}

	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		internalError(c, err)
		return
	}
	value := base64.RawURLEncoding.EncodeToString(random[:])
	// 票据只继承登录身份，不接受客户端传入用户 ID。
	h.tickets.reserve(value, principal{userID: identity.userID, expiresAt: expiresAt}, now)
	c.Header("Cache-Control", "no-store")
	respondSuccess(c, gin.H{"ticket": value, "expires_at": expiresAt})
}

func (h *handler) openWebSocket(c *gin.Context) {
	// 查找和删除在同一把锁内；即使后续升级失败，同一票据也不能重用。
	userID, ok := h.tickets.consume(c.Query("ticket"), time.Now())
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	h.hub.Serve(c.Writer, c.Request, userID)
}
