set positional-arguments

# Image name for dockerctl (default would be the checkout's dir name)
export IMAGE_NAME := env("IMAGE_NAME", "slackhooks")

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X main.version=" + version

default:
  @just --list

# Vet, check formatting and run the tests
test:
  go vet ./...
  @test -z "$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
  go test ./...

# Build the local binary (./slackhooks)
bin:
  go build -trimpath -ldflags="{{ ldflags }}" -o slackhooks .

# Cross-compile a static Linux binary (./slackhooks-linux-ARCH)
bin-linux ARCH="amd64":
  CGO_ENABLED=0 GOOS=linux GOARCH={{ ARCH }} go build -trimpath -ldflags="{{ ldflags }}" -o slackhooks-linux-{{ ARCH }} .

# Run slackhooks with config.yaml (no args = start), e.g. `just run add-hook -label "My CI" '!room:server'`
run *ARGS:
  go run . "$@"

# Build the Docker image (needs dockerctl from dotfiles)
build *ARCH:
  dockerctl build {{ ARCH }}

# Push the Docker image to $REGISTRY_HOST
push *ARCH:
  dockerctl push {{ ARCH }}

# Build and push the Docker image
publish ARCH="amd64":
  dockerctl build push {{ ARCH }}
