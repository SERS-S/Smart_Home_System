package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"smart-home/control/internal/light"
)

type stream struct {
	header  http.Header
	buffer  bytes.Buffer
	flushed chan string
}

func (s *stream) Header() http.Header              { return s.header }
func (s *stream) WriteHeader(int)                  {}
func (s *stream) Write(data []byte) (int, error)   { return s.buffer.Write(data) }
func (s *stream) SetWriteDeadline(time.Time) error { return nil }
func (s *stream) Flush()                           { s.flushed <- s.buffer.String(); s.buffer.Reset() }

func TestSSEInitialSnapshotUpdatesAndCleanup(t *testing.T) {
	server := New(fstest.MapFS{"index.html": &fstest.MapFile{}}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &stream{header: make(http.Header), flushed: make(chan string, 4)}
	request := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); server.Handler().ServeHTTP(writer, request) }()
	next := func() State {
		t.Helper()
		select {
		case event := <-writer.flushed:
			if !strings.Contains(event, "event: state\n") {
				t.Fatalf("invalid SSE: %s", event)
			}
			_, data, ok := strings.Cut(event, "data: ")
			if !ok {
				t.Fatal("missing state data")
			}
			var state State
			if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &state); err != nil {
				t.Fatal(err)
			}
			return state
		case <-time.After(time.Second):
			t.Fatal("SSE event not delivered")
			return State{}
		}
	}
	first := next()
	value := 42.0
	server.OnLightChanged(light.State{Illuminance: &value}, []string{"illuminance"})
	second := next()
	if second.Revision <= first.Revision || second.Light.Illuminance == nil || *second.Light.Illuminance != 42 {
		t.Fatal("stale SSE snapshot")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler leaked")
	}
	server.mu.RLock()
	defer server.mu.RUnlock()
	if len(server.subscribers) != 0 {
		t.Fatal("subscriber not removed")
	}
}

func TestSlowBrowserNotificationsCoalesce(t *testing.T) {
	server := New(fstest.MapFS{}, time.Hour)
	slow := make(chan struct{}, 1)
	server.subscribers[slow] = struct{}{}
	for n := 0; n < 10000; n++ {
		value := float64(n)
		server.OnLightChanged(light.State{Illuminance: &value}, []string{"illuminance"})
	}
	if len(slow) != 1 || *server.Snapshot().Light.Illuminance != 9999 {
		t.Fatal("slow observer blocked updates or lost latest state")
	}
}

func TestHTTPMethodsAndUnavailableCommands(t *testing.T) {
	server := New(fstest.MapFS{"index.html": &fstest.MapFile{}}, time.Second)
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/state", http.StatusOK},
		{http.MethodPost, "/api/motor/up", http.StatusServiceUnavailable},
		{http.MethodGet, "/api/motor/up", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		// The root static-file handler also matches GET, so an API path without
		// a GET route must be absent, rather than invoke a command callback.
		if w.Code != test.status {
			t.Fatalf("%s %s: %d", test.method, test.path, w.Code)
		}
	}
}
