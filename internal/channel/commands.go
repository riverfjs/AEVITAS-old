package channel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/usagehud"
	"github.com/riverfjs/aevitas/pkg/utils"
	"github.com/riverfjs/agentsdk-go/pkg/api"
)

// SessionResetter is an interface for clearing sessions
type SessionResetter interface {
	ClearSession(sessionID string) error
}

type UsageReporter interface {
	GetSessionStats(sessionID string) *api.SessionTokenStats
	GetTotalStats() *api.SessionTokenStats
}

// CommandHandler handles special commands before they reach the agent
type CommandHandler struct {
	runtime             SessionResetter // Runtime for session management
	workspace           string          // Workspace path for listing skills
	contextWindowTokens int
}

// NewCommandHandler creates a new command handler
func NewCommandHandler(runtime SessionResetter, workspace string, contextWindowTokens int) *CommandHandler {
	return &CommandHandler{
		runtime:             runtime,
		workspace:           workspace,
		contextWindowTokens: contextWindowTokens,
	}
}

// CommandResult represents the result of command processing
type CommandResult struct {
	Handled  bool     // Whether the command was handled
	Response string   // Response message to send back
	Files    []string // File paths to send (e.g., log files)
	Event    string   // Optional event hint for channel rendering
	Restart  bool     // Whether gateway should execute restart flow
}

// HandleCommand processes special commands and returns whether it was handled.
func (h *CommandHandler) HandleCommand(msg bus.InboundMessage) CommandResult {
	content := strings.TrimSpace(msg.Content)
	
	// Check if it's a command (starts with /)
	if !strings.HasPrefix(content, "/") {
		return CommandResult{Handled: false}
	}

	// Extract command and args
	parts := strings.Fields(content)
	if len(parts) == 0 {
		return CommandResult{Handled: false}
	}

	command := strings.ToLower(parts[0])

	switch command {
	case "/start":
		return CommandResult{
			Handled: true,
			Response: h.handleStart(),
		}
	case "/help":
		return CommandResult{
			Handled: true,
			Response: h.handleHelp(),
		}
	case "/reset":
		return CommandResult{
			Handled:  true,
			Response: h.handleReset(msg.SessionKey()),
		}
	case "/restart":
		resp, ok := h.handleRestart()
		if !ok {
			return CommandResult{
				Handled:  true,
				Response: resp,
			}
		}
		// Save chat info for post-restart notification.
		restartInfo := fmt.Sprintf("%s:%s", msg.Channel, msg.ChatID)
		restartTriggerFile := filepath.Join(os.Getenv("HOME"), ".aevitas", "restart_trigger.txt")
		if err := os.MkdirAll(filepath.Dir(restartTriggerFile), 0755); err != nil {
			return CommandResult{
				Handled:  true,
				Response: fmt.Sprintf("❌ Failed to prepare restart: %v", err),
			}
		}
		if err := os.WriteFile(restartTriggerFile, []byte(restartInfo), 0644); err != nil {
			return CommandResult{
				Handled:  true,
				Response: fmt.Sprintf("❌ Failed to prepare restart: %v", err),
			}
		}
		return CommandResult{
			Handled: true,
			Response: resp,
			Restart: true,
		}
	case "/logs":
		// Parse argument: number or "all"
		arg := "100" // default 100 lines
		if len(parts) > 1 {
			arg = strings.ToLower(parts[1])
		}
		return h.handleLogs(arg)
	case "/status":
		return CommandResult{
			Handled: true,
			Response: h.handleStatus(),
		}
	case "/usage":
		mode := ""
		if len(parts) > 1 {
			mode = strings.ToLower(strings.TrimSpace(parts[1]))
		}
		return CommandResult{
			Handled:  true,
			Response: h.handleUsage(msg.SessionKey(), mode),
			Event:    "usage_hud",
		}
	case "/chatid":
		return CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("💬 **Your Chat Information**\n\nChannel: %s\nChat ID: `%s`\nSender ID: `%s`", msg.Channel, msg.ChatID, msg.SenderID),
		}
	case "/cleanup":
		// Check if user is confirming previous cleanup request.
		// Supported:
		//   /cleanup confirm
		//   /cleanup confirm all|temp|tts|var
		//   /cleanup yes
		if len(parts) > 1 {
			sub := strings.ToLower(parts[1])
			if sub == "yes" {
				return h.handleCleanupConfirm(msg.ChatID, "all")
			}
			if sub == "confirm" {
				scope := "all"
				if len(parts) > 2 {
					scope = strings.ToLower(strings.TrimSpace(parts[2]))
				}
				return h.handleCleanupConfirm(msg.ChatID, scope)
			}
		}
		// Initial cleanup request - scan and show stats
		return h.handleCleanupScan(msg.ChatID)
	case "/skill":
		// Handle /skill list
		if len(parts) > 1 && strings.ToLower(parts[1]) == "list" {
			return CommandResult{
				Handled: true,
				Response: h.handleSkillList(),
			}
		}
		// Unknown /skill subcommand
		return CommandResult{
			Handled: true,
			Response: "❓ Unknown command. Use `/skill list` to see available skills.",
		}
	default:
		// Unknown command - return friendly message instead of passing to agent
		return CommandResult{
			Handled: true,
			Response: fmt.Sprintf("❓ Unknown command: %s\n\nUse /help to see available commands.", command),
		}
	}
}

