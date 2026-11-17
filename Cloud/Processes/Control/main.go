package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"

	"smart-home/control/internal/config"
	"smart-home/control/internal/hab"
	"smart-home/control/internal/journal"
	"smart-home/control/internal/light"
	"smart-home/control/internal/motor"
	"smart-home/control/internal/mqtt"
	"smart-home/control/internal/temperature"
	"smart-home/control/internal/voice"
	"smart-home/control/internal/webserver"
)

//go:embed static/*
var assets embed.FS

func run(ctx context.Context, c config.Config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return err
	}
	web := webserver.New(static, c.HTTPTimeout)
	transport := mqtt.New(c, logger)
	journalWorker := journal.New(c, transport, web.OnJournalChanged, logger)
	lightDevice := light.New(c, web.OnLightChanged, logger)
	temperatureDevice := temperature.New(c, web.OnTemperatureChanged, logger)
	motorDevice := motor.New(c, transport, func(state motor.State, fields []string) {
		web.OnMotorChanged(state, fields)
		recordMotorEvent(ctx, journalWorker, state, fields)
	}, logger)
	habDevice := hab.New(c, transport, func(state hab.State, fields []string) {
		web.OnHabChanged(state, fields)
		recordHabEvent(ctx, journalWorker, state, fields)
	}, logger)
	voiceWorker := voice.New(c.QueueSize, os.Stdout, logger)
	web.SetCommands(webserver.Commands{StartVoice: habDevice.StartVoice, StopVoice: habDevice.StopVoice,
		MotorUp: motorDevice.Up, MotorDown: motorDevice.Down, MotorUpTo: motorDevice.UpTo, MotorDownTo: motorDevice.DownTo})
	for _, suffix := range []string{"/data", "/info", "/signal"} {
		transport.Route(c.Topics.Light+suffix, lightDevice)
		transport.Route(c.Topics.Temperature+suffix, temperatureDevice)
	}
	transport.Route(c.Topics.Light+"/data", mqtt.Receivers{lightDevice, habDevice})
	transport.Route(c.Topics.Temperature+"/data", mqtt.Receivers{temperatureDevice, habDevice})
	for _, suffix := range []string{"/info", "/signal", "/command/feedback"} {
		transport.Route(c.Topics.Motor+suffix, motorDevice)
	}
	for _, suffix := range []string{"/info", "/signal", "/voice/command/feedback"} {
		transport.Route(c.Topics.Hab+suffix, habDevice)
	}
	transport.Route(c.Topics.Voice, voiceWorker)

	var workers sync.WaitGroup
	start := func(work func(context.Context)) {
		workers.Add(1)
		go func() { defer workers.Done(); work(ctx) }()
	}
	for _, work := range []func(context.Context){lightDevice.Run, temperatureDevice.Run, motorDevice.Run, habDevice.Run, voiceWorker.Run, journalWorker.Run} {
		start(work)
	}
	failures := make(chan error, 2)
	start(func(ctx context.Context) {
		if err := transport.Run(ctx, func(online bool) {
			web.SetMQTTConnected(online)
			motorDevice.Broker(ctx, online)
			habDevice.Broker(ctx, online)
		}); err != nil {
			failures <- err
		}
	})
	server := &http.Server{Addr: c.HTTPAddress, Handler: web.Handler(), ReadHeaderTimeout: c.HTTPTimeout, IdleTimeout: c.HTTPTimeout,
		BaseContext: func(_ net.Listener) context.Context { return ctx }}
	start(func(context.Context) {
		logger.Info("Control HTTP listening", "address", c.HTTPAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- err
		}
	})
	logger.Info("Control started")
	select {
	case <-ctx.Done():
	case err = <-failures:
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer stop()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		if err == nil {
			err = shutdownErr
		}
	}
	workers.Wait()
	logger.Info("Control stopped")
	return err
}

func recordMotorEvent(ctx context.Context, worker *journal.Worker, state motor.State, fields []string) {
	if !slices.Contains(fields, "operation") || state.Operation == nil || state.Operation.Status != "confirmed" || state.TargetLevel == nil {
		return
	}
	action := "поднята"
	if state.Operation.Action == "down" {
		action = "опущена"
	}
	worker.Emit(ctx, journal.Event{OperationID: state.Operation.ID, Text: fmt.Sprintf("Штора %s до %d%%", action, *state.TargetLevel)})
}

func recordHabEvent(ctx context.Context, worker *journal.Worker, state hab.State, fields []string) {
	if !slices.Contains(fields, "operation") || state.Operation == nil {
		return
	}
	if state.Operation.Action == "start" && state.Operation.Status == "confirmed" {
		worker.Emit(ctx, journal.Event{OperationID: state.Operation.ID, Text: "Микрофон на хабе включён"})
	} else if state.Operation.Action == "stop" {
		worker.Emit(ctx, journal.Event{OperationID: state.Operation.ID, Text: "Запрошено выключение микрофона на хабе"})
	}
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	c, err := loadConfig()
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = run(ctx, c, logger)
	}
	if err != nil {
		logger.Error("Control failed", "error", err)
		os.Exit(1)
	}
}
