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

# The macOS app (client/occamy-swift, specs/010-native-swift-client.md)
# runs on MACOS_MIN and later. swift-core builds the Go client core's C
# export layer as liboccamy.a, a universal archive for Apple silicon and
# Intel, in client/occamy-swift/build/core.
MACOS_MIN = 26.0
SWIFT_DIR = client/occamy-swift
CORE_DIR = $(SWIFT_DIR)/build/core
core-slice = CGO_ENABLED=1 GOOS=darwin GOARCH=$(1) CC="xcrun --sdk macosx clang -arch $(2)" \
	CGO_CFLAGS="-O2 -mmacosx-version-min=$(MACOS_MIN)" CGO_LDFLAGS="-mmacosx-version-min=$(MACOS_MIN)" \
	go build -trimpath -buildmode=c-archive -o $(CORE_DIR)/$(1)/liboccamy.a ./$(SWIFT_DIR)/core

swift-core:
	mkdir -p $(CORE_DIR)
	$(call core-slice,arm64,arm64)
	$(call core-slice,amd64,x86_64)
	lipo -create -output $(CORE_DIR)/liboccamy.a $(CORE_DIR)/arm64/liboccamy.a $(CORE_DIR)/amd64/liboccamy.a
.PHONY: swift-core

# swift-test tests OccamyKit, whose tests build and run the interop
# server with go.
swift-test: swift-core
	cd $(SWIFT_DIR) && swift test
.PHONY: swift-test

# swift-app bundles the app as Occamy.app in client/occamy-swift/build/app,
# a universal binary, with its icon compiled from App/AppIcon.icon, an
# Icon Composer file of the logo (docs/occamy.png at three times its
# size, pixel for pixel), and signs it with the hardened runtime: with
# DEVELOPER_ID, the owner's "Developer ID Application: …" identity in
# their keychain, or else ad hoc. swift-dmg puts it in a disk image,
# signed with DEVELOPER_ID too; swift-notarize has Apple notarize the
# image and staples the ticket to it, with NOTARY_PROFILE, the keychain
# profile that `xcrun notarytool store-credentials` saved.
DEVELOPER_ID ?=
NOTARY_PROFILE ?=
APP_DIR = $(SWIFT_DIR)/build/app
APP = $(APP_DIR)/Occamy.app
APP_VERSION = $(patsubst v%,%,$(VERSION))
DMG = $(APP_DIR)/Occamy-$(APP_VERSION).dmg
# One architecture a build: two at once take Xcode's build system, which
# cannot find SwiftTerm's build plugin.
app-slice = cd $(SWIFT_DIR) && swift build -c release --product Occamy --triple $(1)-apple-macosx$(MACOS_MIN) \
	--scratch-path build/app/$(1)

swift-app: swift-core
	$(call app-slice,arm64)
	$(call app-slice,x86_64)
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS
	lipo -create -output $(APP)/Contents/MacOS/Occamy $(APP_DIR)/arm64/release/Occamy $(APP_DIR)/x86_64/release/Occamy
	mkdir -p $(APP)/Contents/Resources
	# actool reads relative paths against $$PWD, which make does not set.
	xcrun actool $(CURDIR)/$(SWIFT_DIR)/App/AppIcon.icon --compile $(CURDIR)/$(APP)/Contents/Resources \
		--platform macosx --minimum-deployment-target $(MACOS_MIN) --app-icon AppIcon \
		--output-partial-info-plist $(CURDIR)/$(APP_DIR)/icon.plist \
		--output-format human-readable-text --errors --warnings
	sed -e 's/@VERSION@/$(APP_VERSION)/' -e "s/@BUILD@/$$(git rev-list --count HEAD)/" -e 's/@MACOS_MIN@/$(MACOS_MIN)/' \
		$(SWIFT_DIR)/App/Info.plist > $(APP)/Contents/Info.plist
	plutil -lint $(APP)/Contents/Info.plist
	codesign --force --sign "$(or $(DEVELOPER_ID),-)" --options runtime $(if $(DEVELOPER_ID),--timestamp) \
		--entitlements $(SWIFT_DIR)/App/Occamy.entitlements $(APP)
	codesign --verify --strict --verbose=2 $(APP)
.PHONY: swift-app

swift-dmg: swift-app
	rm -rf $(APP_DIR)/dmg $(DMG)
	mkdir -p $(APP_DIR)/dmg
	ditto $(APP) $(APP_DIR)/dmg/Occamy.app
	ln -s /Applications $(APP_DIR)/dmg/Applications
	hdiutil create -volname Occamy -srcfolder $(APP_DIR)/dmg -format UDZO -ov $(DMG)
	rm -rf $(APP_DIR)/dmg
	$(if $(DEVELOPER_ID),codesign --force --sign "$(DEVELOPER_ID)" --timestamp $(DMG))
