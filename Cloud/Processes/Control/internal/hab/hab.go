package hab

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
	Recording          *bool          `json:"recording"`
	RequestedRecording *bool          `json:"requested_recording"`
	Operation          *command.State `json:"operation"`
	NowLux             *float64       `json:"now_lux"`
	ListLux            []Point        `json:"list_lux"`
	NowCels            *float64       `json:"now_cels"`
	ListCels           []Point        `json:"list_cels"`
}

func (s State) Copy() State {
	s.Common = s.Common.Copy()
	s.Recording, s.RequestedRecording = device.Clone(s.Recording), device.Clone(s.RequestedRecording)
	s.Operation = device.Clone(s.Operation)
	s.NowLux, s.NowCels = device.Clone(s.NowLux), device.Clone(s.NowCels)
	s.ListLux = append([]Point{}, s.ListLux...)
	s.ListCels = append([]Point{}, s.ListCels...)
	return s
}

type Device struct {
	*device.Inbox
	state       State
	tracker     *command.Tracker
	cfg         config.Config
	onChange    func(State, []string)
	logger      *slog.Logger
	publisher   command.Publisher
	dataResults chan command.PublishResult
	dataPending [2]bool
	dataVersion [2]int
	dataRetry   *time.Timer
}

func New(c config.Config, publisher command.Publisher, onChange func(State, []string), logger *slog.Logger) *Device {
	d := &Device{Inbox: device.NewInbox(c.QueueSize), state: State{Common: device.Common{DeviceID: c.HabID}},
		tracker: command.New(publisher), cfg: c, onChange: onChange, logger: logger,
		publisher: publisher, dataResults: make(chan command.PublishResult, c.QueueSize)}
	if onChange != nil {
		onChange(d.snapshot(), []string{"device_id", "connected", "connection_known"})
	}
	return d
}

func (d *Device) snapshot() State                                       { s := d.state.Copy(); s.Operation = d.tracker.Snapshot(); return s }
func (d *Device) StopVoice(ctx context.Context) (device.Result, error)  { return d.Call(ctx, "stop") }
func (d *Device) StartVoice(ctx context.Context) (device.Result, error) { return d.Call(ctx, "start") }

func (d *Device) Run(ctx context.Context) {
	defer d.tracker.Cancel()
	defer d.stopDataTimer()
	for {
		before := d.snapshot()
		var request *device.Request
		var reply device.Reply
		select {
		case <-ctx.Done():
			return
		case <-d.tracker.Timer():
			d.tracker.Tick()
		case <-d.dataTimer():
			d.dataRetry = nil
			d.flushData(ctx)
		case result := <-d.dataResults:
			d.dataResult(ctx, result)
		case result := <-d.tracker.Results():
			d.tracker.HandleResult(result)
		case event := <-d.Events:
			if event.Online != nil {
				d.tracker.Broker(*event.Online)
				if *event.Online {
					d.flushData(ctx)
				}
			}
			if event.Message != nil {
				if err := d.apply(ctx, *event.Message); err != nil {
					d.logger.Warn("Ignoring invalid hab message", "topic", event.Message.Topic, "error", err)
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

func (d *Device) start(ctx context.Context) (device.Result, error) {
	id, err := d.tracker.Start(ctx, "start", d.cfg.Topics.Hab+"/voice/command/start", d.cfg.HabRetry, true,
		func(id uint64) any {
			return struct {
				ID uint64 `json:"operation_id"`
				config.Audio
			}{id, d.cfg.Audio}
		})
	if err == nil {
		recording := true
		d.state.RequestedRecording = &recording
		d.state.Recording = nil
	}
	return device.Result{OperationID: id}, err
}

func (d *Device) request(ctx context.Context, request *device.Request) (device.Result, error) {
	if err := request.Context.Err(); err != nil {
		return device.Result{}, err
	}
	if request.Action == "publish_light" {
		return device.Result{}, d.publishData(ctx, 0)
	}
	if request.Action == "publish_temperature" {
		return device.Result{}, d.publishData(ctx, 1)
	}
	if request.Action != "stop" && request.Action != "start" {
		return device.Result{}, fmt.Errorf("unknown hab command")
	}
	if !d.state.ConnectionKnown || !d.state.Connected {
		return device.Result{}, device.ErrDisconnected
	}
	if !d.tracker.Ready() {
		return device.Result{}, device.ErrUnavailable
	}
	if request.Action == "start" {
		return d.start(ctx)
	}
	// The wire protocol has no operation_id or completion acknowledgement for stop.
	policy := config.Retry{Interval: d.cfg.HabRetry.Interval, Attempts: 1}
	id, err := d.tracker.Start(ctx, "stop", d.cfg.Topics.Hab+"/voice/command/stop", policy, false,
		func(uint64) any { return struct{}{} })
	if err == nil {
		recording := false
		d.state.RequestedRecording = &recording
		d.state.Recording = nil
	}
	return device.Result{OperationID: id}, err
}

func (d *Device) apply(ctx context.Context, m device.Message) error {
	switch m.Topic {
	case d.cfg.Topics.Light + "/data":
		return d.measurement(ctx, m, 0)
	case d.cfg.Topics.Temperature + "/data":
		return d.measurement(ctx, m, 1)
	case d.cfg.Topics.Hab + "/info":
		info, err := device.ParseInfo(m.Payload, d.state.DeviceID)
		if err != nil {
			return err
		}
		d.state.ApplyInfo(info)
	case d.cfg.Topics.Hab + "/signal":
		connected, err := d.state.Signal(m.Payload)
		if err != nil {
			return err
		}
		if !d.state.Connected {
			d.tracker.Cancel()
			d.state.Recording, d.state.RequestedRecording = nil, nil
		} else if connected {
			_, err := d.start(ctx)
			return err
		}
	case d.cfg.Topics.Hab + "/voice/command/feedback":
		var feedback struct {
			ID        uint64 `json:"operation_id"`
			Completed *bool  `json:"completed"`
		}
		if err := command.DecodeFeedback(m.Payload, &feedback); err != nil {
			return err
		}
		if feedback.ID == 0 || feedback.Completed == nil {
			return fmt.Errorf("invalid hab feedback")
		}
		d.state.Touch()
		if *feedback.Completed && d.tracker.Matches(feedback.ID) && d.tracker.Snapshot().Action == "start" {
			d.tracker.Confirm(feedback.ID)
			recording := true
			d.state.Recording = &recording
		}
	default:
		return fmt.Errorf("unexpected topic")
	}
	return nil
}
