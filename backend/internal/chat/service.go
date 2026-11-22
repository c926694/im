package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/c926694/im/backend/internal/friendship"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
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

// SendMessage 检查当前会话权限，在一个事务内保存消息并推进序号。
func (s *Service) SendMessage(ctx context.Context, userID, conversationID int64, draft MessageDraft) (SendResult, error) {
	tx, err := s.store.BeginTx(ctx)
	if err != nil {
		return SendResult{}, err
	}
	defer tx.Rollback()

	result, err := s.SaveMessage(ctx, tx, userID, conversationID, draft.clientMsgID, TextMessage,
		func(_ Kind, _ []int64, message *Message) error {
			message.Content = draft.content
			return nil
		})
	if err != nil {
		return SendResult{}, err
	}
	return result, tx.Commit()
}

// SaveMessage 在调用方的事务中保存聊天消息。prepare 仅在新消息通过权限检查后执行。
func (s *Service) SaveMessage(ctx context.Context, tx *sqlx.Tx, userID, conversationID int64,
	clientMsgID string, kind MessageKind, prepare func(Kind, []int64, *Message) error) (SendResult, error) {

	// 群成员资格与消息序号采用同一个写入顺序。
	conversation, err := s.store.LockConversation(ctx, tx, conversationID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && conversation.ClosedAt.Valid) {
		return SendResult{}, ErrNotMember
	}
	if err != nil {
		return SendResult{}, err
	}
	member, err := s.requireMember(ctx, tx, userID, conversationID)
	if err != nil {
		return SendResult{}, err
	}

	result := SendResult{}
	accepted := true
	if conversation.Kind == Direct {
		recipientID := conversation.LowID
		if userID == conversation.LowID {
			recipientID = conversation.HighID
		}
		result.Recipients = []int64{recipientID}
		// 好友状态按普通查询判定，不锁好友关系。
		accepted, err = s.store.AreFriendsTx(ctx, tx, userID, recipientID)
		if err != nil {
			return SendResult{}, err
		}
	}
	// 只返回本次入群以来的发送结果；单聊解除好友后仍可取回已保存的请求。
	message, err := s.store.FindSentMessage(ctx, tx, conversationID, userID, clientMsgID)
	if err == nil {
		if message.Seq <= member.JoinedSeq {
			return SendResult{}, ErrPreviousMembershipMessage
		}
		if message.Kind != kind {
			return SendResult{}, ErrMessageConflict
		}
		result.Message = message
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, err
	}
	if !accepted {
		return SendResult{}, friendship.ErrNotFriends
	}

	message = Message{
		ConversationID: conversationID, SenderID: userID, ClientMsgID: clientMsgID, Kind: kind,
		Seq: conversation.LastSeq + 1, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if conversation.Kind == Group {
		members, err := s.store.ActiveMembers(ctx, tx, conversationID)
		if err != nil {
			return SendResult{}, err
		}
		result.Recipients = memberIDs(members)
	}
	if err := prepare(conversation.Kind, result.Recipients, &message); err != nil {
		return SendResult{}, err
	}
	// 写入消息与会话游标使用同一个事务，提交后才确认和推送。
	message.ID, err = s.store.InsertMessage(ctx, tx, message)
	if err != nil {
		return SendResult{}, err
	}
	if err := s.store.UpdateLastMessage(ctx, tx, message); err != nil {
		return SendResult{}, err
	}

	result.Message = message
	result.Created = true
	return result, nil
}

// ListConversations 查询当前用户可访问的会话摘要。
func (s *Service) ListConversations(ctx context.Context, userID int64) ([]ConversationSummary, error) {
	return s.store.ListConversations(ctx, userID)
}

// History 检查会话成员身份后读取较早的历史消息。
func (s *Service) History(ctx context.Context, userID, conversationID, beforeSeq int64, limit int) ([]Message, error) {
	return s.readMessages(ctx, userID, conversationID, beforeSeq, limit, false)
}

// Sync 检查会话成员身份后按设备游标补取新消息。
func (s *Service) Sync(ctx context.Context, userID, conversationID, afterSeq int64, limit int) ([]Message, error) {
	return s.readMessages(ctx, userID, conversationID, afterSeq, limit, true)
}

// readMessages 在同一个快照中读取成员资格和可见消息。
func (s *Service) readMessages(ctx context.Context, userID, conversationID, cursor int64, limit int, forward bool) ([]Message, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInvalidPageSize
	}
	tx, err := s.store.BeginSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	member, err := s.requireMember(ctx, tx, userID, conversationID)
	if err != nil {
		return nil, err
	}
	if forward {
		return s.store.Sync(ctx, tx, conversationID, max(cursor, member.JoinedSeq), limit)
	}
	return s.store.History(ctx, tx, conversationID, member.JoinedSeq, cursor, limit)
}

