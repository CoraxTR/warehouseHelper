// Package usecase — сценарии модуля «Жалобы»: создание и редактирование
// обращений, списки и поиск, фото (zip-архивы на диске), телеграм-теги
// ролей, уведомления в common_chat и тикер напоминаний по дедлайну.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/complaints/photostore"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

const trackPkg = "complaints"

// Ошибки валидации (слой HTTP переводит их в сообщения формы).
var (
	ErrComplaintBadPhone      = errors.New("телефон указан в неверном формате")
	ErrComplaintDeadlinePast  = errors.New("дедлайн должен быть в будущем")
	ErrComplaintNoItems       = errors.New("выберите хотя бы один товар")
	ErrComplaintNoOrderNumber = errors.New("укажите номер заказа в МойСклад")
	ErrComplaintNoDeadline    = errors.New("укажите дедлайн")
	ErrComplaintBadTag        = errors.New("тег не указан")
	ErrComplaintBadRole       = errors.New("роль не предусмотрена")
)

// ComplaintInput — данные обращения из формы (создание и редактирование).
type ComplaintInput struct {
	MSOrderNumber string
	Phone         string
	Description   string
	Actions       string
	Status        domain.ComplaintStatus
	Deadline      time.Time
	// DeadlineAuto — менеджер дедлайн не задавал (значение поля формы не
	// менялось с момента открытия). Модуль сам ставит +24 часа от момента
	// сохранения при создании и при смене статуса; без смены статуса
	// дедлайн остаётся прежним.
	DeadlineAuto bool
	Items        []domain.ComplaintItem // product_id + product_name из формы
}

// ComplaintRepository — хранилище обращений (реализация: postgres.PGClient).
type ComplaintRepository interface {
	CreateComplaint(ctx context.Context, c *domain.Complaint) (int64, error)
	UpdateComplaint(ctx context.Context, c *domain.Complaint) error
	DeleteComplaint(ctx context.Context, id int64) error
	GetComplaint(ctx context.Context, id int64) (*domain.Complaint, error)
	ListActiveComplaintSummaries(ctx context.Context) ([]domain.ComplaintSummary, error)
	ListAllComplaintSummaries(ctx context.Context) ([]domain.ComplaintSummary, error)
	SearchComplaints(ctx context.Context, f domain.ComplaintFilter) ([]domain.ComplaintSummary, error)
	DueComplaints(ctx context.Context, now time.Time) ([]domain.ComplaintDue, error)
	ShiftComplaintDeadline(ctx context.Context, id int64) error
	ListComplaintRoleTags(ctx context.Context) ([]domain.ComplaintRoleTag, error)
	SetComplaintRoleTag(ctx context.Context, role domain.ComplaintRole, tag string) error
	DeleteComplaintRoleTag(ctx context.Context, role domain.ComplaintRole) error
}

// CatalogReader — каталог товаров: имена-снимки для строк обращения
// (реализация: postgres.PGClient).
type CatalogReader interface {
	GetProductsByIDs(ctx context.Context, ids []string) ([]domain.Product, error)
}

// PhotoStore — файловое хранилище фото жалобы (реализация: photostore.Store):
// одна жалоба — один zip-архив <complaint_id>.zip в общей папке.
type PhotoStore interface {
	List(ctx context.Context, complaintID int64) ([]photostore.Photo, error)
	Add(ctx context.Context, complaintID int64, ups []photostore.Upload) error
	Remove(ctx context.Context, complaintID int64, name string) error
	Open(ctx context.Context, complaintID int64, name string) (io.ReadCloser, error)
}

// ComplaintNotifier — уведомления в Telegram (реализация: telegram.Notifier).
type ComplaintNotifier interface {
	// NotifyCommonStatus шлёт в common_chat уведомление о статусе:
	// HTML-текст со ссылкой на обращение + inline-кнопка «Получить
	// подробности» (callbackData — данные кнопки).
	NotifyCommonStatus(ctx context.Context, textHTML string, callbackData string) error
	// SendDetails шлёт обычное текстовое сообщение в указанный чат
	// (ответ на нажатие кнопки).
	SendDetails(ctx context.Context, chatID int64, text string) error
	// SendPhotos шлёт фотографии в указанный чат (media group, до 10
	// в сообщении; крупные файлы — документами).
	SendPhotos(ctx context.Context, chatID int64, photos []domain.ComplaintTGPhoto) error
	// AnswerCallback закрывает «часики» на кнопке (callback query).
	AnswerCallback(ctx context.Context, callbackQueryID string) error
}

