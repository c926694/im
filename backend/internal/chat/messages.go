package chat

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidClientMsgID = errors.New("client message ID must be a UUID")
	ErrInvalidContent     = errors.New("message must contain non-blank UTF-8 text of at most 2000 characters")
	ErrInvalidPageSize    = errors.New("message page size must be between 1 and 100")
)

type MessageDraft struct {
	clientMsgID string
	content     string
}

func NewMessageDraft(clientMsgID, content string) (MessageDraft, error) {
	if len(clientMsgID) != 36 || clientMsgID[8] != '-' || clientMsgID[13] != '-' ||
		clientMsgID[18] != '-' || clientMsgID[23] != '-' {
		return MessageDraft{}, ErrInvalidClientMsgID
	}
	if decoded, err := hex.DecodeString(strings.ReplaceAll(clientMsgID, "-", "")); err != nil || len(decoded) != 16 {
		return MessageDraft{}, ErrInvalidClientMsgID
	}
	if !utf8.ValidString(content) || strings.TrimSpace(content) == "" || utf8.RuneCountInString(content) > 2000 {
		return MessageDraft{}, ErrInvalidContent
	}
	return MessageDraft{clientMsgID: strings.ToLower(clientMsgID), content: content}, nil
}

type Message struct {
	ID             int64     `db:"id"`
	ConversationID int64     `db:"conversation_id"`
	SenderID       int64     `db:"sender_id"`
	ClientMsgID    string    `db:"client_msg_id"`
	Seq            int64     `db:"seq"`
	Content        string    `db:"content"`
	CreatedAt      time.Time `db:"created_at"`
}

const selectMessage = `SELECT id, conversation_id, sender_id, client_msg_id, seq, content, created_at FROM messages`

type SendResult struct {
	Message     Message
	RecipientID int64
	Created     bool
}

func (s *Store) SendMessage(ctx context.Context, userID, conversationID int64, draft MessageDraft) (SendResult, error) {
	// 先确认当前用户是会话成员，防止非成员向别人的会话发消息。
	if err := s.requireMember(ctx, userID, conversationID); err != nil {
		return SendResult{}, err
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return SendResult{}, err
	}
	defer tx.Rollback()

	// 锁住会话行，让同一会话的并发发送依次分配序号。
	var conversation struct {
		LastSeq int64 `db:"last_seq"`
		LowID   int64 `db:"direct_user_low_id"`
		HighID  int64 `db:"direct_user_high_id"`
	}
	if err := tx.GetContext(ctx, &conversation, `
		SELECT last_seq, direct_user_low_id, direct_user_high_id FROM conversations WHERE id = ? FOR UPDATE`, conversationID); err != nil {
		return SendResult{}, err
	}
	recipientID := conversation.LowID
	if userID == conversation.LowID {
		recipientID = conversation.HighID
	}

	// 分配序号前先查重，重试返回原消息，不占用新的序号。
	var message Message
	err = tx.GetContext(ctx, &message, selectMessage+`
		WHERE conversation_id = ? AND sender_id = ? AND client_msg_id = ?`, conversationID, userID, draft.clientMsgID)
	if err == nil {
		return SendResult{Message: message, RecipientID: recipientID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, err
	}

	message = Message{
		ConversationID: conversationID, SenderID: userID, ClientMsgID: draft.clientMsgID,
		Seq: conversation.LastSeq + 1, Content: draft.content, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO messages (conversation_id, sender_id, client_msg_id, seq, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, conversationID, userID, draft.clientMsgID, message.Seq, draft.content, message.CreatedAt)
	if err != nil {
		return SendResult{}, err
	}
	message.ID, err = result.LastInsertId()
	if err != nil {
		return SendResult{}, err
	}
	// 消息和会话游标在同一事务里更新，提交成功后才确认发送。
	_, err = tx.ExecContext(ctx, `
		UPDATE conversations SET last_seq = ?, last_activity_at = ? WHERE id = ?`,
		message.Seq, message.CreatedAt, conversationID)
	if err != nil {
		return SendResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SendResult{}, err
	}
	return SendResult{Message: message, RecipientID: recipientID, Created: true}, nil
}

// History 在 beforeSeq 为 0 时从最新消息开始查。
func (s *Store) History(ctx context.Context, userID, conversationID, beforeSeq int64, limit int) ([]Message, error) {
	query := selectMessage + ` WHERE conversation_id = ?`
	args := []any{conversationID}
	if beforeSeq != 0 {
		query += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	// 复用成员鉴权和分页逻辑；聊天记录从新到旧查。
	return s.listMessages(ctx, userID, conversationID, limit, query+` ORDER BY seq DESC LIMIT ?`, args)
}

func (s *Store) Sync(ctx context.Context, userID, conversationID, afterSeq int64, limit int) ([]Message, error) {
	// 从设备自己的同步游标向后补拉，按序号升序返回，不使用账号的已读游标。
	return s.listMessages(ctx, userID, conversationID, limit,
		selectMessage+` WHERE conversation_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		[]any{conversationID, afterSeq})
}

func (s *Store) listMessages(ctx context.Context, userID, conversationID int64, limit int, query string, args []any) ([]Message, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInvalidPageSize
	}
	// 历史查询和断线补拉统一在这里校验成员权限，再查消息。
	if err := s.requireMember(ctx, userID, conversationID); err != nil {
		return nil, err
	}

	messages := []Message{}
	if err := s.db.SelectContext(ctx, &messages, query, append(args, limit)...); err != nil {
		return nil, err
	}
	return messages, nil
}
