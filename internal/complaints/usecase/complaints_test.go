package usecase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/complaints/photostore"
	"warehouseHelper/internal/domain"
)

// ---- Стабы ----

// stubRepo — минимальный репозиторий для тестов сценариев.
type stubRepo struct {
	complaints map[int64]*domain.Complaint
	nextID     int64
	tags       []domain.ComplaintRoleTag
	due        []domain.ComplaintDue
	shifted    []int64
	lastSearch domain.ComplaintFilter // последний фильтр Search (для проверок)
	err        error                  // общая ошибка хранилища (если задана)
}

func newStubRepo() *stubRepo {
	return &stubRepo{complaints: map[int64]*domain.Complaint{}, nextID: 1}
}

func (s *stubRepo) fail() error {
	if s.err != nil {
		return s.err
	}
	return nil
}

func (s *stubRepo) CreateComplaint(_ context.Context, c *domain.Complaint) (int64, error) {
	if err := s.fail(); err != nil {
		return 0, err
	}
	id := s.nextID
	s.nextID++
	c.ID = id
	c.CreatedAt = time.Now()
	cp := *c
	cp.Items = append([]domain.ComplaintItem(nil), c.Items...)
	s.complaints[id] = &cp
	return id, nil
}

func (s *stubRepo) UpdateComplaint(_ context.Context, c *domain.Complaint) error {
	if err := s.fail(); err != nil {
		return err
	}
	old, ok := s.complaints[c.ID]
	if !ok {
		return domain.ErrComplaintNotFound
	}
	old.MSOrderNumber = c.MSOrderNumber
	old.Phone = c.Phone
	old.Description = c.Description
	old.Actions = c.Actions
	old.Status = c.Status
	old.Deadline = c.Deadline
	old.Items = append([]domain.ComplaintItem(nil), c.Items...)
	return nil
}

func (s *stubRepo) DeleteComplaint(_ context.Context, id int64) error {
	delete(s.complaints, id)
	return nil
}

func (s *stubRepo) GetComplaint(_ context.Context, id int64) (*domain.Complaint, error) {
	if err := s.fail(); err != nil {
		return nil, err
	}
	c, ok := s.complaints[id]
	if !ok {
		return nil, domain.ErrComplaintNotFound
	}
	cp := *c
	cp.Items = append([]domain.ComplaintItem(nil), c.Items...)
	return &cp, nil
}

func (s *stubRepo) ListActiveComplaintSummaries(_ context.Context) ([]domain.ComplaintSummary, error) {
	return nil, nil
}

func (s *stubRepo) ListAllComplaintSummaries(_ context.Context) ([]domain.ComplaintSummary, error) {
	return nil, nil
}

func (s *stubRepo) SearchComplaints(_ context.Context, f domain.ComplaintFilter) ([]domain.ComplaintSummary, error) {
	s.lastSearch = f
	return nil, nil
}

func (s *stubRepo) DueComplaints(_ context.Context, _ time.Time) ([]domain.ComplaintDue, error) {
	return append([]domain.ComplaintDue(nil), s.due...), nil
}

func (s *stubRepo) ShiftComplaintDeadline(_ context.Context, id int64) error {
	s.shifted = append(s.shifted, id)
	return nil
}

func (s *stubRepo) ListComplaintRoleTags(_ context.Context) ([]domain.ComplaintRoleTag, error) {
	return append([]domain.ComplaintRoleTag(nil), s.tags...), nil
}

func (s *stubRepo) SetComplaintRoleTag(_ context.Context, role domain.ComplaintRole, tag string) error {
	for i, t := range s.tags {
		if t.Role == role {
			s.tags[i].Tag = tag
			return nil
		}
	}
	s.tags = append(s.tags, domain.ComplaintRoleTag{Role: role, Tag: tag})
	return nil
}

func (s *stubRepo) DeleteComplaintRoleTag(_ context.Context, role domain.ComplaintRole) error {
	out := s.tags[:0]
	for _, t := range s.tags {
		if t.Role != role {
			out = append(out, t)
		}
	}
	s.tags = out
	return nil
}