// UseCase — сценарии модуля «Жалобы».
type UseCase struct {
	repo    ComplaintRepository
	catalog CatalogReader
	files   PhotoStore
	notify  ComplaintNotifier

	baseURL string // публичный адрес приложения для ссылок в уведомлениях (COMPLAINTS_PUBLIC_URL)

	now func() time.Time
}

// NewUseCase собирает сценарии модуля «Жалобы».
func NewUseCase(
	repo ComplaintRepository,
	catalog CatalogReader,
	files PhotoStore,
	notify ComplaintNotifier,
	baseURL string,
) *UseCase {
	return &UseCase{
		repo:    repo,
		catalog: catalog,
		files:   files,
		notify:  notify,
		baseURL: strings.TrimRight(baseURL, "/"),
		now:     time.Now,
	}
}

// RoleTags возвращает зарегистрированные телеграм-теги ролей.
func (uc *UseCase) RoleTags(ctx context.Context) ([]domain.ComplaintRoleTag, error) {
	done := metrics.Track(trackPkg, "RoleTags")
	defer done()

	return uc.repo.ListComplaintRoleTags(ctx)
}

// SetRoleTag регистрирует тег роли (создаёт или обновляет).
func (uc *UseCase) SetRoleTag(ctx context.Context, role domain.ComplaintRole, tag string) error {
	done := metrics.Track(trackPkg, "SetRoleTag")
	defer done()

	if !role.Valid() {
		return ErrComplaintBadRole
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ErrComplaintBadTag
	}
	return uc.repo.SetComplaintRoleTag(ctx, role, tag)
}

// DeleteRoleTag удаляет тег роли (идемпотентно).
func (uc *UseCase) DeleteRoleTag(ctx context.Context, role domain.ComplaintRole) error {
	done := metrics.Track(trackPkg, "DeleteRoleTag")
	defer done()

	if !role.Valid() {
		return ErrComplaintBadRole
	}
	return uc.repo.DeleteComplaintRoleTag(ctx, role)
}

// roleTag — тег роли из репозитория (пустая строка — тег не зарегистрирован).
func (uc *UseCase) roleTag(ctx context.Context, role domain.ComplaintRole) string {
	tags, err := uc.repo.ListComplaintRoleTags(ctx)
	if err != nil {
		slog.Info(fmt.Sprintf("complaints: чтение тегов ролей: %v", err))
		return ""
	}
	for _, t := range tags {
		if t.Role == role {
			return t.Tag
		}
	}
	return ""
}

// Create создаёт обращение с товарами. Дедлайн, который менеджер не задавал
// вручную (DeadlineAuto), — ровно +24 часа от момента сохранения. При
// создании сразу со статусом, отличным от «Создано», в common_chat уходит
// мгновенное уведомление (текст и тег по статусу). Фото сохраняются
// архивом <id>.zip; при ошибке сохранения фото созданное обращение удаляется.
func (uc *UseCase) Create(ctx context.Context, in ComplaintInput, uploads []photostore.Upload) (int64, error) {
	done := metrics.Track(trackPkg, "Create")
	defer done()

	if in.DeadlineAuto {
		in.Deadline = uc.now().Add(24 * time.Hour)
	}

	c, err := uc.buildComplaint(ctx, in)
	if err != nil {
		return 0, err
	}
	if err := uc.checkDeadline(c); err != nil {
		return 0, err
	}

	id, err := uc.repo.CreateComplaint(ctx, c)
	if err != nil {
		return 0, fmt.Errorf("create complaint: %w", err)
	}

	if len(uploads) > 0 {
		if err := uc.files.Add(ctx, id, uploads); err != nil {
			// Откат: обращение без фото, которые менеджер уже загрузил,
			// не нужно — удаляем целиком, менеджер повторит создание.
			if delErr := uc.repo.DeleteComplaint(ctx, id); delErr != nil {
				slog.Info(fmt.Sprintf("complaints: откат обращения %d после ошибки фото: %v", id, delErr))
			}
			return 0, err
		}
	}

	c.ID = id
	if c.Status != domain.ComplaintStatusCreated {
		// Создание сразу в рабочем статусе — уведомляем один раз
		// (напоминания дальше — по дедлайну). «Создано» молчит.
		uc.notifyStatus(ctx, c)
	}
	return id, nil
}

