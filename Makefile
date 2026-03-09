.PHONY: build run gateway test setup clean docker-up docker-down lint prod start stop restart install-ffmpeg plugin-list plugin-install-feishu plugin-remove plugin-doctor plugin-run plugin-stop plugin-ps plugin-start-enabled plugin-stop-all

BINARY    := aevitas
BUILD_DIR := .
CONFIG    := $(HOME)/.aevitas/config.json
INSTALL_DIR := $(HOME)/.aevitas/bin
SCRIPT_DIR := scripts

## Build
build:
	go build -o $(BINARY) ./cmd/aevitas

## Run agent REPL
run: build
	./$(BINARY) agent

## Run gateway (channels + cron + heartbeat)
gateway: build
	./$(BINARY) gateway

## Run onboard to initialize config and workspace
## Initialize/reset workspace files
onboard: build
	./$(BINARY) onboard

## Show status
status: build
	./$(BINARY) status

## List installed runtime plugins
plugin-list: build
	./$(BINARY) plugin list

## Install feishu official plugin package to ~/.aevitas/plugins
plugin-install-feishu: build
	./$(BINARY) plugin install feishuOfficial

## Remove runtime plugin (usage: make plugin-remove PLUGIN=feishuOfficial)
plugin-remove: build
	@if [ -z "$(PLUGIN)" ]; then \
		echo "Usage: make plugin-remove PLUGIN=<plugin-id>"; \
		exit 1; \
	fi
	./$(BINARY) plugin remove "$(PLUGIN)"

## Diagnose plugin install (usage: make plugin-doctor [PLUGIN=feishuOfficial])
plugin-doctor: build
	@if [ -z "$(PLUGIN)" ]; then \
		./$(BINARY) plugin doctor; \
	else \
		./$(BINARY) plugin doctor "$(PLUGIN)"; \
	fi

## Run plugin runtime dynamically from plugin runtime.json (usage: make plugin-run PLUGIN=feishuOfficial)
plugin-run: build
	@if [ -z "$(PLUGIN)" ]; then \
		echo "Usage: make plugin-run PLUGIN=<plugin-id>"; \
		exit 1; \
	fi
	./$(BINARY) plugin run "$(PLUGIN)"

## Stop plugin runtime (usage: make plugin-stop PLUGIN=feishuOfficial)
plugin-stop: build
	@if [ -z "$(PLUGIN)" ]; then \
		echo "Usage: make plugin-stop PLUGIN=<plugin-id>"; \
		exit 1; \
	fi
	./$(BINARY) plugin stop "$(PLUGIN)"

## Show plugin runtime status
plugin-ps: build
	./$(BINARY) plugin ps

## Start all enabled plugins from registry
plugin-start-enabled: build
	./$(BINARY) plugin start-enabled

## Stop all running plugin runtimes
plugin-stop-all: build
	./$(BINARY) plugin stop-all

## List installed skills
skills-list: build
	./$(BINARY) skills list

## Install or update skills (usage: make skills-install [skill-name])
skills-install: build
	@SKILL="$(filter-out $@,$(MAKECMDGOALS))"; \
	if [ -z "$$SKILL" ]; then \
		read -p "Install all skills? [y/N] " -n 1 -r; echo; \
		if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
			./$(BINARY) skills install; \
		fi \
	else \
		read -p "Install skill '$$SKILL'? [y/N] " -n 1 -r; echo; \
		if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
			./$(BINARY) skills install $$SKILL; \
		fi \
	fi

## Update skills (usage: make skills-update [skill-name])
skills-update: build
	@SKILL="$(filter-out $@,$(MAKECMDGOALS))"; \
	if [ -z "$$SKILL" ]; then \
		read -p "Update all skills? [y/N] " -n 1 -r; echo; \
		if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
			./$(BINARY) skills update; \
		fi \
	else \
		read -p "Update skill '$$SKILL'? [y/N] " -n 1 -r; echo; \
		if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
			./$(BINARY) skills update $$SKILL; \
		fi \
	fi

## Uninstall a skill (usage: make skills-uninstall <skill-name>)
skills-uninstall: build
	@SKILL="$(filter-out $@,$(MAKECMDGOALS))"; \
	if [ -z "$$SKILL" ]; then \
		echo "Usage: make skills-uninstall <skill-name>"; \
		exit 1; \
	fi; \
	read -p "Uninstall skill '$$SKILL'? [y/N] " -n 1 -r; echo; \
	if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
		./$(BINARY) skills uninstall $$SKILL; \
	fi

# Prevent make from treating skill names as targets
%:
	@:

## Verify skills integrity
skills-verify: build
	./$(BINARY) skills verify

