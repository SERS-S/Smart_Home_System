package mqtt

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"smart-home/control/internal/command"
	"smart-home/control/internal/config"
)

type token struct{ done chan struct{} }

func (t token) Done() <-chan struct{}          { return t.done }
func (t token) Wait() bool                     { <-t.done; return true }
func (t token) WaitTimeout(time.Duration) bool { return t.Wait() }
func (t token) Error() error                   { return nil }

type publication struct {
	topic    string
	qos      byte
	retained bool
}
type client struct {
	paho.Client
	mu   sync.Mutex
	sent []publication
}

func (c *client) IsConnectionOpen() bool { return true }
func (c *client) Publish(topic string, qos byte, retained bool, _ any) paho.Token {
	c.mu.Lock()
	c.sent = append(c.sent, publication{topic, qos, retained})
	c.mu.Unlock()
	done := make(chan struct{})
	close(done)
	return token{done}
}

func TestPublisherSkipsCancelledQueueAndNeverRetainsCommands(t *testing.T) {
	transport := New(config.Config{QueueSize: 8, QoS: 1, PublishTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fake := &client{}
	transport.client = fake
	transport.ready.Store(true)
	cancelled, stop := context.WithCancel(context.Background())
	result := make(chan command.PublishResult, 2)
	// Cancel after enqueue, but before the publisher takes the request.
	if err := transport.Enqueue(command.Publication{Context: cancelled, ID: 1, Topic: "cancelled", Result: result}); err != nil {
		t.Fatal(err)
	}
	stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); transport.publishLoop(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("publisher leaked")
		}
	})
	if err := transport.Enqueue(command.Publication{Context: ctx, ID: 2, Attempt: 1, Topic: "motor/home/command/up", Payload: []byte("{}"), Result: result}); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-result:
		if reply.ID != 2 || reply.Err != nil {
			t.Fatalf("unexpected result: %+v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("publication did not complete")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sent) != 1 || fake.sent[0].topic != "motor/home/command/up" || fake.sent[0].qos != 1 || fake.sent[0].retained {
		t.Fatalf("incorrect wire publication: %+v", fake.sent)
	}
}
