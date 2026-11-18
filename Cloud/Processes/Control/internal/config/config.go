package config

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Retry struct {
	Interval time.Duration
	Attempts int
}

type Audio struct {
	Codec            string `json:"codec"`
	SampleFormat     string `json:"sample_format"`
	Channels         int    `json:"channels"`
	SampleRate       int    `json:"sample_rate"`
	FragmentDuration int    `json:"fragment_duration"`
	FragmentSize     int    `json:"fragment_size"`
}

type Topics struct {
	Light, Temperature, Motor, Hab, Voice, Journal string
}

func (t Topics) Subscriptions() []string {
	var result []string
	for _, prefix := range []string{t.Light, t.Temperature} {
		for _, suffix := range []string{"/data", "/info", "/signal"} {
			result = append(result, prefix+suffix)
		}
	}
	return append(result, t.Motor+"/command/feedback", t.Motor+"/info", t.Motor+"/signal",
		t.Motor+"/command/up", t.Motor+"/command/down",
		t.Hab+"/voice/command/feedback", t.Hab+"/info", t.Hab+"/signal", t.Voice)
}

type Config struct {
	MQTTAddress, ClientID, HTTPAddress     string
	QoS                                    byte
	QueueSize                              int
	HistoryPoints, JournalLimit            int
	PublishTimeout, HTTPTimeout            time.Duration
	ShutdownTimeout, ReconnectInterval     time.Duration
	KeepAlive                              time.Duration
	HabRetry, MotorRetry                   Retry
	Audio                                  Audio
	LightID, TemperatureID, MotorID, HabID string
	Topics                                 Topics
	IvusEnabled                            bool
	IvusSocketPath                         string
	IvusWriteTimeout, IvusDedupWindow      time.Duration
}

