# Build stage
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o pull-request-notifier .

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata sqlite && \
    addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /app/pull-request-notifier .

# Data directory for SQLite — mount a volume here in production.
RUN mkdir -p /data && chown app:app /data

ARG VERSION=dev
ENV VERSION=${VERSION}

USER app

EXPOSE 8080

ENTRYPOINT ["./pull-request-notifier"]
