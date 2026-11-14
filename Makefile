COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env

.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker run-analytics \
	e2e-m2 e2e-m3 e2e-m4 e2e-m5 e2e-m6 seed-admin sign-url analytics-sql web-install web-dev web-test web-build

# Tạo .env từ .env.example; nếu .env đã có thì bổ sung các khoá mới (vd. khoá M3) mà không ghi đè giá trị cũ.
env:
	@test -f .env || cp .env.example .env
	@for k in $$(grep -oE '^[A-Z0-9_]+=' .env.example | tr -d =); do \
	  grep -q "^$$k=" .env || { grep "^$$k=" .env.example >> .env; echo "added $$k to .env"; }; \
	done

# Các service one-shot (minio-init, kafka-init) thoát với mã 0 nên không dùng --wait cho chúng.
up: env
	$(COMPOSE) up -d --build
	$(COMPOSE) up -d --wait postgres minio kafka console clickhouse nginx api transcoder analytics

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

logs-app:
	$(COMPOSE) logs -f --tail=100 api transcoder analytics nginx

ps:
	$(COMPOSE) ps

test:
	go test ./...

test-short:
	go test -short ./...

build:
	go build -o bin/api ./cmd/api
	go build -o bin/transcoder ./cmd/transcoder
	go build -o bin/analytics ./cmd/analytics

# Sinh video mẫu 30 giây 1080p có tiếng vào tmp/sample.mp4
sample:
	@mkdir -p tmp
	ffmpeg -hide_banner -y -f lavfi -i testsrc2=size=1920x1080:rate=30 \
	  -f lavfi -i sine=frequency=440:sample_rate=48000 -t 30 \
	  -c:v libx264 -pix_fmt yuv420p -c:a aac tmp/sample.mp4

# make transcode FILE=tmp/sample.mp4 ID=demo
transcode: build env
	set -a; . ./.env; set +a; ./bin/transcoder file -in $(FILE) -id $(ID)

# Chạy api trên host (cần `make up`; dừng service api trong compose trước nếu đã có, vì cùng cổng 8000).
run-api: env
	set -a; . ./.env; set +a; go run ./cmd/api

# Chạy transcoder worker trên host (cần FFmpeg trên máy; dừng service transcoder trong compose trước nếu đã có).
run-worker: env
	set -a; . ./.env; set +a; go run ./cmd/transcoder worker

# Chạy analytics trên host (dừng service analytics trong compose trước nếu đã có, vì cùng consumer group).
run-analytics: env
	set -a; . ./.env; set +a; go run ./cmd/analytics

# SQL phân tích mẫu (scripts/analytics/*.sql) trên ClickHouse: make analytics-sql [Q=top_videos]
analytics-sql: env
	@set -a; . ./.env; set +a; ./scripts/analytics-sql.sh $(Q)

# Tạo hoặc đặt lại admin (ADMIN_EMAIL / ADMIN_PASSWORD trong .env) trong container api đang chạy.
seed-admin: env
	set -a; . ./.env; set +a; $(COMPOSE) exec -T -e ADMIN_EMAIL -e ADMIN_PASSWORD api api seed-admin

# In URL CDN đã ký cho một video: make sign-url ID=<video_id>
sign-url: env
	@$(COMPOSE) exec -T api api sign -id $(ID)

# E2E M2: đăng nhập admin → create → upload presigned → complete → ready → HLS qua URL ký.
e2e-m2: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; ./scripts/e2e-m2.sh tmp/sample.mp4

# E2E M3: auth, phân quyền, /play, nginx chặn URL sai hoặc hết hạn (chạy kèm luồng M2).
e2e-m3: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; ./scripts/e2e-m3.sh tmp/sample.mp4

# E2E M4: SPA + /api/ cùng origin qua nginx, tìm kiếm không dấu (chạy kèm luồng M2 qua nginx).
e2e-m4: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; ./scripts/e2e-m4.sh tmp/sample.mp4

# E2E M5: poster/sprite/thumbnails.vtt qua URL ký, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp" + resume_at,
# playback.events trong Kafka. `make e2e-m5 KAFKA_DOWN=1` thêm bước dừng Kafka (sự kiện xem vẫn 204).
e2e-m5: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; COMPOSE="$(COMPOSE)" E2E_KAFKA_DOWN="$(KAFKA_DOWN)" ./scripts/e2e-m5.sh tmp/sample.mp4

# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
WEB := web/app

web-install:
	cd $(WEB) && npm ci

# Vite dev server http://localhost:5173 (hot reload); /api và /cdn proxy tới nginx :8088 nên cần `make up`.
web-dev:
	cd $(WEB) && npm run dev

web-test:
	cd $(WEB) && npm test

web-build:
	cd $(WEB) && npm run build
