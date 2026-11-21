package chat

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/c926694/im/backend/internal/friendship"
	"github.com/jmoiron/sqlx"
)

var (
	ErrSelfConversation = errors.New("cannot open a direct conversation with yourself")
	ErrUserNotFound     = errors.New("a conversation participant does not exist")
	ErrNotMember        = errors.New("not a member of the conversation")
	ErrInvalidReadSeq   = errors.New("read sequence is outside the conversation")
)

// Store 汇总聊天业务需要的数据库操作，Service 通过它组合数据读写。
type Store interface {
	CountUsers(ctx context.Context, userID, peerID int64) (int, error)
	AreFriends(ctx context.Context, userID, peerID int64) (bool, error)
	AreFriendsTx(ctx context.Context, tx *sqlx.Tx, userID, peerID int64) (bool, error)
	UpsertDirect(ctx context.Context, low, high int64) (int64, error)
	ListConversations(ctx context.Context, userID int64) ([]ConversationSummary, error)
	IsMember(ctx context.Context, userID, conversationID int64) (bool, error)
	LastSequence(ctx context.Context, conversationID int64) (int64, error)
	AdvanceRead(ctx context.Context, userID, conversationID, seq int64) error
	BeginTx(ctx context.Context) (*sqlx.Tx, error)
	LockDirectConversation(ctx context.Context, tx *sqlx.Tx, id int64) (DirectConversation, error)
	FindSentMessage(ctx context.Context, tx *sqlx.Tx, conversationID, userID int64, clientMsgID string) (Message, error)
	InsertMessage(ctx context.Context, tx *sqlx.Tx, message Message) (int64, error)
	UpdateLastMessage(ctx context.Context, tx *sqlx.Tx, message Message) error
	History(ctx context.Context, conversationID, beforeSeq int64, limit int) ([]Message, error)
	Sync(ctx context.Context, conversationID, afterSeq int64, limit int) ([]Message, error)
}

type mysqlStore struct {
	db *sqlx.DB
}

var _ Store = (*mysqlStore)(nil)

// NewStore 创建 MySQL 数据访问对象，供聊天 Service 调用。
func NewStore(db *sqlx.DB) Store {
	return &mysqlStore{db: db}
}

type ConversationSummary struct {
	ID             int64 `db:"id"`
	PeerUserID     int64 `db:"peer_user_id"`
	LastSeq        int64 `db:"last_seq"`
	LastReadSeq    int64
	LastActivityAt time.Time `db:"last_activity_at"`
	UnreadCount    int64
}

// CountUsers 查询两位会话参与者对应的账号数量。
func (s *mysqlStore) CountUsers(ctx context.Context, userID, peerID int64) (int, error) {
	var users int
	err := s.db.GetContext(ctx, &users, `SELECT COUNT(*) FROM users WHERE id IN (?, ?)`, userID, peerID)
	return users, err
}

// AreFriends 查询当前数据库状态中的好友关系。
func (s *mysqlStore) AreFriends(ctx context.Context, userID, peerID int64) (bool, error) {
	return friendship.AreFriends(ctx, s.db, userID, peerID)
}

// AreFriendsTx 在发送事务中查询好友关系。
func (s *mysqlStore) AreFriendsTx(ctx context.Context, tx *sqlx.Tx, userID, peerID int64) (bool, error) {
	return friendship.AreFriends(ctx, tx, userID, peerID)
}

// UpsertDirect 创建单聊及成员记录，已有用户对则返回原会话。
func (s *mysqlStore) UpsertDirect(ctx context.Context, low, high int64) (int64, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 命中用户对的唯一约束时，LAST_INSERT_ID 返回已有会话 ID。
	result, err := tx.ExecContext(ctx, `
		INSERT INTO conversations (direct_user_low_id, direct_user_high_id)
		VALUES (?, ?)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`, low, high)
	if err != nil {
		return 0, err
	}
	conversationID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	// 成员与会话一起提交；重新打开已有会话时保留双方的已读进度。
	_, err = tx.ExecContext(ctx, `
		INSERT INTO conversation_members (conversation_id, user_id)
		VALUES (?, ?), (?, ?)
		ON DUPLICATE KEY UPDATE last_read_seq = conversation_members.last_read_seq`,
		conversationID, low, conversationID, high)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return conversationID, nil
}

