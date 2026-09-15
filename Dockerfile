FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /spanbox ./cmd/spanbox

FROM scratch
COPY --from=build /spanbox /spanbox
ENV DATA_DIR=/data
VOLUME /data
EXPOSE 4318
ENTRYPOINT ["/spanbox"]
