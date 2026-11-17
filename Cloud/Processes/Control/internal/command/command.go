// Package command tracks one operation at a time. All Tracker methods are
// called by the owning device goroutine; publishing is asynchronous.
package command

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"smart-home/control/internal/config"
)

type State struct {
	ID          uint64 `json:"operation_id"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts"`
	LastError   string `json:"last_error,omitempty"`
}

type PublishResult struct {
	ID      uint64
	Attempt int
	Err     error
}
type Publication struct {
	Context context.Context
	ID      uint64
	Attempt int
	Topic   string
	Payload []byte
	Result  chan<- PublishResult
}

type Publisher interface {
	Ready() bool
	Enqueue(Publication) error
}

type Tracker struct {
	public      State
	active      bool
	ackRequired bool
	policy      config.Retry
	publisher   Publisher
	topic       string
	payload     []byte
	ctx         context.Context
	cancel      context.CancelFunc
	timer       *time.Timer
	results     chan PublishResult
}

func New(publisher Publisher) *Tracker {
	return &Tracker{publisher: publisher, results: make(chan PublishResult, 1)}
}
func (t *Tracker) Snapshot() *State {
	if t.public.ID == 0 {
		return nil
	}
	copy := t.public
	return &copy
}
func (t *Tracker) Results() <-chan PublishResult { return t.results }
func (t *Tracker) Timer() <-chan time.Time {
	if t.timer == nil {
		return nil
	}
	return t.timer.C
}
func (t *Tracker) Ready() bool { return t.publisher.Ready() }

func (t *Tracker) Start(ctx context.Context, action, topic string, policy config.Retry, ack bool, payload func(uint64) any) (uint64, error) {
	var bytes [8]byte
	var id uint64
	for id == 0 || id == t.public.ID {
		if _, err := rand.Read(bytes[:]); err != nil {
			return 0, err
		}
		// JSON numbers remain exact in a JavaScript Number and cJSON double.
		id = binary.LittleEndian.Uint64(bytes[:]) & ((1 << 53) - 1)
	}
	body, err := json.Marshal(payload(id))
	if err != nil {
		return 0, err
	}
	t.Cancel()
	t.ctx, t.cancel = context.WithCancel(ctx)
	t.public = State{ID: id, Action: action, Status: "pending", MaxAttempts: policy.Attempts}
	t.active, t.ackRequired, t.policy = true, ack, policy
	t.topic, t.payload = topic, body
	t.Tick()
	return id, nil
}

func (t *Tracker) stopTimer() {
	if t.timer != nil {
		if !t.timer.Stop() {
			select {
			case <-t.timer.C:
			default:
			}
		}
		t.timer = nil
	}
}
func (t *Tracker) schedule() { t.stopTimer(); t.timer = time.NewTimer(t.policy.Interval) }

func (t *Tracker) finish(status string) {
	t.public.Status, t.active = status, false
	t.stopTimer()
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *Tracker) Cancel() {
	if t.active {
		t.finish("cancelled")
	} else {
		t.stopTimer()
	}
}

func (t *Tracker) Tick() {
	if !t.active {
		return
	}
	if t.ctx.Err() != nil {
		t.Cancel()
		return
	}
	if !t.publisher.Ready() {
		t.schedule()
		return
	}
	if t.public.Attempts >= t.public.MaxAttempts {
		t.finish("timed_out")
		return
	}
	t.public.Attempts++
	err := t.publisher.Enqueue(Publication{t.ctx, t.public.ID, t.public.Attempts, t.topic, append([]byte(nil), t.payload...), t.results})
	t.public.LastError = ""
	if err != nil {
		t.public.LastError = err.Error()
	}
	if err != nil && !t.ackRequired {
		t.finish("failed")
		return
	}
	t.schedule()
}

func (t *Tracker) Broker(online bool) {
	// In particular, send a command immediately if connect arrived during SUBACK.
	if online && t.active && t.public.Attempts == 0 {
		t.Tick()
	}
}

func (t *Tracker) HandleResult(result PublishResult) {
	if !t.active || result.ID != t.public.ID || result.Attempt != t.public.Attempts {
		return
	}
	if result.Err != nil {
		t.public.LastError = result.Err.Error()
		if !t.ackRequired {
			t.finish("failed")
		}
	} else if !t.ackRequired {
		t.finish("sent") // Broker delivery, not device completion.
	}
}

func (t *Tracker) Matches(id uint64) bool { return t.active && t.public.ID == id }
func (t *Tracker) Confirm(id uint64) bool {
	if !t.Matches(id) {
		return false
	}
	t.public.LastError = ""
	t.finish("confirmed")
	return true
}

func DecodeFeedback(payload []byte, target any) error {
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("invalid feedback: %w", err)
	}
	return nil
}
