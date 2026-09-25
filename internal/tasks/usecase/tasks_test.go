package usecase

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
)

// Open: строка ленты создаётся, сообщение уходит с кнопкой отметки, id задачи
// едет в callback_data кнопки (по нему нажатие находит задачу).
func TestOpenCreatesTaskAndSendsButton(t *testing.T) {
	uc, repo, notifier := newUC()

	if err := uc.Open(context.Background(), domain.TaskKindDiscountPut, "Поставить скидку 20% на Творог (до 22.06)"); err != nil {
		t.Fatalf("открытие задачи: %v", err)
	}

	if want := []string{"Поставить скидку 20% на Творог (до 22.06)"}; !reflect.DeepEqual(repo.created, want) {
		t.Errorf("созданные задачи %q, want %q", repo.created, want)
	}
	if got := repo.createdKnd; len(got) != 1 || got[0] != domain.TaskKindDiscountPut {
		t.Errorf("виды задач %v, want [%s]", got, domain.TaskKindDiscountPut)
	}
	if len(repo.deleted) != 0 {
		t.Errorf("задача откачена без причины: %v", repo.deleted)
	}

	wantSent := []sentTask{{
		text:         "Поставить скидку 20% на Творог (до 22.06)",
		button:       doneButton,
		callbackData: DoneCallbackData(2), // репозиторий выдал следующую задачу
	}}
	if !reflect.DeepEqual(notifier.sent, wantSent) {
		t.Errorf("отправлено %+v, want %+v", notifier.sent, wantSent)
	}
}

// Ошибка отправки — строку откатываем и отдаём ошибку вызывающему: задачи,
// которой нет в чате, в ленте быть не должно.
func TestOpenRollsBackOnSendError(t *testing.T) {
	uc, repo, notifier := newUC()
	notifier.sendErr = errors.New("телеграм недоступен")

	err := uc.Open(context.Background(), domain.TaskKindStockOut, "Убрать с сайта: Творог")
	if err == nil {
		t.Fatal("ошибка отправки проглочена")
	}
	if !strings.Contains(err.Error(), "телеграм недоступен") {
		t.Errorf("ошибка без причины: %v", err)
	}
	if want := []int64{2}; !reflect.DeepEqual(repo.deleted, want) {
		t.Errorf("откат %v, want %v", repo.deleted, want)
	}
}

// Канал не подключён (SendTask вернул 0 без ошибки) — строку тоже убираем.
func TestOpenRollsBackWhenChannelOff(t *testing.T) {
	uc, repo, notifier := newUC()
	notifier.sendID = 0

	if err := uc.Open(context.Background(), domain.TaskKindStockIn, "Вернуть на сайт: Творог"); err != nil {
		t.Fatalf("тишина канала — не ошибка: %v", err)
	}
	if want := []int64{2}; !reflect.DeepEqual(repo.deleted, want) {
		t.Errorf("откат %v, want %v", repo.deleted, want)
	}
}

// Нотифаер не подключён (nil): задачи нет и в БД — как раньше «текст только
// в лог».
func TestOpenNilNotifier(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUseCase(repo, nil)

	if err := uc.Open(context.Background(), domain.TaskKindStockOut, "Убрать с сайта: Творог"); err != nil {
		t.Fatalf("nil-шов: %v", err)
	}
	if len(repo.created) != 0 {
		t.Errorf("задача создана без канала: %v", repo.created)
	}
}

// Нажатие «✅»: то же сообщение спрашивает «кто отметил» — кнопки меняются на
// список сотрудников по employeesPerRow в строке.
func TestHandleDoneAsksWho(t *testing.T) {
	uc, repo, notifier := newUC()
	repo.employees = []domain.Employee{
		{ID: 1, FullName: "Иванов Иван Иванович", Position: "Кладовщик"},
		{ID: 2, FullName: "Петров Пётр Петрович", Position: "Менеджер"},
		{ID: 3, FullName: "Сидорова Анна", Position: "Фасовщик"},
	}

	if err := uc.HandleDone(context.Background(), "cb-1", -1005, 777, 1); err != nil {
		t.Fatalf("нажатие ✅: %v", err)
	}

	if want := []string{"cb-1"}; !reflect.DeepEqual(notifier.answered, want) {
		t.Errorf("закрытые нажатия %v, want %v", notifier.answered, want)
	}
	wantRows := [][]domain.TGButton{
		{{Text: "Иванов Иван Иванович", CallbackData: "task_who:1:1"}, {Text: "Петров Пётр Петрович", CallbackData: "task_who:1:2"}},
		{{Text: "Сидорова Анна", CallbackData: "task_who:1:3"}},
	}
	if len(notifier.keyboards) != 1 {
		t.Fatalf("правок кнопок %d, want 1", len(notifier.keyboards))
	}
	got := notifier.keyboards[0]
	if got.chatID != -1005 || got.messageID != 777 {
		t.Errorf("правка ушла не в то сообщение: %+v", got)
	}
	if !reflect.DeepEqual(got.rows, wantRows) {
		t.Errorf("кнопки сотрудников %+v, want %+v", got.rows, wantRows)
	}
}

// Повторное нажатие по уже закрытой задаче — всплывающее окно с тем, кто и
// когда отметил; кнопки не трогаются.
func TestHandleDoneAlreadyMarked(t *testing.T) {
	uc, repo, notifier := newUC()
	doneAt := fixedMarkMoment
	task := repo.taskByID[1]
	task.DoneAt, task.DoneBy = &doneAt, "Иванов Иван Иванович"
	repo.taskByID[1] = task

	if err := uc.HandleDone(context.Background(), "cb-2", -1005, 777, 1); err != nil {
		t.Fatalf("повторное нажатие: %v", err)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("всплывающих окон %d, want 1", len(notifier.alerts))
	}
	if alert := notifier.alerts[0].alert; !strings.HasPrefix(alert, "Уже отмечено: Иванов Иван Иванович, ") {
		t.Errorf("текст окна %q, want «Уже отмечено: <ФИО>, <время>»", alert)
	}
	if len(notifier.keyboards) != 0 {
		t.Errorf("кнопки тронуты у закрытой задачи: %+v", notifier.keyboards)
	}
}

