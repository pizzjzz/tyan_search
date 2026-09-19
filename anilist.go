package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ============================================================================
// Файл: anilist.go
// Описание: HTTP-клиент для AniList GraphQL API.
//
// AniList — стабильная публичная база аниме и персонажей. Работает БЕЗ
// API-ключей (публичный GraphQL https://graphql.anilist.co).
//
// Это основной провайдер бота (надёжнее Jikan/MyAnimeList, который часто
// отдаёт 504 из-за своих проблем). Jikan остаётся как запасной источник.
//
// Используемые запросы:
//   - Page.characters(search:) — кандидаты-персонажи (с топ-тайтлами)
//   - Character(id:)           — полная карточка персонажа (описание, роли)
//   - Media(search:, type:)    — поиск аниме
//   - Media(id:/idMal:/search:) characters — персонажи конкретного аниме
// ============================================================================

// anilistEndpoint — адрес GraphQL API AniList.
const anilistEndpoint = "https://graphql.anilist.co"

// Query-шаблоны (передаются как строка в GraphQL).
const (
	gqlCharSearch = `
query ($search: String) {
  Page(perPage: 5) {
    characters(search: $search) {
      id
      name { full native }
      image { large }
      media(sort: POPULARITY_DESC, perPage: 3) {
        nodes { title { romaji english } }
      }
    }
  }
}`

	gqlCharFull = `
query ($id: Int) {
  Character(id: $id) {
    id
    name { full native }
    image { large }
    description(asHtml: false)
    media(sort: POPULARITY_DESC, perPage: 5) {
      nodes { title { romaji english } }
    }
  }
}`

	gqlAnimeSearch = `
query ($search: String) {
  Media(search: $search, type: ANIME) {
    id
    title { romaji english native }
    format
    status
  }
}`

	// Три варианта запроса персонажей аниме — по AniList ID, MAL ID
	// или названию. Разные шаблоны нужны, потому что передача неиспользуемых
	// аргументов (id/idMal/search со значением null) ломает поиск AniList.
	gqlAnimeCharsByID = `
query ($id: Int) {
  Media(id: $id, type: ANIME) {
    id
    characters(sort: [ROLE, RELEVANCE], perPage: 10) {
      edges { role node { id name { full native } image { large } } }
    }
  }
}`

	gqlAnimeCharsByMal = `
query ($idMal: Int) {
  Media(idMal: $idMal, type: ANIME) {
    id
    characters(sort: [ROLE, RELEVANCE], perPage: 10) {
      edges { role node { id name { full native } image { large } } }
    }
  }
}`

	gqlAnimeCharsByTitle = `
query ($search: String) {
  Media(search: $search, type: ANIME) {
    id
    characters(sort: [ROLE, RELEVANCE], perPage: 10) {
      edges { role node { id name { full native } image { large } } }
    }
  }
}`
)

// AniListClient — клиент AniList API, реализующий AnimeProvider.
type AniListClient struct {
	httpClient *http.Client
}

// NewAniListClient создаёт клиент с HTTP-таймаутом 30 секунд.
func NewAniListClient() *AniListClient {
	return &AniListClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name возвращает имя провайдера.
func (a *AniListClient) Name() string { return "anilist" }

// ============================================================================
// JSON-СТРУКТУРЫ ОТВЕТОВ ANILIST
// ============================================================================

// alError — одна ошибка GraphQL.
type alError struct {
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// alResponse — обёртка над ответом GraphQL.
type alResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []alError       `json:"errors"`
}

// alName — имя персонажа/тайтла на разных языках.
type alName struct {
	Full   string `json:"full"`
	Native string `json:"native"`
}

// alTitle — название тайтла на разных языках.
type alTitle struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

// alMediaNode — тайтл внутри media-коннекции персонажа.
type alMediaNode struct {
	ID    int     `json:"id"`
	Title alTitle `json:"title"`
}

// ============================================================================
// СЛУЖЕБНЫЕ МЕТОДЫ
// ============================================================================

// gql выполняет GraphQL-запрос и возвращает тело блока "data".
func (a *AniListClient) gql(query string, variables map[string]any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, &ProviderAPIError{Provider: a.Name(), Err: err}
	}

	req, err := http.NewRequest("POST", anilistEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, &ProviderAPIError{Provider: a.Name(), Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "anime-scanner-bot/1.0")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: a.Name(), Err: err}
		logProviderError(a.Name(), anilistEndpoint, 0, apiErr)
		return nil, apiErr
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		apiErr := &ProviderAPIError{Provider: a.Name(), Err: err}
		logProviderError(a.Name(), anilistEndpoint, 0, apiErr)
		return nil, apiErr
	}

	var envelope alResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		apiErr := &ProviderAPIError{Provider: a.Name(), Err: fmt.Errorf("парсинг ответа GraphQL: %w", err)}
		logProviderError(a.Name(), anilistEndpoint, 0, apiErr)
		return nil, apiErr
	}

	if resp.StatusCode != http.StatusOK {
		msg := "GraphQL-запрос не выполнен"
		if len(envelope.Errors) > 0 {
			msg = firstNonEmpty(envelope.Errors[0].Message, msg)
		}
		apiErr := &ProviderAPIError{
			Provider:   a.Name(),
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("тело: %s", truncateString(msg, 200)),
		}
		logProviderError(a.Name(), anilistEndpoint, resp.StatusCode, apiErr)
		return nil, apiErr
	}
	if len(envelope.Errors) > 0 {
		code := envelope.Errors[0].Status
		if code == 0 {
			code = resp.StatusCode
		}
		apiErr := &ProviderAPIError{Provider: a.Name(), StatusCode: code, Err: fmt.Errorf("%s", envelope.Errors[0].Message)}
		logProviderError(a.Name(), anilistEndpoint, code, apiErr)
		return nil, apiErr
	}
	return envelope.Data, nil
}

