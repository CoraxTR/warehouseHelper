// Пакет usecase — сценарии работы с вики-страницами: валидация и
// нормализация данных перед сохранением, выборка страниц и тегов.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

const trackPkg = "wiki"

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
	// Синк страниц поставщиков из справочника (модуль mssuppliers).
	GetPageBySupplierID(ctx context.Context, supplierID string) (*domain.WikiPage, error)
	GetUnlinkedSupplierPageByTitle(ctx context.Context, title string) (*domain.WikiPage, error)
	CreateSupplierPage(ctx context.Context, page *domain.WikiPage) error
	UpdateSupplierPage(ctx context.Context, pageID int64, supplierID, title string, orderDays, deliveryDays []int) error
	// Синк страниц товаров из каталога и теги (модуль приёмки).
	GetPageByProductID(ctx context.Context, productID string) (*domain.WikiPage, error)
	GetUnlinkedProductPageByTitle(ctx context.Context, title string) (*domain.WikiPage, error)
	CreateProductPage(ctx context.Context, page *domain.WikiPage) error
	UpdateProductPage(ctx context.Context, pageID int64, productID, title, averageWeight string) error
	AddPageTag(ctx context.Context, pageID int64, tag string) error
	RemovePageTag(ctx context.Context, pageID int64, tag string) error
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
	done := metrics.Track(trackPkg, "ListIndex")
	defer done()
	return uc.repo.ListIndex(ctx, query, tags, typ)
}

// GetPage возвращает страницу по заголовку; если страницы нет — (nil, nil).
func (uc *WikiUseCase) GetPage(ctx context.Context, title string) (*domain.WikiPage, error) {
	done := metrics.Track(trackPkg, "GetPage")
	defer done()
	return uc.repo.GetPage(ctx, title)
}

