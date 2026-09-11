# ---------- build ----------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first: this layer only changes when go.mod/go.sum do.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# CGO is disabled so the result is a static binary that runs on distroless.
ENV CGO_ENABLED=0
# Both binaries are built so a broken CLI fails the image build too, but only
# dit-server is shipped: the CLI is meant to run on the operator's machine.
RUN go build \
      -trimpath \
      -ldflags "-s -w \
        -X main.Version=${VERSION}" \
      -o /out/dit-server ./cmd/dit-server \
 && go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/andriotisnikos1/dit/internal/cli.Version=${VERSION} \
        -X github.com/andriotisnikos1/dit/internal/cli.Commit=${COMMIT} \
        -X github.com/andriotisnikos1/dit/internal/cli.Date=${DATE}" \
      -o /out/dit ./cmd/dit

# ---------- runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/dit-server /dit-server

# The database and the generated master key live here; mount a volume.
#
# Deliberately NOT declared with VOLUME: the orchestrator attaches the volume
# (Railway, or docker-compose's named volume), and a Dockerfile-declared VOLUME
# on a path the platform bind-mounts makes the container fail to start.
ENV DIT_DATA_DIR=/data
ENV DIT_LISTEN=:8080

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/dit-server"]
