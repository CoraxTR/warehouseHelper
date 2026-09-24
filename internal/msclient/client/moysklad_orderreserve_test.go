package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/msclient/workerpool"
)

const (
	orderIDTest = "053b3dfc-926b-11f1-0a80-135d00113455"
	cancelledID = "8737d8a5-c0b9-11e3-ac8e-002590a28eca"
	// whAuthHeader / othAuthHeader — Authorization тестовых ключей пула
	// (складской / общий); goconst: не дублировать литералы по пакету.
	whAuthHeader  = "Bearer key-wh"
	othAuthHeader = "Bearer key-oth"
)

// orderRootTest — сырое тело GET ЗАКАЗА: state (отменён) + meta-ссылка на
// позиции. Строк позиций в корне МС НЕ отдаёт (живой API 24.09.2026, заказ
// 19379) — тесты держат именно эту форму, а строки приходят отдельным запросом
// к .../positions. Фикстура с rows внутри корня (как было до 24.09.2026) про
// реальный МС ничего не говорит: на ней прод-баг «резерв не снялся» и прожил.
func orderRootTest(r *http.Request, size int) string {
	return `{"id":"` + orderIDTest + `","name":"19191",` +
		`"state":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/` + cancelledID + `"}},` +
		`"positions":{"meta":{"href":"http://` + r.Host + `/entity/customerorder/` + orderIDTest + `/positions",` +
		`"type":"customerorderposition","size":` + strconv.Itoa(size) + `}}}`
}

// positionsRowsTest — ответ .../positions: 3 строки, две из них в резерве.
const positionsRowsTest = `{"meta":{"size":3},"rows":[` +
	`{"id":"pos-1","quantity":0.657,"reserve":0.657,"price":1000.0,"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/a02a"}}},` +
	`{"id":"pos-2","quantity":2,"reserve":0,"price":500.0,"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/d00d"}}},` +
	`{"id":"pos-3","quantity":0.5,"reserve":0.5,"price":2000.0,"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/b00b"}}}` +
	`]}`

// positionsPathTest — путь, по которому клиент просит строки позиций.
const positionsPathTest = "/entity/customerorder/" + orderIDTest + "/positions"

// noPosHrefRootTest — корень заказа без meta-ссылки на позиции (в МС так не
// бывает, но запрос по пустому href ушёл бы в никуда — ждём явную ошибку).
const noPosHrefRootTest = `{"id":"` + orderIDTest + `","name":"19191","positions":{"meta":{"size":0}}}`

// truncatedRowsTest — неполный ответ .../positions: 2 строки вместо 3.
const truncatedRowsTest = `{"meta":{"size":2},"rows":[` +
	`{"id":"pos-1","quantity":0.657,"reserve":0.657,"price":1000.0,` +
	`"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/a02a"}}},` +
	`{"id":"pos-2","quantity":2,"reserve":0,"price":500.0,` +
	`"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/d00d"}}}]}`

// newReserveTestClient поднимает httptest-сервер и клиент на нём (воркерпул
// валидирует ключ отдельным запросом к организации — на него отвечает orgOK).
func newReserveTestClient(t *testing.T, handler http.HandlerFunc) *MSAPIClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	msCfg := &config.MSConfig{
		URLstart:         server.URL + "/entity/",
		AuthHeader:       "Bearer",
		Refs:             &config.MSRefs{OrgID: "org-test"},
		WarehouseAPIKEYS: []config.MSWorker{{Name: "wh-worker", APIKey: "key-wh"}},
		OthersAPIKEYS:    []config.MSWorker{{Name: "oth-worker", APIKey: "key-oth"}},
		TimeSpan:         time.Second,
		RequestCap:       1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	t.Cleanup(pool.Stop)

	return &MSAPIClient{workerpool: pool, msConfig: msCfg}
}

