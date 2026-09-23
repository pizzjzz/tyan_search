package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ============================================================================
// Файл: telegram.go
// Описание: Вся логика Telegram-бота: маршрутизация сообщений, команды,
// поиск по картинке и по имени, disambiguation (выбор из кандидатов).
//
// Функциональность (соответствует ТЗ):
//   - Поиск по картинке: определяется персонаж (имя + аниме + архетип).
//   - Поиск по имени: карточка персонажа с фото, тайтлом и архетипом.
//   - Работа в личных и групповых чатах (команды, упоминания, фото).
// ============================================================================

// Псевдонимы режимов разметки, чтобы код был короче.
const (
	markdown = tgbotapi.ModeMarkdown // разметка Markdown v1
	none     = ""
)

// Параметры бота.
const (
	// selectionTTL — время жизни сессии выбора кандидата (disambiguation).
	selectionTTL = 15 * time.Minute

	// imageSearchMinSimilarity — порог similarity для предупреждения
	// о недостоверном совпадении trace.moe.
	imageSearchMinSimilarity = 0.5
)

// Bot — экземпляр Telegram-бота со всеми клиентами и состоянием.
type Bot struct {
	api        *tgbotapi.BotAPI
	cfg        *Config
	search     *FailoverSearch
	animeTrace *AnimeTraceClient
	traceMoe   *TraceMoeClient
	tr         *Translator

	// selections — временные сессии выбора кандидата по чатам (disambiguation).
	selections   map[int64][]*UnifiedCharacter
	selectionAt  map[int64]time.Time
	selectionsMu sync.Mutex
}

// NewBot создаёт бота: проверяет токен, настраивает клиенты и команды меню.
func NewBot(cfg *Config) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		return nil, fmt.Errorf("токен не прошёл проверку в Telegram API: %w", err)
	}

	b := &Bot{
		api: api,
		cfg: cfg,
		search: NewFailoverSearch(
			NewAniListClient(), // основной, надёжный источник (без ключей)
			NewJikanClient(),   // запасной источник (MyAnimeList)
		),
		animeTrace:  NewAnimeTraceClient(),
		traceMoe:    NewTraceMoeClient(cfg.TraceMoeAPIKey),
		tr:          NewTranslator(),
		selections:  make(map[int64][]*UnifiedCharacter),
		selectionAt: make(map[int64]time.Time),
	}

	b.setBotCommands()
	return b, nil
}

// setBotCommands регистрирует список команд в меню Telegram (необязательно).
func (b *Bot) setBotCommands() {
	if _, err := b.api.Send(tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "find", Description: "Найти персонажа (или аниме) по имени"},
		tgbotapi.BotCommand{Command: "search", Description: "Распознать персонажа по скриншоту (ответ на фото)"},
		tgbotapi.BotCommand{Command: "help", Description: "Справка по командам"},
	)); err != nil {
		logger.Warn("telegram_api_error", "error", err.Error())
	}
}

// Run запускает лонг-поллинг и обрабатывает обновления до сигнала на выход
// (Ctrl+C / SIGTERM). Каждое сообщение обрабатывается в своей горутине
// через middleware processUpdate (логирование + защита от паник).
func (b *Bot) Run() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)

	logger.Info("bot_started", "username", b.api.Self.UserName)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case update := <-updates:
			go b.processUpdate(update)
		case <-sig:
			logger.Info("bot_stopping")
			b.api.StopReceivingUpdates()
			return
		}
	}
}

// ============================================================================
// MIDDLEWARE: логирование действий пользователя + защита от паник
// ============================================================================

// processUpdate — middleware-обёртка над handleUpdate:
//  1. логирует каждое действие пользователя на уровне INFO;
//  2. перехватывает паники (recover) и пишет ERROR-лог со стектрейсом,
//     чтобы сбой в обработке одного апдейта не ронял весь бот.
func (b *Bot) processUpdate(u tgbotapi.Update) {
	defer b.recoverPanic(u)
	b.logUserActivity(u)
	b.handleUpdate(u)
}

