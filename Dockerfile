FROM golang:1.25-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bridge-go .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /bridge-go /app/bridge-go

EXPOSE 9090
ENTRYPOINT ["/app/bridge-go"]