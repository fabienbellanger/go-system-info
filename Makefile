.PHONY: serve build-linux build-darwin-arm64 build-darwin-amd64 build-windows \
        build-all test test-cover lint fix tidy update-deps clean

BIN_NAME := Go System Info
SRC_DIR  := .
DIST_DIR := dist
LDFLAGS  := -s -w
CGO      := CGO_ENABLED=0
PORT     := 8080
REFRESH  := 3s

serve:
	go run $(SRC_DIR) -p $(PORT) -r $(REFRESH)

watch:
	watchexec -r go run $(SRC_DIR) -p $(PORT) -r $(REFRESH)

build-linux:
	GOOS=linux GOARCH=amd64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o $(DIST_DIR)/$(BIN_NAME)-linux-amd64 $(SRC_DIR)

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o $(DIST_DIR)/$(BIN_NAME)-darwin-arm64 $(SRC_DIR)

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o $(DIST_DIR)/$(BIN_NAME)-darwin-amd64 $(SRC_DIR)

build-windows:
	GOOS=windows GOARCH=amd64 $(CGO) go build -ldflags="$(LDFLAGS)" \
		-o $(DIST_DIR)/$(BIN_NAME)-windows-amd64.exe $(SRC_DIR)

build-all: build-linux build-darwin-arm64 build-darwin-amd64 build-windows

test:
	go test ./... -race

test-cover:
	go test -coverprofile=cover.out ./... && go tool cover -func=cover.out

lint:
	go fmt ./...
	go vet ./...

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
