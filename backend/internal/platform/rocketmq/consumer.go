package rocketmq

import (
	"context"
	"errors"
	"time"

	mq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	mq5 "github.com/apache/rocketmq-clients/golang/v5"
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"
)

type Subscription struct {
	Topic string
	Group string
}

type PushConsumer struct{ client mq.PushConsumer }

// OpenPushConsumer 在回调成功后确认该批消息；回调返回错误时由 Broker 重投。
func OpenPushConsumer(ctx context.Context, address string, sub Subscription, handle func(context.Context, Message) error) (*PushConsumer, error) {
	addresses, err := resolveNameServer(ctx, address)
	if err != nil {
		return nil, err
	}
	client, err := mq.NewPushConsumer(consumer.WithNameServer(addresses), consumer.WithVIPChannel(false),
		consumer.WithGroupName(sub.Group), consumer.WithConsumeFromWhere(consumer.ConsumeFromFirstOffset))
	if err != nil {
		return nil, err
	}
	if err := client.Subscribe(sub.Topic, consumer.MessageSelector{}, func(_ context.Context, messages ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		for _, message := range messages {
			if err := handle(ctx, Message{Topic: message.Topic, Key: message.GetKeys(), Body: message.Body}); err != nil {
				return consumer.ConsumeRetryLater, err
			}
		}
		return consumer.ConsumeSuccess, nil
	}); err != nil {
		return nil, err
	}
	if err := client.Start(); err != nil {
		return nil, err
	}
	return &PushConsumer{client}, nil
}

func (c *PushConsumer) Close() error { return c.client.Shutdown() }

type PullConsumer struct{ client mq5.SimpleConsumer }

type Delivery struct {
	ID   string
	Body []byte
	raw  *mq5.MessageView
}

func OpenPullConsumer(endpoint string, sub Subscription, await time.Duration) (*PullConsumer, error) {
	client, err := mq5.NewSimpleConsumer(proxyConfig(endpoint, sub.Group), mq5.WithAwaitDuration(await),
		mq5.WithSubscriptionExpressions(map[string]*mq5.FilterExpression{sub.Topic: mq5.SUB_ALL}))
	if err != nil {
		return nil, err
	}
	if err := client.Start(); err != nil {
		return nil, err
	}
	return &PullConsumer{client}, nil
}

// Receive 只拉取当前批次；消息确认由调用方显式执行。
func (c *PullConsumer) Receive(ctx context.Context, limit int32, invisible time.Duration) ([]Delivery, error) {
	messages, err := c.client.Receive(ctx, limit, invisible)
	if err != nil {
		var status *mq5.ErrRpcStatus
		if errors.As(err, &status) && status.Code == int32(v2.Code_MESSAGE_NOT_FOUND) {
			return nil, nil
		}
		return nil, err
	}
	result := make([]Delivery, 0, len(messages))
	for _, message := range messages {
		result = append(result, Delivery{ID: message.GetMessageId(), Body: message.GetBody(), raw: message})
	}
	return result, nil
}

func (c *PullConsumer) Ack(ctx context.Context, delivery Delivery) error {
	return c.client.Ack(ctx, delivery.raw)
}

func (c *PullConsumer) Close() error { return c.client.GracefulStop() }
