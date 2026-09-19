package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// ============================================================================
// Файл: animetrace.go
// Описание: HTTP-клиент для AnimeTrace API — нейросети, распознающей
// ПЕРСОНАЖЕЙ аниме по изображению.
//
// Главное отличие от trace.moe: trace.moe находит только аниме (по кадру),
// а AnimeTrace распознаёт непосредственно персонажей в кадре и возвращает
// имя персонажа + название аниме.
//
// Эндпоинт:  POST https://api.animetrace.com/v1/search
// Формат:    multipart/form-data (поле "file")
// Авторизация: не требуется (бесплатно).
// ============================================================================

// animeTraceEndpoint — адрес API AnimeTrace.
const animeTraceEndpoint = "https://api.animetrace.com/v1/search"

// AnimeTraceClient — клиент AnimeTrace API.
type AnimeTraceClient struct {
	httpClient *http.Client
}

// AnimeTraceResponse — обёртка над ответом AnimeTrace API.
type AnimeTraceResponse struct {
	Code    int                `json:"code"`
	Message string             `json:"message"`
	TraceID string             `json:"trace_id"`
	Data    []AnimeTraceResult `json:"data"`
}

// AnimeTraceResult — результат распознавания одного персонажа в кадре.
type AnimeTraceResult struct {
	NotConfident bool                  `json:"not_confident"`
	Character    []AnimeTraceCandidate `json:"character"`
}

// AnimeTraceCandidate — кандидат: имя персонажа и аниме, где он встречается.
type AnimeTraceCandidate struct {
	Work      string `json:"work"`
	Character string `json:"character"`
}

// NewAnimeTraceClient создаёт клиент с HTTP-таймаутом 30 секунд.
func NewAnimeTraceClient() *AnimeTraceClient {
	return &AnimeTraceClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// RecognizeCharacter отправляет изображение в AnimeTrace и возвращает
// имя самого вероятного персонажа и название аниме (обычно на японском).
func (c *AnimeTraceClient) RecognizeCharacter(imageData []byte) (characterName, workTitle string, err error) {
	// Формируем multipart/form-data запрос.
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// is_multi=0 — вернуть только самый вероятный результат.
	if err := writer.WriteField("is_multi", "0"); err != nil {
		return "", "", fmt.Errorf("animetrace: поле is_multi: %w", err)
	}

	part, err := writer.CreateFormFile("file", "image.jpg")
	if err != nil {
		return "", "", fmt.Errorf("animetrace: multipart поле: %w", err)
	}
	if _, err := part.Write(imageData); err != nil {
		return "", "", fmt.Errorf("animetrace: запись изображения: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", "", fmt.Errorf("animetrace: закрытие writer: %w", err)
	}

	req, err := http.NewRequest("POST", animeTraceEndpoint, body)
	if err != nil {
		return "", "", fmt.Errorf("animetrace: создание запроса: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: "animetrace", Err: err}
		logProviderError("animetrace", animeTraceEndpoint, 0, apiErr)
		return "", "", fmt.Errorf("animetrace: ошибка отправки запроса: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: "animetrace", Err: err}
		logProviderError("animetrace", animeTraceEndpoint, 0, apiErr)
		return "", "", fmt.Errorf("animetrace: чтение ответа: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &ProviderAPIError{
			Provider:   "animetrace",
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("тело: %s", truncateString(string(responseBody), 200)),
		}
		logProviderError("animetrace", animeTraceEndpoint, resp.StatusCode, apiErr)
		return "", "", fmt.Errorf("animetrace: статус %d: %s", resp.StatusCode, truncateString(string(responseBody), 200))
	}

	var recognition AnimeTraceResponse
	if err := json.Unmarshal(responseBody, &recognition); err != nil {
		apiErr := &ProviderAPIError{Provider: "animetrace", Err: fmt.Errorf("парсинг ответа: %w", err)}
		logProviderError("animetrace", animeTraceEndpoint, 0, apiErr)
		return "", "", fmt.Errorf("animetrace: парсинг JSON: %w", err)
	}
	if recognition.Code != 0 {
		apiErr := &ProviderAPIError{Provider: "animetrace", Err: fmt.Errorf("код %d (%s)", recognition.Code, recognition.Message)}
		logProviderError("animetrace", animeTraceEndpoint, recognition.Code, apiErr)
		return "", "", fmt.Errorf("animetrace: код %d (%s)", recognition.Code, recognition.Message)
	}
	if len(recognition.Data) == 0 || len(recognition.Data[0].Character) == 0 {
		return "", "", fmt.Errorf("animetrace: персонажи на изображении не обнаружены")
	}

	// Берём самого вероятного кандидата (первый).
	candidate := recognition.Data[0].Character[0]
	return candidate.Character, candidate.Work, nil
}
