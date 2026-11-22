package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/user"
)

type loginResponse struct {
	ID        string `json:"user_id"`
	Token     string `json:"access_token"`
	ExpiresIn int    `json:"expires_in"`
}

type testAccount struct {
	loginResponse
	Username string
}

type ticketResponse struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

type messageEvent struct {
	Type string          `json:"type"`
	Data messageResponse `json:"data"`
}

type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

var testSecret = []byte("test-only-jwt-key-with-at-least-32-bytes")

func TestHealthResponse(t *testing.T) {
	hub := realtime.NewHub()
	server := testServer(t, NewRouter(nil, nil, nil, hub, testSecret, nil, nil), hub)
	var health apiResponse
	request(t, server, http.MethodGet, "/healthz", "", nil, http.StatusOK, &health)
	if health.Code != codeSuccess || health.Msg != "success" || string(health.Data) != `{"status":"ok"}` {
		t.Fatalf("health response = %+v", health)
	}
}

func TestCanceledRequestDoesNotWriteErrorResponse(t *testing.T) {
	router := gin.New()
	router.GET("/cancel", func(c *gin.Context) {
		internalError(c, context.Canceled)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/cancel", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)
	if recorder.Body.Len() != 0 {
		t.Fatalf("canceled request wrote a response: %s", recorder.Body.String())
	}
}

func TestDirectChatMySQL(t *testing.T) {
	dsn := os.Getenv("IM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set IM_TEST_MYSQL_DSN to a migrated test database")
	}
	db, err := mysql.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub()
	server := testServer(t, NewRouter(user.NewStore(db), chat.NewService(chat.NewStore(db)), friendship.NewService(friendship.NewStore(db)), hub, testSecret, nil, nil), hub)
	alice := registerAndLogin(t, server, "alice", "")
	bob := registerAndLogin(t, server, "bob", "?")
	outsider := registerAndLogin(t, server, "outsider", "  "+strings.Repeat("你好!", 25)+"\n\t ")
	// 空字符串是有效密码，省略字段或传 null 仍属于无效请求。
	request(t, server, http.MethodPost, "/api/v1/auth/register", "",
		map[string]string{"username": "missing_password", "nickname": "missing"}, http.StatusBadRequest, nil)
	request(t, server, http.MethodPost, "/api/v1/auth/login", "",
		map[string]any{"username": "missing_password", "password": nil}, http.StatusBadRequest, nil)

	var conversation struct {
		ID string `json:"id"`
	}
	directPath := "/api/v1/conversations/direct"
	request(t, server, http.MethodPost, directPath, alice.Token,
		map[string]string{"peer_username": bob.Username}, http.StatusForbidden, nil)
	befriend(t, server, alice, bob)
	request(t, server, http.MethodPost, directPath, alice.Token,
		map[string]string{"peer_username": " " + strings.ToUpper(bob.Username) + " "}, http.StatusOK, &conversation)
	var reverse struct {
		ID string `json:"id"`
	}
	request(t, server, http.MethodPost, directPath, bob.Token,
		map[string]string{"peer_username": alice.Username}, http.StatusOK, &reverse)
	if conversation.ID == "" || reverse.ID != conversation.ID {
		t.Fatalf("unexpected direct conversation IDs: %q, %q", conversation.ID, reverse.ID)
	}
	request(t, server, http.MethodPost, directPath, alice.Token,
		map[string]string{"peer_username": alice.Username}, http.StatusBadRequest, nil)
	request(t, server, http.MethodPost, directPath, alice.Token,
		map[string]string{"peer_username": "#" + bob.Username}, http.StatusBadRequest, nil)
	request(t, server, http.MethodPost, directPath, alice.Token,
		map[string]string{"peer_username": bob.Username + "_missing"}, http.StatusNotFound, nil)
	var summaries []conversationResponse
	request(t, server, http.MethodGet, "/api/v1/conversations", bob.Token, nil, http.StatusOK, &summaries)
	if len(summaries) != 1 || fmt.Sprint(summaries[0].ID) != conversation.ID ||
		fmt.Sprint(summaries[0].PeerUserID) != alice.ID || summaries[0].PeerNickname != "alice" ||
		!summaries[0].IsFriend || summaries[0].LastMessage != "" {
		t.Fatalf("unexpected conversations: %+v", summaries)
	}
	path := "/api/v1/conversations/" + conversation.ID + "/messages"
	request(t, server, http.MethodGet, path, outsider.Token, nil, http.StatusForbidden, nil)
	request(t, server, http.MethodGet, path+"?before_seq=1&after_seq=0", bob.Token, nil, http.StatusBadRequest, nil)
	request(t, server, http.MethodGet, path+"?limit=101", bob.Token, nil, http.StatusBadRequest, nil)

	conn, events := openDevice(t, server, bob.Token)
	firstDraft := map[string]string{"client_msg_id": "7a4ac694-2a74-4aa6-9611-503ffafc2b15", "content": "你好！", "sender_id": bob.ID}
	request(t, server, http.MethodPost, path, outsider.Token, firstDraft, http.StatusForbidden, nil)
	var first, repeated messageResponse
	request(t, server, http.MethodPost, path, alice.Token, firstDraft, http.StatusOK, &first)
	if first.ID == 0 || first.Seq != 1 || fmt.Sprint(first.SenderID) != alice.ID || first.Content != "你好！" {
		t.Fatalf("unexpected saved message: %+v", first)
	}
	assertPush(t, events, first)
	request(t, server, http.MethodPost, path, alice.Token, firstDraft, http.StatusOK, &repeated)
	if repeated != first {
		t.Fatalf("retry returned %+v, want %+v", repeated, first)
	}
	second := send(t, server, path, alice.Token, 2)
	// 下一个推送必须是第二条新消息，而不是第一条的重试。
	assertPush(t, events, second)

	conn.Close()
	third, fourth := send(t, server, path, alice.Token, 3), send(t, server, path, alice.Token, 4)
	_, events = openDevice(t, server, bob.Token)
	var missed []messageResponse
	request(t, server, http.MethodGet, path+"?after_seq=2&limit=1", bob.Token, nil, http.StatusOK, &missed)
	if len(missed) != 1 || missed[0] != third || third.Seq != 3 {
		t.Fatalf("unexpected first page of missed messages: %+v", missed)
	}
	request(t, server, http.MethodGet, path+"?after_seq=3", bob.Token, nil, http.StatusOK, &missed)
	if len(missed) != 1 || missed[0] != fourth || fourth.Seq != 4 {
		t.Fatalf("unexpected second page of missed messages: %+v", missed)
	}
	request(t, server, http.MethodGet, path+"?after_seq=0", bob.Token, nil, http.StatusOK, &missed)
	if len(missed) != 4 || missed[0] != first || missed[3] != fourth {
		t.Fatalf("unexpected sync from 0: %+v", missed)
	}
	request(t, server, http.MethodGet, path+"?before_seq=4&limit=2", bob.Token, nil, http.StatusOK, &missed)
	if len(missed) != 2 || missed[0] != third || missed[1] != second {
		t.Fatalf("unexpected history page: %+v", missed)
	}
	fifth := send(t, server, path, alice.Token, 5)
	assertPush(t, events, fifth)
	request(t, server, http.MethodGet, "/api/v1/conversations", bob.Token, nil, http.StatusOK, &summaries)
	if len(summaries) != 1 || summaries[0].LastSeq != fifth.Seq || summaries[0].LastMessage != fifth.Content {
		t.Fatalf("unexpected latest message in summaries: %+v", summaries)
	}
	assertReadProgress(t, server, alice.Token, 0, 0)
	assertReadProgress(t, server, bob.Token, 0, 5)
	readPath := "/api/v1/conversations/" + conversation.ID + "/read"
	var read apiResponse
	request(t, server, http.MethodPost, readPath, bob.Token, map[string]string{"seq": "3"}, http.StatusOK, &read)
	if string(read.Data) != "null" {
		t.Fatalf("mark read response data = %s, want null", read.Data)
	}
	assertReadProgress(t, server, bob.Token, 3, 2)
	for _, seq := range []string{"3", "1"} {
		request(t, server, http.MethodPost, readPath, bob.Token, map[string]string{"seq": seq}, http.StatusOK, nil)
	}
	request(t, server, http.MethodPost, readPath, bob.Token, map[string]string{"seq": "6"}, http.StatusBadRequest, &read)
	if read.Code != codeInvalidRequest {
		t.Fatalf("out-of-range read code = %d, want %d", read.Code, codeInvalidRequest)
	}
	request(t, server, http.MethodPost, readPath, outsider.Token, map[string]string{"seq": "5"}, http.StatusForbidden, &read)
	if read.Code != codeNotConversationUser {
		t.Fatalf("non-member read code = %d, want %d", read.Code, codeNotConversationUser)
	}
	assertReadProgress(t, server, bob.Token, 3, 2)
	assertReadProgress(t, server, alice.Token, 0, 0)

	request(t, server, http.MethodDelete, "/api/v1/friends/"+alice.ID, bob.Token, nil, http.StatusOK, nil)
	request(t, server, http.MethodPost, directPath, alice.Token,
		map[string]string{"peer_username": bob.Username}, http.StatusForbidden, nil)
	for _, account := range []testAccount{alice, bob} {
		request(t, server, http.MethodGet, "/api/v1/conversations", account.Token, nil, http.StatusOK, &summaries)
		if len(summaries) != 1 || fmt.Sprint(summaries[0].ID) != conversation.ID || summaries[0].IsFriend {
			t.Fatalf("history conversation should remain after removal: %+v", summaries)
		}
		request(t, server, http.MethodPost, path, account.Token, map[string]string{
			"client_msg_id": "7a4ac694-2a74-4aa6-9611-503ffafc2b19", "content": "after removal",
		}, http.StatusForbidden, nil)
		request(t, server, http.MethodGet, path, account.Token, nil, http.StatusOK, &missed)
		if len(missed) != 5 || missed[4] != first {
			t.Fatalf("unexpected history after removal: %+v", missed)
		}
	}
	request(t, server, http.MethodPost, path, alice.Token, firstDraft, http.StatusOK, &repeated)
	if repeated != first {
		t.Fatalf("retry after removal returned %+v, want %+v", repeated, first)
	}
	befriend(t, server, bob, alice)
	request(t, server, http.MethodPost, directPath, bob.Token,
		map[string]string{"peer_username": alice.Username}, http.StatusOK, &reverse)
	if reverse.ID != conversation.ID {
		t.Fatalf("refriended conversation = %s, want %s", reverse.ID, conversation.ID)
	}
	assertPush(t, events, send(t, server, path, alice.Token, 6))
	send(t, server, path, bob.Token, 7)
	assertReadProgress(t, server, alice.Token, 0, 1)
	assertReadProgress(t, server, bob.Token, 3, 3)
	request(t, server, http.MethodPost, readPath, bob.Token, map[string]string{"seq": "7"}, http.StatusOK, nil)
	assertReadProgress(t, server, bob.Token, 7, 0)
}

func assertReadProgress(t *testing.T, server *httptest.Server, token string, seq, unread int64) {
	t.Helper()
	var conversations []conversationResponse
	request(t, server, http.MethodGet, "/api/v1/conversations", token, nil, http.StatusOK, &conversations)
	if len(conversations) != 1 || conversations[0].LastReadSeq != seq || conversations[0].UnreadCount != unread {
		t.Fatalf("conversations = %+v, want one conversation with read seq %d and unread count %d", conversations, seq, unread)
	}
}

func befriend(t *testing.T, server *httptest.Server, from, to testAccount) {
	t.Helper()
	var entry friendRequestResponse
	request(t, server, http.MethodPost, "/api/v1/friend-requests", from.Token,
		map[string]string{"peer_username": to.Username}, http.StatusOK, &entry)
	request(t, server, http.MethodPatch, "/api/v1/friend-requests/"+entry.ID, to.Token,
		map[string]string{"status": "accepted"}, http.StatusOK, nil)
}

func TestWebSocketTickets(t *testing.T) {
	hub := realtime.NewHub()
	server := testServer(t, NewRouter(nil, nil, nil, hub, testSecret, nil, nil), hub)
	const ticketPath = "/api/v1/ws/tickets"
	request(t, server, http.MethodPost, ticketPath, "", nil, http.StatusUnauthorized, nil)
	request(t, server, http.MethodPost, ticketPath, signedToken(t, jwt.SigningMethodHS512, time.Now().Add(time.Hour)), nil, http.StatusUnauthorized, nil)
	request(t, server, http.MethodPost, ticketPath, signedToken(t, jwt.SigningMethodHS256, time.Now().Add(-time.Minute)), nil, http.StatusUnauthorized, nil)

	longToken := signedToken(t, jwt.SigningMethodHS256, time.Now().Add(time.Hour))
	var ticket ticketResponse
	request(t, server, http.MethodPost, ticketPath, longToken, nil, http.StatusOK, &ticket)
	if ticket.Ticket == "" || time.Until(ticket.ExpiresAt) > ticketTTL || !time.Now().Before(ticket.ExpiresAt) {
		t.Fatalf("unexpected ticket expiry: %v", ticket.ExpiresAt)
	}
	conn, response, err := dial(server, ticket.Ticket, server.URL)
	if err != nil {
		t.Fatalf("ticket handshake failed: %v", err)
	}
	defer conn.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected WS upgrade, got %d", response.StatusCode)
	}
	assertRejectedTicket(t, server, ticket.Ticket, server.URL, http.StatusUnauthorized)
	assertRejectedTicket(t, server, longToken, server.URL, http.StatusUnauthorized)

	request(t, server, http.MethodPost, ticketPath, longToken, nil, http.StatusOK, &ticket)
	assertRejectedTicket(t, server, ticket.Ticket, "https://other.example", http.StatusForbidden)
	assertRejectedTicket(t, server, ticket.Ticket, server.URL, http.StatusUnauthorized)

	expires := time.Now().Truncate(time.Second).Add(2 * time.Second)
	request(t, server, http.MethodPost, ticketPath, signedToken(t, jwt.SigningMethodHS256, expires), nil, http.StatusOK, &ticket)
	if !ticket.ExpiresAt.Equal(expires) {
		t.Fatalf("ticket expires at %v, want access token expiry %v", ticket.ExpiresAt, expires)
	}
	time.Sleep(time.Until(expires))
	assertRejectedTicket(t, server, ticket.Ticket, server.URL, http.StatusUnauthorized)
}

