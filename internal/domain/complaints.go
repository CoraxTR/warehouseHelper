package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrComplaintNotFound — обращение с таким id не найдено.
var ErrComplaintNotFound = errors.New("обращение не найдено")

// ComplaintTGPhoto — фотография для отправки в Telegram (кнопка
// «Получить подробности»): расширение и содержимое файла.
type ComplaintTGPhoto struct {
	Ext  string // jpg/png/... (без точки)
	Data []byte
}

// ComplaintDue — «просроченное» обращение для тикера напоминаний:
// дедлайн наступил, статус ещё не «Завершено».
type ComplaintDue struct {
	ID     int64
	Status ComplaintStatus
}

// Модуль «Жалобы»: обращения клиентов по заказам. Типы знает и usecase,
// и репозиторий (postgres НЕ импортирует usecase — только domain).

// ComplaintStatus — статус обращения. Значения в БД — латиницей (как
// page_type у wiki); русские подписи — StatusLabel.
type ComplaintStatus string

const (
	ComplaintStatusCreated   ComplaintStatus = "created"   // Создано (default при создании)
	ComplaintStatusReviewing ComplaintStatus = "reviewing" // На рассмотрении
	ComplaintStatusWarehouse ComplaintStatus = "warehouse" // Склад
	ComplaintStatusSupplier  ComplaintStatus = "supplier"  // Поставщик
	ComplaintStatusCompleted ComplaintStatus = "completed" // Завершено
	ComplaintStatusClient    ComplaintStatus = "client"    // Клиент
)

// ComplaintStatuses — все допустимые статусы (порядок = порядок в форме).
var ComplaintStatuses = []ComplaintStatus{
	ComplaintStatusCreated,
	ComplaintStatusReviewing,
	ComplaintStatusWarehouse,
	ComplaintStatusSupplier,
	ComplaintStatusCompleted,
	ComplaintStatusClient,
}

// StatusLabel — русская подпись статуса для UI и сообщений Telegram.
func (s ComplaintStatus) StatusLabel() string {
	switch s {
	case ComplaintStatusCreated:
		return "Создано"
	case ComplaintStatusReviewing:
		return "На рассмотрении"
	case ComplaintStatusWarehouse:
		return "Склад"
	case ComplaintStatusSupplier:
		return "Поставщик"
	case ComplaintStatusCompleted:
		return "Завершено"
	case ComplaintStatusClient:
		return "Клиент"
	default:
		return string(s)
	}
}

// Valid сообщает, допустим ли статус.
func (s ComplaintStatus) Valid() bool {
	switch s {
	case ComplaintStatusCreated, ComplaintStatusReviewing, ComplaintStatusWarehouse,
		ComplaintStatusSupplier, ComplaintStatusCompleted, ComplaintStatusClient:
		return true
	default:
		return false
	}
}

// ComplaintRole — роль, для которой регистрируется телеграм-тег
// (страница «Зарегистрировать tg-теги»). Тег валидатора вставляется
// в уведомления «На рассмотрении»/«Завершено», тег склада — в
// «Склад»/«Поставщик» (правило пользователя: у «Поставщика» тег склада).
type ComplaintRole string

const (
	ComplaintRoleValidator ComplaintRole = "validator" // валидатор
	ComplaintRoleWarehouse ComplaintRole = "warehouse" // склад
)

// ComplaintRoles — все роли тегов (порядок = порядок на странице тегов).
var ComplaintRoles = []ComplaintRole{ComplaintRoleValidator, ComplaintRoleWarehouse}

// Valid — известная ли роль (валидатор или склад).
func (r ComplaintRole) Valid() bool {
	switch r {
	case ComplaintRoleValidator, ComplaintRoleWarehouse:
		return true
	default:
		return false
	}
}

// RoleLabel — русская подпись роли для страницы тегов.
func (r ComplaintRole) RoleLabel() string {
	switch r {
	case ComplaintRoleValidator:
		return "Валидатор"
	case ComplaintRoleWarehouse:
		return "Склад"
	default:
		return string(r)
	}
}

// TagRoleForStatus — роль тега для статуса; ok=false — тег для статуса
// не предусмотрен («Создано» и «Клиент» шлются без тега).
func TagRoleForStatus(s ComplaintStatus) (ComplaintRole, bool) {
	switch s {
	case ComplaintStatusReviewing, ComplaintStatusCompleted:
		return ComplaintRoleValidator, true
	case ComplaintStatusWarehouse, ComplaintStatusSupplier:
		return ComplaintRoleWarehouse, true
	case ComplaintStatusCreated, ComplaintStatusClient:
		return "", false
	}
	return "", false
}

