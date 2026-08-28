// Пакет usecase — сценарии работы со справочником поставщиков МойСклад:
// список, создание и редактирование (имя подтягивается из МС при сохранении),
// удаление. Валидация и нормализация полей — здесь.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"warehouseHelper/internal/domain"
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

// MSSuppliersUseCase — сценарии работы с поставщиками.
type MSSuppliersUseCase struct {
	repo MSSuppliersRepository
	ms   CounterpartyClient
}

// NewMSSuppliersUseCase создаёт сценарий с переданным хранилищем и MS-клиентом.
func NewMSSuppliersUseCase(repo MSSuppliersRepository, ms CounterpartyClient) *MSSuppliersUseCase {
	return &MSSuppliersUseCase{repo: repo, ms: ms}
}

// List возвращает всех поставщиков, отсортированных по алфавиту (ORDER BY lower(name)).
func (uc *MSSuppliersUseCase) List(ctx context.Context) ([]domain.Supplier, error) {
	return uc.repo.ListSuppliers(ctx)
}

// Get возвращает поставщика по id; если нет — (nil, nil).
func (uc *MSSuppliersUseCase) Get(ctx context.Context, id string) (*domain.Supplier, error) {
	return uc.repo.GetSupplier(ctx, id)
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

	return uc.repo.SaveSupplier(ctx, s)
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

	return uc.repo.SaveSupplier(ctx, s)
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

// fetchName получает имя контрагента из МС и кладёт в s.Name.
// Ошибка МС оборачивается в ErrCounterpartyNameFetch: форма остаётся
// с заполненными данными, пользователь нажимает «Сохранить» ещё раз.
func (uc *MSSuppliersUseCase) fetchName(ctx context.Context, s *domain.Supplier) error {
	name, err := uc.ms.FetchCounterpartyName(ctx, s.ID)
	if err != nil {
		return fmt.Errorf("%w (id %s): %v", ErrCounterpartyNameFetch, s.ID, err)
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
	decodeRuleRe       = regexp.MustCompile(`^\d+(-\d+)+$`)
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
	if s.DecodeRules, err = normalizeRules("правила вычитки штрихкодов", s.DecodeRules); err != nil {
		return err
	}
	if s.BoxDecodeRules, err = normalizeRules("правила вычитки коробок", s.BoxDecodeRules); err != nil {
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

// normalizeRules чистит правила вычитки: обрезка пробелов, удаление пустых строк.
func normalizeRules(label string, rules []string) ([]string, error) {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !decodeRuleRe.MatchString(r) {
			return nil, fmt.Errorf("%s: правило %q должно быть в формате \"28-1-6-7-6-13-8-21-8\"", label, r)
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
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
