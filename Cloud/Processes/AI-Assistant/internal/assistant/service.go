package assistant

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"smart-home/ai-assistant/internal/config"
	"smart-home/ai-assistant/internal/ledger"
	"smart-home/ai-assistant/internal/protocol"
)

type Agent interface {
	Ready() bool
	Complete(context.Context, protocol.Request) (string, error)
}
type Service struct {
	cfg    config.Config
	agent  Agent
	output io.Writer
	logger *slog.Logger
	store  *ledger.Store
	queue  chan protocol.Request
}

func New(c config.Config, agent Agent, output io.Writer, logger *slog.Logger) (*Service, error) {
	store, err := ledger.New(c.Join(".runtime", "requests"), c.CacheEntries, c.CacheTTL)
	if err != nil {
		return nil, err
	}
	return &Service{c, agent, output, logger, store, make(chan protocol.Request, c.QueueSize)}, nil
}
func (s *Service) Submit(ctx context.Context, request protocol.Request) error {
	if err := request.Validate(s.cfg.MaxTextRunes); err != nil {
		return err
	}
	if !protocol.HasWakeWord(request.Text) {
		return nil
	}
	request.RequestID = strings.ToLower(request.RequestID)
	select {
	case s.queue <- request:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) Worker(ctx context.Context) {
	var last time.Time
	interval := time.Minute / time.Duration(s.cfg.RatePerMinute)
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-s.queue:
			if wait := time.Until(last.Add(interval)); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if time.Since(request.Timestamp) > s.cfg.AgentTimeout {
				s.logger.Warn("IVUS request expired in queue", "request_id", request.RequestID)
				continue
			}
			if !s.agent.Ready() {
				s.logger.Warn("IVUS agent is not configured or unavailable", "request_id", request.RequestID)
				continue
			}
			fresh, err := s.store.Begin(request)
			if err != nil {
				s.logger.Error("IVUS request rejected", "request_id", request.RequestID, "error", err)
				continue
			}
			if !fresh {
				s.logger.Info("IVUS duplicate request ignored", "request_id", request.RequestID)
				continue
			}
			last = time.Now()
			s.process(ctx, request)
		}
	}
}
func (s *Service) process(parent context.Context, request protocol.Request) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, s.cfg.AgentTimeout)
	defer cancel()
	turn := s.cfg.Join(".runtime", "turns", request.RequestID)
	if err := os.MkdirAll(turn, 0700); err != nil {
		s.failed(request.RequestID, "turn_prepare_failed")
		return
	}
	defer os.RemoveAll(turn)
	expiry := strconv.FormatInt(time.Now().Add(s.cfg.AgentTimeout).Unix(), 10)
	if err := os.WriteFile(filepath.Join(turn, "permit"), []byte(expiry+"\n"), 0600); err != nil {
		s.failed(request.RequestID, "turn_prepare_failed")
		return
	}
	answer, err := s.agent.Complete(ctx, request.Normalize(time.Now().UTC(), s.cfg.SensorMaxAge))
	// Revoke permission before another request can start; timed-out late tool
	// calls cannot borrow a new turn's identity or publish an additional action.
	_ = os.Remove(filepath.Join(turn, "permit"))
	if err != nil {
		s.failed(request.RequestID, "agent_failed")
		s.logger.Warn("IVUS agent request failed", "request_id", request.RequestID, "error", err, "duration", time.Since(started))
		return
	}
	if strings.TrimSpace(answer) == "" {
		s.failed(request.RequestID, "empty_answer")
		return
	}
	if err := s.store.Finish(request.RequestID, "completed"); err != nil {
		s.logger.Error("IVUS completion could not be recorded", "request_id", request.RequestID)
		return
	}
	if _, err := fmt.Fprintln(s.output, strings.TrimSpace(answer)); err != nil {
		s.logger.Error("IVUS stdout failed", "request_id", request.RequestID)
		return
	}
	s.logger.Info("IVUS request completed", "request_id", request.RequestID, "duration", time.Since(started))
}
func (s *Service) failed(id, code string) {
	if err := s.store.Finish(id, code); err != nil {
		s.logger.Error("IVUS failure could not be recorded", "request_id", id)
	}
}

// Serve accepts newline-delimited JSON and deliberately writes no replies.
// Closing Control's connection does not cancel an accepted assistant request.
func (s *Service) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(s.cfg.SocketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket IVUS path")
		}
		connection, err := net.DialTimeout("unix", s.cfg.SocketPath, time.Second)
		if err == nil {
			connection.Close()
			return fmt.Errorf("another IVUS process is listening")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("cannot inspect existing IVUS socket: %w", err)
		}
		if err := os.Remove(s.cfg.SocketPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.cfg.SocketPath, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(s.cfg.SocketPath, 0600); err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	slots := make(chan struct{}, s.cfg.MaxConnections)
	s.logger.Info("IVUS listening on UNIX socket", "path", s.cfg.SocketPath)
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slots }()
				defer connection.Close()
				stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
				defer stop()
				_ = connection.SetReadDeadline(time.Now().Add(s.cfg.AgentTimeout))
				scanner := bufio.NewScanner(connection)
				scanner.Buffer(make([]byte, 4096), s.cfg.MaxFrameBytes)
				for scanner.Scan() {
					request, err := protocol.Decode(scanner.Bytes(), s.cfg.MaxTextRunes)
					if err != nil {
						s.logger.Warn("Invalid IVUS frame", "error", err)
						continue
					}
					if err := s.Submit(ctx, request); err != nil && ctx.Err() == nil {
						s.logger.Warn("IVUS frame rejected", "error", err)
					}
				}
				if err := scanner.Err(); err != nil && ctx.Err() == nil {
					s.logger.Warn("IVUS socket read failed", "error", err)
				}
			}()
		default:
			connection.Close()
			s.logger.Warn("IVUS connection limit reached")
		}
	}
}
