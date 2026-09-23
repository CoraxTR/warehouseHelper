package app

import (
	"bytes"
	"html/template"
	"path/filepath"
	"testing"
)

// TestTemplatesRender — страховка от «белого экрана».
//
// Страницы приёмки и сборки коробки разбираются при старте приложения
// (template.Must(template.ParseFiles(...))), поэтому синтаксическую ошибку
// ловит уже запуск. Но проверка КОНТЕКСТОВ (html/template escaper) выполняется
// только при Execute: порванный атрибут, незакрытая кавычка или обрезанная
// строка проходят и парсинг, и `go build`, и линтер, а клиент получает пустую
// страницу (Execute падает и не пишет тело). Ровно это случилось 22.09.2026 с
// тегом <audio> в receive.html: атрибут src="data:audio/wav;base64,…" был обрезан
// и страница приёмки открывалась белым экраном.
//
// cwd теста — каталог app/, те же относительные пути ../internal/..., что и у
// запущенного бинаря, поэтому шаблоны берутся из настоящих файлов.
func TestTemplatesRender(t *testing.T) {
	const dir = "../internal/delivery/web/templates"

	supplier := map[string]any{"ID": "1", "Name": "ООО Тест"}
	cases := []struct {
		name string
		file string
		data any
	}{
		{
			name: "приёмка: поставщик выбран",
			file: "receive.html",
			data: map[string]any{"Supplier": supplier, "Suppliers": []any{}, "Error": ""},
		},
		{
			name: "приёмка: выбор поставщика",
			file: "receive.html",
			data: map[string]any{"Supplier": nil, "Suppliers": []any{supplier}, "Error": ""},
		},
		{
			name: "приёмка: поставщик не найден",
			file: "receive.html",
			data: map[string]any{"Supplier": nil, "Suppliers": []any{}, "Error": "поставщик не найден"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmpl, err := template.ParseFiles(dir+"/"+c.file, dir+"/_nav.html")
			if err != nil {
				t.Fatalf("разбор %s: %v", c.file, err)
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, c.data); err != nil {
				t.Fatalf("исполнение %s: %v", c.file, err)
			}
			if buf.Len() < 2000 {
				t.Fatalf("%s: подозрительно короткий результат — %d байт (пустой экран?)", c.file, buf.Len())
			}
		})
	}
}

// TestTemplatesParse — все шаблоны каталога должны разбираться: ловит поломку в
// шаблонах, которые этот тест не исполняет (у них своя модель данных).
func TestTemplatesParse(t *testing.T) {
	files, err := filepath.Glob("../internal/delivery/web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("шаблоны не найдены — проверьте рабочий каталог теста")
	}
	for _, f := range files {
		if filepath.Base(f) == "_nav.html" {
			continue // частичный шаблон: разбирается вместе со страницей ({{define "nav"}})
		}
		tmpl, err := template.New(filepath.Base(f)).Funcs(tmplStubFuncs()).ParseFiles(f, "../internal/delivery/web/templates/_nav.html")
		if err != nil {
			t.Errorf("разбор %s: %v", f, err)
			continue
		}
		_ = tmpl
	}
}

// tmplStubFuncs — заглушки функций, которые обработчики добавляют через
// template.FuncMap (своя у каждой страницы). Для разбора достаточно, чтобы имя
// существовало: типы проверяются только при исполнении, а исполняем мы лишь
// страницы приёмки (TestTemplatesRender).
func tmplStubFuncs() template.FuncMap {
	stub := func(...any) any { return nil }
	names := []string{
		"coeff", "date", "dates", "folderCtx", "minus", "monthName", "monthSlug",
		"nextMonth", "plus", "prevMonth", "seq", "source", "todayDay",
	}
	fm := make(template.FuncMap, len(names))
	for _, n := range names {
		fm[n] = stub
	}
	return fm
}