func (h *CommandHandler) handleStart() string {
	return `🚀 **Aevitas Activated**

Advanced Evolutionary Virtual Intelligence with Temporal Awareness System online and operational.

Type /help to see what I can do.`
}

func (h *CommandHandler) handleHelp() string {
	return `📚 **Aevitas Capabilities**

**Core Tools:**
• File Operations: Read, Write, Edit files
• Web: Search (Brave API), Fetch web pages  
• Code: Execute commands, Search text, Find files
• Vision: Analyze images (multimodal support)

**Skills:**
Use /skill list to see installed skills

**Commands:**
• /start - Welcome message
• /help - Show this help  
• /skill list - List installed skills
• /reset - Clear conversation history
• /restart - Restart gateway (production only)
• /logs [lines|all] - Show logs (default 100 lines, max 1000, or "all" for full file)
• /status - Show gateway status
• /usage [total] - Show token usage (session or total)
• /chatid - Show your chat ID
• /cleanup - Clean temp/tts/var files (supports confirm all|temp|tts|var)

**Multimodal:**
Send images with text - I can analyze photos, diagrams, screenshots, etc.

Just send a message or image to get started!`
}

func (h *CommandHandler) handleReset(sessionKey string) string {
	if h.runtime == nil {
		return "⚠️ Session reset is not available"
	}
	if err := h.runtime.ClearSession(sessionKey); err != nil {
		return fmt.Sprintf("❌ Failed to reset session: %v", err)
	}
	return "✅ **Session Reset**\n\nLet's start fresh!"
}


func (h *CommandHandler) handleSkillList() string {
	if h.workspace == "" {
		return "⚠️ Skill listing is not available (workspace not configured)"
	}

	skills, err := utils.ListInstalledSkills(h.workspace)
	if err != nil {
		return fmt.Sprintf("❌ Failed to list skills: %v", err)
	}

	if len(skills) == 0 {
		return "📦 **Installed Skills**\n\nNo skills are currently installed.\n\nYou can install skills using the `Skill` tool or ask me to help you create new ones!"
	}

	var sb strings.Builder
	sb.WriteString("📦 **Installed Skills**\n\n")
	for i, skill := range skills {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, skill))
	}
	sb.WriteString("\nUse the `Skill` tool to load a skill's documentation, or ask me about what a specific skill can do!")

	return sb.String()
}

func (h *CommandHandler) handleUsage(sessionKey, mode string) string {
	reporter, ok := h.runtime.(UsageReporter)
	if !ok || reporter == nil {
		return "⚠️ Usage report is not available"
	}

	stats := reporter.GetSessionStats(sessionKey)
	title := "📊 Usage (Current Session)"
	inputTokens := 0
	if stats != nil {
		inputTokens = int(stats.TotalInput)
	}
	if mode == "total" {
		stats = reporter.GetTotalStats()
		title = "📊 Usage (Total)"
		if stats != nil {
			inputTokens = int(stats.TotalInput)
		}
	}
	if stats == nil {
		return "⚠️ Usage data is not available yet"
	}
	return usagehud.Format(title, stats, inputTokens, h.contextWindowTokens)
}

func (h *CommandHandler) RestartScriptPath() string {
	return filepath.Join(os.Getenv("HOME"), ".aevitas", "bin", "..", "..", "Documents", "chatbot", "aevitas", "scripts", "restart.sh")
}

