# ---------- Stage 1: Builder image ---------- 
FROM golang:1.20-alpine AS builder

# Install git (needed for private repos or third-party modules)
RUN apk update && apk add --no-cache git

WORKDIR /app

# Copy go.mod and go.sum first to download dependencies and speed up rebuilds
COPY go.mod go.sum ./
RUN go mod download

# Copy all source files
COPY . .

# Build statically: disable CGO, target linux/amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /reminder-bot

# ---------- Stage 2: Runtime image ---------- 
FROM alpine:latest

# Install ca-certificates and tzdata if your code needs time zones or HTTPS
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /root/

# Copy the compiled binary and config file from the builder image
COPY --from=builder /reminder-bot .
COPY config.json .
# If you already have reminder.json, you can copy it here,
# otherwise it will be created automatically at startup.
# COPY reminder.json .

# Expose port (optional, polling mode does not require a port)
# EXPOSE 8080

# Default entrypoint
ENTRYPOINT ["./reminder-bot"]
