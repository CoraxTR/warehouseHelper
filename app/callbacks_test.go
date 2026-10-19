package app

import (
	"context"
	"fmt"
	"testing"

	"warehouseHelper/internal/telegram"
)

// stubComplaints — кнопки жалоб в тесте диспетчера: копит вызовы строкой.
type stubComplaints struct{ calls []string }

func (s *stubComplaints) HandleDetailsButton(_ context.Context, callbackQueryID string, chatID, complaintID int64) error {
	s.calls = append(s.calls, fmt.Sprintf("complaint:%s:%d:%d", callbackQueryID, chatID, complaintID))

	return nil
}

// stubTasks — кнопки задач в тесте диспетчера.
type stubTasks struct{ calls []string }

func (s *stubTasks) HandleDone(_ context.Context, callbackQueryID string, chatID, messageID, taskID int64) error {
	s.calls = append(s.calls, fmt.Sprintf("done:%s:%d:%d:%d", callbackQueryID, chatID, messageID, taskID))

	return nil
}

func (s *stubTasks) HandleWho(_ context.Context, callbackQueryID string, chatID, messageID, taskID, employeeID int64) error {
	s.calls = append(s.calls, fmt.Sprintf("who:%s:%d:%d:%d:%d", callbackQueryID, chatID, messageID, taskID, employeeID))

	return nil
}

// stubMailing — кнопка «Пропустить рассылку» в тесте диспетчера.
type stubMailing struct{ calls []string }

func (s *stubMailing) SkipMailing(_ context.Context, callbackQueryID string, chatID, messageID int64) error {
	s.calls = append(s.calls, fmt.Sprintf("skip:%s:%d:%d", callbackQueryID, chatID, messageID))

	return nil
}

// Диспетчер нажатий: кнопка уходит своему модулю по префиксу данных, чужие
// данные не трогают никого. message_id доезжает до модуля задач — по нему тот
// правит кнопки и текст в том же сообщении.
func TestHandleCallbackRoutes(t *testing.T) {
	tests := []struct {
		title string
		data  string
		want  []string
	}{
		{"кнопка жалобы", "complaint_details:12", []string{"complaint:cb-1:-1005:12"}},
		{"отметка задачи", "task_done:7", []string{"done:cb-1:-1005:777:7"}},
		{"выбор сотрудника", "task_who:7:3", []string{"who:cb-1:-1005:777:7:3"}},
		{"пропуск рассылки", "ds:skip", []string{"skip:cb-1:-1005:777"}},
		{"чужие данные", "some_button:1", nil},
		{"пустые данные", "", nil},
		{"битые данные задачи", "task_done:семь", nil},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			complaints, tasks, mailing := &stubComplaints{}, &stubTasks{}, &stubMailing{}
			cb := telegram.CallbackQuery{ID: "cb-1", ChatID: -1005, MessageID: 777, Data: tt.data}

			if err := handleCallback(context.Background(), complaints, tasks, mailing, cb); err != nil {
				t.Fatalf("диспетчер: %v", err)
			}

			got := append(append(append([]string{}, complaints.calls...), tasks.calls...), mailing.calls...)
			if len(got) != len(tt.want) {
				t.Fatalf("вызовы %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("вызов %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
