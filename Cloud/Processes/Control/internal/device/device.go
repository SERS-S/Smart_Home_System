// Package device contains shared wire parsing and inboxes. State is owned by
// each device's Run goroutine; observers only receive independent snapshots.
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
)

var (
	ErrDisconnected   = errors.New("device is not known to be connected")
	ErrUnavailable    = errors.New("MQTT is unavailable")
	ErrQueueFull      = errors.New("command queue is full")
	ErrInvalidCommand = errors.New("invalid command")
)

type Common struct {
	DeviceID        string     `json:"device_id"`
	Connected       bool       `json:"connected"`
	ConnectionKnown bool       `json:"connection_known"`
	LastSeen        *time.Time `json:"last_seen"`
	BatteryMV       *float64   `json:"battery_mv"`
	BatteryPercent  *int       `json:"battery"`
	RawVoltage      *int       `json:"raw_voltage"`
	FirmwareVersion *string    `json:"firmware_version"`
}

func Clone[T any](p *T) *T {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

func (c Common) Copy() Common {
	c.LastSeen, c.BatteryMV = Clone(c.LastSeen), Clone(c.BatteryMV)
	c.BatteryPercent, c.RawVoltage = Clone(c.BatteryPercent), Clone(c.RawVoltage)
	c.FirmwareVersion = Clone(c.FirmwareVersion)
	return c
}

func (c *Common) Touch() { now := time.Now().UTC(); c.LastSeen = &now }

func (c *Common) Signal(payload []byte) (newConnection bool, err error) {
	var value struct {
		DeviceID string `json:"device_id"`
		Signal   string `json:"signal"`
	}
	if err = json.Unmarshal(payload, &value); err != nil {
		return false, err
	}
	if value.DeviceID != "" && value.DeviceID != c.DeviceID {
		return false, fmt.Errorf("unexpected device_id")
	}
	if value.Signal != "connect" && value.Signal != "disconnect" {
		return false, fmt.Errorf("signal must be connect or disconnect")
	}
	connected := value.Signal == "connect"
	newConnection = connected && (!c.ConnectionKnown || !c.Connected)
	c.Connected, c.ConnectionKnown = connected, true
	c.Touch()
	return newConnection, nil
}

type Info struct {
	DeviceID  string          `json:"device_id"`
	BatteryMV *float64        `json:"battery_mv"`
	Voltage   *float64        `json:"voltage"`
	Battery   *int            `json:"battery"`
	Raw       *int            `json:"raw_voltage"`
	Firmware  json.RawMessage `json:"firmware_version"`
	Version   json.RawMessage `json:"version"`
	State     *string         `json:"state"`
	Level     *int            `json:"level"`
	firmware  *string
}

func ParseInfo(payload []byte, id string) (Info, error) {
	var i Info
	if err := json.Unmarshal(payload, &i); err != nil {
		return i, err
	}
	if i.DeviceID != id {
		return i, fmt.Errorf("unexpected device_id")
	}
	if i.BatteryMV == nil && i.Voltage != nil {
		value := *i.Voltage * 1000
		i.BatteryMV = &value
	}
	if i.BatteryMV != nil && (!finite(*i.BatteryMV) || *i.BatteryMV < 0) {
		return i, fmt.Errorf("invalid battery voltage")
	}
	if i.Battery != nil && (*i.Battery < 0 || *i.Battery > 100) {
		return i, fmt.Errorf("invalid battery percentage")
	}
	if i.Raw != nil && *i.Raw < 0 {
		return i, fmt.Errorf("invalid raw_voltage")
	}
	version := i.Firmware
	if len(version) == 0 {
		version = i.Version
	}
	if len(version) != 0 {
		var text string
		if err := json.Unmarshal(version, &text); err != nil {
			var number json.Number
			if err := json.Unmarshal(version, &number); err != nil || number == "" {
				return i, fmt.Errorf("invalid firmware version")
			}
			text = number.String()
		}
		if strings.TrimSpace(text) == "" {
			return i, fmt.Errorf("empty firmware version")
		}
		i.firmware = &text
	}
	return i, nil
}

func (c *Common) ApplyInfo(i Info) {
	if i.BatteryMV != nil {
		c.BatteryMV = Clone(i.BatteryMV)
	}
	if i.Battery != nil {
		c.BatteryPercent = Clone(i.Battery)
	}
	if i.Raw != nil {
		c.RawVoltage = Clone(i.Raw)
	}
	if i.firmware != nil {
		c.FirmwareVersion = Clone(i.firmware)
	}
	c.Touch()
}

type Measurement struct {
	Value     float64   `json:"value"`
	Unit      string    `json:"unit"`
	Serial    uint64    `json:"serial"`
	Timestamp time.Time `json:"timestamp"`
}

func ParseMeasurement(payload []byte, id, field, unit string) (*Measurement, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	var wireID, wireUnit, stamp string
	var serial uint64
	var value float64
	for key, destination := range map[string]any{"device_id": &wireID, "unit": &wireUnit, "timestamp": &stamp, "serial": &serial, field: &value} {
		raw, exists := object[key]
		if !exists || string(raw) == "null" {
			return nil, fmt.Errorf("missing %s", key)
		}
		if err := json.Unmarshal(raw, destination); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", key, err)
		}
	}
	if wireID != id || wireUnit != unit || !finite(value) || (field == "illuminance" && value < 0) {
		return nil, fmt.Errorf("invalid device_id, unit or measurement")
	}
	timestamp, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}
	return &Measurement{value, unit, serial, timestamp.UTC()}, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// Changes returns JSON field names, flattening embedded Common fields.
