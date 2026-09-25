package config

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
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
	*ComplaintsConfig
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

// LoadEnv загружает .env в окружение процесса. Вызывается первой в main:
// logging.Setup читает APP_LOG_FILE/LOG_LEVEL из окружения, а godotenv
// кладёт переменные файла в os.Getenv — значит .env должен быть загружен
// до настройки логгера. Panic — файл не найден или не читается.
func LoadEnv() {
	envPath := envFilePath()
	if envPath == "" {
		panic("Cannot read config file")
	}
	if err := godotenv.Load(envPath); err != nil {
		panic("Cannot read config file: " + envPath)
	}
}

func NewConfig() *Config {
	LoadEnv()

	apc := loadAppconfig()
	msc := loadMSConfig()
	rfc := loadRefGoConfig()
	pfc := loadPGConfig()
	tgc := loadTelegramConfig()
	qrc := loadQRConfig()
	cpc := loadComplaintsConfig()

	if os.Getenv("RG_LATESTORDER") == "" {
		panic("RG_LATESTORDER does not exist")
	}

	return &Config{
		AppConfig:        apc,
		MSConfig:         msc,
		RefGoConfig:      rfc,
		PGConfig:         pfc,
		TelegramConfig:   tgc,
		QRConfig:         qrc,
		ComplaintsConfig: cpc,
	}
}

type AppConfig struct {
	HTTPAddress       string
	TempCleanupMaxAge time.Duration
	// WeightsHistoryLimit — сколько последних весов единицы хранить на товар
	// (FIFO, модуль среднего веса); настройка приложения, не в схеме.
	WeightsHistoryLimit int
	// DayStateSnapshotTime — время утреннего снапшота состояний по дням
	// (модуль daystate), от полуночи, локальное время процесса.
	DayStateSnapshotTime time.Duration
	// DiscountWindowCap — ёмкость окна сайта (модуль скидок): сколько позиций
	// показываем менеджеру в окне распродажи.
	DiscountWindowCap int
	// DiscountTelegramCap — ёмкость слота ТГ (модуль скидок): сколько позиций
	// уходит в рассылку чата склада.
	DiscountTelegramCap int
	// DiscountTGPlanTime — время сборки плана ТГ-слота (14:00 по умолчанию),
	// от полуночи, локальное время процесса.
	DiscountTGPlanTime time.Duration
	// DiscountTGRaiseTime — время подъёма general до telegram (16:00 по
	// умолчанию), от полуночи, локальное время процесса.
	DiscountTGRaiseTime time.Duration
	// PickingRetention — срок хранения журнала подбора заказов (модуль msorders):
	// строки старше удаляются фоновой задачей. Номер заказа в МС начинается
	// заново каждый год, поэтому старые записи обязаны уходить — иначе поиск по
	// номеру мог бы встретить прошлогоднего тёзку.
	PickingRetention time.Duration
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

// ComplaintsConfig — модуль «Жалобы»: zip-архивы фото обращений и публичный
// адрес для ссылок в уведомлениях. PhotosDir — папка архивов <id>.zip (по
// умолчанию ../ComplaintsPhotos — от каталога cmd/, как tempdir "../temp");
// PublicURL — база ссылок на приложение (кнопки «Подобрать» и
// «Расформировать», ссылки на обращения), её собирает publicURL.
type ComplaintsConfig struct {
	PhotosDir string
	PublicURL string
}

func loadComplaintsConfig() *ComplaintsConfig {
	photosDir := os.Getenv("COMPLAINTS_PHOTOS_DIR")
	if photosDir == "" {
		photosDir = "../ComplaintsPhotos"
	}

	return &ComplaintsConfig{
		PhotosDir: photosDir,
		PublicURL: publicURL(),
	}
}

// defaultPublicURL — адрес ссылок, когда настроек нет вовсе. mDNS-имя
// warehouse.local резолвится только в локальной сети и только там, где работает
// mDNS: у клиентов с VPN в режиме TUN запрос уходит в чужой DNS и имя не
// находится, поэтому адрес настраиваемый. Оставлен последним рубежом — ссылка
// без хоста хуже нерабочей.
const defaultPublicURL = "http://warehouse.local:8080"

// publicURL — база адресов в уведомлениях: ссылки на обращения, кнопки
// «Подобрать» (reservewatch) и «Расформировать» (returns).
//
// Источники по убыванию старшинства:
//  1. COMPLAINTS_PUBLIC_URL — адрес, заданный целиком: он старше всего, чтобы
//     прежняя настройка продолжала работать.
//  2. APP_PUBLIC_HOST + порт из APP_HTTPADDRESS — обычный случай: хост задаёт
//     владелец, порт не дублируется второй настройкой.
//  3. Локальный IPv4 приложения (APP_PUBLIC_HOST не задан) — работает без
//     правки .env, но зависит от того, какой адрес машина отдаст первой.
//  4. defaultPublicURL.
//
// Порт обязателен: без него адрес на шаге 2/3 не собирается (ссылка на порт по
// умолчанию вела бы мимо приложения) и источник пропускается.
func publicURL() string {
	if url := strings.TrimSpace(os.Getenv("COMPLAINTS_PUBLIC_URL")); url != "" {
		return url
	}

	port := httpPort()
	if port == "" {
		slog.Info("адрес ссылок: порт не задан в APP_HTTPADDRESS", "url", defaultPublicURL)

		return defaultPublicURL
	}

	host := cmp.Or(strings.TrimSpace(os.Getenv("APP_PUBLIC_HOST")), localIPv4())
	if host == "" {
		slog.Info("адрес ссылок: APP_PUBLIC_HOST пуст и локальный IPv4 не найден", "url", defaultPublicURL)

		return defaultPublicURL
	}

	// Хост с портом («192.168.1.71:9000») принимаем как готовую пару: склейка
	// через JoinHostPort дала бы адрес с двумя портами.
	url := "http://" + host
	if _, _, err := net.SplitHostPort(host); err != nil {
		url = "http://" + net.JoinHostPort(host, port)
	}

	slog.Info("адрес ссылок в уведомлениях", "url", url)

	return url
}

// httpPort — порт из APP_HTTPADDRESS (":8080", "0.0.0.0:8080",
// "192.168.1.71:8080"). Пустая строка — адрес пуст, без порта, порт не число
// или вне 1..65535.
func httpPort() string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(os.Getenv("APP_HTTPADDRESS")))
	if err != nil {
		return ""
	}

	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return ""
	}

	return port
}