.PHONY: swift-dmg

ifneq ($(filter swift-notarize,$(MAKECMDGOALS)),)
ifeq ($(DEVELOPER_ID),)
$(error swift-notarize needs DEVELOPER_ID, a "Developer ID Application: …" identity)
endif
ifeq ($(NOTARY_PROFILE),)
$(error swift-notarize needs NOTARY_PROFILE, a profile of xcrun notarytool store-credentials)
endif
endif

swift-notarize: swift-dmg
	xcrun notarytool submit $(DMG) --keychain-profile "$(NOTARY_PROFILE)" --wait
	xcrun stapler staple $(DMG)
	xcrun stapler validate $(DMG)
	spctl --assess --type open --context context:primary-signature --verbose=2 $(DMG)
.PHONY: swift-notarize

build:
	$(CONTAINER_ENGINE) build --label $(BUILD_LABEL) -t $(IMAGE):$(VERSION) -t $(IMAGE):$(IMAGE_TAG) -f docker/Dockerfile .
.PHONY: build

# DEMO_DEBUG sets the variable of docker-compose.yaml that turns the demo
# stack's debug listener on, to on with OCCAMY_DEBUG=1 and to off without
# it, so that the same variable left in the shell or in docker/.env does
# not turn it on.
DEMO_DEBUG = OCCAMY_V1_DEBUG_ADDRESS=$(if $(filter 1,$(OCCAMY_DEBUG)),0.0.0.0:5638)

# DEMO_BIND sets OCCAMY_BIND, the address on which docker-compose.yaml
# publishes occamy-v1's port: the loopback interface, 127.0.0.1, unless
# OCCAMY_BIND names another, such as 0.0.0.0 for every interface. It is
# set either way, so that docker/.env cannot widen it unseen. occamy-v1's
# debug port stays on the loopback interface whatever OCCAMY_BIND says.
DEMO_BIND = OCCAMY_BIND=$(or $(OCCAMY_BIND),127.0.0.1)

# E2E_COMPOSE are the compose files of the stack that the end-to-end
# tests run on: the demo stack's, with docker-compose.e2e.yaml over it,
# which gives occamy-v1 the tests' v1 file, the demo's connections and
# those of the servers that the tests probe. make run E2E=1 starts it,
# and make e2e names the same files for the tests' compose commands.
# DEMO_COMPOSE is what run passes compose: those files with E2E=1, and
# none without it, for docker-compose.yaml alone.
E2E_COMPOSE = docker-compose.yaml docker-compose.e2e.yaml
DEMO_COMPOSE = $(if $(filter 1,$(E2E)), $(addprefix -f ,$(E2E_COMPOSE)))

# run starts the demo stack, with occamy-v1 published on the loopback
# interface unless OCCAMY_BIND says otherwise (DEMO_BIND). E2E=1 starts
# the end-to-end tests' stack instead (E2E_COMPOSE), for make e2e and
# make soak. OCCAMY_DEBUG=1 turns on occamy-v1's debug listener, which
# has no authentication, on the host's loopback interface on
# OCCAMY_V1_DEBUG_PORT, for make soak and the pressure test. make run
# without it turns it off again.
run:
	cd docker && $(DEMO_BIND) $(DEMO_DEBUG) $(CONTAINER_ENGINE) compose$(DEMO_COMPOSE) up -d --build --wait
.PHONY: run

# stop stops the stack that make run started, with E2E=1 or without:
# compose down acts on the project, and the e2e stack has the demo's
# services.
stop:
	cd docker && $(CONTAINER_ENGINE) compose down --remove-orphans
.PHONY: stop

# e2e runs the end-to-end tests against a stack started with make run
# E2E=1, with OCCAMY_DEBUG=1 for the pressure test (OCCAMY_PRESSURE_N),
# through occamy-v1 on OCCAMY_E2E_V1_ENDPOINT, and its debug listener on
# OCCAMY_E2E_V1_DEBUG. They default to the ports the stack publishes,
# OCCAMY_V1_PORT and OCCAMY_V1_DEBUG_PORT as docker-compose.yaml takes
# them. OCCAMY_E2E_COMPOSE is the stack's compose command, with which a
# test kills a process in the rdp container, reads its log and recreates
# occamy-v1: CONTAINER_ENGINE's, with the files that make run E2E=1
# started the stack with (E2E_COMPOSE), by default, so e2e takes the
# CONTAINER_ENGINE that run took. OCCAMY_E2E_CHROME names the Chrome
# that opens the web client, if tests/web_test.go does not find one. The
# VNC parity matrix takes minutes, so the timeout is longer than go
# test's default.
e2e:
	OCCAMY_E2E_V1_ENDPOINT=$(or $(OCCAMY_E2E_V1_ENDPOINT),localhost:$(or $(OCCAMY_V1_PORT),5637)) \
	OCCAMY_E2E_V1_DEBUG=$(or $(OCCAMY_E2E_V1_DEBUG),localhost:$(or $(OCCAMY_V1_DEBUG_PORT),5638)) \
	OCCAMY_E2E_COMPOSE="$(or $(OCCAMY_E2E_COMPOSE),$(CONTAINER_ENGINE) compose $(addprefix -f $(CURDIR)/docker/,$(E2E_COMPOSE)))" \
	go test -v -count=1 -timeout 20m ./tests/
