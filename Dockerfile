# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/ledger-service ./cmd/ledger-service \
 && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/payment-service ./cmd/payment-service

FROM alpine:3.20 AS runtime
RUN addgroup -S app && adduser -S -G app -u 10001 app
USER app

FROM runtime AS ledger-service
COPY --from=build /out/ledger-service /usr/local/bin/ledger-service
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/ledger-service"]

FROM runtime AS payment-service
COPY --from=build /out/payment-service /usr/local/bin/payment-service
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/payment-service"]
