package main

import "os"

// ============================================================================
// Файл: main.go
// Описание: Точка входа в программу.
//
// Порядок запуска:
//   1. LoadConfig() — читает токены из .env (см. config.go и .env.example).
//   2. NewBot(cfg)   — создаёт Telegram-бота и API-клиенты.
//   3. bot.Run()     — запускает лонг-поллинг и обрабатывает сообщения.
// ============================================================================

func main() {
	cfg := LoadConfig()

	bot, err := NewBot(cfg)
	if err != nil {
		logger.Error("bot_startup_error", "error", err.Error())
		os.Exit(1)
	}

	bot.Run()
}
