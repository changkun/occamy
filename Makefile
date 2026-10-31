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

# DEMO_DEBUG sets the variables of docker-compose.yaml that turn the demo
# stack's debug routes on, to on with OCCAMY_DEBUG=1 and to off without
# it, so that the same variables left in the shell or in docker/.env do
# not turn them on.
DEMO_DEBUG = OCCAMY_V0_CONF=$(if $(filter 1,$(OCCAMY_DEBUG)),occamy-debug.yaml,occamy.yaml) \
	OCCAMY_V1_DEBUG_ADDRESS=$(if $(filter 1,$(OCCAMY_DEBUG)),0.0.0.0:5638)

# run starts the demo stack. OCCAMY_DEBUG=1 turns on its debug routes,
# which have no authentication, on the host's loopback interface: the v0
# service's pprof on OCCAMY_PORT, in debug mode (docker/occamy-debug.yaml),
# for the pressure test, and occamy-v1's debug listener on
# OCCAMY_V1_DEBUG_PORT, for make soak. make run without it turns them off
# again.
run:
	cd docker && $(DEMO_DEBUG) $(CONTAINER_ENGINE) compose up -d --build --wait
.PHONY: run

stop:
	cd docker && $(CONTAINER_ENGINE) compose down --remove-orphans
.PHONY: stop

# e2e runs the end-to-end tests against a stack started with make run,
# with OCCAMY_DEBUG=1 for the pressure test (OCCAMY_PRESSURE_N):
# the v0 service's on OCCAMY_E2E_ENDPOINT and occamy-v1's, API v1 with
# native VNC and RDP next to guacd's, on OCCAMY_E2E_V1_ENDPOINT. Both
# default to the ports the stack publishes, OCCAMY_PORT and
# OCCAMY_V1_PORT as docker-compose.yaml takes them. The VNC parity matrix
# takes minutes, so the timeout is longer than go test's default.
e2e:
	OCCAMY_E2E_ENDPOINT=$(or $(OCCAMY_E2E_ENDPOINT),localhost:$(or $(OCCAMY_PORT),5636)) \
	OCCAMY_E2E_V1_ENDPOINT=$(or $(OCCAMY_E2E_V1_ENDPOINT),localhost:$(or $(OCCAMY_V1_PORT),5637)) \
	go test -v -count=1 -timeout 20m ./tests/
.PHONY: e2e

# soak runs TestSoak against a stack started with make run
# OCCAMY_DEBUG=1: sessions through occamy-v1 for a duration while it
# samples occamyd's goroutines, heap and open files from occamy-v1's
# debug listener, on OCCAMY_V1_DEBUG_PORT, and fails if one grows.
# OCCAMY_SOAK_SESSIONS says how many sessions, 8 by default;
# tests/soak_test.go lists the other variables. The full soak
# (specs/008-native-architecture/guacd-bridge.md, Implementation notes:
# VNC's soak) is
#
#	make soak OCCAMY_SOAK_SESSIONS=20 OCCAMY_SOAK_DURATION=8h
soak:
	OCCAMY_E2E_V1_ENDPOINT=$(or $(OCCAMY_E2E_V1_ENDPOINT),localhost:$(or $(OCCAMY_V1_PORT),5637)) \
	OCCAMY_SOAK_DEBUG=$(or $(OCCAMY_SOAK_DEBUG),localhost:$(or $(OCCAMY_V1_DEBUG_PORT),5638)) \
	OCCAMY_SOAK_SESSIONS=$(or $(OCCAMY_SOAK_SESSIONS),8) \
	go test -v -count=1 -timeout 0 -run '^TestSoak$$' ./tests/
.PHONY: soak

# bench builds occamyd's image, starts the benchmark stack and measures
# it, as described in docs/benchmarks.md. BENCH_FLAGS passes flags to the
# harness, for example BENCH_FLAGS="-quick -scenarios T1". A second stack
# on a shared host takes COMPOSE_PROJECT_NAME, OCCAMY_PORT,
# OCCAMY_V1_PORT and GUACD_PORT, which the harness follows. Its debug
# routes are off unless OCCAMY_DEBUG=1, as run's are.
bench: build
	cd docker && $(DEMO_DEBUG) $(CONTAINER_ENGINE) compose -f docker-compose.yaml -f docker-compose.bench.yaml up -d --build --wait
	go run ./bench -engine $(CONTAINER_ENGINE) -project $(or $(COMPOSE_PROJECT_NAME),docker) \
		-addr localhost:$(or $(OCCAMY_PORT),5636) -v1 localhost:$(or $(OCCAMY_V1_PORT),5637) \
		-guacd localhost:$(or $(GUACD_PORT),4822) $(BENCH_FLAGS)
.PHONY: bench

# corpus records the frames the codec measurement works on
# (bench/testdata/corpus/README.md) from the bench desktop with a terminal
# and a browser added.
corpus:
	$(CONTAINER_ENGINE) build -t occamy-bench-corpus -f docker/bench/desktop/Dockerfile \
		--build-arg EXTRA_PACKAGES="dillo procps xterm" .
	go run ./bench/vnccapture -engine $(CONTAINER_ENGINE) -image occamy-bench-corpus
.PHONY: corpus

# codecs measures the tile codecs on the corpus and writes the results
# that docs/benchmarks/codecs.md quotes to docs/benchmarks. bench/codecs
# is a module of its own, so occamyd's go.mod carries none of the
# candidates. It takes about five minutes on one core; keep the host idle.
codecs:
	cd bench/codecs && go test -tags measure -run TestMeasure -timeout 2h . -args -out ../../docs/benchmarks
.PHONY: codecs

# lint needs golangci-lint v2 (https://golangci-lint.run), as CI uses.
lint:
	golangci-lint run
	cd bench/codecs && golangci-lint run --build-tags measure ./...
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