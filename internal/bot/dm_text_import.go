package bot

// DM-console copy for the in-process history import. Russian, neutral
// register, no decorative emoji - same tone as dm_text.go. Format verbs
// are documented inline.

const (
	// Sent after /import in a chat with a valid session. Explains how to
	// produce the export and the upload window. No emoji; the numbered
	// steps mirror msgDMCleanupNoData so the two flows read consistently.
	msgImportAwait = "Загрузка истории чата.\n\n" +
		"1. Я уже должен быть администратором в выбранном чате (иначе импорт недоступен).\n" +
		"2. Telegram Desktop -> откройте чат -> меню -> Экспорт истории чата -> формат JSON " +
		"(без медиа - нужен только текст).\n" +
		"3. Пришлите файл <b>сюда</b> в течение 10 минут как документ.\n\n" +
		"Если файл больше 20 МБ (обычно так и есть), сожмите его в .zip или .gz " +
		"перед отправкой - я распакую сам. Telegram не отдаёт ботам файлы тяжелее 20 МБ " +
		"в любом виде, поэтому архивирование обязательно для крупных чатов.\n\n" +
		"Отмена: просто не присылайте файл, состояние истечёт само."

	// A document arrived but there is no live /import window.
	msgImportNoContext = "Я не ждал файл. Сначала отправьте /import, затем пришлите экспорт " +
		"в течение 10 минут."

	// The document is over the 20 MB bot-download cap and is not an
	// archive, so it cannot be fetched at all.
	msgImportTooBig = "Файл больше 20 МБ - Telegram не отдаёт его боту целиком.\n\n" +
		"Сожмите экспорт в .zip или .gz (обычно сжимается в разы) и пришлите архив - " +
		"я распакую сам. Либо в Telegram Desktop экспортируйте историю по диапазону дат " +
		"несколькими частями и пришлите каждую отдельно (импорт идемпотентный, " +
		"пересечения дат не задвоятся)."

	// Another import is already running for this chat.
	msgImportAlreadyRunning = "По этому чату уже идёт импорт. Дождитесь его завершения " +
		"или остановите в той переписке."

	// getFile failed or returned no path. Generic transient/structural
	// download failure.
	msgImportDownloadFail = "Не удалось получить файл от Telegram. Попробуйте прислать его заново."

	// The download link returned non-200 (expired or revoked).
	msgImportLinkExpired = "Ссылка на файл устарела или недоступна. Пришлите файл заново."

	// Decompression/structure error: not JSON, empty, no .json in zip, or
	// the decompressed stream exceeded the safety limit.
	msgImportBadArchive = "Не получилось разобрать файл. Ожидается JSON-экспорт Telegram Desktop " +
		"(или .zip/.gz с ним внутри). Проверьте, что это именно \"Экспорт истории чата\" в формате JSON, " +
		"а не общий \"Экспорт данных Telegram\" аккаунта."

	// Shown on the progress message while the file is downloaded/parsed
	// for the pre-commit preview.
	msgImportParsing = "Скачиваю и разбираю экспорт... Это может занять до нескольких минут."

	// Pre-commit confirmation. Verbs in order:
	//   %s chat title, %s messages, %d unique users,
	//   %s date range, %s top-3 block.
	msgImportConfirm = "<b>Проверьте перед загрузкой</b>\n" +
		"Чат: <b>%s</b>\n" +
		"Сообщений в экспорте: %s\n" +
		"Уникальных участников: %d\n" +
		"Период: %s\n%s\n" +
		"Загрузка обновит участников (для /cleanup) и помесячную статистику " +
		"(для /stats month). Повторная загрузка того же экспорта не задвоит данные."

	// Final state after cancel or abort.
	msgImportCancelled = "Импорт отменён. Файл удалён, ничего не записано."

	// Progress line during ingest. %d done, %d total participants.
	msgImportProgress = "Загружаю в базу: %d из %d участников. Можно остановить кнопкой ниже."

	// The import path is unavailable (minimal app build without the
	// import dependencies wired).
	msgImportUnavailable = "Импорт истории сейчас недоступен."
)
