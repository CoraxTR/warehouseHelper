package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"warehouseHelper/internal/msclient/client"
)

// ProductFolderClient — источник папок товаров из МойСклад (интерфейс на стороне потребителя).
type ProductFolderClient interface {
	FetchProductFolders(ctx context.Context) ([]client.MSProductFolder, error)
}

// FolderNode — узел дерева папок товаров. Href — meta.href папки из МС (id для чекбоксов).
type FolderNode struct {
	Name     string
	Href     string
	Children []*FolderNode
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
		byFullPath[fullPath] = &FolderNode{Name: folders[i].Name, Href: folders[i].Meta.Href}
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

// sortNodes сортирует узлы по имени без учёта регистра.
func sortNodes(nodes []*FolderNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
}

// GoodsUseCase — сценарий «выгрузить дерево папок товаров из МС».
type GoodsUseCase struct {
	folders ProductFolderClient
}

// NewGoodsUseCase создаёт юзкейс выгрузки дерева папок товаров.
func NewGoodsUseCase(f ProductFolderClient) *GoodsUseCase {
	return &GoodsUseCase{folders: f}
}

// LoadFolderTree тянет папки из МС и строит дерево.
func (uc *GoodsUseCase) LoadFolderTree(ctx context.Context) ([]*FolderNode, error) {
	folders, err := uc.folders.FetchProductFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить папки товаров из МойСклад: %w", err)
	}

	return BuildFolderTree(folders), nil
}
