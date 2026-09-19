package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ============================================================================
// Файл: jikan.go
// Описание: HTTP-клиент для Jikan API v4 (неофициальный API MyAnimeList).
//
// Jikan — публичный REST API поверх данных MyAnimeList и единственный
// источник данных бота (персонажи, аниме, роли).
//
// Используемые эндпоинты:
//   - GET /v4/characters?q=<имя>&limit=5    — поиск персонажей (кандидаты)
//   - GET /v4/characters/<id>/full          — полная карточка персонажа
//   - GET /v4/anime?q=<название>&limit=1    — поиск аниме
//   - GET /v4/anime/<id>/characters         — персонажи аниме
// ============================================================================

const jikanBaseURL = "https://api.jikan.moe/v4"

// JikanClient — клиент Jikan API, реализующий AnimeProvider.
type JikanClient struct {
	httpClient *http.Client
}

// NewJikanClient создаёт клиент с HTTP-таймаутом 30 секунд.
func NewJikanClient() *JikanClient {
	return &JikanClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name возвращает имя провайдера.
func (j *JikanClient) Name() string { return "jikan" }

// ============================================================================
// JSON-СТРУКТУРЫ ОТВЕТОВ JIKAN
// ============================================================================

// jikanCharacter — персонаж из поиска.
type jikanCharacter struct {
	MalID  int    `json:"mal_id"`
	Name   string `json:"name"`
	NameJP string `json:"name_kanji"`
	Images struct {
		Jpg struct {
			ImageURL string `json:"image_url"`
		} `json:"jpg"`
	} `json:"images"`
}

// jikanAnimeRef — аниме-тайтл в relations-массиве персонажа.
type jikanAnimeRef struct {
	MalID    int    `json:"mal_id"`
	Title    string `json:"title"`
	TitleEng string `json:"title_english"`
	Type     string `json:"type"`
}

// jikanMangaRef — манговый тайтл в relations-массиве персонажа.
type jikanMangaRef struct {
	MalID    int    `json:"mal_id"`
	Title    string `json:"title"`
	TitleEng string `json:"title_english"`
	Type     string `json:"type"`
}

// jikanAnimeRelation — пара «роль + аниме» из массива anime[].
type jikanAnimeRelation struct {
	Role  string        `json:"role"`
	Anime jikanAnimeRef `json:"anime"`
}

// jikanMangaRelation — пара «роль + манга» из массива manga[].
type jikanMangaRelation struct {
	Role  string        `json:"role"`
	Manga jikanMangaRef `json:"manga"`
}

// jikanCharacterFull — полная карточка персонажа (/characters/<id>/full).
type jikanCharacterFull struct {
	MalID  int    `json:"mal_id"`
	Name   string `json:"name"`
	NameJP string `json:"name_kanji"`
	About  string `json:"about"`
	Images struct {
		Jpg struct {
			ImageURL string `json:"image_url"`
		} `json:"jpg"`
	} `json:"images"`
	Anime []jikanAnimeRelation `json:"anime"`
	Manga []jikanMangaRelation `json:"manga"`
}

// jikanAnime — аниме из поиска.
type jikanAnime struct {
	MalID    int    `json:"mal_id"`
	Title    string `json:"title"`
	TitleEng string `json:"title_english"`
	Type     string `json:"type"`
	Status   string `json:"status"`
}

// jikanAnimeCharacterRole — роль персонажа в аниме (/anime/<id>/characters).
type jikanAnimeCharacterRole struct {
	Role      string         `json:"role"`
	Character jikanCharacter `json:"character"`
}

// ============================================================================
// ПОИСК ПЕРСОНАЖА
// ============================================================================

// SearchCharactersByName ищет кандидатов-персонажей через Jikan.
// Возвращает до 5 кандидатов; для каждого последовательно подгружает полную
// карточку (/characters/<id>/full), чтобы в списке сразу были описание,
// архетип и список аниме.
func (j *JikanClient) SearchCharactersByName(name string) ([]*UnifiedCharacter, error) {
	endpoint := fmt.Sprintf("%s/characters?q=%s&limit=5", jikanBaseURL, url.QueryEscape(name))
	body, err := j.doRequest(endpoint)
	if err != nil {
		return nil, err
	}

	var list struct {
		Data []jikanCharacter `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, &ProviderAPIError{Provider: j.Name(), Err: fmt.Errorf("парсинг поиска персонажей: %w", err)}
	}
	if len(list.Data) == 0 {
		return nil, &NotFoundError{Provider: j.Name(), Query: name}
	}

	// Последовательно подгружаем полные карточки кандидатов.
	results := make([]*UnifiedCharacter, 0, len(list.Data))
	for _, c := range list.Data {
		full, err := j.fetchCharacterFull(c.MalID)
		if err == nil && full != nil {
			results = append(results, full)
			continue
		}
		// Фолбэк: базовый кандидат из поиска, чтобы список был виден.
		results = append(results, &UnifiedCharacter{
			Source:     j.Name(),
			ID:         strconv.Itoa(c.MalID),
			Name:       c.Name,
			NativeName: c.NameJP,
			ImageURL:   c.Images.Jpg.ImageURL,
		})
	}

	valid := make([]*UnifiedCharacter, 0, len(results))
	for _, r := range results {
		if r != nil && r.Name != "" {
			valid = append(valid, r)
		}
	}
	if len(valid) == 0 {
		return nil, &NotFoundError{Provider: j.Name(), Query: name}
	}
	return valid, nil
}

// FetchCharacterByID загружает полную карточку персонажа по MAL ID.
func (j *JikanClient) FetchCharacterByID(id string) (*UnifiedCharacter, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, &ProviderAPIError{Provider: j.Name(), Err: fmt.Errorf("некорректный ID персонажа %q: %w", id, err)}
	}
	return j.fetchCharacterFull(n)
}

// fetchCharacterFull получает полную карточку персонажа и собирает её
// в единую структуру: имена, описание, фото, архетип и список аниме.
func (j *JikanClient) fetchCharacterFull(malID int) (*UnifiedCharacter, error) {
	endpoint := fmt.Sprintf("%s/characters/%d/full", jikanBaseURL, malID)
	body, err := j.doRequest(endpoint)
	if err != nil {
		return nil, err
	}

	var full struct {
		Data jikanCharacterFull `json:"data"`
	}
	if err := json.Unmarshal(body, &full); err != nil {
		return nil, &ProviderAPIError{Provider: j.Name(), Err: fmt.Errorf("парсинг карточки персонажа: %w", err)}
	}

	uc := &UnifiedCharacter{
		Source:      j.Name(),
		ID:          strconv.Itoa(full.Data.MalID),
		Name:        full.Data.Name,
		NativeName:  full.Data.NameJP,
		Description: cleanJikanAbout(full.Data.About),
		ImageURL:    full.Data.Images.Jpg.ImageURL,
		Archetype:   DetectArchetype(full.Data.About),
	}
	uc.Animes = jikanRelationTitles(full.Data.Anime, full.Data.Manga)

	return uc, nil
}

// titleRole — пара «роль + название тайтла».
type titleRole struct {
	role  string
	title string
}

// titleRolesFromAnime раскладывает anime-relations в список «роль → название».
func titleRolesFromAnime(anime []jikanAnimeRelation) []titleRole {
	out := make([]titleRole, 0, len(anime))
	for _, rel := range anime {
		if t := firstNonEmpty(rel.Anime.TitleEng, rel.Anime.Title); t != "" {
			out = append(out, titleRole{role: canonicalRole(rel.Role), title: t})
		}
	}
	return out
}

// titleRolesFromManga — то же для manga-relations.
func titleRolesFromManga(manga []jikanMangaRelation) []titleRole {
	out := make([]titleRole, 0, len(manga))
	for _, rel := range manga {
		if t := firstNonEmpty(rel.Manga.TitleEng, rel.Manga.Title); t != "" {
			out = append(out, titleRole{role: canonicalRole(rel.Role), title: t})
		}
	}
	return out
}

// jikanRelationTitles собирает названия тайтлов персонажа из relations-массивов.
// Приоритет: аниме с ролью Main, затем остальные роли; если аниме нет —
// используется манга. Максимум 3 уникальных названия.
func jikanRelationTitles(anime []jikanAnimeRelation, manga []jikanMangaRelation) []string {
	roles := titleRolesFromAnime(anime)
	if len(roles) == 0 {
		roles = titleRolesFromManga(manga)
	}

	add := func(res []string, seen map[string]bool, title string) []string {
		if len(res) >= 3 || seen[title] {
			return res
		}
		seen[title] = true
		return append(res, title)
	}

	seen := map[string]bool{}
	res := make([]string, 0, 3)
	for _, r := range roles { // сначала Main-роли
		if r.role == "Main" {
			res = add(res, seen, r.title)
		}
	}
	for _, r := range roles { // затем остальные
		if r.role != "Main" {
			res = add(res, seen, r.title)
		}
	}
	return res
}

// ============================================================================
// ПОИСК АНИМЕ
// ============================================================================

// SearchAnimeByName ищет аниме по названию через Jikan.
func (j *JikanClient) SearchAnimeByName(name string) (*UnifiedAnime, error) {
	endpoint := fmt.Sprintf("%s/anime?q=%s&limit=1", jikanBaseURL, url.QueryEscape(name))
	body, err := j.doRequest(endpoint)
	if err != nil {
		return nil, err
	}

	var list struct {
		Data []jikanAnime `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, &ProviderAPIError{Provider: j.Name(), Err: fmt.Errorf("парсинг поиска аниме: %w", err)}
	}
	if len(list.Data) == 0 {
		return nil, &NotFoundError{Provider: j.Name(), Query: name}
	}

	a := list.Data[0]
	return &UnifiedAnime{
		Source: j.Name(),
		ID:     strconv.Itoa(a.MalID),
		Title:  firstNonEmpty(a.TitleEng, a.Title),
		Format: a.Type,
		Status: a.Status,
	}, nil
}

// ============================================================================
// ПЕРСОНАЖИ АНИМЕ (для поиска по кадру)
// ============================================================================

// SearchAnimeCharacters возвращает персонажей аниме. Работает по MAL ID;
// если ID неизвестен — ищет аниме по названию.
func (j *JikanClient) SearchAnimeCharacters(ref *AnimeRef) ([]*UnifiedRole, error) {
	if ref.MalID == 0 && ref.Title != "" {
		anime, err := j.SearchAnimeByName(ref.Title)
		if err != nil {
			return nil, err
		}
		id, _ := strconv.Atoi(anime.ID)
		ref.MalID = id
	}
	if ref.MalID == 0 {
		return nil, &NotFoundError{Provider: j.Name(), Query: "аниме не определено"}
	}

	endpoint := fmt.Sprintf("%s/anime/%d/characters", jikanBaseURL, ref.MalID)
	body, err := j.doRequest(endpoint)
	if err != nil {
		return nil, err
	}

	var list struct {
		Data []jikanAnimeCharacterRole `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, &ProviderAPIError{Provider: j.Name(), Err: fmt.Errorf("парсинг персонажей аниме: %w", err)}
	}

	result := make([]*UnifiedRole, 0, 10)
	for _, r := range list.Data {
		if r.Character.Name == "" {
			continue
		}
		char := &UnifiedCharacter{
			Source:   j.Name(),
			ID:       strconv.Itoa(r.Character.MalID),
			Name:     r.Character.Name,
			ImageURL: r.Character.Images.Jpg.ImageURL,
		}
		result = append(result, &UnifiedRole{Character: char, Role: canonicalRole(r.Role)})
		// Ограничиваем список 10 персонажами, чтобы не забивать чат.
		if len(result) >= 10 {
			break
		}
	}
	if len(result) == 0 {
		return nil, &NotFoundError{Provider: j.Name(), Query: "аниме"}
	}
	return result, nil
}

// ============================================================================
// ВСПОМОГАТЕЛЬНОЕ
// ============================================================================

// doRequest выполняет GET-запрос к Jikan с обработкой ошибок.
func (j *JikanClient) doRequest(endpoint string) ([]byte, error) {
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: j.Name(), Err: err}
		logProviderError(j.Name(), endpoint, 0, apiErr)
		return nil, apiErr
	}
	req.Header.Set("Accept", "application/json")

	resp, err := j.httpClient.Do(req)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: j.Name(), Err: err}
		logProviderError(j.Name(), endpoint, 0, apiErr)
		return nil, apiErr
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: j.Name(), Err: err}
		logProviderError(j.Name(), endpoint, 0, apiErr)
		return nil, apiErr
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &ProviderAPIError{
			Provider:   j.Name(),
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("тело: %s", truncateString(string(body), 200)),
		}
		logProviderError(j.Name(), endpoint, resp.StatusCode, apiErr)
		return nil, apiErr
	}
	return body, nil
}

// cleanJikanAbout чистит биографию персонажа (Jikan отдаёт текст MAL).
func cleanJikanAbout(s string) string {
	if strings.TrimSpace(s) == "" {
		return descriptionAbsent
	}
	s = strings.ReplaceAll(s, "\n\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// firstNonEmpty возвращает первую непустую строку.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// canonicalRole приводит роль к единому виду: Main / Supporting / Background.
func canonicalRole(role string) string {
	r := strings.ToLower(role)
	switch {
	case strings.Contains(r, "main"):
		return "Main"
	case strings.Contains(r, "support"):
		return "Supporting"
	default:
		return "Background"
	}
}

// truncateString обрезает строку до N символов (для сообщений об ошибках).
func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
