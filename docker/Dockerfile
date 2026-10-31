# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Stage 1: build the web client demo.
FROM node:25-trixie-slim AS web
WORKDIR /occamy/client/occamy-web
COPY client/occamy-web/package.json client/occamy-web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY client/occamy-web ./
RUN npm run build

# Stage 2: build occamyd. It needs no cgo, so the binary is static.
FROM golang:1.27-trixie AS build
WORKDIR /occamy
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o occamyd .
# The license, notice and patent files of the Go code compiled in.
RUN sh scripts/third-party-licenses.sh THIRD_PARTY_LICENSES
# The licences of the lossy tile encoders, for the runtime image's
# licenses/, with the PATENTS file gen2brain/vpx ships: the WebM Project's
# patent grant, which covers Google's implementations of VP8 and does not
# name vpx's port.
RUN mkdir -p /licenses/vpx /licenses/jpegn \
	&& vpx=$(go list -m -f '{{.Dir}}' github.com/gen2brain/vpx) \
	&& cp "$vpx/COPYING" "$vpx/COPYING.webpkit" "$vpx/PATENTS" /licenses/vpx/ \
	&& cp "$(go list -m -f '{{.Dir}}' github.com/gen2brain/jpegn)/LICENSE" /licenses/jpegn/
# The licences of scroll detection's row hash, xxh3, and of the CPU
# feature detection it compiles into amd64 builds.
RUN mkdir -p /licenses/xxh3 /licenses/cpuid \
	&& cp "$(go list -m -f '{{.Dir}}' github.com/zeebo/xxh3)/LICENSE" /licenses/xxh3/ \
	&& cp "$(go list -m -f '{{.Dir}}' github.com/klauspost/cpuid/v2)/LICENSE" /licenses/cpuid/

# Stage 3: runtime image, without a shell or C libraries, running as the
# unprivileged user nonroot. occamyd writes one file, and only for a v1
# file: if auth.signing_key_file does not exist, it creates the signing
# key there at start, through a temporary file in the same directory, so
# that directory must exist and be writable by nonroot, such as a volume
# or a tmpfs (docker-compose.yaml mounts one at /run/occamy).
FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /occamy
COPY --from=build /occamy/occamyd ./
COPY conf.yaml ./
COPY LICENSE ./licenses/
COPY --from=build /occamy/THIRD_PARTY_LICENSES ./licenses/
COPY client/occamy-web/LICENSE.guacamole ./licenses/guacamole/LICENSE
COPY client/occamy-web/NOTICE.guacamole ./licenses/guacamole/NOTICE
COPY --from=build /licenses/vpx ./licenses/vpx/
COPY --from=build /licenses/jpegn ./licenses/jpegn/
COPY --from=build /licenses/xxh3 ./licenses/xxh3/
COPY --from=build /licenses/cpuid ./licenses/cpuid/
# jpegn's adaptive quantisation and progressive scans are ported from
# jpegli, whose notice jpegn's own LICENSE does not carry.
COPY third_party/jpegli/LICENSE ./licenses/jpegli/
COPY --from=web /occamy/client/occamy-web/dist ./client/occamy-web/dist

EXPOSE 5636
CMD ["/occamy/occamyd"]
