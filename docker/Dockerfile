# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Stage 1: build the web client demo.
FROM node:24-trixie-slim AS web
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

# Stage 3: runtime image, without a shell or C libraries, running as the
# unprivileged user nonroot. occamyd writes no files.
FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /occamy
COPY --from=build /occamy/occamyd ./
COPY conf.yaml ./
COPY LICENSE ./licenses/
COPY guacamole/LICENSE guacamole/NOTICE ./licenses/guacamole/
COPY --from=web /occamy/client/occamy-web/dist ./client/occamy-web/dist

EXPOSE 5636
CMD ["/occamy/occamyd"]
