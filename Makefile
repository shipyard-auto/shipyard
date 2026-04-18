APP_NAME         := shipyard
FAIRWAY_APP_NAME := shipyard-fairway
FAIRWAY_NAME     := $(FAIRWAY_APP_NAME)
DIST_DIR         := dist

SHIPYARD_VERSION ?= $(shell grep '^shipyard=' manifest | cut -d= -f2)
FAIRWAY_VERSION  ?= $(shell grep '^fairway=' manifest | cut -d= -f2)

COMMIT     ?= dev
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := \
  -X github.com/shipyard-auto/shipyard/internal/app.Version=$(SHIPYARD_VERSION) \
  -X github.com/shipyard-auto/shipyard/internal/app.Commit=$(COMMIT) \
  -X github.com/shipyard-auto/shipyard/internal/app.BuildDate=$(BUILD_DATE)

FAIRWAY_LDFLAGS := \
  -X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.Version=$(FAIRWAY_VERSION) \
  -X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.Commit=$(COMMIT) \
  -X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.BuildDate=$(BUILD_DATE)

.PHONY: build test fmt tidy clean dist package \
        build-fairway dist-fairway package-fairway checksums-fairway \
        build-all dist-all package-all

# ── core ─────────────────────────────────────────────────────────────────────

build:
	mkdir -p $(DIST_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME) ./cmd/shipyard

test:
	go test ./...

fmt:
	gofmt -w cmd internal addons

tidy:
	go mod tidy

clean:
	rm -rf $(DIST_DIR)

dist: clean
	mkdir -p $(DIST_DIR)/linux-amd64
	GOOS=linux  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/linux-amd64/$(APP_NAME)  ./cmd/shipyard
	mkdir -p $(DIST_DIR)/linux-arm64
	GOOS=linux  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/linux-arm64/$(APP_NAME)  ./cmd/shipyard
	mkdir -p $(DIST_DIR)/darwin-amd64
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/darwin-amd64/$(APP_NAME) ./cmd/shipyard
	mkdir -p $(DIST_DIR)/darwin-arm64
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/darwin-arm64/$(APP_NAME) ./cmd/shipyard

package: dist
	tar -C $(DIST_DIR)/linux-amd64  -czf $(DIST_DIR)/$(APP_NAME)_$(SHIPYARD_VERSION)_linux_amd64.tar.gz  $(APP_NAME)
	tar -C $(DIST_DIR)/linux-arm64  -czf $(DIST_DIR)/$(APP_NAME)_$(SHIPYARD_VERSION)_linux_arm64.tar.gz  $(APP_NAME)
	tar -C $(DIST_DIR)/darwin-amd64 -czf $(DIST_DIR)/$(APP_NAME)_$(SHIPYARD_VERSION)_darwin_amd64.tar.gz $(APP_NAME)
	tar -C $(DIST_DIR)/darwin-arm64 -czf $(DIST_DIR)/$(APP_NAME)_$(SHIPYARD_VERSION)_darwin_arm64.tar.gz $(APP_NAME)

# ── fairway ──────────────────────────────────────────────────────────────────

build-fairway:
	mkdir -p $(DIST_DIR)
	go build -ldflags "$(FAIRWAY_LDFLAGS)" -o $(DIST_DIR)/$(FAIRWAY_APP_NAME) ./addons/fairway/cmd

dist-fairway:
	mkdir -p $(DIST_DIR)/fairway-linux-amd64
	GOOS=linux  GOARCH=amd64 go build -ldflags "$(FAIRWAY_LDFLAGS)" -o $(DIST_DIR)/fairway-linux-amd64/$(FAIRWAY_APP_NAME)  ./addons/fairway/cmd
	mkdir -p $(DIST_DIR)/fairway-linux-arm64
	GOOS=linux  GOARCH=arm64 go build -ldflags "$(FAIRWAY_LDFLAGS)" -o $(DIST_DIR)/fairway-linux-arm64/$(FAIRWAY_APP_NAME)  ./addons/fairway/cmd
	mkdir -p $(DIST_DIR)/fairway-darwin-amd64
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(FAIRWAY_LDFLAGS)" -o $(DIST_DIR)/fairway-darwin-amd64/$(FAIRWAY_APP_NAME) ./addons/fairway/cmd
	mkdir -p $(DIST_DIR)/fairway-darwin-arm64
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(FAIRWAY_LDFLAGS)" -o $(DIST_DIR)/fairway-darwin-arm64/$(FAIRWAY_APP_NAME) ./addons/fairway/cmd

package-fairway: dist-fairway
	tar -C $(DIST_DIR)/fairway-linux-amd64  -czf $(DIST_DIR)/$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_linux_amd64.tar.gz  $(FAIRWAY_APP_NAME)
	tar -C $(DIST_DIR)/fairway-linux-arm64  -czf $(DIST_DIR)/$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_linux_arm64.tar.gz  $(FAIRWAY_APP_NAME)
	tar -C $(DIST_DIR)/fairway-darwin-amd64 -czf $(DIST_DIR)/$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_darwin_amd64.tar.gz $(FAIRWAY_APP_NAME)
	tar -C $(DIST_DIR)/fairway-darwin-arm64 -czf $(DIST_DIR)/$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_darwin_arm64.tar.gz $(FAIRWAY_APP_NAME)

checksums-fairway: package-fairway
	cd $(DIST_DIR) && shasum -a 256 \
		$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_linux_amd64.tar.gz \
		$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_linux_arm64.tar.gz \
		$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_darwin_amd64.tar.gz \
		$(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_darwin_arm64.tar.gz \
		> $(FAIRWAY_APP_NAME)_$(FAIRWAY_VERSION)_checksums.txt

# ── combined ─────────────────────────────────────────────────────────────────

build-all: build build-fairway
dist-all:  dist dist-fairway
package-all: package package-fairway checksums-fairway
