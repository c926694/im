package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c926694/im/backend/internal/realtime"
)

func TestRouteContract(t *testing.T) {
	hub := realtime.NewHub()
	t.Cleanup(hub.Close)
	router := NewRouter(nil, nil, nil, hub, testSecret, t.TempDir())
	routes := map[string]bool{
		"GET /avatars/:name":                         false,
		"GET /healthz":                               false,
		"GET /ws":                                    false,
		"POST /api/v1/auth/register":                 false,
		"POST /api/v1/auth/login":                    false,
		"GET /api/v1/me":                             true,
		"PUT /api/v1/me/avatar":                      true,
		"POST /api/v1/ws/tickets":                    true,
		"GET /api/v1/users/by-username":              true,
		"GET /api/v1/points/balance":                 true,
		"GET /api/v1/points/ledger":                  true,
		"GET /api/v1/conversations":                  true,
		"GET /api/v1/conversations/versions":         true,
		"POST /api/v1/conversations/direct":          true,
		"POST /api/v1/conversations/:id/messages":    true,
		"GET /api/v1/conversations/:id/messages":     true,
		"GET /api/v1/conversations/:id/sync-state":   true,
		"POST /api/v1/conversations/:id/read":        true,
		"GET /api/v1/messages/:id/image":             true,
		"GET /api/v1/friends":                        true,
		"DELETE /api/v1/friends/:id":                 true,
		"GET /api/v1/friend-requests":                true,
		"POST /api/v1/friend-requests":               true,
		"PUT /api/v1/friend-requests/:id":            true,
		"POST /api/v1/groups":                        true,
		"GET /api/v1/groups/:id":                     true,
		"PUT /api/v1/groups/:id":                     true,
		"POST /api/v1/groups/:id/members":            true,
		"DELETE /api/v1/groups/:id/members/:user_id": true,
		"PUT /api/v1/groups/:id/members/:user_id":    true,
		"POST /api/v1/groups/:id/transfer":           true,
		"POST /api/v1/groups/:id/leave":              true,
		"POST /api/v1/conversations/:id/red-packets": true,
		"GET /api/v1/red-packets/:id":                true,
		"POST /api/v1/red-packets/:id/claim":         true,
	}
	// Gin 关闭了尾部斜杠重定向，因此分组后的完整路径也需要核对。
	pathParams := strings.NewReplacer(":id", "1", ":user_id", "2")
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		requiresBearer, exists := routes[key]
		if !exists {
			t.Errorf("出现未预期的路由：%s", key)
			continue
		}
		delete(routes, key)
		if !requiresBearer {
			continue
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(route.Method, pathParams.Replace(route.Path), nil))
		var envelope apiResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil ||
			response.Code != http.StatusOK || envelope.Code != codeInvalidToken {
			t.Errorf("%s 未拒绝缺少 Bearer 的请求：HTTP %d，响应 %s", key, response.Code, response.Body.String())
		}
	}
	for missing := range routes {
		t.Errorf("缺少路由：%s", missing)
	}
}
