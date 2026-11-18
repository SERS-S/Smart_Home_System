// Package protocol defines the one-way Control -> IVUS UNIX-socket envelope.
package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Sensor struct {
	Available  bool       `json:"available"`
	Value      *float64   `json:"value,omitempty"`
	Unit       string     `json:"unit,omitempty"`
	MeasuredAt *time.Time `json:"measured_at,omitempty"`
	DeviceID   string     `json:"device_id,omitempty"`
	Serial     *uint64    `json:"serial,omitempty"`
	Stale      bool       `json:"stale,omitempty"`
}

type Telemetry struct {
	Illuminance Sensor `json:"illuminance"`
	Temperature Sensor `json:"temperature"`
}

type Curtain struct {
	Connected bool `json:"connected"`
	Position  *int `json:"position,omitempty"`
}

type Request struct {
	RequestID string    `json:"request_id"`
	SessionID string    `json:"session_id"`
	Source    string    `json:"source"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	Telemetry Telemetry `json:"telemetry"`
	Curtain   Curtain   `json:"curtain"`
}

// HasWakeWord accepts the separate Cyrillic word Ивус in any case.
// Latin aliases and embedded fragments such as мойИвус or Ивус123 do not wake it.
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

func UUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, c := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (r Request) Validate(maxRunes int) error {
	if !UUID(r.RequestID) {
		return fmt.Errorf("invalid request_id")
	}
	if !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" || utf8.RuneCountInString(r.Text) > maxRunes {
		return fmt.Errorf("text must be nonempty UTF-8 within the configured limit")
	}
	if r.Source != "voice" {
		return fmt.Errorf("source must be voice")
	}
	if r.Timestamp.IsZero() {
		return fmt.Errorf("timestamp is required")
	}
	if r.SessionID == "" || len(r.SessionID) > 128 {
		return fmt.Errorf("invalid session_id")
	}
	for _, c := range r.SessionID {
		if !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("invalid session_id")
		}
	}
	for _, reading := range []struct {
		sensor Sensor
		unit   string
	}{
		{r.Telemetry.Illuminance, "lux"}, {r.Telemetry.Temperature, "celsius"},
	} {
		s := reading.sensor
		if !s.Available {
			continue
		}
		if s.Value == nil || math.IsNaN(*s.Value) || math.IsInf(*s.Value, 0) || s.Unit != reading.unit || s.MeasuredAt == nil || s.MeasuredAt.IsZero() {
			return fmt.Errorf("invalid available sensor reading")
		}
		if reading.unit == "lux" && *s.Value < 0 {
			return fmt.Errorf("illuminance must be nonnegative")
		}
	}
	if r.Curtain.Position != nil && (*r.Curtain.Position < 0 || *r.Curtain.Position > 100) {
		return fmt.Errorf("invalid curtain position")
	}
	return nil
}

// Normalize drops stale/unknown values rather than leaving numbers for the LLM
// to mistake for current data. Thresholds are supplied by trusted configuration.
func (r Request) Normalize(now time.Time, maxAge time.Duration) Request {
	normalize := func(sensor Sensor) Sensor {
		if !sensor.Available {
			return Sensor{Available: false}
		}
		if sensor.MeasuredAt == nil || sensor.MeasuredAt.After(now) || now.Sub(*sensor.MeasuredAt) > maxAge {
			return Sensor{Available: false, Stale: true}
		}
		return sensor
	}
	r.Telemetry.Illuminance = normalize(r.Telemetry.Illuminance)
	r.Telemetry.Temperature = normalize(r.Telemetry.Temperature)
	if !r.Curtain.Connected {
		r.Curtain.Position = nil
	}
	return r
}

func Decode(frame []byte, maxRunes int) (Request, error) {
	var request Request
	if !utf8.Valid(frame) {
		return request, fmt.Errorf("frame must be UTF-8")
	}
	if err := json.Unmarshal(frame, &request); err != nil {
		return request, fmt.Errorf("invalid request JSON")
	}
	return request, request.Validate(maxRunes)
}
