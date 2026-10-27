FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/unicon-report-bot ./cmd/bot

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/unicon-report-bot /app/unicon-report-bot
COPY migrations /app/migrations
CMD ["/app/unicon-report-bot"]
