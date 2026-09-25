package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/domain"
)

// С подделок требуем оба шва модуля: реализация неполная — тест не собрался.
var (
	_ Repository = (*fakeRepo)(nil)
	_ Notifier   = (*fakeNotifier)(nil)
)

// fakeRepo — репозиторий модуля в тестах: задачи и сотрудники в памяти,
// раздельные error-поля на метод (общее поле валит и настройку, и сам тест).
type fakeRepo struct {
	taskByID  map[int64]domain.Task
	feed      []domain.Task
	employees []domain.Employee
	nextID    int64
	nextEmpID int64

	created    []string
	createdKnd []domain.TaskKind
	deleted    []int64
	marks      []markCall
	limit      int
	addedEmp   []domain.Employee
	deletedEmp []int64
	markTaken  bool // задача уже отмечена: MarkTaskDone вернёт ok=false

	createErr  error
	deleteErr  error
	getErr     error
	listErr    error
	markErr    error
	listEmpErr error
	getEmpErr  error
	addEmpErr  error
	delEmpErr  error
}

// markCall — один вызов отметки задачи.
type markCall struct {
	taskID     int64
	employeeID int64
	name       string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{taskByID: make(map[int64]domain.Task), nextID: 0, nextEmpID: 100}
}

func (r *fakeRepo) CreateTask(_ context.Context, kind domain.TaskKind, text string) (int64, error) {
	if r.createErr != nil {
		return 0, r.createErr
	}
	r.nextID++
	r.created = append(r.created, text)
	r.createdKnd = append(r.createdKnd, kind)
	r.taskByID[r.nextID] = domain.Task{ID: r.nextID, Kind: kind, Text: text}

	return r.nextID, nil
}

func (r *fakeRepo) DeleteTask(_ context.Context, id int64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleted = append(r.deleted, id)
	delete(r.taskByID, id)

	return nil
}

func (r *fakeRepo) GetTask(_ context.Context, id int64) (*domain.Task, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	task, ok := r.taskByID[id]
	if !ok {
		//nolint:nilnil // контракт порта: задачи нет — (nil, nil), как у репозитория
		return nil, nil
	}
	copied := task

	return &copied, nil
}

func (r *fakeRepo) ListTasks(_ context.Context, limit int) ([]domain.Task, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	r.limit = limit

	return r.feed, nil
}

func (r *fakeRepo) MarkTaskDone(_ context.Context, id, employeeID int64, name string) (time.Time, bool, error) {
	if r.markErr != nil {
		return time.Time{}, false, r.markErr
	}
	r.marks = append(r.marks, markCall{taskID: id, employeeID: employeeID, name: name})
	if r.markTaken {
		return time.Time{}, false, nil
	}
	if task, ok := r.taskByID[id]; ok {
		task.DoneAt = &fixedMarkMoment
		task.DoneBy = name
		r.taskByID[id] = task
	}

	return fixedMarkMoment, true, nil
}

func (r *fakeRepo) ListEmployees(_ context.Context) ([]domain.Employee, error) {
	if r.listEmpErr != nil {
		return nil, r.listEmpErr
	}

	return r.employees, nil
}

func (r *fakeRepo) GetEmployee(_ context.Context, id int64) (*domain.Employee, error) {
	if r.getEmpErr != nil {
		return nil, r.getEmpErr
	}
	for _, e := range r.employees {
		if e.ID == id {
			copied := e

			return &copied, nil
		}
	}

	//nolint:nilnil // контракт порта: задачи нет — (nil, nil), как у репозитория
	return nil, nil
}

func (r *fakeRepo) AddEmployee(_ context.Context, fullName, position string) (int64, error) {
	if r.addEmpErr != nil {
		return 0, r.addEmpErr
	}
	r.nextEmpID++
	r.addedEmp = append(r.addedEmp, domain.Employee{ID: r.nextEmpID, FullName: fullName, Position: position})

	return r.nextEmpID, nil
}

func (r *fakeRepo) DeleteEmployee(_ context.Context, id int64) error {
	if r.delEmpErr != nil {
		return r.delEmpErr
	}
	r.deletedEmp = append(r.deletedEmp, id)

	return nil
}

// fixedMarkMoment — момент отметки, который отдаёт подделка (в местном времени
// склада: текст сообщения форматируется в нём).
var fixedMarkMoment = time.Date(2026, time.September, 25, 12, 5, 0, 0, loc)

// sentTask — отправленная задача.
type sentTask struct {
	text         string
	button       string
	callbackData string
}

// keyboardCall — правка кнопок сообщения.
type keyboardCall struct {
	chatID    int64
	messageID int64
	rows      [][]domain.TGButton
}

// textCall — правка текста сообщения.
type textCall struct {
	chatID    int64
	messageID int64
	text      string
}

// alertCall — ответ на нажатие всплывающим окном.
type alertCall struct {
	callbackID string
	alert      string
}

// fakeNotifier — телеграм-шов в тестах: копит отправки и правки, ошибки —
// раздельными полями.
type fakeNotifier struct {
	sent      []sentTask
	keyboards []keyboardCall
	texts     []textCall
	answered  []string
	alerts    []alertCall

	sendID    int64 // message_id от SendTask; 0 — «канал не подключён»
	sendErr   error
	answerErr error
	editKbErr error
	editTxErr error
}

func (n *fakeNotifier) SendTask(_ context.Context, text, buttonText, callbackData string) (int64, error) {
	if n.sendErr != nil {
		return 0, n.sendErr
	}
	n.sent = append(n.sent, sentTask{text: text, button: buttonText, callbackData: callbackData})

	return n.sendID, nil
}

func (n *fakeNotifier) EditKeyboard(_ context.Context, chatID, messageID int64, rows [][]domain.TGButton) error {
	if n.editKbErr != nil {
		return n.editKbErr
	}
	n.keyboards = append(n.keyboards, keyboardCall{chatID: chatID, messageID: messageID, rows: rows})

	return nil
}

func (n *fakeNotifier) EditText(_ context.Context, chatID, messageID int64, text string) error {
	if n.editTxErr != nil {
		return n.editTxErr
	}
	n.texts = append(n.texts, textCall{chatID: chatID, messageID: messageID, text: text})

	return nil
}

func (n *fakeNotifier) AnswerCallback(_ context.Context, callbackQueryID string) error {
	if n.answerErr != nil {
		return n.answerErr
	}
	n.answered = append(n.answered, callbackQueryID)

	return nil
}

func (n *fakeNotifier) AnswerCallbackAlert(_ context.Context, callbackQueryID, alert string) error {
	n.alerts = append(n.alerts, alertCall{callbackID: callbackQueryID, alert: alert})

	return nil
}

// newUC — юзкейс на подделках: репозиторий с задачей id=1 и нотифаер,
// который «доставляет» сообщение (message_id=555).
func newUC() (*UseCase, *fakeRepo, *fakeNotifier) {
	repo := newFakeRepo()
	repo.nextID = 1
	repo.taskByID[1] = domain.Task{ID: 1, Kind: domain.TaskKindStockOut, Text: "Убрать с сайта: Творог", CreatedAt: fixedMarkMoment}
	notifier := &fakeNotifier{sendID: 555}

	return NewUseCase(repo, notifier), repo, notifier
}
