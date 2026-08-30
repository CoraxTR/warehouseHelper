// Пакет usecase — сценарии работы со справочником поставщиков МойСклад:
// список, создание и редактирование (имя подтягивается из МС при сохранении),
// удаление. Валидация и нормализация полей — здесь.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"warehouseHelper/internal/decoderules"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/receiving"
)

// MSSuppliersRepository — контракт хранилища поставщиков, реализуется postgres-репозиторием.
type MSSuppliersRepository interface {
	ListSuppliers(ctx context.Context) ([]domain.Supplier, error)
	GetSupplier(ctx context.Context, id string) (*domain.Supplier, error)
	SaveSupplier(ctx context.Context, s *domain.Supplier) error
	DeleteSupplier(ctx context.Context, id string) error
}

// CounterpartyClient — контракт получения имени контрагента из МойСклад,
// реализуется *client.MSAPIClient.
type CounterpartyClient interface {
	FetchCounterpartyName(ctx context.Context, id string) (string, error)
}

// WikiSupplierSynchronizer — контракт синка страницы вики поставщика,
// реализуется *wucase.WikiUseCase (вики — отдельный модуль, его таблицы
// mssuppliers не трогает).
type WikiSupplierSynchronizer interface {
	SyncSupplierPage(ctx context.Context, supplierID, name string, orderDays, deliveryDays []int) error
}

// BarcodeLister — контракт чтения связок «внешний код → товар» поставщика,
// реализуется *rucase.BarcodeEditor (приёмка — отдельный модуль, владеет
// product_supplier_barcodes). Виджет живёт на странице поставщика, данные
// — приёмки.
type BarcodeLister interface {
	List(ctx context.Context, supplierID string) ([]receiving.BarcodeRef, error)
}

// MSSuppliersUseCase — сценарии работы с поставщиками.
type MSSuppliersUseCase struct {
	repo     MSSuppliersRepository
	ms       CounterpartyClient
	wiki     WikiSupplierSynchronizer
	barcodes BarcodeLister
}

// NewMSSuppliersUseCase создаёт сценарий с переданным хранилищем, MS-клиентом,
// синком вики и листером внешних кодов приёмки.
func NewMSSuppliersUseCase(repo MSSuppliersRepository, ms CounterpartyClient, wiki WikiSupplierSynchronizer, barcodes BarcodeLister) *MSSuppliersUseCase {
	return &MSSuppliersUseCase{repo: repo, ms: ms, wiki: wiki, barcodes: barcodes}
}

// List возвращает всех поставщиков, отсортированных по алфавиту (ORDER BY lower(name)).
func (uc *MSSuppliersUseCase) List(ctx context.Context) ([]domain.Supplier, error) {
	return uc.repo.ListSuppliers(ctx)
}

// Get возвращает поставщика по id; если нет — (nil, nil).
func (uc *MSSuppliersUseCase) Get(ctx context.Context, id string) (*domain.Supplier, error) {
	return uc.repo.GetSupplier(ctx, id)
}

// ListBarcodes возвращает связки «внешний код → товар» поставщика для
// виджета на странице поставщика. Без листера (тесты) — пустой список.
func (uc *MSSuppliersUseCase) ListBarcodes(ctx context.Context, supplierID string) ([]receiving.BarcodeRef, error) {
	if uc.barcodes == nil {
		return nil, nil
	}

	return uc.barcodes.List(ctx, supplierID)
}

// Create нормализует и валидирует данные, подтягивает имя из МС и создаёт
// поставщика. Если поставщик с таким id уже существует — domain.ErrSupplierExists.
func (uc *MSSuppliersUseCase) Create(ctx context.Context, s *domain.Supplier) error {
	if err := uc.validate(s); err != nil {
		return err
	}

	existing, err := uc.repo.GetSupplier(ctx, s.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		return domain.ErrSupplierExists
	}

	if err := uc.fetchName(ctx, s); err != nil {
		return err
	}

	if err := uc.repo.SaveSupplier(ctx, s); err != nil {
		return err
	}

	return uc.syncWiki(ctx, s)
}

// Update нормализует и валидирует данные, перезапрашивает имя из МС
// (имя в БД всегда актуальное) и сохраняет поставщика (upsert).
func (uc *MSSuppliersUseCase) Update(ctx context.Context, s *domain.Supplier) error {
	if err := uc.validate(s); err != nil {
		return err
	}

	if err := uc.fetchName(ctx, s); err != nil {
		return err
	}

	if err := uc.repo.SaveSupplier(ctx, s); err != nil {
		return err
	}

	return uc.syncWiki(ctx, s)
}

// Delete удаляет поставщика по id. Каскады на стороне БД:
// barcodes/prices — ON DELETE CASCADE, wiki.supplier_id — ON DELETE SET NULL.
// Если поставщика нет — domain.ErrSupplierNotFound.
func (uc *MSSuppliersUseCase) Delete(ctx context.Context, id string) error {
	return uc.repo.DeleteSupplier(ctx, id)
}

// ErrCounterpartyNameFetch — не удалось получить имя контрагента из МойСклад
// (сеть, WAF, несуществующий id). Хендлер оставляет форму с данными:
// достаточно нажать «Сохранить» ещё раз.
var ErrCounterpartyNameFetch = errors.New("не удалось получить имя контрагента из МС")

// ErrWikiSync — поставщик сохранён, но не удалось создать/обновить его
// страницу вики. Хендлер редиректит на список с сообщением.
var ErrWikiSync = errors.New("поставщик сохранён, но не удалось обновить страницу вики")

