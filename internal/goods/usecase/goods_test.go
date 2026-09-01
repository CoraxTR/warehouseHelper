package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
)

// testGroupA — имя группы товаров в тестах экспорта (goconst).
const testGroupA = "Группа А"

// msFolder собирает папку МС в стиле ответа /entity/productfolder:
// id, name, pathName.
func msFolder(id, name, pathName string) client.MSProductFolder {
	return client.MSProductFolder{
		ID:       id,
		Name:     name,
		PathName: pathName,
	}
}

// assertLeavesConsistent проверяет во всём дереве, что IsLeaf соответствует
// отсутствию детей.
func assertLeavesConsistent(t *testing.T, roots []*FolderNode) {
	t.Helper()

	var walk func(n *FolderNode)
	walk = func(n *FolderNode) {
		if n.IsLeaf() != (len(n.Children) == 0) {
			t.Errorf("узел %q: IsLeaf() = %v, ожидалось %v", n.Name, n.IsLeaf(), len(n.Children) == 0)
		}
		for _, child := range n.Children {
			walk(child)
		}
	}

	for _, root := range roots {
		walk(root)
	}
}

func TestBuildFolderTree(t *testing.T) {
	tests := []struct {
		name    string
		folders []client.MSProductFolder
		want    []*FolderNode
	}{
		{
			name: "корни и вложенность 2-3 уровня",
			folders: []client.MSProductFolder{
				msFolder("id-030", "030 - Минеральное масло", "1 - Ассортимент на продажу/2 - Моторные масла"),
				msFolder("id-2-motor", "2 - Моторные масла", "1 - Ассортимент на продажу"),
				msFolder("id-020", "020 - Синтетическое масло", "1 - Ассортимент на продажу/2 - Моторные масла"),
				msFolder("id-root", "1 - Ассортимент на продажу", ""),
				msFolder("id-2-filters", "2 - Фильтры", "1 - Ассортимент на продажу"),
			},
			want: []*FolderNode{
				{
					Name:     "1 - Ассортимент на продажу",
					ID:       "id-root",
					PathName: "1 - Ассортимент на продажу",
					Children: []*FolderNode{
						{
							Name:     "2 - Моторные масла",
							ID:       "id-2-motor",
							PathName: "1 - Ассортимент на продажу/2 - Моторные масла",
							Children: []*FolderNode{
								{Name: "020 - Синтетическое масло", ID: "id-020", PathName: "1 - Ассортимент на продажу/2 - Моторные масла/020 - Синтетическое масло"},
								{Name: "030 - Минеральное масло", ID: "id-030", PathName: "1 - Ассортимент на продажу/2 - Моторные масла/030 - Минеральное масло"},
							},
						},
						{Name: "2 - Фильтры", ID: "id-2-filters", PathName: "1 - Ассортимент на продажу/2 - Фильтры"},
					},
				},
			},
		},
		{
			name: "родитель отсутствует в списке — папка остаётся корнем",
			folders: []client.MSProductFolder{
				msFolder("id-orphan-2", "2 - Моторные масла", "1 - Ассортимент на продажу/2 - Моторные масла"),
				msFolder("id-orphan-030", "030 - Минеральное масло", "Нет такого корня"),
				msFolder("id-root", "1 - Ассортимент на продажу", ""),
			},
			want: []*FolderNode{
				{Name: "030 - Минеральное масло", ID: "id-orphan-030", PathName: "Нет такого корня/030 - Минеральное масло"},
				{Name: "1 - Ассортимент на продажу", ID: "id-root", PathName: "1 - Ассортимент на продажу"},
				{Name: "2 - Моторные масла", ID: "id-orphan-2", PathName: "1 - Ассортимент на продажу/2 - Моторные масла/2 - Моторные масла"},
			},
		},
		{
			name: "сортировка детей и корней по имени без учёта регистра",
			folders: []client.MSProductFolder{
				msFolder("id-c", "c", "Корень"),
				msFolder("id-A", "A", "Корень"),
				msFolder("id-b", "b", "Корень"),
				msFolder("id-root", "Корень", ""),
				msFolder("id-z", "Zeta", ""),
				msFolder("id-alpha", "alpha", ""),
			},
			want: []*FolderNode{
				{Name: "alpha", ID: "id-alpha", PathName: "alpha"},
				{Name: "Zeta", ID: "id-z", PathName: "Zeta"},
				{
					Name:     "Корень",
					ID:       "id-root",
					PathName: "Корень",
					Children: []*FolderNode{
						{Name: "A", ID: "id-A", PathName: "Корень/A"},
						{Name: "b", ID: "id-b", PathName: "Корень/b"},
						{Name: "c", ID: "id-c", PathName: "Корень/c"},
					},
				},
			},
		},
		{
			name: "дубль полного пути — мапа хранит первого, узел один",
			folders: []client.MSProductFolder{
				msFolder("id-root", "1 - Ассортимент на продажу", ""),
				msFolder("id-dup-1", "2 - Моторные масла", "1 - Ассортимент на продажу"),
				msFolder("id-dup-2", "2 - Моторные масла", "1 - Ассортимент на продажу"),
			},
			want: []*FolderNode{
				{
					Name:     "1 - Ассортимент на продажу",
					ID:       "id-root",
					PathName: "1 - Ассортимент на продажу",
					Children: []*FolderNode{
						{Name: "2 - Моторные масла", ID: "id-dup-1", PathName: "1 - Ассортимент на продажу/2 - Моторные масла"},
					},
				},
			},
		},
		{
			name:    "пустой список папок — пустое дерево",
			folders: []client.MSProductFolder{},
			want:    []*FolderNode{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildFolderTree(tt.folders)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BuildFolderTree() = %#v, want %#v", got, tt.want)
			}

			if len(tt.want) == 0 && got == nil {
				t.Error("ожидался пустой, но не nil слайс")
			}

			assertLeavesConsistent(t, got)
		})
	}
}

