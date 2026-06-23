.PHONY: serve watch build-linux build-darwin-arm64 build-darwin-amd64 build-windows \
        build-all test test-cover bench lint fix tidy update-deps clean \
        docker-build docker-run

BIN_NAME := go-system-info
SRC_DIR  := .
DIST_DIR := dist
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
CGO      := CGO_ENABLED=0
PORT     := 8222
REFRESH  := 3s
IMAGE    := go-system-info

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