// ListConversations 查询用户的会话摘要和个人已读游标。
func (s *mysqlStore) ListConversations(ctx context.Context, userID int64) ([]ConversationSummary, error) {
	// 先从成员表拿到会话 ID 和个人已读游标，再按 ID 批量查会话。
	var memberships []struct {
		ConversationID int64 `db:"conversation_id"`
		LastReadSeq    int64 `db:"last_read_seq"`
	}
	if err := s.db.SelectContext(ctx, &memberships, `
		SELECT conversation_id, last_read_seq FROM conversation_members WHERE user_id = ?`, userID); err != nil {
		return nil, err
	}
	conversations := []ConversationSummary{}
	if len(memberships) == 0 {
		return conversations, nil
	}

	ids := make([]int64, 0, len(memberships))
	readSeqs := make(map[int64]int64, len(memberships))
	for _, member := range memberships {
		ids = append(ids, member.ConversationID)
		readSeqs[member.ConversationID] = member.LastReadSeq
	}
	query, args, err := sqlx.In(`
		SELECT id, IF(direct_user_low_id = ?, direct_user_high_id, direct_user_low_id) AS peer_user_id,
		       last_seq, last_activity_at
		FROM conversations WHERE id IN (?) ORDER BY last_activity_at DESC, id DESC`, userID, ids)
	if err != nil {
		return nil, err
	}
	if err := s.db.SelectContext(ctx, &conversations, query, args...); err != nil {
		return nil, err
	}
	for i := range conversations {
		conversations[i].LastReadSeq = readSeqs[conversations[i].ID]
	}
	// 按各会话的已读游标批量统计未读，排除当前用户自己发的消息。
	if err := s.fillUnreadCounts(ctx, userID, conversations); err != nil {
		return nil, err
	}
	return conversations, nil
}

// fillUnreadCounts 按会话汇总游标之后由其他人发送的消息。
func (s *mysqlStore) fillUnreadCounts(ctx context.Context, userID int64, conversations []ConversationSummary) error {
	ranges := make([]string, 0, len(conversations))
	args := []any{userID}
	byID := make(map[int64]*ConversationSummary, len(conversations))
	for i := range conversations {
		item := &conversations[i]
		if item.LastReadSeq == item.LastSeq {
			continue
		}
		ranges = append(ranges, `(conversation_id = ? AND seq > ? AND seq <= ?)`)
		args = append(args, item.ID, item.LastReadSeq, item.LastSeq)
		byID[item.ID] = item
	}
	if len(ranges) == 0 {
		return nil
	}

	var counts []struct {
		ConversationID int64 `db:"conversation_id"`
		UnreadCount    int64 `db:"unread_count"`
	}
	if err := s.db.SelectContext(ctx, &counts, `
		SELECT conversation_id, COUNT(*) AS unread_count FROM messages
		WHERE sender_id <> ? AND (`+strings.Join(ranges, ` OR `)+`) GROUP BY conversation_id`, args...); err != nil {
		return err
	}
	for _, count := range counts {
		byID[count.ConversationID].UnreadCount = count.UnreadCount
	}
	return nil
}

// IsMember 检查用户是否有权访问指定会话。
func (s *mysqlStore) IsMember(ctx context.Context, userID, conversationID int64) (bool, error) {
	var member bool
	err := s.db.GetContext(ctx, &member, `
		SELECT EXISTS (SELECT 1 FROM conversation_members WHERE conversation_id = ? AND user_id = ?)`, conversationID, userID)
	return member, err
}

// LastSequence 查询会话中已经分配的最新消息序号。
func (s *mysqlStore) LastSequence(ctx context.Context, conversationID int64) (int64, error) {
	var lastSeq int64
	err := s.db.GetContext(ctx, &lastSeq, `SELECT last_seq FROM conversations WHERE id = ?`, conversationID)
	return lastSeq, err
}

