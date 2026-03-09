package channel

import (
	"os"
	"path/filepath"
	"strings"
)

// RestartTriggerFilePath returns the shared trigger file path used by
// /restart writer and startup notifier reader.
func RestartTriggerFilePath() string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		if v, err := os.UserHomeDir(); err == nil {
			home = strings.TrimSpace(v)
		}
	}
	if home == "" {
		return filepath.Join(".aevitas", "restart_trigger.txt")
	}
	return filepath.Join(home, ".aevitas", "restart_trigger.txt")
}
