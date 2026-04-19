# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o docker-log-adapter .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

# Install Docker CLI so the binary can run docker ps / docker logs
RUN apk add --no-cache docker-cli

WORKDIR /app

COPY --from=builder /app/docker-log-adapter .

ENV PORT=9090
ENV LABEL_FILTER=publishLog=true
ENV RESCAN_MS=5000

EXPOSE 9090

CMD ["./docker-log-adapter"]
