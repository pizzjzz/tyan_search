package main

import (
	"fmt"
	"strings"
)

// ============================================================================
// Файл: provider.go
// Описание: Единые типы данных, контракт AnimeProvider и агрегатор с фолбэком.
//
// Все результаты (персонажи, аниме, роли) приводятся к унифицированным типам
// ниже, поэтому остальной код не зависит от конкретного API.
// Сейчас единственный источник данных — Jikan (MyAnimeList), но интерфейс
// и фолбэк позволяют при необходимости легко добавить другие источники.
// ============================================================================

// UnifiedCharacter — персонаж из любого источника.
type UnifiedCharacter struct {
	// Source — имя источника (например, "jikan").
	Source string

	// ID — идентификатор персонажа в системе источника.
	ID string

	// Name — отображаемое имя (латиница/ромадзи).
	Name string

	// NativeName — имя на языке оригинала (обычно кандзи).
	NativeName string

	// Description — биография персонажа (очищенный текст).
	Description string

	// ImageURL — ссылка на изображение персонажа.
	ImageURL string

	// Archetype — архетип характера ("Tsundere", "Kuudere", ...).
	Archetype string

	// Animes — названия тайтлов, где встречается персонаж (до 3).
	Animes []string
}

// UnifiedAnime — аниме из любого источника.
type UnifiedAnime struct {
	Source string
	ID     string
	Title  string
	Format string
	Status string
}

// UnifiedRole — роль персонажа в конкретном аниме.
type UnifiedRole struct {
	Character *UnifiedCharacter
	Role      string
}

// AnimeRef — описание аниме для поиска его персонажей.
// trace.moe отдаёт и MAL ID, и AniList ID; провайдеры используют доступные
// им идентификаторы либо ищут по названию.
type AnimeRef struct {
	// AniListID — ID аниме в AniList (0, если неизвестен).
	AniListID int

	// MalID — ID аниме в MyAnimeList (0, если неизвестен).
	MalID int

	// Title — название аниме (запасной путь поиска, если ID неизвестен).
	Title string
}

// AnimeProvider — контракт, который реализует каждый источник данных.
type AnimeProvider interface {
	// Name возвращает имя провайдера ("jikan").
	Name() string

	// SearchCharactersByName ищет персонажей по имени и возвращает несколько
	// кандидатов (обычно до 5) — для disambiguation при неоднозначном поиске.
	SearchCharactersByName(name string) ([]*UnifiedCharacter, error)

	// FetchCharacterByID загружает полную карточку персонажа по его ID
	// в системе источника. Используется после выбора пользователя.
	FetchCharacterByID(id string) (*UnifiedCharacter, error)

	// SearchAnimeByName ищет аниме по названию.
	SearchAnimeByName(name string) (*UnifiedAnime, error)

	// SearchAnimeCharacters возвращает персонажей аниме, описанного в ref.
	SearchAnimeCharacters(ref *AnimeRef) ([]*UnifiedRole, error)
}

// ============================================================================
// ОШИБКИ ПРОВАЙДЕРОВ
// ============================================================================

// ProviderAPIError — технический сбой провайдера (сеть, статус != 200 и т.п.).
// Именно такие ошибки запускают фолбэк на следующий источник.
type ProviderAPIError struct {
	Provider   string
	StatusCode int
	Err        error
}

func (e *ProviderAPIError) Error() string {
	switch {
	case e.StatusCode != 0:
		return fmt.Sprintf("%s вернул статус %d", e.Provider, e.StatusCode)
	case e.Err != nil:
		return fmt.Sprintf("%s: %v", e.Provider, e.Err)
	default:
		return fmt.Sprintf("%s: неизвестная ошибка", e.Provider)
	}
}

// Unwrap позволяет использовать errors.Is / errors.As.
func (e *ProviderAPIError) Unwrap() error { return e.Err }

// NotFoundError — «не найдено»: это не сбой, просто данных нет.
type NotFoundError struct {
	Provider string
	Query    string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%q не найдено в %s", e.Query, e.Provider)
}

// ============================================================================
// FAILOVER SEARCH
// ============================================================================

// FailoverSearch — поисковый агрегатор с фолбэком: перебирает провайдеров
// в порядке приоритета, пока один из них не вернёт успешный результат.
type FailoverSearch struct {
	providers []AnimeProvider
}

