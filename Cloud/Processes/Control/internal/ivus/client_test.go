package ivus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"smart-home/control/internal/config"
	"smart-home/control/internal/webserver"
)

func TestWakeWord(t *testing.T) {
	for _, text := range []string{"Ивус", "ИВУС, привет", "ивус!", "что такое MQTT, иВуС?"} {
		if !HasWakeWord(text) {
			t.Errorf("did not wake: %s", text)
		}
	}
	for _, text := range []string{"привет", "Ivus", "IVUS", "ivus", "iVuS", "мойИвус", "Ивус123", "ивус_foo", "Івус", "Ивуc"} {
		if HasWakeWord(text) {
			t.Errorf("unexpected wake: %s", text)
		}
	}
}

func TestForwardOneWayWithFreshSnapshotsAndDuplicateWindow(t *testing.T) {
	path := fmt.Sprintf(".ivus-%d.sock", time.Now().UnixNano())
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	frames := make(chan request, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetReadDeadline(time.Now().Add(time.Second))
			var frame request
			err = json.NewDecoder(connection).Decode(&frame)
			// No response is sent; Forward must finish without waiting for one.
			_, _ = io.Copy(io.Discard, connection)
			connection.Close()
			if err == nil {
				frames <- frame
			}
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	c := config.Config{IvusEnabled: true, IvusSocketPath: path, IvusWriteTimeout: time.Second,
		IvusDedupWindow: time.Minute, QueueSize: 8, HabID: "hab-01"}
	state := webserver.State{MQTTConnected: true}
	value, unit, measured := 345.7, "lux", time.Now().UTC()
	state.Light.Illuminance, state.Light.Unit, state.Light.Timestamp = &value, &unit, &measured
	state.Light.DeviceID = "light-01"
	state.Motor.ConnectionKnown, state.Motor.Connected = true, true
	position := 75
	state.Motor.Level = &position
	client := New(c, func() webserver.State { return state })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, text := range []string{"Без обращения", "Ivus, привет", "мойИвус", "ивус123"} {
		if err := client.Forward(ctx, text); err != nil {
			t.Fatal(err)
		}
	}
	text := "иВуС,  какой свет за окном?"
	if err := client.Forward(ctx, text); err != nil {
		t.Fatal(err)
	}
	first := receive(t, frames)
	if first.Text != text || first.Source != "voice" || first.SessionID != c.HabID || first.RequestID == "" {
		t.Fatal("voice envelope changed")
	}
	if !first.Telemetry.Illuminance.Available || *first.Telemetry.Illuminance.Value != value || first.Telemetry.Temperature.Available || *first.Curtain.Position != 75 {
		t.Fatal("incorrect snapshot")
	}
	state.MQTTConnected = false
	if err := client.Forward(ctx, "Ивус, другой вопрос"); err != nil {
		t.Fatal(err)
	}
	second := receive(t, frames)
	if second.RequestID == first.RequestID || second.Telemetry.Illuminance.Available || second.Curtain.Connected || second.Curtain.Position != nil {
		t.Fatal("new request reused stale state or identity")
	}
	// Duplicates can be separated by other utterances inside the window.
	if err := client.Forward(ctx, "ИВУС, какой свет за окном?"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-frames:
		t.Fatal("duplicate/wake-word-free text was forwarded")
	case <-time.After(25 * time.Millisecond):
	}
}

func receive(t *testing.T, frames <-chan request) request {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(time.Second):
		t.Fatal("no UNIX frame received")
		return request{}
	}
}
