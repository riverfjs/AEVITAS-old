package pluginmgr

import (
	"path/filepath"
	"testing"
)

func TestFixedPluginHostScriptPath_UsesHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/aev-home")
	got := fixedPluginHostScriptPath()
	want := filepath.Join("/tmp/aev-home", ".aevitas", "bin", "shim", "plugin-host.mjs")
	if got != want {
		t.Fatalf("fixedPluginHostScriptPath() = %q, want %q", got, want)
	}
}

