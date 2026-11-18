package buildconfig

import (
	"fmt"
	"os"

	"smart-home/ai-assistant/internal/config"
)

func Load(root, envPath string) (config.Config, error) {
	file, err := os.Open(envPath)
	if err != nil {
		return config.Config{}, fmt.Errorf("cannot open .env")
	}
	defer file.Close()
	all, err := config.ReadEnv(file)
	if err != nil {
		return config.Config{}, err
	}
	selected := make(map[string]string)
	for _, key := range config.Keys {
		selected[key] = all[key]
	}
	return config.FromValues(root, selected)
}
