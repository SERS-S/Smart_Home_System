package main

import (
	"fmt"
	"testing"

	"smart-home/control/internal/device"
)

func TestExternalMotorCommandsCancelOldRetriesWithoutEcho(t *testing.T) {
	h := setup(t)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/signal", `{"signal":"connect"}`)
	own := post(t, h, "/api/motor/up", 202)
	pending := take(t, h)
	ownPayload := fmt.Sprintf(`{"operation_id":%d,"level":100}`, own.OperationID)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/up", ownPayload)
	if h.web.Snapshot().Motor.Operation.MaxAttempts != 30 {
		t.Fatal("own MQTT echo replaced its retry policy")
	}
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/down", `{"operation_id":42,"level":35}`)
	state := h.web.Snapshot().Motor
	if pending.Context.Err() == nil || state.Operation.ID != 42 || state.Operation.MaxAttempts != 1 || *state.TargetLevel != 35 {
		t.Fatal("external command was not adopted")
	}
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/up", `{"operation_id":43,"level":75}`)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/down", `{"operation_id":42,"level":35}`)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/up", ownPayload)
	if h.web.Snapshot().Motor.Operation.ID != 43 {
		t.Fatal("an old echo/duplicate cancelled the newer operation")
	}
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", `{"operation_id":43,"state":"up"}`)
	state = h.web.Snapshot().Motor
	if state.Operation.Status != "confirmed" || *state.Level != 75 {
		t.Fatal("external feedback did not update the twin")
	}
	_ = h.motor.Deliver(h.ctx, device.Message{Topic: h.cfg.Topics.Motor + "/command/down", Payload: []byte(`{"operation_id":44,"level":0}`), Retained: true})
	_, _ = h.motor.Call(h.ctx, "barrier")
	if h.web.Snapshot().Motor.Operation.ID != 43 {
		t.Fatal("retained command was replayed")
	}
	select {
	case <-h.pub.sent:
		t.Fatal("an observed command was published again")
	default:
	}
}