// GetPageWithBacklinks возвращает страницу и её обратные ссылки;
// если страницы нет — (nil, nil, nil). Обратные ссылки ищутся
// по каноническому заголовку страницы.
func (uc *WikiUseCase) GetPageWithBacklinks(ctx context.Context, title string) (*domain.WikiPage, []string, error) {
	done := metrics.Track(trackPkg, "GetPageWithBacklinks")
	defer done()
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
func (uc *WikiUseCase) GetPhoto(ctx context.Context, title string) (data []byte, contentType string, err error) {
	done := metrics.Track(trackPkg, "GetPhoto")
	defer done()
	return uc.repo.GetPhoto(ctx, title)
}

// TagCloud возвращает теги с количеством страниц.
func (uc *WikiUseCase) TagCloud(ctx context.Context) ([]domain.WikiTagCount, error) {
	done := metrics.Track(trackPkg, "TagCloud")
	defer done()
	return uc.repo.TagCloud(ctx)
}

// ResolveLinkTargets сопоставляет цели вики-ссылок с реальными заголовками
// страниц; пустой список не требует обращения к хранилищу.
func (uc *WikiUseCase) ResolveLinkTargets(ctx context.Context, rawTitles []string) (map[string]string, error) {
	done := metrics.Track(trackPkg, "ResolveLinkTargets")
	defer done()
	if len(rawTitles) == 0 {
		//nolint:nilnil // контракт: пустой вход — нет ссылок для разрешения
		return nil, nil
	}
	return uc.repo.ResolveLinkTargets(ctx, rawTitles)
}

// ListPageTitlesByType возвращает заголовки страниц заданного типа.
func (uc *WikiUseCase) ListPageTitlesByType(ctx context.Context, typ domain.PageType) ([]string, error) {
	done := metrics.Track(trackPkg, "ListPageTitlesByType")
	defer done()
	return uc.repo.ListPageTitlesByType(ctx, typ)
}

// SyncSupplierPage создаёт или обновляет страницу поставщика из данных
// справочника поставщиков (модуль mssuppliers). Обновляются только привязка
// supplier_id, заголовок (= name) и дни заказа/доставки; пользовательский
// контент страницы (текст, контакты, теги, фото, ссылки) не трогается.
// Синк идемпотентен: удалённую вручную страницу пересоздаёт, вручную
// созданную страницу с тем же названием — привязывает к поставщику.
// Занятый заголовок (страница другого типа) → domain.ErrTitleTaken.
func (uc *WikiUseCase) SyncSupplierPage(ctx context.Context, supplierID, name string, orderDays, deliveryDays []int) error {
	done := metrics.Track(trackPkg, "SyncSupplierPage")
	defer done()
	supplierID = strings.TrimSpace(supplierID)
	name = strings.TrimSpace(name)
	if supplierID == "" {
		return errors.New("не передан id поставщика")
	}
	if name == "" {
		return errors.New("не передано имя поставщика")
	}

	page, err := uc.repo.GetPageBySupplierID(ctx, supplierID)
	if err != nil {
		return fmt.Errorf("получить страницу вики поставщика %s: %w", supplierID, err)
	}
	if page != nil {
		if err := uc.repo.UpdateSupplierPage(ctx, page.ID, supplierID, name, orderDays, deliveryDays); err != nil {
			return fmt.Errorf("обновить страницу вики поставщика %s: %w", supplierID, err)
		}

		return nil
	}

	// Страницы с привязкой нет: привязываем вручную созданную с таким же названием.
	unlinked, err := uc.repo.GetUnlinkedSupplierPageByTitle(ctx, name)
	if err != nil {
		return fmt.Errorf("найти непривязанную страницу %q: %w", name, err)
	}
	if unlinked != nil {
		if err := uc.repo.UpdateSupplierPage(ctx, unlinked.ID, supplierID, name, orderDays, deliveryDays); err != nil {
			return fmt.Errorf("привязать страницу вики %q к поставщику %s: %w", name, supplierID, err)
		}

		return nil
	}

	if err := uc.repo.CreateSupplierPage(ctx, &domain.WikiPage{
		Type:         domain.PageTypeSupplier,
		Title:        name,
		OrderDays:    orderDays,
		DeliveryDays: deliveryDays,
		SupplierID:   supplierID,
	}); err != nil {
		return fmt.Errorf("создать страницу вики поставщика %s: %w", supplierID, err)
	}

	return nil
}

// EnsureProductPage гарантирует страницу вики товара: создаёт (title=name,
// average_weight из каталога, привязка product_id) или обновляет заголовок
// и вес (пользовательский контент не трогается). Вызывается при выгрузке
// товаров из МС и модулем приёмки при добавлении кода поставщика.
func (uc *WikiUseCase) EnsureProductPage(ctx context.Context, productID, name, averageWeight string) error {
	done := metrics.Track(trackPkg, "EnsureProductPage")
	defer done()
	productID = strings.TrimSpace(productID)
	name = strings.TrimSpace(name)
	if productID == "" {
		return errors.New("не передан id товара")
	}
	if name == "" {
		return errors.New("не передано имя товара")
	}

	page, err := uc.repo.GetPageByProductID(ctx, productID)
	if err != nil {
		return fmt.Errorf("получить страницу вики товара %s: %w", productID, err)
	}
	if page != nil {
		if err := uc.repo.UpdateProductPage(ctx, page.ID, productID, name, averageWeight); err != nil {
			return fmt.Errorf("обновить страницу вики товара %s: %w", productID, err)
		}

		return nil
	}

	// Страницы с привязкой нет: привязываем вручную созданную с таким же названием.
	unlinked, err := uc.repo.GetUnlinkedProductPageByTitle(ctx, name)
	if err != nil {
		return fmt.Errorf("найти непривязанную страницу %q: %w", name, err)
	}
	if unlinked != nil {
		if err := uc.repo.UpdateProductPage(ctx, unlinked.ID, productID, name, averageWeight); err != nil {
			return fmt.Errorf("привязать страницу вики %q к товару %s: %w", name, productID, err)
		}

		return nil
	}

	if err := uc.repo.CreateProductPage(ctx, &domain.WikiPage{
		Type:          domain.PageTypeProduct,
		Title:         name,
		AverageWeight: averageWeight,
		ProductID:     productID,
	}); err != nil {
		return fmt.Errorf("создать страницу вики товара %s: %w", productID, err)
	}

	return nil
}

// UpdateProductAverageWeight обновляет только средний вес страницы товара
// (вызов модуля среднего веса: названия у модуля нет — страницу не создаёт).
// Страницы с привязкой нет — пропуск без ошибки (создастся при выгрузке
// из МС или добавлении кода поставщика).
func (uc *WikiUseCase) UpdateProductAverageWeight(ctx context.Context, productID, averageWeight string) error {
	done := metrics.Track(trackPkg, "UpdateProductAverageWeight")
	defer done()
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return errors.New("не передан id товара")
	}

	page, err := uc.repo.GetPageByProductID(ctx, productID)
	if err != nil {
		return fmt.Errorf("получить страницу вики товара %s: %w", productID, err)
	}
	if page == nil {
		return nil
	}

	if err := uc.repo.UpdateProductPage(ctx, page.ID, productID, page.Title, averageWeight); err != nil {
		return fmt.Errorf("обновить средний вес вики товара %s: %w", productID, err)
	}

	return nil
}

// AddTagToPage добавляет тег странице по заголовку (идемпотентно).
func (uc *WikiUseCase) AddTagToPage(ctx context.Context, title, tag string) error {
	done := metrics.Track(trackPkg, "AddTagToPage")
	defer done()
	page, err := uc.repo.GetPage(ctx, title)
	if err != nil {
		return fmt.Errorf("получить страницу %q: %w", title, err)
	}
	if page == nil {
		return domain.ErrPageNotFound
	}

	return uc.repo.AddPageTag(ctx, page.ID, tag)
}

