package text

const (
	ErrSupergroupOnly  = "Команда доступна только в supergroup-чатах."
	ErrNotAdmin        = "У вас нет прав для этой команды."
	ErrBotNoRestrict   = "Боту нужны права администратора с разрешением 'Restrict Members'."
	ErrAnonymousAdmin  = "Модерация недоступна в анонимном режиме. Отключите 'Remain Anonymous'."
	ErrNoTarget        = "Укажите пользователя: ответьте на сообщение или укажите @username."
	ErrTargetNotKnown  = "Пользователь не найден. Используйте reply на его сообщение."
	ErrCantActionBot   = "Нельзя %s бота."
	ErrCantActionAdmin = "Нельзя %s администратора."
	ErrCantActionSelf  = "Нельзя %s самого себя."
	ErrInvalidDuration = "Неверный формат. Примеры: 30m, 1h, 7d."
	ErrUserNotBanned   = "Пользователь не забанен."

	ErrStatsGroupOnly    = "Статистика доступна только в групповых чатах."
	ErrStatsUnknownSub   = "Неизвестная подкоманда. Доступные: top, today, @username."
	ErrStatsUserNotFound = "Пользователь не найден в статистике чата."

	ErrMuteFailed    = "Не удалось замьютить. Попробуйте ещё раз."
	ErrBotLostRights = "Бот потерял права администратора. Верните права."

	MsgWarningsCleared = "Предупреждения сброшены для %s."
	MsgNeedAdmin       = "Боту нужны права администратора с разрешением 'Restrict Members'."
	MsgNotSupergroup   = "Бот работает только в supergroup-чатах."

	// Chat summarization (/summarize). All plain text: the result is
	// posted publicly and the model is untrusted, so no message on this
	// path ever carries a ParseMode.
	ErrSummarizeAnon          = "Суммаризация недоступна в анонимном режиме. Отключите 'Remain Anonymous' и повторите."
	MsgSummarizeNotConfigured = "Суммаризация не настроена: у бота нет ключа GLM."
	MsgSummarizeBusy          = "Уже собираю итог для этого чата - дождитесь результата."
	MsgSummarizeEmpty         = "Пока нечего суммировать: бот слушает чат только с момента запуска, накопленных сообщений нет."
	MsgSummarizeWorking       = "Собираю последние сообщения и суммирую через GLM - это может занять до ~2 минут..."
	ErrSummarizeAuth          = "Суммаризация недоступна: ключ GLM отклонён провайдером."
	ErrSummarizeQuota         = "Суммаризация недоступна: на аккаунте GLM нет средств. Пополните баланс у провайдера."
	ErrSummarizeRateLimited   = "GLM сейчас перегружен. Попробуйте позже."
	ErrSummarizeTooLong       = "Слишком много текста для одной суммаризации. Укажите меньшее N."
	ErrSummarizeTimeout       = "Не успел суммировать за отведённое время. Попробуйте меньшее N."
	ErrSummarizeProvider      = "Временная ошибка суммаризации. Попробуйте позже."
	ErrSummarizeGlobalLimit   = "Слишком много суммаризаций за последний час (по всем чатам). Попробуйте позже."

	// New-member captcha. The %s in Greeting/Solved/Kicked is an HTML
	// mention (@username or a tg://user link); messages carrying it are
	// sent with ParseMode HTML.
	MsgCaptchaGreeting = "Добро пожаловать, %s! Решите капчу, чтобы остаться в чате:"
	MsgCaptchaWrong    = "Неправильно, попробуйте ещё раз."
	MsgCaptchaNotYours = "Эта капча не для вас."
	MsgCaptchaExpired  = "Капча просрочена или не найдена."
	MsgCaptchaSolved   = "Добро пожаловать, %s!"
	MsgCaptchaKicked   = "%s кикнут: не решил капчу."
)
