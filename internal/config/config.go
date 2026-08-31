package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	*AppConfig
	*MSConfig
	*RefGoConfig
	*PGConfig
	*TelegramConfig
	*QRConfig
}

// envFilePath возвращает абсолютный путь к .env: сначала ищет в текущем
// каталоге (запуск из корня репозитория), затем в родительском
// (запуск из каталога cmd/). Пустая строка — файл не найден.
func envFilePath() string {
	for _, path := range []string{".env", "../.env"} {
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}
	return ""
}

func NewConfig() *Config {
	envPath := envFilePath()
	if envPath == "" {
		panic("Cannot read config file")
	}
	if err := godotenv.Load(envPath); err != nil {
		panic("Cannot read config file: " + envPath)
	}

	apc := loadAppconfig()
	msc := loadMSConfig()
	rfc := loadRefGoConfig()
	pfc := loadPGConfig()
	tgc := loadTelegramConfig()
	qrc := loadQRConfig()

	if os.Getenv("RG_LATESTORDER") == "" {
		panic("RG_LATESTORDER does not exist")
	}

	return &Config{
		AppConfig:      apc,
		MSConfig:       msc,
		RefGoConfig:    rfc,
		PGConfig:       pfc,
		TelegramConfig: tgc,
		QRConfig:       qrc,
	}
}

type AppConfig struct {
	HTTPAddress       string
	TempCleanupMaxAge time.Duration
	// WeightsHistoryLimit — сколько последних весов единицы хранить на товар
	// (FIFO, модуль среднего веса); настройка приложения, не в схеме.
	WeightsHistoryLimit int
}

// QRConfig — модуль «Честный знак»: фото кодов маркировки по заказам.
// PhotosDir — корневая папка фото (по умолчанию ../QRCodes — от каталога cmd/,
// как tempdir "../temp"); PhotosMaxAge — срок жизни фото (по умолчанию неделя,
// всё старше удаляется при обращении к списку).
type QRConfig struct {
	PhotosDir    string
	PhotosMaxAge time.Duration
}

func loadQRConfig() *QRConfig {
	photosDir := os.Getenv("QR_PHOTOS_DIR")
	if photosDir == "" {
		photosDir = "../QRCodes"
	}

	photosMaxAge := 7 * 24 * time.Hour
	if hoursStr := os.Getenv("QR_PHOTOS_MAXAGE_HOURS"); hoursStr != "" {
		if hours, err := strconv.Atoi(hoursStr); err == nil && hours > 0 {
			photosMaxAge = time.Duration(hours) * time.Hour
		}
	}

	return &QRConfig{
		PhotosDir:    photosDir,
		PhotosMaxAge: photosMaxAge,
	}
}

func loadAppconfig() *AppConfig {
	httpAddress := os.Getenv("APP_HTTPADDRESS")
	if httpAddress == "" {
		os.Exit(1)
	}

	// Время жизни файлов в temp: по умолчанию сутки; настраивается
	// через APP_TEMPCLEANUP_MAXAGE_HOURS (целое число часов).
	tempCleanupMaxAge := 24 * time.Hour
	if hoursStr := os.Getenv("APP_TEMPCLEANUP_MAXAGE_HOURS"); hoursStr != "" {
		if hours, err := strconv.Atoi(hoursStr); err == nil && hours > 0 {
			tempCleanupMaxAge = time.Duration(hours) * time.Hour
		}
	}

	// Лимит истории весов единицы на товар (модуль среднего веса):
	// по умолчанию 100; PRODUCT_WEIGHTS_HISTORY <= 0 — сброс на дефолт.
	weightsHistoryLimit := 100
	if nStr := os.Getenv("PRODUCT_WEIGHTS_HISTORY"); nStr != "" {
		if n, err := strconv.Atoi(nStr); err == nil && n > 0 {
			weightsHistoryLimit = n
		}
	}

	return &AppConfig{
		HTTPAddress:         httpAddress,
		TempCleanupMaxAge:   tempCleanupMaxAge,
		WeightsHistoryLimit: weightsHistoryLimit,
	}
}

type MSWorker struct {
	APIKey string
	Name   string
}

type MSConfig struct {
	Refs *MSRefs

	WarehouseAPIKEYS []MSWorker
	OthersAPIKEYS    []MSWorker
	TimeSpan         time.Duration
	RequestCap       int
	SellTypeID       string
	RefGoNumberID    string
	CourierID        string
	TimeFormat       string
	URLstart         string
	AuthHeader       string
	EncodeHeader     string
}

