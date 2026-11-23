package redpacket

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/outbox"
	"github.com/c926694/im/backend/internal/points"
	"github.com/jmoiron/sqlx"
)

var (
	ErrNotFound      = errors.New("red packet does not exist")
	ErrInvalidShares = errors.New("share count exceeds eligible members")
	ErrIneligible    = errors.New("user cannot claim this red packet")
	ErrClaimed       = errors.New("red packet is fully claimed")
	ErrExpired       = errors.New("red packet expired")
	ErrNotYetExpired = errors.New("red packet has not expired")
)

const packetTTL = 24 * time.Hour

type Service struct {
	store *Store
	chat  *chat.Service
}

func NewService(store *Store, chatService *chat.Service) *Service {
	return &Service{store: store, chat: chatService}
}

type Draft struct {
	ClientMsgID    string
	PointsPerShare int64
	ShareCount     int
}

type Details struct {
	Packet
	CanClaim bool   `json:"can_claim"`
	MyClaim  *Claim `json:"my_claim"`
}

// Send 将扣减、红包卡片和发布记录保存在同一个事务内。
func (s *Service) Send(ctx context.Context, userID, conversationID int64, draft Draft) (chat.Message, error) {
	tx, err := s.store.beginWrite(ctx)
	if err != nil {
		return chat.Message{}, err
	}
	defer tx.Rollback()

	// SaveMessage 在会话权限和发送标识检查通过后，才调用红包写入步骤。
	result, err := s.chat.SaveMessage(ctx, tx, userID, conversationID, draft.ClientMsgID, chat.RedPacketMessage,
		func(kind chat.Kind, recipients []int64, message *chat.Message) error {
			return preparePacket(ctx, tx, kind, recipients, message, draft)
		})
	if err != nil {
		return chat.Message{}, err
	}
	packetID := *result.Message.RedPacketID
	if result.Created {
		if err := outbox.Add(ctx, tx, outbox.Event{Kind: outbox.Created, PacketID: packetID}); err != nil {
			return chat.Message{}, err
		}
		// 到期任务与红包一起提交，发布重试继续使用业务截止时间。
		deadline := result.Message.CreatedAt.Add(packetTTL)
		if err := outbox.Add(ctx, tx, outbox.Event{Kind: outbox.PacketExpiry, PacketID: packetID, DeliverAt: &deadline}); err != nil {
			return chat.Message{}, err
		}
	} else {
		packet, err := getPacket(ctx, tx, packetID, false)
		if err != nil {
			return chat.Message{}, err
		}
		if packet.PointsPerShare != draft.PointsPerShare || packet.ShareCount != draft.ShareCount {
			return chat.Message{}, chat.ErrMessageConflict
		}
	}
	return result.Message, tx.Commit()
}

// preparePacket 根据同一次会话成员查询确定红包的目标和份数上限。
func preparePacket(ctx context.Context, tx *sqlx.Tx, kind chat.Kind, recipients []int64, message *chat.Message, draft Draft) error {
	packet := Packet{SenderID: message.SenderID, PointsPerShare: draft.PointsPerShare, ShareCount: draft.ShareCount,
		Remaining: draft.ShareCount, ExpiresAt: message.CreatedAt.Add(packetTTL)}
	if kind == chat.Direct {
		if draft.ShareCount != 1 {
			return ErrInvalidShares
		}
		packet.RecipientID = &recipients[0]
	} else if draft.ShareCount > len(recipients)-1 {
		return ErrInvalidShares
	}
	id, err := insertPacket(ctx, tx, packet)
	if err != nil {
		return err
	}
	if err := points.Apply(ctx, tx, message.SenderID, points.Send, id, -draft.PointsPerShare*int64(draft.ShareCount)); err != nil {
		return err
	}
	message.Content = "[红包]"
	message.RedPacketID = &id
	return nil
}

func (s *Service) Details(ctx context.Context, id, userID int64) (Details, error) {
	tx, err := s.store.snapshot(ctx)
	if err != nil {
		return Details{}, err
	}
	defer tx.Rollback()
	if err := s.checkVisibility(ctx, tx, id, userID); err != nil {
		return Details{}, err
	}
	packet, err := getPacket(ctx, tx, id, false)
	if err != nil {
		return Details{}, err
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return Details{}, err
	}
	if packet.Status == Open && !now.Before(packet.ExpiresAt) {
		packet.Status = Expired
	}
	claim, err := getClaim(ctx, tx, id, userID)
	if err == nil {
		return Details{Packet: packet, MyClaim: &claim}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Details{}, err
	}
	return Details{Packet: packet, CanClaim: packet.Status == Open && packet.eligible(userID)}, nil
}

