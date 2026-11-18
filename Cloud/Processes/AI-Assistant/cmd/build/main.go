package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"smart-home/ai-assistant/internal/buildconfig"
	"smart-home/ai-assistant/internal/config"
)

func main() {
	env := flag.String("env", "../../.env", "trusted dotenv file")
	output := flag.String("output", "bin/shs_cloud_ai-assistant", "output executable")
	moduleRoot := flag.String("root", "", "module directory on the target machine")
	goos := flag.String("os", runtime.GOOS, "target OS")
	goarch := flag.String("arch", runtime.GOARCH, "target architecture")
	flag.Parse()
	root, err := os.Getwd()
	if *moduleRoot != "" {
		root = *moduleRoot
	}
	if err == nil {
		var settings config.Config
		settings, err = buildconfig.Load(root, *env)
		if err == nil {
			var encoded string
			encoded, err = config.Encode(settings)
			if err == nil {
				err = os.MkdirAll(filepath.Dir(*output), 0755)
				if err == nil {
					command := exec.Command("go", "build", "-ldflags", "-X main.compiledConfig="+encoded, "-o", *output, ".")
					command.Env = append(os.Environ(), "GOOS="+*goos, "GOARCH="+*goarch, "CGO_ENABLED=0")
					command.Stdout, command.Stderr = os.Stdout, os.Stderr
					err = command.Run()
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "IVUS build failed:", err)
		os.Exit(1)
	}
}
