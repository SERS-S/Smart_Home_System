package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"smart-home/control/internal/command"
	"smart-home/control/internal/config"
	"smart-home/control/internal/device"
	"smart-home/control/internal/hab"
	"smart-home/control/internal/journal"
	"smart-home/control/internal/light"
	"smart-home/control/internal/motor"
	"smart-home/control/internal/temperature"
	"smart-home/control/internal/webserver"
)

type testPublisher struct {
	online atomic.Bool
	sent   chan command.Publication
}

func (p *testPublisher) Ready() bool { return p.online.Load() }
func (p *testPublisher) Enqueue(message command.Publication) error {
	if !p.Ready() {
		return device.ErrUnavailable
	}
	p.sent <- message
	return nil
}

type harness struct {
	cfg         config.Config
	ctx         context.Context
	pub         *testPublisher
	web         *webserver.WebServer
	light       *light.Device
	temperature *temperature.Device
	motor       *motor.Device
	hab         *hab.Device
}

func setup(t *testing.T, withJournal ...bool) *harness {
	t.Helper()
	data, err := os.ReadFile("internal/config/testdata/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	cfg.HabRetry.Interval = time.Hour
	cfg.MotorRetry.Interval = time.Hour
	cfg.PublishTimeout = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	pub := &testPublisher{sent: make(chan command.Publication, 64)}
	pub.online.Store(true)
	web := webserver.New(fstest.MapFS{"index.html": &fstest.MapFile{}}, time.Second)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &harness{cfg: cfg, ctx: ctx, pub: pub, web: web}
	h.light = light.New(cfg, web.OnLightChanged, logger)
	h.temperature = temperature.New(cfg, web.OnTemperatureChanged, logger)
	journalWorker := journal.New(cfg, pub, web.OnJournalChanged, logger)
	journalEnabled := len(withJournal) > 0 && withJournal[0]
	h.motor = motor.New(cfg, pub, func(s motor.State, fields []string) {
		web.OnMotorChanged(s, fields)
		if journalEnabled {
			recordMotorEvent(ctx, journalWorker, s, fields)
		}
	}, logger)
	h.hab = hab.New(cfg, pub, func(s hab.State, fields []string) {
		web.OnHabChanged(s, fields)
		if journalEnabled {
			recordHabEvent(ctx, journalWorker, s, fields)
		}
	}, logger)
	web.SetCommands(webserver.Commands{MotorUp: h.motor.Up, MotorDown: h.motor.Down,
		MotorUpTo: h.motor.UpTo, MotorDownTo: h.motor.DownTo, StopVoice: h.hab.StopVoice, StartVoice: h.hab.StartVoice})
	var wg sync.WaitGroup
	work := []func(context.Context){h.light.Run, h.temperature.Run, h.motor.Run, h.hab.Run}
	if journalEnabled {
		work = append(work, journalWorker.Run)
	}
	for _, run := range work {
		wg.Add(1)
		go func(run func(context.Context)) { defer wg.Done(); run(ctx) }(run)
	}
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("device goroutines did not stop")
		}
	})
	return h
}

func send(t *testing.T, h *harness, inbox *device.Inbox, topic, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, time.Second)
	defer cancel()
	if err := inbox.Deliver(ctx, device.Message{Topic: topic, Payload: []byte(payload)}); err != nil {
		t.Fatal(err)
	}
	// An unsupported command is a barrier: the preceding message and callback
	// have completed before the actor takes this request from its FIFO inbox.
	if _, err := inbox.Call(ctx, "barrier"); err == context.DeadlineExceeded {
		t.Fatal(err)
	}
}

func take(t *testing.T, h *harness) command.Publication {
	t.Helper()
	select {
	case message := <-h.pub.sent:
		return message
	case <-time.After(time.Second):
		t.Fatal("no publication")
		return command.Publication{}
	}
}

