package config

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Keys is the allowlist embedded by the module-local build helper. Other
// services' keys and SSH deployment credentials are never included.
var Keys = []string{
	"IVUS_ENABLED", "IVUS_SOCKET_PATH", "IVUS_MAX_TEXT_RUNES", "IVUS_MAX_FRAME_BYTES",
	"IVUS_QUEUE_SIZE", "IVUS_MAX_CONNECTIONS", "IVUS_SENSOR_MAX_AGE_SECONDS",
	"IVUS_AGENT_TIMEOUT_SECONDS", "IVUS_GATEWAY_START_TIMEOUT_SECONDS",
	"IVUS_SCRIPT_TIMEOUT_SECONDS", "IVUS_SHUTDOWN_TIMEOUT_SECONDS",
	"IVUS_GATEWAY_PORT", "IVUS_GATEWAY_TOKEN", "IVUS_LLM_BASE_URL", "IVUS_LLM_MODEL",
	"IVUS_LLM_API_KEY", "IVUS_LLM_API_STYLE", "IVUS_LLM_CONTEXT_WINDOW",
	"IVUS_LLM_MAX_OUTPUT_TOKENS", "IVUS_RATE_LIMIT_PER_MINUTE",
	"IVUS_REQUEST_CACHE_ENTRIES", "IVUS_REQUEST_CACHE_TTL_SECONDS",
	"IVUS_MOSQUITTO_PUB", "IVUS_MQTT_USERNAME", "IVUS_MQTT_PASSWORD",
	"MOSQUITTO_HOST", "MOSQUITTO_PORT", "CONTROL_TOPIC_MOTOR",
}

type Config struct {
	Root                                                           string
	Enabled                                                        bool
	SocketPath                                                     string
	MaxTextRunes, MaxFrameBytes, QueueSize, MaxConnections         int
	SensorMaxAge, AgentTimeout, GatewayStartTimeout                time.Duration
	ScriptTimeout, ShutdownTimeout                                 time.Duration
	GatewayPort                                                    int
	GatewayToken                                                   string
	ProviderURL, Model, APIKey, APIStyle                           string
	ContextWindow, MaxOutputTokens                                 int
	RatePerMinute, CacheEntries                                    int
	CacheTTL                                                       time.Duration
	MosquittoPub, MQTTHost, MQTTUsername, MQTTPassword, MotorTopic string
	MQTTPort                                                       int
}

func (c Config) Join(parts ...string) string {
	return filepath.Join(append([]string{c.Root}, parts...)...)
}

func Placeholder(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.HasPrefix(value, "REPLACE_") || strings.HasPrefix(value, "CHANGE_ME")
}

func (c Config) ModelConfigured() bool {
	return !Placeholder(c.ProviderURL) && !Placeholder(c.Model) && !Placeholder(c.APIKey) && !Placeholder(c.GatewayToken)
}

