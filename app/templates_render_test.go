package app

import (
	"bytes"
	"errors"
	"html/template"
	"os"
	"path/filepath"
	"strings"
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

// TestNavCopiesInSync — страницы на http.ServeFile (index, refgo,
// refgo_checkagainst, qrcodes) директив не исполняют: блок меню в них лежит
// СТАТИЧНОЙ копией частиала _nav.html, и копию переносят руками. Без этой
// проверки пункт, добавленный в частиал, молча не появляется в меню главной и
// соседних страниц: 25.09.2026 там разом не хватало «Вывод из продажи»,
// «Печать бланков», «Скидки» и всего раздела «Внутренние задачи».
func TestNavCopiesInSync(t *testing.T) {
	const dir = "../internal/delivery/web/templates"

	want, err := navBlock(dir + "/_nav.html")
	if err != nil {
		t.Fatalf("частиал меню: %v", err)
	}
	for _, file := range []string{"index.html", "qrcodes.html", "refgo.html", "refgo_checkagainst.html"} {
		got, err := navBlock(dir + "/" + file)
		if err != nil {
			t.Errorf("%s: %v", file, err)

			continue
		}
		if got != want {
			t.Errorf("%s: статичная копия меню разошлась с _nav.html (копия %d симв, частиал %d) — перенесите блок <nav id=\"sidebar\">…</nav> из частиала как есть",
				file, len(got), len(want))
		}
	}
}

// navBlock вырезает блок <nav id="sidebar">…</nav>: у частиала и статичных
// копий он обязан совпадать дословно (шапка и окружение страниц — своё).
func navBlock(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(raw)
	start := strings.Index(text, `<nav id="sidebar"`)
	if start < 0 {
		return "", errors.New(`блок <nav id="sidebar"> не найден`)
	}
	rel := strings.Index(text[start:], "</nav>")
	if rel < 0 {
		return "", errors.New(`блок <nav id="sidebar"> не закрыт`)
	}

	return text[start : start+rel+len("</nav>")], nil
}
