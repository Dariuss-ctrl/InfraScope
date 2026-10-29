# syntax=docker/dockerfile:1

FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/demo-service ./cmd/demo-service

# Distroless: no shell or package manager; runs as an unprivileged user.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/demo-service /usr/local/bin/demo-service
USER nonroot:nonroot
EXPOSE 8082
ENTRYPOINT ["/usr/local/bin/demo-service"]