// ============================================================================
// ПОИСК ПЕРСОНАЖА
// ============================================================================

// SearchCharactersByName ищет кандидатов-персонажей через AniList.
// В одном запросе для каждого кандидата подтягиваются топ-тайтлы,
// чтобы список для выбора был информативным.
func (a *AniListClient) SearchCharactersByName(name string) ([]*UnifiedCharacter, error) {
	data, err := a.gql(gqlCharSearch, map[string]any{"search": name})
	if err != nil {
		return nil, err
	}

	var out struct {
		Page struct {
			Characters []struct {
				ID    int    `json:"id"`
				Name  alName `json:"name"`
				Image struct {
					Large string `json:"large"`
				} `json:"image"`
				Media struct {
					Nodes []alMediaNode `json:"nodes"`
				} `json:"media"`
			} `json:"characters"`
		} `json:"Page"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &ProviderAPIError{Provider: a.Name(), Err: fmt.Errorf("парсинг поиска персонажей: %w", err)}
	}
	if len(out.Page.Characters) == 0 {
		return nil, &NotFoundError{Provider: a.Name(), Query: name}
	}

	results := make([]*UnifiedCharacter, 0, len(out.Page.Characters))
	for _, c := range out.Page.Characters {
		char := &UnifiedCharacter{
			Source:     a.Name(),
			ID:         strconv.Itoa(c.ID),
			Name:       firstNonEmpty(c.Name.Full, c.Name.Native),
			NativeName: c.Name.Native,
			ImageURL:   c.Image.Large,
		}
		for _, n := range c.Media.Nodes {
			if title := firstNonEmpty(n.Title.English, n.Title.Romaji); title != "" && !containsString(char.Animes, title) {
				char.Animes = append(char.Animes, title)
			}
			if len(char.Animes) >= 3 {
				break
			}
		}
		results = append(results, char)
	}
	return results, nil
}

// FetchCharacterByID загружает полную карточку персонажа по AniList ID:
// описание, архетип, список аниме (сначала главные роли).
func (a *AniListClient) FetchCharacterByID(id string) (*UnifiedCharacter, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, &ProviderAPIError{Provider: a.Name(), Err: fmt.Errorf("некорректный ID персонажа %q: %w", id, err)}
	}

	data, err := a.gql(gqlCharFull, map[string]any{"id": n})
	if err != nil {
		return nil, err
	}

	var out struct {
		Character struct {
			ID          int    `json:"id"`
			Name        alName `json:"name"`
			Description string `json:"description"`
			Image       struct {
				Large string `json:"large"`
			} `json:"image"`
			Media struct {
				Nodes []alMediaNode `json:"nodes"`
			} `json:"media"`
		} `json:"Character"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &ProviderAPIError{Provider: a.Name(), Err: fmt.Errorf("парсинг карточки персонажа: %w", err)}
	}
	if out.Character.ID == 0 {
		return nil, &NotFoundError{Provider: a.Name(), Query: id}
	}

	desc := cleanAniListDesc(out.Character.Description)
	char := &UnifiedCharacter{
		Source:      a.Name(),
		ID:          strconv.Itoa(out.Character.ID),
		Name:        firstNonEmpty(out.Character.Name.Full, out.Character.Name.Native),
		NativeName:  out.Character.Name.Native,
		Description: desc,
		ImageURL:    out.Character.Image.Large,
		Archetype:   DetectArchetype(desc),
	}

	// Тайтлы отсортированы по популярности — берём до 3 уникальных.
	for _, n := range out.Character.Media.Nodes {
		t := firstNonEmpty(n.Title.English, n.Title.Romaji)
		if t == "" || containsString(char.Animes, t) {
			continue
		}
		char.Animes = append(char.Animes, t)
		if len(char.Animes) >= 3 {
			break
		}
	}
	return char, nil
}

