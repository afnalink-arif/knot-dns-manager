FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod ./
COPY . .

ARG APP_VERSION=dev
RUN go mod tidy && CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${APP_VERSION}" -o /backend ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates docker-cli docker-cli-compose git bash bind-tools lmdb-tools
COPY --from=builder /backend /usr/local/bin/backend

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/backend"]