// recoverPanic перехватывает панику после обработки апдейта и логирует её
// с полными деталями (значение паники + стек вызовов).
func (b *Bot) recoverPanic(u tgbotapi.Update) {
	if r := recover(); r != nil {
		logger.Error("panic_recovered",
			"panic", isErrPanic(r),
			"stacktrace", stackTrace(),
			"update_id", u.UpdateID,
		)
	}
}

// logUserActivity логирует каждое текстовое сообщение или нажатие
// инлайн-кнопки на уровне INFO с полями user_id, chat_id, username
// и text (либо callback_data).
func (b *Bot) logUserActivity(u tgbotapi.Update) {
	if u.Message != nil && (u.Message.Text != "" || len(u.Message.Photo) > 0) {
		text := u.Message.Text
		if len(u.Message.Photo) > 0 {
			text = "[photo]"
		}
		logger.Info("user_message",
			"user_id", userIDOf(u.Message.From),
			"chat_id", u.Message.Chat.ID,
			"username", userNameOf(u.Message.From),
			"text", text,
		)
	}
	if u.CallbackQuery != nil && u.CallbackQuery.Message != nil {
		logger.Info("user_callback",
			"user_id", userIDOf(u.CallbackQuery.From),
			"chat_id", u.CallbackQuery.Message.Chat.ID,
			"username", userNameOf(u.CallbackQuery.From),
			"callback_data", u.CallbackQuery.Data,
		)
	}
}

// userIDOf возвращает ID пользователя или 0, если пользователь неизвестен
// (например, сообщение от канала).
func userIDOf(u *tgbotapi.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

// userNameOf возвращает username пользователя (может быть пустым).
func userNameOf(u *tgbotapi.User) string {
	if u == nil {
		return ""
	}
	return u.UserName
}

// handleUpdate распределяет обновление по обработчикам.
func (b *Bot) handleUpdate(u tgbotapi.Update) {
	switch {
	case u.Message != nil:
		b.handleMessage(u.Message)
	case u.CallbackQuery != nil:
		b.handleCallback(u.CallbackQuery)
	}
}

// handleMessage разбирает текстовое сообщение, фото и ответы на бота.
func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	isGroup := msg.Chat.IsGroup() || msg.Chat.IsSuperGroup()
	text := msg.Text

	// В группах реагируем только на команды, упоминания, фото и ответы боту.
	if isGroup && !b.isRelevantInGroup(msg) {
		return
	}

	// Прямая команда (например, "/find Mikoto").
	if strings.HasPrefix(text, "/") {
		cmd, args := parseCommand(text)
		b.handleCommand(msg, cmd, args)
		return
	}

	// В группах: обращение к боту через @упоминание.
	if isGroup && strings.Contains(text, "@"+b.api.Self.UserName) {
		clean := strings.TrimSpace(strings.Replace(text, "@"+b.api.Self.UserName, "", 1))
		if strings.HasPrefix(clean, "/") {
			cmd, args := parseCommand(clean)
			b.handleCommand(msg, cmd, args)
			return
		}
		if clean != "" {
			b.startTextSearch(msg.Chat.ID, clean, msg)
			return
		}
	}

	// В сообщении есть картинка — запускаем распознавание.
	if len(msg.Photo) > 0 {
		b.handleImage(msg)
		return
	}

	// Ответ на сообщение бота текстом — ищем по имени.
	if b.isReplyToBot(msg) {
		if query := strings.TrimSpace(text); query != "" {
			b.startTextSearch(msg.Chat.ID, query, msg)
		}
	}
}

