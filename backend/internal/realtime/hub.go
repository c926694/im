package realtime

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/c926694/im/backend/internal/chat"
)

const (
	sendQueueSize = 64
	readLimit     = 4096
)

var upgrader websocket.Upgrader // 使用 Gorilla 默认的同源检查，不对 Origin 放行。

type MessageCreated struct {
	Kind            chat.MessageKind `json:"kind"`
	RedPacketID     *int64           `json:"red_packet_id,string,omitempty"`
	RedPacketStatus string           `json:"red_packet_status,omitempty"`
	ID              int64            `json:"id,string"`
	ConversationID  int64            `json:"conversation_id,string"`
	SenderID        int64            `json:"sender_id,string"`
	SenderNickname  string           `json:"sender_nickname"`
	ClientMsgID     string           `json:"client_msg_id"`
	Seq             int64            `json:"seq,string"`
	Content         string           `json:"content"`
	CreatedAt       time.Time        `json:"created_at"`
}

// NewMessageCreated 为 HTTP 和实时推送生成同一种消息格式。
func NewMessageCreated(message chat.Message, nickname string) MessageCreated {
	return MessageCreated{ID: message.ID, ConversationID: message.ConversationID, SenderID: message.SenderID,
		SenderNickname: nickname, ClientMsgID: message.ClientMsgID, Seq: message.Seq, Content: message.Content,
		CreatedAt: message.CreatedAt, Kind: message.Kind, RedPacketID: message.RedPacketID}
}

type PacketUpdated struct {
	PacketID int64  `json:"red_packet_id,string"`
	Status   string `json:"status"`
}

type ConversationUpdated struct {
	ConversationID int64 `json:"conversation_id,string"`
}

type Hub struct {
	mu           sync.RWMutex
	users        map[int64]map[*client]struct{}
	pingInterval time.Duration
	readTimeout  time.Duration
	writeTimeout time.Duration
}

func NewHub() *Hub {
	return &Hub{
		users:        make(map[int64]map[*client]struct{}),
		pingInterval: 30 * time.Second,
		readTimeout:  60 * time.Second,
		writeTimeout: 5 * time.Second,
	}
}

// Serve 只接受上层鉴权后的用户 ID；不能直接用请求参数作为身份。
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID int64) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetReadLimit(readLimit)
	if err := conn.SetReadDeadline(time.Now().Add(h.readTimeout)); err != nil {
		return
	}
	// 只有 Pong 会续期，客户端发来的业务帧不作为心跳。
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(h.readTimeout))
	})

	c := &client{conn: conn, send: make(chan []byte, sendQueueSize), done: make(chan struct{})}
	if !h.register(userID, c) {
		return
	}

	// 业务推送和 Ping 都由同一个写循环发送；读循环负责处理控制帧。
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer c.stop()
		c.writeLoop(h.pingInterval, h.writeTimeout)
	}()
	for {
		if _, _, err := conn.NextReader(); err != nil {
			break
		}
	}

	// 只注销这条连接；关闭 socket 同时唤醒可能正在等待的写循环。
	h.unregister(userID, c)
	c.stop()
	<-writerDone
}

// Push 只尝试通知在线连接，不代表客户端已收到；应在消息落库后调用。
func (h *Hub) Push(userID int64, event MessageCreated) error {
	return h.PushMessages([]int64{userID}, event)
}

// PushMessages 将同一条已保存消息通知给会话中的多个用户。
func (h *Hub) PushMessages(userIDs []int64, event MessageCreated) error {
	return h.push(userIDs, "message.created", event)
}

// PushConversationUpdated 提醒客户端重新查询会话，事件只携带会话 ID。
func (h *Hub) PushConversationUpdated(userIDs []int64, event ConversationUpdated) error {
	return h.push(userIDs, "conversation.updated", event)
}

// PushPacketUpdated 通知在线客户端重新查询红包状态。
func (h *Hub) PushPacketUpdated(userIDs []int64, event PacketUpdated) error {
	return h.push(userIDs, "red_packet.updated", event)
}

func (h *Hub) push(userIDs []int64, eventType string, event any) error {
	payload, err := json.Marshal(struct {
		Type string `json:"type"`
		Data any    `json:"data"`
	}{Type: eventType, Data: event})
	if err != nil {
		return err
	}

	var slow []*client
	h.mu.RLock()
	for _, userID := range userIDs {
		for c := range h.users[userID] {
			select {
			case <-c.done:
			case c.send <- payload:
			default:
				slow = append(slow, c)
			}
		}
	}
	h.mu.RUnlock()

	// 先通知其他设备，再关闭积压连接；不持有登记表锁去关闭 socket。
	for _, c := range slow {
		c.stop()
	}
	return nil
}

// Close 需要在服务退出时调用：WebSocket 升级后的连接不由 HTTP Shutdown 关闭。
func (h *Hub) Close() {
	h.mu.Lock()
	users := h.users
	h.users = nil
	h.mu.Unlock()

	for _, connections := range users {
		for c := range connections {
			c.stop()
		}
	}
}

func (h *Hub) register(userID int64, c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.users == nil {
		return false
	}
	if h.users[userID] == nil {
		h.users[userID] = make(map[*client]struct{})
	}
	h.users[userID][c] = struct{}{}
	return true
}

func (h *Hub) unregister(userID int64, c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.users[userID], c)
	if len(h.users[userID]) == 0 {
		delete(h.users, userID)
	}
}
