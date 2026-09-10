// Печать бланков заказов (раздел «Заказы» МС): список заказов с плановой датой
// доставки на заданный день и печать бланков выделенных заказов одним PDF.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/msclient/client"
)

// ErrNoOrdersSelected — на печать не передано ни одного заказа.
var ErrNoOrdersSelected = errors.New("не выбран ни один заказ")

// FormsClient — контракт выборки заказов на день и справочника статусов,
// реализуется *client.MSAPIClient.
type FormsClient interface {
	FetchOrdersByDeliveryDate(ctx context.Context, day time.Time) ([]client.MSOrder, error)
	// FetchOrderStates — id статуса → имя: запасной источник имени статуса,
	// когда МС проигнорировал expand=state в списке (проверено на agent
	// при filter=name — МС вправе не раскрывать вложенные объекты).
	FetchOrderStates(ctx context.Context) (map[string]string, error)
}

// FormPrinter — печать бланков (нижний слой pdfexport.Service): качает бланки
// заказов по их id и сливает пачку в один PDF. Возвращает путь к файлу и id
// заказов, чьи бланки получить не удалось: они не роняют пачку, но и не
// пропадают молча — вызывающий обязан сказать оператору.
type FormPrinter interface {
	GetMultipleOrdersPDF(ctx context.Context, ids []string) (path string, failed []string, err error)
}

// FormsUseCase — сценарии страницы «Печать бланков».
type FormsUseCase struct {
	ms      FormsClient
	printer FormPrinter
}

// NewFormsUseCase создаёт сценарии печати бланков: заказы — из МС (клиент),
// печать — нижним слоем pdfexport. Шов печати отдельный: подбор позиций
// (UseCase) и печать бланков друг от друга не зависят.
func NewFormsUseCase(ms FormsClient, printer FormPrinter) *FormsUseCase {
	return &FormsUseCase{ms: ms, printer: printer}
}

// FormRow — строка списка заказов на странице печати бланков.
type FormRow struct {
	ID       string // id заказа МС — уходит на печать
	Name     string // номер заказа
	State    string // имя статуса
	Delivery string // плановая дата доставки, ДД.ММ.ГГГГ
}

// FormsByDate — заказы с плановой датой доставки в указанный день (день — в TZ
// учётки МС, конвертация в клиенте). Пустой день — пустой список без ошибки.
func (uc *FormsUseCase) FormsByDate(ctx context.Context, day time.Time) ([]FormRow, error) {
	done := metrics.Track(trackPkg, "FormsByDate")
	defer done()

	orders, err := uc.ms.FetchOrdersByDeliveryDate(ctx, day)
	if err != nil {
		return nil, fmt.Errorf("fetch orders by delivery date: %w", err)
	}

	states := uc.stateNames(ctx, orders)

	rows := make([]FormRow, 0, len(orders))
	for i := range orders {
		o := &orders[i]
		rows = append(rows, FormRow{
			ID:       o.ID,
			Name:     o.Name,
			State:    orDash(stateName(o, states)),
			Delivery: shortDate(o.DeliveryPlannedMoment),
		})
	}

	sortFormsByNumber(rows)

	return rows, nil
}

// PrintForms собирает бланки выделенных заказов в один PDF. Порядок печати —
// порядок выделения (как в списке). id заказов, бланки которых получить не
// удалось, возвращаются вызывающему, чтобы тот показал их оператору.
func (uc *FormsUseCase) PrintForms(ctx context.Context, ids []string) (path string, failed []string, err error) {
	done := metrics.Track(trackPkg, "PrintForms")
	defer done()

	ids = cleanIDs(ids)
	if len(ids) == 0 {
		return "", nil, ErrNoOrdersSelected
	}

	path, failed, err = uc.printer.GetMultipleOrdersPDF(ctx, ids)
	if err != nil {
		return "", nil, fmt.Errorf("print forms: %w", err)
	}

	return path, failed, nil
}

// stateNames — имена статусов для строк списка. Из строк (expand=state) либо —
// для строк без имени — картой справочника, один запрос. Ошибка справочника не
// роняет список: статус останется «—» (вторичные данные); клиент залогировал.
func (uc *FormsUseCase) stateNames(ctx context.Context, orders []client.MSOrder) map[string]string {
	needsCatalog := false

	for i := range orders {
		if strings.TrimSpace(orders[i].State.Name) == "" && orders[i].StateID != "" {
			needsCatalog = true

			break
		}
	}

	if !needsCatalog {
		return nil
	}

	states, err := uc.ms.FetchOrderStates(ctx)
	if err != nil {
		return nil
	}

	return states
}

// stateName — имя статуса заказа: из строки, иначе из справочника по id статуса.
func stateName(o *client.MSOrder, states map[string]string) string {
	if name := strings.TrimSpace(o.State.Name); name != "" {
		return name
	}

	return strings.TrimSpace(states[o.StateID])
}

// cleanIDs убирает пустые id и дубликаты, сохраняя порядок выделения.
func cleanIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))

	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		if _, ok := seen[id]; ok {
			continue
		}

		seen[id] = struct{}{}

		out = append(out, id)
	}

	return out
}

// sortFormsByNumber упорядочивает строки по номеру заказа: числовые номера — по
// возрастанию числа («9» перед «10»), нечисловые — лексикографически.
func sortFormsByNumber(rows []FormRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Name, rows[j].Name

		numA, errA := strconv.Atoi(a)
		numB, errB := strconv.Atoi(b)

		if errA == nil && errB == nil {
			return numA < numB
		}

		return a < b
	})
}
