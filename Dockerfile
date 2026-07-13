FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/wallet-management

FROM alpine:3.20
RUN apk add --no-cache wget \
    && adduser -D -u 10001 app
USER app
COPY --from=builder /server /server
EXPOSE 8080 9090
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["/server"]
