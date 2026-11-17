package command

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"smart-home/control/internal/config"
)

type publisher struct {
	online bool
	sent   []Publication
}

func (p *publisher) Ready() bool                       { return p.online }
func (p *publisher) Enqueue(message Publication) error { p.sent = append(p.sent, message); return nil }

func start(t *testing.T, tracker *Tracker, attempts int, ack bool) uint64 {
	t.Helper()
	id, err := tracker.Start(context.Background(), "start", "device/start", config.Retry{Interval: time.Hour, Attempts: attempts}, ack,
		func(id uint64) any { return map[string]uint64{"operation_id": id} })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracker.Cancel)
	return id
}

func TestRetryBudgetsAndStableOperation(t *testing.T) {
	for _, limit := range []int{30, 45} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			p := &publisher{online: true}
			tracker := New(p)
			id := start(t, tracker, limit, true)
			for n := 1; n < limit; n++ {
				tracker.Tick()
			}
			if len(p.sent) != limit || tracker.Snapshot().Status != "pending" {
				t.Fatalf("last attempt must still wait for feedback: %+v", tracker.Snapshot())
			}
			for n, message := range p.sent {
				if message.ID != id || message.Attempt != n+1 || !bytes.Equal(message.Payload, p.sent[0].Payload) {
					t.Fatal("retry changed the operation")
				}
			}
			tracker.Tick()
			if len(p.sent) != limit || tracker.Snapshot().Status != "timed_out" || tracker.Timer() != nil {
				t.Fatalf("retry budget not enforced: %+v", tracker.Snapshot())
			}
			if p.sent[0].Context.Err() == nil {
				t.Fatal("queued publications were not cancelled")
			}
			if tracker.Confirm(id) {
				t.Fatal("expired operation accepted late feedback")
			}
		})
	}
}

func TestReplacementConfirmationAndPause(t *testing.T) {
	p := &publisher{online: false}
	tracker := New(p)
	old := start(t, tracker, 45, true)
	for range 10 {
		tracker.Tick()
	}
	if tracker.Snapshot().Attempts != 0 || len(p.sent) != 0 {
		t.Fatal("offline time spent attempts")
	}
	p.online = true
	tracker.Broker(true)
	if len(p.sent) != 1 {
		t.Fatal("initial command was not sent on broker readiness")
	}
	oldContext := p.sent[0].Context
	current := start(t, tracker, 30, true)
	if current == old || current == 0 || current > 1<<53-1 {
		t.Fatal("invalid operation ID")
	}
	if oldContext.Err() == nil {
		t.Fatal("superseded publication remained active")
	}
	tracker.HandleResult(PublishResult{ID: old, Attempt: 1, Err: errors.New("old failure")})
	if tracker.Confirm(old) || tracker.Snapshot().LastError != "" {
		t.Fatal("old operation affected current state")
	}
	if !tracker.Confirm(current) || tracker.Snapshot().Status != "confirmed" {
		t.Fatal("matching acknowledgement rejected")
	}
	tracker.Tick()
	if len(p.sent) != 2 {
		t.Fatal("confirmed command was repeated")
	}
}

func TestStopIsSentOnceWithoutClaimingDeviceCompletion(t *testing.T) {
	p := &publisher{online: true}
	tracker := New(p)
	id := start(t, tracker, 1, false)
	tracker.HandleResult(PublishResult{ID: id, Attempt: 1})
	tracker.Tick()
	if len(p.sent) != 1 || tracker.Snapshot().Status != "sent" {
		t.Fatal("stop was repeated or reported as confirmed")
	}
}
