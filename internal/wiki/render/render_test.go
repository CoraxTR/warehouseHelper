package render

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want []string
	}{
		{name: "пустой текст", md: "", want: nil},
		{name: "без ссылок", md: "просто текст без ссылок", want: nil},
		{name: "одна ссылка", md: "см. [[РефГо]]", want: []string{"РефГо"}},
		{name: "trim пробелов у целей", md: "[[ РефГо ]] и [[Заказ]]", want: []string{"РефГо", "Заказ"}},
		{name: "дубликаты без учёта регистра", md: "[[РефГо]] [[рефго]] [[РЕФГО]]", want: []string{"РефГо"}},
		{name: "порядок первого вхождения", md: "[[Б]] [[А]] [[б]]", want: []string{"Б", "А"}},
		{name: "только пустые цели", md: "[[ ]] и [[  ]]", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLinks(tt.md)
			if !slices.Equal(got, tt.want) {
				t.Errorf("ExtractLinks(%q) = %v, want %v", tt.md, got, tt.want)
			}
		})
	}
}

func TestRender(t *testing.T) {
	special := `Кавычки "и" скобки (тест) 100%`

	tests := []struct {
		name       string
		md         string
		targets    map[string]string
		wantEmpty  bool     // ожидается пустой HTML
		want       []string // обязательные подстроки результата
		wantAbsent []string // подстроки, которых в результате быть не должно
	}{
		{
			name:    "существующая цель — ссылка с каноническим title",
			md:      "См. [[рефго]]",
			targets: map[string]string{"рефго": "РефГо"},
			want: []string{
				`href="/wiki/page?title=` + url.QueryEscape("РефГо") + `"`,
				`>РефГо</a>`,
			},
		},
		{
			name:    "отсутствующая цель — жирный текст",
			md:      "[[Неизвестно]]",
			targets: nil,
			want:    []string{`<strong>Неизвестно</strong>`},
		},
		{
			name:    "регистр цели не влияет на резолв",
			md:      "[[рефго]]",
			targets: map[string]string{"рефго": "РефГо"},
			want: []string{
				`href="/wiki/page?title=` + url.QueryEscape("РефГо") + `"`,
				`>РефГо</a>`,
			},
		},
		{
			name:    "спецсимволы в цели — корректный URL",
			md:      "[[" + special + "]]",
			targets: map[string]string{strings.ToLower(special): special},
			want:    []string{`href="/wiki/page?title=` + url.QueryEscape(special) + `"`},
		},
		{
			name:       "XSS вырезается",
			md:         `<script>alert(1)</script> и <iframe src="https://evil.example"></iframe>`,
			wantAbsent: []string{"script", "iframe"},
		},
		{
			// Инъекция через канонический заголовок: markdown-метасимволы
			// в заголовке не должны выводить ссылку наружу. Враждебный текст
			// остаётся текстом ссылки (экранирован), но не становится href.
			name:    "враждебный canonical не выходит за [text](url)",
			md:      "[[Кликни]]",
			targets: map[string]string{"кликни": `Кликни](https://evil.example)`},
			want: []string{
				`href="/wiki/page?title=` + url.QueryEscape(`Кликни](https://evil.example)`) + `"`,
			},
			wantAbsent: []string{`href="https:`}, // внешний href не появляется
		},
		{
			// Картинка-трекер в заголовке не должна стать тегом img.
			name:    "canonical с картинкой не рендерит img",
			md:      "[[бейдж]]",
			targets: map[string]string{"бейдж": `![бейдж](https://evil.example/pixel.png)`},
			want: []string{
				`href="/wiki/page?title=` + url.QueryEscape(`![бейдж](https://evil.example/pixel.png)`) + `"`,
			},
			wantAbsent: []string{"<img"},
		},
		{
			// javascript: в заголовке не может стать href ссылки
			// (href всегда /wiki/page?title=...; текст экранируется).
			name:    "canonical с javascript: остаётся текстом внутри вики-ссылки",
			md:      "[[ссылка]]",
			targets: map[string]string{"ссылка": `x](javascript:alert(1))`},
			want: []string{
				`href="/wiki/page?title=` + url.QueryEscape(`x](javascript:alert(1))`) + `"`,
			},
			wantAbsent: []string{`href="javascript:`},
		},
		{
			name:      "пустой контент — пустой HTML",
			md:        "",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.md, tt.targets)
			if err != nil {
				t.Fatalf("Render(%q) error: %v", tt.md, err)
			}

			if tt.wantEmpty {
				if string(got) != "" {
					t.Errorf("Render(%q) = %q, want пустой HTML", tt.md, string(got))
				}
			}
			for _, want := range tt.want {
				if !strings.Contains(string(got), want) {
					t.Errorf("Render(%q) = %q, want содержит %q", tt.md, string(got), want)
				}
			}

			for _, absent := range tt.wantAbsent {
				if strings.Contains(string(got), absent) {
					t.Errorf("Render(%q) содержит недопустимую подстроку %q: %q", tt.md, absent, string(got))
				}
			}
		})
	}
}