// orgOK отвечает на проверку ключей пулом (GET организации) и сообщает,
// обработан ли запрос.
func orgOK(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == orgTestPath {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"org-test"}`))
		return true
	}
	return false
}

// reserveFake — подделка МС для снятия резерва: корень заказа без строк позиций
// + отдельный ответ .../positions + перехват PUT. rowsSize — обещанный корнем
// размер позиций, rows — что реально придёт из .../positions.
type reserveFake struct {
	rowsSize int
	rows     string
	gets     []string // пути GET (кроме проверки ключа)
	getAuths []string
	putCalls int // сколько PUT-ов пришло (повтор воркерпула тест не должен пропустить)
	putPath  string
	putAuth  string
	putBody  []byte
}

func (f *reserveFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPut:
			f.putCalls++
			f.putPath = r.URL.Path
			f.putAuth = r.Header.Get("Authorization")

			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("чтение тела PUT: %v", err)
			}
			f.putBody = body
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == positionsPathTest:
			f.gets = append(f.gets, r.URL.Path)
			f.getAuths = append(f.getAuths, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(f.rows))
		case r.URL.Path == "/entity/customerorder/"+orderIDTest:
			f.gets = append(f.gets, r.URL.Path)
			f.getAuths = append(f.getAuths, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(orderRootTest(r, f.rowsSize)))
		default:
			t.Errorf("неожиданный запрос %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// putPositions разбирает тело PUT: эхо шапки заказа + строки позиций.
type putPositions struct {
	Name      string           `json:"name"`
	State     map[string]any   `json:"state"`
	Positions []map[string]any `json:"positions"`
}

// putBodyDecoded достаёт из перехваченного PUT разобранное тело.
func putBodyDecoded(t *testing.T, f *reserveFake) putPositions {
	t.Helper()

	if f.putCalls == 0 {
		t.Fatal("PUT не ушёл: резерв не снят")
	}

	var body putPositions
	if err := json.Unmarshal(f.putBody, &body); err != nil {
		t.Fatalf("тело PUT не json: %v", err)
	}
	return body
}

// productHREF достаёт href товара строки позиции из тела PUT.
func productHREF(p map[string]any) string {
	assortment, ok := p["assortment"].(map[string]any)
	if !ok {
		return ""
	}
	meta, ok := assortment["meta"].(map[string]any)
	if !ok {
		return ""
	}
	href, ok := meta["href"].(string)
	if !ok {
		return ""
	}
	return href
}

// TestClearOrderReserves — строки берутся из .../positions (в корне заказа их
// нет), PUT уходит эхом шапки заказа с обнулённым reserve у всех позиций:
// прочие поля строк и порядок не тронуты, state не перенесён.
func TestClearOrderReserves(t *testing.T) {
	fake := &reserveFake{rowsSize: 3, rows: positionsRowsTest}
	msac := newReserveTestClient(t, fake.handler(t))

	if err := msac.ClearOrderReserves(context.Background(), orderIDTest); err != nil {
		t.Fatalf("ClearOrderReserves() error: %v", err)
	}

	if len(fake.gets) != 2 || fake.gets[0] != "/entity/customerorder/"+orderIDTest || fake.gets[1] != positionsPathTest {
		t.Errorf("GET-запросы = %v, want [заказ, позиции]", fake.gets)
	}
	// Ключ GET поимённо не проверяем: складской воркер подхватывает и общие
	// задачи (warehouseWorkerLoop читает и otherTasks) — кто возьмёт задачу из
	// пула, зависит от гонки. Важно лишь, что ходили ключом пула, а правка
	// заказа (PUT) ушла складским.
	for i, auth := range fake.getAuths {
		if auth != whAuthHeader && auth != othAuthHeader {
			t.Errorf("GET %d: Authorization = %q, want ключ пула", i, auth)
		}
	}
	if fake.putPath != "/entity/customerorder/"+orderIDTest {
		t.Errorf("PUT path = %s, want customerorder endpoint", fake.putPath)
	}
	if fake.putAuth != whAuthHeader {
		t.Errorf("PUT Authorization = %q, want складской ключ (SubmitWarehouse)", fake.putAuth)
	}
	if fake.putCalls != 1 {
		t.Errorf("PUT-ов = %d, want 1", fake.putCalls)
	}

	body := putBodyDecoded(t, fake)
	if body.Name != "19191" {
		t.Errorf("эхо тела нарушено: name = %q", body.Name)
	}
	// state из эха GET в PUT не переносится (решение владельца 24.09.2026):
	// иначе PUT заново утверждал бы статус, затирая расформирование заказа.
	if body.State != nil {
		t.Errorf("PUT перенёс state из эха GET: %v, want отсутствие", body.State)
	}
	checkPutPositions(t, body.Positions)
}

// checkPutPositions сверяет строки тела PUT: reserve обнулён у всех, id и порядок
// строк — как пришли из .../positions, остальные поля строк не тронуты. PUT
// заменяет раздел целиком, поэтому проверяются ЗНАЧЕНИЯ, а не наличие полей.
func checkPutPositions(t *testing.T, got []map[string]any) {
	t.Helper()

	// Ожидаемые значения — из positionsRowsTest: менять вместе с фикстурой.
	wantIDs := []string{"pos-1", "pos-2", "pos-3"}
	wantQty := []float64{0.657, 2, 0.5}
	wantPrice := []float64{1000, 500, 2000}
	wantProduct := []string{"a02a", "d00d", "b00b"}

	if len(got) != len(wantIDs) {
		t.Fatalf("positions = %d строк, want %d", len(got), len(wantIDs))
	}

	for i, p := range got {
		if reserve, ok := p["reserve"].(float64); !ok || reserve != 0 {
			t.Errorf("position %d: reserve = %v, want 0", i, p["reserve"])
		}
		if p["id"] != wantIDs[i] {
			t.Errorf("position %d: id = %v, want %s (порядок строк из .../positions)", i, p["id"], wantIDs[i])
		}
		if qty, ok := p["quantity"].(float64); !ok || qty != wantQty[i] {
			t.Errorf("position %d: quantity = %v, want %v", i, p["quantity"], wantQty[i])
		}
		if price, ok := p["price"].(float64); !ok || price != wantPrice[i] {
			t.Errorf("position %d: price = %v, want %v", i, p["price"], wantPrice[i])
		}
		if href := productHREF(p); !strings.HasSuffix(href, "/"+wantProduct[i]) {
			t.Errorf("position %d: assortment = %q, want товар %s", i, href, wantProduct[i])
		}
	}
}

// TestClearOrderReserves_NoReserveSkipsPut — резерва нет: PUT не уходит, ошибки нет.
func TestClearOrderReserves_NoReserveSkipsPut(t *testing.T) {
	rows := strings.ReplaceAll(positionsRowsTest, `"reserve":0.657`, `"reserve":0`)
	rows = strings.ReplaceAll(rows, `"reserve":0.5`, `"reserve":0`)

	fake := &reserveFake{rowsSize: 3, rows: rows}
	msac := newReserveTestClient(t, fake.handler(t))

	if err := msac.ClearOrderReserves(context.Background(), orderIDTest); err != nil {
		t.Fatalf("ClearOrderReserves() error: %v", err)
	}
	if fake.putCalls != 0 {
		t.Fatal("PUT ушёл при нулевом резерве — менять нечего")
	}
}

// TestClearOrderReserves_RowsMissing — корень заказа сообщает о позициях, а
// .../positions строк не вернул: ошибка, а не тихий no-op (иначе резерв снова
// останется висеть, а сборка отчитается об успехе — прод-баг 24.09.2026).
func TestClearOrderReserves_RowsMissing(t *testing.T) {
	fake := &reserveFake{rowsSize: 3, rows: `{"meta":{"size":0},"rows":[]}`}
	msac := newReserveTestClient(t, fake.handler(t))

	err := msac.ClearOrderReserves(context.Background(), orderIDTest)
	if err == nil {
		t.Fatal("пустой ответ .../positions при size=3: want ошибку, got nil")
	}
	if !strings.Contains(err.Error(), "строк 0") {
		t.Errorf("текст ошибки = %v, want с числами позиций и строк", err)
	}
	if fake.putCalls != 0 {
		t.Error("PUT ушёл без строк позиций")
	}
}

// TestClearOrderReserves_TruncatedRows — обещано 3 позиции, пришло 2: PUT
// заменяет раздел целиком, поэтому неполный состав — ошибка (иначе позиция
// заказа потерялась бы молча).
func TestClearOrderReserves_TruncatedRows(t *testing.T) {
	fake := &reserveFake{rowsSize: 3, rows: truncatedRowsTest}
	msac := newReserveTestClient(t, fake.handler(t))

	err := msac.ClearOrderReserves(context.Background(), orderIDTest)
	if err == nil {
		t.Fatal("неполный ответ .../positions: want ошибку, got nil")
	}
	if !strings.Contains(err.Error(), "строк 2") {
		t.Errorf("текст ошибки = %v, want с числами позиций и строк", err)
	}
	if fake.putCalls != 0 {
		t.Error("PUT ушёл с неполным составом позиций")
	}
}

// TestClearOrderReserves_NoPositions — заказ без позиций: PUT не нужен, ошибки нет.
func TestClearOrderReserves_NoPositions(t *testing.T) {
	fake := &reserveFake{rowsSize: 0, rows: `{"meta":{"size":0},"rows":[]}`}
	msac := newReserveTestClient(t, fake.handler(t))

	if err := msac.ClearOrderReserves(context.Background(), orderIDTest); err != nil {
		t.Fatalf("ClearOrderReserves() error: %v", err)
	}
	if fake.putCalls != 0 {
		t.Error("PUT ушёл по заказу без позиций")
	}
}

// TestClearOrderReserves_NoPositionHref — корень без ссылки на позиции: явная
// ошибка, запрос к .../positions не уходит (иначе оператор получал бы
// «unsupported protocol scheme ""» от пустого URL).
func TestClearOrderReserves_NoPositionHref(t *testing.T) {
	posRequests := 0
	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == positionsPathTest {
			posRequests++
		}
		_, _ = w.Write([]byte(noPosHrefRootTest))
	})

	err := msac.ClearOrderReserves(context.Background(), orderIDTest)
	if err == nil {
		t.Fatal("корень без ссылки на позиции: want ошибку, got nil")
	}
	if !strings.Contains(err.Error(), "нет ссылки на позиции") {
		t.Errorf("текст ошибки = %v, want про отсутствие ссылки", err)
	}
	if posRequests != 0 {
		t.Errorf("запросов к .../positions = %d, want 0", posRequests)
	}
}

// TestFetchOrderState — id статуса из state.meta.href заказа.
func TestFetchOrderState(t *testing.T) {
	fake := &reserveFake{rowsSize: 0, rows: `{"rows":[]}`}
	msac := newReserveTestClient(t, fake.handler(t))

	stateID, err := msac.FetchOrderState(context.Background(), orderIDTest)
	if err != nil {
		t.Fatalf("FetchOrderState() error: %v", err)
	}
	if stateID != cancelledID {
		t.Errorf("state = %q, want %q", stateID, cancelledID)
	}
}

// TestFetchOrderState_NoState — у заказа нет state: пустая строка, не ошибка.
func TestFetchOrderState_NoState(t *testing.T) {
	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + orderIDTest + `","name":"19191"}`))
	})

	stateID, err := msac.FetchOrderState(context.Background(), orderIDTest)
	if err != nil {
		t.Fatalf("FetchOrderState() error: %v", err)
	}
	if stateID != "" {
		t.Errorf("state = %q, want пусто", stateID)
	}
}
