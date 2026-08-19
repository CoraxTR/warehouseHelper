package domain

import "errors"

// ErrTitleTaken — страница с таким заголовком уже существует.
var ErrTitleTaken = errors.New("страница с таким заголовком уже существует")

// ErrPageNotFound — страница не найдена (в т.ч. удалена конкурентно).
var ErrPageNotFound = errors.New("вики-страница не найдена")

// PageType — тип вики-страницы.
type PageType string

// Типы вики-страниц.
const (
	PageTypeSupplier PageType = "supplier" // страница поставщика
	PageTypeProduct  PageType = "product"  // страница товара
)

// ValidPageType — корректный ли тип вики-страницы.
func ValidPageType(s string) bool {
	return s == string(PageTypeSupplier) || s == string(PageTypeProduct)
}

// WeekdayNames — подписи дней недели, индекс 1..7 (ISO: 1=Пн ... 7=Вс).
var WeekdayNames = []string{"", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}

// Contact — контакт поставщика.
type Contact struct {
	Name  string // имя контакта
	Phone string // телефон
	Email string // электронная почта
	Site  string // сайт
}

// WikiPage — вики-страница поставщика или товара.
type WikiPage struct {
	ID            int64     // внутренний идентификатор страницы
	Type          PageType  // тип страницы
	Title         string    // заголовок
	Content       string    // содержимое страницы
	Tags          []string  // теги
	Contacts      []Contact // контакты
	OrderDays     []int     // дни приёма заказов (1..7, ISO)
	DeliveryDays  []int     // дни доставки (1..7, ISO)
	AverageWeight string    // средний вес заказа
	Suppliers     []string  // поставщики (для страницы товара)
	Products      []string  // продукты (для страницы поставщика)
	HasPhoto      bool      // есть ли фото
}

// PhotoUpload — загружаемое фото страницы.
type PhotoUpload struct {
	Data        []byte // содержимое файла
	ContentType string // MIME-тип файла
}

// WikiIndexEntry — элемент списка вики-страниц.
type WikiIndexEntry struct {
	Type  PageType // тип страницы
	Title string   // заголовок
	Tags  []string // теги
}

// WikiTagCount — количество страниц с тегом.
type WikiTagCount struct {
	Name  string // название тега
	Count int    // количество страниц
}
