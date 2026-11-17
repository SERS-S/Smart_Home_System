package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"smart-home/control/internal/command"
	"smart-home/control/internal/device"
	"smart-home/control/internal/hab"
	"smart-home/control/internal/mqtt"
	"smart-home/control/internal/webserver"
)

func feed(t *testing.T, h *harness, temperature bool, value float64, serial int, stamp time.Time) command.Publication {
	t.Helper()
	topic, id, field, unit, inbox := h.cfg.Topics.Light, h.cfg.LightID, "illuminance", "lux", h.light.Inbox
	if temperature {
		topic, id, field, unit, inbox = h.cfg.Topics.Temperature, h.cfg.TemperatureID, "temperature", "celsius", h.temperature.Inbox
	}
	data, _ := json.Marshal(map[string]any{"device_id": id, field: value, "unit": unit, "serial": serial, "timestamp": stamp.Format(time.RFC3339)})
	if err := (mqtt.Receivers{inbox, h.hab}).Deliver(h.ctx, device.Message{Topic: topic + "/data", Payload: data}); err != nil {
		t.Fatal(err)
	}
	_, _ = inbox.Call(h.ctx, "barrier")
	_, _ = h.hab.Call(h.ctx, "barrier")
	return take(t, h)
}

func TestHabPublishesBoundedHistoriesInArrivalOrder(t *testing.T) {
	h := setup(t)
	stamp := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 25; index++ {
		for _, temperature := range []bool{false, true} {
			publication := feed(t, h, temperature, float64(index), index, stamp.Add(time.Duration(index)*time.Second))
			topic, nowKey, listKey := h.cfg.Topics.Hab+"/data/light", "now_lux", "list_lux"
			if temperature {
				topic, nowKey, listKey = h.cfg.Topics.Hab+"/data/temperature", "now_cels", "list_cels"
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(publication.Payload, &body); err != nil {
				t.Fatal(err)
			}
			if publication.Topic != topic || len(body) != 2 {
				t.Fatalf("wire format: %s %s", publication.Topic, publication.Payload)
			}
			var current float64
			var points []hab.Point
			if err := json.Unmarshal(body[nowKey], &current); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body[listKey], &points); err != nil {
				t.Fatal(err)
			}
			count := min(index+1, 20)
			if current != float64(index) || len(points) != count || points[0].Value != float64(index-count+1) || points[len(points)-1].Value != current {
				t.Fatalf("wrong history: %s", publication.Payload)
			}
			var wirePoints []map[string]any
			_ = json.Unmarshal(body[listKey], &wirePoints)
			if _, ok := wirePoints[0]["timestamp"].(string); !ok {
				t.Fatal("timestamp is not a JSON string")
			}
		}
	}
	_ = feed(t, h, false, 123, 26, stamp.Add(-time.Hour))
	snapshot := h.web.Snapshot()
	if *snapshot.Hab.NowLux != 123 || len(snapshot.Hab.ListLux) != 20 || snapshot.Hab.ListLux[0].Value != 6 || *snapshot.Hab.NowCels != 24 {
		t.Fatal("histories were sorted or mixed")
	}
	snapshot.Hab.ListLux[0].Value = -100
	*snapshot.Hab.NowLux = -100
	if h.web.Snapshot().Hab.ListLux[0].Value != 6 || *h.web.Snapshot().Hab.NowLux != 123 {
		t.Fatal("history snapshot aliases device state")
	}
	before := h.web.Snapshot().Revision
	send(t, h, h.hab.Inbox, h.cfg.Topics.Light+"/data", `{"device_id":"wrong","illuminance":999,"unit":"lux","serial":1,"timestamp":"2026-10-11T12:00:00Z"}`)
	if h.web.Snapshot().Revision != before {
		t.Fatal("invalid sample changed hub")
	}
	select {
	case <-h.pub.sent:
		t.Fatal("invalid sample was republished")
	default:
	}
}

func TestHabDataRetriesWithoutAppendingPoints(t *testing.T) {
	h := setup(t)
	first := feed(t, h, false, 12, 1, time.Now())
	first.Result <- command.PublishResult{ID: first.ID, Attempt: first.Attempt, Err: errors.New("broker unavailable")}
	retry := take(t, h)
	if string(retry.Payload) != string(first.Payload) || retry.Topic != first.Topic {
		t.Fatal("failed delivery did not retry the same history")
	}
	if len(h.web.Snapshot().Hab.ListLux) != 1 {
		t.Fatal("retry appended a history point")
	}
	h.pub.online.Store(false)
	send(t, h, h.hab.Inbox, h.cfg.Topics.Temperature+"/data", `{"device_id":"temperature_sensor_01","temperature":0,"unit":"celsius","serial":1,"timestamp":"2026-10-11T12:00:00Z"}`)
	if p := h.web.Snapshot().Hab.NowCels; p == nil || *p != 0 {
		t.Fatal("offline publication lost current value")
	}
	h.pub.online.Store(true)
	h.hab.Broker(h.ctx, true)
	recovered := take(t, h)
	if recovered.Topic != h.cfg.Topics.Hab+"/data/temperature" {
		t.Fatal("pending data not delivered after reconnect")
	}
}

