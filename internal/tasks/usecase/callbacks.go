package usecase

import (
	"fmt"
	"strconv"
	"strings"
)

// Данные inline-кнопок задачи (callback_data). Формат — «команда:id», как у
// жалоб; лимит Telegram в 64 байта держится с запасом.
const (
	donePrefix = "task_done:" // нажали «✅ Отметить» — спрашиваем, кто отметил
	whoPrefix  = "task_who:"  // выбрали сотрудника — записываем отметку
)

// Callback — разобранные данные кнопки задачи: TaskID — задача, EmployeeID — 0
// при нажатии «✅ Отметить» (этап «кто отметил») и > 0 при выборе сотрудника.
type Callback struct {
	TaskID     int64
	EmployeeID int64
}

// DoneCallbackData — данные кнопки «✅ Отметить» задачи.
func DoneCallbackData(taskID int64) string {
	return fmt.Sprintf("%s%d", donePrefix, taskID)
}

// whoCallbackData — данные кнопки сотрудника в списке «кто отметил».
func whoCallbackData(taskID, employeeID int64) string {
	return fmt.Sprintf("%s%d:%d", whoPrefix, taskID, employeeID)
}

// ParseCallbackData разбирает данные кнопки задачи. Не наш формат — ok=false
// (диспетчер кнопок ищет свой модуль по префиксу).
func ParseCallbackData(data string) (Callback, bool) {
	switch {
	case strings.HasPrefix(data, donePrefix):
		id, err := strconv.ParseInt(data[len(donePrefix):], 10, 64)
		if err != nil || id <= 0 {
			return Callback{}, false
		}
		return Callback{TaskID: id}, true
	case strings.HasPrefix(data, whoPrefix):
		rest := data[len(whoPrefix):]
		task, employee, ok := strings.Cut(rest, ":")
		if !ok {
			return Callback{}, false
		}
		taskID, err := strconv.ParseInt(task, 10, 64)
		if err != nil || taskID <= 0 {
			return Callback{}, false
		}
		employeeID, err := strconv.ParseInt(employee, 10, 64)
		if err != nil || employeeID <= 0 {
			return Callback{}, false
		}
		return Callback{TaskID: taskID, EmployeeID: employeeID}, true
	default:
		return Callback{}, false
	}
}
