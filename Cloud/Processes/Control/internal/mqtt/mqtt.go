// Package mqtt owns the broker connection, ordered routing and publication queue.
package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"smart-home/control/internal/command"
	"smart-home/control/internal/config"
	"smart-home/control/internal/device"
)

type Receiver interface {
	Deliver(context.Context, device.Message) error
}

// Receivers fan a single MQTT stream out to independent device inboxes.
type Receivers []Receiver

func (receivers Receivers) Deliver(ctx context.Context, message device.Message) error {
	for _, receiver := range receivers {
		if err := receiver.Deliver(ctx, message); err != nil {
			return err
		}
	}
	return nil
}

type Transport struct {
	cfg      config.Config
	logger   *slog.Logger
	client   paho.Client
	ready    atomic.Bool
	outgoing chan command.Publication
	routes   map[string]Receiver
}

func New(c config.Config, logger *slog.Logger) *Transport {
	return &Transport{cfg: c, logger: logger, outgoing: make(chan command.Publication, c.QueueSize), routes: make(map[string]Receiver)}
}

// Route is called during initialization, before Run starts.
func (t *Transport) Route(topic string, receiver Receiver) { t.routes[topic] = receiver }
func (t *Transport) Ready() bool                           { return t.ready.Load() }
func (t *Transport) Enqueue(p command.Publication) error {
	if err := p.Context.Err(); err != nil {
		return err
	}
	if !t.Ready() {
		return device.ErrUnavailable
	}
	select {
	case t.outgoing <- p:
		return nil
	default:
		return device.ErrQueueFull
	}
}

func (t *Transport) Run(parent context.Context, onConnection func(bool)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	fatal := make(chan error, 1)
	fail := func(err error) {
		select {
		case fatal <- err:
		default:
		}
	}
	var connectionMu sync.Mutex
	// Serialize connection notifications and publish a snapshot after resubscription.
	setOnline := func(online bool) {
		connectionMu.Lock()
		defer connectionMu.Unlock()
		if t.ready.Swap(online) != online && onConnection != nil {
			onConnection(online)
		}
	}
	options := paho.NewClientOptions().AddBroker(t.cfg.MQTTAddress).SetClientID(t.cfg.ClientID).
		SetCleanSession(true).SetOrderMatters(true).SetAutoReconnect(true).SetConnectRetry(true).
		SetConnectRetryInterval(t.cfg.ReconnectInterval).SetMaxReconnectInterval(t.cfg.ReconnectInterval).
		SetConnectTimeout(t.cfg.PublishTimeout).SetWriteTimeout(t.cfg.PublishTimeout).SetKeepAlive(t.cfg.KeepAlive)
	options.SetOnConnectHandler(func(client paho.Client) {
		connectionMu.Lock()
		defer connectionMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		filters := make(map[string]byte)
		for _, topic := range t.cfg.Topics.Subscriptions() {
			filters[topic] = t.cfg.QoS
		}
		token := client.SubscribeMultiple(filters, func(_ paho.Client, message paho.Message) {
			if receiver := t.routes[message.Topic()]; receiver != nil {
				if err := receiver.Deliver(ctx, device.Message{Topic: message.Topic(), Payload: message.Payload(), Retained: message.Retained()}); err != nil && ctx.Err() == nil {
					fail(fmt.Errorf("MQTT routing: %w", err))
				}
			}
		})
		if err := wait(ctx, token, t.cfg.PublishTimeout); err != nil {
			if ctx.Err() == nil {
				fail(fmt.Errorf("MQTT subscriptions: %w", err))
			}
			return
		}
		if subscribed, ok := token.(*paho.SubscribeToken); ok {
			for _, qos := range subscribed.Result() {
				if qos == 0x80 {
					fail(fmt.Errorf("MQTT broker rejected a subscription"))
					return
				}
			}
		}
		if ctx.Err() != nil || !client.IsConnectionOpen() {
			return
		}
		t.ready.Store(true)
		if onConnection != nil {
			onConnection(true)
		}
		t.logger.Info("MQTT connected and subscribed")
	})
	options.SetConnectionLostHandler(func(_ paho.Client, err error) {
		setOnline(false)
		t.logger.Warn("MQTT connection lost", "error", err)
	})
	t.client = paho.NewClient(options)
	publisherDone := make(chan struct{})
	go func() { defer close(publisherDone); t.publishLoop(ctx) }()
	token := t.client.Connect()
	var err error
	select {
	case <-ctx.Done():
	case err = <-fatal:
	case <-token.Done():
		if err = token.Error(); err == nil {
			select {
			case <-ctx.Done():
			case err = <-fatal:
			}
		}
	}
	cancel() // Unblocks inbox delivery and connection callbacks before Disconnect waits.
	t.ready.Store(false)
	t.client.Disconnect(uint(t.cfg.PublishTimeout.Milliseconds()))
	<-publisherDone
	return err
}

func wait(ctx context.Context, token paho.Token, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("MQTT acknowledgement timeout")
	case <-token.Done():
		return token.Error()
	}
}

func (t *Transport) publishLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case publication := <-t.outgoing:
			if publication.Context.Err() != nil {
				continue
			}
			err := device.ErrUnavailable
			if t.Ready() && t.client.IsConnectionOpen() {
				// Commands must never be retained and replayed to a new connection.
				token := t.client.Publish(publication.Topic, t.cfg.QoS, false, publication.Payload)
				err = wait(publication.Context, token, t.cfg.PublishTimeout)
			}
			result := command.PublishResult{ID: publication.ID, Attempt: publication.Attempt, Err: err}
			select {
			case publication.Result <- result:
			case <-publication.Context.Done():
			case <-ctx.Done():
				return
			}
		}
	}
}