func TestFolderNodeIsLeaf(t *testing.T) {
	roots := BuildFolderTree([]client.MSProductFolder{
		msFolder("id-root", "1 - Ассортимент на продажу", ""),
		msFolder("id-leaf", "2 - Фильтры", "1 - Ассортимент на продажу"),
	})

	if len(roots) != 1 {
		t.Fatalf("ожидался один корень, получено %d", len(roots))
	}
	if roots[0].IsLeaf() {
		t.Error("узел с детьми не должен быть листом")
	}
	if len(roots[0].Children) != 1 {
		t.Fatalf("ожидался один ребёнок, получено %d", len(roots[0].Children))
	}
	if !roots[0].Children[0].IsLeaf() {
		t.Error("узел без детей должен быть листом")
	}

	empty := &FolderNode{}
	if !empty.IsLeaf() {
		t.Error("пустой узел должен быть листом")
	}
}

// errClientFail — сентинел ошибки клиента папок МС для тестов.
var errClientFail = errors.New("клиент МойСклад недоступен")

// stubProductFolderClient — заглушка клиента папок товаров МС.
type stubProductFolderClient struct {
	folders []client.MSProductFolder
	err     error
}

var _ ProductFolderClient = (*stubProductFolderClient)(nil)

func (s *stubProductFolderClient) FetchProductFolders(_ context.Context) ([]client.MSProductFolder, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.folders, nil
}

