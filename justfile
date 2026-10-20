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

# Smoke-test the image locally (env from .env, data in the slackhooks-dev volume)
docker-run *ARGS:
  docker run --rm -it -p 29329:29329 -v slackhooks-dev:/data --env-file .env $IMAGE_NAME:{{ version }} "$@"

# Release: tag main as vVERSION and publish that image, e.g. `just release 0.3.0`
[confirm("Tag main and publish this release?")]
release VERSION ARCH="amd64":
  #!/usr/bin/env bash
  set -euo pipefail
  v="{{ VERSION }}"
  tag="v${v#v}"
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "❌ VERSION must look like 1.2.3"; exit 1; }
  [ -n "${REGISTRY_HOST:-}" ] || { echo "❌ \$REGISTRY_HOST is not set"; exit 1; }
  [ "$(git branch --show-current)" = main ] || { echo "❌ Releases are tagged on main"; exit 1; }
  [ -z "$(git status --porcelain)" ] || { echo "❌ Working tree is not clean"; exit 1; }
  git fetch -q origin main --tags
  [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "❌ main is not in sync with origin/main"; exit 1; }
  if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then echo "❌ $tag already exists"; exit 1; fi
  just test
  # Build before tagging, so a failed build leaves no tag behind
  VERSION="$tag" dockerctl build {{ ARCH }}
  git tag "$tag"
  git push origin "$tag"
  VERSION="$tag" dockerctl push {{ ARCH }}
  echo "✅ Released $tag as $REGISTRY_HOST/$IMAGE_NAME:$tag"
