package main

import "smart-home/control/internal/config"

// Set by CMake using -ldflags -X. Runtime environment variables are not read.
var compiledConfig string

func loadConfig() (config.Config, error) {
	return config.Parse(compiledConfig)
}
