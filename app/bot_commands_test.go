package app

import (
	"regexp"
	"testing"
	"unicode/utf8"
)

// telegramCommandName — правило Telegram для имени команды: строчные латинские
// буквы, цифры, подчёркивание, до 32 символов, без слэша. Кириллица не проходит
// — поэтому в меню стоят /discounts и /sroki; русские написания (/скидки,
// /сроки) в меню не попадают, но разбором принимаются: текст сообщения
// приходит к боту дословно, серверной фильтрации команд нет.
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
		{name: "кириллическая команда", text: "/скидки", want: true},
		{name: "кириллица в верхнем регистре", text: "/СКИДКИ", want: true},
		{name: "кириллица с адресом бота", text: "/скидки@whHlpr_bot", want: true},
		{name: "кириллица с хвостом", text: "/скидки ?", want: true},
		{name: "другая команда", text: "/start", want: false},
		{name: "кириллическая чужая команда", text: "/сроки", want: false},
		{name: "слово без слэша", text: "discounts", want: false},
		{name: "кириллица без слэша", text: "скидки", want: false},
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

// Меню команд: имена — по правилам Telegram, описания заполнены, команды отчёта
// и сроков в списке есть. Отдельно проверяется, что их имена разбирает
// обработчик: иначе команда будет видна в клиентах, но бот на неё не ответит.
// Тем же тестом стережём русские написания: в меню кириллица попасть не может,
// значит если её перестанет принимать разбор — команда исчезнет молча.
func TestBotCommandsMatchTelegramRules(t *testing.T) {
	cmds := botCommands()
	if len(cmds) == 0 {
		t.Fatal("меню команд пусто: /discounts не будет виден в клиентах")
	}

	found := false
	foundSroki := false
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
		if c.Command == srokiCommand {
			foundSroki = true

			if !isSrokiCommand("/" + c.Command) {
				t.Errorf("команда меню %q не разбирается обработчиком", c.Command)
			}
		}
	}

	if !found {
		t.Errorf("в меню нет команды %q", discountsCommand)
	}
	if !foundSroki {
		t.Errorf("в меню нет команды %q", srokiCommand)
	}

	// Русские написания команд (в меню их нет — Telegram не принимает кириллицу
	// в именах команд, но текст сообщения приходит боту дословно).
	if !isDiscountsCommand("/" + discountsAliasRU) {
		t.Errorf("русское написание %q не разбирается обработчиком", discountsAliasRU)
	}
	if !isSrokiCommand("/" + srokiAliasRU + " 19379") {
		t.Errorf("русское написание %q не разбирается обработчиком", srokiAliasRU)
	}
}

// Разбор команды «/sroki <номер>» (и её русского написания «/сроки <номер>»):
// номер заказа берётся из хвоста сообщения, адрес бота отбрасывается. Чужие
// сообщения командой не считаются — иначе бот отвечал бы сроками на любое
// упоминание слова в чате.
func TestIsSrokiCommand(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "с номером", text: "/sroki 19379", want: true},
		{name: "без номера", text: "/sroki", want: true},
		{name: "с адресом бота", text: "/sroki@warehouse_bot 19379", want: true},
		{name: "верхний регистр", text: "/SROKI 19379", want: true},
		{name: "пробелы вокруг", text: "  /sroki 19379  ", want: true},
		{name: "кириллическая команда", text: "/сроки 19379", want: true},
		{name: "кириллица без номера", text: "/сроки", want: true},
		{name: "кириллица в верхнем регистре", text: "/СРОКИ 19379", want: true},
		{name: "кириллица с адресом бота", text: "/сроки@whHlpr_bot 19379", want: true},
		{name: "другая команда", text: "/discounts", want: false},
		{name: "кириллическая чужая команда", text: "/скидки", want: false},
		{name: "слово без слэша", text: "sroki 19379", want: false},
		{name: "кириллица без слэша", text: "сроки 19379", want: false},
		{name: "пустое", text: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSrokiCommand(tt.text); got != tt.want {
				t.Errorf("isSrokiCommand(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// Аргумент команды: номер заказа — хвост сообщения после имени команды.
func TestCommandArg(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "номер", text: "/sroki 19379", want: "19379"},
		{name: "адрес бота", text: "/sroki@warehouse_bot 19379", want: "19379"},
		{name: "лишние пробелы", text: "  /sroki   19379  ", want: "19379"},
		{name: "аргумента нет", text: "/sroki", want: ""},
		{name: "пробел без аргумента", text: "/sroki   ", want: ""},
		{name: "кириллическая команда", text: "/сроки 19379", want: "19379"},
		{name: "кириллица с адресом бота", text: "/сроки@whHlpr_bot 19379", want: "19379"},
		{name: "перенос строки", text: "/sroki\n19379", want: "19379"},
		{name: "два слова", text: "/sroki 19379 и 19380", want: "19379 и 19380"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandArg(tt.text); got != tt.want {
				t.Errorf("commandArg(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
