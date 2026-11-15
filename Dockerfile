# SPDX-License-Identifier: GPL-2.0-or-later
# Syntaxis server image: static Go binary with the embedded frontend, on a
# minimal non-root distroless base. Build: docker build -t syntaxis .

FROM node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# the build writes to ../internal/webui/dist
RUN mkdir -p /src/internal/webui && npm run build

FROM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
ARG COMMIT=
ARG DATE=
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/kolonuk/syntaxis/internal/buildinfo.Version=${VERSION} -X github.com/kolonuk/syntaxis/internal/buildinfo.Commit=${COMMIT} -X github.com/kolonuk/syntaxis/internal/buildinfo.Date=${DATE}" -o /out/syntaxis ./cmd/syntaxis \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/syntaxis /usr/local/bin/syntaxis
# the data directory must be writable by the non-root user (a named volume inherits this ownership)
COPY --from=build --chown=65532:65532 /out/data /data
COPY LICENSE /licenses/LICENSE
ENV SYNTAXIS_DATA_DIR=/data SYNTAXIS_LISTEN=0.0.0.0:8080 SYNTAXIS_ADMIN_LISTEN=0.0.0.0:8081
VOLUME /data
EXPOSE 8080 8081
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/syntaxis"]
CMD ["serve"]
