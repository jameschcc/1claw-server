# 1Claw Server — Build & Run
# GOPROXY=https://goproxy.cn,direct for China users
# GONOSUMCHECK=* skips checksum validation (goproxy.cn doesn't support it)

GO ?= go
GOPROXY ?= https://goproxy.cn,direct
GONOSUMCHECK ?= *
GONOSUMDB ?= *
BINARY ?= 1claw-server

.PHONY: all build run clean test tidy

all: build

tidy:
	$(GO) mod tidy

build: tidy
	GOPROXY=$(GOPROXY) GONOSUMCHECK=$(GONOSUMCHECK) GONOSUMDB=$(GONOSUMDB) $(GO) build -o $(BINARY) .

run: build
	./$(BINARY) -config=config.yaml

test:
	GOPROXY=$(GOPROXY) GONOSUMCHECK=$(GONOSUMCHECK) GONOSUMDB=$(GONOSUMDB) $(GO) test ./...

clean:
	rm -f $(BINARY)
	rm -rf /tmp/$(BINARY)

docker:
	docker build -t 1claw-server .
	docker run -p 8080:8080 1claw-server
