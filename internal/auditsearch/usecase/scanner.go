// Package usecase — модуль «Поиск удалений»: поиск в аудите МойСклада
// событий удаления позиции из заказа, сделанных РУКАМИ (не JSON API).
//
// Состояние скана живёт ТОЛЬКО в памяти процесса: задача, прогресс и
// найденные строки исчезают при рестарте. Одновременно на процесс — одна
// активная задача (вторая получает ErrJobActive).
package usecase

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"warehouseHelper/internal/msclient/client"
)

// Параметры скана.
const (
	// PageSize — размер страницы листинга аудита (контексты заказов).
	PageSize = 100
	// MaxWindowDays — предел окна поиска в календарных днях: дольше — это
	// уже не разбор правок, а выгрузка всей истории (листинг идёт по 100).
	MaxWindowDays = 92
	// ETAFromDone — до скольких обработанных контекстов ETA не считаем:
	// на первых запросах средняя скорость ещё шум, оценка врёт в разы.
	ETAFromDone = 5
	// ResultsBudget — предел найденных строк в памяти задачи (защита от
	// раздувания процесса на широком окне). Дальше находки не копятся,
	// progress.truncated сообщает об этом.
	ResultsBudget = 5000
)

// Состояния задачи (значения уходят в JSON как есть).
const (
	StateRunning = "running"
	StateDone    = "done"
	StateStopped = "stopped"
	StateError   = "error"
)

// mskLoc — TZ учётки МойСклада: аудит приходит и фильтруется в МСК
// (та же зона, что client.auditLoc; МСК без перехода на летнее время).
var mskLoc = time.FixedZone("MSK", 3*60*60)

// Источники правок, которые ручными НЕ считаются: JSON API и remap-адаптер.
// Source == nil (или пустая строка) — веб/приложение, то есть руками.
const (
	sourceJSONAPI = "jsonapi"
	sourceRemap   = "remap-1.2"
)

var (
	// ErrJobNotFound — задачи с таким id в памяти нет (рестарт/опечатка).
	ErrJobNotFound = errors.New("задача не найдена")
	// ErrJobActive — уже идёт поиск: одновременная одна задача на процесс.
	ErrJobActive = errors.New("уже идёт поиск удалений")
)

// ValidationError — отказ по входным данным (текст показывается оператору).
type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string { return e.Reason }

// AuditAPI — шов аудита МойСклада: листинг окна, раскрытие контекста и
// резолв товара в канонический id remap.
type AuditAPI interface {
	FetchAuditPageWindow(ctx context.Context, from, to time.Time, offset, limit int) ([]client.AuditRow, int, error)
	FetchAuditDetail(ctx context.Context, auditID string) ([]client.AuditEventRow, error)
	GetProduct(ctx context.Context, id string) (canonicalID string, name string, err error)
}

// Result — найденное удаление позиции из заказа.
type Result struct {
	Moment      time.Time // момент события (UTC, из аудита)
	OrderName   string    // номер заказа (audit events: name)
	OrderID     string    // id заказа (последний сегмент entity.meta.href)
	ProductName string    // имя товара из диффа позиции
	Quantity    float64   // количество удалённой позиции (кг/шт)
	UOM         string    // «кг»/«шт» (может быть пусто)
	UID         string    // автор правки (uid учётки)
	AuditID     string    // id контекста аудита (audit/<id>)
}

// UnknownUOM — подпись единицы, когда в диффе uom нет (старые события).
const UnknownUOM = "—"

// MomentText — момент события в МСК для страницы и выгрузки; пусто для
// нулевого (момент не разобрался — строка всё равно показывается).
func (r Result) MomentText() string {
	if r.Moment.IsZero() {
		return ""
	}

	return r.Moment.In(mskLoc).Format("02.01.2006 15:04:05")
}

// Progress — снимок задачи для страницы (JSON прогресс-бара).
type Progress struct {
	Job        string
	Total      int
	Done       int
	Found      int
	Percent    int
	ElapsedSec int
	EtaSec     int
	State      string
	Error      string
	Truncated  bool
	Product    string
	Results    []Result
}

// job — состояние одной задачи скана. Все поля под mu юзкейса.
type job struct {
	id          string
	productID   string
	productName string
	canonicalID string
	from, to    time.Time
	total       int
	done        int
	found       int
	truncated   bool
	results     []Result
	state       string
	errMsg      string
	startedAt   time.Time
	finishedAt  time.Time
	cancel      context.CancelFunc
}

