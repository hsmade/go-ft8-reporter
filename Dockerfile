# syntax=docker/dockerfile:1
FROM golang:1.27.3-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ ./vendor/

COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -mod=vendor -trimpath -ldflags="-s -w" \
      -o /out/go-ft8-reporter ./cmd/go-ft8-reporter

# Keep glibc/libm for go-ft8's default PocketFFT C backend.
FROM debian:bookworm-slim
COPY --from=build /out/go-ft8-reporter /usr/local/bin/go-ft8-reporter
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY LICENSE /usr/share/doc/go-ft8-reporter/LICENSE
COPY vendor/github.com/ColonelBlimp/go-ft8/internal/pfft/pocketfft/LICENSE.md /usr/share/doc/go-ft8-reporter/pocketfft/LICENSE.md

USER 65532:65532
EXPOSE 2112/tcp
ENTRYPOINT ["/usr/local/bin/go-ft8-reporter"]