// ============================================================================
// ПОИСК АНИМЕ
// ============================================================================

// SearchAnimeByName ищет аниме по названию через AniList.
func (a *AniListClient) SearchAnimeByName(name string) (*UnifiedAnime, error) {
	data, err := a.gql(gqlAnimeSearch, map[string]any{"search": name})
	if err != nil {
		return nil, err
	}

	var out struct {
		Media struct {
			ID     int     `json:"id"`
			Title  alTitle `json:"title"`
			Format string  `json:"format"`
			Status string  `json:"status"`
		} `json:"Media"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &ProviderAPIError{Provider: a.Name(), Err: fmt.Errorf("парсинг поиска аниме: %w", err)}
	}
	if out.Media.ID == 0 {
		return nil, &NotFoundError{Provider: a.Name(), Query: name}
	}

	return &UnifiedAnime{
		Source: a.Name(),
		ID:     strconv.Itoa(out.Media.ID),
		Title:  firstNonEmpty(out.Media.Title.English, out.Media.Title.Romaji, out.Media.Title.Native),
		Format: out.Media.Format,
		Status: out.Media.Status,
	}, nil
}

// ============================================================================
// ПЕРСОНАЖИ АНИМЕ (для поиска по кадру)
// ============================================================================

// SearchAnimeCharacters возвращает персонажей аниме. Приоритет поиска:
// AniList ID > MAL ID (idMal) > название (search).
func (a *AniListClient) SearchAnimeCharacters(ref *AnimeRef) ([]*UnifiedRole, error) {
	var (
		query     string
		variables map[string]any
	)
	switch {
	case ref.AniListID > 0:
		query = gqlAnimeCharsByID
		variables = map[string]any{"id": ref.AniListID}
	case ref.MalID > 0:
		query = gqlAnimeCharsByMal
		variables = map[string]any{"idMal": ref.MalID}
	case ref.Title != "":
		query = gqlAnimeCharsByTitle
		variables = map[string]any{"search": ref.Title}
	default:
		return nil, &NotFoundError{Provider: a.Name(), Query: "аниме не определено"}
	}

	data, err := a.gql(query, variables)
	if err != nil {
		return nil, err
	}

	var out struct {
		Media struct {
			ID         int `json:"id"`
			Characters struct {
				Edges []struct {
					Role string `json:"role"`
					Node struct {
						ID    int    `json:"id"`
						Name  alName `json:"name"`
						Image struct {
							Large string `json:"large"`
						} `json:"image"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"characters"`
		} `json:"Media"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &ProviderAPIError{Provider: a.Name(), Err: fmt.Errorf("парсинг персонажей аниме: %w", err)}
	}
	if out.Media.ID == 0 {
		return nil, &NotFoundError{Provider: a.Name(), Query: ref.Title}
	}

	result := make([]*UnifiedRole, 0, 10)
	for _, e := range out.Media.Characters.Edges {
		if e.Node.Name.Full == "" {
			continue
		}
		char := &UnifiedCharacter{
			Source:   a.Name(),
			ID:       strconv.Itoa(e.Node.ID),
			Name:     firstNonEmpty(e.Node.Name.Full, e.Node.Name.Native),
			ImageURL: e.Node.Image.Large,
		}
		result = append(result, &UnifiedRole{Character: char, Role: canonicalRole(e.Role)})
	}
	if len(result) == 0 {
		return nil, &NotFoundError{Provider: a.Name(), Query: ref.Title}
	}
	return result, nil
}

// ============================================================================
// ВСПОМОГАТЕЛЬНОЕ
// ============================================================================

// mdImageRe — Markdown-изображения вида ![alt](url) в описаниях AniList.
var mdImageRe = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)

// cleanAniListDesc чистит описание персонажа: убирает спойлеры и разметку,
// схлопывает пробелы и ограничивает длину.
func cleanAniListDesc(s string) string {
	if strings.TrimSpace(s) == "" {
		return descriptionAbsent
	}
	s = strings.ReplaceAll(s, "~!", "") // открывающий тег спойлера
	s = strings.ReplaceAll(s, "!~", "") // закрывающий тег спойлера
	s = mdImageRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ") // все пробелы/переносы -> одиночные
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// containsString проверяет наличие строки в срезе.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
