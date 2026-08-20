package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/msclient/workerpool"
)

func TestProductFolderListEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		urlStart   string
		offset     int
		limit      int
		wantPath   string
		wantOffset string
		wantLimit  string
	}{
		{
			name:       "URLstart с /entity/ — без двойного entity, пагинация в query",
			urlStart:   "https://api.moysklad.ru/api/remap/1.2/entity/",
			offset:     0,
			limit:      1000,
			wantPath:   "/api/remap/1.2/entity/productfolder",
			wantOffset: "0",
			wantLimit:  "1000",
		},
		{
			name:       "вторая страница: offset накапливается",
			urlStart:   "https://api.moysklad.ru/api/remap/1.2/entity/",
			offset:     2000,
			limit:      1000,
			wantPath:   "/api/remap/1.2/entity/productfolder",
			wantOffset: "2000",
			wantLimit:  "1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msac := &MSAPIClient{msConfig: &config.MSConfig{URLstart: tt.urlStart}}

			got, err := msac.productFolderListEndpoint(tt.offset, tt.limit)
			if err != nil {
				t.Fatalf("productFolderListEndpoint() error: %v", err)
			}

			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("url.Parse(%q) error: %v", got, err)
			}

			if u.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", u.Path, tt.wantPath)
			}

			q := u.Query()
			if q.Get("offset") != tt.wantOffset {
				t.Errorf("query offset = %q, want %q (полный URL: %s)", q.Get("offset"), tt.wantOffset, got)
			}
			if q.Get("limit") != tt.wantLimit {
				t.Errorf("query limit = %q, want %q (полный URL: %s)", q.Get("limit"), tt.wantLimit, got)
			}
		})
	}
}

func TestUnmarshalMSProductFolderList(t *testing.T) {
	const raw = `{
		"meta": {"size": 2, "limit": 1000, "offset": 0},
		"rows": [
			{
				"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/productfolder/11111111-1111-1111-1111-111111111111"},
				"id": "11111111-1111-1111-1111-111111111111",
				"name": "Коробки",
				"pathName": ""
			},
			{
				"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/productfolder/22222222-2222-2222-2222-222222222222"},
				"id": "22222222-2222-2222-2222-222222222222",
				"name": "Маленькие",
				"pathName": "Коробки"
			}
		]
	}`

	var list MSProductFolderList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if list.Meta.Size != 2 || list.Meta.Limit != 1000 || list.Meta.Offset != 0 {
		t.Errorf("meta = %+v, want size=2 limit=1000 offset=0", list.Meta)
	}

	if len(list.Rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(list.Rows))
	}

	root := list.Rows[0]
	if root.ID == "" || root.Meta.Href == "" {
		t.Errorf("у корневой папки пустые id/href: %+v", root)
	}
	if root.Name != "Коробки" || root.PathName != "" {
		t.Errorf("root = %+v, want name=Коробки pathName=\"\"", root)
	}

	child := list.Rows[1]
	if child.Name != "Маленькие" || child.PathName != "Коробки" {
		t.Errorf("child = %+v, want name=Маленькие pathName=Коробки", child)
	}
}

func TestFetchProductFoldersPagination(t *testing.T) {
	const totalSize = 2500
	const pageLimit = 1000
	wantPages := totalSize / pageLimit
	if totalSize%pageLimit != 0 {
		wantPages++
	}

	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/entity/productfolder" {
			// validateKey при создании воркерпула (GET на Hrefs.Orghref)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))

			return
		}

		requests++

		q := r.URL.Query()
		offset := 0
		fmt.Sscanf(q.Get("offset"), "%d", &offset)
		limit := pageLimit
		fmt.Sscanf(q.Get("limit"), "%d", &limit)

		if limit != pageLimit {
			t.Errorf("limit = %d, want %d", limit, pageLimit)
		}

		list := MSProductFolderList{}
		list.Meta = struct {
			Size   int `json:"size"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		}{Size: totalSize, Limit: pageLimit, Offset: offset}

		for i := 0; i < limit && offset+i < totalSize; i++ {
			n := offset + i
			list.Rows = append(list.Rows, MSProductFolder{
				Meta: struct {
					Href string `json:"href"`
				}{Href: fmt.Sprintf("%s/entity/productfolder/id-%d", server.URL, n)},
				ID:       fmt.Sprintf("id-%d", n),
				Name:     fmt.Sprintf("Папка %d", n),
				PathName: "",
			})
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	msCfg := &config.MSConfig{
		URLstart:      server.URL + "/entity/",
		AuthHeader:    "Bearer",
		Hrefs:         &config.MShrefs{Orghref: server.URL},
		OthersAPIKEYS: []config.MSWorker{{Name: "test-worker", APIKey: "test-key"}},
		TimeSpan:      time.Second,
		RequestCap:    1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	defer pool.Stop()

	if len(pool.OtherWorkers) != 1 {
		t.Fatalf("воркерпул создал %d other-воркеров, want 1 (validateKey не прошёл?)", len(pool.OtherWorkers))
	}

	msac := &MSAPIClient{workerpool: pool, msConfig: msCfg}

	folders, err := msac.FetchProductFolders(context.Background())
	if err != nil {
		t.Fatalf("FetchProductFolders() error: %v", err)
	}

	if len(folders) != totalSize {
		t.Errorf("len(folders) = %d, want %d", len(folders), totalSize)
	}
	if requests != wantPages {
		t.Errorf("HTTP-запросов = %d, want %d (пагинация по offset)", requests, wantPages)
	}
	if folders[0].Name != "Папка 0" {
		t.Errorf("folders[0].Name = %q, want %q", folders[0].Name, "Папка 0")
	}
	if last := folders[totalSize-1]; last.Name != fmt.Sprintf("Папка %d", totalSize-1) {
		t.Errorf("последний элемент = %q, want %q", last.Name, fmt.Sprintf("Папка %d", totalSize-1))
	}
}
