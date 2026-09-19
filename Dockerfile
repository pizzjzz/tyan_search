# syntax=docker/dockerfile:1

# ============================================================================
# Dockerfile для anime-scanner-bot
#
# Двухстадийная сборка: стадия build (Go) и минимальная стадия runtime (alpine).
#
# ВАЖНО: токены в образ НЕ запекаются. Конфигурация (.env) монтируется из хоста
# при запуске, поэтому ключи не попадают ни в слои образа, ни в сам контейнер:
#   docker run -v "$(pwd)/.env:/app/.env:ro" anime-scanner-bot
# ============================================================================

# --- Стадия 1: сборка бинарника из исходников ---
FROM golang:1.21-alpine AS build

WORKDIR /src

# Сначала только манифесты зависимостей: слой `go mod download` кэшируется
# и не пересобирается при изменениях исходников.
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходники и собираем статический бинарник.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/anime-scanner-bot .

# --- Стадия 2: минимальный runtime-образ ---
FROM alpine:3.20

# ca-certificates обязательны: бот ходит по HTTPS к api.telegram.org,
# graphql.anilist.co, api.jikan.moe, api.trace.moe и api.animetrace.com.
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 appuser

WORKDIR /app

COPY --from=build /out/anime-scanner-bot /app/anime-scanner-bot

# /app — рабочая директория, куда при запуске монтируется .env.
RUN chown appuser:appuser /app

# Запуск от непривилегированного пользователя (без root в контейнере).
USER appuser

CMD ["/app/anime-scanner-bot"]