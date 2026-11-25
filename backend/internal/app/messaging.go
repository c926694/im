package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/c926694/im/backend/internal/notify"
	"github.com/c926694/im/backend/internal/outbox"
	"github.com/c926694/im/backend/internal/platform/rocketmq"
	"github.com/c926694/im/backend/internal/social"
)

const (
	notificationTopic   = "im_redpacket_events"
	expiryTopic         = "im_redpacket_expiry"
	refundBatch         = 4
	chatPublisherName   = "聊天通知发布"
	chatConsumerName    = "聊天通知消费"
	expiryPublisherName = "红包到期发布"
	refundConsumerName  = "红包退款消费"
)

type notificationPublisher struct{ rocketmq.Producer }

func (p notificationPublisher) Publish(ctx context.Context, event outbox.Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.Send(ctx, rocketmq.Message{Topic: notificationTopic, Key: strconv.FormatInt(event.ID, 10), Body: body})
}

type expiryPublisher struct{ rocketmq.Producer }

func (p expiryPublisher) Publish(ctx context.Context, event outbox.Event) error {
	return p.Send(ctx, rocketmq.Message{Topic: expiryTopic, Key: strconv.FormatInt(event.ID, 10),
		Body: []byte(strconv.FormatInt(event.PacketID, 10)), DeliverAt: event.DeliverAt})
}

type messagingTasks struct {
	nameServer string
	endpoint   string
	events     *outbox.Store
	notifier   notify.Notifier
	packets    *social.Service
}

// Start 分别启动发布和消费；单个 Topic 暂不可用时不阻止其他任务。
func (m messagingTasks) Start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	startWorker := func(name string, run func(context.Context) error) {
		slog.Info("starting RocketMQ task", "task", name)
		workers.Add(1)
		go func() {
			defer workers.Done()
			runMQWorker(ctx, name, run)
		}()
	}
	startWorker(chatPublisherName, m.runChatPublisher)
	startWorker(chatConsumerName, m.runChatConsumer)
	startWorker(expiryPublisherName, m.runExpiryPublisher)
	startWorker(refundConsumerName, m.runRefundConsumer)
	return func() { cancel(); workers.Wait() }
}

func (m messagingTasks) runChatPublisher(ctx context.Context) error {
	p, err := rocketmq.OpenNameServerProducer(ctx, m.nameServer, "im_redpacket_publishers")
	if err != nil {
		return err
	}
	defer closeMQ(chatPublisherName, p)
	m.events.Run(ctx, notificationPublisher{p}, 8)
	return nil
}

func (m messagingTasks) runChatConsumer(ctx context.Context) error {
	c, err := rocketmq.OpenPushConsumer(ctx, m.nameServer,
		rocketmq.Subscription{Topic: notificationTopic, Group: "im_redpacket_notifications"}, m.handleChatNotification)
	if err != nil {
		return err
	}
	defer closeMQ(chatConsumerName, c)
	<-ctx.Done()
	return nil
}

func (m messagingTasks) handleChatNotification(ctx context.Context, message rocketmq.Message) error {
	return m.notifier.HandleMessage(ctx, message.Body)
}

func (m messagingTasks) runExpiryPublisher(ctx context.Context) error {
	p, err := rocketmq.OpenProxyProducer(m.endpoint, expiryTopic)
	if err != nil {
		return err
	}
	defer closeMQ(expiryPublisherName, p)
	m.events.RunExpiry(ctx, expiryPublisher{p}, 4)
	return nil
}

func (m messagingTasks) runRefundConsumer(ctx context.Context) error {
	c, err := rocketmq.OpenPullConsumer(m.endpoint,
		rocketmq.Subscription{Topic: expiryTopic, Group: "im_redpacket_refunds"}, 5*time.Second)
	if err != nil {
		return err
	}
	defer closeMQ(refundConsumerName, c)
	refunds := refundHandler{consumer: c, packets: m.packets}
	consumeBatch(ctx, c, refundBatch, 30*time.Second, refunds.refundAndAck)
	return nil
}

// consumeBatch 只负责按批拉取和调度；处理函数负责确认消息。
func consumeBatch(ctx context.Context, consumer *rocketmq.PullConsumer, limit int32, invisible time.Duration,
	handle func(context.Context, rocketmq.Delivery) error) {
	for ctx.Err() == nil {
		// 当前批处理结束再拉下一批，积压留在 Broker。
		messages, err := consumer.Receive(ctx, limit, invisible)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("receive RocketMQ tasks", "error", err)
			}
			waitMQ(ctx, time.Second)
			continue
		}
		var batch sync.WaitGroup
		for _, message := range messages {
			batch.Add(1)
			go func() {
				defer batch.Done()
				if err := handle(ctx, message); err != nil && ctx.Err() == nil {
					slog.Warn("RocketMQ task remains unacknowledged", "message_id", message.ID, "error", err)
				}
			}()
		}
		batch.Wait()
	}
}

type refundHandler struct {
	consumer *rocketmq.PullConsumer
	packets  *social.Service
}

func (h refundHandler) refundAndAck(ctx context.Context, message rocketmq.Delivery) error {
	txCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	err := h.packets.HandleExpiry(txCtx, message.Body)
	cancel()
	if err != nil {
		return err
	}
	// 确认使用独立的期限，并且必须在退款提交之后。
	ackCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return h.consumer.Ack(ackCtx, message)
}

func runMQWorker(ctx context.Context, worker string, run func(context.Context) error) {
	for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 15*time.Second) {
		if err := run(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("RocketMQ worker unavailable", "worker", worker, "error", err, "retry_in", delay)
		} else {
			return
		}
		waitMQ(ctx, delay)
	}
}

func waitMQ(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func closeMQ(worker string, client io.Closer) {
	if err := client.Close(); err != nil {
		slog.Warn("close RocketMQ worker", "worker", worker, "error", err)
	}
}