// requireMember 使用成员记录判断用户是否能访问会话。
func (s *Service) requireMember(ctx context.Context, tx *sqlx.Tx, userID, conversationID int64) (Member, error) {
	member, err := s.store.Membership(ctx, tx, conversationID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrNotMember
	}
	return member, err
}

// MarkRead 检查成员身份和会话最新序号后推进已读位置。
func (s *Service) MarkRead(ctx context.Context, userID, conversationID, seq int64) error {
	tx, err := s.store.BeginSnapshot(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := s.requireMember(ctx, tx, userID, conversationID)
	if err != nil {
		return err
	}
	// 只能将已读进度标记到已保存的消息。
	conversation, err := s.store.GetConversation(ctx, tx, conversationID)
	if err != nil {
		return err
	}
	if seq < member.JoinedSeq || seq > conversation.LastSeq {
		return ErrInvalidReadSeq
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// 更新时限定有效成员和可读起点，避免旧请求推进重新加入后的进度。
	return s.store.AdvanceRead(ctx, userID, conversationID, seq)
}

// VisiblePacketMessage 将卡片查询限制在当前成员本次加入后可见的历史内。
func (s *Service) VisiblePacketMessage(ctx context.Context, tx *sqlx.Tx, packetID, userID int64) (Message, error) {
	message, err := s.store.PacketMessage(ctx, tx, packetID)
	if err != nil {
		return Message{}, err
	}
	member, err := s.requireMember(ctx, tx, userID, message.ConversationID)
	if err != nil {
		return Message{}, err
	}
	if message.Seq <= member.JoinedSeq {
		return Message{}, ErrNotMember
	}
	return message, nil
}

// SavePacketClaimMessage 在红包领取事务内写入可同步的聊天通知。
func (s *Service) SavePacketClaimMessage(ctx context.Context, tx *sqlx.Tx, packetID, userID int64) (Message, error) {
	packetMessage, err := s.store.PacketMessage(ctx, tx, packetID)
	if err != nil {
		return Message{}, err
	}
	conversation, err := s.store.LockConversation(ctx, tx, packetMessage.ConversationID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && conversation.ClosedAt.Valid) {
		return Message{}, ErrNotMember
	}
	if err != nil {
		return Message{}, err
	}
	member, err := s.requireMember(ctx, tx, userID, conversation.ID)
	if err != nil {
		return Message{}, err
	}
	if packetMessage.Seq <= member.JoinedSeq {
		return Message{}, ErrNotMember
	}

	message := Message{
		ConversationID: conversation.ID,
		SenderID:       userID,
		ClientMsgID:    uuid.NewString(),
		Seq:            conversation.LastSeq + 1,
		Content:        "领取了红包",
		CreatedAt:      time.Now().UTC().Truncate(time.Microsecond),
		Kind:           RedPacketClaimMessage,
	}
	message.ID, err = s.store.InsertMessage(ctx, tx, message)
	if err != nil {
		return Message{}, err
	}
	if err := s.store.UpdateLastMessage(ctx, tx, message); err != nil {
		return Message{}, err
	}
	return message, nil
}

// PacketAudience 以同一读取快照取得卡片和当前有权查看的在线通知对象。
func (s *Service) PacketAudience(ctx context.Context, packetID int64) (Message, []int64, error) {
	tx, err := s.store.BeginSnapshot(ctx)
	if err != nil {
		return Message{}, nil, err
	}
	defer tx.Rollback()
	message, err := s.store.PacketMessage(ctx, tx, packetID)
	if err != nil {
		return Message{}, nil, err
	}
	members, err := s.store.ActiveMembers(ctx, tx, message.ConversationID)
	if err != nil {
		return Message{}, nil, err
	}
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		if message.Seq > member.JoinedSeq {
			ids = append(ids, member.UserID)
		}
	}
	return message, ids, nil
}

// MessageAudience 根据消息序号筛选当前有权接收该消息通知的会话成员。
func (s *Service) MessageAudience(ctx context.Context, messageID int64) (Message, []int64, error) {
	tx, err := s.store.BeginSnapshot(ctx)
	if err != nil {
		return Message{}, nil, err
	}
	defer tx.Rollback()
	message, err := s.store.MessageByID(ctx, tx, messageID)
	if err != nil {
		return Message{}, nil, err
	}
	members, err := s.store.ActiveMembers(ctx, tx, message.ConversationID)
	if err != nil {
		return Message{}, nil, err
	}
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		if message.Seq > member.JoinedSeq {
			ids = append(ids, member.UserID)
		}
	}
	return message, ids, nil
}