func (h *CommandHandler) handleRestart() (string, bool) {
	scriptPath := h.RestartScriptPath()
	
	// Check if restart script exists
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return "⚠️ Restart script not found. This command only works in production mode.\n\nUse `make prod` to install and `scripts/start.sh` to run in background.", false
	}

	return "🔄 Restarting Gateway\n\nThe gateway will restart in a few seconds. You'll receive a notification when it's back online.", true
}

func (h *CommandHandler) handleLogs(arg string) CommandResult {
	// Always use home directory for logs in production
	logFile := filepath.Join(os.Getenv("HOME"), ".aevitas", "workspace", "logs", "aevitas.log")
	
	// Check if log file exists
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		return CommandResult{
			Handled:  true,
			Response: "⚠️ Log file not found at: " + logFile,
		}
	}
	
	// Check if user wants full file
	if arg == "all" {
		return CommandResult{
			Handled:  true,
			Files:    []string{logFile},
		}
	}
	
	// Parse number of lines (default 100)
	lines := 100
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		if n > 1000 {
			lines = 1000 // Max 1000 lines
		} else {
			lines = n
		}
	}
	
	// Read last N lines
	content, err := readLastLines(logFile, lines)
	if err != nil {
		return CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("❌ Failed to read log file: %v", err),
		}
	}
	
	exportDir := filepath.Join(os.TempDir(), "aevitas-logs")
	if err := os.MkdirAll(exportDir, 0755); err != nil {
		return CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("❌ Failed to prepare log export: %v", err),
		}
	}
	exportPath := filepath.Join(exportDir, fmt.Sprintf("gateway-logs-last-%d-%d.txt", lines, time.Now().UnixNano()))
	if err := os.WriteFile(exportPath, []byte(content+"\n"), 0644); err != nil {
		return CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("❌ Failed to write log export: %v", err),
		}
	}
	
	return CommandResult{
		Handled:  true,
		Files:    []string{exportPath},
	}
}

// readLastLines reads the last n lines from a file
func readLastLines(filePath string, n int) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	
	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return "", err
	}
	fileSize := stat.Size()
	
	// Read file in chunks from the end
	const bufSize = 4096
	var lines []string
	var buffer []byte
	
	for offset := fileSize; offset > 0 && len(lines) < n; {
		// Calculate chunk size
		chunkSize := int64(bufSize)
		if offset < chunkSize {
			chunkSize = offset
		}
		
		// Seek to position
		offset -= chunkSize
		_, err := file.Seek(offset, 0)
		if err != nil {
			return "", err
		}
		
		// Read chunk
		chunk := make([]byte, chunkSize)
		_, err = file.Read(chunk)
		if err != nil {
			return "", err
		}
		
		// Prepend to buffer
		buffer = append(chunk, buffer...)
		
		// Split into lines
		text := string(buffer)
		allLines := strings.Split(text, "\n")
		
		// If we're not at the beginning of the file, keep incomplete first line in buffer
		if offset > 0 && len(allLines) > 0 {
			buffer = []byte(allLines[0])
			allLines = allLines[1:]
		} else {
			buffer = nil
		}
		
		// Prepend lines
		lines = append(allLines, lines...)
	}
	
	// Get last n lines
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	
	// Remove empty trailing line
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	
	return strings.Join(lines, "\n"), nil
}

func (h *CommandHandler) handleStatus() string {
	pidFile := filepath.Join(os.Getenv("HOME"), ".aevitas", "aevitas.pid")
	
	// Check if PID file exists
	if _, err := os.Stat(pidFile); os.IsNotExist(err) {
		// Maybe running in foreground, show current process PID
		currentPID := os.Getpid()
		return fmt.Sprintf("🟡 **Gateway Status: Running (Foreground)**\n\nCurrent PID: %d\n\nNo PID file found - gateway may be running in foreground mode.", currentPID)
	}
	
	// Read PID
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Sprintf("⚠️ Failed to read PID file: %v", err)
	}
	
	pid := strings.TrimSpace(string(pidBytes))
	
	// Check if process is running
	cmd := exec.Command("ps", "-p", pid, "-o", "pid,etime,command")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("🔴 **Gateway Status: Not Running**\n\nStale PID file found (PID: %s)\n\nUse `make start` or `/restart` to start.", pid)
	}
	
	// Parse ps output to get uptime
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var uptime string
	if len(lines) > 1 {
		fields := strings.Fields(lines[1])
		if len(fields) >= 2 {
			uptime = fields[1] // ELAPSED time
		}
	}
	
	statusMsg := fmt.Sprintf("🟢 **Gateway Status: Running (Background)**\n\nPID: %s", pid)
	if uptime != "" {
		statusMsg += fmt.Sprintf("\nUptime: %s", uptime)
	}
	
	return statusMsg
}