// Задачи в БД нет (кнопка осталась от старого сообщения) — окно, без паники.
func TestHandleDoneTaskMissing(t *testing.T) {
	uc, _, notifier := newUC()

	if err := uc.HandleDone(context.Background(), "cb-3", -1005, 777, 999); err != nil {
		t.Fatalf("нажатие по пропавшей задаче: %v", err)
	}
	if len(notifier.alerts) != 1 || notifier.alerts[0].alert != "Задача не найдена" {
		t.Errorf("окна %+v, want «Задача не найдена»", notifier.alerts)
	}
}

// База сотрудников пуста: спрашивать некого — окно со ссылкой на страницу.
func TestHandleDoneNoEmployees(t *testing.T) {
	uc, _, notifier := newUC()

	if err := uc.HandleDone(context.Background(), "cb-4", -1005, 777, 1); err != nil {
		t.Fatalf("нажатие без базы: %v", err)
	}
	if len(notifier.alerts) != 1 || !strings.Contains(notifier.alerts[0].alert, "Сотрудники") {
		t.Errorf("окна %+v, want подсказку про базу сотрудников", notifier.alerts)
	}
	if len(notifier.keyboards) != 0 {
		t.Errorf("кнопки без базы: %+v", notifier.keyboards)
	}
}

// Выбор сотрудника: отметка записана (кто и когда), кнопки погашены, в то же
// сообщение дописано «✅ Выполнено: ФИО, время».
func TestHandleWhoMarksTask(t *testing.T) {
	uc, repo, notifier := newUC()
	repo.employees = []domain.Employee{{ID: 7, FullName: "Иванов Иван Иванович", Position: "Кладовщик"}}

	if err := uc.HandleWho(context.Background(), "cb-5", -1005, 777, 1, 7); err != nil {
		t.Fatalf("выбор сотрудника: %v", err)
	}

	wantMarks := []markCall{{taskID: 1, employeeID: 7, name: "Иванов Иван Иванович"}}
	if !reflect.DeepEqual(repo.marks, wantMarks) {
		t.Errorf("отметки %+v, want %+v", repo.marks, wantMarks)
	}
	if len(notifier.texts) != 1 {
		t.Fatalf("правок текста %d, want 1", len(notifier.texts))
	}
	got := notifier.texts[0]
	if got.chatID != -1005 || got.messageID != 777 {
		t.Errorf("правка ушла не в то сообщение: %+v", got)
	}
	want := "Убрать с сайта: Творог\n✅ Выполнено: Иванов Иван Иванович, " + fixedMarkMoment.Format(momentLayout)
	if got.text != want {
		t.Errorf("текст сообщения %q, want %q", got.text, want)
	}
	if want := []string{"cb-5"}; !reflect.DeepEqual(notifier.answered, want) {
		t.Errorf("закрытые нажатия %v, want %v", notifier.answered, want)
	}
}

// Гонка двух нажатий: второй выбор сотрудника отметку не переписывает.
func TestHandleWhoAlreadyMarked(t *testing.T) {
	uc, repo, notifier := newUC()
	repo.markTaken = true
	repo.employees = []domain.Employee{{ID: 7, FullName: "Иванов Иван Иванович", Position: "Кладовщик"}}

	if err := uc.HandleWho(context.Background(), "cb-6", -1005, 777, 1, 7); err != nil {
		t.Fatalf("повторная отметка: %v", err)
	}
	if len(notifier.alerts) != 1 || notifier.alerts[0].alert != "Задачу уже отметили" {
		t.Errorf("окна %+v, want «Задачу уже отметили»", notifier.alerts)
	}
	if len(notifier.texts) != 0 {
		t.Errorf("текст переписан повторной отметкой: %+v", notifier.texts)
	}
}

// Сотрудника удалили из базы, пока он висел в кнопках — окно с подсказкой.
func TestHandleWhoEmployeeMissing(t *testing.T) {
	uc, _, notifier := newUC()

	if err := uc.HandleWho(context.Background(), "cb-7", -1005, 777, 1, 42); err != nil {
		t.Fatalf("выбор удалённого сотрудника: %v", err)
	}
	if len(notifier.alerts) != 1 || !strings.Contains(notifier.alerts[0].alert, "Сотрудник не найден") {
		t.Errorf("окна %+v, want «Сотрудник не найден»", notifier.alerts)
	}
	if len(notifier.texts) != 0 {
		t.Errorf("текст тронут без отметки: %+v", notifier.texts)
	}
}

// Ошибки швов наружу: юзкейс их не глотает (логирует вызывающий).
func TestHandleDoneRepoError(t *testing.T) {
	uc, repo, _ := newUC()
	repo.getErr = errors.New("БД недоступна")

	if err := uc.HandleDone(context.Background(), "cb-8", -1005, 777, 1); !errors.Is(err, repo.getErr) {
		t.Errorf("ошибка %v, want %v", err, repo.getErr)
	}
}

// Минуты отметки форматируются в местном времени склада, а не в UTC.
func TestMarkedMomentUsesLocalTime(t *testing.T) {
	at := time.Date(2026, time.September, 25, 23, 30, 0, 0, loc)
	if got, want := formatMoment(at), "25.09 23:30"; got != want {
		t.Errorf("момент отметки %q, want %q", got, want)
	}
}
