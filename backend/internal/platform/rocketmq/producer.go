package rocketmq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	mq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/apache/rocketmq-client-go/v2/rlog"
	mq5 "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
)

type Message struct {
	Topic     string
	Key       string
	Body      []byte
	DeliverAt *time.Time
}

type Producer interface {
	Send(context.Context, Message) error
	Close() error
}

type nameServerProducer struct{ client mq.Producer }

func OpenNameServerProducer(ctx context.Context, address, group string) (Producer, error) {
	addresses, err := resolveNameServer(ctx, address)
	if err != nil {
		return nil, err
	}
	client, err := mq.NewProducer(producer.WithNameServer(addresses), producer.WithVIPChannel(false),
		producer.WithGroupName(group), producer.WithRetry(0))
	if err != nil {
		return nil, err
	}
	if err := client.Start(); err != nil {
		return nil, err
	}
	return nameServerProducer{client}, nil
}

func (p nameServerProducer) Send(ctx context.Context, message Message) error {
	if message.DeliverAt != nil {
		return errors.New("absolute delivery time requires a Proxy producer")
	}
	receipt, err := p.client.SendSync(ctx, primitive.NewMessage(message.Topic, message.Body).WithKeys([]string{message.Key}))
	if err != nil {
		return err
	}
	if receipt.Status != primitive.SendOK {
		return fmt.Errorf("RocketMQ send status %v", receipt.Status)
	}
	return nil
}

func (p nameServerProducer) Close() error { return p.client.Shutdown() }

type proxyProducer struct{ client mq5.Producer }

func OpenProxyProducer(endpoint string, topics ...string) (Producer, error) {
	client, err := mq5.NewProducer(proxyConfig(endpoint, ""), mq5.WithTopics(topics...))
	if err != nil {
		return nil, err
	}
	if err := client.Start(); err != nil {
		return nil, err
	}
	return proxyProducer{client}, nil
}

func (p proxyProducer) Send(ctx context.Context, message Message) error {
	payload := mq5.Message{Topic: message.Topic, Body: message.Body}
	payload.SetKeys(message.Key)
	if message.DeliverAt != nil {
		payload.SetDelayTimestamp(*message.DeliverAt)
	}
	_, err := p.client.Send(ctx, &payload)
	return err
}

func (p proxyProducer) Close() error { return p.client.GracefulStop() }

func proxyConfig(endpoint, group string) *mq5.Config {
	return &mq5.Config{Endpoint: endpoint, ConsumerGroup: group, Credentials: &credentials.SessionCredentials{}}
}

var nameServerLogger sync.Once

func resolveNameServer(ctx context.Context, address string) (primitive.NamesrvAddr, error) {
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
	nameServerLogger.Do(func() { rlog.SetLogLevel("error") })
	return addresses, nil
}