// RemoveTagFromPage снимает тег со страницы по заголовку.
func (uc *WikiUseCase) RemoveTagFromPage(ctx context.Context, title, tag string) error {
	done := metrics.Track(trackPkg, "RemoveTagFromPage")
	defer done()
	page, err := uc.repo.GetPage(ctx, title)
	if err != nil {
		return fmt.Errorf("получить страницу %q: %w", title, err)
	}
	if page == nil {
		return domain.ErrPageNotFound
	}

	return uc.repo.RemovePageTag(ctx, page.ID, tag)
}

// DeletePage удаляет страницу по заголовку.
func (uc *WikiUseCase) DeletePage(ctx context.Context, title string) error {
	done := metrics.Track(trackPkg, "DeletePage")
	defer done()
	return uc.repo.DeletePage(ctx, title)
}

// RemovePhoto удаляет фото страницы.
func (uc *WikiUseCase) RemovePhoto(ctx context.Context, title string) error {
	done := metrics.Track(trackPkg, "RemovePhoto")
	defer done()
	return uc.repo.RemovePhoto(ctx, title)
}

// SavePage валидирует и нормализует страницу, затем сохраняет её.
// Ошибки хранилища (в т.ч. domain.ErrTitleTaken) возвращаются как есть.
// Внимание: метод мутирует переданный page (нормализация полей).
func (uc *WikiUseCase) SavePage(ctx context.Context, currentTitle string, page *domain.WikiPage, photo *domain.PhotoUpload) error {
	done := metrics.Track(trackPkg, "SavePage")
	defer done()
	if err := validatePageBasics(page); err != nil {
		return err
	}

	// Тип страницы неизменяем при редактировании.
	if currentTitle != "" {
		existing, err := uc.repo.GetPage(ctx, currentTitle)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Type != page.Type {
				return errors.New("тип страницы менять нельзя")
			}
		} else {
			// Страница не найдена — считаем созданием.
			currentTitle = ""
		}
	}

	if err := normalizeAndValidatePage(page); err != nil {
		return err
	}

	// Пустое фото считаем отсутствующим.
	if photo != nil && len(photo.Data) == 0 {
		photo = nil
	}
	if err := validatePhoto(photo); err != nil {
		return err
	}

	return uc.repo.SavePage(ctx, page, currentTitle, photo)
}

// validatePageBasics проверяет переданную страницу и её тип.
func validatePageBasics(page *domain.WikiPage) error {
	if page == nil {
		return errors.New("страница не передана")
	}
	if !domain.ValidPageType(string(page.Type)) {
		return errors.New("неизвестный тип страницы")
	}
	return nil
}

// normalizeAndValidatePage приводит поля страницы к нормальному виду и
// проверяет ограничения. Порядок валидации: заголовок, контакты,
// дни заказа/доставки, средний вес, содержимое, списки.
func normalizeAndValidatePage(page *domain.WikiPage) error {
	page.Title = strings.TrimSpace(page.Title)
	if page.Title == "" {
		return errors.New("заголовок не может быть пустым")
	}
	if utf8.RuneCountInString(page.Title) > 255 {
		return errors.New("заголовок слишком длинный (максимум 255 символов)")
	}
	// Управляющие символы ломают рендер [[ссылок]] и URL.
	for _, r := range page.Title {
		if unicode.IsControl(r) {
			return errors.New("заголовок содержит недопустимые символы")
		}
	}

	if err := normalizeContacts(page.Contacts); err != nil {
		return err
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
		return errors.New("средний вес слишком длинный (максимум 100 символов)")
	}

	// Ограничение размера содержимого: каждая страница рендерится
	// goldmark+bluemonday при каждом просмотре.
	if len(page.Content) > 256<<10 {
		return errors.New("содержимое слишком большое (максимум 256 КБ)")
	}

	if page.Suppliers, err = normalizeStrings(page.Suppliers, 50, "поставщиков", 100); err != nil {
		return err
	}
	if page.Products, err = normalizeStrings(page.Products, 50, "продуктов", 100); err != nil {
		return err
	}
	if page.Tags, err = normalizeStrings(page.Tags, 50, "тегов", 100); err != nil {
		return err
	}

	return nil
}

// normalizeContacts нормализует и валидирует контакты страницы.
func normalizeContacts(contacts []domain.Contact) error {
	if len(contacts) > 20 {
		return errors.New("слишком много контактов (максимум 20)")
	}
	for i := range contacts {
		c := &contacts[i]
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
	return nil
}

// validatePhoto проверяет загружаемое фото страницы.
func validatePhoto(photo *domain.PhotoUpload) error {
	if photo == nil {
		return nil
	}
	if len(photo.Data) > 5<<20 {
		return errors.New("фото больше 5 МБ")
	}
	if !strings.HasPrefix(photo.ContentType, "image/") {
		return errors.New("фото должно быть изображением")
	}
	return nil
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
func normalizeStrings(items []string, maxCount int, kind string, maxLen int) ([]string, error) {
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
	if len(out) > maxCount {
		return nil, fmt.Errorf("слишком много %s (максимум %d)", kind, maxCount)
	}
	return out, nil
}