func TestGoodsUseCaseLoadFolderTree(t *testing.T) {
	tests := []struct {
		name    string
		stub    *stubProductFolderClient
		wantLen int
		wantErr bool
	}{
		{
			name: "успех: папки из МС превращаются в дерево",
			stub: &stubProductFolderClient{
				folders: []client.MSProductFolder{
					msFolder("id-root", "1 - Ассортимент на продажу", ""),
					msFolder("id-child", "2 - Фильтры", "1 - Ассортимент на продажу"),
				},
			},
			wantLen: 1,
		},
		{
			name:    "ошибка клиента пробрасывается",
			stub:    &stubProductFolderClient{err: errClientFail},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc := NewGoodsUseCase(tt.stub, nil, nil, nil)
			roots, err := uc.LoadFolderTree(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatal("ожидалась ошибка клиента")
				}
				if !errors.Is(err, errClientFail) {
					t.Errorf("ожидалась ошибка errClientFail, получено: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if len(roots) != tt.wantLen {
				t.Errorf("ожидалось %d корней, получено %d", tt.wantLen, len(roots))
			}
		})
	}
}

// msProduct собирает товар МС с атрибутами для тестов.
func msProduct(id, code, name, uomHref string, attrs ...client.MSAttribute) client.MSProduct {
	p := client.MSProduct{ID: id, Code: code, Name: name}
	p.Uom.Meta.Href = uomHref
	p.Attributes = attrs
	return p
}

func strAttr(name, val string) client.MSAttribute {
	return client.MSAttribute{Type: "string", Name: name, Value: json.RawMessage(fmt.Sprintf("%q", val))}
}

func boolAttr(name string, val bool) client.MSAttribute {
	return client.MSAttribute{Name: name, Value: json.RawMessage(fmt.Sprintf("%t", val))}
}

func numAttr(name string, val float64) client.MSAttribute {
	return client.MSAttribute{Name: name, Value: json.RawMessage(fmt.Sprintf("%v", val))}
}

func TestAttrString_CustomEntity(t *testing.T) {
	// Справочник (customentity): берём name из объекта.
	attrs := attributeMap([]client.MSAttribute{
		entityAttr("Вид инвентаризации", "охлаждёнка"),
	})
	if v, err := attrString(attrs, "Вид инвентаризации"); err != nil || v != "охлаждёнка" {
		t.Fatalf("customentity: got %q, err %v", v, err)
	}

	// customentity со строкой вместо объекта — ошибка.
	if _, err := attrString(attributeMap([]client.MSAttribute{
		{Name: "Вид инвентаризации", Type: client.MSCustomEntityType, Value: json.RawMessage(`"строка"`)},
	}), "Вид инвентаризации"); err == nil {
		t.Error("customentity со строкой — ожидалась ошибка")
	}

	// string-тип с объектом — ошибка.
	if _, err := attrString(attributeMap([]client.MSAttribute{
		{Name: "Вид инвентаризации", Type: "string", Value: json.RawMessage(`{"name":"x"}`)},
	}), "Вид инвентаризации"); err == nil {
		t.Error("string-тип с объектом — ожидалась ошибка")
	}

	// Неизвестный тип — ошибка.
	if _, err := attrString(attributeMap([]client.MSAttribute{
		{Name: "Вид инвентаризации", Type: "file", Value: json.RawMessage(`"x"`)},
	}), "Вид инвентаризации"); err == nil {
		t.Error("неизвестный тип — ожидалась ошибка")
	}
}

// entityAttr — атрибут-справочник МС (customentity): value — объект с name.
func entityAttr(name, val string) client.MSAttribute {
	value := fmt.Sprintf(`{"meta":{"href":"https://online.moysklad.ru/api/remap/1.2/entity/customentity/xxx/yyy","type":"customentity"},"name":%q}`, val)
	return client.MSAttribute{Type: client.MSCustomEntityType, Name: name, Value: json.RawMessage(value)}
}

// fullProductAttrs — полный набор атрибутов валидного товара.
func fullProductAttrs() []client.MSAttribute {
	return []client.MSAttribute{
		entityAttr("Вид инвентаризации", "охлаждёнка"),
		boolAttr("Шорт лист", true),
		boolAttr("Недельный", false),
		numAttr("Средний вес", 12.5),
		numAttr("Общий срок годности", 30),
		numAttr("Кол-во в упаковке", 8),
	}
}

