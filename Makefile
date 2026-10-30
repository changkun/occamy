# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

VERSION = $(shell git describe --always --tags)
BUILD = $(shell date +%F)
GOPATH=$(shell go env GOPATH)

IMAGE = occamy
CONTAINER_ENGINE ?= podman

compile:
	CGO_ENABLED=0 go build -trimpath -o occamyd
.PHONY: compile

client:
	cd client/occamy-web && npm ci && npm run build
.PHONY: client

build:
	$(CONTAINER_ENGINE) build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest -f docker/Dockerfile .
.PHONY: build

run:
	cd docker && $(CONTAINER_ENGINE) compose up -d --build --wait
.PHONY: run

stop:
	cd docker && $(CONTAINER_ENGINE) compose down --remove-orphans
.PHONY: stop

# e2e runs the end-to-end tests against a stack started with make run.
e2e:
	OCCAMY_E2E_ENDPOINT=$(or $(OCCAMY_E2E_ENDPOINT),localhost:5636) go test -v -count=1 ./tests/
.PHONY: e2e

# bench builds occamyd's image, starts the benchmark stack and measures
# it, as described in docs/benchmarks.md. BENCH_FLAGS passes flags to the
# harness, for example BENCH_FLAGS="-quick -scenarios T1".
bench: build
	cd docker && $(CONTAINER_ENGINE) compose -f docker-compose.yaml -f docker-compose.bench.yaml up -d --build --wait
	go run ./bench -engine $(CONTAINER_ENGINE) $(BENCH_FLAGS)
.PHONY: bench

# corpus records the frames the codec measurement works on
# (bench/testdata/corpus/README.md) from the bench desktop with a terminal
# and a browser added.
corpus:
	$(CONTAINER_ENGINE) build -t occamy-bench-corpus -f docker/bench/desktop/Dockerfile \
		--build-arg EXTRA_PACKAGES="dillo procps xterm" .
	go run ./bench/vnccapture -engine $(CONTAINER_ENGINE) -image occamy-bench-corpus
.PHONY: corpus

# lint needs golangci-lint v2 (https://golangci-lint.run), as CI uses.
lint:
	golangci-lint run
.PHONY: lint

test:
	go test -cover -coverprofile=cover.test -v ./...
	go tool cover -html=cover.test -o cover.html

clean:
	$(CONTAINER_ENGINE) images -f "dangling=true" -q | xargs $(CONTAINER_ENGINE) rmi -f
	$(CONTAINER_ENGINE) image prune -f
	# docker rm $(docker ps -a -q)
	# docker rmi $(docker images | grep occamy)
.PHONY: clean