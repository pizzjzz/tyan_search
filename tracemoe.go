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
// Файл: tracemoe.go
// Описание: HTTP-клиент для API trace.moe.
//
// Сервис trace.moe определяет аниме по скриншоту/кадру. Принимает изображение
// (multipart/form-data) и возвращает список совпадений с указанием тайтла,
// эпизода и таймкода. Это запасной путь распознавания, когда AnimeTrace
// не справился с персонажем.
//
// Требует ли ключ? Нет. Без ключа работает с лимитом ~10 запросов в минуту
// на IP; ключ (опционально) повышает лимиты.
// ============================================================================

// traceMoeEndpoint — адрес API trace.moe поиска по изображению.
const traceMoeEndpoint = "https://api.trace.moe/search"

// TraceMoeClient — клиент trace.moe API.
type TraceMoeClient struct {
	httpClient *http.Client

	// apiKey — ключ API, передаётся в заголовке "x-apikey". Может быть пустым.
	apiKey string
}

// TraceMoeSearchResult — один результат поиска по изображению.
type TraceMoeSearchResult struct {
	// From — время начала совпавшего фрагмента от начала эпизода (секунды).
	From float64 `json:"from"`

	// To — время окончания совпавшего фрагмента (секунды).
	To float64 `json:"to"`

	// Episode — номер эпизода (может быть nil, если не определён).
	Episode *float64 `json:"episode"`

	// Similarity — коэффициент релевантности (0..1). 0.95+ почти гарантия.
	Similarity float64 `json:"similarity"`

	// Title, TitleEnglish, TitleNative — названия найденного аниме.
	Title        string `json:"title"`
	TitleEnglish string `json:"title_english"`
	TitleAnime   string `json:"title_native"`

	// Mal — ID аниме в MyAnimeList (nil, если неизвестен).
	Mal *int `json:"mal"`

	// AniList — ID аниме в AniList (в текущей архитектуре не используется).
	AniList int `json:"anilist"`

	// Image — URL превью совпавшего кадра (для показа пользователю).
	Image string `json:"image"`

	// Video — URL превью видео с совпавшим фрагментом.
	Video string `json:"video"`

	// Filename — имя видеофайла, из которого взят кадр.
	Filename string `json:"filename"`
}

// MalID возвращает MAL ID аниме (0, если неизвестен).
func (r *TraceMoeSearchResult) MalID() int {
	if r.Mal == nil {
		return 0
	}
	return *r.Mal
}

// TraceMoeResponse — обёртка над ответом trace.moe.
type TraceMoeResponse struct {
	FrameCount int                    `json:"frameCount"`
	Error      string                 `json:"error"`
	Result     []TraceMoeSearchResult `json:"result"`
}

// NewTraceMoeClient создаёт клиент trace.moe API с заданным ключом.
func NewTraceMoeClient(apiKey string) *TraceMoeClient {
	return &TraceMoeClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiKey:     apiKey,
	}
}

// SearchByImage отправляет изображение в trace.moe и возвращает наиболее
// релевантное совпадение (первый результат).
func (c *TraceMoeClient) SearchByImage(imageData []byte) (*TraceMoeSearchResult, error) {
	// Формируем multipart/form-data запрос.
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("image", "screenshot.jpg")
	if err != nil {
		return nil, fmt.Errorf("trace.moe: multipart поле: %w", err)
	}
	if _, err := part.Write(imageData); err != nil {
		return nil, fmt.Errorf("trace.moe: запись изображения: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("trace.moe: закрытие writer: %w", err)
	}

	req, err := http.NewRequest("POST", traceMoeEndpoint, body)
	if err != nil {
		return nil, fmt.Errorf("trace.moe: создание запроса: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.apiKey != "" {
		req.Header.Set("x-apikey", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: "tracemoe", Err: err}
		logProviderError("tracemoe", traceMoeEndpoint, 0, apiErr)
		return nil, fmt.Errorf("trace.moe: ошибка отправки запроса: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: "tracemoe", Err: err}
		logProviderError("tracemoe", traceMoeEndpoint, 0, apiErr)
		return nil, fmt.Errorf("trace.moe: чтение ответа: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &ProviderAPIError{
			Provider:   "tracemoe",
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("тело: %s", truncateString(string(responseBody), 200)),
		}
		logProviderError("tracemoe", traceMoeEndpoint, resp.StatusCode, apiErr)
		return nil, fmt.Errorf("trace.moe: статус %d: %s", resp.StatusCode, truncateString(string(responseBody), 200))
	}

	var searchResponse TraceMoeResponse
	if err := json.Unmarshal(responseBody, &searchResponse); err != nil {
		apiErr := &ProviderAPIError{Provider: "tracemoe", Err: fmt.Errorf("парсинг ответа: %w", err)}
		logProviderError("tracemoe", traceMoeEndpoint, 0, apiErr)
		return nil, fmt.Errorf("trace.moe: парсинг JSON: %w", err)
	}
	if searchResponse.Error != "" {
		apiErr := &ProviderAPIError{Provider: "tracemoe", Err: fmt.Errorf("%s", searchResponse.Error)}
		logProviderError("tracemoe", traceMoeEndpoint, 0, apiErr)
		return nil, fmt.Errorf("trace.moe: ошибка: %s", searchResponse.Error)
	}
	if len(searchResponse.Result) == 0 {
		return nil, fmt.Errorf("trace.moe: совпадений не найдено")
	}

	// Первый результат — самый релевантный (trace.moe сортирует по similarity).
	return &searchResponse.Result[0], nil
}
