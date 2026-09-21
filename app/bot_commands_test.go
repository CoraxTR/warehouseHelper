package app

import (
	"regexp"
	"testing"
	"unicode/utf8"
)

// telegramCommandName — правило Telegram для имени команды: строчные латинские
// буквы, цифры, подчёркивание, до 32 символов, без слэша. Кириллица не проходит —
// именно поэтому команда называется /discounts, а не /скидки.
var telegramCommandName = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// Разбор бот-команды «/discounts»: сравнение по первому слову, регистр не важен,
// адрес бота отбрасывается. Чужие сообщения командой не считаются — на них
// модуль скидок не отвечает.
func TestIsDiscountsCommand(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "команда", text: "/discounts", want: true},
		{name: "с адресом бота", text: "/discounts@warehouse_bot", want: true},
		{name: "с хвостом", text: "/discounts ?", want: true},
		{name: "верхний регистр", text: "/DISCOUNTS", want: true},
		{name: "пробелы вокруг", text: "  /discounts  ", want: true},
		{name: "прежняя кириллическая команда", text: "/скидки", want: false},
		{name: "другая команда", text: "/start", want: false},
		{name: "слово без слэша", text: "discounts", want: false},
		{name: "команда в тексте", text: "а какие /discounts сейчас", want: false},
		{name: "пустое", text: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDiscountsCommand(tt.text); got != tt.want {
				t.Errorf("isDiscountsCommand(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// Меню команд: имена — по правилам Telegram, описания заполнены, команда отчёта
// в списке есть. Отдельно проверяется, что её имя разбирает обработчик: иначе
// команда будет видна в клиентах, но бот на неё не ответит.
func TestBotCommandsMatchTelegramRules(t *testing.T) {
	cmds := botCommands()
	if len(cmds) == 0 {
		t.Fatal("меню команд пусто: /discounts не будет виден в клиентах")
	}

	found := false
	for _, c := range cmds {
		if !telegramCommandName.MatchString(c.Command) {
			t.Errorf("имя команды %q не по правилам Telegram ([a-z0-9_], до 32)", c.Command)
		}
		if desc := utf8.RuneCountInString(c.Description); desc == 0 || desc > 256 {
			t.Errorf("описание команды %q: %d символов, want 1..256", c.Command, desc)
		}
		if c.Command == discountsCommand {
			found = true

			if !isDiscountsCommand("/" + c.Command) {
				t.Errorf("команда меню %q не разбирается обработчиком", c.Command)
			}
		}
	}

	if !found {
		t.Errorf("в меню нет команды %q", discountsCommand)
	}
}
