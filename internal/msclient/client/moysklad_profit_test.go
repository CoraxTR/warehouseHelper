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

func TestProfitReportEndpoint(t *testing.T) {
	from := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.July, 31, 23, 59, 59, 0, time.UTC)

	tests := []struct {
		name       string
		urlStart   string
		filter     ProfitFilter
		wantPath   string
		wantFilter string // подстрока query filter (пустая — фильтра нет)
	}{
		{
			name:     "пачка товаров — полные href через ;",
			urlStart: "https://api.moysklad.ru/api/remap/1.2/entity/",
			filter:   ProfitFilter{ProductIDs: []string{"id-1", "id-2"}},
			wantPath: "https://api.moysklad.ru/api/remap/1.2/report/profit/byproduct",
			wantFilter: "product=https://api.moysklad.ru/api/remap/1.2/entity/product/id-1;" +
				"product=https://api.moysklad.ru/api/remap/1.2/entity/product/id-2",
		},
		{
			name:       "группа — productFolder с полным href",
			urlStart:   "https://api.moysklad.ru/api/remap/1.2/entity/",
			filter:     ProfitFilter{ProductFolderID: "171b04e8-fe51-11ea-0a80-03480008d1e8"},
			wantPath:   "https://api.moysklad.ru/api/remap/1.2/report/profit/byproduct",
			wantFilter: "productFolder=https://api.moysklad.ru/api/remap/1.2/entity/productfolder/171b04e8-fe51-11ea-0a80-03480008d1e8",
		},
		{
			name:       "без фильтра — все товары",
			urlStart:   "https://api.moysklad.ru/api/remap/1.2/entity/",
			filter:     ProfitFilter{},
			wantPath:   "https://api.moysklad.ru/api/remap/1.2/report/profit/byproduct",
			wantFilter: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msac := &MSAPIClient{msConfig: &config.MSConfig{URLstart: tt.urlStart}}

			got, err := msac.profitReportEndpoint("month", from, to, tt.filter, 0, 1000)
			if err != nil {
				t.Fatalf("profitReportEndpoint() error: %v", err)
			}

			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("непарсится URL: %v", err)
			}

			if gotPath := u.Scheme + "://" + u.Host + u.Path; gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}

			q := u.Query()
			if q.Get("interval") != "month" {
				t.Errorf("interval = %q, want month", q.Get("interval"))
			}
			if q.Get("momentFrom") != "2026-07-01 00:00:00" {
				t.Errorf("momentFrom = %q, want 2026-07-01 00:00:00", q.Get("momentFrom"))
			}
			if q.Get("momentTo") != "2026-07-31 23:59:59" {
				t.Errorf("momentTo = %q, want 2026-07-31 23:59:59", q.Get("momentTo"))
			}
			if q.Get("limit") != "1000" || q.Get("offset") != "0" {
				t.Errorf("limit/offset = %q/%q, want 1000/0", q.Get("limit"), q.Get("offset"))
			}

			if gotFilter := q.Get("filter"); gotFilter != tt.wantFilter {
				t.Errorf("filter = %q, want %q", gotFilter, tt.wantFilter)
			}
		})
	}
}

func TestFetchProfitTurnover(t *testing.T) {
	const (
		totalSize = 1500 // > profitPageLimit — проверяем пагинацию по offset
		pageLimit = 1000
	)

	var requests int
	var gotFilters []string
	var gotPaths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Запрос валидации ключа воркерпулом (entity/organization) не считаем.
		if r.URL.Path != "/report/profit/byproduct" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}

		requests++
		gotPaths = append(gotPaths, r.URL.Path)
		gotFilters = append(gotFilters, r.URL.Query().Get("filter"))

		q := r.URL.Query()
		if q.Get("interval") != "month" {
			t.Errorf("interval = %q, want month", q.Get("interval"))
		}
		if q.Get("momentFrom") != "2026-07-01 00:00:00" {
			t.Errorf("momentFrom = %q, want 2026-07-01 00:00:00", q.Get("momentFrom"))
		}

		offset := 0
		_, _ = fmt.Sscanf(q.Get("offset"), "%d", &offset)

		report := ProfitReport{}
		report.Meta = struct {
			Size   int `json:"size"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		}{Size: totalSize, Limit: pageLimit, Offset: offset}

		for i := 0; i < pageLimit && offset+i < totalSize; i++ {
			n := offset + i
			row := ProfitRow{}
			row.Assortment.Meta.Href = fmt.Sprintf("https://api.moysklad.ru/api/remap/1.2/entity/product/id-%d", n)
			row.Assortment.Name = fmt.Sprintf("Товар %d", n)
			row.SellQuantity = float64(n) + 0.5
			row.ReturnQuantity = 0.25
			report.Rows = append(report.Rows, row)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(report); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	msCfg := &config.MSConfig{
		URLstart:      server.URL + "/entity/",
		AuthHeader:    "Bearer",
		Refs:          &config.MSRefs{OrgID: "org-test"},
		OthersAPIKEYS: []config.MSWorker{{Name: "test-worker", APIKey: "test-key"}},
		TimeSpan:      time.Second,
		RequestCap:    1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	defer pool.Stop()

	msac := &MSAPIClient{workerpool: pool, msConfig: msCfg}

	from := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.July, 31, 23, 59, 59, 0, time.UTC)

	rows, err := msac.FetchProfitTurnover(context.Background(), from, to, "month", ProfitFilter{ProductIDs: []string{"id-1", "id-2"}})
	if err != nil {
		t.Fatalf("FetchProfitTurnover() error: %v", err)
	}

	if len(rows) != totalSize {
		t.Errorf("len(rows) = %d, want %d", len(rows), totalSize)
	}
	if requests != 2 {
		t.Errorf("HTTP-запросов = %d, want 2 (пагинация по offset)", requests)
	}
	if rows[0].Assortment.Name != "Товар 0" || rows[totalSize-1].Assortment.Name != fmt.Sprintf("Товар %d", totalSize-1) {
		t.Errorf("крайние строки не сошлись: first=%q last=%q", rows[0].Assortment.Name, rows[totalSize-1].Assortment.Name)
	}

	wantFilter := "product=" + msCfg.URLstart + "product/id-1;product=" + msCfg.URLstart + "product/id-2"
	for i, got := range gotFilters {
		if got != wantFilter {
			t.Errorf("запрос %d: filter = %q, want %q", i, got, wantFilter)
		}
	}
	for i, p := range gotPaths {
		if p != "/report/profit/byproduct" {
			t.Errorf("запрос %d: path = %q, want /report/profit/byproduct", i, p)
		}
	}
}