// localIPv4 — IPv4-адрес машины для ссылок: частный адрес (ссылки открывают из
// локальной сети) предпочтительнее прочего, loopback и link-local (169.254.*)
// пропускаются. Пустая строка — подходящих адресов нет.
func localIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}

	candidates := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		addr, ok := ipv4Of(a)
		if !ok {
			continue
		}
		candidates = append(candidates, addr)
	}

	addr, ok := firstUsableIPv4(candidates)
	if !ok {
		return ""
	}

	return addr.String()
}

// ipv4Of — IPv4-адрес интерфейса из net.Addr: IPv6 и не-IP значения
// отбрасываются.
func ipv4Of(a net.Addr) (netip.Addr, bool) {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	default:
		return netip.Addr{}, false
	}

	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}

	addr = addr.Unmap()
	if !addr.Is4() {
		return netip.Addr{}, false
	}

	return addr, true
}

// firstUsableIPv4 — первый пригодный для ссылки адрес: частный, если он есть в
// списке, иначе первый не-loopback и не link-local. Отдельная функция (а не
// цикл в localIPv4), чтобы выбор адреса проверялся тестом без реальных
// интерфейсов машины.
func firstUsableIPv4(addrs []netip.Addr) (netip.Addr, bool) {
	for _, addr := range addrs {
		if addr.Is4() && addr.IsPrivate() {
			return addr, true
		}
	}

	for _, addr := range addrs {
		if addr.Is4() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() {
			return addr, true
		}
	}

	return netip.Addr{}, false
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

	// Время утреннего снапшота daystate: APP_DAYSTATE_SNAPSHOT_TIME в формате
	// "HH:MM" (локальное время процесса); по умолчанию 09:00; невалидное
	// значение — дефолт.
	dayStateSnapshotTime := 9 * time.Hour
	if tStr := os.Getenv("APP_DAYSTATE_SNAPSHOT_TIME"); tStr != "" {
		if t, err := time.Parse("15:04", tStr); err == nil {
			dayStateSnapshotTime = time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
		}
	}

	// Модуль скидок: ёмкости окна сайта и слота ТГ (APP_DISCOUNT_WINDOW_CAP=12,
	// APP_DISCOUNT_TELEGRAM_CAP=10) — неположительное значение сбрасывается на
	// дефолт, как у остальных счётчиков приложения.
	discountWindowCap := 12
	if nStr := os.Getenv("APP_DISCOUNT_WINDOW_CAP"); nStr != "" {
		if n, err := strconv.Atoi(nStr); err == nil && n > 0 {
			discountWindowCap = n
		}
	}
	discountTelegramCap := 10
	if nStr := os.Getenv("APP_DISCOUNT_TELEGRAM_CAP"); nStr != "" {
		if n, err := strconv.Atoi(nStr); err == nil && n > 0 {
			discountTelegramCap = n
		}
	}

	// Время шагов ТГ-дня (модуль скидок): APP_DISCOUNT_TG_PLAN_TIME=14:00 —
	// план слота, APP_DISCOUNT_TG_RAISE_TIME=16:00 — подъём general до telegram.
	// Формат "HH:MM", локальное время процесса; невалидное значение — дефолт.
	discountTGPlanTime := 14 * time.Hour
	if tStr := os.Getenv("APP_DISCOUNT_TG_PLAN_TIME"); tStr != "" {
		if t, err := time.Parse("15:04", tStr); err == nil {
			discountTGPlanTime = time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
		}
	}
	discountTGRaiseTime := 16 * time.Hour
	if tStr := os.Getenv("APP_DISCOUNT_TG_RAISE_TIME"); tStr != "" {
		if t, err := time.Parse("15:04", tStr); err == nil {
			discountTGRaiseTime = time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
		}
	}

	pickingRetention := loadPickingRetention()

	return &AppConfig{
		HTTPAddress:          httpAddress,
		TempCleanupMaxAge:    tempCleanupMaxAge,
		WeightsHistoryLimit:  weightsHistoryLimit,
		DayStateSnapshotTime: dayStateSnapshotTime,
		DiscountWindowCap:    discountWindowCap,
		DiscountTelegramCap:  discountTelegramCap,
		DiscountTGPlanTime:   discountTGPlanTime,
		DiscountTGRaiseTime:  discountTGRaiseTime,
		PickingRetention:     pickingRetention,
	}
}