// stubProductClient — заглушка клиента товаров МС.
type stubProductClient struct {
	byPath    map[string][]client.MSProduct
	byID      map[string]client.MSProduct
	uomNames  map[string]string
	pathErr   error
	idErr     error
	uomCalls  []string // запросы uom (для проверки кэша)
	pathCalls []string // запросы групп
	idCalls   []string // запросы по id
}

var _ ProductClient = (*stubProductClient)(nil)

func (s *stubProductClient) FetchProductsByPathName(_ context.Context, pathName string) ([]client.MSProduct, error) {
	s.pathCalls = append(s.pathCalls, pathName)
	if s.pathErr != nil {
		return nil, s.pathErr
	}
	return s.byPath[pathName], nil
}

func (s *stubProductClient) FetchProductByID(_ context.Context, id string) (client.MSProduct, error) {
	s.idCalls = append(s.idCalls, id)
	if s.idErr != nil {
		return client.MSProduct{}, s.idErr
	}
	p, ok := s.byID[id]
	if !ok {
		return client.MSProduct{}, errors.New("товар не найден в МС")
	}
	return p, nil
}

func (s *stubProductClient) FetchUOMName(_ context.Context, href string) (string, error) {
	s.uomCalls = append(s.uomCalls, href)
	return s.uomNames[href], nil
}

// stubProductsRepo — заглушка хранилища каталога.
type stubProductsRepo struct {
	saved        []domain.Product
	search       []domain.Product
	searchErr    error
	get          *domain.Product
	getErr       error
	upsertErr    error
	updateAvgErr error
	updateAvg    []float64
}

var _ ProductsRepository = (*stubProductsRepo)(nil)

func (s *stubProductsRepo) UpsertProduct(_ context.Context, p *domain.Product) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.saved = append(s.saved, *p)
	return nil
}

func (s *stubProductsRepo) SearchProducts(_ context.Context, _ string) ([]domain.Product, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.search, nil
}

func (s *stubProductsRepo) GetProduct(_ context.Context, _ string) (*domain.Product, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.get, nil
}

func (s *stubProductsRepo) GetProductsByIDs(_ context.Context, _ []string) ([]domain.Product, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.get != nil {
		return []domain.Product{*s.get}, nil
	}
	return s.search, nil
}

func (s *stubProductsRepo) LoadAllProducts(_ context.Context) ([]domain.Product, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.search, nil
}

func (s *stubProductsRepo) UpdateProductAverageWeight(_ context.Context, _ string, avgKg float64) error {
	if s.updateAvgErr != nil {
		return s.updateAvgErr
	}
	s.updateAvg = append(s.updateAvg, avgKg)
	return nil
}

func TestUpdateAverageWeight(t *testing.T) {
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(nil, nil, repo, nil)

	if err := uc.UpdateAverageWeight(context.Background(), "p1", 0.25); err != nil {
		t.Fatalf("UpdateAverageWeight: %v", err)
	}
	if len(repo.updateAvg) != 1 || repo.updateAvg[0] != 0.25 {
		t.Fatalf("каталог не обновлён: %v", repo.updateAvg)
	}

	if err := uc.UpdateAverageWeight(context.Background(), "p1", 0); err == nil {
		t.Fatal("ожидалась ошибка на неположительном весе")
	}
	if len(repo.updateAvg) != 1 {
		t.Fatalf("невалидный вес не должен доходить до хранилища: %v", repo.updateAvg)
	}

	repo.updateAvgErr = errors.New("сбой БД")
	if err := uc.UpdateAverageWeight(context.Background(), "p1", 0.3); !errors.Is(err, repo.updateAvgErr) {
		t.Fatalf("ошибка хранилища должна пробрасываться: %v", err)
	}
}

const (
	uomKgHref = "https://api.moysklad.ru/api/remap/1.2/entity/uom/kg-uuid"
	uomPcHref = "https://api.moysklad.ru/api/remap/1.2/entity/uom/pc-uuid"
)

