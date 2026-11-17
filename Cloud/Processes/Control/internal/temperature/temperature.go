package temperature

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"smart-home/control/internal/config"
	"smart-home/control/internal/device"
)

type State struct {
	device.Common
	Temperature *float64   `json:"temperature"`
	Unit        *string    `json:"unit"`
	Serial      *uint64    `json:"serial"`
	Timestamp   *time.Time `json:"timestamp"`
}

func (s State) Copy() State {
	s.Common = s.Common.Copy()
	s.Temperature, s.Unit = device.Clone(s.Temperature), device.Clone(s.Unit)
	s.Serial, s.Timestamp = device.Clone(s.Serial), device.Clone(s.Timestamp)
	return s
}

type Device struct {
	*device.Inbox
	state    State
	topic    string
	onChange func(State, []string)
	logger   *slog.Logger
}

func New(c config.Config, onChange func(State, []string), logger *slog.Logger) *Device {
	d := &Device{Inbox: device.NewInbox(c.QueueSize), state: State{Common: device.Common{DeviceID: c.TemperatureID}},
		topic: c.Topics.Temperature, onChange: onChange, logger: logger}
	if onChange != nil {
		onChange(d.state.Copy(), []string{"device_id", "connected", "connection_known"})
	}
	return d
}

func (d *Device) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-d.Events:
			before := d.state.Copy()
			if event.Message != nil {
				if err := d.apply(*event.Message); err != nil {
					d.logger.Warn("Ignoring invalid temperature message", "topic", event.Message.Topic, "error", err)
				}
			}
			if event.Request != nil {
				event.Request.Reply <- device.Reply{Err: fmt.Errorf("temperature has no control commands")}
			}
			after := d.state.Copy()
			if changed := device.Changes(before, after); len(changed) > 0 && d.onChange != nil {
				d.onChange(after, changed)
			}
		}
	}
}

func (d *Device) apply(m device.Message) error {
	switch m.Topic {
	case d.topic + "/data":
		measurement, err := device.ParseMeasurement(m.Payload, d.state.DeviceID, "temperature", "celsius")
		if err != nil {
			return err
		}
		d.state.Temperature, d.state.Unit = &measurement.Value, &measurement.Unit
		d.state.Serial, d.state.Timestamp = &measurement.Serial, &measurement.Timestamp
		d.state.Touch()
	case d.topic + "/info":
		info, err := device.ParseInfo(m.Payload, d.state.DeviceID)
		if err != nil {
			return err
		}
		d.state.ApplyInfo(info)
	case d.topic + "/signal":
		_, err := d.state.Signal(m.Payload)
		return err
	default:
		return fmt.Errorf("unexpected topic")
	}
	return nil
}
