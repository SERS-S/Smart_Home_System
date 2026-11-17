package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"smart-home/control/internal/device"
	"smart-home/control/internal/hab"
	"smart-home/control/internal/journal"
	"smart-home/control/internal/light"
	"smart-home/control/internal/motor"
	"smart-home/control/internal/temperature"
)

type Command func(context.Context) (device.Result, error)
type LevelCommand func(context.Context, int) (device.Result, error)
type Commands struct {
	StartVoice, StopVoice, MotorUp, MotorDown Command
	MotorUpTo, MotorDownTo                    LevelCommand
}
type State struct {
	Revision      uint64            `json:"revision"`
	MQTTConnected bool              `json:"mqtt_connected"`
	Light         light.State       `json:"light"`
	Temperature   temperature.State `json:"temperature"`
	Motor         motor.State       `json:"motor"`
	Hab           hab.State         `json:"hab"`
	Journal       []journal.Entry   `json:"journal"`
}

type WebServer struct {
	mu          sync.RWMutex
	state       State
	subscribers map[chan struct{}]struct{}
	timeout     time.Duration
	commands    Commands
	mux         *http.ServeMux
}

func New(static fs.FS, timeout time.Duration) *WebServer {
	s := &WebServer{subscribers: make(map[chan struct{}]struct{}), timeout: timeout, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/state", s.getState)
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("POST /api/hab/voice/start", func(w http.ResponseWriter, r *http.Request) { s.command(w, r, s.commands.StartVoice) })
	s.mux.HandleFunc("POST /api/hab/voice/stop", func(w http.ResponseWriter, r *http.Request) { s.command(w, r, s.commands.StopVoice) })
	s.mux.HandleFunc("POST /api/motor/up", func(w http.ResponseWriter, r *http.Request) {
		s.motorCommand(w, r, 100, s.commands.MotorUp, s.commands.MotorUpTo)
	})
	s.mux.HandleFunc("POST /api/motor/down", func(w http.ResponseWriter, r *http.Request) {
		s.motorCommand(w, r, 0, s.commands.MotorDown, s.commands.MotorDownTo)
	})
	s.mux.Handle("GET /", http.FileServer(http.FS(static)))
	return s
}

// SetCommands is called once during initialization, before serving requests.
func (s *WebServer) SetCommands(commands Commands) { s.commands = commands }
func (s *WebServer) Handler() http.Handler         { return s.mux }

// notifyLocked never performs network I/O or waits for a browser.
func (s *WebServer) notifyLocked() {
	s.state.Revision++
	for subscriber := range s.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
}
func (s *WebServer) OnLightChanged(state light.State, _ []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Light = state.Copy()
	s.notifyLocked()
}
func (s *WebServer) OnTemperatureChanged(state temperature.State, _ []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Temperature = state.Copy()
	s.notifyLocked()
}
func (s *WebServer) OnMotorChanged(state motor.State, _ []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Motor = state.Copy()
	s.notifyLocked()
}
func (s *WebServer) OnHabChanged(state hab.State, _ []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Hab = state.Copy()
	s.notifyLocked()
}
func (s *WebServer) SetMQTTConnected(online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.MQTTConnected = online
	s.notifyLocked()
}

func (s *WebServer) OnJournalChanged(entries []journal.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Journal = append([]journal.Entry{}, entries...)
	s.notifyLocked()
}

func (s *WebServer) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := s.state
	snapshot.Light, snapshot.Temperature = snapshot.Light.Copy(), snapshot.Temperature.Copy()
	snapshot.Motor, snapshot.Hab = snapshot.Motor.Copy(), snapshot.Hab.Copy()
	snapshot.Journal = append([]journal.Entry{}, snapshot.Journal...)
	return snapshot
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *WebServer) getState(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, s.Snapshot())
}
func (s *WebServer) command(w http.ResponseWriter, r *http.Request, callback Command) {
	if callback == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "command is unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	result, err := callback(ctx)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, device.ErrDisconnected) {
			status = http.StatusConflict
		}
		if errors.Is(err, device.ErrInvalidCommand) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		jsonResponse(w, status, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusAccepted, result)
}

func (s *WebServer) motorCommand(w http.ResponseWriter, r *http.Request, defaultLevel int, legacy Command, callback LevelCommand) {
	var body struct {
		Level *int `json:"level"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "expected JSON with integer level from 0 to 100"})
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "only one JSON object is allowed"})
		return
	}
	level := defaultLevel
	if body.Level != nil {
		level = *body.Level
	}
	if level < 0 || level > 100 {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "level must be between 0 and 100"})
		return
	}
	if callback == nil {
		if body.Level != nil {
			jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "level control is unavailable"})
			return
		}
		s.command(w, r, legacy)
		return
	}
	s.command(w, r, func(ctx context.Context) (device.Result, error) { return callback(ctx, level) })
}

func (s *WebServer) events(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	subscriber := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers[subscriber] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subscribers, subscriber); s.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(snapshot bool) error {
		// A slow browser must not leave an uninterruptible writer behind.
		_ = controller.SetWriteDeadline(time.Now().Add(s.timeout))
		if snapshot {
			state := s.Snapshot()
			data, err := json.Marshal(state)
			if err != nil {
				return err
			}
			if _, err = fmt.Fprintf(w, "id: %d\nevent: state\ndata: %s\n\n", state.Revision, data); err != nil {
				return err
			}
		} else if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := write(true); err != nil {
		return
	}
	heartbeat := time.NewTicker(s.timeout)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-subscriber:
			if err := write(true); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := write(false); err != nil {
				return
			}
		}
	}
}