func TestExportProducts_HappyPath(t *testing.T) {
	prod1 := msProduct("p1", "11110001", "Говядина охл.", uomKgHref, fullProductAttrs()...)
	prod2 := msProduct("p2", "11110002", "Масло моторное", uomPcHref, fullProductAttrs()...)

	pc := &stubProductClient{
		byPath: map[string][]client.MSProduct{
			testGroupA: {prod1, prod2},
		},
		uomNames: map[string]string{uomKgHref: "кг", uomPcHref: "шт"},
	}
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, repo, nil)

	errs, err := uc.ExportProducts(context.Background(), []ExportItem{
		{ProductID: "p1", GroupPath: testGroupA},
		{ProductID: "p2", GroupPath: testGroupA},
	})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("не ожидалось ошибок, получено: %v", errs)
	}
	if len(repo.saved) != 2 {
		t.Fatalf("сохранено %d, ожидалось 2", len(repo.saved))
	}

	got := repo.saved[0]
	if got.ID != "p1" || got.InternalCode != "11110001" || got.Name != "Говядина охл." {
		t.Errorf("товар p1: %+v", got)
	}
	if got.UOM != "кг" || got.GroupName != testGroupA {
		t.Errorf("uom/группа p1: %+v", got)
	}
	if got.AverageWeight == nil || *got.AverageWeight != 12.5 {
		t.Errorf("средний вес p1: %v", got.AverageWeight)
	}
	if got.ShelfLife == nil || *got.ShelfLife != 30 {
		t.Errorf("срок годности p1: %v", got.ShelfLife)
	}
	if got.PackSize == nil || *got.PackSize != 8 {
		t.Errorf("упаковка p1: %v", got.PackSize)
	}
	if got.InventoryType != "охлаждёнка" || !got.ShortList || got.TrackWeekly {
		t.Errorf("атрибуты p1: %+v", got)
	}

	// Кэш uom: два товара, но разные href — два запроса; второй запуск одного href — один.
	if len(pc.uomCalls) != 2 {
		t.Errorf("запросов uom: %d, ожидалось 2 (по уникальным href)", len(pc.uomCalls))
	}
}

func TestExportProducts_UOMCache(t *testing.T) {
	pc := &stubProductClient{
		byPath: map[string][]client.MSProduct{
			testGroupA: {
				msProduct("p1", "11110001", "Товар 1", uomKgHref, fullProductAttrs()...),
				msProduct("p2", "11110002", "Товар 2", uomKgHref, fullProductAttrs()...),
			},
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, &stubProductsRepo{}, nil)

	_, err := uc.ExportProducts(context.Background(), []ExportItem{
		{ProductID: "p1", GroupPath: testGroupA},
		{ProductID: "p2", GroupPath: testGroupA},
	})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	if len(pc.uomCalls) != 1 {
		t.Errorf("запросов uom: %d, ожидалось 1 (кэш по href)", len(pc.uomCalls))
	}
}

func TestExportProducts_MissingAttribute_FailsThatProduct(t *testing.T) {
	ok := msProduct("p1", "11110001", "Хороший", uomKgHref, fullProductAttrs()...)
	bad := msProduct("p2", "11110002", "Без веса", uomKgHref, fullProductAttrs()[:3]...)

	pc := &stubProductClient{
		byPath:   map[string][]client.MSProduct{testGroupA: {ok, bad}},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, repo, nil)

	errs, err := uc.ExportProducts(context.Background(), []ExportItem{
		{ProductID: "p1", GroupPath: testGroupA},
		{ProductID: "p2", GroupPath: testGroupA},
	})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("ожидалась 1 ошибка, получено: %v", errs)
	}
	if errs[0].Name != "Без веса" {
		t.Errorf("ошибка по товару: %q, ожидалось %q", errs[0].Name, "Без веса")
	}
	if !strings.Contains(errs[0].Err, "Средний вес") {
		t.Errorf("сообщение ошибки: %q, ожидалось упоминание «Средний вес»", errs[0].Err)
	}
	if len(repo.saved) != 1 || repo.saved[0].ID != "p1" {
		t.Errorf("сохранён только хороший товар, получено: %+v", repo.saved)
	}
}

