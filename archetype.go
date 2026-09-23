package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// ============================================================================
// Файл: archetype.go
// Описание: Определение архетипа персонажа через бесплатный облачный API
// Pollinations.ai (OpenAI-совместимый формат, без токенов).
//
// Промпт просит вернуть только строку вида «ИмяАрхетипа (описание на русском)»,
// например: "Tsundere (холодна снаружи, внутри добрая)".
//
// Если био пустое либо сервер недоступен (ошибка/таймаут 5с/не-200) — функция
// возвращает "Не определён". Это опциональная функция: её можно отключить
// через ARCHETYPE_ENABLED в .env.
// ============================================================================

const (
	// archetypeEndpoint — адрес API Pollinations.ai (метод POST, формат OpenAI).
	// Примечание: корень pollinations.ai на POST отдаёт 405, поэтому используем
	// рабочий chat-эндпоинт text.pollinations.ai/openai.
	archetypeEndpoint = "https://text.pollinations.ai/openai"

	// archetypeTimeout — таймаут запроса к API (сек).
	archetypeTimeout = 5 * time.Second

	// archetypeModel — имя модели в OpenAI-формате.
	archetypeModel = "openai"

	// archetypeUnknown — значение, когда архетип не удалось определить.
	archetypeUnknown = "Не определён"
)

// archetypePromptPre — фиксированная часть промпта; в конце дописывается био.
const archetypePromptPre = "Analyze this anime bio and find personality archetype (Tsundere, Yandere, etc.). Respond ONLY in format: 'Archetype (3-word description in Russian)'. No extra text. Bio: "

// archetypeHTTPClient — клиент с таймаутом 5с. Безопасен для параллельных вызовов.
var archetypeHTTPClient = &http.Client{Timeout: archetypeTimeout}

// archetypeRequest — тело запроса в OpenAI-совместимом формате.
type archetypeRequest struct {
	Model    string             `json:"model"`
	Messages []archetypeMessage `json:"messages"`
}

type archetypeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// archetypeResponse — ответ Pollinations.ai.
type archetypeResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// DetectArchetype определяет архетип персонажа по биографии через Pollinations.ai.
// Возвращает «ИмяАрхетипа (описание на русском)» либо archetypeUnknown.
func DetectArchetype(about string) string {
	// Пустая биография или плейсхолдер отсутствия описания — нечего анализировать.
	about = strings.TrimSpace(about)
	if about == "" || about == descriptionAbsent {
		return archetypeUnknown
	}

	payload := archetypeRequest{
		Model: archetypeModel,
		Messages: []archetypeMessage{
			{Role: "user", Content: archetypePromptPre + about},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return archetypeUnknown
	}

	req, err := http.NewRequest(http.MethodPost, archetypeEndpoint, bytes.NewReader(body))
	if err != nil {
		return archetypeUnknown
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "anime-scanner-bot/1.0")

	resp, err := archetypeHTTPClient.Do(req)
	if err != nil {
		return archetypeUnknown
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return archetypeUnknown
	}

	var out archetypeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return archetypeUnknown
	}
	if len(out.Choices) == 0 {
		return archetypeUnknown
	}

	// Схлопываем переносы/лишние пробелы: модель может вернуть текст в несколько строк.
	text := strings.Join(strings.Fields(out.Choices[0].Message.Content), " ")
	if text == "" {
		return archetypeUnknown
	}
	return text
}

// archetypeLabel приводит строку вида "Tsundere (описание на русском)"
// к формату "Tsundere — описание на русском" для карточки персонажа.
// Если скобок в ответе нет — возвращает ответ как есть.
func archetypeLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == archetypeUnknown {
		return s
	}

	i := strings.IndexByte(s, '(')
	if i <= 0 || !strings.HasSuffix(s, ")") {
		return s
	}

	name := strings.TrimSpace(s[:i])
	desc := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s[i+1:]), ")"))
	if name != "" && desc != "" {
		return name + " — " + desc
	}
	return s
}
