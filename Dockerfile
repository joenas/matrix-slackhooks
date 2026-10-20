# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
ARG TARGETOS TARGETARCH
ARG APP_VERSION=dev
COPY go.mod go.sum ./
RUN go mod download
# Host binary (no GOOS/GOARCH), in its own layer so it stays cached; keep the version in sync with the justfile
RUN go install github.com/google/go-licenses/v2@v2.0.1
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${APP_VERSION}" -o /out/slackhooks .
# Dependency licenses and notices (plus MPL-2.0 source, e.g. mautrix) for /usr/share/licenses/slackhooks
RUN go-licenses save ./... --save_path /out/licenses --ignore github.com/joenas/matrix-slackhooks \
    && cp LICENSE /out/licenses/LICENSE

FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 -S slackhooks \
    && adduser -u 10001 -S slackhooks -G slackhooks \
    && mkdir -p /data \
    && chown 10001:10001 /data
COPY --from=build /out/slackhooks /usr/local/bin/slackhooks
COPY --from=build /out/licenses /usr/share/licenses/slackhooks
USER 10001:10001
WORKDIR /data
VOLUME /data
ENV SLACKHOOKS_CONFIG=/data/config.yaml \
    SLACKHOOKS_DB=/data/slackhooks.db \
    SLACKHOOKS_AS_ADDRESS=0.0.0.0:29329
EXPOSE 29329
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["slackhooks", "healthcheck"]
ENTRYPOINT ["slackhooks"]
CMD ["start"]