func post(t *testing.T, h *harness, path string, status int) device.Result {
	t.Helper()
	w := httptest.NewRecorder()
	h.web.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
	if w.Code != status {
		t.Fatalf("%s: status %d, %s", path, w.Code, w.Body.String())
	}
	var result device.Result
	if status == http.StatusAccepted {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestMotorHTTPCommandsAndFeedbackIsolation(t *testing.T) {
	h := setup(t)
	post(t, h, "/api/motor/up", http.StatusConflict)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/signal", `{"signal":"connect"}`)
	select {
	case <-h.pub.sent:
		t.Fatal("motor moved automatically")
	default:
	}
	first := post(t, h, "/api/motor/up", http.StatusAccepted)
	up := take(t, h)
	var body struct {
		ID    uint64 `json:"operation_id"`
		Level int    `json:"level"`
	}
	if err := json.Unmarshal(up.Payload, &body); err != nil || body.ID != first.OperationID || body.Level != 100 || up.Topic != h.cfg.Topics.Motor+"/command/up" {
		t.Fatalf("invalid up payload: %s", up.Payload)
	}
	if h.web.Snapshot().Motor.State != "unknown" {
		t.Fatal("unconfirmed state became up")
	}
	wrong, _ := json.Marshal(map[string]any{"operation_id": first.OperationID + 1, "state": "up"})
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", string(wrong))
	if h.web.Snapshot().Motor.Operation.Status != "pending" {
		t.Fatal("unrelated feedback confirmed command")
	}
	second := post(t, h, "/api/motor/down", http.StatusAccepted)
	down := take(t, h)
	if second.OperationID == first.OperationID || up.Context.Err() == nil {
		t.Fatal("new command did not replace old operation")
	}
	old, _ := json.Marshal(map[string]any{"operation_id": first.OperationID, "state": "up"})
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", string(old))
	if h.web.Snapshot().Motor.Operation.Status != "pending" {
		t.Fatal("old acknowledgement ended new operation")
	}
	matching, _ := json.Marshal(map[string]any{"operation_id": second.OperationID, "state": "down"})
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", string(matching))
	state := h.web.Snapshot().Motor
	if state.State != "down" || state.Operation.Status != "confirmed" || down.Context.Err() == nil {
		t.Fatalf("matching acknowledgement failed: %+v", state)
	}
	post(t, h, "/api/motor/up", http.StatusAccepted)
	pending := take(t, h)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/signal", `{"signal":"disconnect"}`)
	if pending.Context.Err() == nil || h.web.Snapshot().Motor.Operation.Status != "cancelled" {
		t.Fatal("disconnect did not cancel operation")
	}
}

func TestHubConnectFeedbackStopAndReconnect(t *testing.T) {
	h := setup(t)
	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/signal", `{"signal":"connect"}`)
	first := take(t, h)
	var start struct {
		ID uint64 `json:"operation_id"`
		config.Audio
	}
	if err := json.Unmarshal(first.Payload, &start); err != nil || start.Audio != h.cfg.Audio || start.ID == 0 {
		t.Fatalf("invalid audio format: %s", first.Payload)
	}
	if h.web.Snapshot().Hab.Operation.MaxAttempts != 45 {
		t.Fatal("hub budget is not 45")
	}
	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/signal", `{"signal":"connect"}`)
	select {
	case <-h.pub.sent:
		t.Fatal("duplicate connect restarted recording")
	default:
	}
	incomplete, _ := json.Marshal(map[string]any{"operation_id": start.ID, "completed": false})
	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/voice/command/feedback", string(incomplete))
	if h.web.Snapshot().Hab.Recording != nil {
		t.Fatal("completed:false confirmed recording")
	}
	wrongTopic, _ := json.Marshal(map[string]any{"operation_id": start.ID, "state": "up"})
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", string(wrongTopic))
	if h.web.Snapshot().Hab.Operation.Status != "pending" {
		t.Fatal("motor feedback confirmed hub")
	}
	matching, _ := json.Marshal(map[string]any{"operation_id": start.ID, "completed": true})
	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/voice/command/feedback", string(matching))
	if state := h.web.Snapshot().Hab; state.Recording == nil || !*state.Recording || state.Operation.Status != "confirmed" {
		t.Fatal("hub confirmation failed")
	}
	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/signal", `{"signal":"disconnect"}`)
	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/signal", `{"signal":"connect"}`)
	second := take(t, h)
	if second.ID == first.ID {
		t.Fatal("reconnect reused the operation ID")
	}
	post(t, h, "/api/hab/voice/stop", http.StatusAccepted)
	stop := take(t, h)
	if second.Context.Err() == nil || stop.Topic != h.cfg.Topics.Hab+"/voice/command/stop" || string(stop.Payload) != "{}" {
		t.Fatal("stop did not cancel start or has wrong payload")
	}
	if h.web.Snapshot().Hab.Recording != nil {
		t.Fatal("unacknowledged stop reported a confirmed recording state")
	}
	stop.Result <- command.PublishResult{ID: stop.ID, Attempt: 1}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for h.web.Snapshot().Hab.Operation.Status != "sent" {
		select {
		case <-deadline.C:
			t.Fatal("stop result was not processed")
		case <-tick.C:
		}
	}
}

func TestSensorsAndIndependentSnapshots(t *testing.T) {
	h := setup(t)
	info := `{"device_id":"light_sensor_01","voltage":3.3,"raw_voltage":1000,"version":"0.1"}`
	send(t, h, h.light.Inbox, h.cfg.Topics.Light+"/info", info)
	state := h.web.Snapshot().Light
	if state.BatteryMV == nil || *state.BatteryMV != 3300 || state.BatteryPercent != nil || state.ConnectionKnown {
		t.Fatalf("legacy info was not normalized: %+v", state)
	}
	send(t, h, h.light.Inbox, h.cfg.Topics.Light+"/data", `{"device_id":"light_sensor_01","illuminance":345.7,"unit":"lux","serial":25,"timestamp":"2026-10-11T12:00:00Z"}`)
	state = h.web.Snapshot().Light
	if state.Illuminance == nil || *state.Illuminance != 345.7 || state.Connected {
		t.Fatal("measurement or connection semantics incorrect")
	}
	*state.Illuminance = -999
	if *h.web.Snapshot().Light.Illuminance != 345.7 {
		t.Fatal("HTTP observer can mutate twin state")
	}
	revision := h.web.Snapshot().Revision
	send(t, h, h.light.Inbox, h.cfg.Topics.Light+"/data", `{"device_id":"wrong","illuminance":7,"unit":"lux","serial":1,"timestamp":"2026-10-11T12:00:00Z"}`)
	if h.web.Snapshot().Revision != revision {
		t.Fatal("invalid message changed state")
	}
	send(t, h, h.temperature.Inbox, h.cfg.Topics.Temperature+"/data", `{"device_id":"temperature_sensor_01","temperature":0,"unit":"celsius","serial":1,"timestamp":"2026-10-11T12:00:00Z"}`)
	if p := h.web.Snapshot().Temperature.Temperature; p == nil || *p != 0 {
		t.Fatal("zero temperature was confused with unknown")
	}
	send(t, h, h.temperature.Inbox, h.cfg.Topics.Temperature+"/info", `{"device_id":"temperature_sensor_01","battery_mv":3300,"battery":77,"firmware_version":0.1}`)
	if p := h.web.Snapshot().Temperature.FirmwareVersion; p == nil || *p != "0.1" {
		t.Fatal("diagram info format was not accepted")
	}
}

func TestConcurrentDeviceUpdatesAndHTTPReads(t *testing.T) {
	h := setup(t)
	var wg sync.WaitGroup
	for _, item := range []struct {
		inbox *device.Inbox
		topic string
	}{{h.light.Inbox, h.cfg.Topics.Light}, {h.temperature.Inbox, h.cfg.Topics.Temperature}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = item.inbox.Deliver(h.ctx, device.Message{Topic: item.topic + "/signal", Payload: []byte(`{"signal":"connect"}`)})
			}
		}()
	}
	for range 100 {
		w := httptest.NewRecorder()
		h.web.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
		if w.Code != http.StatusOK || !json.Valid(w.Body.Bytes()) {
			t.Fatal("invalid concurrent snapshot")
		}
	}
	wg.Wait()
}