// loadPickingRetention — срок хранения журнала подбора заказов (модуль
// msorders): PICKJOURNAL_RETENTION_DAYS в днях, по умолчанию 180 дней.
// Неположительное или неразбираемое значение — дефолт (как у остальных
// счётчиков приложения).
func loadPickingRetention() time.Duration {
	const defaultRetention = 180 * 24 * time.Hour

	if daysStr := os.Getenv("PICKJOURNAL_RETENTION_DAYS"); daysStr != "" {
		if days, err := strconv.Atoi(daysStr); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}

	return defaultRetention
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
	// CancelledStateID — id статуса заказа «Отменён» (audit-наблюдатель,
	// модуль returns). Пусто — перевод в «Отменён» не детектится.
	CancelledStateID string
	// SkipAuditSources — source-источники событий audit, которые поллер
	// пропускает (наши API-изменения). По умолчанию remap-1.2.
	SkipAuditSources []string
	// ReserveWatchStates — id статусов заказов, которые раз в минуту
	// проверяет модуль reservewatch (резерв позиций == quantity).
	// Пусто — модуль не запускается.
	ReserveWatchStates []string
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

	cancelledStateID := strings.Trim(os.Getenv("MSAPI_CANCELLED_STATE_ID"), `"`)
	// Пусто — допустимо: модуль returns не детектит отмены (warn при старте).
	// Кавычки снимаем: значение задаётся без них (как соседние id); кавычки
	// в переменной окружения стали бы частью строки и сломали бы матч статуса.

	skipAuditSources := make([]string, 0, 1)
	for v := range strings.SplitSeq(os.Getenv("MSAPI_SKIP_AUDIT_SOURCES"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			skipAuditSources = append(skipAuditSources, v)
		}
	}
	if len(skipAuditSources) == 0 {
		// Безопасный дефолт: события, порождённые нашими API-правками
		// (source=remap-1.2), не должны триггерить уведомления складу.
		skipAuditSources = append(skipAuditSources, "remap-1.2")
	}

	// Id статусов заказов модуля reservewatch («Вес подобран», «Обработан»,
	// «РефГо», «Курьер», «Перепроверен» — значения из metadata/states).
	// Пусто — модуль не запускается (аналог CancelledStateID).
	reserveWatchStates := reserveWatchStatesEnv()

	return &MSConfig{
		Refs: msrefs,

		WarehouseAPIKEYS:   wrhworkers,
		OthersAPIKEYS:      othrworkers,
		TimeSpan:           tspn,
		RequestCap:         rqcap,
		SellTypeID:         selltypeID,
		RefGoNumberID:      refgonumberid,
		CourierID:          courierid,
		TimeFormat:         timeFormat,
		URLstart:           urlstart,
		AuthHeader:         authheader,
		EncodeHeader:       encodeheader,
		CancelledStateID:   cancelledStateID,
		SkipAuditSources:   skipAuditSources,
		ReserveWatchStates: reserveWatchStates,
	}
}