// handleCommand обрабатывает команду (cmd) и её аргументы (args).
func (b *Bot) handleCommand(msg *tgbotapi.Message, cmd, args string) {
	switch cmd {
	case "/start":
		b.sendText(msg.Chat.ID, welcomeText(), markdown, msg.MessageID)

	case "/help":
		b.sendText(msg.Chat.ID, helpText(), markdown, msg.MessageID)

	case "/find":
		query := strings.TrimSpace(args)
		if query == "" {
			b.sendText(msg.Chat.ID, "Укажите имя персонажа или название аниме.\nПример: `/find Mikoto Misaka`", markdown, msg.MessageID)
			return
		}
		b.startTextSearch(msg.Chat.ID, query, msg)

	case "/search":
		// Отвечаем на сообщение с фото — распознаём изображение из него.
		if msg.ReplyToMessage != nil && len(msg.ReplyToMessage.Photo) > 0 {
			b.handleImage(msg.ReplyToMessage)
			return
		}
		b.sendText(msg.Chat.ID,
			"Ответьте этой командой на сообщение с изображением из аниме, или просто отправьте скриншот боту.",
			none, msg.MessageID)

	case "/select":
		num := strings.TrimSpace(args)
		if num == "" {
			b.sendText(msg.Chat.ID, "Укажите номер из списка, например: `/select 1`.", markdown, msg.MessageID)
			return
		}
		idx, err := strconv.Atoi(num)
		if err != nil {
			b.sendText(msg.Chat.ID, "Номер должен быть числом, например: `/select 1`.", markdown, msg.MessageID)
			return
		}
		b.selectByIndex(msg.Chat.ID, idx-1)
	}
}

// parseCommand разбирает строку "/команда@bot аргументы" на команду и аргументы.
func parseCommand(text string) (cmd, args string) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}

	raw := fields[0]
	if i := strings.IndexByte(raw, '@'); i >= 0 {
		raw = raw[:i] // команда с юзернеймом бота: /find@bot
	}

	cmd = strings.ToLower(strings.TrimSpace(raw))
	args = strings.TrimSpace(text[len(fields[0]):])
	return cmd, args
}

// isRelevantInGroup — фильтр сообщений в группах: реагируем только на
// команды, упоминания бота, фото и ответы на бота (флуд игнорируем).
func (b *Bot) isRelevantInGroup(msg *tgbotapi.Message) bool {
	if strings.HasPrefix(msg.Text, "/") {
		return true
	}
	if len(msg.Photo) > 0 {
		return true
	}
	if strings.Contains(msg.Text, "@"+b.api.Self.UserName) {
		return true
	}
	return b.isReplyToBot(msg)
}

// isReplyToBot — отвечает ли пользователь на сообщение бота.
func (b *Bot) isReplyToBot(msg *tgbotapi.Message) bool {
	return msg.ReplyToMessage != nil &&
		msg.ReplyToMessage.From != nil &&
		msg.ReplyToMessage.From.ID == b.api.Self.ID
}

// ============================================================================
// ТЕКСТОВЫЙ ПОИСК
// ============================================================================

// startTextSearch ищет персонажа по имени (с транслитерацией русского ввода),
// а если персонаж не найден — пробует найти аниме по названию.
func (b *Bot) startTextSearch(chatID int64, query string, origin *tgbotapi.Message) {
	replyTo := 0
	if origin != nil {
		replyTo = origin.MessageID
	}

	candidates, err := b.search.SearchCharactersSmart(query)
	if err != nil || len(candidates) == 0 {
		anime, animeErr := b.search.SearchAnimeSmart(query)
		if animeErr != nil {
			b.sendText(chatID, fmt.Sprintf("Не удалось найти «%s» ни среди персонажей, ни среди аниме.", query), none, replyTo)
			return
		}
		b.sendAnimeInfo(chatID, anime, replyTo)
		return
	}

	b.showCandidates(chatID, candidates, replyTo)
}

// ============================================================================
// ПОИСК ПО КАРТИНКЕ
// ============================================================================

