package bot

import (
	"slices"
	"strings"

	"github.com/mymmrac/telego"
)

var professionNames = []string{
	"Backend Developer", "Frontend Developer", "Full-Stack Developer", "DevOps Engineer",
	"ML Engineer", "Software Architect", "QA Engineer", "Data Engineer", "Mobile Developer", "Security Engineer",
}

var professionAliases = map[string]string{
	"backend": "Backend Developer", "frontend": "Frontend Developer",
	"fullstack": "Full-Stack Developer", "full-stack": "Full-Stack Developer",
	"devops": "DevOps Engineer", "ml": "ML Engineer", "machine learning": "ML Engineer",
	"architect": "Software Architect", "qa": "QA Engineer",
}

var technologyGroups = []struct {
	name  string
	items []string
}{
	{"Популярное", []string{"Python", "Go", "TypeScript", "React", "PostgreSQL", "Docker", "Kubernetes", "Git"}},
	{"Языки", []string{"Python", "Go", "TypeScript", "JavaScript", "Java", "Kotlin", "C#", "C++", "Rust", "Clojure", "Ruby", "PHP", "Swift", "SQL", "Bash"}},
	{"Фронтенд", []string{"React", "Vue", "Angular", "Svelte", "Next.js", "HTML", "CSS", "Tailwind CSS"}},
	{"Бэкенд", []string{"Node.js", "Spring Boot", "Django", "FastAPI", "ASP.NET Core", "NestJS", "gRPC", "Kafka"}},
	{"Базы данных", []string{"PostgreSQL", "Redis", "MySQL", "SQLite", "MongoDB", "ClickHouse", "Elasticsearch"}},
	{"Инфраструктура", []string{"Docker", "Kubernetes", "Linux", "Terraform", "Ansible", "AWS", "Azure", "Google Cloud", "Nginx", "Prometheus", "Grafana", "GitHub Actions"}},
	{"Машинное обучение", []string{"PyTorch", "TensorFlow", "scikit-learn", "Pandas", "NumPy", "Hugging Face", "Jupyter", "MLflow"}},
	{"Инструменты и архитектура", []string{"Git", "C4", "oh-my-pi", "pi", "OpenAPI", "Figma", "PlantUML"}},
}

func canonicalProfession(name string) string {
	if canonical := professionAliases[strings.ToLower(name)]; canonical != "" {
		return canonical
	}
	for _, canonical := range professionNames {
		if strings.EqualFold(name, canonical) {
			return canonical
		}
	}
	return name
}

func professionSuggestions(mode int, prefix string) []string {
	var matches []string
	add := func(items []string) {
		for _, name := range items {
			if len(matches) < 8 && strings.HasPrefix(strings.ToLower(name), prefix) && !slices.Contains(matches, name) {
				matches = append(matches, name)
			}
		}
	}
	if mode == professionRoles {
		add(professionNames)
		for alias, name := range professionAliases {
			if strings.HasPrefix(alias, prefix) && !slices.Contains(matches, name) {
				matches = append(matches, name)
			}
		}
		slices.Sort(matches)
		return matches
	}
	for _, group := range technologyGroups {
		add(group.items)
	}
	return matches
}

func (m *professionsMenu) startChoice(s *professionSession, mode int, focus string) error {
	previous, err := m.currentChoice(s.chat, s.actor, mode)
	if err != nil {
		return err
	}
	s.mode, s.view, s.group, s.offset = mode, "pick", 0, 0
	s.before = previous
	s.selected = make(map[string]bool, len(previous))
	for _, name := range previous {
		s.selected[name] = true
	}
	s.choices = nil
	if mode == professionRoles {
		s.choices = slices.Clone(professionNames)
		for _, name := range previous {
			if !slices.Contains(s.choices, name) {
				s.choices = append(s.choices, name)
			}
		}
		if index := slices.Index(s.choices, focus); index >= 0 {
			s.offset = index / professionsPage * professionsPage
		}
		return nil
	}
	for i, group := range technologyGroups {
		if index := slices.Index(group.items, focus); index >= 0 {
			s.group, s.offset = i, index/professionsPage*professionsPage
			break
		}
	}
	return nil
}

func (s *professionSession) picked() []string {
	items := make([]string, 0, len(s.selected))
	for name := range s.selected {
		items = append(items, name)
	}
	slices.Sort(items)
	return items
}

func (s *professionSession) pickerItems() []string {
	if s.view == "selected" {
		return s.picked()
	}
	if s.mode == professionRoles {
		return s.choices
	}
	return technologyGroups[s.group].items
}

func (s *professionSession) choose(action string, arg int) bool {
	switch action {
	case "categories":
		if s.mode != professionStack {
			return false
		}
		s.view, s.offset = "categories", 0
	case "group":
		if s.mode != professionStack || arg >= len(technologyGroups) {
			return false
		}
		s.view, s.group, s.offset = "pick", arg, 0
	case "pick":
		s.view, s.offset = "pick", 0
	case "pickpage":
		if s.view == "categories" || arg >= len(s.pickerItems()) {
			return false
		}
		s.offset = arg
	case "selected":
		s.view, s.offset = "selected", 0
	case "mark", "unmark":
		if s.view != "pick" && (s.view != "selected" || action != "unmark") {
			return false
		}
		items := s.pickerItems()
		if arg >= len(items) {
			return false
		}
		name := items[arg]
		if action == "unmark" {
			delete(s.selected, name)
			if s.view == "selected" && s.offset >= len(items)-1 {
				s.offset = max(0, s.offset-professionsPage)
			}
		} else {
			s.selected[name] = true
		}
	case "cancel":
		s.view, s.selected = "home", nil
	default:
		return false
	}
	return true
}

func (s *professionSession) picker(button func(string, string, int) telego.InlineKeyboardButton) (string, [][]telego.InlineKeyboardButton) {
	title := "Мои профессии"
	if s.mode == professionStack {
		title = "Мой стек"
		if s.view == "pick" {
			title += " · " + technologyGroups[s.group].name
		}
	}
	var options []telego.InlineKeyboardButton
	var nav []telego.InlineKeyboardButton
	if s.view == "categories" {
		title += " · Разделы"
		for i, group := range technologyGroups {
			options = append(options, button(group.name, "group", i))
		}
	} else {
		items := s.pickerItems()
		if s.view == "selected" {
			title += " · Выбрано"
			if len(items) == 0 {
				title += "\nПока ничего не выбрано."
			}
		}
		for i := s.offset; i < min(s.offset+professionsPage, len(items)); i++ {
			label := items[i]
			action := "mark"
			if s.selected[label] {
				action = "unmark"
				label = "✓ " + label
			}
			options = append(options, button(label, action, i))
		}
		nav = professionPageButtons(button, "pickpage", s.offset, professionsPage, len(items))
	}
	rows := professionGrid(options)
	if len(nav) != 0 {
		rows = append(rows, nav)
	}
	if s.view == "selected" || s.view == "categories" {
		rows = append(rows, []telego.InlineKeyboardButton{button("К выбору", "pick", 0)})
	} else if s.mode == professionStack {
		rows = append(rows, []telego.InlineKeyboardButton{button("Разделы", "categories", 0)})
	}
	if s.view != "selected" {
		rows = append(rows, []telego.InlineKeyboardButton{button(professionSelectionLabel(len(s.selected)), "selected", 0)})
	}
	rows = append(rows, []telego.InlineKeyboardButton{button("Сохранить", "save", 0), button("Отмена", "cancel", 0)})
	return title, rows
}