.PHONY: e2e

# soak runs TestSoak against a stack started with make run E2E=1
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
# on a shared host takes COMPOSE_PROJECT_NAME, OCCAMY_V1_PORT,
# OCCAMY_V1_CUBIC_PORT, GUACD_PORT, OCCAMY_V1_DEBUG_PORT and
# OCCAMY_V1_CUBIC_DEBUG_PORT, which the harness follows. Its debug routes
# are off unless OCCAMY_DEBUG=1, as run's are, or BENCH_RUNTIME=1, which
# turns on the v1 services' debug listeners alone (BENCH_DEBUG), on the
# loopback interface, and has the harness read occamyd's runtime there.
# The bench stack leaves out the demo's desktop service, but compose does
# not stop one that make run started, which would idle next to the
# measurements, so bench removes it; make run brings it back.
comma := ,
BENCH_DEBUG = $(if $(filter 1,$(BENCH_RUNTIME)),OCCAMY_V1_DEBUG_ADDRESS=0.0.0.0:5638)
BENCH_RUNTIME_FLAGS = $(if $(filter 1,$(BENCH_RUNTIME)),-runtime \
	occamy-v1=localhost:$(or $(OCCAMY_V1_DEBUG_PORT),5638)$(comma)occamy-v1-cubic=localhost:$(or $(OCCAMY_V1_CUBIC_DEBUG_PORT),5643))
bench: build
	cd docker && $(DEMO_BIND) $(DEMO_DEBUG) $(BENCH_DEBUG) $(CONTAINER_ENGINE) compose -f docker-compose.yaml -f docker-compose.bench.yaml rm -f -s desktop
	cd docker && $(DEMO_BIND) $(DEMO_DEBUG) $(BENCH_DEBUG) $(CONTAINER_ENGINE) compose -f docker-compose.yaml -f docker-compose.bench.yaml up -d --build --wait
	go run ./bench -engine $(CONTAINER_ENGINE) -project $(or $(COMPOSE_PROJECT_NAME),occamy) \
		-v1 localhost:$(or $(OCCAMY_V1_PORT),5637) -v1-cubic localhost:$(or $(OCCAMY_V1_CUBIC_PORT),5642) \
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

# bench-realtime-2 collects step 2 of the real-time transport, loss,
# about four hours of measuring (bench/plans/realtime-2.yaml), as
# bench-compare does, into bench-realtime-2.json. It builds the image
# that emulates the link in the kernel (docker/bench/netem) first.
bench-realtime-2:
	$(CONTAINER_ENGINE) build -t occamy-bench-netem docker/bench/netem
	$(MAKE) bench-compare BENCH_PLAN=bench/plans/realtime-2.yaml BENCH_OUT=bench-realtime-2.json
.PHONY: bench-realtime-2

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

# bench-realtime-2-report analyses step 2 of the real-time transport
# into docs/benchmarks/realtime/2.
bench-realtime-2-report:
	$(MAKE) bench-report BENCH_OUT=bench-realtime-2.json BENCH_REPORT=docs/benchmarks/realtime/2
.PHONY: bench-realtime-2-report

# bench-realtime-3a collects step 3a of the real-time transport, the
# congestion control, about two hours of measuring
# (bench/plans/realtime-3a.yaml), as bench-realtime-2 does, into
# bench-realtime-3a.json. The host's kernel must have BBR loaded, which
# occamy-v1's occamyd asks for (docs/benchmarks.md).
bench-realtime-3a:
	$(CONTAINER_ENGINE) build -t occamy-bench-netem docker/bench/netem
	$(MAKE) bench-compare BENCH_PLAN=bench/plans/realtime-3a.yaml BENCH_OUT=bench-realtime-3a.json
.PHONY: bench-realtime-3a

# bench-realtime-3a-report analyses step 3a into
# docs/benchmarks/realtime/3a.
bench-realtime-3a-report:
	$(MAKE) bench-report BENCH_OUT=bench-realtime-3a.json BENCH_REPORT=docs/benchmarks/realtime/3a
.PHONY: bench-realtime-3a-report

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