// handleImage — распознавание по изображению (двухступенчатая схема):
//
//	ШАГ А: AnimeTrace — кто изображён (имя персонажа + аниме);
//	       затем Jikan обогащает карточку (архетип, описание, фото).
//	ШАГ Б (фолбэк): trace.moe — если AnimeTrace не справился, определяем хотя бы
//	       аниме по кадру и предлагаем его главных персонажей для выбора.
func (b *Bot) handleImage(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	// Индикатор обработки (удаляется в конце).
	processing := tgbotapi.NewMessage(chatID, "Распознаю персонажа...")
	sent, sendErr := b.api.Send(processing)
	if sendErr != nil {
		logger.Warn("telegram_api_error", "chat_id", chatID, "error", sendErr.Error())
	}
	cleanup := func() {
		if sent.MessageID == 0 {
			return
		}
		if _, err := b.api.Send(tgbotapi.NewDeleteMessage(chatID, sent.MessageID)); err != nil {
			logger.Warn("telegram_api_error", "chat_id", chatID, "error", err.Error())
		}
	}

	imageData, err := b.downloadLargestPhoto(msg)
	if err != nil {
		cleanup()
		b.sendText(chatID, "Не удалось получить изображение: "+err.Error(), none, msg.MessageID)
		return
	}

	// --- ШАГ А: AnimeTrace + Jikan ---
	charName, workTitle, aerr := b.animeTrace.RecognizeCharacter(imageData)
	cleanup()

	if aerr == nil && charName != "" {
		if character, cerr := b.search.SearchCharacterByName(charName); cerr == nil && character != nil {
			b.sendCandidateCard(chatID, character, msg.MessageID)

			// AnimeTrace возвращает тайтл на японском. Сверяем его со списком
			// аниме персонажа — защита от одноимённых персонажей.
			if workTitle != "" && !titleMatches(workTitle, character.Animes) {
				b.sendText(chatID, fmt.Sprintf(
					"Возможно, персонаж *%s* встречается и в другом тайтле. Уточните через `/find %s`.",
					mdEscape(character.Name), mdEscape(character.Name)),
					markdown, msg.MessageID)
			}
			return
		}
	}

	// --- ШАГ Б (фолбэк): trace.moe ---
	result, terr := b.traceMoe.SearchByImage(imageData)
	if terr != nil {
		b.sendText(chatID, "Не удалось распознать ни персонажа, ни аниме на этом изображении.", none, msg.MessageID)
		return
	}

	title := firstNonEmpty(result.TitleEnglish, result.Title, workTitle)
	characters, cerr := b.search.SearchAnimeCharacters(&AnimeRef{
		AniListID: result.AniList,
		MalID:     result.MalID(),
		Title:     title,
	})
	if cerr != nil || len(characters) == 0 {
		b.sendText(chatID, "*Найдено аниме по кадру*, но не удалось загрузить список его персонажей.", markdown, msg.MessageID)
		return
	}

	candidates := make([]*UnifiedCharacter, 0, len(characters))
	for _, role := range characters {
		if role == nil || role.Character == nil {
			continue
		}
		candidates = append(candidates, role.Character)
	}

	var sb strings.Builder
	sb.WriteString("*Найдено аниме по кадру*")
	if result.Similarity < imageSearchMinSimilarity {
		sb.WriteString("\n_Уверенность низкая, результат может быть неточным._")
	}
	if found := firstNonEmpty(result.TitleEnglish, result.Title); found != "" {
		sb.WriteString("\n" + mdEscape(b.localizeTitle(found)))
	}
	sb.WriteString("\n\n_Вероятно, это один из персонажей:_")

	b.sendText(chatID, sb.String(), markdown, msg.MessageID)
	b.showCandidates(chatID, candidates, 0)

	if result.Image != "" {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileURL(result.Image))
		photo.Caption = "Совпавший кадр"
		if _, err := b.api.Send(photo); err != nil {
			logger.Warn("telegram_api_error", "chat_id", chatID, "error", err.Error())
		}
	}
}

