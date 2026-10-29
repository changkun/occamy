# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

VERSION = $(shell git describe --always --tags)
BUILD = $(shell date +%F)
GOPATH=$(shell go env GOPATH)

IMAGE = occamy
CONTAINER_ENGINE ?= podman

compile:
	go build -x -o occamyd
.PHONY: compile

client:
	cd client/occamy-web && npm install && npm run build
.PHONY: client

build:
	$(CONTAINER_ENGINE) build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest -f docker/Dockerfile .
.PHONY: build

run:
	cd docker && $(CONTAINER_ENGINE) compose up -d

stop:
	cd docker && $(CONTAINER_ENGINE) compose down

test:
	go test -cover -coverprofile=cover.test -v ./...
	go tool cover -html=cover.test -o cover.html

clean:
	$(CONTAINER_ENGINE) images -f "dangling=true" -q | xargs $(CONTAINER_ENGINE) rmi -f
	$(CONTAINER_ENGINE) image prune -f
	# docker rm $(docker ps -a -q)
	# docker rmi $(docker images | grep occamy)
.PHONY: clean