// stubCatalog — каталог товаров.
type stubCatalog struct{ products map[string]domain.Product }

func (c stubCatalog) GetProductsByIDs(_ context.Context, ids []string) ([]domain.Product, error) {
	out := make([]domain.Product, 0, len(ids))
	for _, id := range ids {
		if p, ok := c.products[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// stubPhotos — файловое хранилище фото.
type stubPhotos struct {
	list map[int64][]photostore.Photo
	open map[string][]byte // key: complaintID/name
}

func (p *stubPhotos) List(_ context.Context, complaintID int64) ([]photostore.Photo, error) {
	return append([]photostore.Photo(nil), p.list[complaintID]...), nil
}

func (p *stubPhotos) Add(_ context.Context, complaintID int64, ups []photostore.Upload) error {
	if p.list == nil {
		p.list = map[int64][]photostore.Photo{}
	}
	for _, u := range ups {
		p.list[complaintID] = append(p.list[complaintID], photostore.Photo{Name: u.ID + "." + u.Ext, ID: u.ID, Ext: u.Ext})
	}
	return nil
}

func (p *stubPhotos) Remove(_ context.Context, complaintID int64, name string) error {
	out := p.list[complaintID][:0]
	for _, ph := range p.list[complaintID] {
		if ph.Name != name {
			out = append(out, ph)
		}
	}
	p.list[complaintID] = out
	return nil
}

func (p *stubPhotos) Open(_ context.Context, complaintID int64, name string) (io.ReadCloser, error) {
	if p.open == nil {
		return nil, errors.New("нет файла")
	}
	data, ok := p.open[photoKey(complaintID, name)]
	if !ok {
		return nil, errors.New("нет файла")
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func photoKey(complaintID int64, name string) string {
	return fmt.Sprintf("%d/%s", complaintID, name)
}

// stubNotifier — telegram-уведомления.
type stubNotifier struct {
	statusTexts  []string
	statusData   []string
	details      []string
	photoBatches int
	answered     []string
	photoCalls   int
}

func (n *stubNotifier) NotifyCommonStatus(_ context.Context, textHTML, callbackData string) error {
	n.statusTexts = append(n.statusTexts, textHTML)
	n.statusData = append(n.statusData, callbackData)
	return nil
}

func (n *stubNotifier) SendDetails(_ context.Context, _ int64, text string) error {
	n.details = append(n.details, text)
	return nil
}

func (n *stubNotifier) SendPhotos(_ context.Context, _ int64, photos []domain.ComplaintTGPhoto) error {
	n.photoCalls++
	n.photoBatches += len(photos)
	return nil
}

func (n *stubNotifier) AnswerCallback(_ context.Context, callbackQueryID string) error {
	n.answered = append(n.answered, callbackQueryID)
	return nil
}

// newTestUC собирает UseCase со стабами; now фиксируется.
func newTestUC(repo *stubRepo, tags []domain.ComplaintRoleTag) (*UseCase, *stubNotifier, *stubPhotos) {
	repo.tags = tags
	notify := &stubNotifier{}
	photos := &stubPhotos{}
	uc := NewUseCase(repo, stubCatalog{products: map[string]domain.Product{
		"p1": {ID: "p1", Name: "Пылесос"},
		"p2": {ID: "p2", Name: "Наушники"},
	}}, photos, notify, "http://warehouse.local:8080")
	fixed := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.Now().Location())
	uc.now = func() time.Time { return fixed }
	return uc, notify, photos
}

func validInput() ComplaintInput {
	return ComplaintInput{
		MSOrderNumber: "000123",
		Phone:         "+7-936-123-45-67",
		Description:   "Сломался",
		Actions:       "Позвонили клиенту",
		Status:        domain.ComplaintStatusCreated,
		Deadline:      time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Now().Location()),
		Items:         []domain.ComplaintItem{{ProductID: "p1", ProductName: ""}},
	}
}

func TestCreateNormalizesAndSaves(t *testing.T) {
	repo := newStubRepo()
	uc, notify, _ := newTestUC(repo, nil)

	id, err := uc.Create(context.Background(), validInput(), nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if id != 1 {
		t.Errorf("id = %d, want 1", id)
	}
	c := repo.complaints[id]
	if c.Phone != "79361234567" {
		t.Errorf("phone = %q, want 79361234567 (нормализованный)", c.Phone)
	}
	if c.Items[0].ProductName != "Пылесос" {
		t.Errorf("product name = %q, want снимок из каталога «Пылесос»", c.Items[0].ProductName)
	}
	if len(notify.statusTexts) != 0 {
		t.Errorf("status notifications = %d, want 0 для «Создано»", len(notify.statusTexts))
	}
}

func TestCreateValidations(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*ComplaintInput)
		want error
	}{
		{"номер заказа обязателен", func(in *ComplaintInput) { in.MSOrderNumber = " " }, ErrComplaintNoOrderNumber},
		{"телефон невалидный", func(in *ComplaintInput) { in.Phone = "abc" }, ErrComplaintBadPhone},
		{"дедлайн не указан", func(in *ComplaintInput) { in.Deadline = time.Time{} }, ErrComplaintNoDeadline},
		{"товар не выбран", func(in *ComplaintInput) { in.Items = nil }, ErrComplaintNoItems},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newStubRepo()
			uc, _, _ := newTestUC(repo, nil)
			in := validInput()
			tt.mut(&in)
			if _, err := uc.Create(context.Background(), in, nil); !errors.Is(err, tt.want) {
				t.Errorf("Create error = %v, want %v", err, tt.want)
			}
		})
	}

	t.Run("дедлайн в прошлом", func(t *testing.T) {
		repo := newStubRepo()
		uc, _, _ := newTestUC(repo, nil)
		in := validInput()
		in.Deadline = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.Now().Location())
		if _, err := uc.Create(context.Background(), in, nil); !errors.Is(err, ErrComplaintDeadlinePast) {
			t.Errorf("Create error = %v, want ErrComplaintDeadlinePast", err)
		}
	})
}

