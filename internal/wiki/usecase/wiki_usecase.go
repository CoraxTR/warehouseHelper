// Пакет usecase — сценарии работы с вики-страницами: валидация и
// нормализация данных перед сохранением, выборка страниц и тегов.
package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"warehouseHelper/internal/domain"
)

// WikiRepository — контракт хранилища вики-страниц, реализуется postgres-репозиторием.
type WikiRepository interface {
	ListIndex(ctx context.Context, query string, tags []string, typ domain.PageType) ([]domain.WikiIndexEntry, error)
	GetPage(ctx context.Context, title string) (*domain.WikiPage, error)
	GetBacklinks(ctx context.Context, title string) ([]string, error)
	SavePage(ctx context.Context, page *domain.WikiPage, currentTitle string, photo *domain.PhotoUpload) error
	DeletePage(ctx context.Context, title string) error
	RemovePhoto(ctx context.Context, title string) error
	GetPhoto(ctx context.Context, title string) (data []byte, contentType string, err error)
	TagCloud(ctx context.Context) ([]domain.WikiTagCount, error)
	ResolveLinkTargets(ctx context.Context, rawTitles []string) (map[string]string, error)
	ListPageTitlesByType(ctx context.Context, typ domain.PageType) ([]string, error)
}

// WikiUseCase — сценарии работы с вики-страницами.
type WikiUseCase struct {
	repo WikiRepository
}

// NewWikiUseCase создаёт сценарий с переданным хранилищем.
func NewWikiUseCase(repo WikiRepository) *WikiUseCase {
	return &WikiUseCase{repo: repo}
}

// ListIndex возвращает список вики-страниц по фильтрам.
func (uc *WikiUseCase) ListIndex(ctx context.Context, query string, tags []string, typ domain.PageType) ([]domain.WikiIndexEntry, error) {
	return uc.repo.ListIndex(ctx, query, tags, typ)
}

// GetPage возвращает страницу по заголовку; если страницы нет — (nil, nil).
func (uc *WikiUseCase) GetPage(ctx context.Context, title string) (*domain.WikiPage, error) {
	return uc.repo.GetPage(ctx, title)
}

// GetPageWithBacklinks возвращает страницу и её обратные ссылки;
// если страницы нет — (nil, nil, nil). Обратные ссылки ищутся
// по каноническому заголовку страницы.
func (uc *WikiUseCase) GetPageWithBacklinks(ctx context.Context, title string) (*domain.WikiPage, []string, error) {
	page, err := uc.repo.GetPage(ctx, title)
	if err != nil {
		return nil, nil, err
	}
	if page == nil {
		return nil, nil, nil
	}
	backlinks, err := uc.repo.GetBacklinks(ctx, page.Title)
	if err != nil {
		return nil, nil, err
	}
	return page, backlinks, nil
}

// GetPhoto возвращает данные фото страницы и его MIME-тип.
func (uc *WikiUseCase) GetPhoto(ctx context.Context, title string) ([]byte, string, error) {
	return uc.repo.GetPhoto(ctx, title)
}

// TagCloud возвращает теги с количеством страниц.
func (uc *WikiUseCase) TagCloud(ctx context.Context) ([]domain.WikiTagCount, error) {
	return uc.repo.TagCloud(ctx)
}

// ResolveLinkTargets сопоставляет цели вики-ссылок с реальными заголовками
// страниц; пустой список не требует обращения к хранилищу.
func (uc *WikiUseCase) ResolveLinkTargets(ctx context.Context, rawTitles []string) (map[string]string, error) {
	if len(rawTitles) == 0 {
		return nil, nil
	}
	return uc.repo.ResolveLinkTargets(ctx, rawTitles)
}

// ListPageTitlesByType возвращает заголовки страниц заданного типа.
func (uc *WikiUseCase) ListPageTitlesByType(ctx context.Context, typ domain.PageType) ([]string, error) {
	return uc.repo.ListPageTitlesByType(ctx, typ)
}

// DeletePage удаляет страницу по заголовку.
func (uc *WikiUseCase) DeletePage(ctx context.Context, title string) error {
	return uc.repo.DeletePage(ctx, title)
}

// RemovePhoto удаляет фото страницы.
func (uc *WikiUseCase) RemovePhoto(ctx context.Context, title string) error {
	return uc.repo.RemovePhoto(ctx, title)
}

