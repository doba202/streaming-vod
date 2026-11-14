FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/transcoder ./cmd/transcoder

FROM alpine:3.24.4
# Gói ffmpeg của Alpine có sẵn libx264 và ffprobe.
RUN apk add --no-cache ffmpeg && adduser -D -u 10001 app
COPY --from=build /out/transcoder /usr/local/bin/transcoder
USER app
ENV WORK_DIR=/tmp
ENTRYPOINT ["/usr/local/bin/transcoder"]
CMD ["worker"]