// downloadLargestPhoto скачивает самое большое изображение из сообщения.
func (b *Bot) downloadLargestPhoto(msg *tgbotapi.Message) ([]byte, error) {
	if len(msg.Photo) == 0 {
		return nil, errors.New("в сообщении нет изображения")
	}

	// Telegram отдаёт несколько размеров; последний — самый большой.
	photo := msg.Photo[len(msg.Photo)-1]

	file, err := b.api.GetFile(tgbotapi.FileConfig{FileID: photo.FileID})
	if err != nil {
		logger.Warn("telegram_api_error", "chat_id", msg.Chat.ID, "error", err.Error())
		return nil, fmt.Errorf("Telegram не отдал файл: %w", err)
	}
	if file.FilePath == "" {
		return nil, errors.New("Telegram не отдал путь к файлу")
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.api.Token, file.FilePath)
	resp, err := http.Get(url)
	if err != nil {
		logger.Warn("telegram_api_error", "chat_id", msg.Chat.ID, "error", err.Error())
		return nil, fmt.Errorf("не удалось скачать изображение: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание изображения вернуло статус %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// titleMatches проверяет, совпадает ли название тайтла с одним из списка
// (в обе стороны — текст может быть на японском, английском или русском).
func titleMatches(title string, titles []string) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}
	for _, t := range titles {
		if strings.Contains(t, title) || strings.Contains(title, t) {
			return true
		}
	}
	return false
}

// ============================================================================
// DISAMBIGUATION (выбор из кандидатов)
// ============================================================================

// showCandidates показывает результат поиска с учётом неоднозначности:
//   - 1 кандидат — сразу карточка персонажа;
//   - 2+ кандидатов — список с кнопками и командой /select N;
//   - 0 кандидатов — ничего не делает (вызвавший код сообщит об ошибке).
func (b *Bot) showCandidates(chatID int64, candidates []*UnifiedCharacter, replyTo int) {
	if len(candidates) == 0 {
		return
	}
	if len(candidates) == 1 {
		b.sendCandidateCard(chatID, candidates[0], replyTo)
		return
	}

	b.saveSelection(chatID, candidates)

	var sb strings.Builder
	sb.WriteString("*Выберите нужного персонажа:*\n\n")

	var rows [][]tgbotapi.InlineKeyboardButton
	for i, c := range candidates {
		num := i + 1
		label := c.Name
		if title := candidateTitle(c); title != "" {
			label += " — " + title
		}
		sb.WriteString(fmt.Sprintf("*%d.* %s\n", num, mdEscape(label)))
		sb.WriteString(fmt.Sprintf("   `/select %d`\n", num))

		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(strconv.Itoa(num), fmt.Sprintf("char_sel:%d", num)),
		))
	}

	msg := tgbotapi.NewMessage(chatID, sb.String())
	msg.ParseMode = markdown
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	if replyTo > 0 {
		msg.ReplyToMessageID = replyTo
	}
	if _, err := b.api.Send(msg); err != nil {
		logger.Warn("telegram_api_error", "chat_id", chatID, "error", err.Error())
	}
}

// selectByIndex обрабатывает выбор кандидата по индексу (0-based)
// из сохранённой сессии поиска.
func (b *Bot) selectByIndex(chatID int64, index int) {
	candidates := b.loadSelection(chatID)
	if len(candidates) == 0 {
		b.sendText(chatID, "Список для выбора устарел. Повторите `/find`.", markdown, 0)
		return
	}
	if index < 0 || index >= len(candidates) {
		b.sendText(chatID, fmt.Sprintf("Некорректный номер. Доступны варианты 1–%d.", len(candidates)), markdown, 0)
		return
	}

	c := candidates[index]
	b.sendCandidateCard(chatID, c, 0)
}

// sendCandidateCard отправляет карточку персонажа, догружая полную
// информацию (описание, архетип, список аниме) по ID источника.
// При ошибке показывает то, что уже есть в кандидате.
func (b *Bot) sendCandidateCard(chatID int64, c *UnifiedCharacter, replyTo int) {
	full, err := b.search.FetchCharacterByID(c.Source, c.ID)
	if err != nil {
		logger.Error("character_card_fetch_error",
			"source", c.Source,
			"id", c.ID,
			"error", err.Error(),
		)
		b.sendCharacterInfo(chatID, c, replyTo)
		return
	}

	full.Source = c.Source
	b.sendCharacterInfo(chatID, full, replyTo)
}