func TestExportProducts_InternalCodeTaken(t *testing.T) {
	pc := &stubProductClient{
		byPath: map[string][]client.MSProduct{
			testGroupA: {msProduct("p1", "11110001", "Дубль кода", uomKgHref, fullProductAttrs()...)},
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	repo := &stubProductsRepo{upsertErr: domain.ErrInternalCodeTaken}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, repo, nil)

	errs, err := uc.ExportProducts(context.Background(), []ExportItem{{ProductID: "p1", GroupPath: testGroupA}})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	if len(errs) != 1 || errs[0].Name != "Дубль кода" || !strings.Contains(errs[0].Err, "код уже занят") {
		t.Errorf("ошибки: %+v", errs)
	}
}

func TestExportProducts_ProductNotFoundInGroup(t *testing.T) {
	pc := &stubProductClient{
		byPath:   map[string][]client.MSProduct{testGroupA: {}},
		uomNames: map[string]string{},
	}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, &stubProductsRepo{}, nil)

	errs, err := uc.ExportProducts(context.Background(), []ExportItem{{ProductID: "ghost", GroupPath: testGroupA}})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Err, "не найден") {
		t.Errorf("ошибки: %+v", errs)
	}
}

func TestExportProducts_GroupingByPath(t *testing.T) {
	pc := &stubProductClient{
		byPath: map[string][]client.MSProduct{
			testGroupA: {msProduct("p1", "11110001", "Товар А", uomKgHref, fullProductAttrs()...)},
			"Группа Б": {msProduct("p2", "11110002", "Товар Б", uomKgHref, fullProductAttrs()...)},
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, &stubProductsRepo{}, nil)

	_, err := uc.ExportProducts(context.Background(), []ExportItem{
		{ProductID: "p1", GroupPath: testGroupA},
		{ProductID: "p2", GroupPath: "Группа Б"},
	})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	// Один запрос на группу.
	if len(pc.pathCalls) != 2 || pc.pathCalls[0] != testGroupA || pc.pathCalls[1] != "Группа Б" {
		t.Errorf("запросы групп: %v", pc.pathCalls)
	}
}

func TestLoadTreeWithProducts(t *testing.T) {
	folderStub := &stubProductFolderClient{
		folders: []client.MSProductFolder{
			msFolder("id-root", testGroupA, ""),
			msFolder("id-child", "Подгруппа", testGroupA),
		},
	}
	pc := &stubProductClient{
		byPath: map[string][]client.MSProduct{
			testGroupA: {
				msProduct("p1", "11110001", "Товар 1", uomKgHref, fullProductAttrs()...),
				msProduct("p2", "11110002", "Товар 2", uomKgHref, fullProductAttrs()...),
			},
			"Группа А/Подгруппа": {},
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	uc := NewGoodsUseCase(folderStub, pc, &stubProductsRepo{}, nil)

	tree, err := uc.LoadTreeWithProducts(context.Background())
	if err != nil {
		t.Fatalf("LoadTreeWithProducts error: %v", err)
	}
	if len(tree) != 1 || len(tree[0].Products) != 2 {
		t.Fatalf("товары корня: %+v", tree)
	}
	if tree[0].PathName != testGroupA {
		t.Errorf("PathName корня: %q", tree[0].PathName)
	}
	if len(tree[0].Children) != 1 || tree[0].Children[0].PathName != "Группа А/Подгруппа" {
		t.Errorf("PathName подгруппы: %+v", tree[0].Children)
	}
	if tree[0].Products[0].Name != "Товар 1" || tree[0].Products[0].Path != testGroupA {
		t.Errorf("товар в дереве: %+v", tree[0].Products[0])
	}
}

func TestAttrHelpers(t *testing.T) {
	attrs := attributeMap([]client.MSAttribute{
		strAttr("строка", "значение"),
		boolAttr("булев", true),
		numAttr("число", 12.5),
		{Name: "число-строкой", Value: json.RawMessage(`"30"`)},
		{Name: "булев-строкой", Value: json.RawMessage(`"true"`)},
	})

	if v, err := attrString(attrs, "строка"); err != nil || v != "значение" {
		t.Errorf("attrString: %q, %v", v, err)
	}
	if _, err := attrString(attrs, "нет-такого"); err == nil {
		t.Error("attrString отсутствующего атрибута — ожидалась ошибка")
	}
	if v, err := attrBool(attrs, "булев"); err != nil || !v {
		t.Errorf("attrBool: %v, %v", v, err)
	}
	if v, err := attrBool(attrs, "булев-строкой"); err != nil || !v {
		t.Errorf("attrBool из строки: %v, %v", v, err)
	}
	if v, err := attrFloat(attrs, "число"); err != nil || v != 12.5 {
		t.Errorf("attrFloat: %v, %v", v, err)
	}
	if v, err := attrFloat(attrs, "число-строкой"); err != nil || v != 30 {
		t.Errorf("attrFloat из строки: %v, %v", v, err)
	}
	if _, err := attrFloat(attrs, "булев"); err == nil {
		t.Error("attrFloat от bool — ожидалась ошибка")
	}
}

func TestSearchProducts(t *testing.T) {
	repo := &stubProductsRepo{search: []domain.Product{{ID: "p1", Name: "Говядина", InternalCode: "11110001"}}}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, &stubProductClient{}, repo, nil)

	got, err := uc.SearchProducts(context.Background(), "говядина")
	if err != nil {
		t.Fatalf("SearchProducts error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "p1" {
		t.Errorf("результат поиска: %+v", got)
	}

	if _, err := uc.SearchProducts(context.Background(), "   "); err == nil {
		t.Error("пустой запрос — ожидалась ошибка")
	}
}

func TestGetProduct(t *testing.T) {
	repo := &stubProductsRepo{get: &domain.Product{ID: "p1", Name: "Говядина"}}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, &stubProductClient{}, repo, nil)

	got, err := uc.GetProduct(context.Background(), "p1")
	if err != nil || got.ID != "p1" {
		t.Errorf("GetProduct: %+v, %v", got, err)
	}

	repo.getErr = domain.ErrProductNotFound
	if _, err := uc.GetProduct(context.Background(), "nope"); !errors.Is(err, domain.ErrProductNotFound) {
		t.Errorf("ожидался ErrProductNotFound, получено: %v", err)
	}
}

func TestSaveProduct(t *testing.T) {
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, &stubProductClient{}, repo, nil)

	p := &domain.Product{ID: "p1", Name: "Говядина"}
	if err := uc.SaveProduct(context.Background(), p); err != nil {
		t.Fatalf("SaveProduct error: %v", err)
	}
	if len(repo.saved) != 1 || repo.saved[0].Name != "Говядина" {
		t.Errorf("сохранено: %+v", repo.saved)
	}
}

func TestResyncProduct_HappyPath_KeepsGroupName(t *testing.T) {
	repo := &stubProductsRepo{
		get: &domain.Product{ID: "p1", GroupName: "1 - Ассортимент на продажу/013 - SaltLab"},
	}
	pc := &stubProductClient{
		byID: map[string]client.MSProduct{
			"p1": msProduct("p1", "11110001", "Говядина охл. (обновлено)", uomKgHref, fullProductAttrs()...),
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, repo, nil)

	got, err := uc.ResyncProduct(context.Background(), "p1")
	if err != nil {
		t.Fatalf("ResyncProduct error: %v", err)
	}
	if got.Name != "Говядина охл. (обновлено)" {
		t.Errorf("имя не обновилось: %q", got.Name)
	}
	if got.GroupName != "1 - Ассортимент на продажу/013 - SaltLab" {
		t.Errorf("group_name изменился: %q", got.GroupName)
	}
	if len(repo.saved) != 1 || repo.saved[0].Name != "Говядина охл. (обновлено)" {
		t.Errorf("upsert не вызван: %+v", repo.saved)
	}
	if len(pc.idCalls) != 1 || pc.idCalls[0] != "p1" {
		t.Errorf("запрос по id: %v", pc.idCalls)
	}
}

func TestResyncProduct_MissingAttr_NoUpsert(t *testing.T) {
	repo := &stubProductsRepo{get: &domain.Product{ID: "p1", GroupName: testGroupA}}
	pc := &stubProductClient{
		byID: map[string]client.MSProduct{
			"p1": msProduct("p1", "11110001", "Без веса", uomKgHref, fullProductAttrs()[:3]...),
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, repo, nil)

	_, err := uc.ResyncProduct(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "Средний вес") {
		t.Fatalf("ожидалась ошибка про «Средний вес», получено: %v", err)
	}
	if len(repo.saved) != 0 {
		t.Errorf("upsert не должен был вызваться: %+v", repo.saved)
	}
}

func TestResyncProduct_NotFound(t *testing.T) {
	repo := &stubProductsRepo{getErr: domain.ErrProductNotFound}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, &stubProductClient{}, repo, nil)

	if _, err := uc.ResyncProduct(context.Background(), "nope"); !errors.Is(err, domain.ErrProductNotFound) {
		t.Errorf("ожидался ErrProductNotFound, получено: %v", err)
	}
}

// stubWikiSyncer — стаб ProductPageSynchronizer: запоминает вызовы.
type stubWikiSyncer struct {
	calls []wikiSyncCall
	err   error
}

type wikiSyncCall struct {
	productID, name, avgWeight string
}

func (s *stubWikiSyncer) EnsureProductPage(_ context.Context, productID, name, averageWeight string) error {
	s.calls = append(s.calls, wikiSyncCall{productID: productID, name: name, avgWeight: averageWeight})

	return s.err
}

func TestExportProducts_SyncsWikiPage(t *testing.T) {
	pc := &stubProductClient{
		byPath: map[string][]client.MSProduct{
			testGroupA: {
				msProduct("p1", "11110001", "Говядина охл.", uomKgHref, fullProductAttrs()...),
				msProduct("p2", "11110002", "Товар 2", uomKgHref, fullProductAttrs()...),
			},
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	wiki := &stubWikiSyncer{}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, &stubProductsRepo{}, wiki)

	_, err := uc.ExportProducts(context.Background(), []ExportItem{
		{ProductID: "p1", GroupPath: testGroupA},
		{ProductID: "p2", GroupPath: testGroupA},
	})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	if len(wiki.calls) != 2 {
		t.Fatalf("EnsureProductPage вызван %d раз, ожидалось 2: %+v", len(wiki.calls), wiki.calls)
	}
	if c := wiki.calls[0]; c.productID != "p1" || c.name != "Говядина охл." || c.avgWeight != "12.5" {
		t.Fatalf("вызов 1 неверен: %+v", c)
	}
	if c := wiki.calls[1]; c.productID != "p2" || c.avgWeight != "12.5" {
		t.Fatalf("вызов 2 неверен: %+v", c)
	}
}

func TestExportProducts_WikiErrorIsReportedNotFatal(t *testing.T) {
	pc := &stubProductClient{
		byPath: map[string][]client.MSProduct{
			testGroupA: {msProduct("p1", "11110001", "Говядина охл.", uomKgHref, fullProductAttrs()...)},
		},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	wiki := &stubWikiSyncer{err: errors.New("заголовок занят")}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, &stubProductsRepo{}, wiki)

	errs, err := uc.ExportProducts(context.Background(), []ExportItem{
		{ProductID: "p1", GroupPath: testGroupA},
	})
	if err != nil {
		t.Fatalf("ExportProducts error: %v", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Err, "страницу вики") {
		t.Fatalf("ожидалась ошибка вики в отчёте, получено: %v", errs)
	}
}
