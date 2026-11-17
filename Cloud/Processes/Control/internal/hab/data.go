package hab

import (
	"context"
	"encoding/json"
	"time"

	"smart-home/control/internal/command"
	"smart-home/control/internal/device"
)

type Point struct {
	Value     float64   `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// Calls enter the owning goroutine; external callers never read its state.
func (d *Device) SendLightData(ctx context.Context) error {
	_, err := d.Call(ctx, "publish_light")
	return err
}
func (d *Device) SendTemperatureData(ctx context.Context) error {
	_, err := d.Call(ctx, "publish_temperature")
	return err
}

func (d *Device) measurement(ctx context.Context, m device.Message, kind int) error {
	id, field, unit := d.cfg.LightID, "illuminance", "lux"
	if kind == 1 {
		id, field, unit = d.cfg.TemperatureID, "temperature", "celsius"
	}
	sample, err := device.ParseMeasurement(m.Payload, id, field, unit)
	if err != nil {
		return err
	}
	point := Point{Value: sample.Value, Timestamp: sample.Timestamp}
	if kind == 0 {
		d.state.NowLux = &sample.Value
		d.state.ListLux = appendPoint(d.state.ListLux, point, d.cfg.HistoryPoints)
	} else {
		d.state.NowCels = &sample.Value
		d.state.ListCels = appendPoint(d.state.ListCels, point, d.cfg.HistoryPoints)
	}
	// Current values follow arrival order, including out-of-order timestamps.
	d.dataVersion[kind]++
	if err := d.publishData(ctx, kind); err != nil && ctx.Err() == nil {
		d.logger.Warn("Hab telemetry publication deferred", "topic", m.Topic, "error", err)
	}
	return nil
}

func appendPoint(points []Point, point Point, limit int) []Point {
	points = append(points, point)
	if len(points) > limit {
		points = append([]Point(nil), points[len(points)-limit:]...)
	}
	return points
}

func (d *Device) publishData(ctx context.Context, kind int) error {
	var payload any
	topic := d.cfg.Topics.Hab + "/data/light"
	if kind == 0 {
		if d.state.NowLux == nil {
			return nil
		}
		payload = struct {
			Now  float64 `json:"now_lux"`
			List []Point `json:"list_lux"`
		}{*d.state.NowLux, d.state.ListLux}
	} else {
		if d.state.NowCels == nil {
			return nil
		}
		topic = d.cfg.Topics.Hab + "/data/temperature"
		payload = struct {
			Now  float64 `json:"now_cels"`
			List []Point `json:"list_cels"`
		}{*d.state.NowCels, d.state.ListCels}
	}
	data, err := json.Marshal(payload)
	if err == nil {
		err = d.publisher.Enqueue(command.Publication{Context: ctx, ID: uint64(kind + 1), Attempt: d.dataVersion[kind], Topic: topic, Payload: data, Result: d.dataResults})
	}
	d.dataPending[kind] = err != nil
	if err != nil {
		d.scheduleDataRetry()
	}
	return err
}

func (d *Device) dataResult(ctx context.Context, result command.PublishResult) {
	if result.ID < 1 || result.ID > 2 {
		return
	}
	kind := int(result.ID - 1)
	if result.Attempt != d.dataVersion[kind] {
		return
	}
	if result.Err != nil && ctx.Err() == nil {
		d.dataPending[kind] = true
		d.scheduleDataRetry()
		d.logger.Warn("Hab telemetry publication failed", "error", result.Err)
	}
}

func (d *Device) dataTimer() <-chan time.Time {
	if d.dataRetry == nil {
		return nil
	}
	return d.dataRetry.C
}
func (d *Device) stopDataTimer() {
	if d.dataRetry != nil {
		d.dataRetry.Stop()
		d.dataRetry = nil
	}
}
func (d *Device) scheduleDataRetry() {
	if d.dataRetry == nil {
		d.dataRetry = time.NewTimer(d.cfg.PublishTimeout)
	}
}
func (d *Device) flushData(ctx context.Context) {
	for kind, pending := range d.dataPending {
		if pending {
			_ = d.publishData(ctx, kind)
		}
	}
}