// Parse accepts only the settings explicitly selected by CMake, not the whole .env.
func Parse(compiled string) (Config, error) {
	var c Config
	if compiled == "" {
		return c, fmt.Errorf("compiled configuration is missing; build Control through CMake with Cloud/.env exported")
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(compiled), &values); err != nil {
		return c, fmt.Errorf("invalid compiled configuration: %w", err)
	}
	var firstErr error
	text := func(key string) string {
		value := values[key]
		if strings.TrimSpace(value) == "" && firstErr == nil {
			firstErr = fmt.Errorf("%s is required at compilation", key)
		}
		return value
	}
	number := func(key string, min, max int) int {
		value, err := strconv.Atoi(text(key))
		if (err != nil || value < min || value > max) && firstErr == nil {
			firstErr = fmt.Errorf("%s must be between %d and %d", key, min, max)
		}
		return value
	}
	seconds := func(key string) time.Duration {
		return time.Duration(number(key, 1, 86400)) * time.Second
	}
	host := text("MOSQUITTO_HOST")
	switch host {
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	c.MQTTAddress = "tcp://" + net.JoinHostPort(host, strconv.Itoa(number("MOSQUITTO_PORT", 1, 65535)))
	c.HTTPAddress = net.JoinHostPort(text("REMOTE_CONTROL_HOST"), strconv.Itoa(number("REMOTE_CONTROL_PORT", 1, 65535)))
	c.ClientID = text("CONTROL_MQTT_CLIENT_ID")
	c.QoS = byte(number("CONTROL_MQTT_QOS", 0, 2))
	c.QueueSize = number("CONTROL_QUEUE_SIZE", 1, 65536)
	if value := values["CONTROL_IVUS_ENABLED"]; value != "" {
		var err error
		c.IvusEnabled, err = strconv.ParseBool(value)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("CONTROL_IVUS_ENABLED must be true or false")
		}
		if c.IvusEnabled {
			c.IvusSocketPath = text("CONTROL_IVUS_SOCKET_PATH")
			if !filepath.IsAbs(c.IvusSocketPath) && firstErr == nil {
				firstErr = fmt.Errorf("CONTROL_IVUS_SOCKET_PATH must be absolute after CMake configuration")
			}
			c.IvusWriteTimeout = time.Duration(number("CONTROL_IVUS_WRITE_TIMEOUT_MS", 1, 60000)) * time.Millisecond
			c.IvusDedupWindow = time.Duration(number("CONTROL_IVUS_DEDUP_WINDOW_MS", 0, 60000)) * time.Millisecond
		}
	}
	c.HistoryPoints = number("CONTROL_HISTORY_POINTS", 1, 10000)
	c.JournalLimit = number("CONTROL_JOURNAL_LIMIT", 1, 10000)
	c.PublishTimeout = time.Duration(number("CONTROL_PUBLISH_TIMEOUT_MS", 1, 60000)) * time.Millisecond
	c.HTTPTimeout = seconds("CONTROL_HTTP_TIMEOUT_SECONDS")
	c.ShutdownTimeout = seconds("CONTROL_SHUTDOWN_TIMEOUT_SECONDS")
	c.ReconnectInterval = seconds("CONTROL_MQTT_RECONNECT_SECONDS")
	c.KeepAlive = seconds("CONTROL_MQTT_KEEPALIVE_SECONDS")
	c.HabRetry = Retry{seconds("CONTROL_HAB_RETRY_SECONDS"), number("CONTROL_HAB_MAX_ATTEMPTS", 1, 10000)}
	c.MotorRetry = Retry{seconds("CONTROL_MOTOR_RETRY_SECONDS"), number("CONTROL_MOTOR_MAX_ATTEMPTS", 1, 10000)}
	c.Audio = Audio{
		Codec: text("CONTROL_HAB_CODEC"), SampleFormat: text("CONTROL_HAB_SAMPLE_FORMAT"),
		Channels: number("CONTROL_HAB_CHANNELS", 1, 8), SampleRate: number("CONTROL_HAB_SAMPLE_RATE", 1, 192000),
		FragmentDuration: number("CONTROL_HAB_FRAGMENT_DURATION_MS", 1, 10000),
		FragmentSize:     number("CONTROL_HAB_FRAGMENT_SIZE", 1, 10000000),
	}
	c.LightID, c.TemperatureID = text("CONTROL_LIGHT_DEVICE_ID"), text("CONTROL_TEMPERATURE_DEVICE_ID")
	c.MotorID, c.HabID = text("CONTROL_MOTOR_DEVICE_ID"), text("CONTROL_HAB_DEVICE_ID")
	c.Topics = Topics{
		Light: text("CONTROL_TOPIC_LIGHT"), Temperature: text("CONTROL_TOPIC_TEMPERATURE"),
		Motor: text("CONTROL_TOPIC_MOTOR"), Hab: text("CONTROL_TOPIC_HAB"),
		Voice: text("CONTROL_TOPIC_VOICE"), Journal: text("CONTROL_TOPIC_JOURNAL"),
	}
	if firstErr != nil {
		return c, firstErr
	}
	if c.PublishTimeout >= c.MotorRetry.Interval || c.PublishTimeout >= c.HabRetry.Interval {
		return c, fmt.Errorf("CONTROL_PUBLISH_TIMEOUT_MS must be shorter than both retry intervals")
	}
	if c.Audio.Codec != "PCM" || c.Audio.SampleFormat != "s16le" {
		return c, fmt.Errorf("only PCM/s16le is supported by the current Voice service")
	}
	if c.Audio.SampleRate*c.Audio.Channels*2*c.Audio.FragmentDuration != c.Audio.FragmentSize*1000 {
		return c, fmt.Errorf("audio fragment size does not match sample rate, channels and duration")
	}
	seen := make(map[string]bool)
	for _, topic := range c.Topics.Subscriptions() {
		if strings.ContainsAny(topic, "+#\x00") || strings.HasSuffix(topic, "/") || seen[topic] {
			return c, fmt.Errorf("device topic prefixes must produce distinct literal MQTT topics")
		}
		seen[topic] = true
	}
	return c, nil
}
