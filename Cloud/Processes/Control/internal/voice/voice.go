package voice

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"smart-home/control/internal/device"
)

type Worker struct {
	*device.Inbox
	output io.StringWriter
	logger *slog.Logger
}

func New(size int, output io.StringWriter, logger *slog.Logger) *Worker {
	return &Worker{device.NewInbox(size), output, logger}
}

func (w *Worker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-w.Events:
			if event.Message == nil {
				continue
			}
			var message struct {
				Text *string `json:"text"`
			}
			if err := json.Unmarshal(event.Message.Payload, &message); err != nil || message.Text == nil {
				w.logger.Warn("Ignoring invalid voice message")
				continue
			}
			if _, err := w.output.WriteString("Voice placeholder: " + *message.Text + "\n"); err != nil {
				w.logger.Error("Voice output failed", "error", err)
			}
		}
	}
}