func testServer(t *testing.T, handler http.Handler, hub *realtime.Hub) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	server.Client().Timeout = 5 * time.Second
	t.Cleanup(server.Close)
	t.Cleanup(hub.Close)
	return server
}

func registerAndLogin(t *testing.T, server *httptest.Server, name, password string) testAccount {
	t.Helper()
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("%s_%x", name, nonce)
	var registered, login loginResponse
	form := map[string]string{"username": strings.ToUpper(username), "password": password, "nickname": name}
	request(t, server, http.MethodPost, "/api/v1/auth/register", "", form, http.StatusCreated, &registered)
	request(t, server, http.MethodPost, "/api/v1/auth/register", "", form, http.StatusConflict, nil)
	request(t, server, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": username, "password": password + "x"}, http.StatusUnauthorized, nil)
	request(t, server, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": username, "password": password}, http.StatusOK, &login)
	if login.ID == "" || login.ID != registered.ID || login.Token == "" || login.ExpiresIn != 3600 {
		t.Fatal("invalid login response")
	}
	return testAccount{loginResponse: login, Username: username}
}

func send(t *testing.T, server *httptest.Server, path, token string, number int) messageResponse {
	t.Helper()
	var message messageResponse
	request(t, server, http.MethodPost, path, token, map[string]string{
		"client_msg_id": fmt.Sprintf("7a4ac694-2a74-4aa6-9611-%012x", number), "content": fmt.Sprintf("message %d", number),
	}, http.StatusOK, &message)
	return message
}

func request(t *testing.T, server *httptest.Server, method, path, token string, body any, status int, out any) {
	t.Helper()
	var input io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, server.URL+path, input)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d, want 200; body %s", method, path, resp.StatusCode, payload)
	}
	var envelope apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	wantSuccess := status == http.StatusOK || status == http.StatusCreated
	if wantSuccess && envelope.Code != codeSuccess {
		t.Fatalf("%s %s: code %d, want success; msg %s", method, path, envelope.Code, envelope.Msg)
	}
	if !wantSuccess && envelope.Code == codeSuccess {
		t.Fatalf("%s %s: code %d, want business error; msg %s", method, path, envelope.Code, envelope.Msg)
	}
	if out == nil {
		return
	}
	if full, ok := out.(*apiResponse); ok {
		*full = envelope
		return
	}
	if string(envelope.Data) == "null" {
		return
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		t.Fatal(err)
	}
}