func loadMSConfig() *MSConfig {
	msrefs := loadMSRefs()

	wrhsakeysStr := os.Getenv("MSAPI_KEYS_WAREHOUSE")
	wrhsakeys := strings.Split(wrhsakeysStr, ",")
	wrhworkers := make([]MSWorker, 0, len(wrhsakeys))

	for _, key := range wrhsakeys {
		parts := strings.Split(key, "-")
		if len(parts) == 2 {
			wrhworkers = append(wrhworkers, MSWorker{APIKey: parts[0], Name: parts[1]})
		}
	}

	othrsakeysStr := os.Getenv("MSAPI_KEYS_OTHERS")
	othrsakeys := strings.Split(othrsakeysStr, ",")

	othrworkers := make([]MSWorker, 0, len(othrsakeys))
	for _, key := range othrsakeys {
		parts := strings.Split(key, "-")
		if len(parts) == 2 {
			othrworkers = append(othrworkers, MSWorker{APIKey: parts[0], Name: parts[1]})
		}
	}

	if len(wrhworkers) == 0 && len(othrworkers) == 0 {
		os.Exit(1)
	}

	tspnint, err := strconv.Atoi(os.Getenv("MSAPI_REQUESTCAPTIMESPAN"))
	if err != nil {
		os.Exit(1)
	}

	tspn := time.Duration(int64(tspnint)) * time.Second

	rqcap, err := strconv.Atoi(os.Getenv("MSAPI_REQUESTCAP"))
	if err != nil {
		os.Exit(1)
	}

	selltypeID := os.Getenv("MSAPI_SELLTYPEID")
	if selltypeID == "" {
		os.Exit(1)
	}

	refgonumberid := os.Getenv("MSAPI_REFGONUMBERID")
	if refgonumberid == "" {
		os.Exit(1)
	}

	courierid := os.Getenv("MSAPI_COURIERID")
	if courierid == "" {
		os.Exit(1)
	}

	timeFormat := os.Getenv("MSAPI_TIMEFORMAT")
	if timeFormat == "" {
		os.Exit(1)
	}

	urlstart := os.Getenv("MSAPI_URLSTART")
	if urlstart == "" {
		os.Exit(1)
	}

	authheader := os.Getenv("MSAPI_AUTHHEADER")
	if authheader == "" {
		os.Exit(1)
	}

	encodeheader := os.Getenv("MSAPI_ENCODEHEADER")
	if encodeheader == "" {
		os.Exit(1)
	}

	return &MSConfig{
		Refs: msrefs,

		WarehouseAPIKEYS: wrhworkers,
		OthersAPIKEYS:    othrworkers,
		TimeSpan:         tspn,
		RequestCap:       rqcap,
		SellTypeID:       selltypeID,
		RefGoNumberID:    refgonumberid,
		CourierID:        courierid,
		TimeFormat:       timeFormat,
		URLstart:         urlstart,
		AuthHeader:       authheader,
		EncodeHeader:     encodeheader,
	}
}

// MSRefs — идентификаторы сущностей МойСклад, из которых собираются href'ы:
// href = MSAPI_URLSTART + путь сущности + id. Пути:
//   - статусы:          customerorder/metadata/states/{id}
//   - атрибуты заказа:  customerorder/metadata/attributes/{id}
//   - значение кастом-сущности: customentity/{тип}/{id}
//   - сотрудник:        employee/{id}
//   - шаблон печати:    customtemplate/{id}
//   - организация:      organization/{id}
type MSRefs struct {
	ReadystateID      string
	ShipedstateID     string
	SellTypeOtherID   string
	SellTypeOtherType string
	OrgID             string
	RefGoCourierID    string
	PrinttemplateID   string
}

func loadMSRefs() *MSRefs {
	readystateID := os.Getenv("MSAPI_READYSTATE_ID")
	if readystateID == "" {
		os.Exit(1)
	}

	shipedstateID := os.Getenv("MSAPI_SHIPEDSTATE_ID")
	if shipedstateID == "" {
		os.Exit(1)
	}

	sellTypeOtherID := os.Getenv("MSAPI_SELLTYPEOTHER_ID")
	if sellTypeOtherID == "" {
		os.Exit(1)
	}

	sellTypeOtherType := os.Getenv("MSAPI_SELLTYPEOTHER_TYPE")
	if sellTypeOtherType == "" {
		os.Exit(1)
	}

	orgID := os.Getenv("MSAPI_ORG_ID")
	if orgID == "" {
		os.Exit(1)
	}

	refGoCourierID := os.Getenv("MSAPI_REFGOCOURIER_ID")
	if refGoCourierID == "" {
		os.Exit(1)
	}

	printtemplateID := os.Getenv("MSAPI_PRINTTEMPLATE_ID")
	if printtemplateID == "" {
		os.Exit(1)
	}

	return &MSRefs{
		ReadystateID:      readystateID,
		ShipedstateID:     shipedstateID,
		SellTypeOtherID:   sellTypeOtherID,
		SellTypeOtherType: sellTypeOtherType,
		OrgID:             orgID,
		RefGoCourierID:    refGoCourierID,
		PrinttemplateID:   printtemplateID,
	}
}