// AdvanceRead 单向推进成员的已读游标。
func (s *mysqlStore) AdvanceRead(ctx context.Context, userID, conversationID, seq int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_members
		SET last_read_seq = GREATEST(last_read_seq, ?)
		WHERE conversation_id = ? AND user_id = ?`, seq, conversationID, userID)
	return err
}

var (
	ErrInvalidClientMsgID = errors.New("client message ID must be a UUID")
	ErrInvalidContent     = errors.New("message must contain non-blank UTF-8 text of at most 2000 characters")
	ErrInvalidPageSize    = errors.New("message page size must be between 1 and 100")
)

type MessageDraft struct {
	clientMsgID string
	content     string
}

// NewMessageDraft 校验并规范化 HTTP 入口传入的消息草稿。
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

type DirectConversation struct {
	LastSeq int64 `db:"last_seq"`
	LowID   int64 `db:"direct_user_low_id"`
	HighID  int64 `db:"direct_user_high_id"`
}

// BeginTx 为一次消息发送创建数据库事务。
func (s *mysqlStore) BeginTx(ctx context.Context) (*sqlx.Tx, error) {
	return s.db.BeginTxx(ctx, nil)
}

// LockDirectConversation 锁定会话行，读取分配下一条消息所需的数据。
func (s *mysqlStore) LockDirectConversation(ctx context.Context, tx *sqlx.Tx, id int64) (DirectConversation, error) {
	var conversation DirectConversation
	err := tx.GetContext(ctx, &conversation, `
		SELECT last_seq, direct_user_low_id, direct_user_high_id FROM conversations WHERE id = ? FOR UPDATE`, id)
	return conversation, err
}

// FindSentMessage 按客户端消息标识查找已保存的发送结果。
func (s *mysqlStore) FindSentMessage(ctx context.Context, tx *sqlx.Tx, conversationID, userID int64, clientMsgID string) (Message, error) {
	var message Message
	err := tx.GetContext(ctx, &message, selectMessage+`
		WHERE conversation_id = ? AND sender_id = ? AND client_msg_id = ?`, conversationID, userID, clientMsgID)
	return message, err
}

// InsertMessage 将新消息写入消息表并返回数据库消息 ID。
func (s *mysqlStore) InsertMessage(ctx context.Context, tx *sqlx.Tx, message Message) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO messages (conversation_id, sender_id, client_msg_id, seq, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, message.ConversationID, message.SenderID, message.ClientMsgID, message.Seq, message.Content, message.CreatedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateLastMessage 将会话最新序号和活动时间更新为本次消息。
func (s *mysqlStore) UpdateLastMessage(ctx context.Context, tx *sqlx.Tx, message Message) error {
	_, err := tx.ExecContext(ctx, `UPDATE conversations SET last_seq = ?, last_activity_at = ? WHERE id = ?`,
		message.Seq, message.CreatedAt, message.ConversationID)
	return err
}

// History 按序号倒序读取会话历史消息。
func (s *mysqlStore) History(ctx context.Context, conversationID, beforeSeq int64, limit int) ([]Message, error) {
	query := selectMessage + ` WHERE conversation_id = ?`
	args := []any{conversationID}
	if beforeSeq != 0 {
		query += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	return s.queryMessages(ctx, query+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
}

// Sync 按序号正序读取设备游标之后的新消息。
func (s *mysqlStore) Sync(ctx context.Context, conversationID, afterSeq int64, limit int) ([]Message, error) {
	return s.queryMessages(ctx,
		selectMessage+` WHERE conversation_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		conversationID, afterSeq, limit)
}

// queryMessages 执行历史或增量查询并映射消息记录。
func (s *mysqlStore) queryMessages(ctx context.Context, query string, args ...any) ([]Message, error) {
	messages := []Message{}
	if err := s.db.SelectContext(ctx, &messages, query, args...); err != nil {
		return nil, err
	}
	return messages, nil
}
