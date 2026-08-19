package usecase

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/refgo/registry"
)

// RefGoCheckOrdersRepository — источник заказов для сверки.
type RefGoCheckOrdersRepository interface {
	GetRefGoCheckOrdersByDateRange(ctx context.Context, dateFrom, dateTo string) (map[string]domain.InternalRefGoCheckAgainstOrder, error)
}

// RefGoXlsxParser — парсер xlsx-реестра перевозчика.
type RefGoXlsxParser interface {
	ParseRefGoCheckFile(data []byte) (int64, map[int64]registry.RefGoCheckAgainstOrder, error)
}

// RefGoCheckAgainstUseCase — сверка реестра перевозчика с заказами из базы.
type RefGoCheckAgainstUseCase struct {
	repo   RefGoCheckOrdersRepository
	parser RefGoXlsxParser
	cfg    *config.RefGoConfig
}

// NewRefGoCheckAgainstUseCase создаёт юзкейс сверки с перевозчиком.
func NewRefGoCheckAgainstUseCase(repo RefGoCheckOrdersRepository, parser RefGoXlsxParser, cfg *config.RefGoConfig) *RefGoCheckAgainstUseCase {
	return &RefGoCheckAgainstUseCase{
		repo:   repo,
		parser: parser,
		cfg:    cfg,
	}
}

// Enabled сообщает, включён ли модуль сверки (заданы ли его параметры).
func (uc *RefGoCheckAgainstUseCase) Enabled() bool {
	return uc.cfg.CheckAgainstModule
}

// RefGoCheckResult — результат сверки реестра с базой.
type RefGoCheckResult struct {
	RetrievesCount int64
	Errors         []string
}

// Check сверяет реестр перевозчика (file) с заказами из базы за период
// [dateFrom, dateTo] (включительно).
func (uc *RefGoCheckAgainstUseCase) Check(ctx context.Context, dateFrom, dateTo string, file []byte) (*RefGoCheckResult, error) {
	retrievesCount, fileOrders, err := uc.parser.ParseRefGoCheckFile(file)
	if err != nil {
		return nil, fmt.Errorf("не удалось разобрать файл реестра: %w", err)
	}

	dbOrders, err := uc.repo.GetRefGoCheckOrdersByDateRange(ctx, dateFrom, dateTo)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить заказы из базы: %w", err)
	}

	errorsList := make([]string, 0)

	rowNumbers := make([]int64, 0, len(fileOrders))
	for row := range fileOrders {
		rowNumbers = append(rowNumbers, row)
	}
	sort.Slice(rowNumbers, func(i, j int) bool { return rowNumbers[i] < rowNumbers[j] })

	for _, row := range rowNumbers {
		order := fileOrders[row]

		dbOrder, ok := dbOrders[order.RefGoNumber]
		if !ok {
			errorsList = append(errorsList, fmt.Sprintf("Ошибка на строке %d: Этот заказ не найден в базе", row))

			continue
		}

		switch dbOrder.PaymentMethod {
		case domain.PaymentMethodCash, domain.PaymentMethodCard:
			if !sumsMatch(dbOrder.Sum, order.CashFact+order.TerminalFact) {
				errorsList = append(errorsList, fmt.Sprintf("Ошибка на строке %d: Сумма оплаты не совпадает с базой", row))
			}
		}

		if diff := order.ValidateTaxFact(uc.cfg.RGCashtax, uc.cfg.RGCardtax); diff != 0 {
			errorsList = append(errorsList, fmt.Sprintf("Ошибка на строке %d: Некорректная обработка д/с", row))
		}

		if order.ReducedTimeIntervalPayments != 0 {
			errorsList = append(errorsList, fmt.Sprintf("Ошибка на строке %d: Проверь сокращение интервала", row))
		}

		if order.AdditionalPayments != 0 {
			errorsList = append(errorsList, fmt.Sprintf("Ошибка на строке %d: Проверь доп. услуги", row))
		}

		if msg := uc.checkDeliveryPrice(dbOrder, order, row); msg != "" {
			errorsList = append(errorsList, msg)
		}

		// Заказ найден и проверен — убираем его из мапы БД, чтобы после
		// прохода по реестру увидеть заказы, не участвовавшие в сверке.
		delete(dbOrders, order.RefGoNumber)
	}

	// Заказы из базы, которые не встретились в реестре перевозчика.
	leftoverNumbers := make([]string, 0, len(dbOrders))
	for refgoNumber := range dbOrders {
		leftoverNumbers = append(leftoverNumbers, refgoNumber)
	}
	sort.Slice(leftoverNumbers, func(i, j int) bool { return refgoNumberLess(leftoverNumbers[i], leftoverNumbers[j]) })

	for _, refgoNumber := range leftoverNumbers {
		errorsList = append(errorsList, fmt.Sprintf("Заказ %s не обнаружен в сверке", refgoNumber))
	}

	return &RefGoCheckResult{
		RetrievesCount: retrievesCount,
		Errors:         errorsList,
	}, nil
}

// refgoNumberLess сравнивает номера РефГо как числа, если оба — числа,
// иначе лексикографически.
func refgoNumberLess(a, b string) bool {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		return ai < bi
	}

	return a < b
}

// sumsMatch сравнивает сумму заказа из базы (рубли) с фактической оплатой
// из реестра (копейки) с допуском в 1 копейку.
func sumsMatch(dbSum float64, factKopecks int64) bool {
	dbKopecks := int64(math.Round(dbSum * 100))

	diff := dbKopecks - factKopecks
	if diff < 0 {
		diff = -diff
	}

	return diff <= 1
}

// checkDeliveryPrice сверяет стоимость доставки из реестра с расчётной:
// один тариф за каждые RGWeightlimit килограмм веса, округление вверх.
func (uc *RefGoCheckAgainstUseCase) checkDeliveryPrice(dbOrder domain.InternalRefGoCheckAgainstOrder, order registry.RefGoCheckAgainstOrder, row int64) string {
	zonePrice, ok := uc.zonePrice(dbOrder.RefGoZone)
	if !ok {
		return fmt.Sprintf("Ошибка на строке %d: Неизвестная зона доставки", row)
	}

	if dbOrder.Weight <= 0 {
		return fmt.Sprintf("Ошибка на строке %d: Нулевой вес заказа", row)
	}

	if uc.cfg.RGWeightlimit <= 0 {
		return fmt.Sprintf("Ошибка на строке %d: Не задан лимит веса тарифа", row)
	}

	tariffCount := int64(math.Ceil(dbOrder.Weight / uc.cfg.RGWeightlimit))
	expectedKopecks := int64(math.Round(zonePrice * float64(tariffCount) * 100))

	if order.DeliveryPrice != expectedKopecks {
		return fmt.Sprintf("Ошибка на строке %d: Неверная стоимость доставки", row)
	}

	return ""
}

// zonePrice возвращает цену тарифа для зоны доставки.
func (uc *RefGoCheckAgainstUseCase) zonePrice(zone string) (float64, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(zone, "ё", "е"))

	switch {
	case strings.Contains(normalized, "зелен"):
		return uc.cfg.RGGreenzonePrice, true
	case strings.Contains(normalized, "желт"):
		return uc.cfg.RGYellowzonePrice, true
	case strings.Contains(normalized, "оранж"):
		return uc.cfg.RGOrangezonePrice, true
	case strings.Contains(normalized, "красн"):
		return uc.cfg.RGRedzonePrice, true
	case strings.Contains(normalized, "гол"):
		return uc.cfg.RGBluezonePrice, true
	default:
		return 0, false
	}
}
