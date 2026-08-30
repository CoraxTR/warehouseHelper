// Пакет usecase — сценарии модуля «Продукция»: дерево папок товаров МойСклад
// с товарами-листьями и выгрузка отмеченных товаров в каталог (products).
// Все запросы к МС — только через интерфейсы msclient (асинхронно, с
// рейт-лимитом); модуль напрямую HTTP не шлёт.
package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
)

// ProductFolderClient — источник папок товаров из МС (реализует *client.MSAPIClient).
type ProductFolderClient interface {
	FetchProductFolders(ctx context.Context) ([]client.MSProductFolder, error)
}

// ProductClient — товары папки, единицы измерения и товар по id из МС
// (реализует *client.MSAPIClient).
type ProductClient interface {
	FetchProductsByPathName(ctx context.Context, pathName string) ([]client.MSProduct, error)
	FetchProductByID(ctx context.Context, id string) (client.MSProduct, error)
	FetchUOMName(ctx context.Context, href string) (string, error)
}

// ProductsRepository — хранилище каталога товаров (реализует PGClient).
type ProductsRepository interface {
	UpsertProduct(ctx context.Context, p *domain.Product) error
	SearchProducts(ctx context.Context, query string) ([]domain.Product, error)
	GetProduct(ctx context.Context, id string) (*domain.Product, error)
	GetProductsByIDs(ctx context.Context, ids []string) ([]domain.Product, error)
	// UpdateProductAverageWeight — обновление среднего веса (кг) по входу
	// модуля среднего веса (products.average_weight пишет только каталог).
	UpdateProductAverageWeight(ctx context.Context, productID string, avgKg float64) error
}

// ProductPageSynchronizer — контракт автосоздания страницы товара в вики
// при выгрузке из МС, реализуется *wucase.WikiUseCase (вики — отдельный
// модуль, его таблицы goods не трогает).
type ProductPageSynchronizer interface {
	EnsureProductPage(ctx context.Context, productID, name, averageWeight string) error
}

// FolderNode — узел дерева папок. PathName — полный путь папки
// (для корня — name); по нему запрашиваются товары папки и заполняется
// group_name. Products — товары, лежащие непосредственно в папке.
type FolderNode struct {
	Name     string
	ID       string
	PathName string
	Children []*FolderNode
	Products []ProductNode
}

// ProductNode — товар-лист дерева: id и название из МС; Path — полный путь
// группы (папки), передаётся формой при выгрузке.
type ProductNode struct {
	ID   string
	Name string
	Path string
}

// IsLeaf сообщает, является ли узел листом, то есть не имеет детей.
func (n *FolderNode) IsLeaf() bool {
	return len(n.Children) == 0
}

// BuildFolderTree — чистая функция: строит дерево из плоского списка папок МС.
// Правило из ТЗ: pathName == "" — папка верхнего уровня; pathName != "" — полный
// путь предков через "/" (напр. папка «030 - Минеральное масло» с pathName
// «1 - Ассортимент на продажу» — родитель корень «1 - Ассортимент на продажу»).
// Полный путь папки fp = (pathName == "" ? name : pathName + "/" + name);
// мапа fp -> узел; родитель узла = мапа[folder.pathName]; папка с pathName == ""
// — корень; папка, чей pathName не найден в мапе (аномалия) — тоже корень.
// Дети сортируются по имени (case-insensitive), корни возвращаются
// в отсортированном порядке.
func BuildFolderTree(folders []client.MSProductFolder) []*FolderNode {
	// Полный путь папки -> узел. При дубле полного пути мапа хранит первого.
	byFullPath := make(map[string]*FolderNode, len(folders))
	for i := range folders {
		fullPath := fullFolderPath(folders[i])
		if _, exists := byFullPath[fullPath]; exists {
			continue
		}
		byFullPath[fullPath] = &FolderNode{Name: folders[i].Name, ID: folders[i].ID, PathName: fullPath}
	}

	roots := make([]*FolderNode, 0, len(folders))
	linked := make(map[string]bool, len(folders))

	for i := range folders {
		fullPath := fullFolderPath(folders[i])
		if linked[fullPath] {
			// Дубль полного пути: узел уже прицеплен к родителю, пропускаем.
			continue
		}
		linked[fullPath] = true

		node := byFullPath[fullPath]
		if parent, ok := byFullPath[folders[i].PathName]; ok {
			parent.Children = append(parent.Children, node)
		} else {
			roots = append(roots, node)
		}
	}

	for _, node := range byFullPath {
		sortNodes(node.Children)
	}
	sortNodes(roots)

	return roots
}