// Update сохраняет изменения обращения. Уведомление в common_chat шлётся
// при каждой смене статуса — один раз (текст и тег по новому статусу);
// повторное сохранение того же статуса молчит. Дедлайн, который менеджер
// не задавал вручную (DeadlineAuto): без смены статуса остаётся прежним
// (в т.ч. просроченный — тикер по нему и так напоминает); при смене
// статуса — ровно +24 часа от момента сохранения; при переходе в
// «Завершено» не трогается (тикер завершённые не обслуживает).
func (uc *UseCase) Update(ctx context.Context, id int64, in ComplaintInput) error {
	done := metrics.Track(trackPkg, "Update")
	defer done()

	old, err := uc.repo.GetComplaint(ctx, id)
	if err != nil {
		return err
	}

	if in.DeadlineAuto {
		switch in.Status {
		case old.Status, domain.ComplaintStatusCompleted:
			in.Deadline = old.Deadline
		default:
			in.Deadline = uc.now().Add(24 * time.Hour)
		}
	}

	c, err := uc.buildComplaint(ctx, in)
	if err != nil {
		return err
	}
	c.ID = id

	// Валидируем только фактическое изменение дедлайна: «оставленный как
	// есть» дедлайн (в т.ч. просроченный у незавершённого обращения) —
	// состояние, которое уже обслуживает тикер, а не новая ошибка ввода.
	if !c.Deadline.Equal(old.Deadline) {
		if err := uc.checkDeadline(c); err != nil {
			return err
		}
	}

	if err := uc.repo.UpdateComplaint(ctx, c); err != nil {
		return fmt.Errorf("update complaint %d: %w", id, err)
	}

	if old.Status != c.Status {
		uc.notifyStatus(ctx, c)
	}
	return nil
}

// buildComplaint собирает доменную модель из данных формы с валидацией:
// нормализация телефона, обязательные поля, товары — минимум один,
// имена-снимки из каталога. Проверка дедлайна — в Create/Update
// (checkDeadline): правило зависит от смены статуса и авто-значения.
func (uc *UseCase) buildComplaint(ctx context.Context, in ComplaintInput) (*domain.Complaint, error) {
	orderNumber := strings.TrimSpace(in.MSOrderNumber)
	if orderNumber == "" {
		return nil, ErrComplaintNoOrderNumber
	}

	phone, err := domain.NormalizePhone(in.Phone)
	if err != nil {
		return nil, ErrComplaintBadPhone
	}

	if !in.Status.Valid() {
		in.Status = domain.ComplaintStatusCreated
	}

	items, err := uc.resolveItems(ctx, in.Items)
	if err != nil {
		return nil, err
	}

	return &domain.Complaint{
		MSOrderNumber: orderNumber,
		Phone:         phone,
		Description:   strings.TrimSpace(in.Description),
		Actions:       strings.TrimSpace(in.Actions),
		Status:        in.Status,
		Deadline:      in.Deadline,
		Items:         items,
	}, nil
}

// checkDeadline — дедлайн обязан быть задан; у незавершённого обращения он
// должен быть строго в будущем (иначе тикер напоминаний сработает сразу).
// «Завершено» прошлый дедлайн не мешает — тикер его не обслуживает.
func (uc *UseCase) checkDeadline(c *domain.Complaint) error {
	if c.Deadline.IsZero() {
		return ErrComplaintNoDeadline
	}
	if c.Status != domain.ComplaintStatusCompleted && !c.Deadline.After(uc.now()) {
		return ErrComplaintDeadlinePast
	}
	return nil
}

