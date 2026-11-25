FROM golang:1.26-alpine AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/im ./cmd/im

FROM alpine:3.23
WORKDIR /etc/im
COPY --from=build /out/im /usr/local/bin/im
RUN mkdir -p /var/lib/im/avatars /var/lib/im/chat-images && chown 65534:65534 /var/lib/im/avatars /var/lib/im/chat-images
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/im"]
