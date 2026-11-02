# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

VERSION = $(shell git describe --always --tags)
BUILD = $(shell date +%F)
GOPATH=$(shell go env GOPATH)

# IMAGE is the name that make build tags, without a tag, and IMAGE_TAG the
# tag it gives next to the version. OCCAMY_IMAGE, the image that the demo
# stack runs, sets both, so that a build for a stack of its own leaves
# occamy:latest alone on a shared host. A colon in its last path element
# starts its tag; any before that ends a registry's host and port.
OCCAMY_IMAGE_TAG = $(word 2,$(subst :, ,$(lastword $(subst /, ,$(OCCAMY_IMAGE)))))
IMAGE = $(or $(patsubst %:$(OCCAMY_IMAGE_TAG),%,$(OCCAMY_IMAGE)),occamy)
IMAGE_TAG = $(or $(OCCAMY_IMAGE_TAG),latest)
CONTAINER_ENGINE ?= podman
# BUILD_LABEL marks the images that make build makes, so that make clean
# removes those, once a later build has untagged them, and no other
# image of the engine.
BUILD_LABEL = de.changkun.occamy.make=build

compile:
	CGO_ENABLED=0 go build -trimpath -o occamyd
.PHONY: compile

client:
	cd client/occamy-web && npm ci && npm run build
.PHONY: client

build:
	$(CONTAINER_ENGINE) build --label $(BUILD_LABEL) -t $(IMAGE):$(VERSION) -t $(IMAGE):$(IMAGE_TAG) -f docker/Dockerfile .
.PHONY: build

# DEMO_DEBUG sets the variables of docker-compose.yaml that turn the demo
# stack's debug routes on, to on with OCCAMY_DEBUG=1 and to off without
# it, so that the same variables left in the shell or in docker/.env do
# not turn them on.
DEMO_DEBUG = OCCAMY_V0_CONF=$(if $(filter 1,$(OCCAMY_DEBUG)),occamy-debug.yaml,occamy.yaml) \
	OCCAMY_V1_DEBUG_ADDRESS=$(if $(filter 1,$(OCCAMY_DEBUG)),0.0.0.0:5638)

# DEMO_BIND sets OCCAMY_BIND, the address on which docker-compose.yaml
# publishes occamy-v1's port: the loopback interface, 127.0.0.1, unless
# OCCAMY_BIND names another, such as 0.0.0.0 for every interface. It is
# set either way, so that docker/.env cannot widen it unseen. The v0
# service's port and occamy-v1's debug port stay on the loopback
# interface whatever OCCAMY_BIND says.
DEMO_BIND = OCCAMY_BIND=$(or $(OCCAMY_BIND),127.0.0.1)

# run starts the demo stack, with occamy-v1 published on the loopback
# interface unless OCCAMY_BIND says otherwise (DEMO_BIND), and the rest
# on it always. OCCAMY_DEBUG=1 turns on its debug routes,
# which have no authentication, on the host's loopback interface: the v0
# service's pprof on OCCAMY_PORT, in debug mode (docker/occamy-debug.yaml),
# for the pressure test, and occamy-v1's debug listener on
# OCCAMY_V1_DEBUG_PORT, for make soak. make run without it turns them off
# again.
run:
	cd docker && $(DEMO_BIND) $(DEMO_DEBUG) $(CONTAINER_ENGINE) compose up -d --build --wait
.PHONY: run

stop:
	cd docker && $(CONTAINER_ENGINE) compose down --remove-orphans
.PHONY: stop

# e2e runs the end-to-end tests against a stack started with make run,
# with OCCAMY_DEBUG=1 for the pressure test (OCCAMY_PRESSURE_N):
# the v0 service's on OCCAMY_E2E_ENDPOINT and occamy-v1's, API v1 with
# native VNC and RDP next to guacd's, on OCCAMY_E2E_V1_ENDPOINT. Both
# default to the ports the stack publishes, OCCAMY_PORT and
# OCCAMY_V1_PORT as docker-compose.yaml takes them. OCCAMY_E2E_COMPOSE
# is the stack's compose command, with which a test kills a process in
# the rdp container and reads its log: CONTAINER_ENGINE's by default, so
# e2e takes the CONTAINER_ENGINE that run took. OCCAMY_E2E_CHROME names
# the Chrome that opens the web client, if tests/web_test.go does not
# find one. The VNC parity matrix takes minutes, so the timeout is longer
# than go test's default.
e2e:
	OCCAMY_E2E_ENDPOINT=$(or $(OCCAMY_E2E_ENDPOINT),localhost:$(or $(OCCAMY_PORT),5636)) \
	OCCAMY_E2E_V1_ENDPOINT=$(or $(OCCAMY_E2E_V1_ENDPOINT),localhost:$(or $(OCCAMY_V1_PORT),5637)) \
	OCCAMY_E2E_COMPOSE="$(or $(OCCAMY_E2E_COMPOSE),$(CONTAINER_ENGINE) compose -f $(CURDIR)/docker/docker-compose.yaml)" \
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
# OCCAMY_V1_PORT, OCCAMY_V1_GFX_PORT, GUACD_PORT, OCCAMY_V1_DEBUG_PORT and
# OCCAMY_V1_GFX_DEBUG_PORT, which the harness follows. Its debug routes
# are off unless OCCAMY_DEBUG=1, as run's are, or BENCH_RUNTIME=1, which
# turns on the v1 services' debug listeners alone (BENCH_DEBUG), on the
# loopback interface, and has the harness read occamyd's runtime there.
# The bench stack leaves out the demo's desktop service, but compose does
# not stop one that make run started, which would idle next to the
# measurements, so bench removes it; make run brings it back.
comma := ,
BENCH_DEBUG = $(if $(filter 1,$(BENCH_RUNTIME)),OCCAMY_V1_DEBUG_ADDRESS=0.0.0.0:5638)
BENCH_RUNTIME_FLAGS = $(if $(filter 1,$(BENCH_RUNTIME)),-runtime \
	occamy-v1=localhost:$(or $(OCCAMY_V1_DEBUG_PORT),5638)$(comma)occamy-v1-gfx=localhost:$(or $(OCCAMY_V1_GFX_DEBUG_PORT),5641))
