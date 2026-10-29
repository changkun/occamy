---
title: Open source release readiness
status: complete
track: maintenance
depends_on: []
affects:
  - CHANGELOG.md
  - SECURITY.md
  - CODE_OF_CONDUCT.md
  - CONTRIBUTING.md
  - LICENSE
  - client/occamy-web/LICENSE.guacamole
  - client/occamy-web/NOTICE.guacamole
  - .goreleaser.yaml
  - scripts/third-party-licenses.sh
  - .github/
  - docker/Dockerfile
  - README.md
  - Makefile
effort: large
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Open source release readiness

## Overview

Occamy builds, tests and runs again, but it is not yet a project someone
can adopt with confidence:

- it has no releases or published artifacts;
- it has no changelog or security policy;
- it redistributes Apache-licensed code without that code's license.

This spec brings the repository up to the standard of a well-maintained
open source project. It covers licensing, a changelog, a security policy,
community files, CI quality gates, and a tag-driven release pipeline that
publishes checksummed, attested artifacts.

## Current State

- **License.** `LICENSE` is MIT, "2019-2020 Ou Changkun".
  - `guacamole/` holds a modified guacamole-server 1.0.0. That is 206
    files, 200 of them with Apache-2.0 ASF headers that point to a NOTICE
    file, but neither `LICENSE` nor `NOTICE` is present.
  - The image ships the compiled libguac.
  - Apache-2.0 §4 requires a copy of the license and the NOTICE with any
    redistribution, and notices on modified files. `guacamole/README.md`
    says the copy is modified.
- **History.** Tags `0.1.0` (2019-01-31), `0.2.0` (2019-11-05), `0.3.0`
  (2019-11-12) and `0.4.0` (2020-02-17) exist, with no `v` prefix and no
  GitHub releases. 57 commits from the 2026 revival are unreleased. The Go
  module proxy only recognises `vX.Y.Z` tags, so pkg.go.dev shows
  pseudo-versions.
- **Community files.**
  - `CONTRIBUTING.md` is three sentences.
  - Issue templates are free-form Markdown with no security routing.
  - The PR template asks for "resolve #issue_id".
  - No `CODE_OF_CONDUCT.md` or `SECURITY.md`.
- **CI** (`.github/workflows/dockerimage.yml`, "Builds"):
  - the jobs are `go` (vet, test with libguac), `web` (lint, build),
    `image` (Podman build) and `e2e` (demo stack, from
    [006](006-demo-stack-refresh.md));
  - there is no race detector, vulnerability scan or dependency update
    automation.
- **Repository settings.** Private vulnerability reporting, secret
  scanning and Dependabot security updates are disabled. `master` is not
  protected. Merge commits, squash and rebase are all allowed.
- **Artifacts.** None are published. Users build the image themselves
  (`make build`).
- **Security posture.** [004](004-security-hardening.md) lists the gaps:
  credentials in tokens, an open login endpoint, default secrets, plain
  HTTP and a root container.
- **Portability.** `occamyd` links libguac through cgo, so a standalone
  binary is not useful until
  [remove-libguac](005-upstream-guacamole/remove-libguac.md) lands. From
  then on it is pure Go and cross-compiles freely.

## Components

### Licensing

