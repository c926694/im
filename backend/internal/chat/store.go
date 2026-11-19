package chat

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

var (
	ErrSelfConversation = errors.New("cannot open a direct conversation with yourself")
	ErrUserNotFound     = errors.New("a conversation participant does not exist")
	ErrNotMember        = errors.New("not a member of the conversation")
	ErrInvalidReadSeq   = errors.New("read sequence is outside the conversation")
)

type Store struct {
	db *sqlx.DB
}

func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

type ConversationSummary struct {
	ID             int64 `db:"id"`
	PeerUserID     int64 `db:"peer_user_id"`
	LastSeq        int64 `db:"last_seq"`
	LastReadSeq    int64
	LastActivityAt time.Time `db:"last_activity_at"`
	UnreadCount    int64
}

func (s *Store) CreateDirect(ctx context.Context, userID, peerID int64) (int64, error) {
	if userID == peerID {
		return 0, ErrSelfConversation
	}
	// 没有物理外键，建聊前要确认双方账号都存在。
	var users int
	if err := s.db.GetContext(ctx, &users, `SELECT COUNT(*) FROM users WHERE id IN (?, ?)`, userID, peerID); err != nil {
		return 0, err
	}
	if users != 2 {
		return 0, ErrUserNotFound
	}
	// 固定用户 ID 的顺序，避免双方各建出一个单聊。
	low, high := min(userID, peerID), max(userID, peerID)
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

func (s *Store) ListConversations(ctx context.Context, userID int64) ([]ConversationSummary, error) {
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

func (s *Store) fillUnreadCounts(ctx context.Context, userID int64, conversations []ConversationSummary) error {
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

func (s *Store) requireMember(ctx context.Context, userID, conversationID int64) error {
	var member int
	err := s.db.GetContext(ctx, &member, `
		SELECT 1 FROM conversation_members WHERE conversation_id = ? AND user_id = ?`, conversationID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotMember
	}
	return err
}

func (s *Store) MarkRead(ctx context.Context, userID, conversationID, seq int64) error {
	// 先确认是会话成员，再读取会话的最新序号，防止把游标标到尚未发送的消息。
	if err := s.requireMember(ctx, userID, conversationID); err != nil {
		return err
	}
	var lastSeq int64
	if err := s.db.GetContext(ctx, &lastSeq, `SELECT last_seq FROM conversations WHERE id = ?`, conversationID); err != nil {
		return err
	}
	if seq < 0 || seq > lastSeq {
		return ErrInvalidReadSeq
	}
	// 旧设备上报的游标不能覆盖更新的已读进度。
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_members
		SET last_read_seq = GREATEST(last_read_seq, ?)
		WHERE conversation_id = ? AND user_id = ?`, seq, conversationID, userID)
	return err
}
