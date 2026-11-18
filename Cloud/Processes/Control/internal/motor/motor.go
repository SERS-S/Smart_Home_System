package motor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"smart-home/control/internal/command"
	"smart-home/control/internal/config"
	"smart-home/control/internal/device"
)

type State struct {
	device.Common
	State       string         `json:"state"`
	TargetLevel *int           `json:"target_level"`
	Level       *int           `json:"level"`
	Operation   *command.State `json:"operation"`
}

func (s State) Copy() State {
	s.Common = s.Common.Copy()
	s.TargetLevel, s.Operation = device.Clone(s.TargetLevel), device.Clone(s.Operation)
	s.Level = device.Clone(s.Level)
	return s
}

type Device struct {
	*device.Inbox
	state     State
	tracker   *command.Tracker
	cfg       config.Config
	onChange  func(State, []string)
	logger    *slog.Logger
	seen      map[uint64]string
	seenOrder []uint64
}

func New(c config.Config, publisher command.Publisher, onChange func(State, []string), logger *slog.Logger) *Device {
	d := &Device{Inbox: device.NewInbox(c.QueueSize), state: State{Common: device.Common{DeviceID: c.MotorID}, State: "unknown"},
		tracker: command.New(publisher), cfg: c, onChange: onChange, logger: logger, seen: make(map[uint64]string)}
	if onChange != nil {
		onChange(d.snapshot(), []string{"device_id", "connected", "connection_known", "state"})
	}
	return d
}

func (d *Device) snapshot() State                                 { s := d.state.Copy(); s.Operation = d.tracker.Snapshot(); return s }
func (d *Device) Up(ctx context.Context) (device.Result, error)   { return d.Call(ctx, "up") }
func (d *Device) Down(ctx context.Context) (device.Result, error) { return d.Call(ctx, "down") }
func (d *Device) UpTo(ctx context.Context, level int) (device.Result, error) {
	return d.CallLevel(ctx, "up", level)
}
func (d *Device) DownTo(ctx context.Context, level int) (device.Result, error) {
	return d.CallLevel(ctx, "down", level)
}

func (d *Device) Run(ctx context.Context) {
	defer d.tracker.Cancel()
	for {
		before := d.snapshot()
		var request *device.Request
		var reply device.Reply
		select {
		case <-ctx.Done():
			return
		case <-d.tracker.Timer():
			d.tracker.Tick()
		case result := <-d.tracker.Results():
			d.tracker.HandleResult(result)
		case event := <-d.Events:
			if event.Online != nil {
				d.tracker.Broker(*event.Online)
			}
			if event.Message != nil {
				if err := d.apply(ctx, *event.Message); err != nil {
					d.logger.Warn("Ignoring invalid motor message", "topic", event.Message.Topic, "error", err)
				}
			}
			if event.Request != nil {
				request = event.Request
				reply.Result, reply.Err = d.request(ctx, event.Request)
			}
		}
		after := d.snapshot()
		if changed := device.Changes(before, after); len(changed) > 0 && d.onChange != nil {
			d.onChange(after, changed)
		}
		if request != nil {
			request.Reply <- reply
		}
	}
}

func (d *Device) request(ctx context.Context, request *device.Request) (device.Result, error) {
	if err := request.Context.Err(); err != nil {
		return device.Result{}, err
	}
	if request.Action != "up" && request.Action != "down" {
		return device.Result{}, fmt.Errorf("unknown motor command")
	}
	if !d.state.ConnectionKnown || !d.state.Connected {
		return device.Result{}, device.ErrDisconnected
	}
	if !d.tracker.Ready() {
		return device.Result{}, device.ErrUnavailable
	}
	level := 0
	if request.Action == "up" {
		level = 100
	}
	if request.Level != nil {
		level = *request.Level
	}
	if level < 0 || level > 100 {
		return device.Result{}, device.ErrInvalidCommand
	}
	id, err := d.tracker.Start(ctx, request.Action, d.cfg.Topics.Motor+"/command/"+request.Action, d.cfg.MotorRetry, true,
		func(id uint64) any {
			return struct {
				ID    uint64 `json:"operation_id"`
				Level int    `json:"level"`
			}{id, level}
		})
	if err == nil {
		d.state.TargetLevel = &level
		d.remember(id, fmt.Sprintf("%s:%d", request.Action, level))
	}
	return device.Result{OperationID: id}, err
}

