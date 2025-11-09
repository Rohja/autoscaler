# Build stage
# NOTE: This Dockerfile must be built from the parent directory (autoscaler/)
# Example: docker build -f minimal-grpc-provider/Dockerfile -t minimal-grpc-provider:latest .
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /build

# Copy go mod files
COPY minimal-grpc-provider/go.mod minimal-grpc-provider/go.sum* ./

# Copy the cluster-autoscaler dependency (needed for the replace directive)
COPY cluster-autoscaler/cloudprovider/externalgrpc/protos ./cluster-autoscaler/cloudprovider/externalgrpc/protos

# Download dependencies
RUN go mod download

# Copy source code
COPY minimal-grpc-provider/main.go ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -o minimal-grpc-provider \
    main.go

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (ca-certificates for TLS, and shell for shutdown command)
RUN apk --no-cache add ca-certificates tzdata curl openssl

# Install talosctl
RUN curl -sL https://talos.dev/install | sh

# Create non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/minimal-grpc-provider /app/minimal-grpc-provider

# Copy default config file (optional, can be overridden via volume mount)
COPY minimal-grpc-provider/config.yaml /app/config.yaml

# Change ownership to non-root user
RUN chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

# Expose gRPC port
EXPOSE 8086

# Health check (requires netcat-openbsd, but we'll use a simpler approach)
# Note: Alpine's netcat might not support -z flag, so we'll skip healthcheck
# or install netcat-openbsd if needed
# HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
#     CMD nc -z localhost 8086 || exit 1

# Run the application
ENTRYPOINT ["/app/minimal-grpc-provider"]
CMD ["--config", "/app/config.yaml", "--address", ":8086"]