// saveSelection запоминает кандидатов для чата с таймстампом.
func (b *Bot) saveSelection(chatID int64, candidates []*UnifiedCharacter) {
	b.selectionsMu.Lock()
	defer b.selectionsMu.Unlock()
	b.selections[chatID] = candidates
	b.selectionAt[chatID] = time.Now()
}

// loadSelection возвращает сохранённых кандидатов; устаревшие сессии удаляются.
func (b *Bot) loadSelection(chatID int64) []*UnifiedCharacter {
	b.selectionsMu.Lock()
	defer b.selectionsMu.Unlock()

	ts, ok := b.selectionAt[chatID]
	if !ok || time.Since(ts) > selectionTTL {
		delete(b.selections, chatID)
		delete(b.selectionAt, chatID)
		return nil
	}
	return b.selections[chatID]
}

// ============================================================================
// CALLBACKI (инлайн-клавиатура)
// ============================================================================

// handleCallback обрабатывает нажатие инлайн-кнопки "char_sel:N".
func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	chatID := int64(0)
	if q.Message != nil {
		chatID = q.Message.Chat.ID
	}
	if _, err := b.api.Send(tgbotapi.NewCallback(q.ID, "")); err != nil {
		logger.Warn("telegram_api_error", "chat_id", chatID, "error", err.Error())
	}

	if q.Message == nil || q.Data == "" {
		return
	}
	if !strings.HasPrefix(q.Data, "char_sel:") {
		return
	}

	num, err := strconv.Atoi(strings.TrimPrefix(q.Data, "char_sel:"))
	if err != nil {
		return
	}
	b.selectByIndex(q.Message.Chat.ID, num-1)
}

// ============================================================================
// ОТПРАВКА СООБЩЕНИЙ
// ============================================================================

// sendText отправляет текстовое сообщение (parseMode — markdown или "").
func (b *Bot) sendText(chatID int64, text, parseMode string, replyTo int) {
	msg := tgbotapi.NewMessage(chatID, strings.Replace(text, "\\]", "]", -1))
	msg.ParseMode = parseMode
	msg.DisableWebPagePreview = true
	if replyTo > 0 {
		msg.ReplyToMessageID = replyTo
	}
	if _, err := b.api.Send(msg); err != nil {
		logger.Warn("telegram_api_error", "chat_id", chatID, "error", err.Error())
	}
}

// sendCharacterInfo отправляет карточку персонажа:
// имя, архетип, названия аниме, описание и фотографию.
func (b *Bot) sendCharacterInfo(chatID int64, c *UnifiedCharacter, replyTo int) {
	b.sendText(chatID, b.characterCard(c), markdown, replyTo)

	if c.ImageURL != "" {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileURL(c.ImageURL))
		photo.Caption = c.Name
		if replyTo > 0 {
			photo.ReplyToMessageID = replyTo
		}
		if _, err := b.api.Send(photo); err != nil {
			logger.Warn("telegram_api_error", "chat_id", chatID, "error", err.Error())
		}
	}
}

// characterCard формирует markdown-текст карточки персонажа.
// Локализуется на русский: имя, названия аниме и описание переводятся
// параллельно (с ограничением на число одновременных запросов к API перевода).
func (b *Bot) characterCard(c *UnifiedCharacter) string {
	var sb strings.Builder

	sb.WriteString("*" + mdEscape(b.localizeName(c)) + "*")

	// Архетип — опциональная функция (отключается через конфиг).
	if b.cfg.ArchetypeEnabled {
		sb.WriteString("\n\n*Архетип:* " + mdEscape(archetypeLabel(c.Archetype)))
	}

	// Названия аниме и описание переводим параллельно.
	localized := make([]string, len(c.Animes))
	desc := c.Description
	var wg sync.WaitGroup
	for i, t := range c.Animes {
		wg.Add(1)
		go func(i int, title string) {
			defer wg.Done()
			localized[i] = b.localizeTitle(title)
		}(i, t)
	}
	if c.Description != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			desc = b.localizeDescription(c.Description)
		}()
	}
	wg.Wait()

	if len(localized) > 0 {
		if len(localized) == 1 {
			sb.WriteString("\n*Аниме:* " + mdEscape(localized[0]))
		} else {
			sb.WriteString("\n*Аниме:*")
			for _, t := range localized {
				sb.WriteString("\n• " + mdEscape(t))
			}
		}
	}

	if desc != "" {
		sb.WriteString("\n\n" + mdEscape(desc))
	}

	return sb.String()
}