func TestBatteryInfoUpdatesBothTwins(t *testing.T) {
	h := setup(t)
	send(t, h, h.light.Inbox, h.cfg.Topics.Light+"/info", `{"device_id":"light_sensor_01","battery_mv":3.3,"battery":77,"firmware_version":0.1}`)
	send(t, h, h.temperature.Inbox, h.cfg.Topics.Temperature+"/info", `{"device_id":"temperature_sensor_01","battery_mv":3300,"battery":60,"firmware_version":"0.2"}`)
	state := h.web.Snapshot()
	if *state.Light.BatteryMV != 3.3 || *state.Light.BatteryPercent != 77 || *state.Temperature.BatteryMV != 3300 || *state.Temperature.BatteryPercent != 60 {
		t.Fatal("battery fields did not update")
	}
	send(t, h, h.light.Inbox, h.cfg.Topics.Light+"/info", `{"device_id":"light_sensor_01","voltage":3.4,"version":"0.2"}`)
	if s := h.web.Snapshot().Light; *s.BatteryMV != 3400 || *s.BatteryPercent != 77 {
		t.Fatal("partial legacy info cleared battery")
	}
	send(t, h, h.light.Inbox, h.cfg.Topics.Light+"/info", `{"device_id":"light_sensor_01","battery_mv":5000,"battery":101}`)
	if *h.web.Snapshot().Light.BatteryMV != 3400 {
		t.Fatal("invalid info partially mutated state")
	}
}

func postLevel(t *testing.T, h *harness, path, body string, want int) device.Result {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.web.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if recorder.Code != want {
		t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
	}
	var result device.Result
	if want == http.StatusAccepted {
		_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	}
	return result
}

func waitState(t *testing.T, h *harness, predicate func(webserver.State) bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !predicate(h.web.Snapshot()) {
		select {
		case <-deadline.C:
			t.Fatal("state did not converge")
		case <-ticker.C:
		}
	}
}

func journalEntry(t *testing.T, h *harness, text string) {
	t.Helper()
	p := take(t, h)
	if p.Topic != h.cfg.Topics.Journal {
		t.Fatalf("wanted journal, got %s", p.Topic)
	}
	var payload map[string]string
	if err := json.Unmarshal(p.Payload, &payload); err != nil || len(payload) != 1 || payload["text"] != text {
		t.Fatalf("unexpected journal payload: %s", p.Payload)
	}
	p.Result <- command.PublishResult{ID: p.ID, Attempt: 1}
	waitState(t, h, func(s webserver.State) bool {
		for _, entry := range s.Journal {
			if entry.Text == text && entry.Delivery == "sent" {
				return true
			}
		}
		return false
	})
}

func TestJournalUsesConfirmedActionsAndStopRequests(t *testing.T) {
	h := setup(t, true)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/signal", `{"signal":"connect"}`)
	result := postLevel(t, h, "/api/motor/up", `{"level":35}`, http.StatusAccepted)
	movement := take(t, h)
	if !strings.Contains(string(movement.Payload), `"level":35`) {
		t.Fatal("custom level not sent")
	}
	select {
	case <-h.pub.sent:
		t.Fatal("unconfirmed movement logged as completed")
	default:
	}
	feedback := fmt.Sprintf(`{"operation_id":%d,"state":"up"}`, result.OperationID)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", feedback)
	journalEntry(t, h, "Штора поднята до 35%")
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", feedback)
	result = postLevel(t, h, "/api/motor/down", `{"level":10}`, http.StatusAccepted)
	_ = take(t, h)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/command/feedback", fmt.Sprintf(`{"operation_id":%d,"state":"down"}`, result.OperationID))
	journalEntry(t, h, "Штора опущена до 10%")
	if level := h.web.Snapshot().Motor.Level; level == nil || *level != 10 {
		t.Fatal("confirmed target not displayed")
	}

	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/signal", `{"signal":"connect"}`)
	start := take(t, h)
	send(t, h, h.hab.Inbox, h.cfg.Topics.Hab+"/voice/command/feedback", fmt.Sprintf(`{"operation_id":%d,"completed":true}`, start.ID))
	journalEntry(t, h, "Микрофон на хабе включён")
	post(t, h, "/api/hab/voice/stop", http.StatusAccepted)
	stop := take(t, h)
	if stop.Topic != h.cfg.Topics.Hab+"/voice/command/stop" {
		t.Fatal("stop not sent")
	}
	journalEntry(t, h, "Запрошено выключение микрофона на хабе")
	stop.Result <- command.PublishResult{ID: stop.ID, Attempt: 1}
	waitState(t, h, func(s webserver.State) bool { return s.Hab.Operation.Status == "sent" })
	if len(h.web.Snapshot().Journal) != 4 {
		t.Fatal("publish result duplicated a journal event")
	}
	post(t, h, "/api/hab/voice/start", http.StatusAccepted)
	manual := take(t, h)
	if manual.Topic != h.cfg.Topics.Hab+"/voice/command/start" || manual.ID == start.ID {
		t.Fatal("microphone cannot be enabled manually")
	}
	select {
	case <-h.pub.sent:
		t.Fatal("manual start logged before confirmation")
	default:
	}
}

func TestMotorRejectsInvalidLevelsWithoutPublishing(t *testing.T) {
	h := setup(t)
	send(t, h, h.motor.Inbox, h.cfg.Topics.Motor+"/signal", `{"signal":"connect"}`)
	for _, body := range []string{`{"level":-1}`, `{"level":101}`, `{"level":1.5}`, `{"level":"50"}`, `{"unknown":50}`, `{"level":50}{}`} {
		postLevel(t, h, "/api/motor/up", body, http.StatusBadRequest)
	}
	select {
	case <-h.pub.sent:
		t.Fatal("invalid level published a command")
	default:
	}
}

func TestEmbeddedDashboard(t *testing.T) {
	for _, path := range []string{"static/index.html", "static/style.css", "static/idx.js"} {
		data, err := fs.ReadFile(assets, path)
		if err != nil || len(data) == 0 {
			t.Fatalf("missing embedded asset %s: %v", path, err)
		}
	}
	data, _ := fs.ReadFile(assets, "static/index.html")
	if strings.Contains(string(data), "led-toggle") || strings.Contains(string(data), "auto-toggle") {
		t.Fatal("LED control or working automation unexpectedly present")
	}
}