// NewFailoverSearch создаёт агрегатор. Порядок аргументов = приоритет.
func NewFailoverSearch(providers ...AnimeProvider) *FailoverSearch {
	return &FailoverSearch{providers: providers}
}

// providerNames возвращает имена провайдеров через запятую (для логов).
func (f *FailoverSearch) providerNames() string {
	names := make([]string, 0, len(f.providers))
	for _, p := range f.providers {
		names = append(names, p.Name())
	}
	return strings.Join(names, ", ")
}

// SearchCharacterByName возвращает первого найденного кандидата-персонажа.
func (f *FailoverSearch) SearchCharacterByName(name string) (*UnifiedCharacter, error) {
	candidates, err := f.SearchCharactersByName(name)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, &NotFoundError{Provider: f.providerNames(), Query: name}
	}
	return candidates[0], nil
}

// SearchCharactersByName ищет всех кандидатов-персонажей у провайдеров.
func (f *FailoverSearch) SearchCharactersByName(name string) ([]*UnifiedCharacter, error) {
	return sliceWithFallback(f, func(p AnimeProvider) ([]*UnifiedCharacter, error) {
		return p.SearchCharactersByName(name)
	})
}

// FetchCharacterByID загружает полную карточку персонажа у нужного провайдера.
func (f *FailoverSearch) FetchCharacterByID(source, id string) (*UnifiedCharacter, error) {
	for _, p := range f.providers {
		if p.Name() == source {
			return p.FetchCharacterByID(id)
		}
	}
	return nil, &NotFoundError{Provider: source, Query: id}
}

// SearchAnimeByName ищет аниме у провайдеров по очереди.
func (f *FailoverSearch) SearchAnimeByName(name string) (*UnifiedAnime, error) {
	return valueWithFallback(f, func(p AnimeProvider) (*UnifiedAnime, error) {
		return p.SearchAnimeByName(name)
	})
}

// SearchAnimeCharacters ищет персонажей аниме у провайдеров по очереди.
func (f *FailoverSearch) SearchAnimeCharacters(ref *AnimeRef) ([]*UnifiedRole, error) {
	return sliceWithFallback(f, func(p AnimeProvider) ([]*UnifiedRole, error) {
		return p.SearchAnimeCharacters(ref)
	})
}

// SearchCharacterSmart ищет персонажа с поддержкой русского ввода.
func (f *FailoverSearch) SearchCharacterSmart(name string) (*UnifiedCharacter, error) {
	candidates, err := f.SearchCharactersSmart(name)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, &NotFoundError{Provider: f.providerNames(), Query: name}
	}
	return candidates[0], nil
}

// SearchCharactersSmart — умный поиск кандидатов с транслитерацией.
// Если запрос на кириллице — пробует варианты ромадзи и берёт первый,
// давший результаты.
func (f *FailoverSearch) SearchCharactersSmart(name string) ([]*UnifiedCharacter, error) {
	if !isCyrillic(name) {
		return f.SearchCharactersByName(name)
	}

	var lastErr error
	for _, v := range RussianToRomajiVariants(name) {
		candidates, err := f.SearchCharactersByName(CapitalizeFirst(v))
		if err == nil && len(candidates) > 0 {
			return candidates, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// SearchAnimeSmart — аналог SearchCharacterSmart для названий аниме.
func (f *FailoverSearch) SearchAnimeSmart(name string) (*UnifiedAnime, error) {
	if !isCyrillic(name) {
		return f.SearchAnimeByName(name)
	}

	var lastErr error
	for _, v := range RussianToRomajiVariants(name) {
		anime, err := f.SearchAnimeByName(CapitalizeFirst(v))
		if err == nil {
			return anime, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// ============================================================================
// ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ ФОЛБЭКА
// ============================================================================

// valueWithFallback перебирает провайдеров для одиночного результата.
func valueWithFallback[T any](f *FailoverSearch, call func(AnimeProvider) (T, error)) (T, error) {
	var zero T
	var lastErr error

	for _, p := range f.providers {
		res, err := call(p)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return zero, lastErr
}

// sliceWithFallback — то же для срезов: берётся первый провайдер,
// вернувший непустой результат.
func sliceWithFallback[T any](f *FailoverSearch, call func(AnimeProvider) ([]T, error)) ([]T, error) {
	var lastErr error

	for _, p := range f.providers {
		res, err := call(p)
		if err == nil && len(res) > 0 {
			return res, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &NotFoundError{Provider: f.providerNames(), Query: "данные не найдены"}
}