// fullFolderPath возвращает полный путь папки: для верхнего уровня — name,
// иначе pathName + "/" + name.
func fullFolderPath(f client.MSProductFolder) string {
	if f.PathName == "" {
		return f.Name
	}
	return f.PathName + "/" + f.Name
}

// sortNodes сортирует узлы по имени без учёта регистра (стабильно —
// при равных именах порядок не меняется между выгрузками).
func sortNodes(nodes []*FolderNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
}

// sortProductNodes сортирует товары узла по имени без учёта регистра.
func sortProductNodes(products []ProductNode) {
	sort.SliceStable(products, func(i, j int) bool {
		return strings.ToLower(products[i].Name) < strings.ToLower(products[j].Name)
	})
}

// GoodsUseCase — сценарии модуля «Продукция».
type GoodsUseCase struct {
	folders  ProductFolderClient
	products ProductClient
	repo     ProductsRepository
	wiki     ProductPageSynchronizer
	turnover TurnoverBackfiller // опционально (nil в тестах)
}

// TurnoverBackfiller — асинхронный первичный бэкфилл оборотов средних продаж
// (реализует *asucase.UseCase; запросы к МС идут в фоне, не задерживая каталог).
// Контекст не передаётся намеренно: очередь бэкфилла живёт своей жизнью,
// а не жизнью запроса, который поставил товар.
type TurnoverBackfiller interface {
	BackfillProducts(productIDs []string)
}

// NewGoodsUseCase создаёт юзкейс с источником папок, клиентом товаров
// и хранилищем каталога.
func NewGoodsUseCase(f ProductFolderClient, products ProductClient, repo ProductsRepository, wiki ProductPageSynchronizer) *GoodsUseCase {
	return &GoodsUseCase{folders: f, products: products, repo: repo, wiki: wiki}
}

// SetTurnoverBackfiller подключает асинхронный бэкфилл оборотов (di.go после
// создания юзкейса; nil — отключён, тесты).
func (uc *GoodsUseCase) SetTurnoverBackfiller(t TurnoverBackfiller) {
	uc.turnover = t
}

// TurnoverProduct — срез товара для модуля средних продаж
// (реализация averagesales.ProductReader; products читает только каталог).
func (uc *GoodsUseCase) TurnoverProduct(ctx context.Context, id string) (*averagesales.TurnoverProduct, error) {
	p, err := uc.repo.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	return &averagesales.TurnoverProduct{
		ID:            p.ID,
		UOM:           p.UOM,
		AverageWeight: p.AverageWeight,
		TrackWeekly:   p.TrackWeekly,
		FolderID:      p.FolderID,
	}, nil
}

// TurnoverProductsByIDs — срез каталога для списка товаров
// (реализация averagesales.ProductReader).
func (uc *GoodsUseCase) TurnoverProductsByIDs(ctx context.Context, ids []string) ([]averagesales.TurnoverProduct, error) {
	products, err := uc.repo.GetProductsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]averagesales.TurnoverProduct, 0, len(products))
	for _, p := range products {
		out = append(out, averagesales.TurnoverProduct{
			ID:            p.ID,
			UOM:           p.UOM,
			AverageWeight: p.AverageWeight,
			TrackWeekly:   p.TrackWeekly,
			FolderID:      p.FolderID,
		})
	}
	return out, nil
}

// UpdateAverageWeight обновляет средний вес товара (кг) — контракт модуля
// среднего веса (ProductWeightUpdater на его стороне): приёмка передаёт
// единичные веса туда, модуль считает среднее и через каталог пишет
// products.average_weight (граница: владелец products — каталог).
func (uc *GoodsUseCase) UpdateAverageWeight(ctx context.Context, productID string, avgKg float64) error {
	if avgKg <= 0 {
		return errors.New("средний вес должен быть больше нуля")
	}

	return uc.repo.UpdateProductAverageWeight(ctx, productID, avgKg)
}

