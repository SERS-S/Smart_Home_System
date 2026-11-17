// Package journal publishes domain events independently of device workers.
package journal

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"smart-home/control/internal/command"
	"smart-home/control/internal/config"
)

type Event struct {
	OperationID uint64
	Text        string
}

type Entry struct {
	ID          uint64    `json:"id"`
	OperationID uint64    `json:"operation_id"`
	Text        string    `json:"text"`
	Timestamp   time.Time `json:"timestamp"`
	Delivery    string    `json:"delivery"`
	Error       string    `json:"error,omitempty"`
}

type Worker struct {
	cfg       config.Config
	publisher command.Publisher
	logger    *slog.Logger
	onChange  func([]Entry)
	events    chan Event
	results   chan command.PublishResult
}

func New(c config.Config, publisher command.Publisher, onChange func([]Entry), logger *slog.Logger) *Worker {
	return &Worker{cfg: c, publisher: publisher, onChange: onChange, logger: logger,
		events: make(chan Event, c.QueueSize), results: make(chan command.PublishResult, c.QueueSize)}
}

func (w *Worker) Emit(ctx context.Context, event Event) {
	select {
	case w.events <- event:
	case <-ctx.Done():
	}
}

func (w *Worker) Run(ctx context.Context) {
	entries := make([]Entry, 0, w.cfg.JournalLimit)
	inflight := make(map[uint64]bool)
	var nextID uint64
	notify := func() {
		if w.onChange != nil {
			w.onChange(append([]Entry{}, entries...))
		}
	}
	publish := func(entry *Entry) {
		if entry.Delivery == "sent" || inflight[entry.ID] || !w.publisher.Ready() {
			return
		}
		payload, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{entry.Text})
		err := w.publisher.Enqueue(command.Publication{Context: ctx, ID: entry.ID, Attempt: 1, Topic: w.cfg.Topics.Journal, Payload: payload, Result: w.results})
		if err != nil {
			entry.Error = err.Error()
			return
		}
		inflight[entry.ID] = true
		entry.Error = ""
	}
	retry := time.NewTicker(w.cfg.PublishTimeout)
	defer retry.Stop()
	notify()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-w.events:
			duplicate := false
			for _, entry := range entries {
				if entry.OperationID == event.OperationID && entry.Text == event.Text {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			nextID++
			entries = append(entries, Entry{ID: nextID, OperationID: event.OperationID, Text: event.Text, Timestamp: time.Now().UTC(), Delivery: "pending"})
			if len(entries) > w.cfg.JournalLimit {
				old := entries[0]
				if old.Delivery != "sent" {
					w.logger.Error("Journal history limit reached before delivery", "operation_id", old.OperationID)
				}
				delete(inflight, old.ID)
				entries = append([]Entry(nil), entries[1:]...)
			}
			publish(&entries[len(entries)-1])
			notify()
		case result := <-w.results:
			delete(inflight, result.ID)
			for i := range entries {
				if entries[i].ID != result.ID {
					continue
				}
				if result.Err == nil {
					entries[i].Delivery = "sent"
					entries[i].Error = ""
				} else {
					entries[i].Error = result.Err.Error()
					w.logger.Warn("Journal publication deferred", "error", result.Err)
				}
				notify()
				break
			}
		case <-retry.C:
			for i := range entries {
				publish(&entries[i])
			}
		}
	}
}
