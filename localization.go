package main

import "strings"

// ============================================================================
// Файл: localization.go
// Описание: Локализация ответов бота на русский язык.
//
// Формат вывода (соответствует ТЗ):
//   - Имя персонажа: «Русское имя (Оригинал)»;
//   - Архетип:       «Shy — застенчивый и скромный персонаж»;
//   - Аниме:         переведённые названия (сначала словарь «где принято»);
//   - Описание:      перевод биографии персонажа.
//
// Если перевод недоступен (сеть/лимит) — подставляется оригинал (fallback),
// бот продолжает работать без локализации.
// ============================================================================

// knownTitleRu — словарь устоявшихся русских названий аниме («где принято»).
// Проверяется ДО запроса к API перевода. Расширяется при необходимости.
var knownTitleRu = map[string]string{
	"bocchi the rock!":       "Одинокий рокер!",
	"slow loop":              "Медленная петля",
	"k-on!":                  "K-ON!",
	"one piece":              "Ван-Пис",
	"naruto":                 "Наруто",
	"death note":             "Тетрадь смерти",
	"attack on titan":        "Атака титанов",
	"steins;gate":            "Врата Штейна",
	"steins;gate 0":          "Врата Штейна 0",
	"tokyo ghoul":            "Токийский гуль",
	"kimetsu no yaiba":       "Клинок, рассекающий демонов",
	"jujutsu kaisen":         "Магическая битва",
	"kimi no na wa":          "Твоё имя",
	"fullmetal alchemist":    "Стальной алхимик",
	"boku no hero academia":  "Моя геройская академия",
	"shingeki no kyojin":     "Атака титанов",
	"sword art online":       "Мастера меча онлайн",
	"hyouka":                 "Хьёка",
	"kobayashis dragon maid": "Дракон горничной Кобаяши",
	"wataten":                "Сказочная гавань Аманэ!",
}

// knownTitleOf возвращает словарный перевод названия, если он задан.
func knownTitleOf(title string) (string, bool) {
	if v, ok := knownTitleRu[strings.ToLower(strings.TrimSpace(title))]; ok {
		return v, true
	}
	return "", false
}

// descriptionAbsent — плейсхолдер отсутствующего описания (не переводится).
const descriptionAbsent = "Описание отсутствует"

// localizeTitle переводит название аниме на русский.
func (b *Bot) localizeTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return title
	}
	if ru, ok := knownTitleOf(title); ok {
		return ru
	}
	if ru, err := b.tr.ToRussian(title); err == nil && ru != "" {
		return ru
	}
	return title
}

// localizeName возвращает имя персонажа в формате «Русское имя (Оригинал)».
func (b *Bot) localizeName(c *UnifiedCharacter) string {
	if c == nil || c.Name == "" {
		return ""
	}

	ru := c.Name
	if c.NativeName != "" {
		// Для японских имён точнее переводить из кандзи/катаканы.
		if t, err := b.tr.ToRussianFromJA(c.NativeName); err == nil && t != "" {
			ru = t
		}
	} else if t, err := b.tr.ToRussian(c.Name); err == nil && t != "" {
		ru = t
	}

	if ru == c.Name {
		return c.Name
	}
	return ru + " (" + c.Name + ")"
}

// localizeDescription переводит биографию персонажа на русский.
func (b *Bot) localizeDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" || desc == descriptionAbsent {
		return desc
	}
	if ru, err := b.tr.ToRussian(desc); err == nil && ru != "" {
		return ru
	}
	return desc
}
