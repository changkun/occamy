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

The docs are for the people who try, deploy, use and build on Occamy.
Write them to the standard of Apple's developer documentation and the
Claude platform docs: a reader who arrives from a search should
understand what a page is for in its first two sentences, see how the
thing works in a figure, and find each detail where they expect it.
[The protocol page](docs/protocol.md) shows the standard applied.

- **Write in complete, natural sentences.** Address the reader as
  "you", use the present tense and the active voice, and connect ideas
  with ordinary words such as *because*, *so* and *when*. A paragraph
  explains; a list enumerates parallel items; a table holds values to
  look up. Read a page aloud: if it sounds like notes, rewrite it.
- **Open with what the page is for.** Under the title, one or two
  sentences say what the page covers and who it helps. An overview
  follows, which explains the main idea in a few paragraphs and a
  figure before any detail. Then come tasks and concepts, and the
  reference last.
- **Name headings for what the reader does or needs to know**, such as
  "How a session works" or "Deploy behind a proxy", rather than for
  the code that implements it.
- **Show it.** Give every structure a figure: a message's bytes, a
  sequence of messages, a state machine, a deployment's topology. Give
  every screen a screenshot, in light and dark. Draw sequences and
  flowcharts in Mermaid, which GitHub and the website both render. Draw
  what Mermaid can't, such as a byte layout or a screen of tiles, as a
  pair of SVGs from [`scripts/docs_figures.py`](scripts/docs_figures.py),
  shown with `<picture>` as the benchmark figures are. Give every
  procedure an example that the reader can run as written. If a section
  runs for more than a screen without a figure, a table or an example,
  ask what you could show instead.
- **Define each term once,** where it first appears, in italics, and use
  it the same way everywhere after. Link to the definition from other
  pages instead of defining the term again.
- **Compare by saying what is divided by what,** or give both values.
  Write "49 ms instead of 155 ms" or "an improvement of 3×, a third of
  Guacamole's latency", never "3 times better", "3× faster" or "3 times
  smaller". The benchmark report calls Guacamole's value divided by
  Occamy's the *improvement*.
- **Use callouts sparingly,** for what the reader must not miss: a
  secret that must not leak, an action that can't be undone. Write
  them as GitHub alerts, such as `> [!IMPORTANT]`, which the website
  shows as admonitions.
- **Say what Occamy does now.** History, removed features and plans
  belong in [CHANGELOG.md](CHANGELOG.md) and the specs. Leave out "not
  yet", "for now" and "since v1.0.0-beta.2". The one exception is the
  Upgrading section of the configuration page.
- **Use the reader's names:** Occamy, Guacamole, the web client, the
  95th percentile of a key's latency. Package paths and test names
  belong on the Architecture and Development pages, and scenario codes
  and series names belong in the benchmark's method.
- **Link specs from reference pages only.** A user page doesn't link
  into `specs/`. The API, protocol and architecture pages end with a
  Design section that does.
- **Check every claim against the code.** Some tests read the docs:
  `TestAPIDoc`, `TestOpenAPI` and the web client's `ending.test.ts` read
  `api.md`; `TestDocExamples` and the web client's `errors.test.ts` read
  `protocol.md`; and `TestConfigurationExampleRefused` reads the first
  example in `configuration.md`. A measurement is a figure or table from
  `bench/report.py`, with its intervals.

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
