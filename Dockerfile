# SPDX-License-Identifier: GPL-2.0-or-later
# Syntaxis server image: static Go binary with the embedded frontend, on a
# minimal non-root distroless base. Build: docker build -t syntaxis .

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# the build writes to ../internal/webui/dist
RUN mkdir -p /src/internal/webui && npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/syntaxis ./cmd/syntaxis \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/syntaxis /usr/local/bin/syntaxis
# the data directory must be writable by the non-root user (a named volume inherits this ownership)
COPY --from=build --chown=65532:65532 /out/data /data
COPY LICENSE /licenses/LICENSE
COPY THIRD_PARTY_LICENSES /licenses/THIRD_PARTY_LICENSES
ENV SYNTAXIS_DATA_DIR=/data SYNTAXIS_LISTEN=0.0.0.0:8080 SYNTAXIS_ADMIN_LISTEN=0.0.0.0:8081
VOLUME /data
EXPOSE 8080 8081
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/syntaxis"]
CMD ["serve"]