// LoadFolderTree тянет папки из МС и строит дерево (без товаров).
func (uc *GoodsUseCase) LoadFolderTree(ctx context.Context) ([]*FolderNode, error) {
	folders, err := uc.folders.FetchProductFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить папки товаров из МойСклад: %w", err)
	}

	return BuildFolderTree(folders), nil
}

// LoadTreeWithProducts тянет папки и товары каждой папки (GET /entity/product
// с filter=pathName, через msclient) и строит дерево с товарами-листьями.
// Ошибка любой папки прерывает загрузку целиком.
func (uc *GoodsUseCase) LoadTreeWithProducts(ctx context.Context) ([]*FolderNode, error) {
	folders, err := uc.folders.FetchProductFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить папки товаров из МойСклад: %w", err)
	}

	tree := BuildFolderTree(folders)

	var walk func(n *FolderNode) error
	walk = func(n *FolderNode) error {
		products, err := uc.products.FetchProductsByPathName(ctx, n.PathName)
		if err != nil {
			return fmt.Errorf("не удалось получить товары папки %q: %w", n.PathName, err)
		}

		for _, p := range products {
			n.Products = append(n.Products, ProductNode{ID: p.ID, Name: p.Name, Path: n.PathName})
		}
		sortProductNodes(n.Products)

		for _, child := range n.Children {
			if err := walk(child); err != nil {
				return err
			}
		}

		return nil
	}

	for _, root := range tree {
		if err := walk(root); err != nil {
			return nil, err
		}
	}

	return tree, nil
}

// ExportItem — отмеченный на бланке товар: id из МС и полный путь группы.
type ExportItem struct {
	ProductID string
	GroupPath string
}

// ProductExportError — ошибка выгрузки конкретного товара (человекочитаемая,
// по названию товара). Остальные товары при этом продолжают выгружаться.
type ProductExportError struct {
	Name string
	Err  string
}

// ExportProducts выгружает отмеченные товары в каталог (products).
// Запросы к МС — по группам: один FetchProductsByPathName на полный путь,
// единицы измерения кэшируются по href. Ошибка любого товара не прерывает
// остальных: по каждому неуспешному возвращается ProductExportError.
func (uc *GoodsUseCase) ExportProducts(ctx context.Context, items []ExportItem) ([]ProductExportError, error) {
	byPath := make(map[string][]ExportItem)
	for _, it := range items {
		byPath[it.GroupPath] = append(byPath[it.GroupPath], it)
	}

	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	uomCache := make(map[string]string) // href → название
	var exportErrs []ProductExportError

	// id папки по полному пути (products.folder_id) — из дерева папок.
	// Ошибка папок не роняет выгрузку: folder_id останется пустым, товары
	// дозаливки оборотов уйдут в безгрупповые пачки.
	folderByPath := make(map[string]string)
	folders, err := uc.folders.FetchProductFolders(ctx)
	if err != nil {
		log.Printf("выгрузка: папки не получены, folder_id не заполнится: %v", err)
	} else {
		for _, f := range folders {
			folderByPath[fullFolderPath(f)] = f.ID
		}
	}

	for _, path := range paths {
		products, err := uc.products.FetchProductsByPathName(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("не удалось получить товары группы %q: %w", path, err)
		}

		byID := make(map[string]client.MSProduct, len(products))
		for _, p := range products {
			byID[p.ID] = p
		}

		for _, it := range byPath[path] {
			ms, ok := byID[it.ProductID]
			if !ok {
				exportErrs = append(exportErrs, ProductExportError{Name: "товар " + it.ProductID, Err: "не найден в группе " + path})
				continue
			}

			prod, err := uc.buildProduct(ctx, ms, path, folderByPath[path], uomCache)
			if err != nil {
				exportErrs = append(exportErrs, ProductExportError{Name: ms.Name, Err: err.Error()})
				continue
			}

			if err := uc.repo.UpsertProduct(ctx, prod); err != nil {
				if errors.Is(err, domain.ErrInternalCodeTaken) {
					exportErrs = append(exportErrs, ProductExportError{Name: ms.Name, Err: "внутренний код уже занят другим товаром"})
				} else {
					exportErrs = append(exportErrs, ProductExportError{Name: ms.Name, Err: "не удалось сохранить в каталог: " + err.Error()})
				}
				continue
			}

			// Первичный бэкфилл средних продаж — асинхронно, в фоне.
			uc.scheduleTurnoverBackfill(prod.ID)

			if err := uc.syncWikiProduct(ctx, prod); err != nil {
				exportErrs = append(exportErrs, ProductExportError{Name: ms.Name, Err: "не удалось создать страницу вики: " + err.Error()})
			}
		}
	}

	return exportErrs, nil
}

