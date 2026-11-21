package chat

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
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

type directConversation struct {
	LastSeq int64 `db:"last_seq"`
	LowID   int64 `db:"direct_user_low_id"`
	HighID  int64 `db:"direct_user_high_id"`
}

func (s *Store) lockDirectConversation(ctx context.Context, tx *sqlx.Tx, id int64) (directConversation, error) {
	var conversation directConversation
	err := tx.GetContext(ctx, &conversation, `
		SELECT last_seq, direct_user_low_id, direct_user_high_id FROM conversations WHERE id = ? FOR UPDATE`, id)
	return conversation, err
}

func (s *Store) findSentMessage(ctx context.Context, tx *sqlx.Tx, conversationID, userID int64, clientMsgID string) (Message, error) {
	var message Message
	err := tx.GetContext(ctx, &message, selectMessage+`
		WHERE conversation_id = ? AND sender_id = ? AND client_msg_id = ?`, conversationID, userID, clientMsgID)
	return message, err
}

func (s *Store) insertMessage(ctx context.Context, tx *sqlx.Tx, message Message) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO messages (conversation_id, sender_id, client_msg_id, seq, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, message.ConversationID, message.SenderID, message.ClientMsgID, message.Seq, message.Content, message.CreatedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) updateLastMessage(ctx context.Context, tx *sqlx.Tx, message Message) error {
	_, err := tx.ExecContext(ctx, `UPDATE conversations SET last_seq = ?, last_activity_at = ? WHERE id = ?`,
		message.Seq, message.CreatedAt, message.ConversationID)
	return err
}

func (s *Store) history(ctx context.Context, conversationID, beforeSeq int64, limit int) ([]Message, error) {
	query := selectMessage + ` WHERE conversation_id = ?`
	args := []any{conversationID}
	if beforeSeq != 0 {
		query += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	return s.queryMessages(ctx, query+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
}

func (s *Store) sync(ctx context.Context, conversationID, afterSeq int64, limit int) ([]Message, error) {
	return s.queryMessages(ctx,
		selectMessage+` WHERE conversation_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		conversationID, afterSeq, limit)
}

func (s *Store) queryMessages(ctx context.Context, query string, args ...any) ([]Message, error) {
	messages := []Message{}
	if err := s.db.SelectContext(ctx, &messages, query, args...); err != nil {
		return nil, err
	}
	return messages, nil
}