// UseCase — менеджер задач поиска удалений (одна активная, история — в
// памяти, с ограничением на число задач).
type UseCase struct {
	api AuditAPI
	now func() time.Time

	mu       sync.Mutex
	jobs     map[string]*job
	lastID   string // последняя созданная задача (проверка «уже идёт»)
	maxJobs  int
	maxRows  int
	pageSize int
	temp     string // директория выгрузок (пусто — tempdir.Dir)
}

// Option — настройка юзкейса (тесты подменяют часы, размеры и temp).
type Option func(*UseCase)

// WithClock — свои часы: ETA и elapsed считаются по ним (тесты).
func WithClock(now func() time.Time) Option {
	return func(uc *UseCase) { uc.now = now }
}

// WithTempDir — директория xlsx-выгрузок (тесты пишут в t.TempDir()).
func WithTempDir(dir string) Option {
	return func(uc *UseCase) { uc.temp = dir }
}

// New — менеджер задач поверх API аудита МойСклада.
func New(api AuditAPI, opts ...Option) *UseCase {
	uc := &UseCase{
		api:      api,
		now:      time.Now,
		jobs:     make(map[string]*job),
		maxJobs:  10,
		maxRows:  ResultsBudget,
		pageSize: PageSize,
	}
	for _, opt := range opts {
		opt(uc)
	}

	return uc
}

// Start — создать задачу и запустить скан в горутине. Возвращает id задачи.
// from/to — календарные дни окна (время суток игнорируется): скан берёт
// сутки «с» с 00:00:00.000 до суток «по» 23:59:59.999 по МСК.
func (uc *UseCase) Start(ctx context.Context, productID string, from, to time.Time) (string, error) {
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return "", &ValidationError{Reason: "товар не выбран"}
	}

	fromAt, toAt, err := normalizeWindow(from, to)
	if err != nil {
		return "", err
	}

	uc.mu.Lock()
	defer uc.mu.Unlock()

	if j := uc.jobs[uc.lastID]; j != nil && j.state == StateRunning {
		return "", ErrJobActive
	}

	id, err := newJobID()
	if err != nil {
		return "", fmt.Errorf("id задачи: %w", err)
	}

	// Контекст задачи не наследует отмену HTTP-запроса (скан живёт дольше
	// ответа), но сохраняет его значения: stop отменяет только задачу.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	j := &job{
		id:        id,
		productID: productID,
		from:      fromAt,
		to:        toAt,
		state:     StateRunning,
		startedAt: uc.now(),
		cancel:    cancel,
	}
	uc.jobs[id] = j
	uc.lastID = id
	uc.pruneLocked()

	go uc.run(jobCtx, j)

	return id, nil
}

// Stop — остановить задачу: флаг отмены, скан прекращается на ближайшей
// итерации, найденное остаётся. Повторный stop и stop завершённой — no-op.
func (uc *UseCase) Stop(jobID string) error {
	uc.mu.Lock()
	j := uc.jobs[jobID]
	if j == nil {
		uc.mu.Unlock()

		return ErrJobNotFound
	}

	if j.state != StateRunning {
		uc.mu.Unlock()

		return nil
	}

	// Состояние меняем здесь, а не в горутине: progress сразу после stop
	// показывает stopped, а поздний finish не перепишет его на error.
	j.state = StateStopped
	j.finishedAt = uc.now()
	cancel := j.cancel
	uc.mu.Unlock()

	cancel()

	return nil
}

// Progress — снимок задачи: прогресс, ETA и найденные строки (копия).
func (uc *UseCase) Progress(jobID string) (Progress, error) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	j := uc.jobs[jobID]
	if j == nil {
		return Progress{}, ErrJobNotFound
	}

	return uc.snapshotLocked(j), nil
}

// run — горутина задачи: скан и фиксация итогового состояния. Горутина
// завершается вместе со сканом (ошибка/конец окна/отмена), утечек нет.
func (uc *UseCase) run(ctx context.Context, j *job) {
	err := uc.scan(ctx, j)

	uc.mu.Lock()
	defer uc.mu.Unlock()

	if j.state != StateRunning {
		return // задачу остановили: stopped уже выставлен
	}

	j.finishedAt = uc.now()
	switch {
	case err == nil:
		j.state = StateDone
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		j.state = StateStopped
	default:
		j.state = StateError
		j.errMsg = err.Error()
	}
}