bench: build
	cd docker && $(DEMO_BIND) $(DEMO_DEBUG) $(BENCH_DEBUG) $(CONTAINER_ENGINE) compose -f docker-compose.yaml -f docker-compose.bench.yaml rm -f -s desktop
	cd docker && $(DEMO_BIND) $(DEMO_DEBUG) $(BENCH_DEBUG) $(CONTAINER_ENGINE) compose -f docker-compose.yaml -f docker-compose.bench.yaml up -d --build --wait
	go run ./bench -engine $(CONTAINER_ENGINE) -project $(or $(COMPOSE_PROJECT_NAME),occamy) \
		-addr localhost:$(or $(OCCAMY_PORT),5636) -v1 localhost:$(or $(OCCAMY_V1_PORT),5637) \
		-v1-gfx localhost:$(or $(OCCAMY_V1_GFX_PORT),5640) \
		-guacd localhost:$(or $(GUACD_PORT),4822) $(BENCH_RUNTIME_FLAGS) $(BENCH_FLAGS)
.PHONY: bench

# bench-compare collects the comparison benchmark of
# specs/008-native-architecture/comparison-benchmark.md in one go: the
# plan in bench/plans/compare.yaml, with occamyd's runtime, into
# BENCH_OUT, saved after each block. Before each result it waits for a
# quiet host; if the host stays busy, it saves and exits, and running it
# again resumes. It takes the variables of bench.
BENCH_OUT ?= bench-compare.json
BENCH_PLAN ?= bench/plans/compare.yaml
bench-compare:
	$(MAKE) bench BENCH_RUNTIME=1 BENCH_FLAGS="-plan $(BENCH_PLAN) -resume -out $(BENCH_OUT) $(BENCH_FLAGS)"
.PHONY: bench-compare

# bench-essential collects the essential comparison, about an hour of
# measuring (bench/plans/essential.yaml), as bench-compare does, into
# bench-essential.json.
bench-essential:
	$(MAKE) bench-compare BENCH_PLAN=bench/plans/essential.yaml BENCH_OUT=bench-essential.json
.PHONY: bench-essential

# bench-realtime-1 collects step 1 of
# specs/008-native-architecture/realtime-transport.md, v1-native beside
# v1-native-w2, about two hours of measuring
# (bench/plans/realtime-1.yaml), as bench-compare does, into
# bench-realtime-1.json.
bench-realtime-1:
	$(MAKE) bench-compare BENCH_PLAN=bench/plans/realtime-1.yaml BENCH_OUT=bench-realtime-1.json
.PHONY: bench-realtime-1

# bench-report analyses the comparison's results (BENCH_OUT) into tables,
# tests and figures in BENCH_REPORT with bench/report.py, which uv runs
# with its own numpy, scipy and matplotlib.
BENCH_REPORT ?= docs/benchmarks/comparison
bench-report:
	uv run bench/report.py $(BENCH_OUT) -o $(BENCH_REPORT)
.PHONY: bench-report

# bench-essential-report analyses the essential comparison into
# docs/benchmarks/essential.
bench-essential-report:
	$(MAKE) bench-report BENCH_OUT=bench-essential.json BENCH_REPORT=docs/benchmarks/essential
.PHONY: bench-essential-report

# bench-realtime-1-report analyses step 1 of the real-time transport
# into docs/benchmarks/realtime/1.
bench-realtime-1-report:
	$(MAKE) bench-report BENCH_OUT=bench-realtime-1.json BENCH_REPORT=docs/benchmarks/realtime/1
.PHONY: bench-realtime-1-report

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

# clean removes what make build and make run leave behind, and nothing
# else of the engine's, which other projects may share: the demo
# stack's containers, network and volume and the images that compose
# built for it (compose down, in the project occamy that
# docker-compose.yaml names, or COMPOSE_PROJECT_NAME's), and the images
# of make build that a later build untagged, which carry BUILD_LABEL.
# The tagged images stay; podman rmi or docker rmi removes them.
clean:
	cd docker && $(CONTAINER_ENGINE) compose down --remove-orphans --volumes --rmi local
	$(CONTAINER_ENGINE) image prune -f --filter label=$(BUILD_LABEL)
.PHONY: clean