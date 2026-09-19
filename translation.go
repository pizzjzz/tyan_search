package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Файл: translation.go
// Описание: Перевод текстов на русский язык для локализации ответов бота.
//
// Используется бесплатный публичный эндпоинт Google Translate (без ключей),
// поэтому бот остаётся self-hosted: не нужен платный API-ключ.
//
// Особенности:
//   - Результаты кэшируются в памяти (имена/названия не меняются);
//   - Параллельные запросы ограничены семафором (3), чтобы не перегружать API;
//   - Если перевод недоступен (нет сети/лимит) — возвращается оригинал,
//     бот продолжает работать (fallback).
// ============================================================================

// translateEndpoint — бесплатный (неофициальный) эндпоинт Google Translate.
// Без ключа, доступен публично. Формат: GET ?client=gtx&sl=<src>&tl=ru&q=<text>.
const translateEndpoint = "https://translate.googleapis.com/translate_a/single"

// translationLang — целевой язык всех переводов.
const translationLang = "ru"

// translationMaxConcurrent — сколько запросов к API перевода выполняется одновременно.
const translationMaxConcurrent = 3

// Translator — клиент перевода на русский с кэшем и ограничением параллелизма.
type Translator struct {
	httpClient *http.Client

	cacheMu sync.RWMutex
	cache   map[string]string

	sem chan struct{}
}

// NewTranslator создаёт клиент перевода.
func NewTranslator() *Translator {
	return &Translator{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		cache:      make(map[string]string),
		sem:        make(chan struct{}, translationMaxConcurrent),
	}
}

// ToRussian переводит текст на русский с автодетекцией исходного языка.
func (t *Translator) ToRussian(text string) (string, error) {
	return t.translate(text, "auto")
}

// ToRussianFromJA переводит текст с японского на русский.
// Используется для имён персонажей: точнее транслитерировать из кандзи/катаканы.
func (t *Translator) ToRussianFromJA(text string) (string, error) {
	return t.translate(text, "ja")
}

// translate выполняет перевод с указанием исходного языка.
func (t *Translator) translate(text, srcLang string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	key := srcLang + "|" + text

	// Быстрый путь: текст уже переводился.
	t.cacheMu.RLock()
	if v, ok := t.cache[key]; ok {
		t.cacheMu.RUnlock()
		return v, nil
	}
	t.cacheMu.RUnlock()

	// Ограничиваем параллелизм внешних запросов.
	t.sem <- struct{}{}
	defer func() { <-t.sem }()

	// Повторная проверка: другой вызов мог успеть закэшировать результат.
	t.cacheMu.RLock()
	if v, ok := t.cache[key]; ok {
		t.cacheMu.RUnlock()
		return v, nil
	}
	t.cacheMu.RUnlock()

	params := url.Values{
		"client": {"gtx"},
		"sl":     {srcLang},
		"tl":     {translationLang},
		"dt":     {"t"},
		"q":      {text},
	}
	req, err := http.NewRequest("GET", translateEndpoint+"?"+params.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("translate: создание запроса: %w", err)
	}
	req.Header.Set("User-Agent", "anime-scanner-bot/1.0")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("translate: запрос: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("translate: чтение ответа: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("translate: статус %d", resp.StatusCode)
	}

	// Ответ Google: [[["перевод","оригинал",null,null,5],...], null, "en", ...]
	// Каждый сегмент — массив смешанных типов; перевод всегда первый элемент.
	var envelope []json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("translate: парсинг ответа: %w", err)
	}
	if len(envelope) == 0 {
		return "", fmt.Errorf("translate: пустой ответ")
	}
	var segments [][]any
	if err := json.Unmarshal(envelope[0], &segments); err != nil {
		return "", fmt.Errorf("translate: парсинг сегментов: %w", err)
	}

	var sb strings.Builder
	for _, seg := range segments {
		if len(seg) > 0 {
			if s, ok := seg[0].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	translated := strings.TrimSpace(sb.String())
	if translated == "" {
		return "", fmt.Errorf("translate: пустой перевод")
	}

	t.cacheMu.Lock()
	t.cache[key] = translated
	t.cacheMu.Unlock()
	return translated, nil
}
