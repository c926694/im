package rocketmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	mq5 "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"

	"github.com/c926694/im/backend/internal/outbox"
)

const (
	expiryTopic = "im_redpacket_expiry"
	expiryGroup = "im_redpacket_refunds"
	refundBatch = 4
)

type expiryPublisher struct{ producer mq5.Producer }

func (p expiryPublisher) Publish(ctx context.Context, event outbox.Event) error {
	message := mq5.Message{Topic: expiryTopic, Body: []byte(strconv.FormatInt(event.PacketID, 10))}
	message.SetKeys(strconv.FormatInt(event.ID, 10))
	message.SetDelayTimestamp(*event.DeliverAt)
	_, err := p.producer.Send(ctx, &message)
	return err
}

func RunExpiryPublisher(ctx context.Context, endpoint string, store *outbox.Store) {
	retryExpiryStartup(ctx, "expiry publisher", func() error {
		p, err := mq5.NewProducer(&mq5.Config{Endpoint: endpoint, Credentials: &credentials.SessionCredentials{}},
			mq5.WithTopics(expiryTopic))
		if err != nil {
			return err
		}
		if err := p.Start(); err != nil {
			return err
		}
		defer p.GracefulStop()
		store.RunExpiry(ctx, expiryPublisher{p}, 4)
		return nil
	})
}

func RunExpiryConsumer(ctx context.Context, endpoint string, refund func(context.Context, int64) error) {
	retryExpiryStartup(ctx, "expiry consumer", func() error {
		consumer, err := mq5.NewSimpleConsumer(&mq5.Config{
			Endpoint: endpoint, ConsumerGroup: expiryGroup, Credentials: &credentials.SessionCredentials{},
		}, mq5.WithAwaitDuration(5*time.Second),
			mq5.WithSubscriptionExpressions(map[string]*mq5.FilterExpression{expiryTopic: mq5.SUB_ALL}))
		if err != nil {
			return err
		}
		if err := consumer.Start(); err != nil {
			return err
		}
		defer consumer.GracefulStop()
		consumeExpiry(ctx, consumer, refund)
		return nil
	})
}

func consumeExpiry(ctx context.Context, consumer mq5.SimpleConsumer, refund func(context.Context, int64) error) {
	for ctx.Err() == nil {
		// 当前批结束后再拉下一批，积压保留在 Broker。
		messages, err := consumer.Receive(ctx, refundBatch, 30*time.Second)
		if err != nil {
			var status *mq5.ErrRpcStatus
			if errors.As(err, &status) && status.Code == int32(v2.Code_MESSAGE_NOT_FOUND) {
				continue
			}
			if ctx.Err() == nil {
				slog.Warn("receive refund tasks", "error", err)
			}
			waitExpiry(ctx, time.Second)
			continue
		}
		var batch sync.WaitGroup
		for _, message := range messages {
			batch.Add(1)
			go func() {
				defer batch.Done()
				if err := refundAndAck(ctx, consumer, message, refund); err != nil && ctx.Err() == nil {
					slog.Warn("refund task remains unacknowledged", "message_id", message.GetMessageId(), "error", err)
				}
			}()
		}
		batch.Wait()
	}
}

func refundAndAck(ctx context.Context, consumer mq5.SimpleConsumer, message *mq5.MessageView, refund func(context.Context, int64) error) error {
	var id int64
	if err := json.Unmarshal(message.GetBody(), &id); err != nil || id <= 0 {
		return errors.New("invalid refund task packet ID")
	}
	txCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := refund(txCtx, id); err != nil {
		return fmt.Errorf("refund packet %d: %w", id, err)
	}
	// 独立的确认期限，避免退款事务耗完请求时间后无法确认。
	ackCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return consumer.Ack(ackCtx, message)
}

func retryExpiryStartup(ctx context.Context, worker string, run func() error) {
	for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 15*time.Second) {
		if err := run(); err != nil && ctx.Err() == nil {
			slog.Warn("RocketMQ refund worker unavailable", "worker", worker, "error", err, "retry_in", delay)
		} else {
			return
		}
		waitExpiry(ctx, delay)
	}
}

func waitExpiry(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
