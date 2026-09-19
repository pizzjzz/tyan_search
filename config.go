package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// ============================================================================
// Файл: config.go
// Описание: Загрузка конфигурации бота из файла .env.
//
// Файл .env содержит все токены и ключи, необходимые для работы.
// Реальный .env в git не попадает (см. .gitignore).
// Шаблон без секретов — .env.example (его можно коммитить).
// ============================================================================

// Config — все настройки бота. Каждое поле соответствует переменной из .env.
type Config struct {
	// TelegramBotToken — токен Telegram-бота (ОБЯЗАТЕЛЬНО).
	// Получается у @BotFather: /newbot -> токен вида 123456:ABC-DEF...
	TelegramBotToken string

	// TraceMoeAPIKey — ключ API сервиса trace.moe (ОПЦИОНАЛЬНО).
	// Сервис определяет аниме по скриншоту (запасной путь, когда AnimeTrace
	// не распознал персонажа). Без ключа работает с лимитом ~10 запросов/мин.
	TraceMoeAPIKey string

	// ArchetypeEnabled — включать ли определение архетипа персонажа.
	// Архетип — опциональная функция; можно отключить через ARCHETYPE_ENABLED.
	ArchetypeEnabled bool
}

// LoadConfig читает файл .env и заполняет структуру Config.
// Если .env отсутствует или TELEGRAM_BOT_TOKEN пуст — программа завершается
// с понятным сообщением, чтобы сразу было видно, что исправить.
func LoadConfig() *Config {
	// Файл .env может отсутствовать: в Docker настройки передаются через
	// env_file (docker-compose.yml) либо docker run -e.
	loadErr := godotenv.Load()

	cfg := &Config{
		TelegramBotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		TraceMoeAPIKey:   getEnv("TRACEMOE_API_KEY", ""),
		ArchetypeEnabled: getEnvBool("ARCHETYPE_ENABLED", true),
	}

	if cfg.TelegramBotToken == "" {
		if loadErr != nil {
			logger.Error("config_error",
				"error", fmt.Sprintf("не найден файл .env, а в окружении не задан TELEGRAM_BOT_TOKEN. Скопируйте .env.example в .env, либо передайте токен через переменную окружения (env_file в docker-compose или docker run -e). Ошибка: %v", loadErr),
			)
			os.Exit(1)
		}
		logger.Error("config_error",
			"error", "в файле .env не задан TELEGRAM_BOT_TOKEN. Получите его у @BotFather (команда /newbot).",
		)
		os.Exit(1)
	}

	return cfg
}

// getEnv читает переменную окружения; при отсутствии/пустоте возвращает def.
func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getEnvBool читает булеву переменную окружения; при отсутствии — def.
func getEnvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on", "да":
		return true
	default:
		return false
	}
}