// SavePage валидирует и нормализует страницу, затем сохраняет её.
// Ошибки хранилища (в т.ч. domain.ErrTitleTaken) возвращаются как есть.
// Внимание: метод мутирует переданный page (нормализация полей).
func (uc *WikiUseCase) SavePage(ctx context.Context, currentTitle string, page *domain.WikiPage, photo *domain.PhotoUpload) error {
	if page == nil {
		return fmt.Errorf("страница не передана")
	}
	if !domain.ValidPageType(string(page.Type)) {
		return fmt.Errorf("неизвестный тип страницы")
	}

	page.Title = strings.TrimSpace(page.Title)
	if page.Title == "" {
		return fmt.Errorf("заголовок не может быть пустым")
	}
	if utf8.RuneCountInString(page.Title) > 255 {
		return fmt.Errorf("заголовок слишком длинный (максимум 255 символов)")
	}
	// Управляющие символы ломают рендер [[ссылок]] и URL.
	for _, r := range page.Title {
		if unicode.IsControl(r) {
			return fmt.Errorf("заголовок содержит недопустимые символы")
		}
	}

	// Тип страницы неизменяем при редактировании.
	if currentTitle != "" {
		existing, err := uc.repo.GetPage(ctx, currentTitle)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Type != page.Type {
				return fmt.Errorf("тип страницы менять нельзя")
			}
		} else {
			// Страница не найдена — считаем созданием.
			currentTitle = ""
		}
	}

	if len(page.Contacts) > 20 {
		return fmt.Errorf("слишком много контактов (максимум 20)")
	}
	for i := range page.Contacts {
		c := &page.Contacts[i]
		c.Name = strings.TrimSpace(c.Name)
		c.Phone = strings.TrimSpace(c.Phone)
		c.Email = strings.TrimSpace(c.Email)
		c.Site = strings.TrimSpace(c.Site)
		if c.Phone == "" && c.Email == "" && c.Site == "" {
			return fmt.Errorf("заполните телефон, email или сайт в контакте %d", i+1)
		}
		if utf8.RuneCountInString(c.Name) > 255 ||
			utf8.RuneCountInString(c.Phone) > 255 ||
			utf8.RuneCountInString(c.Email) > 255 ||
			utf8.RuneCountInString(c.Site) > 255 {
			return fmt.Errorf("поле контакта %d слишком длинное (максимум 255 символов)", i+1)
		}
	}

	var err error
	if page.OrderDays, err = normalizeDays(page.OrderDays); err != nil {
		return err
	}
	if page.DeliveryDays, err = normalizeDays(page.DeliveryDays); err != nil {
		return err
	}

	page.AverageWeight = strings.TrimSpace(page.AverageWeight)
	if utf8.RuneCountInString(page.AverageWeight) > 100 {
		return fmt.Errorf("средний вес слишком длинный (максимум 100 символов)")
	}

	// Ограничение размера содержимого: каждая страница рендерится
	// goldmark+bluemonday при каждом просмотре.
	if len(page.Content) > 256<<10 {
		return fmt.Errorf("содержимое слишком большое (максимум 256 КБ)")
	}

	page.Suppliers, err = normalizeStrings(page.Suppliers, 50, "поставщиков", 100)
	if err != nil {
		return err
	}
	page.Tags, err = normalizeStrings(page.Tags, 50, "тегов", 100)
	if err != nil {
		return err
	}

	// Пустое фото считаем отсутствующим.
	if photo != nil && len(photo.Data) == 0 {
		photo = nil
	}
	if photo != nil {
		if len(photo.Data) > 5<<20 {
			return fmt.Errorf("фото больше 5 МБ")
		}
		if !strings.HasPrefix(photo.ContentType, "image/") {
			return fmt.Errorf("фото должно быть изображением")
		}
	}

	return uc.repo.SavePage(ctx, page, currentTitle, photo)
}

// normalizeDays валидирует дни недели (1..7), убирает дубли
// и сортирует по возрастанию.
func normalizeDays(days []int) ([]int, error) {
	if len(days) == 0 {
		return nil, nil
	}
	seen := make(map[int]struct{}, len(days))
	out := make([]int, 0, len(days))
	for _, d := range days {
		if d < 1 || d > 7 {
			return nil, fmt.Errorf("неверный день недели: %d", d)
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Ints(out)
	return out, nil
}

// normalizeStrings обрезает строки по пробелам, выкидывает пустые,
// убирает дубли без учёта регистра (сохраняя первый вариант),
// проверяет максимальное количество элементов и длину каждого.
func normalizeStrings(items []string, max int, kind string, maxLen int) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if utf8.RuneCountInString(it) > maxLen {
			return nil, fmt.Errorf("элемент %s слишком длинный (максимум %d символов)", kind, maxLen)
		}
		key := strings.ToLower(it)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, it)
	}
	if len(out) > max {
		return nil, fmt.Errorf("слишком много %s (максимум %d)", kind, max)
	}
	return out, nil
}
