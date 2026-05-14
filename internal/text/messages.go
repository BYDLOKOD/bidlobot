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
)
