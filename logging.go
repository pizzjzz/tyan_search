package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
)

// ============================================================================
// Файл: logging.go
// Описание: Структурированное JSON-логирование на базе стандартного log/slog.
//
// Все логи пишутся в os.Stdout строго в формате JSON — это нужно, чтобы
// `docker compose logs -f` показывал разборчивые строки в реальном времени,
// а `docker compose logs | grep ERROR` выводил только системные ошибки.
//
// Уровни:
//   - INFO  — действия пользователей (сообщения, нажатия кнопок), события бота;
//   - WARN  — ошибки Telegram API (например, бот заблокирован пользователем);
//   - ERROR — ошибки сторонних API, паники, фатальные сбои при старте.
// ============================================================================

// logger — глобальный структурированный логгер. JSON-хендлер в os.Stdout.
var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelDebug,
}))

// logProviderError логирует ошибку стороннего API на уровне ERROR с деталями
// запроса: провайдер, endpoint, HTTP-статус и текст ошибки.
func logProviderError(provider, endpoint string, status int, err error) {
	attrs := []any{
		"provider", provider,
		"endpoint", endpoint,
		"error", err.Error(),
	}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	logger.Error("provider_request_error", attrs...)
}

// isErrPanic — вспомогательная проверка значения паники (для читаемости лога).
func isErrPanic(r any) string {
	if err, ok := r.(error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%v", r)
}

// stackTrace возвращает стек вызовов текущей горутины (для логов паник).
func stackTrace() string {
	return string(debug.Stack())
}
