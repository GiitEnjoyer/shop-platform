package orders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// AMQPPublisher publishes persistent messages with publisher confirms and
// reconnects lazily after broker restarts.
type AMQPPublisher struct {
	url      string
	exchange string

	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

func NewAMQPPublisher(url, exchange string) *AMQPPublisher {
	return &AMQPPublisher{url: url, exchange: exchange}
}

func (p *AMQPPublisher) connect() error {
	if p.conn != nil && !p.conn.IsClosed() && p.ch != nil && !p.ch.IsClosed() {
		return nil
	}
	conn, err := amqp.DialConfig(p.url, amqp.Config{Heartbeat: 10 * time.Second, Properties: amqp.Table{"connection_name": "orders"}})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("channel: %w", err)
	}
	if err := ch.ExchangeDeclare(p.exchange, "topic", true, false, false, false, nil); err != nil {
		conn.Close()
		return fmt.Errorf("declare exchange: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		conn.Close()
		return fmt.Errorf("confirm mode: %w", err)
	}
	p.conn, p.ch = conn, ch
	return nil
}

func (p *AMQPPublisher) Publish(ctx context.Context, routingKey string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.connect(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	conf, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, p.exchange, routingKey, true, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Body:         body,
	})
	if err != nil {
		return err
	}
	ok, err := conf.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("broker nacked message")
	}
	return nil
}

// Ping is a readiness check: the connection must be open.
func (p *AMQPPublisher) Ping(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.connect()
}

func (p *AMQPPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}
