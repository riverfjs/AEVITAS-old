package pluginmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/riverfjs/aevitas/internal/config"
)

type RuntimeSpec struct {
	Command string
	Args    []string
	WorkDir string
	Env     map[string]string
}

type RuntimeDescriptor struct {
	Command      string            `json:"command,omitempty"`
	HostCompat   string            `json:"hostCompat"`
	Platform     string            `json:"platform"`
	Entry        string            `json:"entry"`
	Args         []string          `json:"args,omitempty"`
	Listen       string            `json:"listen,omitempty"`
	OutboundPath string            `json:"outboundPath,omitempty"`
	WorkDir      string            `json:"workDir,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

type RuntimeState struct {
	PluginID  string            `json:"pluginId"`
	Status    string            `json:"status,omitempty"`
	Running   bool              `json:"running"`
	PID       int               `json:"pid,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	WorkDir   string            `json:"workDir,omitempty"`
	LogPath   string            `json:"logPath,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	StartedAt string            `json:"startedAt,omitempty"`
	StoppedAt string            `json:"stoppedAt,omitempty"`
	LastError string            `json:"lastError,omitempty"`
}

type RuntimeManager struct {
	cfg *config.Config
	sv  *runtimeSupervisor
}

type runtimeSupervisor struct {
	mu      sync.Mutex
	started bool
	tasks   chan string
}

const (
	taskStartEnabled = "start-enabled"
	taskStopAll      = "stop-all"
)

var (
	supervisorRegistryMu sync.Mutex
	supervisorRegistry   = map[string]*runtimeSupervisor{}
)

func NewRuntimeManager(cfg *config.Config) *RuntimeManager {
	return &RuntimeManager{
		cfg: cfg,
		sv:  getOrCreateSupervisor(cfg.PluginHomeDir()),
	}
}

func getOrCreateSupervisor(home string) *runtimeSupervisor {
	key := strings.TrimSpace(home)
	supervisorRegistryMu.Lock()
	defer supervisorRegistryMu.Unlock()
	if sv, ok := supervisorRegistry[key]; ok {
		return sv
	}
	sv := &runtimeSupervisor{tasks: make(chan string, 16)}
	supervisorRegistry[key] = sv
	return sv
}

func (m *RuntimeManager) Start(pluginID string, spec RuntimeSpec) (RuntimeState, error) {
	m.debugf("start.begin plugin=%s command=%s args=%d workdir=%s", pluginID, strings.TrimSpace(spec.Command), len(spec.Args), strings.TrimSpace(spec.WorkDir))
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return RuntimeState{}, fmt.Errorf("plugin id is required")
	}
	spec.Command = strings.TrimSpace(spec.Command)
	if spec.Command == "" {
		return RuntimeState{}, fmt.Errorf("runtime command is required")
	}
	if err := m.ensureLayout(); err != nil {
		return RuntimeState{}, err
	}
	current, _ := m.readState(pluginID)
	if current.Running && current.PID > 0 {
		if processAlive(current.PID) {
			return RuntimeState{}, fmt.Errorf("plugin runtime already marked running: %s (pid=%d)", pluginID, current.PID)
		}
		current.Running = false
		current.Status = "stopped"
		current.StoppedAt = time.Now().Format(time.RFC3339)
		current.LastError = "stale runtime state reset: process not found"
		_ = m.writeState(current)
		m.debugf("start.stale-state-reset plugin=%s stale_pid=%d", pluginID, current.PID)
	}

	logPath := m.logPath(pluginID)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("open runtime log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(spec.Command, spec.Args...)
	if wd := strings.TrimSpace(spec.WorkDir); wd != "" {
		cmd.Dir = wd
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append([]string{}, os.Environ()...)
	for k, v := range spec.Env {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, v))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		m.debugf("start.error plugin=%s err=%v", pluginID, err)
		return RuntimeState{}, fmt.Errorf("start runtime process: %w", err)
	}

	state := RuntimeState{
		PluginID:  pluginID,
		Status:    "running",
		Running:   true,
		PID:       cmd.Process.Pid,
		Command:   spec.Command,
		Args:      append([]string(nil), spec.Args...),
		WorkDir:   strings.TrimSpace(spec.WorkDir),
		LogPath:   logPath,
		Env:       spec.Env,
		StartedAt: time.Now().Format(time.RFC3339),
	}
	if err := m.writeState(state); err != nil {
		return RuntimeState{}, err
	}
	m.debugf("start.ok plugin=%s pid=%d log=%s", pluginID, state.PID, state.LogPath)
	return state, nil
}

func (m *RuntimeManager) ResolveRuntimeSpec(pluginID string) (RuntimeSpec, error) {
	m.debugf("resolve.begin plugin=%s", pluginID)
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return RuntimeSpec{}, fmt.Errorf("plugin id is required")
	}
	store := NewStore(m.cfg)
	reg, err := store.List()
	if err != nil {
		return RuntimeSpec{}, fmt.Errorf("read plugin registry: %w", err)
	}
	entry, ok := reg.Plugins[pluginID]
	if !ok {
		return RuntimeSpec{}, fmt.Errorf("plugin not installed: %s", pluginID)
	}
	desc, err := m.readRuntimeDescriptor(entry.InstallPath)
	if err != nil {
		return RuntimeSpec{}, err
	}
	hostScript := fixedPluginHostScriptPath()
	if _, err := os.Stat(hostScript); err != nil {
		m.debugf("resolve.error plugin=%s missing_host=%s err=%v", pluginID, hostScript, err)
		return RuntimeSpec{}, fmt.Errorf("missing plugin host script: %s", hostScript)
	}
	args := []string{
		"--experimental-specifier-resolution=node",
		hostScript,
		"--plugin-id", pluginID,
		"--compat", strings.TrimSpace(desc.HostCompat),
	}
	if strings.TrimSpace(desc.Platform) != "" {
		args = append(args, "--platform", strings.TrimSpace(desc.Platform))
	}
	if strings.TrimSpace(desc.Entry) != "" {
		entryPath := filepath.Join(entry.InstallPath, strings.TrimSpace(desc.Entry))
		if err := m.ensurePrepared(entry.InstallPath); err != nil {
			return RuntimeSpec{}, err
		}
		if err := m.ensureEntry(entry.InstallPath, entryPath); err != nil {
			return RuntimeSpec{}, err
		}
		args = append(args, "--entry", entryPath)
	} else {
		if err := m.ensurePrepared(entry.InstallPath); err != nil {
			return RuntimeSpec{}, err
		}
		if err := m.ensureEntry(entry.InstallPath, ""); err != nil {
			return RuntimeSpec{}, err
		}
	}
	if strings.TrimSpace(desc.Listen) != "" {
		args = append(args, "--listen", strings.TrimSpace(desc.Listen))
	}
	if strings.TrimSpace(desc.OutboundPath) != "" {
		args = append(args, "--outbound-path", strings.TrimSpace(desc.OutboundPath))
	}
	args = append(args, desc.Args...)
	workDir := strings.TrimSpace(desc.WorkDir)
	if workDir != "" && !filepath.IsAbs(workDir) {
		workDir = filepath.Join(entry.InstallPath, workDir)
	}
	spec := RuntimeSpec{
		Command: firstNonEmpty(strings.TrimSpace(desc.Command), "node"),
		Args:    args,
		WorkDir: workDir,
		Env:     desc.Env,
	}
	m.debugf("resolve.ok plugin=%s command=%s host=%s entry=%s", pluginID, spec.Command, hostScript, strings.TrimSpace(desc.Entry))
	return spec, nil
}

func fixedPluginHostScriptPath() string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = strings.TrimSpace(h)
		}
	}
	return filepath.Join(home, ".aevitas", "bin", "shim", "plugin-host.mjs")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (m *RuntimeManager) readRuntimeDescriptor(installPath string) (RuntimeDescriptor, error) {
	p := filepath.Join(strings.TrimSpace(installPath), "runtime.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return RuntimeDescriptor{}, fmt.Errorf("missing runtime descriptor: %s", p)
		}
		return RuntimeDescriptor{}, fmt.Errorf("read runtime descriptor: %w", err)
	}
	var desc RuntimeDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return RuntimeDescriptor{}, fmt.Errorf("parse runtime descriptor: %w", err)
	}
	if strings.TrimSpace(desc.HostCompat) == "" {
		return RuntimeDescriptor{}, fmt.Errorf("invalid runtime descriptor: hostCompat required")
	}
	return desc, nil
}

func (m *RuntimeManager) ensureEntry(installPath, entryPath string) error {
	root := strings.TrimSpace(installPath)
	if root == "" {
		return fmt.Errorf("empty install path")
	}
	if strings.TrimSpace(entryPath) == "" {
		return nil
	}
	if _, err := os.Stat(entryPath); err != nil {
		return fmt.Errorf("runtime entry not found: %s", entryPath)
	}
	return nil
}

func (m *RuntimeManager) ensurePrepared(installPath string) error {
	if strings.TrimSpace(os.Getenv("AEVITAS_PLUGIN_SKIP_NPM_INSTALL")) == "1" {
		return nil
	}
	packageDir := filepath.Join(strings.TrimSpace(installPath), "package")
	pkgJSON := filepath.Join(packageDir, "package.json")
	if _, err := os.Stat(pkgJSON); err != nil {
		return nil
	}
	nodeModules := filepath.Join(packageDir, "node_modules")
	if _, err := os.Stat(nodeModules); err == nil {
		return nil
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm not found in PATH; cannot prepare plugin dependencies")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npm", "install", "--omit=dev")
	cmd.Dir = packageDir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("npm install timeout in %s", packageDir)
	}
	if err != nil {
		return fmt.Errorf("npm install failed in %s: %v (%s)", packageDir, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *RuntimeManager) Stop(pluginID string) (RuntimeState, error) {
	m.debugf("stop.begin plugin=%s", pluginID)
	state, err := m.Status(pluginID)
	if err != nil {
		return RuntimeState{}, err
	}
	if !state.Running || state.PID <= 0 {
		state.Status = "stopped"
		_ = m.writeState(state)
		return state, nil
	}
	p, err := os.FindProcess(state.PID)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("find process: %w", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return RuntimeState{}, fmt.Errorf("send SIGTERM: %w", err)
	}
	for i := 0; i < 15; i++ {
		if !processAlive(state.PID) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if processAlive(state.PID) {
		if err := syscall.Kill(state.PID, syscall.SIGKILL); err != nil {
			return RuntimeState{}, fmt.Errorf("send SIGKILL: %w", err)
		}
		for i := 0; i < 10; i++ {
			if !processAlive(state.PID) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	if processAlive(state.PID) {
		return RuntimeState{}, fmt.Errorf("process still alive after SIGKILL: pid=%d", state.PID)
	}
	state.Running = false
	state.Status = "stopped"
	state.PID = 0
	state.StoppedAt = time.Now().Format(time.RFC3339)
	if err := m.writeState(state); err != nil {
		return RuntimeState{}, err
	}
	m.debugf("stop.ok plugin=%s pid=%d", state.PluginID, state.PID)
	return state, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func (m *RuntimeManager) StartEnabledAsync() error {
	m.debugf("start-enabled.schedule.inproc")
	return m.enqueueTask(taskStartEnabled)
}

func (m *RuntimeManager) StopAllAsync() error {
	m.debugf("stop-all.schedule.inproc")
	return m.enqueueTask(taskStopAll)
}

func (m *RuntimeManager) StartEnabled() error {
	m.debugf("start-enabled.sync")
	return m.ReconcileStartEnabled()
}

func (m *RuntimeManager) StopAll() error {
	m.debugf("stop-all.sync")
	return m.ReconcileStopAll()
}

func (m *RuntimeManager) IsRunning(pluginID string) (bool, error) {
	st, err := m.Status(pluginID)
	if err != nil {
		return false, err
	}
	return st.Running && st.PID > 0 && processAlive(st.PID), nil
}

func (m *RuntimeManager) ReconcileStartEnabled() error {
	startAt := time.Now()
	m.debugf("reconcile.start-enabled.begin")
	store := NewStore(m.cfg)
	reg, err := store.List()
	if err != nil {
		m.debugf("reconcile.start-enabled.error err=%v", err)
		return err
	}
	for id, item := range reg.Plugins {
		if !item.Enabled {
			continue
		}
		spec, serr := m.ResolveRuntimeSpec(id)
		if serr != nil {
			m.debugf("reconcile.start-enabled.resolve-failed plugin=%s err=%v", id, serr)
			_ = m.writeState(RuntimeState{
				PluginID:  id,
				Status:    "failed",
				Running:   false,
				LastError: serr.Error(),
				StoppedAt: time.Now().Format(time.RFC3339),
			})
			continue
		}
		if current, cerr := m.readState(id); cerr == nil && current.Running && current.PID > 0 {
			if runtimeDrifted(current, spec) {
				m.debugf("reconcile.start-enabled.drift-detected plugin=%s old_command=%s new_command=%s", id, current.Command, spec.Command)
				if derr := os.Remove(m.statePath(id)); derr != nil && !os.IsNotExist(derr) {
					m.debugf("reconcile.start-enabled.drift-delete-failed plugin=%s err=%v", id, derr)
				} else {
					m.debugf("reconcile.start-enabled.drift-delete-ok plugin=%s", id)
				}
			}
		}
		if _, serr := m.Start(id, spec); serr != nil {
			if !strings.Contains(strings.ToLower(serr.Error()), "already marked running") {
				m.debugf("reconcile.start-enabled.start-failed plugin=%s err=%v", id, serr)
				_ = m.writeState(RuntimeState{
					PluginID:  id,
					Status:    "failed",
					Running:   false,
					LastError: serr.Error(),
					StoppedAt: time.Now().Format(time.RFC3339),
				})
			}
		}
	}
	m.debugf("reconcile.start-enabled.done elapsed_ms=%d", time.Since(startAt).Milliseconds())
	return nil
}

func runtimeDrifted(st RuntimeState, spec RuntimeSpec) bool {
	if strings.TrimSpace(st.Command) != strings.TrimSpace(spec.Command) {
		return true
	}
	if strings.TrimSpace(st.WorkDir) != strings.TrimSpace(spec.WorkDir) {
		return true
	}
	if len(st.Args) != len(spec.Args) {
		return true
	}
	for i := range st.Args {
		if strings.TrimSpace(st.Args[i]) != strings.TrimSpace(spec.Args[i]) {
			return true
		}
	}
	return false
}

func (m *RuntimeManager) ReconcileStopAll() error {
	startAt := time.Now()
	m.debugf("reconcile.stop-all.begin")
	items, err := m.ListStatus()
	if err != nil {
		m.debugf("reconcile.stop-all.error err=%v", err)
		return err
	}
	for _, item := range items {
		_, _ = m.Stop(item.PluginID)
	}
	m.debugf("reconcile.stop-all.done elapsed_ms=%d", time.Since(startAt).Milliseconds())
	return nil
}

func (m *RuntimeManager) Status(pluginID string) (RuntimeState, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return RuntimeState{}, fmt.Errorf("plugin id is required")
	}
	state, err := m.readState(pluginID)
	if err != nil {
		if os.IsNotExist(err) {
			return RuntimeState{PluginID: pluginID, Running: false}, nil
		}
		return RuntimeState{}, err
	}
	return state, nil
}

func (m *RuntimeManager) ListStatus() ([]RuntimeState, error) {
	if err := m.ensureLayout(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.runtimeDir())
	if err != nil {
		return nil, fmt.Errorf("read runtime dir: %w", err)
	}
	out := make([]RuntimeState, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		st, err := m.readState(id)
		if err != nil {
			continue
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	return out, nil
}

func (m *RuntimeManager) ensureLayout() error {
	return os.MkdirAll(m.runtimeDir(), 0755)
}

func (m *RuntimeManager) runtimeDir() string {
	return filepath.Join(m.cfg.PluginHomeDir(), "runtime")
}

func (m *RuntimeManager) statePath(pluginID string) string {
	return filepath.Join(m.runtimeDir(), pluginID+".json")
}

func (m *RuntimeManager) logPath(pluginID string) string {
	return filepath.Join(m.runtimeDir(), pluginID+".log")
}

func (m *RuntimeManager) readState(pluginID string) (RuntimeState, error) {
	raw, err := os.ReadFile(m.statePath(pluginID))
	if err != nil {
		return RuntimeState{}, err
	}
	var state RuntimeState
	if err := json.Unmarshal(raw, &state); err != nil {
		return RuntimeState{}, fmt.Errorf("parse runtime state: %w", err)
	}
	if state.PluginID == "" {
		state.PluginID = pluginID
	}
	return state, nil
}

func (m *RuntimeManager) writeState(state RuntimeState) error {
	if err := m.ensureLayout(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime state: %w", err)
	}
	return os.WriteFile(m.statePath(state.PluginID), raw, 0644)
}

func (m *RuntimeManager) enqueueTask(mode string) error {
	if m.sv == nil {
		return fmt.Errorf("runtime supervisor not initialized")
	}
	m.sv.mu.Lock()
	if !m.sv.started {
		m.sv.started = true
		go m.runSupervisor()
	}
	m.sv.mu.Unlock()
	select {
	case m.sv.tasks <- mode:
		return nil
	default:
		return fmt.Errorf("runtime supervisor queue is full")
	}
}

func (m *RuntimeManager) runSupervisor() {
	for mode := range m.sv.tasks {
		switch strings.TrimSpace(mode) {
		case taskStartEnabled:
			if err := m.ReconcileStartEnabled(); err != nil {
				m.debugf("supervisor.task.error mode=%s err=%v", mode, err)
			}
		case taskStopAll:
			if err := m.ReconcileStopAll(); err != nil {
				m.debugf("supervisor.task.error mode=%s err=%v", mode, err)
			}
		default:
			m.debugf("supervisor.task.unknown mode=%s", mode)
		}
	}
}

func (m *RuntimeManager) debugf(format string, args ...interface{}) {
	if err := m.ensureLayout(); err != nil {
		return
	}
	logPath := filepath.Join(m.runtimeDir(), "supervisor.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "[runtime-debug] %s %s\n", time.Now().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))
}

func (m *RuntimeManager) Probe(pluginID string, timeout time.Duration) (RuntimeState, error) {
	st, err := m.Status(pluginID)
	if err != nil {
		return RuntimeState{}, err
	}
	if !st.Running || st.PID <= 0 {
		return st, nil
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-p", fmt.Sprintf("%d", st.PID), "-o", "pid=")
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return st, nil
	}
	st.Running = false
	st.LastError = "process not found"
	st.StoppedAt = time.Now().Format(time.RFC3339)
	_ = m.writeState(st)
	return st, nil
}
