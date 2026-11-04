# Contributing to Occamy

Thank you for helping with Occamy. This page explains how to report a
problem, how to make a change, and what a pull request needs before it
merges. Everyone who takes part follows the
[code of conduct](CODE_OF_CONDUCT.md). If you find a security issue,
report it privately, as [SECURITY.md](SECURITY.md) describes, rather
than in an issue.

## Report a problem

Search the existing issues first, since someone may have reported the
problem already. Then open an issue with the bug report form, which
asks for what we need to reproduce the problem:

- the release, image tag or commit you ran, and how you deployed it;
- the protocol you connected with, such as VNC or RDP;
- what you did, what you expected, and what happened instead, with the
  steps that reproduce it;
- what `occamyd` logged at the time, with tokens and passwords
  removed;
- your host's operating system and architecture, your container engine,
  and the browser or client you used.

## Make a change

Larger changes start as a design spec in [specs/](specs/README.md). A
spec records the problem, the options and the decision, and gains an
Outcome section once the change is built. If your change touches an
area that a spec covers, read the spec first, and update it in the same
pull request when your change makes it inaccurate.

[Development](./docs/development.md) shows you how to build Occamy and
run each test suite. Before you open a pull request, run the main
checks that CI runs on it:

```sh
go test -race ./...   # the unit tests
make lint             # golangci-lint v2, with .golangci.yml
make build            # the image, then the end-to-end tests against it
make run E2E=1
make e2e
make stop
```

If your change touches the web client, also run
[its tests](./docs/development.md#the-web-clients-tests). Format the Go
code with `gofmt`, and group its imports as `goimports` does; `make lint`
checks both.

The README and `docs/` are also the website,
<https://changkun.github.io/occamy/>. If you change them,
[preview the website](./docs/development.md#preview-the-website) and
build it with `--strict`, as the `Pages` workflow does for your pull
request, so that a link or an anchor that doesn't resolve fails before
review.

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
  pair of SVGs from a module of [`scripts/figures/`](scripts/figures)
  named after the page, shown with `<picture>` as the benchmark figures
  are. Give every
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

## Send a pull request

- **Keep each commit to one logical change,** and leave the tree
  building and passing its tests after every commit.
- **Write each commit message as `area: lowercase summary`,** where the
  area is the package or directory, such as
  `config: refuse a file without version 1`. Explain why in the body.
- **Add tests** for new behaviour, and a regression test for every bug
  you fix.
- **Update the docs and the changelog in the same pull request.** Add a
  line under `Unreleased` in [CHANGELOG.md](CHANGELOG.md) for any change
  that users can see.

A pull request merges once CI passes: the jobs `go`, `lint`, `build` on
macOS and on Windows, `macos` for the Mac app, `web`, `image`, `e2e`
and `bench`, and `Pages` when the docs change. It merges with a merge
commit, so that the commit hashes that the specs cite stay valid.

## Releases

Maintainers tag releases as `vMAJOR.MINOR.PATCH`, with a suffix such as
`-beta.1` for a prerelease. [Cut a release](docs/development.md#cut-a-release)
gives every step, from the changelog to the signed Mac app.
