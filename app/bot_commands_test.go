package app

import "testing"

// Разбор бот-команды «/скидки»: сравнение по первому слову, регистр не важен,
// адрес бота отбрасывается. Чужие сообщения командой не считаются — на них
// модуль скидок не отвечает.
func TestIsDiscountsCommand(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "команда", text: "/скидки", want: true},
		{name: "с адресом бота", text: "/скидки@warehouse_bot", want: true},
		{name: "с хвостом", text: "/скидки ?", want: true},
		{name: "верхний регистр", text: "/СКИДКИ", want: true},
		{name: "пробелы вокруг", text: "  /скидки  ", want: true},
		{name: "другая команда", text: "/start", want: false},
		{name: "слово без слэша", text: "скидки", want: false},
		{name: "скидки в тексте", text: "а какие /скидки сейчас", want: false},
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
