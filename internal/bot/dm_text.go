package bot

// All DM-console user-facing copy. Russian, neutral register, no
// decorative emoji - status glyphs only where they carry meaning.
// Format verbs are documented inline so callers pass the right args.

const (
	// Sent privately to an admin who typed a moderation command in the
	// public group. The command itself is deleted from the group.
	msgPublicModerationRedirect = "Модерация теперь только в личке - так участники чата не видят " +
		"управление.\n\nОтправьте мне здесь /start, выберите чат и управляйте: " +
		"/ban, /warn, /mute, /cleanup и т.д. Ваша команда в группе удалена."

	// Posted once when the bot is promoted to admin. Tells the chat
	// the bot exists and that management is private.
	msgOnboardingAdmin = "<b>BidloBot</b> подключён.\n\n" +
		"Статистика и игры - здесь: /stats, /dice, /quiz.\n" +
		"Модерация и чистка неактивных - только в личке со мной " +
		"(участники чата ничего не видят): откройте со мной личный чат " +
		"и отправьте /start."

	msgDMUnknown = "Неизвестная команда. Отправьте /help."

	msgDMError = "Внутренняя ошибка. Попробуйте позже."

	msgDMNoChats = "Не вижу чатов, где вы админ, а я могу модерировать.\n\n" +
		"Если я ещё не в группе: добавьте меня администратором с правом " +
		"ограничивать участников.\n\n" +
		"Если я уже добавлен админом, но вы видите это сообщение: я мог " +
		"пропустить событие назначения. Снимите и снова выдайте мне права " +
		"администратора в группе (или переназначьте) - это меня зарегистрирует. " +
		"Затем отправьте /start снова."

	// %s = chat title
	msgDMReady = "Активный чат: <b>%s</b>.\n"

	dmHelpBody = "\nУправление (всё приватно, в этой переписке - участники чата ничего не видят):\n\n" +
		"<b>Статистика</b>\n" +
		"/stats - обзор\n" +
		"/stats top - самые активные\n" +
		"/stats today - за сегодня\n" +
		"/stats @user - по участнику\n\n" +
		"<b>Модерация</b>\n" +
		"/warn @user причина\n" +
		"/warns @user - список предупреждений\n" +
		"/warns clear @user - сбросить\n" +
		"/mute @user 1h\n" +
		"/unmute @user\n" +
		"/ban @user причина - с подтверждением\n" +
		"/unban @user\n\n" +
		"<b>Чистка неактивных</b>\n" +
		"/cleanup 6mo - предпросмотр, затем подтверждение\n\n" +
		"<b>Прочее</b>\n" +
		"/chat - сменить активный чат\n\n" +
		"Цель указывается как @username или числовой id. " +
		"Бот знает участника, если тот хотя бы раз писал или реагировал, " +
		"либо если история была загружена командой /import."

	msgDMPick = "Выберите чат для управления:"

	msgDMNoSession = "Сначала выберите чат: отправьте /start."

	msgDMLostAdmin = "Вы больше не администратор в выбранном чате. Отправьте /start, чтобы выбрать другой."

	msgDMTargetNotFound = "Цель не найдена. Укажите @username или числовой id. " +
		"Бот знает участника, только если тот писал/реагировал или история загружена через import."

	msgDMNeedTarget = "Укажите цель: @username или числовой id."

	// %s = display name
	msgDMWarnsCleared = "Предупреждения сняты: %s."

	// %s = display name, %d = count
	msgDMWarned = "Предупреждение выдано: %s (%d/3)."

	// %s = reason
	msgDMReasonLine = "Причина: %s"

	msgDMAutomuteFailed = "Порог 3/3 достигнут, но авто-мьют не удался (нет прав?)."
	msgDMAutomuteOn     = "Порог 3/3 - включён авто-мьют на 24 часа."

	msgDMBadDuration = "Неверная длительность. Примеры: 30m, 1h, 12h, 7d."

	// %s = display name, %s = duration
	msgDMMuted = "Мьют выдан: %s на %s."

	msgDMPermOrTransient = "Не удалось замьютить. Проверьте, что у бота есть право ограничивать участников."

	// %s = display name
	msgDMUnmuted  = "Мьют снят: %s."
	msgDMUnbanned = "Разбанен: %s."

	// %s = display name
	msgDMConfirmBan = "Забанить <b>%s</b>?\nДействие необратимо для участника (нужно будет разбанивать вручную)."

	// %s = display name
	msgDMBanned = "Забанен: <b>%s</b>."

	// %s = display name
	msgDMBanFailed = "Не удалось забанить <b>%s</b>. У бота нет права ограничивать участников. " +
		"Запросите бан заново после выдачи прав."

	msgDMCancelled = "Отменено."

	// --- cleanup ---

	msgDMCleanupUsage = "Укажите период: /cleanup 6mo (примеры: 30d, 3mo, 1y).\n" +
		"Остановить запущенную чистку: /cleanup stop."

	msgDMCleanupBadPeriod = "Неверный период. Допустимо: 1d-5y. Примеры: 30d, 6mo, 1y."

	msgDMCleanupNoData = "У бота пока нет данных об участниках этого чата.\n\n" +
		"Бот видит только тех, кто писал или реагировал <i>после</i> его добавления. " +
		"Чтобы почистить по полугодовой истории, загрузите экспорт чата прямо здесь:\n\n" +
		"1. Я уже должен быть администратором в этом чате (без этого импорт недоступен).\n" +
		"2. Telegram Desktop -> откройте чат -> меню -> Экспорт истории чата -> формат JSON.\n" +
		"3. Отправьте мне здесь /import, затем пришлите этот файл. " +
		"Если файл больше ~20 МБ, сожмите его в .zip или .gz - я распакую сам.\n\n" +
		"После импорта снова отправьте /cleanup."

	// %d = known members
	msgDMCleanupNoneActive = "Чистить некого: все %d наблюдаемых ботом участников активны за заданный период."

	// Shown when there are no proven-inactive members, but some members
	// have zero recorded activity (a data gap, not silence). %d = count
	// of such members.
	msgDMCleanupOnlyNoEv = "🧹 <b>Чистка неактивных</b>\n\n" +
		"Доказанных молчунов нет. Но у <b>%d</b> участников бот не видел " +
		"ни одного сообщения или реакции - это <i>отсутствие данных</i>, " +
		"а не доказательство неактивности. Их нельзя кикать вслепую:"

	// %d = threshold (human), %d = known members
	msgDMCleanupHeader = "🧹 <b>Чистка неактивных</b>\n\n" +
		"Период: <b>%s</b>\nИзвестно боту участников: <b>%d</b>"

	// %s = install date (human), %s = window (human)
	msgDMCleanupWindow = "Данные бот видит с %s (~%s)."

	// Loud warning: requested period is longer than the data window, so
	// "no activity" really means "no data". %s = period, %s = window.
	msgDMCleanupWindowWarn = "⚠️ <b>Запрошено %s, а данных только за ~%s.</b> " +
		"Кто был активен <i>до</i> начала данных, выглядит молчуном по ошибке. " +
		"Раздел [активность не зафиксирована] ниже - это НЕ доказанные молчуны."

	// Loud warning: the bot has no recorded install/earliest-data date.
	msgDMCleanupNoInstallWarn = "⚠️ <b>Бот не помнит, с какой даты у него данные по этому чату.</b> " +
		"Окно наблюдения неизвестно, список ненадёжен - проверяйте вручную."

	// %d = count
	msgDMCleanupStaleHeader = "\n\n<b>Молчат давно</b> (%d) - последняя активность раньше периода:"

	// %d = count
	msgDMCleanupNoEvHeader = "\n\n<b>Активность не зафиксирована</b> (%d) - бот ни разу не " +
		"видел их сообщений или реакций (вступили до начала данных или только читают). " +
		"<b>В авто-кик не войдут</b>, проверьте вручную:"

	msgDMCleanupExportNote = "\n\nℹ️ В экспорте Telegram нет реакций и нет @username. " +
		"Имена и теги бот подтягивает живым запросом; кто молчал, но ставит реакции, " +
		"после добавления бота админом перестанет попадать в список."

	// %d = proven-stale count the campaign will work through
	msgDMCleanupConfirmFooter = "\n\nКнопка <b>запустит</b> чистку по списку [молчат давно] (%d): " +
		"бот будет раз в день <b>публично в чате</b> тегать их пачками, давать время " +
		"проявиться (написать или поставить реакцию) и кикать молчунов, пока список " +
		"не кончится. Раздел [активность не зафиксирована] НЕ участвует.\n" +
		"Запустить может только инициатор. Остановить потом: /cleanup stop."

	// Shown instead of a confirm when nothing is safe to auto-act on.
	msgDMCleanupReviewOnly = "\n\nℹ️ Запускать чистку не на ком - " +
		"эти участники требуют ручной проверки. Для надёжного списка " +
		"загрузите свежий экспорт через /import и повторите."

	// /cleanup feature not wired (minimal/test build).
	msgDMCleanupUnavailable = "Чистка сейчас недоступна."

	// %d = how many newly queued into the campaign
	msgDMCleanupStarted = "✅ Чистка запущена: <b>%d</b> в очереди.\n\n" +
		"Бот сам, раз в день, будет публично тегать их в чате пачками и " +
		"давать время проявиться; кто промолчит - кик (вернуться можно по ссылке). " +
		"Идёт, пока список не кончится.\n\n" +
		"Остановить в любой момент: /cleanup stop (по выбранному чату)."

	// %d = records still in the active campaign
	msgDMCleanupCampaignActive = "По этому чату уже идёт чистка: в работе <b>%d</b>. " +
		"Останови её (/cleanup stop), потом запускай заново."

	// %d = how many records dropped
	msgDMCleanupStopped = "🛑 Чистка остановлена. Снято из очереди: <b>%d</b>. " +
		"Уже кикнутых это не возвращает."

	msgDMCleanupNoCampaign = "По этому чату чистка не запущена - останавливать нечего."

	msgDMCleanupNothingLeft = "Кандидатов не осталось - возможно, данные изменились."

	msgDMCleanupAlreadyRunning = "По этому чату уже идёт чистка (запущена другим админом). " +
		"Дождитесь её завершения или остановите в той переписке."

	// %d = done, %d = total
	msgDMCleanupRunning = "Чистка идёт: %d из %d. Можно остановить кнопкой ниже."

	msgDMCleanupDone = "Чистка завершена."

	// %d kicked, %d skipped, %d failed
	msgDMCleanupReport = "Чистка завершена.\nКикнуто: %d\nПропущено: %d\nОшибок: %d"

	// %d kicked, %d not processed
	msgDMCleanupAborted = "Чистка остановлена.\nКикнуто: %d\nНе обработано: %d"
)
