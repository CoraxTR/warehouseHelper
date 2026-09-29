package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// siteCheckFeed — загрузка фида сайта по HTTP (модуль «Проверка сайта»).
// Реализует шов sitecheck/usecase.Fetcher: разбор тела — в sitecheck.ParseFeed,
// адаптер отдаёт байты как есть.
//
// Модуль сам net/http не импортирует — правило проекта: внешние запросы идут
// через швы и адаптеры (как запросы к МС через msclient).
type siteCheckFeed struct {
	url    string
	client *http.Client
}

// siteCheckTimeout — таймаут одного запроса фида: сайт отдаёт файл размером в
// сотни килобайт, больше минуты на него не нужно.
const siteCheckTimeout = time.Minute

// siteCheckMaxBytes — предел тела ответа: фид поиска сейчас ~150 КБ, лимит
// защищает от бесконечного потока (робот-заглушка, редирект на что-то большое).
const siteCheckMaxBytes = 16 << 20

// NewSiteCheckFeed собирает адаптер загрузки фида.
func NewSiteCheckFeed(url string) *siteCheckFeed {
	return &siteCheckFeed{
		url:    url,
		client: &http.Client{Timeout: siteCheckTimeout},
	}
}

// FetchFeed загружает тело фида. Не-200 — ошибка с кодом: фид отдаётся статикой,
// любой другой ответ означает, что сайт не готов (или адрес неверен).
func (f *siteCheckFeed) FetchFeed(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return nil, fmt.Errorf("sitecheck: запрос фида %s: %w", f.url, err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sitecheck: запрос фида %s: %w", f.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitecheck: фид %s: статус %d", f.url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, siteCheckMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("sitecheck: чтение фида %s: %w", f.url, err)
	}

	return data, nil
}
