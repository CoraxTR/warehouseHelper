package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// msFolder собирает папку МС в стиле ответа /entity/productfolder:
// meta.href, name, pathName.
func msFolder(href, name, pathName string) client.MSProductFolder {
	return client.MSProductFolder{
		Meta: struct {
			Href string `json:"href"`
		}{
			Href: href,
		},
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
				msFolder("href-030", "030 - Минеральное масло", "1 - Ассортимент на продажу/2 - Моторные масла"),
				msFolder("href-2-motor", "2 - Моторные масла", "1 - Ассортимент на продажу"),
				msFolder("href-020", "020 - Синтетическое масло", "1 - Ассортимент на продажу/2 - Моторные масла"),
				msFolder("href-root", "1 - Ассортимент на продажу", ""),
				msFolder("href-2-filters", "2 - Фильтры", "1 - Ассортимент на продажу"),
			},
			want: []*FolderNode{
				{
					Name: "1 - Ассортимент на продажу",
					Href: "href-root",
					Children: []*FolderNode{
						{
							Name: "2 - Моторные масла",
							Href: "href-2-motor",
							Children: []*FolderNode{
								{Name: "020 - Синтетическое масло", Href: "href-020"},
								{Name: "030 - Минеральное масло", Href: "href-030"},
							},
						},
						{Name: "2 - Фильтры", Href: "href-2-filters"},
					},
				},
			},
		},
		{
			name: "родитель отсутствует в списке — папка остаётся корнем",
			folders: []client.MSProductFolder{
				msFolder("href-orphan-2", "2 - Моторные масла", "1 - Ассортимент на продажу/2 - Моторные масла"),
				msFolder("href-orphan-030", "030 - Минеральное масло", "Нет такого корня"),
				msFolder("href-root", "1 - Ассортимент на продажу", ""),
			},
			want: []*FolderNode{
				{Name: "030 - Минеральное масло", Href: "href-orphan-030"},
				{Name: "1 - Ассортимент на продажу", Href: "href-root"},
				{Name: "2 - Моторные масла", Href: "href-orphan-2"},
			},
		},
		{
			name: "сортировка детей и корней по имени без учёта регистра",
			folders: []client.MSProductFolder{
				msFolder("href-c", "c", "Корень"),
				msFolder("href-A", "A", "Корень"),
				msFolder("href-b", "b", "Корень"),
				msFolder("href-root", "Корень", ""),
				msFolder("href-z", "Zeta", ""),
				msFolder("href-alpha", "alpha", ""),
			},
			want: []*FolderNode{
				{Name: "alpha", Href: "href-alpha"},
				{Name: "Zeta", Href: "href-z"},
				{
					Name: "Корень",
					Href: "href-root",
					Children: []*FolderNode{
						{Name: "A", Href: "href-A"},
						{Name: "b", Href: "href-b"},
						{Name: "c", Href: "href-c"},
					},
				},
			},
		},
		{
			name: "дубль полного пути — мапа хранит первого, узел один",
			folders: []client.MSProductFolder{
				msFolder("href-root", "1 - Ассортимент на продажу", ""),
				msFolder("href-dup-1", "2 - Моторные масла", "1 - Ассортимент на продажу"),
				msFolder("href-dup-2", "2 - Моторные масла", "1 - Ассортимент на продажу"),
			},
			want: []*FolderNode{
				{
					Name: "1 - Ассортимент на продажу",
					Href: "href-root",
					Children: []*FolderNode{
						{Name: "2 - Моторные масла", Href: "href-dup-1"},
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
		msFolder("href-root", "1 - Ассортимент на продажу", ""),
		msFolder("href-leaf", "2 - Фильтры", "1 - Ассортимент на продажу"),
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
					msFolder("href-root", "1 - Ассортимент на продажу", ""),
					msFolder("href-child", "2 - Фильтры", "1 - Ассортимент на продажу"),
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
			uc := NewGoodsUseCase(tt.stub)
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
