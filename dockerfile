FROM golang:1.23.0-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .

#Build Binary
RUN CGO_ENABLED=0 GOOS=linux go build \
     -ldflags="-s -w" \
    -o gopher-log-transporter \
    ./cmd/main.go
#Runtime Image
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/gopher-log-transporter .
ENTRYPOINT ["./gopher-log-transporter"]
