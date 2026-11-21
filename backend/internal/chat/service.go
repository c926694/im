package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/c926694/im/backend/internal/friendship"
)

// Service 组合聊天业务流程，并通过 Store 执行数据库读写。
type Service struct {
	store Store
}

// NewService 创建聊天业务服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// CreateDirect 校验账号和好友关系后创建或复用单聊。
func (s *Service) CreateDirect(ctx context.Context, userID, peerID int64) (int64, error) {
	if userID == peerID {
		return 0, ErrSelfConversation
	}
	// 使用逻辑外键，建聊前确认双方账号存在。
	users, err := s.store.CountUsers(ctx, userID, peerID)
	if err != nil {
		return 0, err
	}
	if users != 2 {
		return 0, ErrUserNotFound
	}
	// 好友资格以本次查询为准，建聊不锁好友关系。
	accepted, err := s.store.AreFriends(ctx, userID, peerID)
	if err != nil {
		return 0, err
	}
	if !accepted {
		return 0, friendship.ErrNotFriends
	}
	// 规范化用户对，复用原会话及其成员记录。
	return s.store.UpsertDirect(ctx, min(userID, peerID), max(userID, peerID))
}

// SendMessage 检查权限和好友状态，在一个事务内保存消息并推进会话序号。
func (s *Service) SendMessage(ctx context.Context, userID, conversationID int64, draft MessageDraft) (SendResult, error) {
	// 会话成员资格是读取该会话和发送消息的前提。
	if err := s.requireMember(ctx, userID, conversationID); err != nil {
		return SendResult{}, err
	}
	tx, err := s.store.BeginTx(ctx)
	if err != nil {
		return SendResult{}, err
	}
	defer tx.Rollback()

	// 锁住会话行，保证并发写入分配不同的序号。
	conversation, err := s.store.LockDirectConversation(ctx, tx, conversationID)
	if err != nil {
		return SendResult{}, err
	}
	recipientID := conversation.LowID
	if userID == conversation.LowID {
		recipientID = conversation.HighID
	}

	// 首次普通查询读取好友状态；通过检查的发送可以在解除好友后提交。
	accepted, err := s.store.AreFriendsTx(ctx, tx, userID, recipientID)
	if err != nil {
		return SendResult{}, err
	}
	// 已提交的请求仍可返回原结果，解除好友只限制新消息。
	message, err := s.store.FindSentMessage(ctx, tx, conversationID, userID, draft.clientMsgID)
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
	message.ID, err = s.store.InsertMessage(ctx, tx, message)
	if err != nil {
		return SendResult{}, err
	}
	if err := s.store.UpdateLastMessage(ctx, tx, message); err != nil {
		return SendResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SendResult{}, err
	}
	return SendResult{Message: message, RecipientID: recipientID, Created: true}, nil
}

// ListConversations 查询当前用户可访问的会话摘要。
func (s *Service) ListConversations(ctx context.Context, userID int64) ([]ConversationSummary, error) {
	return s.store.ListConversations(ctx, userID)
}

// History 检查会话成员身份后读取较早的历史消息。
func (s *Service) History(ctx context.Context, userID, conversationID, beforeSeq int64, limit int) ([]Message, error) {
	limit, err := s.messagePageLimit(ctx, userID, conversationID, limit)
	if err != nil {
		return nil, err
	}
	return s.store.History(ctx, conversationID, beforeSeq, limit)
}

// Sync 检查会话成员身份后按设备游标补取新消息。
func (s *Service) Sync(ctx context.Context, userID, conversationID, afterSeq int64, limit int) ([]Message, error) {
	limit, err := s.messagePageLimit(ctx, userID, conversationID, limit)
	if err != nil {
		return nil, err
	}
	// 按设备的同步游标升序补拉，解除好友后仍可读取已保存的消息。
	return s.store.Sync(ctx, conversationID, afterSeq, limit)
}

// messagePageLimit 采用默认页数并确认请求用户属于会话。
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

// requireMember 使用成员记录判断用户是否能访问会话。
func (s *Service) requireMember(ctx context.Context, userID, conversationID int64) error {
	member, err := s.store.IsMember(ctx, userID, conversationID)
	if err != nil {
		return err
	}
	if !member {
		return ErrNotMember
	}
	return nil
}

// MarkRead 检查成员身份和会话最新序号后推进已读位置。
func (s *Service) MarkRead(ctx context.Context, userID, conversationID, seq int64) error {
	if err := s.requireMember(ctx, userID, conversationID); err != nil {
		return err
	}
	// 只能将已读进度标记到已保存的消息。
	lastSeq, err := s.store.LastSequence(ctx, conversationID)
	if err != nil {
		return err
	}
	if seq < 0 || seq > lastSeq {
		return ErrInvalidReadSeq
	}
	// 旧设备上报的游标不能覆盖更新的已读进度。
	return s.store.AdvanceRead(ctx, userID, conversationID, seq)
}
