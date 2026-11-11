# Node and pnpm versions match .github/workflows (ci.yml, release.yml).
# The UI stage runs on the build host's platform, so multi-arch builds never run Node under QEMU.
FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
RUN npm install -g pnpm@12.11.2
WORKDIR /src/web-ui
COPY web-ui/package.json web-ui/pnpm-lock.yaml web-ui/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web-ui/ ./
# vite.config.ts writes to ../internal/web/static/ui, i.e. /src/internal/web/static/ui.
RUN pnpm build

FROM golang:1.26.11-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/web/static/ui internal/web/static/ui
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /spanbox ./cmd/spanbox

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /spanbox /spanbox
ENV DATA_DIR=/data
VOLUME /data
EXPOSE 4318
ENTRYPOINT ["/spanbox"]