- `guacamole/LICENSE`: the Apache-2.0 text, and `guacamole/NOTICE`: the
  upstream 1.0.0 NOTICE ("Apache Guacamole / Copyright 2018 The Apache
  Software Foundation ..."). Add a line that Occamy modified the copy, and
  point to `guacamole/README.md` for what changed.
- The image copies both into `/occamy/licenses/guacamole/`.
- `LICENSE`: copyright "2019-2026 Ou Changkun".
- README License section: Occamy is MIT, and `guacamole/` is Apache-2.0.
- Release archives carry `LICENSE`, plus the guacamole notices while
  libguac ships. Once binaries ship, they carry a third-party license
  report, `THIRD_PARTY_LICENSES`, written by
  `scripts/third-party-licenses.sh`. `go-licenses check` gates
  dependencies in CI.
- Since [remove-libguac](005-upstream-guacamole/remove-libguac.md), the
  Apache files live in `client/occamy-web/` as `LICENSE.guacamole` and
  `NOTICE.guacamole`, for the web client's fork of guacamole-common-js
  0.9.14. The image keeps them under `/occamy/licenses/guacamole/`, and the
  web client archive carries them.

### Versioning and changelog

- Tags are `vMAJOR.MINOR.PATCH[-pre]` from now on. Old tags stay as they
  are. Milestones follow [008](008-native-architecture.md): `v0.5.0` adds
  guacd and makes `occamyd` cgo-free, then `v1.0.0-beta.1`, and so on.
- `CHANGELOG.md` in [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/)
  format:
  - an `Unreleased` section summarising the revival;
  - short entries for 0.1.0 to 0.4.0, reconstructed from the tags;
  - comparison links at the bottom.
- Every user-visible PR adds a line under `Unreleased`. A release moves
  them under the new version.
- Release notes are the version's changelog section.

### Security policy

`SECURITY.md`:

- **Supported versions:** before 1.0, only the latest release and
  `master`.
- **Reporting:** GitHub private vulnerability reporting
  (`/security/advisories/new`), with `hi@changkun.de` as the fallback.
  Never public issues. Best-effort response, stated plainly as a volunteer
  project.
- **Security notes:** the current caveats from
  [004](004-security-hardening.md) in operator terms:
  - tokens carry the remote credentials and must be treated as secrets;
  - put TLS in front, because `occamyd` speaks plain HTTP;
  - change `jwt_secret` and use `mode: release`;
  - `client: true` makes `/api/v1/login` an open proxy and is for demos
    only;
  - the demo credentials are public.

### Community files

- `CODE_OF_CONDUCT.md`: Contributor Covenant 2.1, with reports to
  `hi@changkun.de`.
- `CONTRIBUTING.md`:
  - how to build and test (`make build`, `make run`, `make e2e`);
  - the spec workflow (`specs/`);
  - commit style `area: lowercase summary`;
  - PRs merge with merge commits only after CI is green, so the commit
    SHAs that specs cite stay valid;
  - changelog lines;
  - the release procedure.
- Issue forms (`.github/ISSUE_TEMPLATE/*.yml`) for bugs and features, and
  a `config.yml` that disables blank issues and links to the security
  policy.
- A PR template with the changelog and spec checkboxes.

### CI quality gates

Added to "Builds":

- `go test -race` in the `go` job, which has libguac installed;
- `on: push` limited to `master`, plus `pull_request`, so a PR branch no
  longer runs every job twice;
- a `govulncheck` job;
- `.github/dependabot.yml` for `gomod`, `npm` (`client/occamy-web`),
  `github-actions` and `docker` (`docker/`, `docker/demo/*`). It runs
  weekly with grouped minor and patch updates.

### Release pipeline

`.github/workflows/release.yml` runs on `v*` tags, and on
`workflow_dispatch` as a dry run that builds everything and publishes
nothing. It has four jobs:

1. **image**:
   - builds `docker/Dockerfile` natively on `ubuntu-latest` (amd64) and
     `ubuntu-24.04-arm` (arm64), which is faster than QEMU;
   - pushes by digest to `ghcr.io/changkun/occamy`, then merges a
     multi-arch manifest tagged with the version, plus `latest` for
     non-prereleases;
   - attests build provenance with `actions/attest-build-provenance`
     (`push-to-registry: true`).
2. **web**: builds `client/occamy-web` into
   `occamy-web-<version>.tar.gz`.
3. **binaries**, after
   [remove-libguac](005-upstream-guacamole/remove-libguac.md):
   - GoReleaser builds `occamyd` with `CGO_ENABLED=0` for
     linux/darwin/windows × amd64/arm64;
   - archives include `LICENSE`, `README.md`, `conf.yaml` and the
     third-party license report.
4. **release**:
   - writes `SHA256SUMS` over all files;
   - attests provenance for every file;
   - runs `gh release create` with the changelog section as notes, marked
     as a prerelease when the version has a `-`.

Verification for users goes in the README:

```
sha256sum -c SHA256SUMS
gh attestation verify <file> --repo changkun/occamy
gh attestation verify oci://ghcr.io/changkun/occamy:<version> --repo changkun/occamy
```

### README and badges

- **Badges:** Builds, Release (latest GitHub release, replacing "Latest
  tag"), Go Reference, Go version and License.
- **Install section:** `podman run ghcr.io/changkun/occamy:<version>`,
  checksum and attestation verification, and a link to `SECURITY.md`.

### Repository settings (maintainer)

These are settings, not files, so the maintainer applies them:

- private vulnerability reporting;
- secret scanning with push protection;
- Dependabot alerts and security updates;
- branch protection on `master`, requiring `go`, `web`, `image` and `e2e`
  with the branch up to date, and letting administrators bypass it;
- merge commits only, with branches deleted on merge.

## Plan

Each phase is one PR, merged when CI is green.

1. **Licensing and community files:** `guacamole/LICENSE` and `NOTICE`,
   the image notices, `LICENSE` year, `CHANGELOG.md`, `SECURITY.md`,
   `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md`, issue forms, PR template and
   the README License section. Prerequisite: the maintainer enables
   private vulnerability reporting, otherwise the `SECURITY.md` link
   returns 404.
2. **CI gates:** race tests, govulncheck, Dependabot and the `master`-only
   push trigger.
3. **Release pipeline:** `release.yml` with the image, web, checksum,
   attestation and release jobs, a dry run through `workflow_dispatch`,
   and the README install section and badges.
4. **Binaries** (unblocked:
   [remove-libguac](005-upstream-guacamole/remove-libguac.md) is complete):
   GoReleaser, the binaries job and the third-party license report.

## Decisions

Settled with the maintainer on 2026-10-29:

1. **First release:** `v0.5.0`, once [005](005-upstream-guacamole.md) is
   complete and `occamyd` is cgo-free. Phases 3 and 4 both land before
   it. Until then the pipeline is exercised only through its
   `workflow_dispatch` dry run, and no prerelease is tagged.
2. **Contacts:** vulnerabilities go through GitHub private vulnerability
   reporting, with `hi@changkun.de` as the fallback in `SECURITY.md`.
   Conduct reports go to `hi@changkun.de`, the address already public as
   a commit author.
3. **Branch protection:** checks `go`, `web`, `image` and `e2e` are
   required on `master`, and administrators may bypass them.

## Testing Strategy

- **Phase 1:** `make build`, and the notices are present in the image.
  Links in the new Markdown files resolve.
- **Phase 2:** the new jobs pass on the PR, and a Dependabot dry parse
  shows no configuration errors in the repository's Insights.
- **Phase 3:** a `workflow_dispatch` dry run builds both architectures,
  the web tarball and `SHA256SUMS`, and publishes nothing.
- **First release (`v0.5.0`):** the tag publishes, and
  `gh attestation verify` succeeds for one file and for the image.

## Acceptance Criteria

- [x] Redistributed Apache-2.0 code carries its LICENSE and NOTICE, in the
      repository and in the image.
- [x] `CHANGELOG.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md` and a complete
      `CONTRIBUTING.md` exist.
- [x] CI runs race tests and govulncheck; Dependabot is configured.
- [x] A `v*` tag produces a GitHub release with a multi-arch GHCR image,
      the web client archive, `SHA256SUMS` and provenance attestations.
- [x] After remove-libguac, releases include `occamyd` binaries for
      linux, darwin and windows on amd64 and arm64.

## Outcome

**Summary.** Implemented directly in five PRs:

- #59, licensing and community files;
- #60, CI gates;
- #61, the release pipeline;
- #66, binaries;
- #70, linting.

[v0.5.0](https://github.com/changkun/occamy/releases/tag/v0.5.0) was
released from them on 2026-10-29 (tag on `30fef0a`, run 40932881518). Drift:
Minimal. Every item shipped. The one design change is that the license
report comes from a script rather than `go-licenses report`, with
`go-licenses check` kept as a gate.

**Release verified.**

- The release is published, not a draft. It has 8 assets: six `occamyd`
  archives, the web archive and `SHA256SUMS`. Its notes are CHANGELOG's
  `[0.5.0]` section.
- `shasum -c` passes for the downloaded archives, and the darwin/arm64
  binary runs.
- `gh attestation verify` passes for an archive and for
  `oci://ghcr.io/changkun/occamy:v0.5.0`. Both carry SLSA provenance v1
  signed by `release.yml@refs/tags/v0.5.0`.
- The image manifest lists amd64 and arm64 (plus buildx's provenance
  entries). The GHCR package is public. The image runs as `nonroot` and
  is 34.7 MB.

**Unspecified work.**

- golangci-lint (#70), added after the maintainer asked whether linting
  was enough (improvement). Its first run found:
  - two real defects, fixed before the tag: ambiguous session keys
    (concatenated claims, MD5), and no `ReadHeaderTimeout`;
  - an extra reply after a failed upgrade.
- The release dry run warns instead of failing when `[Unreleased]` is
  empty (#69). Otherwise every Dockerfile PR would fail it right after a
  release (improvement).

### Implementation notes

- **Status.** All four phases merged, and `v0.5.0` was released from them:
  - phase 1: #59;
  - phase 2: #60;
  - phase 3: #61;
  - phase 4: #66.
- **What was done.**
  - **Licensing.** Apache LICENSE and NOTICE, first for `guacamole/`. They
    moved to `client/occamy-web/` when #65 deleted the fork. `LICENSE` year
    updated, and the image carries the notices.
  - **Community.** `CHANGELOG.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`,
    `CONTRIBUTING.md`, issue forms and a PR template.
  - **CI.** `go test -race`, govulncheck and the `master`-only push
    trigger, plus Dependabot for gomod, npm, actions and the five Docker
    directories. Since #65, also a build-only macOS and Windows job. Since
    #66, `go-licenses check`.
  - **Release.** `release.yml` has image (native amd64 and arm64), manifest,
    web, binaries (GoReleaser) and release jobs. Every PR that touches the
    release build runs the dry run. The README covers the release contents
    and verification, and its badge shows the latest release.
  - **Repository settings.** Private vulnerability reporting, secret
    scanning with push protection, and Dependabot alerts and security
    fixes. Branch protection on `master`: go, web, image and e2e, strict,
    admins may bypass.
- **Decisions made during implementation.**
  - Archives are named `occamyd-<version>-<os>-<arch>`, with the tag
    including its `v`, to match `occamy-web-<version>`.
  - They are reproducible: fixed root owner, commit-date mtimes and
    `mod_timestamp`. A second local build was byte-identical.
  - GoReleaser is pinned to v2.18.3, and its checksum, changelog and
    release steps are disabled, so `release.yml` owns those.
  - The binaries use `go-version: stable` like the other jobs, which
    picks up Go patch releases.
- **Deviations from the spec.**
  - The license report comes from `scripts/third-party-licenses.sh`, not
    `go-licenses report`. The report template can reach a module's
    license text but not its NOTICE files. Apache-2.0 requires carrying
    those, and `go.yaml.in/yaml/v3` (NOTICE) and `go.mongodb.org/mongo-driver/v2`
    (THIRD-PARTY-NOTICES) have them.
  - The script includes every LICENSE, COPYING, NOTICE and
    THIRD-PARTY-NOTICES file, taking modules from the union over linux,
    darwin and windows (the same 24 modules today), plus Go's own
    license. It fails if a module has none.
  - go-licenses still classifies licenses: `check
    --disallowed_types=forbidden,restricted` runs in the `go` job.
- **Surprises / gotchas.**
  - The 172 MB to 34.7 MB image drop came from #65, not from this spec.
  - macOS bsdtar lacks `--sort` and `--transform`, so the web archive
    command only runs in CI.
  - Vite drops `/*!` license comments, so shipped notices must be files.
- **Follow-ups.**
  - Branch protection could also require `build (macos-latest)`,
    `build (windows-latest)` and `binaries`.

