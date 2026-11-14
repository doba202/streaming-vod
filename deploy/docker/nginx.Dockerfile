# nginx = cổng vào của trình duyệt (ADR 0011): build SPA (web/app) bằng Node rồi chép vào image nginx.
# Template cấu hình (deploy/nginx/templates) vẫn mount từ compose để sửa không cần build lại.
FROM node:20.19.6-alpine3.22 AS web
WORKDIR /app
COPY web/app/package.json web/app/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/app/ ./
RUN npm run build

FROM nginx:1.27-alpine
COPY --from=web /app/dist /usr/share/nginx/html
