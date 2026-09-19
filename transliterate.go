package main

import (
	"strings"
	"unicode"
)

// ============================================================================
// Файл: transliterate.go
// Описание: Транслитерация русского текста в ромадзи (латиницу) для поиска
// в Jikan. Пользователь вводит имя по-русски ("Май Сакурадзима"), а Jikan
// хранит имена в латинице/ромадзи ("Sakurajima Mai").
//
// Русская транскрипция японского неоднозначна:
//   - "дзи" — "ji" либо "dzi"
//   - "си" — "shi" либо "si"
//   - "ти" — "chi" либо "ti"
//   - "цу" — "tsu" либо "tu"
//
// Поэтому функция генерирует НЕСКОЛЬКО вариантов написания, и поиск
// перебирает их, пока не найдёт совпадение.
// ============================================================================

// baseRomaji — прямое соответствие «русская буква → латинская».
// Буквы "ь"/"ъ" обрабатываются отдельно (палатализация / без звука).
var baseRomaji = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d",
	'е': "e", 'ё': "yo", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n",
	'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch",
	'ш': "sh", 'щ': "sh", 'ы': "y", 'э': "e", 'ю': "yu", 'я': "ya",
}

// ambiguousSyllables — слоги с несколькими корректными написаниями в ромадзи.
// Первый вариант — самый распространённый (система Хэпбёрна).
var ambiguousSyllables = map[string][]string{
	"дзи": {"ji", "dzi", "zi"},
	"джа": {"ja", "dja"},
	"дже": {"je", "dje"},
	"джё": {"jo", "djo"},
	"джо": {"jo", "djo"},
	"джу": {"ju", "dju"},

	"си": {"shi", "si"},
	"сё": {"sho", "syo"},
	"сю": {"shu", "syu"},

	"ти": {"chi", "ti"},
	"тё": {"cho", "tyo"},
	"тя": {"cha", "tya"},
	"тю": {"chu", "tyu"},

	"цу": {"tsu", "tu"},

	"дзя": {"ja", "dza"},
	"дзё": {"jo", "djo"},
	"дзу": {"zu", "dzu"},

	"жю": {"ju", "jju"},

	"ай": {"ai", "ay"},
	"ей": {"ei", "ey"},
	"ой": {"oi", "oy"},
	"уй": {"ui", "uy"},
	"ый": {"i", "y", "iy"},
	"ий": {"i", "ii", "iy"},
}

// isCyrillic проверяет, есть ли в строке кириллическая буква.
func isCyrillic(s string) bool {
	for _, r := range s {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

// readSyllable считывает один слог (1–3 буквы) из строки рун с позиции i.
// Сначала проверяются более длинные сочетания из ambiguousSyllables,
// затем мягкие знаки и одиночные буквы из baseRomaji.
func readSyllable(runes []rune, i int) ([]string, int) {
	// Сначала ищем дву-/трёхбуквенные сочетания в ambiguousSyllables.
	for l := 3; l >= 2; l-- {
		if i+l <= len(runes) {
			if variants, ok := ambiguousSyllables[string(runes[i:i+l])]; ok {
				return variants, i + l
			}
		}
	}

	// Мягкий/твёрдый знаки звука не дают — пропускаем.
	if i < len(runes) && (runes[i] == 'ь' || runes[i] == 'ъ') {
		return []string{""}, i + 1
	}

	// Одна буква из базового словаря.
	if i < len(runes) {
		if romaji, ok := baseRomaji[runes[i]]; ok {
			return []string{romaji}, i + 1
		}
	}

	// Незнакомая буква (например, латиница) — оставляем как есть.
	return []string{string(runes[i])}, i + 1
}

// generateVariants рекурсивно перебирает комбинации вариантов слогов
// и собирает полные строки-кандидаты. Количество ограничено maxVariants.
func generateVariants(syllVars [][]string, maxVariants int) []string {
	result := []string{}
	unique := map[string]bool{}

	var build func(pos int, acc string)
	build = func(pos int, acc string) {
		if len(result) >= maxVariants {
			return
		}
		if pos == len(syllVars) {
			if !unique[acc] {
				unique[acc] = true
				result = append(result, acc)
			}
			return
		}
		for _, v := range syllVars[pos] {
			build(pos+1, acc+v)
		}
	}

	build(0, "")
	return result
}

// RussianToRomajiVariants транслитерирует русский текст в массив вариантов
// ромадзи. Возвращает как минимум один вариант. Если кириллицы нет —
// возвращает саму строку.
func RussianToRomajiVariants(input string) []string {
	if !isCyrillic(input) {
		return []string{strings.TrimSpace(input)}
	}

	lower := strings.ToLower(strings.TrimSpace(input))
	runes := []rune(lower)

	syllVars := [][]string{}
	for i := 0; i < len(runes); {
		variants, next := readSyllable(runes, i)
		syllVars = append(syllVars, variants)
		i = next
	}

	variants := generateVariants(syllVars, 16)
	if len(variants) == 0 {
		return []string{lower}
	}
	return variants
}

// CapitalizeFirst делает заглавной первую букву строки.
// Например: "mai sakurajima" -> "Mai sakurajima".
func CapitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
