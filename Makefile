.PHONY: serve watch build build-linux build-darwin-arm64 build-darwin-amd64 build-windows \
        build-all test test-cover bench lint fix tidy update-deps clean \
        docker-build docker-run install install-darwin install-linux uninstall

BIN_NAME := go-system-info
SRC_DIR  := .
DIST_DIR := dist
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
CGO      := CGO_ENABLED=0
PORT     := 8223
REFRESH  := 3s
IMAGE    := go-system-info

# Installation en tant que service (cf. README, section « Lancer en tant que service »).
OS          := $(shell uname -s)
# PREFIX par défaut selon l'OS : macOS installe un LaunchAgent **utilisateur**
# (sans sudo) → préfixe inscriptible dans le HOME ($HOME/.local, déjà dans le
# PATH usuel) ; Linux installe un service systemd **système** (avec sudo) →
# /usr/local. Surchargeable dans les deux cas (ex. make install PREFIX=/opt).
ifeq ($(OS),Darwin)
PREFIX      ?= $(HOME)/.local
else
PREFIX      ?= /usr/local
endif
BINDIR      := $(PREFIX)/bin
INSTALL_BIN := $(BINDIR)/$(BIN_NAME)
LABEL       ?= com.fabien.go-system-info
PLIST_PATH  := $(HOME)/Library/LaunchAgents/$(LABEL).plist
UNIT_PATH   := /etc/systemd/system/$(BIN_NAME).service

serve:
	go run $(SRC_DIR) -p $(PORT) -r $(REFRESH)

watch:
	watchexec -r go run $(SRC_DIR) -p $(PORT) -r $(REFRESH)

build-linux:
	GOOS=linux GOARCH=amd64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o "$(DIST_DIR)/$(BIN_NAME)-linux-amd64" $(SRC_DIR)

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o "$(DIST_DIR)/$(BIN_NAME)-darwin-arm64" $(SRC_DIR)

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o "$(DIST_DIR)/$(BIN_NAME)-darwin-amd64" $(SRC_DIR)

build-windows:
	GOOS=windows GOARCH=amd64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o "$(DIST_DIR)/$(BIN_NAME)-windows-amd64.exe" $(SRC_DIR)

build-all: build-linux build-darwin-arm64 build-darwin-amd64 build-windows

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-run: docker-build
	docker run --rm -p $(PORT):$(PORT) $(IMAGE):latest -p $(PORT) -r $(REFRESH)

test:
	go test ./... -race

test-cover:
	go test -coverprofile=cover.out ./... && go tool cover -func=cover.out

bench:
	go test -bench=. -benchmem -run=^$$ ./...