// sendAnimeInfo отправляет информацию об аниме (название, формат, статус).
func (b *Bot) sendAnimeInfo(chatID int64, a *UnifiedAnime, replyTo int) {
	format := a.Format
	if format == "" {
		format = "Неизвестно"
	}
	status := a.Status
	if status == "" {
		status = "Неизвестно"
	}

	var sb strings.Builder
	sb.WriteString("*" + mdEscape(b.localizeTitle(a.Title)) + "*")
	sb.WriteString("\n\n*Формат:* " + mdEscape(format))
	sb.WriteString("\n*Статус:* " + mdEscape(status))

	b.sendText(chatID, strings.Replace(sb.String(), "\\]", "]", -1), markdown, replyTo)
}

// ============================================================================
// ВСПОМОГАТЕЛЬНОЕ
// ============================================================================

// candidateTitle возвращает первое аниме кандидата (для подписи в списке).
func candidateTitle(c *UnifiedCharacter) string {
	if c == nil || len(c.Animes) == 0 {
		return ""
	}
	return c.Animes[0]
}

// mdEscape экранирует спецсимволы Markdown v1 в подставляемом тексте,
// чтобы ответы Telegram не ломались из-за скобок/подчёркиваний в названиях.
func mdEscape(s string) string {
	r := strings.NewReplacer(
		"\\", "\\\\",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"`", "\\`",
	)
	return r.Replace(s)
}

// welcomeText — текст команды /start.
func welcomeText() string {
	return "*Привет! Я аниме-сканер.*\n\n" +
		"Я определяю аниме по скриншоту и нахожу информацию о персонажах — " +
		"имя, тайтл и архетип характера.\n\n" +
		"*Как пользоваться:*\n" +
		"• Отправь скриншот из аниме — определю персонажа и аниме\n" +
		"• Напиши `/find Имя персонажа` — покажу фото, аниме и архетип\n" +
		"• Напиши `/find Название аниме` — покажу информацию об аниме\n" +
		"• `/help` — подробная справка\n\n" +
		"*Примеры:*\n" +
		"• `/find Taiga Aisaka`\n" +
		"• `/find Тайга Айсака`\n" +
		"• Или просто отправь скриншот"
}

// helpText — текст команды /help.
func helpText() string {
	return "*Справка по командам*\n\n" +
		"*Поиск по картинке:*\n" +
		"Просто отправь скриншот из аниме — бот определит название тайтла, " +
		"имя персонажа и его архетип. Либо ответь командой `/search` на сообщение с фото.\n\n" +
		"*Поиск по имени:*\n" +
		"• `/find Имя персонажа` — фото персонажа, название аниме и архетип\n" +
		"• `/find Название аниме` — информация об аниме\n" +
		"• Имя можно писать по-русски — бот сам транслитерирует\n\n" +
		"*Работа в группах:*\n" +
		"• Команды работают прямо в чате: `/find Имя`\n" +
		"• Можно обратиться к боту: `@имя_бота /find Имя` или просто `@имя_бота Имя`\n" +
		"• Можно ответить на сообщение бота текстом с именем персонажа\n\n" +
		"*Команды:*\n" +
		"• `/start` — приветствие\n" +
		"• `/help` — эта справка"
}