func dial(server *httptest.Server, ticket, origin string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	return dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?ticket="+ticket, http.Header{"Origin": {origin}})
}

func openDevice(t *testing.T, server *httptest.Server, token string) (*websocket.Conn, <-chan messageEvent) {
	t.Helper()
	var ticket ticketResponse
	request(t, server, http.MethodPost, "/api/v1/ws/tickets", token, nil, http.StatusOK, &ticket)
	conn, resp, err := dial(server, ticket.Ticket, server.URL)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("WS dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	ready := make(chan struct{}, 1)
	conn.SetPongHandler(func(string) error {
		ready <- struct{}{}
		return nil
	})
	events := make(chan messageEvent, 8)
	go func() {
		defer close(events)
		for {
			var event messageEvent
			if err := conn.ReadJSON(&event); err != nil {
				return
			}
			events <- event
		}
	}()
	// 101 返回早于 Hub 登记；等真实 Ping/Pong 确认服务端开始读循环再发消息。
	if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("WS Pong not received")
	}
	return conn, events
}

func assertPush(t *testing.T, events <-chan messageEvent, want messageResponse) {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != "message.created" || event.Data != want {
			t.Fatalf("event = %+v, want message %+v", event, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no push for message %d", want.ID)
	}
}

func signedToken(t *testing.T, method jwt.SigningMethod, expires time.Time) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, jwt.RegisteredClaims{Subject: "42", ExpiresAt: jwt.NewNumericDate(expires)}).SignedString(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func assertRejectedTicket(t *testing.T, server *httptest.Server, ticket, origin string, status int) {
	t.Helper()
	conn, response, err := dial(server, ticket, origin)
	if conn != nil {
		conn.Close()
	}
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != status {
		t.Fatalf("WS handshake = %v, err = %v; want %d", response, err, status)
	}
}
