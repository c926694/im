package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

func (h *handler) pointBalance(c *gin.Context) {
	balance, err := h.users.PointBalance(c.Request.Context(), authenticated(c).userID)
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
	entries, err := h.users.PointLedger(c.Request.Context(), authenticated(c).userID, beforeID, limit)
	if err != nil {
		internalError(c, err)
		return
	}
	respondSuccess(c, entries)
}