// Complaint — обращение целиком (поля + товары).
type Complaint struct {
	ID            int64
	MSOrderNumber string
	CreatedAt     time.Time
	Phone         string // нормализованный телефон: 7XXXXXXXXXX
	Description   string
	Actions       string // предпринятые действия
	Status        ComplaintStatus
	Deadline      time.Time
	Items         []ComplaintItem
}

// ComplaintItem — товар обращения (снимок названия на момент сохранения;
// product_id может стать NULL, если товар удалён из каталога — история жива).
type ComplaintItem struct {
	ProductID   string // UUID МойСклад из products.id; может быть NULL в БД
	ProductName string // снимок названия (products.name на момент сохранения)
}

// ComplaintSummary — строка списка обращений («Жалобы» / «Полный список» /
// результат поиска): товары собраны в ProductNames.
type ComplaintSummary struct {
	ID            int64
	MSOrderNumber string
	CreatedAt     time.Time
	Phone         string
	Status        ComplaintStatus
	Items         []ComplaintItem
}

// ComplaintFilter — условия поиска обращений. Пустые поля не фильтруют;
// From/To — диапазон по дате создания (From включительно, To включительно,
// локальный день сервера). ProductID — точное вхождение товара (выбор из
// каталога); ProductName — подстрока названия по снимкам товаров обращения
// (когда товар не выбирали из каталога); при заданном ProductID текстовая
// подстрока игнорируется (решает usecase).
type ComplaintFilter struct {
	MSOrderNumber string
	Phone         string // нормализуется в usecase
	ProductID     string
	ProductName   string // подстрока названия товара (по снимкам complaint_items)
	From          time.Time
	To            time.Time
}

// ComplaintRoleTag — телеграм-тег роли (например "@ivanov").
type ComplaintRoleTag struct {
	Role ComplaintRole
	Tag  string
}

// PhoneReDigits — «цифры» нормализованного телефона: ровно 7XXXXXXXXXX.
var phoneDigitsRe = regexp.MustCompile(`^\d{10,11}$`)

// NormalizePhone приводит телефон к единому виду 7XXXXXXXXXX.
// Принимаются любые варианты записи: +7936..., +7-936-..., 8936..., 936...
// (10 цифр без кода страны), 7936...; разделители (пробелы, дефисы,
// скобки) игнорируются. Пустая строка или непохожий на телефон ввод —
// ошибка.
func NormalizePhone(raw string) (string, error) {
	digits := make([]byte, 0, 11)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= '0' && c <= '9' {
			digits = append(digits, c)
		}
	}
	if len(digits) == 0 {
		return "", errors.New("телефон не указан")
	}
	switch len(digits) {
	case 11:
		// 8 936... → 7 936... (8 — старый формат кода страны)
		if digits[0] == '8' {
			digits[0] = '7'
		}
	case 10:
		// 936... без кода страны → 7 936...
		// 7936123456 (7 + 9 цифр) — обрезанный номер, а не отсутствие кода: ошибка.
		if digits[0] == '7' || digits[0] == '8' {
			return "", fmt.Errorf("не удалось распознать номер телефона %q", raw)
		}
		digits = append([]byte{'7'}, digits...)
	default:
		return "", fmt.Errorf("не удалось распознать номер телефона %q", raw)
	}
	if digits[0] != '7' {
		return "", fmt.Errorf("не удалось распознать номер телефона %q", raw)
	}
	return string(digits), nil
}

// FormatPhone отображает нормализованный телефон как «+7 936 123-45-67».
// Ожидает ровно 11 цифр (7XXXXXXXXXX); иной ввод возвращается как есть.
func FormatPhone(phone string) string {
	if len(phone) != 11 || !phoneDigitsRe.MatchString(phone) || phone[0] != '7' {
		return phone
	}
	d := phone[1:]
	return "+7 " + d[:3] + " " + d[3:6] + "-" + d[6:8] + "-" + d[8:10]
}

// IsValidPhoneDigits — форматная проверка нормализованного телефона
// (для тестов и защиты SQL-параметров не нужна — значения параметризуются).
func IsValidPhoneDigits(phone string) bool {
	return phoneDigitsRe.MatchString(phone) && strings.HasPrefix(phone, "7")
}
