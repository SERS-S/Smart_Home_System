package openclaw

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"smart-home/ai-assistant/internal/config"
	"smart-home/ai-assistant/internal/prompt"
)

type Runtime struct {
	cfg    config.Config
	client *Client
	logger *slog.Logger
}

func NewRuntime(c config.Config, client *Client, logger *slog.Logger) *Runtime {
	return &Runtime{c, client, logger}
}
func (r *Runtime) node() string {
	return r.cfg.Join("runtime", "node", "node_modules", "node", "bin", "node")
}
func (r *Runtime) cli() string {
	return r.cfg.Join("runtime", "node_modules", "openclaw", "openclaw.mjs")
}

func privateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (r *Runtime) Prepare() error {
	c := r.cfg
	for _, directory := range []string{".runtime", ".cache/tmp", ".cache/node", ".cache/xdg", "openclaw/generated"} {
		if err := os.MkdirAll(c.Join(directory), 0700); err != nil {
			return err
		}
	}
	cli, err := exec.LookPath(c.MosquittoPub)
	if err != nil {
		return fmt.Errorf("IVUS_MOSQUITTO_PUB is unavailable; supply an installed mosquitto_pub path")
	}
	if err := privateFile(c.Join("openclaw", "workspace", "AGENTS.md"), []byte(prompt.System)); err != nil {
		return err
	}
	values := map[string]string{
		"IVUS_MODULE_ROOT": c.Root, "IVUS_MOSQUITTO_PUB": cli, "IVUS_MOTOR_TOPIC": c.MotorTopic,
		"IVUS_MQTT_HOST": c.MQTTHost, "IVUS_MQTT_PORT": strconv.Itoa(c.MQTTPort),
		"IVUS_MQTT_USERNAME": c.MQTTUsername, "IVUS_MQTT_PASSWORD": c.MQTTPassword,
	}
	var env strings.Builder
	for _, key := range []string{"IVUS_MODULE_ROOT", "IVUS_MOSQUITTO_PUB", "IVUS_MOTOR_TOPIC", "IVUS_MQTT_HOST", "IVUS_MQTT_PORT", "IVUS_MQTT_USERNAME", "IVUS_MQTT_PASSWORD"} {
		fmt.Fprintf(&env, "%s=%s\n", key, shellQuote(values[key]))
	}
	if err := privateFile(c.Join("openclaw", "generated", "mqtt.env"), []byte(env.String())); err != nil {
		return err
	}
	if !c.ModelConfigured() {
		return nil
	}
	model := map[string]any{"id": c.Model, "name": "IVUS model", "input": []string{"text"}}
	if c.ContextWindow > 0 {
		model["contextWindow"] = c.ContextWindow
	}
	if c.MaxOutputTokens > 0 {
		model["maxTokens"] = c.MaxOutputTokens
	}
	redactions := []string{}
	for _, value := range []string{c.APIKey, c.GatewayToken, c.MQTTPassword} {
		if value != "" {
			redactions = append(redactions, regexp.QuoteMeta(value))
		}
	}
	document := map[string]any{
		"gateway": map[string]any{"mode": "local", "bind": "loopback", "port": c.GatewayPort,
			"auth":      map[string]any{"mode": "token", "token": "${IVUS_GATEWAY_TOKEN}"},
			"controlUi": map[string]any{"enabled": false},
			"http":      map[string]any{"endpoints": map[string]any{"chatCompletions": map[string]bool{"enabled": true}}}},
		"models": map[string]any{"mode": "replace", "providers": map[string]any{
			"ivus-provider": map[string]any{"baseUrl": c.ProviderURL, "apiKey": "${IVUS_LLM_API_KEY}", "api": c.APIStyle, "models": []any{model}}}},
		"agents": map[string]any{"defaults": map[string]any{
			"workspace": c.Join("openclaw", "workspace"), "model": map[string]string{"primary": "ivus-provider/" + c.Model},
			"skipBootstrap": true, "maxConcurrent": 1, "timeoutSeconds": int(c.AgentTimeout.Seconds()),
			"elevatedDefault": "off", "verboseDefault": "off"}},
		"tools": map[string]any{"profile": "full", "allow": []string{"read", "exec"},
			"fs": map[string]bool{"workspaceOnly": true}, "elevated": map[string]bool{"enabled": false},
			"exec": map[string]any{"host": "gateway", "mode": "allowlist", "safeBins": []string{},
				"timeoutSeconds": int(c.ScriptTimeout.Seconds()), "backgroundMs": int(c.ScriptTimeout.Milliseconds()), "notifyOnExit": false}},
		"skills":    map[string]any{"allowBundled": []string{}, "load": map[string]any{"watch": false}},
		"plugins":   map[string]any{"slots": map[string]string{"memory": "none"}},
		"update":    map[string]any{"checkOnStart": false},
		"browser":   map[string]bool{"enabled": false},
		"discovery": map[string]any{"mdns": map[string]string{"mode": "off"}},
		"logging":   map[string]any{"level": "warn", "consoleLevel": "warn", "file": c.Join(".runtime", "openclaw.log"), "redactPatterns": redactions},
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := privateFile(c.Join("openclaw", "generated", "openclaw.json"), data); err != nil {
		return err
	}
	allowlist := []any{}
	for _, direction := range []string{"up", "down"} {
		allowlist = append(allowlist, map[string]string{"pattern": c.Join("openclaw", "workspace", "skills", "curtain-control", "scripts", "curtain-"+direction+".sh")})
	}
	approvals := map[string]any{"version": 1,
		"defaults": map[string]any{"security": "allowlist", "ask": "off", "askFallback": "deny", "autoAllowSkills": false},
		"agents":   map[string]any{"main": map[string]any{"allowlist": allowlist}}}
	data, err = json.MarshalIndent(approvals, "", "  ")
	if err != nil {
		return err
	}
	return privateFile(c.Join("openclaw", "generated", "exec-approvals.json"), data)
}

// Env redirects all managed state/cache paths into this module. Secret values
// come from compiled configuration, never from the launcher's environment.
func (r *Runtime) Env() []string {
	c := r.cfg
	result := []string{
		"PATH=" + filepath.Dir(r.node()) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"LANG=C.UTF-8", "LC_ALL=C", "HOME=" + os.Getenv("HOME"),
		"OPENCLAW_HOME=" + c.Join(".runtime", "home"),
		"OPENCLAW_STATE_DIR=" + c.Join(".runtime", "state"),
		"OPENCLAW_CONFIG_PATH=" + c.Join("openclaw", "generated", "openclaw.json"),
		"OPENCLAW_WORKSPACE_DIR=" + c.Join("openclaw", "workspace"),
		"NODE_COMPILE_CACHE=" + c.Join(".cache", "node"),
		"TMPDIR=" + c.Join(".cache", "tmp"),
		"XDG_CACHE_HOME=" + c.Join(".cache", "xdg"),
		"IVUS_LLM_API_KEY=" + c.APIKey, "IVUS_GATEWAY_TOKEN=" + c.GatewayToken,
	}
	return result
}
func (r *Runtime) Redact(value string) string {
	for _, secret := range []string{r.cfg.APIKey, r.cfg.GatewayToken, r.cfg.MQTTPassword} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func (r *Runtime) command(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, r.node(), append([]string{r.cli()}, args...)...)
	command.Dir = r.cfg.Root
	command.Env = r.Env()
	return command
}

func (r *Runtime) Validate(ctx context.Context) error {
	command := r.command(ctx, "config", "validate")
	output, err := command.CombinedOutput()
	if err != nil {
		r.logger.Warn("OpenClaw configuration validation failed", "detail", r.Redact(string(output)))
		return fmt.Errorf("OpenClaw configuration is invalid")
	}
	return nil
}

func (r *Runtime) Run(ctx context.Context) error {
	if !r.cfg.ModelConfigured() {
		r.logger.Warn("IVUS model is unconfigured; fill IVUS_LLM_* and IVUS_GATEWAY_TOKEN and rebuild")
		<-ctx.Done()
		return nil
	}
	if err := r.Validate(ctx); err != nil {
		return err
	}
	permissions := r.command(ctx, "approvals", "set", "--file", r.cfg.Join("openclaw", "generated", "exec-approvals.json"), "--json")
	output, err := permissions.CombinedOutput()
	if err != nil {
		r.logger.Warn("OpenClaw script permissions could not be initialized", "detail", r.Redact(string(output)))
		return fmt.Errorf("OpenClaw approvals initialization failed")
	}
	// The Gateway is a managed child, not a system daemon or global install.
	command := r.command(context.Background(), "gateway", "run")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("cannot start the local OpenClaw runtime")
	}
	stream := func(reader io.Reader) {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1048576)
		for scanner.Scan() {
			r.logger.Info("OpenClaw", "message", r.Redact(scanner.Text()))
		}
	}
	go stream(stdout)
	go stream(stderr)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer r.client.SetReady(false)
	stop := func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(r.cfg.ShutdownTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	readyCtx, cancel := context.WithTimeout(ctx, r.cfg.GatewayStartTimeout)
	ready := make(chan error, 1)
	go func() { ready <- ProbeUntil(readyCtx, r.client) }()
	select {
	case <-ctx.Done():
		cancel()
		stop()
		return nil
	case err := <-done:
		cancel()
		return fmt.Errorf("OpenClaw exited before readiness: %v", err)
	case err := <-ready:
		cancel()
		if err != nil {
			stop()
			return err
		}
	}
	r.logger.Info("OpenClaw is ready")
	select {
	case <-ctx.Done():
		stop()
		return nil
	case err := <-done:
		return fmt.Errorf("OpenClaw exited: %v", err)
	}
}