// scan — обход окна: резолв товара, листинг страницами по PageSize и
// раскрытие только ручных контекстов. Контексты из JSON API в done
// считаются (прогресс сходится с total), но не раскрываются.
func (uc *UseCase) scan(ctx context.Context, j *job) error {
	canonicalID, name, err := uc.api.GetProduct(ctx, j.productID)
	if err != nil {
		return fmt.Errorf("товар: %w", err)
	}

	uc.mu.Lock()
	j.canonicalID = canonicalID
	j.productName = name
	uc.mu.Unlock()

	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return err
		}

		rows, size, err := uc.api.FetchAuditPageWindow(ctx, j.from, j.to, offset, uc.pageSize)
		if err != nil {
			return fmt.Errorf("лист аудита (offset %d): %w", offset, err)
		}

		uc.mu.Lock()
		j.total = size
		uc.mu.Unlock()

		if len(rows) == 0 {
			return nil
		}

		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}

			if isManualSource(row.Source) {
				if err := uc.scanContext(ctx, j, row, canonicalID, name); err != nil {
					return err
				}
			}

			uc.mu.Lock()
			j.done++
			uc.mu.Unlock()
		}

		offset += len(rows)
		if offset >= size {
			return nil
		}
	}
}

// scanContext — раскрытие одного контекста аудита и накопление удалений.
func (uc *UseCase) scanContext(ctx context.Context, j *job, row client.AuditRow, canonicalID, name string) error {
	events, err := uc.api.FetchAuditDetail(ctx, row.ID)
	if err != nil {
		return fmt.Errorf("раскрытие события %s: %w", row.ID, err)
	}

	found := deletions(events, canonicalID, name)

	uc.mu.Lock()
	defer uc.mu.Unlock()

	if j.state != StateRunning {
		return nil // остановили, пока шло раскрытие — не дописываем
	}

	for _, r := range found {
		if len(j.results) >= uc.maxRows {
			j.truncated = true

			break
		}

		r.AuditID = row.ID
		j.results = append(j.results, r)
	}
	j.found = len(j.results)

	return nil
}

// deletions — удаления позиций выбранного товара среди строк раскрытия
// контекста. Удаление: OldValue есть, NewValue nil. События из JSON API
// отсеиваются повторно (листинг уже отфильтрован, но источник бывает
// разным у строк одного контекста).
func deletions(events []client.AuditEventRow, canonicalID, name string) []Result {
	var out []Result

	for _, ev := range events {
		if !isManualSource(ev.Source) {
			continue
		}

		orderID := hrefID(ev.Entity.Meta.HREF)
		for _, change := range ev.Diff.Positions {
			if change.NewValue != nil || change.OldValue == nil {
				continue
			}

			old := change.OldValue
			if !matchesProduct(*old, canonicalID, name) {
				continue
			}

			moment, err := client.ParseAuditMoment(ev.Moment)
			if err != nil {
				moment = time.Time{}
			}

			uom := ""
			if old.Uom != nil {
				uom = *old.Uom
			}

			out = append(out, Result{
				Moment:      moment,
				OrderName:   ev.Name,
				OrderID:     orderID,
				ProductName: old.Assortment.Name,
				Quantity:    old.Quantity,
				UOM:         uom,
				UID:         ev.UID,
			})
		}
	}

	return out
}

// matchesProduct — позиция диффа про выбранный товар: по каноническому id
// remap, а при расхождении id — по имени (легаси-ссылки в старых диффах).
func matchesProduct(p client.AuditPosition, canonicalID, name string) bool {
	if canonicalID != "" && hrefID(p.Assortment.Meta.HREF) == canonicalID {
		return true
	}

	return name != "" && strings.TrimSpace(p.Assortment.Name) == strings.TrimSpace(name)
}

// isManualSource — правка сделана руками: Source пуст (веб/приложение) или
// не является JSON API/remap-адаптером.
func isManualSource(src *string) bool {
	if src == nil {
		return true
	}

	s := strings.ToLower(strings.TrimSpace(*src))

	return s != sourceJSONAPI && s != sourceRemap
}

// hrefID — последний сегмент meta.href (id сущности МойСклада).
func hrefID(href string) string {
	href = strings.TrimSuffix(strings.TrimSpace(href), "/")
	if i := strings.LastIndexByte(href, '/'); i >= 0 {
		return href[i+1:]
	}

	return href
}

