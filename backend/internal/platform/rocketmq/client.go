package rocketmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	mq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/apache/rocketmq-client-go/v2/rlog"

	"github.com/c926694/im/backend/internal/outbox"
)

const topic = "im_redpacket_events"

type Client struct {
	producer mq.Producer
	consumer mq.PushConsumer
}

// Open 连接已有 Topic。Go 客户端要求 NameServer 使用 IP 地址。
func Open(ctx context.Context, address string, notify func(context.Context, outbox.Event) error) (*Client, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	addresses := make(primitive.NamesrvAddr, 0, len(ips))
	for _, ip := range ips {
		addresses = append(addresses, net.JoinHostPort(ip.String(), port))
	}
	rlog.SetLogLevel("error")
	p, err := mq.NewProducer(producer.WithNameServer(addresses), producer.WithVIPChannel(false),
		producer.WithGroupName("im_redpacket_publishers"), producer.WithRetry(0))
	if err != nil {
		return nil, err
	}
	c, err := mq.NewPushConsumer(consumer.WithNameServer(addresses), consumer.WithVIPChannel(false),
		consumer.WithGroupName("im_redpacket_notifications"), consumer.WithConsumeFromWhere(consumer.ConsumeFromFirstOffset))
	if err != nil {
		return nil, err
	}
	if err := c.Subscribe(topic, consumer.MessageSelector{}, func(_ context.Context, messages ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		// 由应用上下文控制通知查询，关闭时取消数据库请求。
		consumeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for _, message := range messages {
			var event outbox.Event
			if err := json.Unmarshal(message.Body, &event); err != nil {
				return consumer.ConsumeRetryLater, err
			}
			if err := notify(consumeCtx, event); err != nil {
				return consumer.ConsumeRetryLater, err
			}
		}
		return consumer.ConsumeSuccess, nil
	}); err != nil {
		return nil, err
	}
	if err := p.Start(); err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, errors.Join(err, p.Shutdown())
	}
	return &Client{producer: p, consumer: c}, nil
}

func (c *Client) Publish(ctx context.Context, event outbox.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	message := primitive.NewMessage(topic, payload).WithKeys([]string{strconv.FormatInt(event.ID, 10)})
	result, err := c.producer.SendSync(ctx, message)
	if err != nil {
		return err
	}
	if result.Status != primitive.SendOK {
		return fmt.Errorf("RocketMQ send status %v", result.Status)
	}
	return nil
}

func (c *Client) Close() error {
	return errors.Join(c.consumer.Shutdown(), c.producer.Shutdown())
}
