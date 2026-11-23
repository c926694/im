package chat

import (
	"context"
	"database/sql"
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
	ErrMessageConflict  = errors.New("client message ID is used by a different message")
	ErrInvalidReadSeq   = errors.New("read sequence is outside the conversation")
)

// Store 汇总聊天业务需要的数据库操作，Service 通过它组合数据读写。
type Store interface {
	CountUsers(ctx context.Context, userID, peerID int64) (int, error)
	AreFriends(ctx context.Context, userID, peerID int64) (bool, error)
	AreFriendsTx(ctx context.Context, tx *sqlx.Tx, userID, peerID int64) (bool, error)
	UpsertDirect(ctx context.Context, low, high int64) (int64, error)
	ListConversations(ctx context.Context, userID int64, ids ...int64) ([]ConversationSummary, error)
	ListConversationVersions(ctx context.Context, userID int64) ([]ConversationSummary, error)
	Membership(ctx context.Context, tx *sqlx.Tx, conversationID, userID int64) (Member, error)
	AdvanceRead(ctx context.Context, userID, conversationID, seq int64) error
	BeginTx(ctx context.Context) (*sqlx.Tx, error)
	BeginSnapshot(ctx context.Context) (*sqlx.Tx, error)
	LockConversation(ctx context.Context, tx *sqlx.Tx, id int64) (Conversation, error)
	GetConversation(ctx context.Context, tx *sqlx.Tx, id int64) (Conversation, error)
	InsertGroup(ctx context.Context, tx *sqlx.Tx, name string) (int64, error)
	ActiveMembers(ctx context.Context, tx *sqlx.Tx, id int64) ([]Member, error)
	AddMembers(ctx context.Context, tx *sqlx.Tx, id, joinedSeq int64, role Role, userIDs []int64) error
	CountFriends(ctx context.Context, tx *sqlx.Tx, actor int64, ids []int64) (int, error)
	RenameGroup(ctx context.Context, tx *sqlx.Tx, id int64, name string) error
	SetRole(ctx context.Context, tx *sqlx.Tx, id, userID int64, role Role) error
	RemoveMember(ctx context.Context, tx *sqlx.Tx, id, userID int64) error
	CloseGroup(ctx context.Context, tx *sqlx.Tx, id int64) error
	FindSentMessage(ctx context.Context, tx *sqlx.Tx, conversationID, userID int64, clientMsgID string) (Message, error)
	InsertMessage(ctx context.Context, tx *sqlx.Tx, message Message) (int64, error)
	UpdateLastMessage(ctx context.Context, tx *sqlx.Tx, message Message) error
	PacketMessage(ctx context.Context, tx *sqlx.Tx, packetID int64) (Message, error)
	MessageByID(ctx context.Context, tx *sqlx.Tx, messageID int64) (Message, error)
	History(ctx context.Context, tx *sqlx.Tx, conversationID, joinedSeq, beforeSeq int64, limit int) ([]Message, error)
	Sync(ctx context.Context, tx *sqlx.Tx, conversationID, afterSeq int64, limit int) ([]Message, error)
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
	ID             int64  `db:"id"`
	PeerUserID     int64  `db:"peer_user_id"`
	LastSeq        int64  `db:"last_seq"`
	Kind           Kind   `db:"kind"`
	GroupName      string `db:"group_name"`
	JoinedSeq      int64
	LastReadSeq    int64
	LastActivityAt time.Time `db:"last_activity_at"`
	LastMessage    string
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
func (s *mysqlStore) ListConversations(ctx context.Context, userID int64, ids ...int64) ([]ConversationSummary, error) {
	return s.listConversations(ctx, userID, true, ids)
}

func (s *mysqlStore) ListConversationVersions(ctx context.Context, userID int64) ([]ConversationSummary, error) {
	return s.listConversations(ctx, userID, false, nil)
}

func (s *mysqlStore) listConversations(ctx context.Context, userID int64, details bool, filter []int64) ([]ConversationSummary, error) {
	// 会话摘要的成员资格、正文和未读数读取同一个快照。
	tx, err := s.BeginSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 先从成员表拿到会话 ID 和个人已读游标，再按 ID 批量查会话。
	var memberships []struct {
		ConversationID int64 `db:"conversation_id"`
		LastReadSeq    int64 `db:"last_read_seq"`
		JoinedSeq      int64 `db:"joined_seq"`
	}
	query := `SELECT conversation_id, last_read_seq, joined_seq FROM conversation_members WHERE user_id = ? AND left_at IS NULL`
	args := []any{userID}
	if len(filter) > 0 {
		query, args, err = sqlx.In(query+` AND conversation_id IN (?)`, userID, filter)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.SelectContext(ctx, &memberships, query, args...); err != nil {
		return nil, err
	}
	conversations := []ConversationSummary{}
	if len(memberships) == 0 {
		return conversations, nil
	}

	ids := make([]int64, 0, len(memberships))
	readSeqs := make(map[int64]int64, len(memberships))
	joinedSeqs := make(map[int64]int64, len(memberships))
	for _, member := range memberships {
		ids = append(ids, member.ConversationID)
		readSeqs[member.ConversationID] = member.LastReadSeq
		joinedSeqs[member.ConversationID] = member.JoinedSeq
	}
	query, args, err = sqlx.In(`
		SELECT id, COALESCE(IF(direct_user_low_id = ?, direct_user_high_id, direct_user_low_id), 0) AS peer_user_id,
		       kind, COALESCE(group_name, '') AS group_name, last_seq, last_activity_at
		FROM conversations WHERE id IN (?) AND closed_at IS NULL ORDER BY last_activity_at DESC, id DESC`, userID, ids)
	if err != nil {
		return nil, err
	}
	if err := tx.SelectContext(ctx, &conversations, query, args...); err != nil {
		return nil, err
	}
	for i := range conversations {
		conversations[i].LastReadSeq = readSeqs[conversations[i].ID]
		conversations[i].JoinedSeq = joinedSeqs[conversations[i].ID]
	}
	if !details {
		return conversations, nil
	}
	if err := s.fillLastMessages(ctx, tx, conversations); err != nil {
		return nil, err
	}
	// 按各会话的已读游标批量统计未读，排除当前用户自己发的消息。
	if err := s.fillUnreadCounts(ctx, tx, userID, conversations); err != nil {
		return nil, err
	}
	return conversations, nil
}

// fillLastMessages 用会话 ID 和最新序号批量读取摘要对应的正文。
func (s *mysqlStore) fillLastMessages(ctx context.Context, tx *sqlx.Tx, conversations []ConversationSummary) error {
	keys := make([]string, 0, len(conversations))
	args := make([]any, 0, 2*len(conversations))
	byID := make(map[int64]*ConversationSummary, len(conversations))
	for i := range conversations {
		item := &conversations[i]
		if item.LastSeq <= item.JoinedSeq {
			continue
		}
		keys = append(keys, "(?, ?)")
		args = append(args, item.ID, item.LastSeq)
		byID[item.ID] = item
	}
	if len(keys) == 0 {
		return nil
	}
	var messages []struct {
		ConversationID int64  `db:"conversation_id"`
		Content        string `db:"content"`
	}
	if err := tx.SelectContext(ctx, &messages, `SELECT conversation_id, content FROM messages
		WHERE (conversation_id, seq) IN (`+strings.Join(keys, ", ")+`)`, args...); err != nil {
		return err
	}
	for _, message := range messages {
		byID[message.ConversationID].LastMessage = message.Content
	}
	return nil
}

// fillUnreadCounts 按会话汇总游标之后由其他人发送的消息。
func (s *mysqlStore) fillUnreadCounts(ctx context.Context, tx *sqlx.Tx, userID int64, conversations []ConversationSummary) error {
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
	if err := tx.SelectContext(ctx, &counts, `
		SELECT conversation_id, COUNT(*) AS unread_count FROM messages
		WHERE sender_id <> ? AND (`+strings.Join(ranges, ` OR `)+`) GROUP BY conversation_id`, args...); err != nil {
		return err
	}
	for _, count := range counts {
		byID[count.ConversationID].UnreadCount = count.UnreadCount
	}
	return nil
}

// AdvanceRead 单向推进成员的已读游标。
func (s *mysqlStore) AdvanceRead(ctx context.Context, userID, conversationID, seq int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_members
		SET last_read_seq = GREATEST(last_read_seq, ?)
		WHERE conversation_id = ? AND user_id = ? AND left_at IS NULL AND joined_seq <= ?`, seq, conversationID, userID, seq)
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
func ParseClientMessageID(clientMsgID string) (string, error) {
	if len(clientMsgID) != 36 || clientMsgID[8] != '-' || clientMsgID[13] != '-' ||
		clientMsgID[18] != '-' || clientMsgID[23] != '-' {
		return "", ErrInvalidClientMsgID
	}
	if decoded, err := hex.DecodeString(strings.ReplaceAll(clientMsgID, "-", "")); err != nil || len(decoded) != 16 {
		return "", ErrInvalidClientMsgID
	}
	return strings.ToLower(clientMsgID), nil
}

func NewMessageDraft(clientMsgID, content string) (MessageDraft, error) {
	clientMsgID, err := ParseClientMessageID(clientMsgID)
	if err != nil {
		return MessageDraft{}, err
	}

	if err := ValidateMessageContent(content); err != nil {
		return MessageDraft{}, err
	}
	return MessageDraft{clientMsgID: clientMsgID, content: content}, nil
}

func ValidateMessageContent(content string) error {
	if !utf8.ValidString(content) || strings.TrimSpace(content) == "" || utf8.RuneCountInString(content) > 2000 {
		return ErrInvalidContent
	}
	return nil
}

type MessageKind string

const (
	ImageMessage          MessageKind = "image"
	TextMessage           MessageKind = "text"
	RedPacketMessage      MessageKind = "red_packet"
	RedPacketClaimMessage MessageKind = "red_packet_claim"
)

type Message struct {
	ImagePath      sql.NullString `db:"image_path"`
	Kind           MessageKind    `db:"kind"`
	RedPacketID    *int64         `db:"red_packet_id"`
	ID             int64          `db:"id"`
	ConversationID int64          `db:"conversation_id"`
	SenderID       int64          `db:"sender_id"`
	ClientMsgID    string         `db:"client_msg_id"`
	Seq            int64          `db:"seq"`
	Content        string         `db:"content"`
	CreatedAt      time.Time      `db:"created_at"`
}

const selectMessage = `SELECT id, conversation_id, sender_id, client_msg_id, seq, content, created_at, kind, red_packet_id, image_path FROM messages`

type SendResult struct {
	Message    Message
	Recipients []int64
	Created    bool
}

// BeginTx 为一次消息发送创建数据库事务。
func (s *mysqlStore) BeginTx(ctx context.Context) (*sqlx.Tx, error) {
	return s.db.BeginTxx(ctx, nil)
}

// BeginSnapshot 保证多个读取操作看到相同的成员状态和消息版本。
func (s *mysqlStore) BeginSnapshot(ctx context.Context) (*sqlx.Tx, error) {
	return s.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
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
		INSERT INTO messages (conversation_id, sender_id, client_msg_id, seq, content, created_at, kind, red_packet_id, image_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, message.ConversationID, message.SenderID, message.ClientMsgID, message.Seq, message.Content, message.CreatedAt, message.Kind, message.RedPacketID, message.ImagePath)
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
func (s *mysqlStore) History(ctx context.Context, tx *sqlx.Tx, conversationID, joinedSeq, beforeSeq int64, limit int) ([]Message, error) {
	query := selectMessage + ` WHERE conversation_id = ? AND seq > ?`
	args := []any{conversationID, joinedSeq}
	if beforeSeq != 0 {
		query += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	return s.queryMessages(ctx, tx, query+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
}

// Sync 按序号正序读取设备游标之后的新消息。
func (s *mysqlStore) Sync(ctx context.Context, tx *sqlx.Tx, conversationID, afterSeq int64, limit int) ([]Message, error) {
	return s.queryMessages(ctx, tx,
		selectMessage+` WHERE conversation_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		conversationID, afterSeq, limit)
}

// queryMessages 执行历史或增量查询并映射消息记录。
func (s *mysqlStore) queryMessages(ctx context.Context, tx *sqlx.Tx, query string, args ...any) ([]Message, error) {
	messages := []Message{}
	if err := tx.SelectContext(ctx, &messages, query, args...); err != nil {
		return nil, err
	}
	return messages, nil
}

type Kind string

const (
	Direct Kind = "direct"
	Group  Kind = "group"
)

type Role string

const (
	Owner  Role = "owner"
	Admin  Role = "admin"
	Normal Role = "member"
)

type Conversation struct {
	ID       int64        `db:"id"`
	Kind     Kind         `db:"kind"`
	Name     string       `db:"group_name"`
	LastSeq  int64        `db:"last_seq"`
	LowID    int64        `db:"direct_user_low_id"`
	HighID   int64        `db:"direct_user_high_id"`
	ClosedAt sql.NullTime `db:"closed_at"`
}

type Member struct {
	UserID    int64 `db:"user_id"`
	Role      Role  `db:"role"`
	JoinedSeq int64 `db:"joined_seq"`
}

const selectConversation = `SELECT id, kind, COALESCE(group_name, '') AS group_name, last_seq,
	COALESCE(direct_user_low_id, 0) AS direct_user_low_id, COALESCE(direct_user_high_id, 0) AS direct_user_high_id,
	closed_at FROM conversations WHERE id = ?`

// GetConversation 读取会话状态，调用方通过只读事务保持查询范围一致。
func (s *mysqlStore) GetConversation(ctx context.Context, tx *sqlx.Tx, id int64) (Conversation, error) {
	var conversation Conversation
	err := tx.GetContext(ctx, &conversation, selectConversation, id)
	return conversation, err
}

// LockConversation 让同一会话的序号分配和成员变更共用行锁。
func (s *mysqlStore) LockConversation(ctx context.Context, tx *sqlx.Tx, id int64) (Conversation, error) {
	var conversation Conversation
	err := tx.GetContext(ctx, &conversation, selectConversation+` FOR UPDATE`, id)
	return conversation, err
}

// Membership 查询用户当前有效的成员记录及可读起点。
func (s *mysqlStore) Membership(ctx context.Context, tx *sqlx.Tx, conversationID, userID int64) (Member, error) {
	var member Member
	err := tx.GetContext(ctx, &member, `SELECT user_id, role, joined_seq FROM conversation_members
		WHERE conversation_id = ? AND user_id = ? AND left_at IS NULL`, conversationID, userID)
	return member, err
}

// ActiveMembers 一次读取所有在群的成员，供角色判断和推送选择使用。
func (s *mysqlStore) ActiveMembers(ctx context.Context, tx *sqlx.Tx, id int64) ([]Member, error) {
	var members []Member
	err := tx.SelectContext(ctx, &members, `SELECT user_id, role, joined_seq FROM conversation_members
		WHERE conversation_id = ? AND left_at IS NULL ORDER BY user_id`, id)
	return members, err
}

// InsertGroup 保存群会话，群主由 Service 在同一事务中写入。
func (s *mysqlStore) InsertGroup(ctx context.Context, tx *sqlx.Tx, name string) (int64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO conversations (kind, group_name) VALUES ('group', ?)`, name)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// AddMembers 批量写入新成员；重新加入时覆盖成员的角色和可读起点。
func (s *mysqlStore) AddMembers(ctx context.Context, tx *sqlx.Tx, id, joinedSeq int64, role Role, userIDs []int64) error {
	values := make([]string, 0, len(userIDs))
	args := make([]any, 0, 5*len(userIDs)+3)
	for _, userID := range userIDs {
		values = append(values, "(?, ?, ?, ?, ?)")
		args = append(args, id, userID, role, joinedSeq, joinedSeq)
	}
	args = append(args, role, joinedSeq, joinedSeq)
	_, err := tx.ExecContext(ctx, `INSERT INTO conversation_members (conversation_id, user_id, role, joined_seq, last_read_seq)
		VALUES `+strings.Join(values, ", ")+` ON DUPLICATE KEY UPDATE role = ?, joined_seq = ?, last_read_seq = ?,
		joined_at = CURRENT_TIMESTAMP(6), left_at = NULL`, args...)
	return err
}

// CountFriends 批量检查邀请对象与操作者的已接受好友关系。
func (s *mysqlStore) CountFriends(ctx context.Context, tx *sqlx.Tx, actor int64, ids []int64) (int, error) {
	query, args, err := sqlx.In(`SELECT COUNT(*) FROM friendships WHERE status = 'accepted'
		AND ((user_low_id = ? AND user_high_id IN (?)) OR (user_high_id = ? AND user_low_id IN (?)))`, actor, ids, actor, ids)
	if err != nil {
		return 0, err
	}
	var count int
	err = tx.GetContext(ctx, &count, query, args...)
	return count, err
}

// RenameGroup 更新会话标题，保留最近发消息的活动时间。
func (s *mysqlStore) RenameGroup(ctx context.Context, tx *sqlx.Tx, id int64, name string) error {
	_, err := tx.ExecContext(ctx, `UPDATE conversations SET group_name = ? WHERE id = ?`, name, id)
	return err
}

// SetRole 更新在群成员的群内角色。
func (s *mysqlStore) SetRole(ctx context.Context, tx *sqlx.Tx, id, userID int64, role Role) error {
	_, err := tx.ExecContext(ctx, `UPDATE conversation_members SET role = ? WHERE conversation_id = ? AND user_id = ?`, role, id, userID)
	return err
}

// RemoveMember 记录本次退群时间，保留成员和历史消息记录。
func (s *mysqlStore) RemoveMember(ctx context.Context, tx *sqlx.Tx, id, userID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE conversation_members SET left_at = CURRENT_TIMESTAMP(6) WHERE conversation_id = ? AND user_id = ?`, id, userID)
	return err
}

// CloseGroup 记录最后一名群成员退出的时间。
func (s *mysqlStore) CloseGroup(ctx context.Context, tx *sqlx.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE conversations SET closed_at = CURRENT_TIMESTAMP(6) WHERE id = ?`, id)
	return err
}

// PacketMessage 按红包 ID 查询对应的聊天卡片。
func (s *mysqlStore) PacketMessage(ctx context.Context, tx *sqlx.Tx, packetID int64) (Message, error) {
	var message Message
	err := tx.GetContext(ctx, &message, selectMessage+` WHERE red_packet_id = ?`, packetID)
	return message, err
}

// MessageByID 按全局消息标识读取已保存的聊天消息。
func (s *mysqlStore) MessageByID(ctx context.Context, tx *sqlx.Tx, messageID int64) (Message, error) {
	var message Message
	err := tx.GetContext(ctx, &message, selectMessage+` WHERE id = ?`, messageID)
	return message, err
}