// normalizeWindow — окно скана: сутки «с» 00:00:00.000 и сутки «по»
// 23:59:59.999 по МСК (аудит и фильтр живут в TZ учётки).
func normalizeWindow(from, to time.Time) (time.Time, time.Time, error) {
	if from.IsZero() || to.IsZero() {
		return time.Time{}, time.Time{}, &ValidationError{Reason: "не заданы даты окна"}
	}

	fromDay := from.In(mskLoc)
	toDay := to.In(mskLoc)

	fromAt := time.Date(fromDay.Year(), fromDay.Month(), fromDay.Day(), 0, 0, 0, 0, mskLoc)
	toAt := time.Date(toDay.Year(), toDay.Month(), toDay.Day(), 23, 59, 59, int(999*time.Millisecond), mskLoc)

	if fromAt.After(toAt) {
		return time.Time{}, time.Time{}, &ValidationError{Reason: "дата «с» позже даты «по»"}
	}

	days := int(toAt.Sub(fromAt)/(24*time.Hour)) + 1
	if days > MaxWindowDays {
		return time.Time{}, time.Time{}, &ValidationError{
			Reason: fmt.Sprintf("окно больше %d дней (%d)", MaxWindowDays, days),
		}
	}

	return fromAt, toAt, nil
}

// snapshotLocked — снимок задачи; вызывается под mu.
func (uc *UseCase) snapshotLocked(j *job) Progress {
	elapsed := uc.now().Sub(j.startedAt)
	if !j.finishedAt.IsZero() {
		elapsed = j.finishedAt.Sub(j.startedAt)
	}

	if elapsed < 0 {
		elapsed = 0
	}

	p := Progress{
		Job:        j.id,
		Total:      j.total,
		Done:       j.done,
		Found:      j.found,
		Percent:    percent(j.total, j.done, j.state),
		ElapsedSec: int(elapsed / time.Second),
		EtaSec:     etaSec(j.total, j.done, elapsed, j.state),
		State:      j.state,
		Error:      j.errMsg,
		Truncated:  j.truncated,
		Product:    j.productName,
	}

	// Страница показывает не больше 200 последних строк: весь список — в xlsx.
	const pageRows = 200
	if len(j.results) > pageRows {
		p.Results = slices.Clone(j.results[len(j.results)-pageRows:])
	} else {
		p.Results = slices.Clone(j.results)
	}

	return p
}

// percent — прогресс в процентах: total=0 до первого листинга — 0, а
// завершённая задача без событий — 100 (иначе полоса висела бы пустой).
func percent(total, done int, state string) int {
	if total <= 0 {
		if state == StateDone {
			return 100
		}

		return 0
	}

	return max(0, min(done*100/total, 100))
}

// etaSec — оценка остатка по средней скорости done/elapsed. До ETAFromDone
// обработанных контекстов не прикидываем (0): на первых итерациях средняя
// ещё шум. Завершённая/остановленная задача ETA не имеет.
func etaSec(total, done int, elapsed time.Duration, state string) int {
	if state != StateRunning || done < ETAFromDone || total <= 0 || done >= total || elapsed <= 0 {
		return 0
	}

	left := total - done
	sec := int(float64(elapsed/time.Second) * float64(left) / float64(done))
	if sec < 0 {
		return 0
	}

	return sec
}

// pruneLocked — держим в памяти не больше maxJobs задач: старые (не
// активные) выкидываем, чтобы долгие сессии не копили результаты.
func (uc *UseCase) pruneLocked() {
	for len(uc.jobs) > uc.maxJobs {
		oldestID := ""
		var oldest time.Time

		for id, j := range uc.jobs {
			if j.state == StateRunning {
				continue
			}

			if oldestID == "" || j.startedAt.Before(oldest) {
				oldestID, oldest = id, j.startedAt
			}
		}

		if oldestID == "" {
			return
		}

		delete(uc.jobs, oldestID)
	}
}

// newJobID — uuid v4 из crypto/rand (своей зависимости uuid в проекте нет).
func newJobID() (string, error) {
	var b [16]byte

	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	b[6] = (b[6] & 0x0f) | 0x40 // версия 4
	b[8] = (b[8] & 0x3f) | 0x80 // вариант RFC 4122

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
