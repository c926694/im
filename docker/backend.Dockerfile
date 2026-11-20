FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/im ./cmd/im

FROM alpine:3.23
COPY --from=build /out/im /usr/local/bin/im
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/im"]