// resolveItems превращает строки товаров из формы в доменные items:
// имена-снимки берутся из каталога (по id); строки без id (товар уже
// удалён из каталога) сохраняют имя, присланное формой. Пустые строки
// отбрасываются.
func (uc *UseCase) resolveItems(ctx context.Context, raw []domain.ComplaintItem) ([]domain.ComplaintItem, error) {
	ids := make([]string, 0, len(raw))
	seen := make(map[string]bool)
	for _, it := range raw {
		if it.ProductID == "" {
			continue
		}
		if !seen[it.ProductID] {
			seen[it.ProductID] = true
			ids = append(ids, it.ProductID)
		}
	}

	names := make(map[string]string)
	if len(ids) > 0 {
		products, err := uc.catalog.GetProductsByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("resolve products: %w", err)
		}
		for _, p := range products {
			names[p.ID] = p.Name
		}
	}

	items := make([]domain.ComplaintItem, 0, len(raw))
	for _, it := range raw {
		name := strings.TrimSpace(it.ProductName)
		if it.ProductID != "" {
			// id из формы, но каталог его уже не знает (товар удалён
			// между выбором и сохранением) — оставляем строку без id,
			// имя — из формы (снимок, который показал браузер).
			if catalogName, ok := names[it.ProductID]; ok {
				name = catalogName
			} else {
				it.ProductID = ""
			}
		}
		if name == "" {
			continue
		}
		items = append(items, domain.ComplaintItem{ProductID: it.ProductID, ProductName: name})
	}

	if len(items) == 0 {
		return nil, ErrComplaintNoItems
	}
	return items, nil
}

// Get возвращает обращение с товарами. Обращения нет — domain.ErrComplaintNotFound.
func (uc *UseCase) Get(ctx context.Context, id int64) (*domain.Complaint, error) {
	done := metrics.Track(trackPkg, "Get")
	defer done()

	return uc.repo.GetComplaint(ctx, id)
}

// ListActive возвращает список «Жалобы»: статус != «Завершено», по убыванию
// даты создания.
func (uc *UseCase) ListActive(ctx context.Context) ([]domain.ComplaintSummary, error) {
	done := metrics.Track(trackPkg, "ListActive")
	defer done()

	return uc.repo.ListActiveComplaintSummaries(ctx)
}

// ListAll возвращает полный список обращений (все статусы) по убыванию
// даты создания.
func (uc *UseCase) ListAll(ctx context.Context) ([]domain.ComplaintSummary, error) {
	done := metrics.Track(trackPkg, "ListAll")
	defer done()

	return uc.repo.ListAllComplaintSummaries(ctx)
}

// Search ищет обращения по фильтру (AND по заполненным полям). Телефон
// нормализуется: поиск по любому варианту записи номера.
func (uc *UseCase) Search(ctx context.Context, f domain.ComplaintFilter) ([]domain.ComplaintSummary, error) {
	done := metrics.Track(trackPkg, "Search")
	defer done()

	if f.Phone != "" {
		norm, err := domain.NormalizePhone(f.Phone)
		if err != nil {
			return nil, ErrComplaintBadPhone
		}
		f.Phone = norm
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return nil, errors.New("начало диапазона дат позже конца")
	}

	return uc.repo.SearchComplaints(ctx, f)
}

// Photos возвращает список фото обращения (имена в архиве).
func (uc *UseCase) Photos(ctx context.Context, complaintID int64) ([]photostore.Photo, error) {
	done := metrics.Track(trackPkg, "Photos")
	defer done()

	return uc.files.List(ctx, complaintID)
}

// OpenPhoto открывает фото обращения по имени для раздачи в браузере.
func (uc *UseCase) OpenPhoto(ctx context.Context, complaintID int64, name string) (io.ReadCloser, error) {
	done := metrics.Track(trackPkg, "OpenPhoto")
	defer done()

	return uc.files.Open(ctx, complaintID, name)
}