lint:
	go fmt ./...
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint absent : installez-le pour reproduire la CI (https://golangci-lint.run/)."; \
	fi

fix:
	go fix ./...

tidy:
	go mod tidy

update-deps:
	go get -u ./...
	go mod tidy
	go mod verify

clean:
	rm -rf $(DIST_DIR) cover.out

# Compile un binaire natif (pour la machine courante) dans dist/.
build:
	$(CGO) go build -ldflags="$(LDFLAGS)" -o "$(DIST_DIR)/$(BIN_NAME)" $(SRC_DIR)

# Installe le binaire + le fichier de service du système hôte, puis l'active.
# - macOS : LaunchAgent utilisateur (pas de sudo) ; binaire dans $HOME/.local/bin
#   par défaut. Pilote vos propres processus.
# - Linux : service systemd (nécessite « sudo make install ») ; binaire dans
#   /usr/local/bin. Tourne sous l'utilisateur appelant ($SUDO_USER) pour que la
#   terminaison reste utile.
# Surchageable : PREFIX, PORT, REFRESH, LABEL.
install:
ifeq ($(OS),Darwin)
	@$(MAKE) install-darwin
else ifeq ($(OS),Linux)
	@$(MAKE) install-linux
else
	@echo "make install : non pris en charge sur $(OS) (voir le README pour Windows)."; exit 1
endif

install-darwin: build
	@if [ "$$(id -u)" -eq 0 ]; then echo "Sur macOS, lancez « make install » SANS sudo : le LaunchAgent est installé pour votre session (gui/\$$(id -u))."; exit 1; fi
	install -d "$(BINDIR)"
	install -m 0755 "$(DIST_DIR)/$(BIN_NAME)" "$(INSTALL_BIN)"
	@mkdir -p "$(HOME)/Library/LaunchAgents"
	printf '%s\n' \
	  '<?xml version="1.0" encoding="UTF-8"?>' \
	  '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
	  '<plist version="1.0">' \
	  '<dict>' \
	  '    <key>Label</key>' \
	  '    <string>$(LABEL)</string>' \
	  '    <key>ProgramArguments</key>' \
	  '    <array>' \
	  '        <string>$(INSTALL_BIN)</string>' \
	  '        <string>-p</string>' \
	  '        <string>$(PORT)</string>' \
	  '        <string>-r</string>' \
	  '        <string>$(REFRESH)</string>' \
	  '    </array>' \
	  '    <key>RunAtLoad</key>' \
	  '    <true/>' \
	  '    <key>KeepAlive</key>' \
	  '    <true/>' \
	  '    <key>StandardOutPath</key>' \
	  '    <string>/tmp/$(BIN_NAME).log</string>' \
	  '    <key>StandardErrorPath</key>' \
	  '    <string>/tmp/$(BIN_NAME).err.log</string>' \
	  '</dict>' \
	  '</plist>' \
	  > "$(PLIST_PATH)"
	-launchctl bootout gui/$$(id -u) "$(PLIST_PATH)" 2>/dev/null
	launchctl bootstrap gui/$$(id -u) "$(PLIST_PATH)"
	launchctl enable gui/$$(id -u)/$(LABEL)
	@echo "LaunchAgent installé : $(PLIST_PATH) (binaire : $(INSTALL_BIN))"

install-linux: build
	@if [ "$$(id -u)" -ne 0 ]; then echo "Lancez : sudo make install"; exit 1; fi
	install -d "$(BINDIR)"
	install -m 0755 "$(DIST_DIR)/$(BIN_NAME)" "$(INSTALL_BIN)"
	@RUN_USER="$${SUDO_USER:-root}"; \
	printf '%s\n' \
	  '[Unit]' \
	  'Description=go-system-info — métriques système (web/API)' \
	  'After=network.target' \
	  '' \
	  '[Service]' \
	  'Type=simple' \
	  'ExecStart=$(INSTALL_BIN) -p $(PORT) -r $(REFRESH)' \
	  'Restart=on-failure' \
	  'RestartSec=5' \
	  "User=$$RUN_USER" \
	  'KillSignal=SIGTERM' \
	  'TimeoutStopSec=15' \
	  'NoNewPrivileges=true' \
	  '' \
	  '[Install]' \
	  'WantedBy=multi-user.target' \
	  > "$(UNIT_PATH)"
	systemctl daemon-reload
	systemctl enable --now $(BIN_NAME).service
	@echo "Service systemd installé : $(UNIT_PATH) (binaire : $(INSTALL_BIN))"

# Arrête, désactive et supprime le service + le binaire installés par « install ».
uninstall:
ifeq ($(OS),Darwin)
	-launchctl bootout gui/$$(id -u) "$(PLIST_PATH)" 2>/dev/null
	rm -f "$(PLIST_PATH)" "$(INSTALL_BIN)"
	@echo "LaunchAgent et binaire supprimés."
else ifeq ($(OS),Linux)
	@if [ "$$(id -u)" -ne 0 ]; then echo "Lancez : sudo make uninstall"; exit 1; fi
	-systemctl disable --now $(BIN_NAME).service
	rm -f "$(UNIT_PATH)" "$(INSTALL_BIN)"
	systemctl daemon-reload
	@echo "Service systemd et binaire supprimés."
else
	@echo "make uninstall : non pris en charge sur $(OS)."; exit 1
endif