func FromValues(root string, v map[string]string) (Config, error) {
	var c Config
	absolute, err := filepath.Abs(root)
	if err != nil {
		return c, fmt.Errorf("invalid module root")
	}
	c.Root = filepath.Clean(absolute)
	var first error
	number := func(key string, min, max int) int {
		n, err := strconv.Atoi(v[key])
		if (err != nil || n < min || n > max) && first == nil {
			first = fmt.Errorf("%s must be an integer in %d..%d", key, min, max)
		}
		return n
	}
	text := func(key string) string {
		value := v[key]
		if value == "" && first == nil {
			first = fmt.Errorf("%s is required", key)
		}
		if strings.ContainsAny(value, "\r\n\x00") && first == nil {
			first = fmt.Errorf("%s must not contain control characters", key)
		}
		return value
	}
	c.Enabled, err = strconv.ParseBool(v["IVUS_ENABLED"])
	if err != nil {
		first = fmt.Errorf("IVUS_ENABLED must be true or false")
	}
	relative := text("IVUS_SOCKET_PATH")
	if filepath.IsAbs(relative) {
		c.SocketPath = filepath.Clean(relative)
	} else {
		c.SocketPath = c.Join(relative)
	}
	if !Within(c.Root, c.SocketPath) || len([]byte(c.SocketPath)) > 103 {
		first = fmt.Errorf("IVUS_SOCKET_PATH must remain inside the module and fit a UNIX socket path (103 bytes)")
	}
	c.MaxTextRunes = number("IVUS_MAX_TEXT_RUNES", 1, 65536)
	c.MaxFrameBytes = number("IVUS_MAX_FRAME_BYTES", 1024, 1048576)
	c.QueueSize = number("IVUS_QUEUE_SIZE", 1, 1024)
	c.MaxConnections = number("IVUS_MAX_CONNECTIONS", 1, 128)
	c.SensorMaxAge = time.Duration(number("IVUS_SENSOR_MAX_AGE_SECONDS", 1, 86400)) * time.Second
	c.AgentTimeout = time.Duration(number("IVUS_AGENT_TIMEOUT_SECONDS", 1, 600)) * time.Second
	c.GatewayStartTimeout = time.Duration(number("IVUS_GATEWAY_START_TIMEOUT_SECONDS", 1, 300)) * time.Second
	c.ScriptTimeout = time.Duration(number("IVUS_SCRIPT_TIMEOUT_SECONDS", 1, 300)) * time.Second
	c.ShutdownTimeout = time.Duration(number("IVUS_SHUTDOWN_TIMEOUT_SECONDS", 1, 60)) * time.Second
	c.GatewayPort = number("IVUS_GATEWAY_PORT", 1, 65535)
	c.GatewayToken = text("IVUS_GATEWAY_TOKEN")
	c.ProviderURL = text("IVUS_LLM_BASE_URL")
	c.Model = text("IVUS_LLM_MODEL")
	c.APIKey = text("IVUS_LLM_API_KEY")
	c.APIStyle = text("IVUS_LLM_API_STYLE")
	if c.APIStyle != "openai-completions" && c.APIStyle != "openai-responses" {
		first = fmt.Errorf("IVUS_LLM_API_STYLE must be openai-completions or openai-responses")
	}
	c.ContextWindow = number("IVUS_LLM_CONTEXT_WINDOW", 0, 2000000)
	c.MaxOutputTokens = number("IVUS_LLM_MAX_OUTPUT_TOKENS", 0, 1000000)
	c.RatePerMinute = number("IVUS_RATE_LIMIT_PER_MINUTE", 1, 600)
	c.CacheEntries = number("IVUS_REQUEST_CACHE_ENTRIES", 1, 100000)
	c.CacheTTL = time.Duration(number("IVUS_REQUEST_CACHE_TTL_SECONDS", 1, 604800)) * time.Second
	c.MosquittoPub = text("IVUS_MOSQUITTO_PUB")
	c.MQTTHost = text("MOSQUITTO_HOST")
	c.MQTTPort = number("MOSQUITTO_PORT", 1, 65535)
	if c.MQTTHost == "0.0.0.0" {
		c.MQTTHost = "127.0.0.1"
	}
	if c.MQTTHost == "::" {
		c.MQTTHost = "::1"
	}
	c.MQTTUsername, c.MQTTPassword = v["IVUS_MQTT_USERNAME"], v["IVUS_MQTT_PASSWORD"]
	c.MotorTopic = text("CONTROL_TOPIC_MOTOR")
	if strings.ContainsAny(c.MotorTopic, "+#\x00\r\n") || strings.HasSuffix(c.MotorTopic, "/") {
		first = fmt.Errorf("CONTROL_TOPIC_MOTOR must be a literal MQTT prefix")
	}
	if !Placeholder(c.ProviderURL) {
		parsed, err := url.Parse(c.ProviderURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			first = fmt.Errorf("invalid IVUS_LLM_BASE_URL")
		}
	}
	return c, first
}

func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ReadEnv reads data without executing shell expansions or command substitutions.
func ReadEnv(reader io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1048576)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("invalid .env assignment on line %d", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			if len(value) < 2 || value[len(value)-1] != quote {
				return nil, fmt.Errorf("unclosed .env quote on line %d", line)
			}
			if quote == '"' {
				decoded, err := strconv.Unquote(value)
				if err != nil {
					return nil, fmt.Errorf("invalid .env quoted value on line %d", line)
				}
				value = decoded
			} else {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values, scanner.Err()
}

func Encode(c Config) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
func Decode(encoded string) (Config, error) {
	var c Config
	if encoded == "" {
		return c, fmt.Errorf("compiled configuration missing; use scripts/build-local.sh")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return c, fmt.Errorf("invalid compiled configuration")
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("invalid compiled configuration")
	}
	return c, nil
}
