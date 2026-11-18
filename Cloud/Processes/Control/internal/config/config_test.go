package config

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func values(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("testdata/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	values["MOSQUITTO_HOST"] = "0.0.0.0"
	return values
}

func TestCompiledSettings(t *testing.T) {
	v := values(t)
	data, _ := json.Marshal(v)
	t.Setenv("MOSQUITTO_PORT", "9999")
	c, err := Parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTTAddress != "tcp://127.0.0.1:1883" || c.HabRetry.Attempts != 45 || c.HabRetry.Interval != 4*time.Second || c.MotorRetry.Attempts != 30 || c.MotorRetry.Interval != 2*time.Second {
		t.Fatalf("wrong compiled settings: %+v", c)
	}
	if len(c.Topics.Subscriptions()) != 15 {
		t.Fatal("missing subscriptions")
	}
	if _, err := Parse(""); err == nil {
		t.Fatal("runtime environment substituted for compiled configuration")
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for key, value := range map[string]string{
		"REMOTE_CONTROL_PORT":        "0",
		"CONTROL_HAB_MAX_ATTEMPTS":   "0",
		"CONTROL_PUBLISH_TIMEOUT_MS": "4000",
		"CONTROL_HAB_FRAGMENT_SIZE":  "1",
		"CONTROL_TOPIC_VOICE":        "motor/home/info",
	} {
		t.Run(key, func(t *testing.T) {
			v := values(t)
			v[key] = value
			data, _ := json.Marshal(v)
			if _, err := Parse(string(data)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
