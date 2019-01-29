.PHONY: test

GO ?= go
GOFMT ?= gofmt "-s"
PACKAGES ?= $(shell $(GO) list ./... | grep -v /vendor/)
GOFILES := find . -name "*.go" -type f -not -path "./vendor/*"

install:
	@hash govendor > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/kardianos/govendor; \
	fi
	govendor sync

install-module:
	$(GO) mod download

embedmd-check:
	@hash embedmd > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/campoy/embedmd; \
	fi
	embedmd -d *.md

embedmd:
	@hash embedmd > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/campoy/embedmd; \
	fi
	embedmd -w *.md

fmt:
	$(GOFILES) | xargs $(GOFMT) -w

.PHONY: fmt-check
fmt-check:
	@files=$$($(GOFILES) | xargs $(GOFMT) -l); if [ -n "$$files" ]; then \
		echo "Please run 'make fmt' and commit the result:"; \
		echo "$${files}"; \
		exit 1; \
		fi;

test: fmt-check
	for PKG in $(PACKAGES); do $(GO) test -v -cover -coverprofile $$GOPATH/src/$$PKG/coverage.txt $$PKG || exit 1; done;

html:
	$(GO) tool cover -html=.cover/coverage.txt

vet:
	$(GO) vet $(PACKAGES)

errcheck:
	@which errcheck > /dev/null; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/kisielk/errcheck; \
	fi
	errcheck $(PACKAGES)

revive:
	@hash revive > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/mgechev/revive; \
	fi
	revive -config config.toml -exclude=./vendor/... ./... || exit 1

.PHONY: coverage
coverage:
	@hash gocovmerge > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/wadey/gocovmerge; \
	fi
	gocovmerge $(shell find . -type f -name "coverage.out") > coverage.all;\

.PHONY: misspell-check
misspell-check:
	@hash misspell > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/client9/misspell/cmd/misspell; \
	fi
	misspell -error $(GOFILES)

.PHONY: misspell
misspell:
	@hash misspell > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GO) get -u github.com/client9/misspell/cmd/misspell; \
	fi
	misspell -w $(GOFILES)

clean:
	$(GO) clean -modcache -cache -i
	rm -rf .cover
	find . -name "coverage.txt" -delete