// SearchProducts ищет товары каталога: точное совпадение internal_code
// или подстрока name. Пустой запрос — ошибка.
func (uc *GoodsUseCase) SearchProducts(ctx context.Context, query string) ([]domain.Product, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, errors.New("пустой запрос поиска")
	}

	products, err := uc.repo.SearchProducts(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("поиск по каталогу: %w", err)
	}

	return products, nil
}

// GetProduct — товар каталога по id.
func (uc *GoodsUseCase) GetProduct(ctx context.Context, id string) (*domain.Product, error) {
	p, err := uc.repo.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}

	return p, nil
}

// SaveProduct сохраняет ручные правки позиции (upsert по id).
// Новый товар (первичное добавление) уходит в фоновый бэкфилл оборотов.
func (uc *GoodsUseCase) SaveProduct(ctx context.Context, p *domain.Product) error {
	if err := uc.repo.UpsertProduct(ctx, p); err != nil {
		return err
	}
	uc.scheduleTurnoverBackfill(p.ID)
	return nil
}

// ResyncProduct перезаписывает позицию каталога данными из МС (по id):
// все поля из МС заново, group_name остаётся текущий (из каталога).
// Жёсткая валидация — как при выгрузке; при ошибке запись в БД не меняется.
func (uc *GoodsUseCase) ResyncProduct(ctx context.Context, id string) (*domain.Product, error) {
	existing, err := uc.repo.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}

	ms, err := uc.products.FetchProductByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить товар из МойСклад: %w", err)
	}

	prod, err := uc.buildProduct(ctx, ms, existing.GroupName, existing.FolderID, make(map[string]string))
	if err != nil {
		return nil, err
	}

	if err := uc.repo.UpsertProduct(ctx, prod); err != nil {
		return nil, err
	}

	uc.scheduleTurnoverBackfill(prod.ID)

	if err := uc.syncWikiProduct(ctx, prod); err != nil {
		// Товар в каталог записан; страница вики — вторичная операция,
		// не валим ресинк из-за неё (пользователь увидит ошибку в логе).
		log.Printf("resync %s: страница вики: %v", id, err)
	}

	return prod, nil
}

// scheduleTurnoverBackfill ставит товар в очередь первичного бэкфилла оборотов
// (асинхронно; бэкфилл идемпотентен — уже заполненные товары пропускает).
func (uc *GoodsUseCase) scheduleTurnoverBackfill(productID string) {
	if uc.turnover == nil {
		return
	}
	uc.turnover.BackfillProducts([]string{productID})
}

// syncWikiProduct гарантирует страницу вики товара после записи в каталог
// (автосоздание при выгрузке из МС, решение 30.08). Без синка (wiki == nil,
// тесты) — пропуск.
func (uc *GoodsUseCase) syncWikiProduct(ctx context.Context, prod *domain.Product) error {
	if uc.wiki == nil {
		return nil
	}

	return uc.wiki.EnsureProductPage(ctx, prod.ID, prod.Name, averageWeightString(prod.AverageWeight))
}

// averageWeightString форматирует средний вес товара (кг, float из МС) для
// поля вики average_weight; nil — пустая строка.
func averageWeightString(w *float64) string {
	if w == nil {
		return ""
	}

	return strconv.FormatFloat(*w, 'f', -1, 64)
}

