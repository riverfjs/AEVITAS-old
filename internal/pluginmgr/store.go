package pluginmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/riverfjs/aevitas/internal/config"
)

type Registry struct {
	Plugins map[string]RegistryPlugin `json:"plugins"`
}

type RegistryPlugin struct {
	ID          string `json:"id"`
	Version     string `json:"version,omitempty"`
	InstallPath string `json:"installPath"`
	Source      string `json:"source,omitempty"`
	Enabled     bool   `json:"enabled"`
	InstalledAt string `json:"installedAt"`
}

type Store struct {
	cfg *config.Config
}

func NewStore(cfg *config.Config) *Store {
	return &Store{cfg: cfg}
}

func (s *Store) EnsureLayout() error {
	if err := os.MkdirAll(s.cfg.PluginHomeDir(), 0755); err != nil {
		return fmt.Errorf("create plugin home: %w", err)
	}
	registryPath := s.cfg.PluginRegistryPath()
	if _, err := os.Stat(registryPath); err == nil {
		return nil
	}
	reg := Registry{Plugins: map[string]RegistryPlugin{}}
	return s.writeRegistry(reg)
}

func (s *Store) List() (Registry, error) {
	if err := s.EnsureLayout(); err != nil {
		return Registry{}, err
	}
	return s.readRegistry()
}