func (d *Device) remember(id uint64, intent string) {
	d.seen[id] = intent
	d.seenOrder = append(d.seenOrder, id)
	if len(d.seenOrder) > d.cfg.QueueSize {
		delete(d.seen, d.seenOrder[0])
		d.seenOrder = d.seenOrder[1:]
	}
}

func (d *Device) apply(ctx context.Context, m device.Message) error {
	switch m.Topic {
	case d.cfg.Topics.Motor + "/command/up", d.cfg.Topics.Motor + "/command/down":
		if m.Retained {
			return nil
		}
		var incoming struct {
			ID    uint64 `json:"operation_id"`
			Level *int   `json:"level"`
		}
		if err := command.DecodeFeedback(m.Payload, &incoming); err != nil {
			return err
		}
		if incoming.ID == 0 || incoming.ID > (1<<53)-1 || incoming.Level == nil || *incoming.Level < 0 || *incoming.Level > 100 {
			return device.ErrInvalidCommand
		}
		action := "down"
		if m.Topic == d.cfg.Topics.Motor+"/command/up" {
			action = "up"
		}
		intent := fmt.Sprintf("%s:%d", action, *incoming.Level)
		if old, seen := d.seen[incoming.ID]; seen {
			if old != intent {
				return fmt.Errorf("motor operation_id reused with another command")
			}
			return nil
		}
		d.remember(incoming.ID, intent)
		d.tracker.Observe(ctx, incoming.ID, action, d.cfg.MotorRetry.Interval*time.Duration(d.cfg.MotorRetry.Attempts))
		d.state.TargetLevel = device.Clone(incoming.Level)
	case d.cfg.Topics.Motor + "/info":
		info, err := device.ParseInfo(m.Payload, d.state.DeviceID)
		if err != nil {
			return err
		}
		if info.State != nil && *info.State != "up" && *info.State != "down" {
			return fmt.Errorf("invalid motor state")
		}
		if info.Level != nil && (*info.Level < 0 || *info.Level > 100) {
			return device.ErrInvalidCommand
		}
		d.state.ApplyInfo(info)
		if info.State != nil {
			d.state.State = *info.State
			// Legacy info reports the two end positions.
			level := 0
			if *info.State == "up" {
				level = 100
			}
			d.state.Level = &level
		}
		if info.Level != nil {
			d.state.Level = device.Clone(info.Level)
		}
	case d.cfg.Topics.Motor + "/signal":
		if _, err := d.state.Signal(m.Payload); err != nil {
			return err
		}
		if !d.state.Connected {
			d.tracker.Cancel()
		}
	case d.cfg.Topics.Motor + "/command/feedback":
		var feedback struct {
			ID    uint64 `json:"operation_id"`
			State string `json:"state"`
		}
		if err := command.DecodeFeedback(m.Payload, &feedback); err != nil {
			return err
		}
		if feedback.ID == 0 || (feedback.State != "up" && feedback.State != "down") {
			return fmt.Errorf("invalid motor feedback")
		}
		d.state.Touch()
		if d.tracker.Matches(feedback.ID) {
			d.state.State = feedback.State
			if d.tracker.Snapshot().Action == feedback.State {
				d.tracker.Confirm(feedback.ID)
				d.state.Level = device.Clone(d.state.TargetLevel)
			}
		}
	default:
		return fmt.Errorf("unexpected topic")
	}
	return nil
}
