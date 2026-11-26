// Package kafkax 封装消息投递：真实 Kafka 或本地 Noop（仅打日志，保证通路联调）。
package kafkax

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	"go.uber.org/zap"

	"pet-translate-gateway/config"
	"pet-translate-gateway/internal/model"
)

// Publisher 音频/定位消息投递接口（Kafka 可关闭时用 Noop 实现）。
type Publisher interface {
	SendAudio(ctx context.Context, msg *model.AudioMessage) error
	SendLocation(ctx context.Context, msg *model.LocationMessage) error
	Close() error
}

// NewPublisher 按配置创建投递器：enabled=false 时返回 Noop，不连接 Kafka。
func NewPublisher(cfg config.KafkaConfig, log *zap.Logger) (Publisher, error) {
	if !cfg.Enabled {
		log.Warn("Kafka 已关闭，使用 Noop 投递（消息仅写日志）")
		return NewNoop(log), nil
	}
	return NewProducer(cfg, log)
}

// Producer 真实 Kafka 同步生产者。
type Producer struct {
	producer      sarama.SyncProducer
	topicAudio    string
	topicLocation string
	timeout       time.Duration
	log           *zap.Logger
}

// NewProducer 创建 Kafka 生产者。
func NewProducer(cfg config.KafkaConfig, log *zap.Logger) (*Producer, error) {
	saramaCfg := sarama.NewConfig()
	saramaCfg.Producer.Return.Successes = true
	saramaCfg.Producer.Return.Errors = true
	saramaCfg.Producer.RequiredAcks = sarama.WaitForLocal
	saramaCfg.Producer.Retry.Max = 3
	saramaCfg.Producer.Timeout = cfg.Timeout
	saramaCfg.Version = sarama.V2_8_0_0

	p, err := sarama.NewSyncProducer(cfg.Brokers, saramaCfg)
	if err != nil {
		return nil, fmt.Errorf("创建 kafka 生产者失败: %w", err)
	}

	return &Producer{
		producer:      p,
		topicAudio:    cfg.TopicAudio,
		topicLocation: cfg.TopicLocation,
		timeout:       cfg.Timeout,
		log:           log,
	}, nil
}

// Close 关闭生产者。
func (p *Producer) Close() error {
	return p.producer.Close()
}

// SendAudio 投递音频分片。
func (p *Producer) SendAudio(ctx context.Context, msg *model.AudioMessage) error {
	return p.sendJSON(ctx, p.topicAudio, msg.SN, msg)
}

// SendLocation 投递定位点位。
func (p *Producer) SendLocation(ctx context.Context, msg *model.LocationMessage) error {
	return p.sendJSON(ctx, p.topicLocation, msg.SN, msg)
}

func (p *Producer) sendJSON(ctx context.Context, topic, key string, v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json 序列化失败: %w", err)
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		partition int32
		offset    int64
		err       error
	}
	ch := make(chan result, 1)
	go func() {
		partition, offset, err := p.producer.SendMessage(&sarama.ProducerMessage{
			Topic: topic,
			Key:   sarama.StringEncoder(key),
			Value: sarama.ByteEncoder(body),
		})
		ch <- result{partition: partition, offset: offset, err: err}
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("kafka 发送超时: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("kafka 发送失败 topic=%s: %w", topic, r.err)
		}
		p.log.Debug("kafka消息已投递",
			zap.String("topic", topic),
			zap.String("key", key),
			zap.Int32("partition", r.partition),
			zap.Int64("offset", r.offset),
		)
		return nil
	}
}

// NoopPublisher 不连 Kafka，把消息内容打到日志，用于本地通路联调。
type NoopPublisher struct {
	log *zap.Logger
}

// NewNoop 创建 Noop 投递器。
func NewNoop(log *zap.Logger) *NoopPublisher {
	return &NoopPublisher{log: log}
}

// Close 空实现。
func (n *NoopPublisher) Close() error { return nil }

// SendAudio 仅记录日志。
func (n *NoopPublisher) SendAudio(_ context.Context, msg *model.AudioMessage) error {
	n.log.Info("noop音频转发",
		zap.String("sn", msg.SN),
		zap.Int("seq", msg.Seq),
		zap.Int("total", msg.Total),
		zap.Bool("end", msg.End),
		zap.Bool("retransmit", msg.Retransmit),
		zap.Int("opus_len", len(msg.OpusData)),
	)
	return nil
}

// SendLocation 仅记录日志。
func (n *NoopPublisher) SendLocation(_ context.Context, msg *model.LocationMessage) error {
	n.log.Info("noop定位转发",
		zap.String("sn", msg.SN),
		zap.Float64("lat", msg.LocLat),
		zap.Float64("lng", msg.LocLng),
		zap.Int("macs", len(msg.LocMacs)),
	)
	return nil
}
