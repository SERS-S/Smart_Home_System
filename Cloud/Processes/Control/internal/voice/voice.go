package voice

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode/utf8"

	"smart-home/control/internal/device"
)

type Worker struct {
	*device.Inbox
	forward func(context.Context, string) error
	logger  *slog.Logger
}

func New(size int, forward func(context.Context, string) error, logger *slog.Logger) *Worker {
	return &Worker{device.NewInbox(size), forward, logger}
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
			if event.Message.Retained || !utf8.Valid(event.Message.Payload) {
				continue
			}
			var message struct {
				Text *string `json:"text"`
			}
			if err := json.Unmarshal(event.Message.Payload, &message); err != nil || message.Text == nil || strings.TrimSpace(*message.Text) == "" {
				w.logger.Warn("Ignoring invalid voice message")
				continue
			}
			if w.forward != nil {
				if err := w.forward(ctx, *message.Text); err != nil && ctx.Err() == nil {
					w.logger.Warn("IVUS forwarding failed", "error", err)
				}
			}
		}
	}
}