// AddPhotos добавляет фотографии к обращению (пересборка архива).
func (uc *UseCase) AddPhotos(ctx context.Context, complaintID int64, uploads []photostore.Upload) error {
	done := metrics.Track(trackPkg, "AddPhotos")
	defer done()

	if _, err := uc.repo.GetComplaint(ctx, complaintID); err != nil {
		return err
	}
	if len(uploads) == 0 {
		return errors.New("нет фотографий для сохранения")
	}
	if err := uc.files.Add(ctx, complaintID, uploads); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("complaints: к обращению %d добавлено фото: %d", complaintID, len(uploads)))
	return nil
}

// DeletePhoto удаляет фотографию обращения (пересборка архива).
func (uc *UseCase) DeletePhoto(ctx context.Context, complaintID int64, name string) error {
	done := metrics.Track(trackPkg, "DeletePhoto")
	defer done()

	if err := uc.files.Remove(ctx, complaintID, name); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("complaints: фото %s обращения %d удалено", name, complaintID))
	return nil
}

// notifyStatus отправляет мгновенное уведомление о статусе обращения в
// common_chat (текст и тег по правилу статуса). Вызывается при создании
// сразу в рабочем статусе и при каждой смене статуса. Ошибка отправки
// логируется и не роняет операцию.
func (uc *UseCase) notifyStatus(ctx context.Context, c *domain.Complaint) {
	if err := uc.notifyForStatus(ctx, c.ID, c.Status); err != nil {
		slog.Info(fmt.Sprintf("complaints: уведомление о статусе %s обращения %d: %v", c.Status, c.ID, err))
	}
}

// notifyForStatus собирает и отправляет уведомление о статусе обращения.
// Возвращает ошибку отправки: тикеру нужен успех — дедлайн сдвигается
// только после реально ушедшего сообщения.
func (uc *UseCase) notifyForStatus(ctx context.Context, id int64, status domain.ComplaintStatus) error {
	role, hasTag := domain.TagRoleForStatus(status)
	tag := ""
	if hasTag {
		tag = uc.roleTag(ctx, role)
	}
	text := uc.notificationText(id, status, tag)
	return uc.notify.NotifyCommonStatus(ctx, text, callbackData(id))
}

// notificationText собирает текст уведомления о статусе: тег (если
// зарегистрирован) в начало, «Обращение {id}» — ссылка на просмотр,
// далее текст по статусу; в конец — приписка про локальную сеть.
func (uc *UseCase) notificationText(id int64, status domain.ComplaintStatus, tag string) string {
	var sb strings.Builder
	if tag != "" {
		sb.WriteString(htmlEscape(tag))
		sb.WriteString(" ")
	}
	link := fmt.Sprintf("%s/complaint?id=%d", uc.baseURL, id)
	sb.WriteString(fmt.Sprintf("<a href=%q>Обращение %d</a>: %s", link, id, statusMessage(status)))
	sb.WriteString("\nСсылка открывается только в локальной сети")
	return sb.String()
}

// statusMessage — текст уведомления по статусу (правила модуля).
func statusMessage(status domain.ComplaintStatus) string {
	switch status {
	case domain.ComplaintStatusReviewing:
		return "Ожидаем согласования"
	case domain.ComplaintStatusWarehouse:
		return "Ожидаем действий от склада"
	case domain.ComplaintStatusSupplier:
		return "Ожидаем ответ от поставщика"
	case domain.ComplaintStatusCompleted:
		return "Завершено"
	case domain.ComplaintStatusCreated:
		return `Статус "Создано" - поправьте пожалуйста`
	case domain.ComplaintStatusClient:
		return "Ожидаем подробностей от клиента"
	default:
		return string(status)
	}
}

// callbackData — данные inline-кнопки «Получить подробности».
func callbackData(complaintID int64) string {
	return fmt.Sprintf("complaint_details:%d", complaintID)
}

// ParseCallbackData разбирает данные кнопки: complaint_details:<id>.
// Не наш формат — ok=false.
func ParseCallbackData(data string) (complaintID int64, ok bool) {
	const prefix = "complaint_details:"
	if !strings.HasPrefix(data, prefix) {
		return 0, false
	}
	id, err := parseID(data[len(prefix):])
	if err != nil {
		return 0, false
	}
	return id, true
}