func Changes(before, after any) []string {
	var fields []string
	var visit func(reflect.Value, reflect.Value)
	visit = func(a, b reflect.Value) {
		for n := 0; n < a.NumField(); n++ {
			field := a.Type().Field(n)
			if field.Anonymous {
				visit(a.Field(n), b.Field(n))
				continue
			}
			if !reflect.DeepEqual(a.Field(n).Interface(), b.Field(n).Interface()) {
				fields = append(fields, strings.Split(field.Tag.Get("json"), ",")[0])
			}
		}
	}
	visit(reflect.ValueOf(before), reflect.ValueOf(after))
	return fields
}

type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
}

type Result struct {
	OperationID uint64 `json:"operation_id"`
}
type Reply struct {
	Result Result
	Err    error
}
type Request struct {
	Context context.Context
	Action  string
	Level   *int
	Reply   chan Reply
}
type Event struct {
	Message *Message
	Request *Request
	Online  *bool
}

type Inbox struct{ Events chan Event }

func NewInbox(size int) *Inbox { return &Inbox{Events: make(chan Event, size)} }

func (i *Inbox) Deliver(ctx context.Context, m Message) error {
	m.Payload = append([]byte(nil), m.Payload...)
	select {
	case i.Events <- Event{Message: &m}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *Inbox) Broker(ctx context.Context, online bool) {
	select {
	case i.Events <- Event{Online: &online}:
	case <-ctx.Done():
	}
}

func (i *Inbox) Call(ctx context.Context, action string) (Result, error) {
	return i.call(ctx, action, nil)
}

func (i *Inbox) CallLevel(ctx context.Context, action string, level int) (Result, error) {
	if level < 0 || level > 100 {
		return Result{}, ErrInvalidCommand
	}
	return i.call(ctx, action, &level)
}

func (i *Inbox) call(ctx context.Context, action string, level *int) (Result, error) {
	r := &Request{Context: ctx, Action: action, Level: Clone(level), Reply: make(chan Reply, 1)}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}
	select {
	case i.Events <- Event{Request: r}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
		return Result{}, ErrQueueFull
	}
	select {
	case reply := <-r.Reply:
		return reply.Result, reply.Err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}
