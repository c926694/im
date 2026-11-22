package app

import (
	"context"
	"fmt"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/outbox"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/redpacket"
	"github.com/c926694/im/backend/internal/user"
)

type packetNotifier struct {
	chat       *chat.Service
	redPackets *redpacket.Service
	users      *user.Store
	hub        *realtime.Hub
}

// notify 读取消费时的消息可见范围，通知重复也不会变更积分。
func (n packetNotifier) notify(ctx context.Context, event outbox.Event) error {
	switch event.Kind {
	case outbox.Created:
		message, recipients, err := n.chat.PacketAudience(ctx, event.PacketID)
		if err != nil {
			return err
		}
		if len(recipients) == 0 {
			return nil
		}
		profiles, err := n.users.ProfilesByIDs(ctx, []int64{message.SenderID})
		if err != nil {
			return err
		}
		created := realtime.NewMessageCreated(message, profiles[message.SenderID].Nickname)
		statuses, err := n.redPackets.Statuses(ctx, []int64{event.PacketID})
		if err != nil {
			return err
		}
		status, ok := statuses[event.PacketID]
		if !ok {
			return fmt.Errorf("red packet %d is missing its status", event.PacketID)
		}
		created.RedPacketStatus = string(status)
		return n.hub.PushMessages(recipients, created)
	case outbox.Claimed:
		if event.MessageID == nil {
			return fmt.Errorf("claim event %d is missing its chat message", event.ID)
		}
		claimMessage, claimRecipients, err := n.chat.MessageAudience(ctx, *event.MessageID)
		if err != nil {
			return err
		}
		profiles, err := n.users.ProfilesByIDs(ctx, []int64{claimMessage.SenderID})
		if err != nil {
			return err
		}
		if err := n.hub.PushMessages(claimRecipients, realtime.NewMessageCreated(claimMessage, profiles[claimMessage.SenderID].Nickname)); err != nil {
			return err
		}
		_, packetRecipients, err := n.chat.PacketAudience(ctx, event.PacketID)
		if err != nil {
			return err
		}
		if len(packetRecipients) == 0 {
			return nil
		}
		statuses, err := n.redPackets.Statuses(ctx, []int64{event.PacketID})
		if err != nil {
			return err
		}
		status, ok := statuses[event.PacketID]
		if !ok {
			return fmt.Errorf("red packet %d is missing its status", event.PacketID)
		}
		return n.hub.PushPacketUpdated(packetRecipients, realtime.PacketUpdated{PacketID: event.PacketID, Status: string(status)})
	case outbox.Refunded:
		_, recipients, err := n.chat.PacketAudience(ctx, event.PacketID)
		if err != nil {
			return err
		}
		if len(recipients) == 0 {
			return nil
		}
		statuses, err := n.redPackets.Statuses(ctx, []int64{event.PacketID})
		if err != nil {
			return err
		}
		status, ok := statuses[event.PacketID]
		if !ok {
			return fmt.Errorf("red packet %d is missing its status", event.PacketID)
		}
		return n.hub.PushPacketUpdated(recipients, realtime.PacketUpdated{PacketID: event.PacketID, Status: string(status)})
	default:
		return fmt.Errorf("unknown outbox event kind %q", event.Kind)
	}
}
