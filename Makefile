# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

VERSION = $(shell git describe --always --tags)
BUILD = $(shell date +%F)
GOPATH=$(shell go env GOPATH)

HOME = github.com/changkun/occamy
IMAGE = occamy

build: clean
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest -f docker/Dockerfile .
.PHONY: occamy

run:
	cd docker && docker-compose up

stop:
	cd docker && docker-compose down

test:
	go test -cover -coverprofile=cover.test -v ./...
	go tool cover -html=cover.test -o cover.html

clean:
	docker images -f "dangling=true" -q | xargs docker rmi -f
	docker image prune -f
.PHONY: clean