func TestCreateInstantNotifyOnReviewing(t *testing.T) {
	repo := newStubRepo()
	uc, notify, _ := newTestUC(repo, []domain.ComplaintRoleTag{
		{Role: domain.ComplaintRoleValidator, Tag: "@ivanov"},
	})

	in := validInput()
	in.Status = domain.ComplaintStatusReviewing
	if _, err := uc.Create(context.Background(), in, nil); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	if len(notify.statusTexts) != 1 {
		t.Fatalf("уведомлений = %d, want 1", len(notify.statusTexts))
	}
	text := notify.statusTexts[0]
	if !strings.HasPrefix(text, "@ivanov ") {
		t.Errorf("текст без тега в начале: %q", text)
	}
	if !strings.Contains(text, `href="http://warehouse.local:8080/complaint?id=1"`) {
		t.Errorf("нет ссылки на просмотр: %q", text)
	}
	if !strings.Contains(text, "Обращение 1") || !strings.Contains(text, "Ожидаем согласования") {
		t.Errorf("текст не похож на уведомление: %q", text)
	}
	if !strings.HasSuffix(text, "\nСсылка открывается только в локальной сети") {
		t.Errorf("нет приписки про локальную сеть: %q", text)
	}
	if notify.statusData[0] != "complaint_details:1" {
		t.Errorf("callback data = %q, want complaint_details:1", notify.statusData[0])
	}
}

func TestCreateInstantNotifyOnCompleted(t *testing.T) {
	repo := newStubRepo()
	uc, notify, _ := newTestUC(repo, nil)

	in := validInput()
	in.Status = domain.ComplaintStatusCompleted
	if _, err := uc.Create(context.Background(), in, nil); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if len(notify.statusTexts) != 1 || !strings.Contains(notify.statusTexts[0], "Завершено") {
		t.Errorf("нет мгновенного уведомления «Завершено»: %#v", notify.statusTexts)
	}
}

