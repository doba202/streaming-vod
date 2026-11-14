FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/analytics ./cmd/analytics

FROM alpine:3.24.4
RUN adduser -D -u 10001 app
COPY --from=build /out/analytics /usr/local/bin/analytics
USER app
ENTRYPOINT ["/usr/local/bin/analytics"]
