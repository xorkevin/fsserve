.PHONY: all install

all: install

install:
	go install -trimpath -ldflags "-w -s" .

.PHONY: test coverage cover

TEST_ARGS?=
TEST_PACKAGE?=./...
COVERAGE?=cover.out

test:
	go test -race -trimpath -ldflags "-w -s" -cover -covermode atomic -coverprofile $(COVERAGE) $(TEST_ARGS) $(TEST_PACKAGE)


coverage:
	go tool cover -html $(COVERAGE)

cover: test coverage

.PHONY: fmt vet prepare

fmt:
	goimports -w .

vet:
	go vet ./...

prepare: fmt vet

BIN_NAME=fsserve
MAIN_PATH=.
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
DOCKER_VERSION=0.1.1-0

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