// syncWiki синхронизирует страницу вики поставщика после сохранения
// в справочник. Ошибка не откатывает сохранение (контуры без
// кросс-транзакций): поставщик уже в БД, ошибка оборачивается в
// ErrWikiSync для показа пользователю.
func (uc *MSSuppliersUseCase) syncWiki(ctx context.Context, s *domain.Supplier) error {
	if err := uc.wiki.SyncSupplierPage(ctx, s.ID, s.Name, int16ToInt(s.OrderDays), int16ToInt(s.DeliveryDays)); err != nil {
		return fmt.Errorf("%w: %w", ErrWikiSync, err)
	}

	return nil
}

// int16ToInt конвертирует дни (SMALLINT[] из БД) в []int — конвенцию вики.
func int16ToInt(src []int16) []int {
	res := make([]int, len(src))
	for i, v := range src {
		res[i] = int(v)
	}

	return res
}

// fetchName получает имя контрагента из МС и кладёт в s.Name.
// Ошибка МС оборачивается в ErrCounterpartyNameFetch: форма остаётся
// с заполненными данными, пользователь нажимает «Сохранить» ещё раз.
func (uc *MSSuppliersUseCase) fetchName(ctx context.Context, s *domain.Supplier) error {
	name, err := uc.ms.FetchCounterpartyName(ctx, s.ID)
	if err != nil {
		return fmt.Errorf("%w (id %s): %w", ErrCounterpartyNameFetch, s.ID, err)
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("не удалось получить имя контрагента из МойСклад: МС вернул пустое имя")
	}

	s.Name = name
	return nil
}

// UUID-контрагента МС (counterparty.id).
var (
	counterpartyUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	uuidInLinkRe       = regexp.MustCompile(`id=([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
)

// ExtractCounterpartyID извлекает uuid контрагента из ссылки МойСклад
// (https://online.moysklad.ru/app/#counterparty/edit?id=<uuid>, фрагмент может
// быть любым — главное id=<uuid>) или из голого uuid. Пустая строка — ошибка.
func ExtractCounterpartyID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("ссылка на контрагента не заполнена")
	}

	if m := uuidInLinkRe.FindStringSubmatch(raw); m != nil {
		return m[1], nil
	}

	if counterpartyUUIDRe.MatchString(raw) {
		return raw, nil
	}

	return "", errors.New("не удалось распознать id из ссылки: ожидается ссылка вида https://online.moysklad.ru/app/#counterparty/edit?id=<uuid> или сам uuid")
}

// validate нормализует и проверяет поля поставщика. Мутирует переданный s:
// правила штрихкодов — без пустых строк, дни — без дублей и по возрастанию.
func (uc *MSSuppliersUseCase) validate(s *domain.Supplier) error {
	if s == nil {
		return errors.New("поставщик не передан")
	}

	s.ID = strings.TrimSpace(s.ID)
	if !counterpartyUUIDRe.MatchString(s.ID) {
		return errors.New("id должен быть uuid контрагента МойСклад")
	}

	var err error
	if s.DecodeRules, err = normalizeRules("правило вычитки штрихкодов", s.DecodeRules, validateItemRule); err != nil {
		return err
	}
	if s.BoxDecodeRules, err = normalizeRules("правило вычитки коробок", s.BoxDecodeRules, validateBoxRule); err != nil {
		return err
	}

	if s.OrderDays, err = normalizeDays("дни заказа", s.OrderDays); err != nil {
		return err
	}
	if s.DeliveryDays, err = normalizeDays("дни доставки", s.DeliveryDays); err != nil {
		return err
	}
	if s.SpecialOrderDays, err = normalizeDays("спец. дни заказа", s.SpecialOrderDays); err != nil {
		return err
	}
	if s.SpecialDeliveryDays, err = normalizeDays("спец. дни доставки", s.SpecialDeliveryDays); err != nil {
		return err
	}

	if s.DelayDays != nil && *s.DelayDays < 0 {
		return errors.New("макс. дней между заказом и доставкой не может быть отрицательным")
	}
	if s.SpecialDelayDays != nil && *s.SpecialDelayDays < 0 {
		return errors.New("спец. макс. дней не может быть отрицательным")
	}
	if s.MinOrderAmount != nil && *s.MinOrderAmount < 0 {
		return errors.New("минимальная сумма заказа не может быть отрицательной")
	}

	return nil
}

// validateItemRule / validateBoxRule — тонкие обёртки парсера decoderules для
// валидации правил при сохранении (значение парсера не нужно).
func validateItemRule(r string) error {
	_, err := decoderules.ParseItem(r)
	return err
}

func validateBoxRule(r string) error {
	_, err := decoderules.ParseBox(r)
	return err
}

// normalizeRules чистит правила вычитки: обрезка пробелов, удаление пустых строк,
// валидация формата парсером decoderules (один источник истины с приёмкой).
func normalizeRules(label string, rules []string, parse func(string) error) ([]string, error) {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if err := parse(r); err != nil {
			return nil, fmt.Errorf("%s %q: %v", label, r, err)
		}
		if utf8.RuneCountInString(r) > 255 {
			return nil, fmt.Errorf("%s: правило %q слишком длинное (максимум 255 символов)", label, r)
		}
		out = append(out, r)
	}
	return out, nil
}

// normalizeDays проверяет диапазон 1..7, убирает дубли и сортирует по возрастанию.
func normalizeDays(label string, days []int16) ([]int16, error) {
	seen := make(map[int16]struct{}, len(days))
	out := make([]int16, 0, len(days))
	for _, d := range days {
		if d < 1 || d > 7 {
			return nil, fmt.Errorf("%s: день %d вне диапазона 1..7 (1=Пн ... 7=Вс)", label, d)
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
