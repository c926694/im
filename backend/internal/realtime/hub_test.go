package realtime

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var sampleMessage = MessageCreated{
	ID: 9007199254740993, ConversationID: 42, SenderID: 1,
	ClientMsgID: "7a4ac694-2a74-4aa6-9611-503ffafc2b15", Seq: 3,
	Content: "你好", CreatedAt: time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
}

func TestPushToUserDevices(t *testing.T) {
	hub := NewHub()
	server := startTestServer(t, hub)
	phone := dialDevice(t, server, "/alice")
	web := dialDevice(t, server, "/alice")
	otherUser := dialDevice(t, server, "/bob")
	waitFor(t, "all devices registered", func() bool {
		return len(userConnections(hub, 1)) == 2 && len(userConnections(hub, 2)) == 1
	})

	push(t, hub, 1, sampleMessage)
	for _, conn := range []*websocket.Conn{phone, web} {
		var event struct {
			Type string         `json:"type"`
			Data MessageCreated `json:"data"`
		}
		payload := readEvent(t, conn)
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "message.created" || event.Data != sampleMessage {
			t.Fatalf("unexpected event: %s", payload)
		}
	}
	marker := sampleMessage
	marker.Content = "给另一位用户"
	push(t, hub, 2, marker)
	if payload := readEvent(t, otherUser); !strings.Contains(string(payload), marker.Content) {
		t.Fatalf("other user received an event not addressed to them: %s", payload)
	}

	phone.Close()
	waitFor(t, "only one of Alice's devices removed", func() bool { return len(userConnections(hub, 1)) == 1 })
	push(t, hub, 1, sampleMessage)
	readEvent(t, web)
	push(t, hub, 3, sampleMessage)
}

func TestHeartbeat(t *testing.T) {
	hub := NewHub()
	hub.pingInterval = 25 * time.Millisecond
	hub.readTimeout = 300 * time.Millisecond
	server := startTestServer(t, hub)
	responsive := dialDevice(t, server, "/alice")
	result := make(chan error, 1)
	go func() {
		// Gorilla 在读循环内处理 Ping，并发送默认的 Pong。
		_, _, err := responsive.ReadMessage()
		result <- err
	}()
	waitFor(t, "responsive device registered", func() bool { return len(userConnections(hub, 1)) == 1 })
	dialDevice(t, server, "/bob") // 不读取 Ping，因而不会回复 Pong。
	waitFor(t, "silent device registered", func() bool { return len(userConnections(hub, 2)) == 1 })
	waitFor(t, "silent device timed out", func() bool { return len(userConnections(hub, 2)) == 0 })
	push(t, hub, 1, sampleMessage)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Pong did not keep the responsive connection alive: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("responsive device did not receive the push")
	}
}

func TestSlowDeviceDoesNotBlockOtherDevice(t *testing.T) {
	hub := NewHub()
	hub.writeTimeout = 15 * time.Second
	server := startTestServer(t, hub)
	slow := dialDevice(t, server, "/alice")
	if err := slow.UnderlyingConn().(*net.TCPConn).SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slow device registered", func() bool { return len(userConnections(hub, 1)) == 1 })
	slowServer := userConnections(hub, 1)[0]
	if err := slowServer.conn.UnderlyingConn().(*net.TCPConn).SetWriteBuffer(1024); err != nil {
		t.Fatal(err)
	}
	// 用真实 TCP 写背压占住写循环，不靠 sleep 等待队列积压。
	large := sampleMessage
	large.Content = strings.Repeat("x", 4<<20)
	push(t, hub, 1, large)
	waitFor(t, "writer dequeued the oversized test event", func() bool { return len(slowServer.send) == 0 })
	fast := dialDevice(t, server, "/alice")
	waitFor(t, "fast device registered", func() bool { return len(userConnections(hub, 1)) == 2 })

	for i := 0; i < 64; i++ {
		push(t, hub, 1, sampleMessage)
		readEvent(t, fast)
	}
	if pending := len(slowServer.send); pending != 64 {
		t.Fatalf("slow device queue length = %d, want 64", pending)
	}
	push(t, hub, 1, sampleMessage)
	readEvent(t, fast)
	waitFor(t, "only the slow device disconnected", func() bool {
		connections := userConnections(hub, 1)
		return len(connections) == 1 && connections[0] != slowServer
	})
}

func TestOriginAndShutdown(t *testing.T) {
	hub := NewHub()
	server := startTestServer(t, hub)
	conn, response, err := websocket.DefaultDialer.Dial(wsURL(server, "/alice"), http.Header{"Origin": {"https://other.example"}})
	if conn != nil {
		conn.Close()
	}
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin handshake = %v, err = %v; want 403", response, err)
	}

	phone := dialDevice(t, server, "/alice")
	web := dialDevice(t, server, "/alice")
	waitFor(t, "both devices registered before shutdown", func() bool { return len(userConnections(hub, 1)) == 2 })
	hub.Close()
	for _, conn := range []*websocket.Conn{phone, web} {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := conn.ReadMessage(); err == nil {
			t.Fatal("connection was not closed")
		} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatalf("reading from an open connection timed out after shutdown: %v", err)
		}
	}
}

func startTestServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/alice", func(w http.ResponseWriter, r *http.Request) { hub.Serve(w, r, 1) })
	mux.HandleFunc("/bob", func(w http.ResponseWriter, r *http.Request) { hub.Serve(w, r, 2) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Cleanup(hub.Close)
	return server
}

func wsURL(server *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + path
}

func dialDevice(t *testing.T, server *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	conn, response, err := websocket.DefaultDialer.Dial(wsURL(server, path), http.Header{"Origin": {server.URL}})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func push(t *testing.T, hub *Hub, userID int64, event MessageCreated) {
	t.Helper()
	if err := hub.Push(userID, event); err != nil {
		t.Fatal(err)
	}
}

func readEvent(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := conn.ReadMessage()
	if err != nil || kind != websocket.TextMessage {
		t.Fatalf("read event: frame type = %d, err = %v", kind, err)
	}
	return payload
}

func waitFor(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func userConnections(hub *Hub, userID int64) []*client {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	var clients []*client
	for c := range hub.users[userID] {
		clients = append(clients, c)
	}
	return clients
}