// reserveWatchStatesEnv — id статусов заказов модуля reservewatch из
// MSAPI_RESERVEWATCH_STATES (CSV: значения из customerorder/metadata/states).
// Кавычки снимаем (как у соседних id); пусто — модуль не запускается.
func reserveWatchStatesEnv() []string {
	states := make([]string, 0, 5)
	for v := range strings.SplitSeq(os.Getenv("MSAPI_RESERVEWATCH_STATES"), ",") {
		if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
			states = append(states, v)
		}
	}
	return states
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
	ReadystateID  string
	ShipedstateID string
	// WeightPickedStateID — статус заказа «Вес подобран»: ставится при акте
	// подбора (модуль msorders). Необязательный: пусто — статус не ставится,
	// старт приложения не падает (прод .env правит владелец руками).
	WeightPickedStateID string
	SellTypeOtherID     string
	SellTypeOtherType   string
	OrgID               string
	RefGoCourierID      string
	PrinttemplateID     string
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

	// Статус «Вес подобран» (модуль подбора) — необязательный: прод .env
	// правит владелец руками, поэтому пустое значение деградирует мягко
	// (подбор не ставит статус), а не роняет старт, как обязательные refs.
	weightPickedStateID := strings.Trim(os.Getenv("MSAPI_WEIGHT_PICKED_STATE_ID"), `"`)

	return &MSRefs{
		ReadystateID:        readystateID,
		ShipedstateID:       shipedstateID,
		WeightPickedStateID: weightPickedStateID,
		SellTypeOtherID:     sellTypeOtherID,
		SellTypeOtherType:   sellTypeOtherType,
		OrgID:               orgID,
		RefGoCourierID:      refGoCourierID,
		PrinttemplateID:     printtemplateID,
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
	CommonChatID    int64
}

func loadTelegramConfig() *TelegramConfig {
	return &TelegramConfig{
		BotToken:        os.Getenv("TG_BOT_TOKEN"),
		WarehouseChatID: parseEnvInt64("TG_WAREHOUSE_CHAT_ID"),
		EveryoneChatID:  parseEnvInt64("TG_EVERYONE_CHAT_ID"),
		CommonChatID:    parseEnvInt64("TG_COMMON_CHAT_ID"),
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
