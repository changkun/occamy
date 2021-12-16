# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

VERSION = $(shell git describe --always --tags)
BUILD = $(shell date +%F)
GOPATH=$(shell go env GOPATH)

HOME = changkun.de/x/occamy
IMAGE = occamy

compile:
	go build -x -o occamyd cmd/occamyd/*
.PHONY: compile

build:
	docker build --platform linux/x86_64 -t $(IMAGE):$(VERSION) -t $(IMAGE):latest -f docker/Dockerfile .
.PHONY: occamy

up:
	cd docker && docker-compose up -d

down:
	cd docker && docker-compose down

clean:
	docker images -f "dangling=true" -q | xargs docker rmi -f
	docker image prune -f
	# docker rm $(docker ps -a -q)
	# docker rmi $(docker images | grep occamy)
.PHONY: clean