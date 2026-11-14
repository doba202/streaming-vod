COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env

.PHONY: env up down logs ps test test-short build sample transcode

env:
	@test -f .env || cp .env.example .env

up: env
	$(COMPOSE) up -d --wait

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

ps:
	$(COMPOSE) ps

test:
	go test ./...

test-short:
	go test -short ./...

build:
	go build -o bin/transcoder ./cmd/transcoder

# Sinh video mẫu 30 giây 1080p có tiếng vào tmp/sample.mp4
sample:
	@mkdir -p tmp
	ffmpeg -hide_banner -y -f lavfi -i testsrc2=size=1920x1080:rate=30 \
	  -f lavfi -i sine=frequency=440:sample_rate=48000 -t 30 \
	  -c:v libx264 -pix_fmt yuv420p -c:a aac tmp/sample.mp4

# make transcode FILE=tmp/sample.mp4 ID=demo
transcode: build env
	set -a; . ./.env; set +a; ./bin/transcoder file -in $(FILE) -id $(ID)
