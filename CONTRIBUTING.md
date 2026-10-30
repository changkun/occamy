# Contributing to Occamy

Feedback, bug reports and pull requests are welcome. Please follow the
[code of conduct](CODE_OF_CONDUCT.md), and report security issues
privately as described in [SECURITY.md](SECURITY.md).

## Issues

Search the existing issues first. A good bug report says which version or
commit you ran, how you deployed it (image or source), which protocol you
connected with, what you expected, and what happened, with `occamyd`
logs. Remove tokens and passwords from the logs: tokens contain the remote
credentials.

## Development

You need Go 1.26 or later, Node.js 20.19+ or 22.12+ for the web client, and
Podman or Docker with a compose provider.

```
make build      # build the image (CONTAINER_ENGINE=docker for Docker)
make run        # start occamy and the demo servers on localhost:5636
make e2e        # end-to-end tests against the running stack
make stop
```

`go test ./...` runs the unit tests; the end-to-end tests in `tests/` skip
unless `make e2e` points them at a running stack. No C toolchain is needed.
See the README for a native build. The guacd bridge's conformance corpus,
recorded guacd sessions that `go test` replays, is recorded again with
`go test -tags record` against the bench stack, run as a compose project
of its own (`$CONTAINER_ENGINE compose -p occamy-record`), as the doc
comment of `internal/backend/guacd/record_test.go` shows. When a replay
fails, `OCCAMY_CONFORMANCE_DUMP=<dir> go test -run TestConformance
./internal/backend/guacd` writes the failing frames there as PNG files.

`make lint` runs golangci-lint v2 with the linters and formatters in
`.golangci.yml`, as CI does. Code must be `gofmt`ed, with imports grouped
by `goimports`.

## Design specs

Larger changes start as a design spec in [specs/](specs/README.md). A spec
records the problem, the options and the decision, and an Outcome section
once it has been built. If your change touches an area with a spec, read
the spec first, and update it when the change makes it inaccurate.

## Pull requests

- Keep each commit to one logical change, and leave the tree building and
  passing tests after every commit.
- Write commit messages as `area: lowercase summary`, where the area is
  the package or directory, for example
  `guacd: add a handshake client for upstream guacd`. Explain the why in
  the body.
- Add tests for new behaviour, and a regression test for every bug fix.
- Add a line under `Unreleased` in [CHANGELOG.md](CHANGELOG.md) for any
  user-visible change.
- CI (`go`, `web`, `image` and `e2e`) must pass before a PR merges. PRs
  merge with a merge commit, so that the commit hashes cited in specs stay
  valid.

## Releases

Releases are tagged `vMAJOR.MINOR.PATCH`, with a pre-release suffix such
as `-beta.1` when needed. To cut one:

1. Move the `Unreleased` entries in `CHANGELOG.md` under the new version
   and date, and update the comparison links.
2. Merge that change, wait for CI to pass on `master`, and tag that commit.
3. Push the tag. `.github/workflows/release.yml` builds the multi-arch
   image, the `occamyd` archives (`.goreleaser.yaml`) and the web client
   archive, and publishes them with
   `SHA256SUMS`, provenance attestations, and the changelog section as
   release notes. It fails if `CHANGELOG.md` has no entries for the
   version.

Run the workflow by hand (Actions, Release, Run workflow) for a dry run
that builds everything and publishes nothing. Pull requests that change
the workflow, `.goreleaser.yaml`, `docker/Dockerfile` or
`scripts/third-party-licenses.sh` run the dry run automatically.
