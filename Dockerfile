FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod ./
COPY go.sum ./
RUN go mod download
COPY . ./
RUN go build -o tiktocker cmd/tiktocker/main.go


FROM gcr.io/distroless/static

WORKDIR /app
COPY --from=builder /app/tiktocker ./
COPY --from=builder /app/config.yaml ./

VOLUME ["/etc/tiktocker"]

ENTRYPOINT ["/app/tiktocker"]
