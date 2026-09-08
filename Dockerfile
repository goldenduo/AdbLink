# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/adblink-server ./cmd/adblink-server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/adblink-ctl ./cmd/adblink-ctl

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/adblink-server /app/adblink-server
COPY --from=builder /out/adblink-ctl /app/adblink-ctl

# Expose control port (8888), web dashboard (9999), and ADB port range (55550-55599)
EXPOSE 8888 9999 55550-55599

ENTRYPOINT ["/app/adblink-server"]
CMD ["-listen", ":8888", "-web", ":9999", "-port-min", "55550", "-port-max", "55599"]
