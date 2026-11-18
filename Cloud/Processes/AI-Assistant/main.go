package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"smart-home/ai-assistant/internal/assistant"
	"smart-home/ai-assistant/internal/config"
	"smart-home/ai-assistant/internal/openclaw"
)

var compiledConfig string

func run(ctx context.Context, c config.Config, logger *slog.Logger) error {
	if !c.Enabled {
		logger.Info("IVUS is disabled")
		return nil
	}
	if err := os.MkdirAll(c.Join(".runtime"), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(c.Join(".runtime", "owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another IVUS process owns this module")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client := openclaw.New(c)
	runtime := openclaw.NewRuntime(c, client, logger)
	if err := runtime.Prepare(); err != nil {
		return err
	}
	service, err := assistant.New(c, client, os.Stdout, logger)
	if err != nil {
		return err
	}
	errors := make(chan error, 2)
	done := make(chan struct{})
	go func() { defer close(done); service.Worker(ctx) }()
	go func() { errors <- service.Serve(ctx) }()
	go func() { errors <- runtime.Run(ctx) }()
	completed := 0
	select {
	case <-ctx.Done():
	case err = <-errors:
		completed = 1
	}
	cancel()

	for completed < 2 {
		result := <-errors
		if err == nil {
			err = result
		}
		completed++
	}
	<-done
	return err
}
func main() {
	prepareOnly := flag.Bool("prepare-only", false, "prepare module-local runtime files without starting OpenClaw")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	settings, err := config.Decode(compiledConfig)
	if err == nil {
		if *prepareOnly {
			err = openclaw.NewRuntime(settings, openclaw.New(settings), logger).Prepare()
			if err != nil {
				logger.Error("IVUS prepare failed", "error", err)
				os.Exit(1)
			}
			return
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = run(ctx, settings, logger)
	}
	if err != nil {
		logger.Error("IVUS stopped", "error", err)
		os.Exit(1)
	}
}