func TestUpdateNotifiesOnlyOnTransition(t *testing.T) {
	repo := newStubRepo()
	uc, notify, _ := newTestUC(repo, nil)

	in := validInput()
	id, err := uc.Create(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	// повторное сохранение того же статуса — молча
	in2 := in
	in2.Description = "обновили описание"
	if err := uc.Update(context.Background(), id, in2); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if len(notify.statusTexts) != 0 {
		t.Errorf("уведомлений при том же статусе = %d, want 0", len(notify.statusTexts))
	}

	// переход в «На рассмотрении» — уведомление
	in3 := in2
	in3.Status = domain.ComplaintStatusReviewing
	if err := uc.Update(context.Background(), id, in3); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if len(notify.statusTexts) != 1 || !strings.Contains(notify.statusTexts[0], "Ожидаем согласования") {
		t.Errorf("нет уведомления о переходе в «На рассмотрении»: %#v", notify.statusTexts)
	}

	// переход в «Склад» — молча (напоминание придёт по дедлайну)
	in4 := in3
	in4.Status = domain.ComplaintStatusWarehouse
	if err := uc.Update(context.Background(), id, in4); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if len(notify.statusTexts) != 1 {
		t.Errorf("уведомлений при переходе в «Склад» = %d, want 1 (не больше)", len(notify.statusTexts))
	}

	// «Завершено» — мгновенное
	in5 := in4
	in5.Status = domain.ComplaintStatusCompleted
	if err := uc.Update(context.Background(), id, in5); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if len(notify.statusTexts) != 2 || !strings.Contains(notify.statusTexts[1], "Завершено") {
		t.Errorf("нет уведомления о «Завершено»: %#v", notify.statusTexts)
	}
}

func TestUpdateRejectsPastDeadline(t *testing.T) {
	repo := newStubRepo()
	uc, _, _ := newTestUC(repo, nil)

	in := validInput()
	id, err := uc.Create(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	in.Deadline = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.Now().Location())
	if err := uc.Update(context.Background(), id, in); !errors.Is(err, ErrComplaintDeadlinePast) {
		t.Errorf("Update error = %v, want ErrComplaintDeadlinePast", err)
	}
}

// TestSearchByProductName — текстовая подстрока названия товара (без выбора
// из каталога) уходит в фильтр как есть.
func TestSearchByProductName(t *testing.T) {
	repo := newStubRepo()
	uc, _, _ := newTestUC(repo, nil)

	f := domain.ComplaintFilter{ProductName: "микроволнов"}
	if _, err := uc.Search(context.Background(), f); err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if repo.lastSearch.ProductName != "микроволнов" {
		t.Errorf("ProductName в фильтре = %q, want %q", repo.lastSearch.ProductName, "микроволнов")
	}
}

// TestSearchProductIDOverridesName — выбранный из каталога товар (ProductID)
// главнее текстовой подстроки: имя в снимке обращения могло отличаться от
// текущего (товар переименовывали), точное вхождение — источник истины.
func TestSearchProductIDOverridesName(t *testing.T) {
	repo := newStubRepo()
	uc, _, _ := newTestUC(repo, nil)

	f := domain.ComplaintFilter{
		ProductID:   "abc-123",
		ProductName: "старое название",
	}
	if _, err := uc.Search(context.Background(), f); err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if repo.lastSearch.ProductID != "abc-123" {
		t.Errorf("ProductID в фильтре = %q, want abc-123", repo.lastSearch.ProductID)
	}
	if repo.lastSearch.ProductName != "" {
		t.Errorf("ProductName = %q, want пустую (id главнее подстроки)", repo.lastSearch.ProductName)
	}
}

func TestTickRemindsAndShifts(t *testing.T) {
	repo := newStubRepo()
	uc, notify, _ := newTestUC(repo, []domain.ComplaintRoleTag{
		{Role: domain.ComplaintRoleWarehouse, Tag: "@sklad"},
	})
	repo.due = []domain.ComplaintDue{
		{ID: 1, Status: domain.ComplaintStatusWarehouse},
	}

	uc.tickReminders(context.Background())

	if len(notify.statusTexts) != 1 {
		t.Fatalf("напоминаний = %d, want 1", len(notify.statusTexts))
	}
	text := notify.statusTexts[0]
	if !strings.HasPrefix(text, "@sklad ") || !strings.Contains(text, "Ожидаем действий от склада") {
		t.Errorf("текст напоминания: %q", text)
	}
	if len(repo.shifted) != 1 || repo.shifted[0] != 1 {
		t.Errorf("сдвинутые дедлайны = %v, want [1]", repo.shifted)
	}
}

func TestTickSupplierUsesWarehouseTag(t *testing.T) {
	repo := newStubRepo()
	uc, notify, _ := newTestUC(repo, []domain.ComplaintRoleTag{
		{Role: domain.ComplaintRoleWarehouse, Tag: "@sklad"},
	})
	repo.due = []domain.ComplaintDue{
		{ID: 2, Status: domain.ComplaintStatusSupplier},
	}

	uc.tickReminders(context.Background())

	if len(notify.statusTexts) != 1 {
		t.Fatalf("напоминаний = %d, want 1", len(notify.statusTexts))
	}
	if !strings.HasPrefix(notify.statusTexts[0], "@sklad ") || !strings.Contains(notify.statusTexts[0], "Ожидаем ответ от поставщика") {
		t.Errorf("текст напоминания поставщику: %q", notify.statusTexts[0])
	}
}

func TestTickCreatedNoTag(t *testing.T) {
	repo := newStubRepo()
	uc, notify, _ := newTestUC(repo, []domain.ComplaintRoleTag{
		{Role: domain.ComplaintRoleWarehouse, Tag: "@sklad"},
		{Role: domain.ComplaintRoleValidator, Tag: "@ivanov"},
	})
	repo.due = []domain.ComplaintDue{
		{ID: 3, Status: domain.ComplaintStatusCreated},
	}

	uc.tickReminders(context.Background())

	if len(notify.statusTexts) != 1 {
		t.Fatalf("напоминаний = %d, want 1", len(notify.statusTexts))
	}
	text := notify.statusTexts[0]
	if strings.HasPrefix(text, "@") {
		t.Errorf("у «Создано» не должно быть тега: %q", text)
	}
	if !strings.Contains(text, `Статус "Создано" - поправьте пожалуйста`) {
		t.Errorf("текст: %q", text)
	}
}

func TestHandleDetailsButton(t *testing.T) {
	repo := newStubRepo()
	uc, notify, photos := newTestUC(repo, nil)
	photos.list = map[int64][]photostore.Photo{
		1: {{Name: "ab12.jpg", ID: "ab12", Ext: "jpg"}},
	}
	photos.open = map[string][]byte{photoKey(1, "ab12.jpg"): []byte("jpeg")}

	in := validInput()
	id, err := uc.Create(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	repo.complaints[id].Description = "Сломался"
	repo.complaints[id].Phone = "79361234567"

	if err := uc.HandleDetailsButton(context.Background(), "cb-9", -1005, id); err != nil {
		t.Fatalf("HandleDetailsButton error: %v", err)
	}

	if len(notify.answered) != 1 || notify.answered[0] != "cb-9" {
		t.Errorf("answerCallback = %v, want [cb-9]", notify.answered)
	}
	if len(notify.details) != 1 {
		t.Fatalf("детальных сообщений = %d, want 1", len(notify.details))
	}
	text := notify.details[0]
	for _, want := range []string{"Статус: Создано", "Номер заказа: 000123", "Телефон получателя: +7 936 123-45-67", "Товар: Пылесос", "Предпринятые шаги: Позвонили клиенту"} {
		if !strings.Contains(text, want) {
			t.Errorf("детали не содержат %q: %q", want, text)
		}
	}
	if notify.photoBatches != 1 {
		t.Errorf("отправлено фото = %d, want 1", notify.photoBatches)
	}
}

func TestParseCallbackData(t *testing.T) {
	if id, ok := ParseCallbackData("complaint_details:42"); !ok || id != 42 {
		t.Errorf("ParseCallbackData = (%d, %v), want (42, true)", id, ok)
	}
	for _, data := range []string{"", "complaint_details:", "complaint_details:abc", "other:42"} {
		if _, ok := ParseCallbackData(data); ok {
			t.Errorf("ParseCallbackData(%q): ok = true, want false", data)
		}
	}
}
