package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

// OrderSchedulesRepository — хранилище вариантов заказа товара у поставщиков
// (таблица product_order_schedules). Владелец записи — каталог: пишет карточка
// позиции, читает автозаказ через методы этого юзкейса.
type OrderSchedulesRepository interface {
	ListProductOrderSchedules(ctx context.Context, productID string) ([]domain.OrderSchedule, error)
	// SaveProductOrderSchedules сохраняет набор вариантов: строки с id
	// обновляются, без id — вставляются, отсутствующие в списке — удаляются.
	SaveProductOrderSchedules(ctx context.Context, productID string, list []domain.OrderSchedule) error
}

// ProductSuppliers — поставщики, у которых есть внешний код товара
// (product_supplier_barcodes). Владелец таблицы — приёмка, поэтому каталог
// спрашивает её методом (реализация *rucase.BarcodeEditor в di.go), а свои
// таблицы не читает.
type ProductSuppliers interface {
	SuppliersByProduct(ctx context.Context, productID string) ([]string, error)
}

// SupplierDirectory — справочник поставщиков: названия для выпадающего списка
// формы. Таблица suppliers принадлежит модулю mssuppliers — читаем его методом
// List (реализация *msu.MSSuppliersUseCase в di.go).
type SupplierDirectory interface {
	List(ctx context.Context) ([]domain.Supplier, error)
}

// SetOrderScheduleStore подключает хранилище вариантов заказа, справочник
// поставщиков и список поставщиков товара (di.go после создания юзкейса;
// nil — страница не работает, остальной каталог живёт).
func (uc *GoodsUseCase) SetOrderScheduleStore(
	schedules OrderSchedulesRepository,
	suppliers SupplierDirectory,
	productSuppliers ProductSuppliers,
) {
	uc.schedules = schedules
	uc.suppliers = suppliers
	uc.productSuppliers = productSuppliers
}

// ListOrderSchedules возвращает варианты заказа товара и поставщиков для
// выпадающего списка: только те, у кого есть внешний код этого товара —
// заказывать у поставщика без кода товара нельзя.
func (uc *GoodsUseCase) ListOrderSchedules(ctx context.Context, productID string) ([]domain.OrderSchedule, []domain.Supplier, error) {
	done := metrics.Track(trackPkg, "ListOrderSchedules")
	defer done()

	if uc.schedules == nil || uc.suppliers == nil || uc.productSuppliers == nil {
		return nil, nil, errors.New("варианты заказа не подключены")
	}

	list, err := uc.schedules.ListProductOrderSchedules(ctx, productID)
	if err != nil {
		return nil, nil, err
	}
	suppliers, _, err := uc.productSupplierOptions(ctx, productID)
	if err != nil {
		return nil, nil, err
	}

	return list, suppliers, nil
}

// productSupplierOptions возвращает поставщиков товара (по внешним кодам)
// в виде справочных записей и множество их id.
func (uc *GoodsUseCase) productSupplierOptions(ctx context.Context, productID string) ([]domain.Supplier, map[string]struct{}, error) {
	ids, err := uc.productSuppliers.SuppliersByProduct(ctx, productID)
	if err != nil {
		return nil, nil, fmt.Errorf("поставщики товара %s: %w", productID, err)
	}
	known := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		known[id] = struct{}{}
	}

	all, err := uc.suppliers.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("справочник поставщиков: %w", err)
	}
	out := make([]domain.Supplier, 0, len(ids))
	for _, s := range all {
		if _, ok := known[s.ID]; ok {
			out = append(out, s)
		}
	}

	return out, known, nil
}

// SaveOrderSchedules нормализует и сохраняет набор вариантов заказа товара:
// вариант с id обновляется (id сохраняется — на него будет ссылаться автозаказ),
// без id — вставляется, отсутствующие в форме — удаляются. Пустой список —
// товар без вариантов заказа.
func (uc *GoodsUseCase) SaveOrderSchedules(ctx context.Context, productID string, list []domain.OrderSchedule) error {
	done := metrics.Track(trackPkg, "SaveOrderSchedules")
	defer done()

	if uc.schedules == nil || uc.suppliers == nil || uc.productSuppliers == nil {
		return errors.New("варианты заказа не подключены")
	}
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return errors.New("не передан товар")
	}

	product, err := uc.repo.GetProduct(ctx, productID)
	if err != nil {
		return fmt.Errorf("проверить товар %s: %w", productID, err)
	}
	if product == nil {
		return fmt.Errorf("товар %s: %w", productID, domain.ErrProductNotFound)
	}

	_, known, err := uc.productSupplierOptions(ctx, productID)
	if err != nil {
		return err
	}

	if list, err = normalizeOrderSchedules(productID, list, known); err != nil {
		return err
	}

	return uc.schedules.SaveProductOrderSchedules(ctx, productID, list)
}

// normalizeOrderSchedules проверяет варианты заказа: поставщик — из числа
// поставщиков товара, непустое название без дублей в паре, хотя бы один день
// заказа, дни недели 1..7 без дублей, неотрицательная задержка.
func normalizeOrderSchedules(productID string, list []domain.OrderSchedule, known map[string]struct{}) ([]domain.OrderSchedule, error) {
	out := make([]domain.OrderSchedule, 0, len(list))
	seen := make(map[string]struct{}, len(list))

	for i := range list {
		item := list[i]
		item.ProductID = productID
		item.SupplierName = ""
		item.SupplierID = strings.TrimSpace(item.SupplierID)
		item.Name = strings.TrimSpace(item.Name)

		if item.SupplierID == "" {
			return nil, fmt.Errorf("вариант %d: не выбран поставщик", i+1)
		}
		if _, ok := known[item.SupplierID]; !ok {
			return nil, fmt.Errorf("вариант %d: у товара нет внешнего кода этого поставщика", i+1)
		}
		if item.Name == "" {
			return nil, fmt.Errorf("вариант %d: не заполнено название (например «Общий»)", i+1)
		}
		if utf8.RuneCountInString(item.Name) > 100 {
			return nil, fmt.Errorf("вариант %d: название слишком длинное (максимум 100 символов)", i+1)
		}

		key := item.SupplierID + "\x00" + strings.ToLower(item.Name)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("вариант %q у этого поставщика уже есть", item.Name)
		}
		seen[key] = struct{}{}

		var err error
		if item.OrderDays, err = normalizeWeekdays(item.OrderDays); err != nil {
			return nil, fmt.Errorf("вариант %q: дни заказа: %w", item.Name, err)
		}
		if len(item.OrderDays) == 0 {
			return nil, fmt.Errorf("вариант %q: выберите хотя бы один день заказа", item.Name)
		}
		if item.DeliveryDays, err = normalizeWeekdays(item.DeliveryDays); err != nil {
			return nil, fmt.Errorf("вариант %q: дни доставки: %w", item.Name, err)
		}
		if item.DelayDays != nil && *item.DelayDays < 0 {
			return nil, fmt.Errorf("вариант %q: макс. дней между заказом и доставкой не может быть отрицательным", item.Name)
		}

		out = append(out, item)
	}

	return out, nil
}

// normalizeWeekdays проверяет дни недели 1..7, убирает дубли и сортирует.
func normalizeWeekdays(days []int16) ([]int16, error) {
	if len(days) == 0 {
		return nil, nil
	}

	seen := make(map[int16]struct{}, len(days))
	out := make([]int16, 0, len(days))
	for _, d := range days {
		if d < 1 || d > 7 {
			return nil, fmt.Errorf("день %d вне диапазона 1..7 (1=Пн ... 7=Вс)", d)
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	slices.Sort(out)

	return out, nil
}
