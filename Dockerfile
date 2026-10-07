# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
ARG APP_VERSION=dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${APP_VERSION}" -o /out/slackhooks .

FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 -S slackhooks \
    && adduser -u 10001 -S slackhooks -G slackhooks \
    && mkdir -p /data \
    && chown 10001:10001 /data
COPY --from=build /out/slackhooks /usr/local/bin/slackhooks
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