type RefGoConfig struct {
	RGNextOrder int64

	// Параметры модуля сверки с перевозчиком. Цены зон — рубли,
	// лимит веса — килограммы, комиссии — проценты.
	// Если хотя бы одна переменная не задана или невалидна,
	// модуль сверки отключается (CheckAgainstModule = false).
	CheckAgainstModule bool
	RGGreenzonePrice   float64
	RGYellowzonePrice  float64
	RGOrangezonePrice  float64
	RGRedzonePrice     float64
	RGBluezonePrice    float64
	RGWeightlimit      float64
	RGCashtax          float64
	RGCardtax          float64
}

func loadRefGoConfig() *RefGoConfig {
	if os.Getenv("RG_LATESTORDER") == "" {
		os.Exit(1)
	}

	latestorder, err := strconv.Atoi(os.Getenv("RG_LATESTORDER"))
	if err != nil {
		os.Exit(1)
	}

	rgc := &RefGoConfig{
		RGNextOrder: int64(latestorder),
	}

	checkVars := map[string]*float64{
		"RG_GREENZONE_PRICE":  &rgc.RGGreenzonePrice,
		"RG_YELLOWZONE_PRICE": &rgc.RGYellowzonePrice,
		"RG_ORANGEZONE_PRICE": &rgc.RGOrangezonePrice,
		"RG_REDZONE_PRICE":    &rgc.RGRedzonePrice,
		"RG_BLUEZONE_PRICE":   &rgc.RGBluezonePrice,
		"RG_WEIGHTLIMIT":      &rgc.RGWeightlimit,
		"RG_CASHTAX":          &rgc.RGCashtax,
		"RG_CARDTAX":          &rgc.RGCardtax,
	}

	rgc.CheckAgainstModule = true
	for name, dst := range checkVars {
		v, ok := parseEnvFloat(name)
		if !ok {
			rgc.CheckAgainstModule = false
			slog.Info(fmt.Sprintf("RefGo check module disabled: %s is empty or invalid", name))

			continue
		}

		*dst = v
	}

	return rgc
}

// parseEnvFloat читает числовую переменную окружения; запятая допускается
// как десятичный разделитель. ok=false, если переменная пуста или не число.
func parseEnvFloat(name string) (float64, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}

	v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64)
	if err != nil {
		return 0, false
	}

	return v, true
}

func (rgc *RefGoConfig) ChangeRefGoLatest(latestOrder int64) error {
	envFile := envFilePath()
	if envFile == "" {
		return errors.New("файл .env не найден")
	}

	content, err := os.ReadFile(envFile)
	if err != nil {
		return fmt.Errorf("ошибка чтения файла: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	found := false

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "RG_LATESTORDER=") {
			lines[i] = fmt.Sprintf("RG_LATESTORDER=%d", latestOrder)
			found = true

			break
		}
	}

	if !found {
		lines = append(lines, fmt.Sprintf("RG_LATESTORDER=%d", latestOrder))
	}

	err = os.WriteFile(envFile, []byte(strings.Join(lines, "\n")), 0o600)
	if err != nil {
		return fmt.Errorf("ошибка записи файла: %w", err)
	}

	rgc.RGNextOrder = latestOrder

	return nil
}

// TelegramConfig — уведомления через Telegram-бота.
// Токен и chat_id групп берутся из TG_* переменных; если токен или
// chat_id склада не заданы или невалидны, уведомления отключены
// (Notifier молча пропускает отправку), приложение не падает.
type TelegramConfig struct {
	BotToken        string
	WarehouseChatID int64
	EveryoneChatID  int64
}

func loadTelegramConfig() *TelegramConfig {
	return &TelegramConfig{
		BotToken:        os.Getenv("TG_BOT_TOKEN"),
		WarehouseChatID: parseEnvInt64("TG_WAREHOUSE_CHAT_ID"),
		EveryoneChatID:  parseEnvInt64("TG_EVERYONE_CHAT_ID"),
	}
}

// parseEnvInt64 читает целочисленную переменную окружения;
// 0, если переменная пуста или не число.
func parseEnvInt64(name string) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}

	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}

	return v
}

type PGConfig struct {
	PGHost     string
	PGPort     string
	PGUser     string
	PGPassword string
	PGDatabase string
}

func loadPGConfig() *PGConfig {
	pgHost := os.Getenv("PG_HOST")
	if pgHost == "" {
		os.Exit(1)
	}

	pgPort := os.Getenv("PG_PORT")
	if pgPort == "" {
		os.Exit(1)
	}

	pgUser := os.Getenv("PG_USER")
	if pgUser == "" {
		os.Exit(1)
	}

	pgPassword := os.Getenv("PG_PASSWORD")
	if pgPassword == "" {
		os.Exit(1)
	}

	pgDatabase := os.Getenv("PG_DATABASE")
	if pgDatabase == "" {
		os.Exit(1)
	}

	return &PGConfig{
		PGHost:     pgHost,
		PGPort:     pgPort,
		PGUser:     pgUser,
		PGPassword: pgPassword,
		PGDatabase: pgDatabase,
	}
}