// handleCleanupScan scans for temporary files and shows statistics.
func (h *CommandHandler) handleCleanupScan(chatID string) CommandResult {
	type cleanupRecord struct {
		scope string
		path  string
	}

	// Scan project temp files in common temp directories + workspace TTS cache + workspace var.
	var tempFiles []string
	var records []cleanupRecord
	var totalSize int64
	var tempCount, ttsCount, varCount int
	var tempSize, ttsSize, varSize int64

	seen := map[string]struct{}{}
	tempRoot := filepath.Clean(os.TempDir())

	// Check OS temp dir and common temp dirs.
	tempDirs := []string{tempRoot, "/tmp", "/var/tmp", "/private/var/tmp"}
	for _, dir := range tempDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		// Keep the pattern narrow to avoid accidental deletion of unrelated temp files.
		patterns := []string{
			filepath.Join(dir, "screenshot-*.png"),
			filepath.Join(dir, "aevitas-*"),
			filepath.Join(dir, "agentsdk-*"),
		}
		for _, pattern := range patterns {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}

			for _, file := range matches {
				if _, ok := seen[file]; ok {
					continue
				}
				info, err := os.Stat(file)
				if err != nil || info.IsDir() {
					continue
				}
				seen[file] = struct{}{}
				tempFiles = append(tempFiles, file)
				totalSize += info.Size()
				records = append(records, cleanupRecord{scope: "temp", path: file})
				tempCount++
				tempSize += info.Size()
			}
		}
	}

	// Also scan nested files under TMPDIR, because macOS often stores temp payloads
	// under subdirectories in /var/folders/.../T.
	_ = filepath.Walk(tempRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		name := strings.ToLower(info.Name())
		if !(strings.HasPrefix(name, "screenshot-") || strings.HasPrefix(name, "aevitas-") || strings.HasPrefix(name, "agentsdk-")) {
			return nil
		}
		if _, ok := seen[path]; ok {
			return nil
		}
		seen[path] = struct{}{}
		tempFiles = append(tempFiles, path)
		totalSize += info.Size()
		records = append(records, cleanupRecord{scope: "temp", path: path})
		tempCount++
		tempSize += info.Size()
		return nil
	})

	// Include workspace TTS cache files.
	ttsDir := ""
	if h.workspace != "" {
		ttsDir = filepath.Join(h.workspace, ".claude", "voice", "tts")
	} else {
		ttsDir = filepath.Join(os.Getenv("HOME"), ".aevitas", "workspace", ".claude", "voice", "tts")
	}

	if info, err := os.Stat(ttsDir); err == nil && info.IsDir() {
		_ = filepath.Walk(ttsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if _, ok := seen[path]; ok {
				return nil
			}
			seen[path] = struct{}{}
			tempFiles = append(tempFiles, path)
			totalSize += info.Size()
			records = append(records, cleanupRecord{scope: "tts", path: path})
			ttsCount++
			ttsSize += info.Size()
			return nil
		})
	}

	// Include workspace var artifacts (all files under var/) for deterministic cleanup.
	varDir := ""
	if h.workspace != "" {
		varDir = filepath.Join(h.workspace, "var")
	} else {
		varDir = filepath.Join(os.Getenv("HOME"), ".aevitas", "workspace", "var")
	}
	if info, err := os.Stat(varDir); err == nil && info.IsDir() {
		_ = filepath.Walk(varDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if _, ok := seen[path]; ok {
				return nil
			}
			seen[path] = struct{}{}
			tempFiles = append(tempFiles, path)
			totalSize += info.Size()
			records = append(records, cleanupRecord{scope: "var", path: path})
			varCount++
			varSize += info.Size()
			return nil
		})
	}

	if len(tempFiles) == 0 {
		return CommandResult{
			Handled:  true,
			Response: "✨ **No Temporary Files Found**\n\nYour system is clean!",
		}
	}
	
	// Get oldest and newest file times
	var oldestTime, newestTime int64
	for _, file := range tempFiles {
		info, _ := os.Stat(file)
		modTime := info.ModTime().Unix()
		if oldestTime == 0 || modTime < oldestTime {
			oldestTime = modTime
		}
		if modTime > newestTime {
			newestTime = modTime
		}
	}
	
	// Save pending cleanup list to temp file
	cleanupFile := filepath.Join(os.TempDir(), fmt.Sprintf("cleanup_%s.txt", chatID))
	var lines []string
	for _, r := range records {
		lines = append(lines, r.scope+"\t"+r.path)
	}
	data := strings.Join(lines, "\n")
	if err := os.WriteFile(cleanupFile, []byte(data), 0644); err != nil {
		return CommandResult{
			Handled:  true,
			Response: fmt.Sprintf("❌ Failed to save cleanup list: %v", err),
		}
	}
	
	// Format response
	response := "🗑️ **Temporary Files Found**\n\n"
	response += "📊 Statistics:\n"
	response += fmt.Sprintf("• Files: %d file(s)\n", len(tempFiles))
	response += fmt.Sprintf("• Total Size: %.2f MB\n", float64(totalSize)/(1024*1024))
	response += fmt.Sprintf("• temp: %d file(s), %.2f MB\n", tempCount, float64(tempSize)/(1024*1024))
	response += fmt.Sprintf("• tts: %d file(s), %.2f MB\n", ttsCount, float64(ttsSize)/(1024*1024))
	response += fmt.Sprintf("• var: %d file(s), %.2f MB\n", varCount, float64(varSize)/(1024*1024))
	response += fmt.Sprintf("• Oldest: %s\n", utils.FormatRelativeTime(oldestTime))
	response += fmt.Sprintf("• Newest: %s\n\n", utils.FormatRelativeTime(newestTime))
	response += "⚠️ **Warning**: This action cannot be undone!\n\n"
	response += "Reply with one option:\n"
	response += "• `/cleanup confirm all` (or `/cleanup confirm` / `/cleanup yes`)\n"
	response += "• `/cleanup confirm temp`\n"
	response += "• `/cleanup confirm tts`\n"
	response += "• `/cleanup confirm var`"
	
	return CommandResult{
		Handled:  true,
		Response: response,
	}
}

