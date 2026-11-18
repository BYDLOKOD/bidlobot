package automat

// Item is one curated excuse from the spin pool.
type Item struct {
	ID     int
	Phrase string
}

// catalogue holds the twelve curated excuses from the TZ "Excuses - spin
// prizes" section. The order is the stable item identifier order.
var catalogue = []Item{
	{1, `У меня локально работает.`},
	{2, `Я думал, это черновик.`},
	{3, `Это согласовали на созвоне, записи нет.`},
	{4, `Я как раз собирался это проверить.`},
	{5, `В документации был другой понедельник.`},
	{6, `Я нажал "сохранить". Дальше началась самостоятельная жизнь файла.`},
	{7, `У меня открыта правильная вкладка. Просто не та.`},
	{8, `Я не опоздал. Я пришёл к повторному обсуждению.`},
	{9, `Сначала надо выяснить, кто записался добровольцем. Надеюсь, не я.`},
	{10, `Я отвечу, как только вспомню вопрос.`},
	{11, `Последний раз я видел это в рабочем состоянии.`},
	{12, `Я оставил это до утра. Утро пришло без предупреждения.`},
}

// Catalogue returns a copy of the curated excuse pool.
func Catalogue() []Item {
	out := make([]Item, len(catalogue))
	copy(out, catalogue)
	return out
}

func itemByID(id int) (Item, bool) {
	if id < 1 || id > len(catalogue) {
		return Item{}, false
	}
	return catalogue[id-1], true
}
