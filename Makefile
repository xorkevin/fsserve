.PHONY: all install

all: install

install:
	go install .

.PHONY: test coverage cover bench fmt vet prepare

COVERAGE=cover.out
COVERAGE_ARGS=-covermode count -coverprofile $(COVERAGE)

test:
	go test -cover $(COVERAGE_ARGS) ./...

coverage:
	go tool cover -html $(COVERAGE)

cover: test coverage

BENCHMARK_ARGS=-benchtime 5s -benchmem

bench:
	go test -bench . $(BENCHMARK_ARGS)

fmt:
	go fmt ./...

vet:
	go vet ./...

prepare: fmt vet

BIN_NAME=fsserve
MAIN_PATH=main.go
BIN_DIR=./bin
BIN_PATH=$(BIN_DIR)/$(BIN_NAME)

.PHONY: clean build

clean:
	if [ -d $(BIN_DIR) ]; then rm -r $(BIN_DIR); fi

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags "-w -s" -o $(BIN_PATH) $(MAIN_PATH)

DOCKER_NAME=xorkevin/fsserve

DOCKER_MAJOR_VERSION=0.1
DOCKER_VERSION=0.1.0-0

DOCKER_LATEST_IMAGE=$(DOCKER_NAME):latest
DOCKER_MAJOR_IMAGE=$(DOCKER_NAME):$(DOCKER_MAJOR_VERSION)
DOCKER_IMAGE=$(DOCKER_NAME):$(DOCKER_VERSION)

.PHONY: build-docker

build-docker: build
	docker build -t $(DOCKER_IMAGE) -t $(DOCKER_MAJOR_IMAGE) -t $(DOCKER_LATEST_IMAGE) .

publish-docker:
	docker push $(DOCKER_IMAGE)
	docker push $(DOCKER_MAJOR_IMAGE)
	docker push $(DOCKER_LATEST_IMAGE)

docker: build-docker publish-docker
