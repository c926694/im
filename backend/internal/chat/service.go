package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/c926694/im/backend/internal/friendship"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) CreateDirect(ctx context.Context, userID, peerID int64) (int64, error) {
	if userID == peerID {
		return 0, ErrSelfConversation
	}
	// 使用逻辑外键，建聊前确认双方账号存在。
	users, err := s.store.countUsers(ctx, userID, peerID)
	if err != nil {
		return 0, err
	}
	if users != 2 {
		return 0, ErrUserNotFound
	}
	// 好友资格以本次查询为准，建聊不锁好友关系。
	accepted, err := friendship.AreFriends(ctx, s.store.db, userID, peerID)
	if err != nil {
		return 0, err
	}
	if !accepted {
		return 0, friendship.ErrNotFriends
	}
	// 规范化用户对，复用原会话及其成员记录。
	return s.store.upsertDirect(ctx, min(userID, peerID), max(userID, peerID))
}

func (s *Service) SendMessage(ctx context.Context, userID, conversationID int64, draft MessageDraft) (SendResult, error) {
	// 会话成员资格是读取该会话和发送消息的前提。
	if err := s.requireMember(ctx, userID, conversationID); err != nil {
		return SendResult{}, err
	}
	tx, err := s.store.db.BeginTxx(ctx, nil)
	if err != nil {
		return SendResult{}, err
	}
	defer tx.Rollback()

	// 锁住会话行，保证并发写入分配不同的序号。
	conversation, err := s.store.lockDirectConversation(ctx, tx, conversationID)
	if err != nil {
		return SendResult{}, err
	}
	recipientID := conversation.LowID
	if userID == conversation.LowID {
		recipientID = conversation.HighID
	}

	// 首次普通查询读取好友状态；通过检查的发送可以在解除好友后提交。
	accepted, err := friendship.AreFriends(ctx, tx, userID, recipientID)
	if err != nil {
		return SendResult{}, err
	}
	// 已提交的请求仍可返回原结果，解除好友只限制新消息。
	message, err := s.store.findSentMessage(ctx, tx, conversationID, userID, draft.clientMsgID)
	if err == nil {
		return SendResult{Message: message, RecipientID: recipientID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, err
	}
	if !accepted {
		return SendResult{}, friendship.ErrNotFriends
	}

	message = Message{
		ConversationID: conversationID, SenderID: userID, ClientMsgID: draft.clientMsgID,
		Seq: conversation.LastSeq + 1, Content: draft.content, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	// 写入消息与会话游标使用同一个事务，提交后才确认和推送。
	message.ID, err = s.store.insertMessage(ctx, tx, message)
	if err != nil {
		return SendResult{}, err
	}
	if err := s.store.updateLastMessage(ctx, tx, message); err != nil {
		return SendResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SendResult{}, err
	}
	return SendResult{Message: message, RecipientID: recipientID, Created: true}, nil
}

func (s *Service) ListConversations(ctx context.Context, userID int64) ([]ConversationSummary, error) {
	return s.store.ListConversations(ctx, userID)
}

// History 在 beforeSeq 为 0 时从最新消息开始查。
func (s *Service) History(ctx context.Context, userID, conversationID, beforeSeq int64, limit int) ([]Message, error) {
	limit, err := s.messagePageLimit(ctx, userID, conversationID, limit)
	if err != nil {
		return nil, err
	}
	return s.store.history(ctx, conversationID, beforeSeq, limit)
}

func (s *Service) Sync(ctx context.Context, userID, conversationID, afterSeq int64, limit int) ([]Message, error) {
	limit, err := s.messagePageLimit(ctx, userID, conversationID, limit)
	if err != nil {
		return nil, err
	}
	// 按设备的同步游标升序补拉，解除好友后仍可读取已保存的消息。
	return s.store.sync(ctx, conversationID, afterSeq, limit)
}

func (s *Service) messagePageLimit(ctx context.Context, userID, conversationID int64, limit int) (int, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return 0, ErrInvalidPageSize
	}
	// 读取历史只检查成员资格，好友关系变更不影响同步。
	return limit, s.requireMember(ctx, userID, conversationID)
}

func (s *Service) requireMember(ctx context.Context, userID, conversationID int64) error {
	member, err := s.store.isMember(ctx, userID, conversationID)
	if err != nil {
		return err
	}
	if !member {
		return ErrNotMember
	}
	return nil
}

func (s *Service) MarkRead(ctx context.Context, userID, conversationID, seq int64) error {
	if err := s.requireMember(ctx, userID, conversationID); err != nil {
		return err
	}
	// 只能将已读进度标记到已保存的消息。
	lastSeq, err := s.store.lastSequence(ctx, conversationID)
	if err != nil {
		return err
	}
	if seq < 0 || seq > lastSeq {
		return ErrInvalidReadSeq
	}
	// 旧设备上报的游标不能覆盖更新的已读进度。
	return s.store.advanceRead(ctx, userID, conversationID, seq)
}
