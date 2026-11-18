// Package ivus forwards voice requests over a one-way local UNIX stream.
package ivus

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"

	"smart-home/control/internal/config"
	"smart-home/control/internal/webserver"
)

type sensor struct {
	Available  bool       `json:"available"`
	Value      *float64   `json:"value,omitempty"`
	Unit       string     `json:"unit,omitempty"`
	MeasuredAt *time.Time `json:"measured_at,omitempty"`
	DeviceID   string     `json:"device_id,omitempty"`
	Serial     *uint64    `json:"serial,omitempty"`
}
type request struct {
	RequestID string    `json:"request_id"`
	SessionID string    `json:"session_id"`
	Source    string    `json:"source"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	Telemetry struct {
		Illuminance sensor `json:"illuminance"`
		Temperature sensor `json:"temperature"`
	} `json:"telemetry"`
	Curtain struct {
		Connected bool `json:"connected"`
		Position  *int `json:"position,omitempty"`
	} `json:"curtain"`
}
type Client struct {
	cfg      config.Config
	snapshot func() webserver.State
	mu       sync.Mutex
	seen     map[[32]byte]time.Time
}

func New(c config.Config, snapshot func() webserver.State) *Client {
	return &Client{cfg: c, snapshot: snapshot, seen: make(map[[32]byte]time.Time)}
}

func HasWakeWord(text string) bool {
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if strings.EqualFold(word, "ивус") {
			return true
		}
	}
	return false
}
func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 15) | 64
	bytes[8] = (bytes[8] & 63) | 128
	encoded := hex.EncodeToString(bytes[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
func reading(online bool, id string, known, connected bool, value *float64, unit *string, measured *time.Time, serial *uint64) sensor {
	if !online || (known && !connected) || value == nil || unit == nil || measured == nil {
		return sensor{Available: false}
	}
	return sensor{Available: true, Value: value, Unit: *unit, MeasuredAt: measured, DeviceID: id, Serial: serial}
}
func (c *Client) Forward(ctx context.Context, text string) error {
	if !c.cfg.IvusEnabled || !HasWakeWord(text) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fingerprint := sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(text), " "))))
	now := time.Now()
	for key, accepted := range c.seen {
		if now.Sub(accepted) >= c.cfg.IvusDedupWindow {
			delete(c.seen, key)
		}
	}
	if _, duplicate := c.seen[fingerprint]; duplicate {
		return nil
	}
	// A failed write may already have reached the receiver; never replay it
	// automatically with a new ID when MQTT redelivers the same utterance.
	if len(c.seen) >= max(1, c.cfg.QueueSize) {
		var oldest [32]byte
		oldestAt := now
		for key, accepted := range c.seen {
			if accepted.Before(oldestAt) {
				oldest, oldestAt = key, accepted
			}
		}
		delete(c.seen, oldest)
	}
	c.seen[fingerprint] = now
	id, err := newID()
	if err != nil {
		return err
	}
	state := c.snapshot()
	frame := request{RequestID: id, SessionID: c.cfg.HabID, Source: "voice", Text: text, Timestamp: now.UTC()}
	light, temp := state.Light, state.Temperature
	frame.Telemetry.Illuminance = reading(state.MQTTConnected, light.DeviceID, light.ConnectionKnown, light.Connected, light.Illuminance, light.Unit, light.Timestamp, light.Serial)
	frame.Telemetry.Temperature = reading(state.MQTTConnected, temp.DeviceID, temp.ConnectionKnown, temp.Connected, temp.Temperature, temp.Unit, temp.Timestamp, temp.Serial)
	frame.Curtain.Connected = state.MQTTConnected && state.Motor.ConnectionKnown && state.Motor.Connected
	if frame.Curtain.Connected {
		frame.Curtain.Position = state.Motor.Level
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dialer := net.Dialer{Timeout: c.cfg.IvusWriteTimeout}
	connection, err := dialer.DialContext(ctx, "unix", c.cfg.IvusSocketPath)
	if err != nil {
		return fmt.Errorf("IVUS UNIX socket unavailable")
	}
	defer connection.Close()
	_ = connection.SetWriteDeadline(time.Now().Add(c.cfg.IvusWriteTimeout))
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	for len(data) > 0 {
		written, err := connection.Write(data)
		if err != nil {
			return fmt.Errorf("IVUS frame delivery failed; it will not be retried automatically")
		}
		data = data[written:]
	}
	return nil // AI-Assistant prints its answer itself; there is no read/reply.
}