// Statuses 按红包 ID 批量读取卡片颜色需要的当前状态。
func (s *Service) Statuses(ctx context.Context, packetIDs []int64) (map[int64]Status, error) {
	return s.store.statuses(ctx, packetIDs)
}

// Claim 与退款共用同一红包行锁，时间判断在取得锁之后进行。
func (s *Service) Claim(ctx context.Context, id, userID int64) (Claim, error) {
	tx, err := s.store.beginWrite(ctx)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback()
	if err := s.checkVisibility(ctx, tx, id, userID); err != nil {
		return Claim{}, err
	}
	packet, now, err := lockedPacket(ctx, tx, id)
	if err != nil {
		return Claim{}, err
	}
	if !packet.eligible(userID) {
		return Claim{}, ErrIneligible
	}
	previous, err := getClaim(ctx, tx, id, userID)
	if err == nil {
		return previous, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Claim{}, err
	}
	if packet.Status == Claimed {
		return Claim{}, ErrClaimed
	}
	if packet.Status == Refunded || !now.Before(packet.ExpiresAt) {
		return Claim{}, ErrExpired
	}

	claim := Claim{Points: packet.PointsPerShare, CreatedAt: now}
	status := Open
	if packet.Remaining == 1 {
		status = Claimed
	}
	if err := updatePacket(ctx, tx, id, packet.Remaining-1, status); err != nil {
		return Claim{}, err
	}
	if err := insertClaim(ctx, tx, id, userID, claim); err != nil {
		return Claim{}, err
	}
	if err := points.Apply(ctx, tx, userID, points.Claim, id, claim.Points); err != nil {
		return Claim{}, err
	}
	message, err := s.chat.SavePacketClaimMessage(ctx, tx, id, userID)
	if err != nil {
		return Claim{}, err
	}
	if err := outbox.Add(ctx, tx, outbox.Event{Kind: outbox.Claimed, PacketID: id, MessageID: &message.ID}); err != nil {
		return Claim{}, err
	}
	return claim, tx.Commit()
}

// checkVisibility 复用聊天的成员资格和历史范围检查，单聊已有卡片仍可查看。
func (s *Service) checkVisibility(ctx context.Context, tx *sqlx.Tx, id, userID int64) error {
	_, err := s.chat.VisiblePacketMessage(ctx, tx, id, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (p Packet) eligible(userID int64) bool {
	return p.SenderID != userID && (p.RecipientID == nil || *p.RecipientID == userID)
}

// lockedPacket 在数据库授予行锁后读取 UTC 时间，等待期间跨过截止时间的请求无法继续领取。
func lockedPacket(ctx context.Context, tx *sqlx.Tx, id int64) (Packet, time.Time, error) {
	packet, err := getPacket(ctx, tx, id, true)
	if err != nil {
		return Packet{}, time.Time{}, err
	}
	now, err := databaseTime(ctx, tx)
	return packet, now, err
}

// HandleExpiry 解析到期通知中的红包 ID，并交给退款事务处理。
func (s *Service) HandleExpiry(ctx context.Context, body []byte) error {
	id, err := strconv.ParseInt(string(body), 10, 64)
	if err != nil || id <= 0 {
		return errors.New("invalid refund task packet ID")
	}
	if err := s.RefundDue(ctx, id); err != nil {
		return fmt.Errorf("refund packet %d: %w", id, err)
	}
	return nil
}

// RefundDue 处理指定红包；仅在事务提交或确认已结束后允许确认消费。
func (s *Service) RefundDue(ctx context.Context, id int64) error {
	tx, err := s.store.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	packet, now, err := lockedPacket(ctx, tx, id)
	if err != nil {
		return err
	}
	if packet.Status != Open {
		return nil
	}
	if now.Before(packet.ExpiresAt) {
		return ErrNotYetExpired
	}
	if err := updatePacket(ctx, tx, id, 0, Refunded); err != nil {
		return err
	}
	if err := points.Apply(ctx, tx, packet.SenderID, points.Refund, id, packet.PointsPerShare*int64(packet.Remaining)); err != nil {
		return err
	}
	if err := outbox.Add(ctx, tx, outbox.Event{Kind: outbox.Refunded, PacketID: id}); err != nil {
		return err
	}
	return tx.Commit()
}