## Build and install to production directory
prod: install-ffmpeg
	@echo "Tidying dependencies..."
	@go mod tidy
	@$(MAKE) build
	@echo "Installing aevitas to $(INSTALL_DIR)..."
	@mkdir -p $(INSTALL_DIR)
	@cp $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@mkdir -p "$(HOME)/.aevitas/scripts"
	@cp scripts/start.sh "$(HOME)/.aevitas/scripts/start.sh"
	@cp scripts/stop.sh "$(HOME)/.aevitas/scripts/stop.sh"
	@cp scripts/restart.sh "$(HOME)/.aevitas/scripts/restart.sh"
	@chmod +x "$(HOME)/.aevitas/scripts/start.sh" "$(HOME)/.aevitas/scripts/stop.sh" "$(HOME)/.aevitas/scripts/restart.sh"
	@rm -rf "$(INSTALL_DIR)/shim"
	@cp -R shim "$(INSTALL_DIR)/shim"
	@echo "✓ aevitas installed to $(INSTALL_DIR)/$(BINARY)"
	@echo "Use 'make start' or 'scripts/start.sh' to start in background"

## Download local ffmpeg/ffprobe binaries for production
install-ffmpeg:
	@bash $(SCRIPT_DIR)/install_ffmpeg.sh "$(INSTALL_DIR)"

## Start gateway in background (production mode)
start:
	@bash $(SCRIPT_DIR)/start.sh

## Stop gateway gracefully
stop:
	@bash $(SCRIPT_DIR)/stop.sh

## Restart gateway
restart:
	@bash $(SCRIPT_DIR)/restart.sh

## Interactive setup: generate config.json
setup:
	@bash scripts/setup.sh

## Run all tests
test:
	go test ./... -count=1

## Run tests with race detection
test-race:
	go test -race ./... -count=1

## Run tests with coverage
test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

## Docker: build and start
docker-up:
	docker compose up -d --build

## Docker: stop
docker-down:
	docker compose down

## Clean build artifacts
clean:
	rm -f $(BINARY) coverage.out

## Lint (requires golangci-lint)
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "Install: brew install golangci-lint"; exit 1; }
	golangci-lint run ./...

## Help
help:
	@echo "aevitas Makefile targets:"
	@echo ""
	@echo "Core:"
	@echo "  build            Build binary"
	@echo "  run              Run agent REPL"
	@echo "  gateway          Start gateway (channels + cron + heartbeat)"
	@echo "  onboard          Initialize config and workspace"
	@echo "  status           Show aevitas status"
	@echo "  plugin-list      List installed runtime plugins"
	@echo "  plugin-install-feishu Install official Feishu plugin package"
	@echo "  plugin-remove PLUGIN=<id> Remove plugin"
	@echo "  plugin-doctor [PLUGIN=<id>] Check plugin installation"
	@echo "  plugin-run PLUGIN=<id> Start plugin runtime from descriptor"
	@echo "  plugin-stop PLUGIN=<id> Stop plugin runtime"
	@echo "  plugin-ps       Show plugin runtime status"
	@echo "  plugin-start-enabled Start enabled plugin runtimes"
	@echo "  plugin-stop-all Stop all plugin runtimes"
	@echo "  setup            Interactive config setup"
	@echo "  prod             Build + install + download ffmpeg/ffprobe"
	@echo ""
	@echo "Production Control:"
	@echo "  start            Start gateway in background"
	@echo "  stop             Stop gateway gracefully"
	@echo "  restart          Restart gateway"
	@echo ""
	@echo "Skills Management:"
	@echo "  skills-list         List installed skills"
	@echo "  skills-install [name] Install skill(s) (name or all)"
	@echo "  skills-update [name]  Update skill(s) (name or all)"
	@echo "  skills-uninstall <name> Uninstall a skill (required)"
	@echo "  skills-verify       Verify skills integrity"
	@echo ""
	@echo "Testing:"
	@echo "  test             Run all tests"
	@echo "  test-race        Run tests with race detection"
	@echo "  test-cover       Run tests with coverage report"
	@echo ""
	@echo "Deployment:"
	@echo "  docker-up        Docker build and start"
	@echo "  docker-down      Docker stop"
	@echo ""
	@echo "Production Scripts (or use make commands above):"
	@echo "  ./scripts/start.sh   Start gateway in background"
	@echo "  ./scripts/stop.sh    Stop gateway gracefully"
	@echo "  ./scripts/restart.sh Restart gateway"
	@echo ""
	@echo "Utilities:"
	@echo "  clean            Remove build artifacts"
	@echo "  lint             Run golangci-lint"