// HandleDetailsButton отвечает на нажатие «Получить подробности»: закрывает
// callback, шлёт в чат карточку обращения и фотографии (если есть).
func (uc *UseCase) HandleDetailsButton(ctx context.Context, callbackQueryID string, chatID, complaintID int64) error {
	done := metrics.Track(trackPkg, "HandleDetailsButton")
	defer done()

	if err := uc.notify.AnswerCallback(ctx, callbackQueryID); err != nil {
		slog.Info(fmt.Sprintf("complaints: answerCallbackQuery: %v", err))
	}

	c, err := uc.repo.GetComplaint(ctx, complaintID)
	if err != nil {
		msg := "Обращение не найдено"
		if sendErr := uc.notify.SendDetails(ctx, chatID, msg); sendErr != nil {
			return fmt.Errorf("details button: %w", sendErr)
		}
		return nil
	}

	names := make([]string, 0, len(c.Items))
	for _, it := range c.Items {
		names = append(names, it.ProductName)
	}
	text := fmt.Sprintf(
		"Статус: %s\nНомер заказа: %s\nТелефон получателя: %s\nОписание: %s\nТовар: %s\nПредпринятые шаги: %s",
		c.Status.StatusLabel(), c.MSOrderNumber, domain.FormatPhone(c.Phone),
		c.Description, strings.Join(names, ", "), c.Actions,
	)
	if err := uc.notify.SendDetails(ctx, chatID, text); err != nil {
		return fmt.Errorf("details button: send text: %w", err)
	}

	if err := uc.sendPhotos(ctx, chatID, complaintID); err != nil {
		return fmt.Errorf("details button: send photos: %w", err)
	}
	return nil
}

// sendPhotos читает фото обращения из архива и отправляет в чат.
func (uc *UseCase) sendPhotos(ctx context.Context, chatID, complaintID int64) error {
	photos, err := uc.files.List(ctx, complaintID)
	if err != nil {
		return err
	}
	if len(photos) == 0 {
		return nil
	}

	files := make([]domain.ComplaintTGPhoto, 0, len(photos))
	for _, p := range photos {
		rc, err := uc.files.Open(ctx, complaintID, p.Name)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil {
			return fmt.Errorf("read photo %s: %w", p.Name, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close photo %s: %w", p.Name, closeErr)
		}
		files = append(files, domain.ComplaintTGPhoto{Ext: p.Ext, Data: data})
	}
	return uc.notify.SendPhotos(ctx, chatID, files)
}

// Start запускает тикер напоминаний: раз в минуту ищет обращения, у которых
// наступил дедлайн и статус не «Завершено», шлёт уведомление по правилу
// текущего статуса и сдвигает дедлайн на сутки. Ошибки не роняют тикер:
// дедлайн не сдвигается, пока уведомление не ушло (ретрай на следующем тике).
func (uc *UseCase) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			uc.tickReminders(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// tickReminders — один тик напоминаний; ошибки только логируются.
func (uc *UseCase) tickReminders(ctx context.Context) {
	due, err := uc.repo.DueComplaints(ctx, uc.now())
	if err != nil {
		slog.Info(fmt.Sprintf("complaints: тикер напоминаний: %v", err))
		return
	}

	for _, d := range due {
		uc.remindOne(ctx, d)
	}
}

// remindOne — одно напоминание: уведомление по статусу, затем сдвиг
// дедлайна на сутки. Сдвиг — только после успешной отправки.
func (uc *UseCase) remindOne(ctx context.Context, d domain.ComplaintDue) {
	if err := uc.notifyForStatus(ctx, d.ID, d.Status); err != nil {
		slog.Info(fmt.Sprintf("complaints: напоминание по обращению %d: %v", d.ID, err))
		return
	}
	slog.Info(fmt.Sprintf("complaints: напоминание по обращению %d отправлено, дедлайн +24ч", d.ID))
	if err := uc.repo.ShiftComplaintDeadline(ctx, d.ID); err != nil {
		slog.Info(fmt.Sprintf("complaints: сдвиг дедлайна обращения %d: %v", d.ID, err))
	}
}

// htmlEscape экранирует пользовательский текст (тег) для parse_mode=HTML.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

// parseID разбирает целое id из строки (данные кнопки).
func parseID(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