// handleCleanupConfirm deletes pending cleanup files by scope.
func (h *CommandHandler) handleCleanupConfirm(chatID, scope string) CommandResult {
	allowedScopes := map[string]bool{
		"all":  true,
		"temp": true,
		"tts":  true,
		"var":  true,
		"":     true,
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "all"
	}
	if !allowedScopes[scope] {
		return CommandResult{
			Handled:  true,
			Response: "⚠️ Invalid cleanup scope. Use one of: `all`, `temp`, `tts`, `var`.",
		}
	}

	// Load pending cleanup list
	cleanupFile := filepath.Join(os.TempDir(), fmt.Sprintf("cleanup_%s.txt", chatID))
	data, err := os.ReadFile(cleanupFile)
	if err != nil {
		return CommandResult{
			Handled:  true,
			Response: "⚠️ No pending cleanup request found or it has expired.\n\nUse `/cleanup` to scan for temporary files first.",
		}
	}
	
	rawLines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rawLines) == 0 {
		return CommandResult{
			Handled:  true,
			Response: "⚠️ No files to clean.",
		}
	}

	type cleanupEntry struct {
		scope string
		path  string
	}
	var entries []cleanupEntry
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			entries = append(entries, cleanupEntry{scope: strings.TrimSpace(parts[0]), path: strings.TrimSpace(parts[1])})
			continue
		}
		// Backward compatibility with older plain-path format.
		entries = append(entries, cleanupEntry{scope: "temp", path: line})
	}
	if len(entries) == 0 {
		return CommandResult{
			Handled:  true,
			Response: "⚠️ No files to clean.",
		}
	}
	
	// Delete files
	deletedCount := 0
	var failedFiles []string
	
	for _, entry := range entries {
		if entry.path == "" {
			continue
		}
		if scope != "all" && scope != entry.scope {
			continue
		}
		if err := os.Remove(entry.path); err != nil {
			failedFiles = append(failedFiles, filepath.Base(entry.path))
		} else {
			deletedCount++
		}
	}
	
	// Remove cleanup list file
	os.Remove(cleanupFile)
	
	// Format response
	response := fmt.Sprintf("✅ **Cleanup Complete**\n\nScope: %s\nDeleted %d file(s)", scope, deletedCount)
	if len(failedFiles) > 0 {
		response += fmt.Sprintf("\n\n⚠️ Failed to delete %d file(s):\n%s", len(failedFiles), strings.Join(failedFiles, ", "))
	}
	
	return CommandResult{
		Handled:  true,
		Response: response,
	}
}

