# Contributing to Occamy

Feedback, bug reports and pull requests are welcome. Please follow the
[code of conduct](CODE_OF_CONDUCT.md), and report security issues
privately as described in [SECURITY.md](SECURITY.md).

## Issues

Search the existing issues first. A good bug report says which version or
commit you ran, how you deployed it (image or source), which protocol you
connected with, what you expected, and what happened, with `occamyd`
logs. Remove tokens and passwords from the logs.

## Development

[docs/development.md](./docs/development.md) describes what you need,
building, and every test suite, with the command of each. The checks
that CI runs on a pull request:

```sh
go test -race ./...   # the unit tests
make lint             # golangci-lint v2, with .golangci.yml
make build            # the image, then the end-to-end tests against it:
make run E2E=1
make e2e
make stop
```

Code must be `gofmt`ed, with imports grouped by `goimports`.

The README and `docs/` are also the website,
<https://changkun.github.io/occamy/>.
[Development](./docs/development.md#the-website) shows how to preview
it. The `Pages` workflow builds it with `--strict` for a pull request
that touches them, so a link or an anchor that does not resolve fails
it.

## Writing the docs

The docs are for people who try, deploy, use and build on Occamy. Specs
record how it was designed. Keep the two apart:

- **One page, one job.** Open with what the page lets the reader do. Put
  the common case first and the exceptions after it.
- **Say what Occamy does now.** History, removed features and plans
  belong in [CHANGELOG.md](CHANGELOG.md) and the specs. Leave out "not
  yet", "for now" and "since v1.0.0-beta.2". The one exception is the
  Upgrading section of the configuration page.
- **Short sentences, one idea each.** When a sentence chains clauses
  with colons and semicolons, split it. Use a numbered list for steps and
  a table for keys and values. Keep each cell to a phrase or a sentence.
- **Use the reader's names:** Occamy, Guacamole, the web client, the
  95th percentile of a key's latency. Package paths and test names belong
  on the Architecture and Development pages. Scenario codes and series
  names belong in the benchmark's method.
- **Link specs from reference pages only.** A user page does not link
  into `specs/`. The API, protocol and architecture pages end with a
  Design section that does.
- **Show it.** Give an example the reader can run as written. Draw a
  structure as a figure in Mermaid, which both GitHub and the website
  render. A measurement is a figure or table from `bench/report.py`,
  with its intervals.
- **Check every claim against the code.** Some tests read the docs.
  `TestAPIDoc`, `TestOpenAPI` and the web client's `ending.test.ts` read
  `api.md`. `TestDocExamples` and the web client's `errors.test.ts` read
  `protocol.md`.
  `TestConfigurationExampleRefused` reads `configuration.md`'s first
  example.

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
  `config: refuse a file without version 1`. Explain the why in
  the body.
- Add tests for new behaviour, and a regression test for every bug fix.
- Add a line under `Unreleased` in [CHANGELOG.md](CHANGELOG.md) for any
  user-visible change.
- CI (`go`, `lint`, `build` on macOS and Windows, `macos` for the Mac
  app, `web`, `image`, `e2e` and `bench`, and `Pages` when the docs
  change) must pass before a PR merges. PRs merge with a merge commit,
  so that the commit hashes cited in specs stay valid.

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
4. On a Mac with the Developer ID, build Occamy for macOS from the tag,
   since its version comes from `git describe`, have Apple notarize it,
   and add the disk image to the release:

   ```sh
   git worktree add --detach ../occamy-release vMAJOR.MINOR.PATCH
   cd ../occamy-release
   make swift-notarize DEVELOPER_ID="Developer ID Application: …" NOTARY_PROFILE=…
   gh release upload vMAJOR.MINOR.PATCH client/occamy-swift/build/app/Occamy-MAJOR.MINOR.PATCH.dmg
   ```

   `NOTARY_PROFILE` names a profile that `xcrun notarytool
   store-credentials` saved. The disk image is not in `SHA256SUMS`: its
   Developer ID signature and Apple's notarization vouch for it.

Run the workflow by hand (Actions, Release, Run workflow) for a dry run
that builds everything and publishes nothing. Pull requests that change
the workflow, `.goreleaser.yaml`, `docker/Dockerfile` or
`scripts/third-party-licenses.sh` run the dry run automatically.