func (s *Store) InstallPlugin(pluginID string) (RegistryPlugin, error) {
	if err := s.EnsureLayout(); err != nil {
		return RegistryPlugin{}, err
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return RegistryPlugin{}, fmt.Errorf("plugin id is required")
	}
	entryCfg, ok := s.cfg.Plugins.Entry(pluginID)
	if !ok {
		return RegistryPlugin{}, fmt.Errorf("plugins.%s not configured", pluginID)
	}
	source := strings.TrimSpace(entryCfg.Source.NpmSpec)
	if source == "" {
		return RegistryPlugin{}, fmt.Errorf("plugins.%s.source.npmSpec is required", pluginID)
	}
	targetDir := filepath.Join(s.cfg.PluginHomeDir(), pluginID)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return RegistryPlugin{}, fmt.Errorf("create plugin dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "aevitas-plugin-pack-*")
	if err != nil {
		return RegistryPlugin{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tgz, err := s.npmPack(source, tmpDir)
	if err != nil {
		return RegistryPlugin{}, err
	}
	if err := s.extractTarball(tgz, targetDir); err != nil {
		return RegistryPlugin{}, err
	}
	if err := s.installPluginDependencies(targetDir); err != nil {
		return RegistryPlugin{}, err
	}
	if err := s.writeRuntimeDescriptor(targetDir); err != nil {
		return RegistryPlugin{}, err
	}
	version := readPackageVersion(filepath.Join(targetDir, "package", "package.json"))
	entry := RegistryPlugin{
		ID:          pluginID,
		Version:     version,
		InstallPath: targetDir,
		Source:      source,
		Enabled:     true,
		InstalledAt: time.Now().Format(time.RFC3339),
	}
	reg, err := s.readRegistry()
	if err != nil {
		return RegistryPlugin{}, err
	}
	reg.Plugins[pluginID] = entry
	if err := s.writeRegistry(reg); err != nil {
		return RegistryPlugin{}, err
	}
	return entry, nil
}

func (s *Store) writeRuntimeDescriptor(targetDir string) error {
	type runtimeDescriptor struct {
		Command    string `json:"command,omitempty"`
		HostCompat string `json:"hostCompat"`
		Entry      string `json:"entry,omitempty"`
	}
	nodeCommand := strings.TrimSpace(os.Getenv("AEVITAS_PLUGIN_NODE"))
	if nodeCommand == "" {
		nodeCommand = "node"
	}
	desc := runtimeDescriptor{Command: nodeCommand, HostCompat: "openclaw", Entry: "package/index.js"}
	raw, err := json.MarshalIndent(desc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime descriptor: %w", err)
	}
	p := filepath.Join(targetDir, "runtime.json")
	if err := os.WriteFile(p, raw, 0644); err != nil {
		return fmt.Errorf("write runtime descriptor: %w", err)
	}
	return nil
}

func (s *Store) Remove(pluginID string) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("plugin id is required")
	}
	reg, err := s.readRegistry()
	if err != nil {
		return err
	}
	entry, ok := reg.Plugins[pluginID]
	if ok && strings.TrimSpace(entry.InstallPath) != "" {
		_ = os.RemoveAll(entry.InstallPath)
	}
	delete(reg.Plugins, pluginID)
	return s.writeRegistry(reg)
}

func (s *Store) Doctor(pluginID string) (string, error) {
	reg, err := s.readRegistry()
	if err != nil {
		return "", err
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return "", fmt.Errorf("plugin id is required")
	}
	entry, ok := reg.Plugins[pluginID]
	if !ok {
		return fmt.Sprintf("%s: not installed", pluginID), nil
	}
	if _, err := os.Stat(entry.InstallPath); err != nil {
		return fmt.Sprintf("%s: missing install path %s", pluginID, entry.InstallPath), nil
	}
	pkgDir := filepath.Join(entry.InstallPath, "package")
	nodeSDK := filepath.Join(pkgDir, "node_modules", "@larksuiteoapi", "node-sdk", "package.json")
	if _, err := os.Stat(nodeSDK); err != nil {
		return fmt.Sprintf("%s: installed but dependencies missing. run: cd %s && npm install --omit=dev", pluginID, pkgDir), nil
	}
	return fmt.Sprintf("%s: installed version=%s path=%s enabled=%v deps=ok", pluginID, entry.Version, entry.InstallPath, entry.Enabled), nil
}

func (s *Store) npmPack(spec, outDir string) (string, error) {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("npm pack %q --pack-destination %q", spec, outDir))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("npm pack failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	filename := strings.TrimSpace(lines[len(lines)-1])
	if filename == "" {
		return "", fmt.Errorf("npm pack returned empty filename")
	}
	return filepath.Join(outDir, filename), nil
}

func (s *Store) extractTarball(tgzPath, targetDir string) error {
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("cleanup old install: %w", err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf("tar -xzf %q -C %q", tgzPath, targetDir))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("extract tarball failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *Store) readRegistry() (Registry, error) {
	if err := s.EnsureLayout(); err != nil {
		return Registry{}, err
	}
	raw, err := os.ReadFile(s.cfg.PluginRegistryPath())
	if err != nil {
		return Registry{}, fmt.Errorf("read registry: %w", err)
	}
	var reg Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		return Registry{}, fmt.Errorf("parse registry: %w", err)
	}
	if reg.Plugins == nil {
		reg.Plugins = map[string]RegistryPlugin{}
	}
	return reg, nil
}

func (s *Store) writeRegistry(reg Registry) error {
	if reg.Plugins == nil {
		reg.Plugins = map[string]RegistryPlugin{}
	}
	raw, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	return os.WriteFile(s.cfg.PluginRegistryPath(), raw, 0644)
}

func readPackageVersion(pkgPath string) string {
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return ""
	}
	return strings.TrimSpace(pkg.Version)
}

func (s *Store) installPluginDependencies(targetDir string) error {
	if strings.TrimSpace(os.Getenv("AEVITAS_PLUGIN_SKIP_NPM_INSTALL")) == "1" {
		return nil
	}
	pkgDir := filepath.Join(targetDir, "package")
	if _, err := os.Stat(filepath.Join(pkgDir, "package.json")); err != nil {
		return fmt.Errorf("plugin package.json not found: %w", err)
	}
	cmd := exec.Command("sh", "-c", "npm install --omit=dev")
	cmd.Dir = pkgDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if strings.Contains(strings.ToLower(msg), "command not found") || strings.Contains(strings.ToLower(msg), "npm: not found") {
			return fmt.Errorf("install plugin dependencies failed: npm not found in PATH")
		}
		return fmt.Errorf("install plugin dependencies failed: %w (%s)", err, msg)
	}
	return nil
}
