package main

import "strings"

// ============================================================================
// Файл: archetype.go
// Описание: Определение архетипа персонажа по его биографии.
//
// Jikan (MyAnimeList) не отдаёт структурных тегов характера, поэтому архетип
// детектится по ключевым словам в тексте биографии "about" (приходит на
// русском либо английском). Это опциональная функция — её можно отключить
// через ARCHETYPE_ENABLED в .env.
// ============================================================================

// archetypeDef — одно определение архетипа: ключ (имя), слова-маркеры
// из биографии и краткое описание на русском для ответа бота.
type archetypeDef struct {
	key      string
	keywords []string
	ru       string
}

var archetypeDefs = []archetypeDef{
	{"Tsundere", []string{"цундере", "цундерэ", "тсундере", "tsundere", "притворно холодна"}, "холодна снаружи, но мягкая и заботливая внутри"},
	{"Kuudere", []string{"кудере", "куудере", "kuudere", "хладнокровн", "бесстрастн", "рассудочн"}, "эмоционально сдержана, кажется холодной и невозмутимой"},
	{"Dandere", []string{"дандере", "дандэрэ", "dandere", "застенчива", "молчалив", "тих"}, "тихая и застенчивая, раскрывается только с близкими"},
	{"Yandere", []string{"яндере", "яндэрэ", "yandere", "одержим", "маниакальн"}, "нездорово одержима объектом симпатии и агрессивна к соперникам"},
	{"Genki", []string{"генки", "genki", "энергичн", "жизнерадостн", "весёл", "боев"}, "энергичная и жизнерадостная, всегда в движении"},
	{"Kamidere", []string{"камидере", "камидерэ", "kamidere", "божественн", "высокомерн", "преисполнен"}, "ведёт себя как божество, высокомерна и надменна"},
	{"Bakadere", []string{"бакадере", "bakadere", "глуповат", "рассеянн", "clumsy"}, "простодушная и неуклюжая, часто глупит"},
	{"Ojou", []string{"одзё", "оджо", "ojou", "аристократ", "высокородн", "из богатой семьи"}, "воспитанная девушка из богатой (аристократической) семьи"},
	{"Delinquent", []string{"делинквент", "delinquent", "хулиган", "правонарушитель", "punk"}, "хулиган или правонарушитель"},
	{"Tomboy", []string{"tomboy", "сорванец", "пацанка"}, "девушка с мальчишескими повадками"},
	{"Childhood Friend", []string{"друг детства", "подруга детства", "childhood friend"}, "друг или подруга детства главного героя"},
	{"Shy", []string{"застенчив", "стеснител", "скромн", "shy", "quiet"}, "застенчивый и скромный персонаж"},
}

// archetypeByKey — быстрый поиск русского описания по имени архетипа.
var archetypeByKey = func() map[string]string {
	m := make(map[string]string, len(archetypeDefs))
	for _, d := range archetypeDefs {
		m[d.key] = d.ru
	}
	return m
}()

// archetypeUnknown — значение, когда архетип не удалось определить.
const archetypeUnknown = "Не определён"

// DetectArchetype ищет в биографии персонажа маркеры архетипов
// и возвращает имя архетипа либо archetypeUnknown.
func DetectArchetype(about string) string {
	if about == "" {
		return archetypeUnknown
	}
	low := strings.ToLower(about)
	for _, d := range archetypeDefs {
		for _, kw := range d.keywords {
			if strings.Contains(low, kw) {
				return d.key
			}
		}
	}
	return archetypeUnknown
}

// archetypeLabel возвращает строку вида "Tsundere — краткое описание"
// для показа в карточке персонажа.
func archetypeLabel(name string) string {
	if ru, ok := archetypeByKey[name]; ok {
		return name + " — " + ru
	}
	return name
}
