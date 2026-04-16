APP_NAME := shipyard
FAIRWAY_NAME := shipyard-fairway
DIST_DIR := dist
VERSION ?= $(shell cat VERSION)
COMMIT ?= dev
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X github.com/shipyard-auto/shipyard/internal/app.Version=$(VERSION) -X github.com/shipyard-auto/shipyard/internal/app.Commit=$(COMMIT) -X github.com/shipyard-auto/shipyard/internal/app.BuildDate=$(BUILD_DATE)
FAIRWAY_LDFLAGS := -X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.Version=$(VERSION) -X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.Commit=$(COMMIT) -X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.BuildDate=$(BUILD_DATE)

.PHONY: build build-fairway test fmt tidy clean dist package

build:
	mkdir -p $(DIST_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME) ./cmd/shipyard

build-fairway:
	mkdir -p $(DIST_DIR)
	go build -ldflags "$(FAIRWAY_LDFLAGS)" -o $(DIST_DIR)/$(FAIRWAY_NAME) ./addons/fairway/cmd/...

test:
	go test ./...

fmt:
	gofmt -w cmd internal

tidy:
	go mod tidy

clean:
	rm -rf $(DIST_DIR)

dist: clean
	mkdir -p $(DIST_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/linux-amd64/$(APP_NAME) ./cmd/shipyard
	mkdir -p $(DIST_DIR)/linux-arm64
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/linux-arm64/$(APP_NAME) ./cmd/shipyard
	mkdir -p $(DIST_DIR)/darwin-amd64
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/darwin-amd64/$(APP_NAME) ./cmd/shipyard
	mkdir -p $(DIST_DIR)/darwin-arm64
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/darwin-arm64/$(APP_NAME) ./cmd/shipyard

package: dist
	tar -C $(DIST_DIR)/linux-amd64 -czf $(DIST_DIR)/shipyard_$(VERSION)_linux_amd64.tar.gz $(APP_NAME)
	tar -C $(DIST_DIR)/linux-arm64 -czf $(DIST_DIR)/shipyard_$(VERSION)_linux_arm64.tar.gz $(APP_NAME)
	tar -C $(DIST_DIR)/darwin-amd64 -czf $(DIST_DIR)/shipyard_$(VERSION)_darwin_amd64.tar.gz $(APP_NAME)
	tar -C $(DIST_DIR)/darwin-arm64 -czf $(DIST_DIR)/shipyard_$(VERSION)_darwin_arm64.tar.gz $(APP_NAME)