// buildProduct собирает domain.Product из данных МС и жёстко проверяет
// заполненность всех полей (пропуск любого → ошибка с перечнем).
// group_name и folder_id — из пути папки.
func (uc *GoodsUseCase) buildProduct(ctx context.Context, ms client.MSProduct, groupPath, folderID string, uomCache map[string]string) (*domain.Product, error) {
	attrs := attributeMap(ms.Attributes)

	shortList, err := attrBool(attrs, "Шорт лист")
	if err != nil {
		return nil, err
	}
	trackWeekly, err := attrBool(attrs, "Недельный")
	if err != nil {
		return nil, err
	}
	avgWeight, err := attrFloat(attrs, "Средний вес")
	if err != nil {
		return nil, err
	}
	shelfLife, err := attrFloat(attrs, "Общий срок годности")
	if err != nil {
		return nil, err
	}
	packSize, err := attrFloat(attrs, "Кол-во в упаковке")
	if err != nil {
		return nil, err
	}
	inventoryType, err := attrString(attrs, "Вид инвентаризации")
	if err != nil {
		return nil, err
	}

	uomName, err := uc.fetchUOMName(ctx, ms, uomCache)
	if err != nil {
		return nil, err
	}

	prod := &domain.Product{
		ID:            ms.ID,
		InternalCode:  strings.TrimSpace(ms.Code),
		Name:          strings.TrimSpace(ms.Name),
		UOM:           uomName,
		GroupName:     groupPath,
		FolderID:      folderID,
		AverageWeight: &avgWeight,
		InventoryType: inventoryType,
		ShortList:     shortList,
		TrackWeekly:   trackWeekly,
	}
	if shelfLife > 0 {
		v := int16(shelfLife)
		prod.ShelfLife = &v
	}
	if packSize > 0 {
		v := int16(packSize)
		prod.PackSize = &v
	}

	// Жёсткая проверка заполненности: всё, что могло остаться пустым.
	var missing []string
	if prod.InternalCode == "" {
		missing = append(missing, "внутренний код")
	}
	if prod.Name == "" {
		missing = append(missing, "название")
	}
	if prod.UOM == "" {
		missing = append(missing, "единица измерения")
	}
	if prod.GroupName == "" {
		missing = append(missing, "группа")
	}
	if prod.AverageWeight == nil || *prod.AverageWeight <= 0 {
		missing = append(missing, "средний вес")
	}
	if prod.ShelfLife == nil {
		missing = append(missing, "общий срок годности")
	}
	if prod.PackSize == nil {
		missing = append(missing, "кол-во в упаковке")
	}
	if prod.InventoryType == "" {
		missing = append(missing, "вид инвентаризации")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("не заполнены поля: %s", strings.Join(missing, ", "))
	}

	return prod, nil
}

// fetchUOMName получает название единицы измерения (с кэшем по href).
func (uc *GoodsUseCase) fetchUOMName(ctx context.Context, ms client.MSProduct, cache map[string]string) (string, error) {
	href := strings.TrimSpace(ms.Uom.Meta.Href)
	if href == "" {
		return "", errors.New("не заполнена единица измерения (uom)")
	}

	if name, ok := cache[href]; ok {
		return name, nil
	}

	name, err := uc.products.FetchUOMName(ctx, href)
	if err != nil {
		return "", fmt.Errorf("не удалось получить единицу измерения: %w", err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("не заполнена единица измерения (uom)")
	}

	cache[href] = name

	return name, nil
}

// attributeMap — атрибуты товара по имени.
func attributeMap(attrs []client.MSAttribute) map[string]json.RawMessage {
	m := make(map[string]json.RawMessage, len(attrs))
	for _, a := range attrs {
		m[a.Name] = a.Value
	}
	return m
}

// attrString читает строковый атрибут; отсутствует/пуст — ошибка.
func attrString(attrs map[string]json.RawMessage, name string) (string, error) {
	raw, ok := attrs[name]
	if !ok {
		return "", fmt.Errorf("не заполнен атрибут %q", name)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("атрибут %q — не строка", name)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("не заполнен атрибут %q", name)
	}
	return s, nil
}

// attrBool читает булев атрибут (true/false или строка "true"/"false").
func attrBool(attrs map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := attrs[name]
	if !ok {
		return false, fmt.Errorf("не заполнен атрибут %q", name)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if b, err = strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return b, nil
		}
	}
	return false, fmt.Errorf("атрибут %q — не булев", name)
}

// attrFloat читает числовой атрибут (число или строка с числом).
func attrFloat(attrs map[string]json.RawMessage, name string) (float64, error) {
	raw, ok := attrs[name]
	if !ok {
		return 0, fmt.Errorf("не заполнен атрибут %q", name)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err = strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f, nil
		}
	}
	return 0, fmt.Errorf("атрибут %q — не число", name)
}
