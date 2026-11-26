package usecase

import (
	"fmt"
	"strings"

	"warehouseHelper/internal/msclient/client"
)

// MaxProducts — предел числа товаров в одном скане: окно аудита обходится один
// раз, и каждый раскрытый контекст проверяется сразу по всем товарам задачи.
const MaxProducts = 20

// target — товар задачи: id из нашего каталога и канонический id remap, по
// которому позиции сравниваются в диффе (легаси id не совпадают с аудитом).
type target struct {
	id        string
	canonical string
	name      string
}

// normalizeProducts — список id товаров из формы: пустые отбрасываются,
// повторы схлопываются (порядок сохраняется), больше MaxProducts — отказ.
func normalizeProducts(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))

	for _, raw := range ids {
		for part := range strings.SplitSeq(raw, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}

	if len(out) == 0 {
		return nil, &ValidationError{Reason: "товар не выбран"}
	}

	if len(out) > MaxProducts {
		return nil, &ValidationError{
			Reason: fmt.Sprintf("товаров больше %d (%d): сузьте список", MaxProducts, len(out)),
		}
	}

	return out, nil
}

// productLabel — подпись задачи для страницы и шапки выгрузки: один товар —
// его имя, несколько — их перечисление.
func productLabel(ts []target) string {
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		name := strings.TrimSpace(t.name)
		if name == "" {
			name = t.id
		}
		names = append(names, name)
	}

	return strings.Join(names, ", ")
}

// matchesTargets — позиция диффа про один из искомых товаров; возвращает имя
// искомого товара (колонка «Поиск» в выгрузке) и признак совпадения.
func matchesTargets(p client.AuditPosition, targets []target) (string, bool) {
	for _, t := range targets {
		if matchesProduct(p, t.canonical, t.name) {
			return t.name, true
		}
	}

	return "", false
}
