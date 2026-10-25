package usecase

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

// SetSiteURL — записать адрес позиции на сайте (products.site_url), пустая
// строка стирает адрес. Ввод ручной, поэтому базовые проверки здесь: только
// http(s) и разумная длина — мусор в ссылке ломает проверку сайта.
func (uc *GoodsUseCase) SetSiteURL(ctx context.Context, productID, siteURL string) error {
	done := metrics.Track(trackPkg, "SetSiteURL")
	defer done()

	productID = strings.TrimSpace(productID)
	if productID == "" {
		return fmt.Errorf("не выбран товар: %w", domain.ErrProductNotFound)
	}

	siteURL = strings.TrimSpace(siteURL)
	if siteURL != "" {
		if len(siteURL) > 2048 {
			return fmt.Errorf("ссылка длиннее 2048 символов")
		}
		parsed, err := url.Parse(siteURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("ссылка должна начинаться с http:// или https://")
		}
	}

	if err := uc.repo.SetProductSiteURL(ctx, productID, siteURL); err != nil {
		return fmt.Errorf("сохранить ссылку: %w", err)
	}

	uc.notifyCatalogChanged(ctx)

	return nil
}
