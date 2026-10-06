package app

import (
	"bytes"
	"errors"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/collab"
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
	room := collabRoom()
	invRoom := invCollabRoom()

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
		{
			name: "приёмка: открытые совместные приёмки",
			file: "receive.html",
			data: map[string]any{
				"Supplier": nil, "Suppliers": []any{supplier}, "Error": "",
				"Open": []collab.Session{collabRoom()},
			},
		},
		{
			name: "приёмка: хост совместной приёмки",
			file: "receive.html",
			data: map[string]any{
				"Supplier": supplier, "Suppliers": []any{}, "Error": "",
				"Room": &room, "IsGuest": false,
				"Others": []collab.Session{room},
			},
		},
		{
			name: "приёмка: гость совместной приёмки",
			file: "receive.html",
			data: map[string]any{
				"Supplier": supplier, "Suppliers": []any{}, "Error": "",
				"Room": &room, "IsGuest": true,
			},
		},
		{
			// «Проверка цен»: строка с посчитанной наценкой и строка, где
			// данных не хватает (пустые Markup/SBox) — шаблон обязан не упасть.
			name: "проверка цен: наценка и неполные данные",
			file: "price_check.html",
			data: map[string]any{"Rows": []any{
				map[string]any{
					"ProductID": "p-1", "Name": "Сыр", "InternalCode": "00001234",
					"GroupName": "Молочка/Сыры", "VatIn": "20",
					"Markup": "53.33", "Missing": "", "SBoxMarkup": "186.67", "SBoxMissing": "",
					"SBox": map[string]any{"Sale": "450.00", "Buy": "300.00", "VatOut": "20", "VatIn": "20"},
				},
				map[string]any{
					"ProductID": "p-2", "Name": "Без данных", "InternalCode": "",
					"GroupName": "Молочка/Сыры", "VatIn": "",
					"Markup": "", "Missing": "цена продажи, закупочная цена, наш НДС, входящий НДС",
					"SBoxMarkup": "", "SBoxMissing": "цена продажи, закупочная цена, наш НДС, входящий НДС",
					"SBox": map[string]any{},
				},
			}},
		},
		{
			// «Инвентаризация»: выбор вида — список видов и пустой список.
			name: "инвентаризация: виды инвентаризации",
			file: "goods_inventory.html",
			data: map[string]any{
				"Error": "", "Types": []string{"Заморозка", "Сопутка"}, "StoreReady": true,
			},
		},
		{
			name: "инвентаризация: нет видов, склад не настроен",
			file: "goods_inventory.html",
			data: map[string]any{
				"Error": "", "Types": []string{}, "StoreReady": false,
			},
		},
		{
			// «Инвентаризация»: сканирование вида — группа для клиента.
			name: "инвентаризация: сканирование вида",
			file: "goods_inventory_scan.html",
			data: map[string]any{
				"Type": "Заморозка", "GroupJSON": `[{"c":"00001234","n":"Стейк","w":1}]`,
				"Lengths": "29,33", "StoreReady": true, "Error": "",
			},
		},
		{
			// Пустые строки — страховка от nil-полей: шаблон обязан не упасть.
			name: "инвентаризация: сканирование, пустые данные",
			file: "goods_inventory_scan.html",
			data: map[string]any{
				"Type": "", "GroupJSON": "", "Lengths": "",
				"StoreReady": false, "Error": "",
			},
		},
		{
			// «Инвентаризация»: выбор вида — список идущих совместных
			// инвентаризаций, из него подключаются гости.
			name: "инвентаризация: идущие совместные инвентаризации",
			file: "goods_inventory.html",
			data: map[string]any{
				"Error": "", "Types": []string{"Заморозка"}, "StoreReady": true,
				"Open": []collab.Session{invCollabRoom()},
			},
		},
		{
			// «Инвентаризация»: страница хоста — комната и чужая инвентаризация
			// того же вида (подсказка, иначе вид проведут дважды).
			name: "инвентаризация: хост совместной инвентаризации",
			file: "goods_inventory_scan.html",
			data: map[string]any{
				"Type": "Заморозка", "GroupJSON": `[{"c":"00001234","n":"Стейк","w":1}]`,
				"Lengths": "29,33", "StoreReady": true, "Error": "",
				"Room": &invRoom, "IsGuest": false,
				"Others": []collab.Session{invCollabRoom()},
			},
		},
		{
			// «Инвентаризация»: страница гостя — своя кнопка «Отправить сканы»
			// вместо проведения, предпросмотра нет.
			name: "инвентаризация: гость совместной инвентаризации",
			file: "goods_inventory_scan.html",
			data: map[string]any{
				"Type": "Заморозка", "GroupJSON": `[{"c":"00001234","n":"Стейк","w":1}]`,
				"Lengths": "29,33", "StoreReady": true, "Error": "",
				"Room": &invRoom, "IsGuest": true,
			},
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

// collabRoom — комната совместной приёмки для проверки страницы: два гостя,
// первый заход отправил, второй ещё сканирует.
func collabRoom() collab.Session {
	return collab.Session{
		ID:        "room1",
		Kind:      collab.KindReceive,
		Ref:       "1",
		Title:     "ООО Тест",
		CreatedAt: time.Date(2026, time.October, 1, 9, 30, 0, 0, time.UTC),
		GuestSeq:  2,
		Guests: []collab.Guest{
			{ID: "g1", Name: "Гость 1", Status: collab.GuestReady, Chunks: 1, Rows: 12},
			{ID: "g2", Name: "Гость 2", Status: collab.GuestScanning},
		},
	}
}

// invCollabRoom — комната совместной инвентаризации для проверки страницы вида:
// Ref — вид инвентаризации (не поставщик), два гостя: первый отправил, второй
// ещё сканирует.
func invCollabRoom() collab.Session {
	return collab.Session{
		ID:        "inv1",
		Kind:      collab.KindInventory,
		Ref:       "Заморозка",
		Title:     "Заморозка",
		CreatedAt: time.Date(2026, time.October, 6, 11, 5, 0, 0, time.UTC),
		GuestSeq:  2,
		Guests: []collab.Guest{
			{ID: "g1", Name: "Гость 1", Status: collab.GuestReady, Chunks: 1, Rows: 7},
			{ID: "g2", Name: "Гость 2", Status: collab.GuestScanning},
		},
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
