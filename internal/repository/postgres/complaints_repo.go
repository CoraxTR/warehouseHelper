package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/domain"
)

// Модуль «Жалобы»: методы на общем PGClient с префиксом Complaint.
// Фото жалоб в БД не хранятся (zip-архивы на диске — photostore),
// таблицы фото нет: архив — источник истины.

// complaintColumns — колонки complaints в порядке SELECT/Scan.
const complaintColumns = `id, ms_order_number, created_at, phone, description, actions, status, deadline`

// scanComplaintRow сканирует строку complaints в domain.Complaint
// (порядок complaintColumns; товары добираются отдельным запросом).
func scanComplaintRow(row pgx.Row) (*domain.Complaint, error) {
	var c domain.Complaint
	if err := row.Scan(
		&c.ID, &c.MSOrderNumber, &c.CreatedAt, &c.Phone, &c.Description, &c.Actions,
		(*string)(&c.Status), &c.Deadline,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateComplaint создаёт обращение с товарами в одной транзакции.
// Возвращает id созданного обращения. product_id == "" → NULL
// (товар удалён из каталога; строка живёт со снимком названия).
func (pg *PGClient) CreateComplaint(ctx context.Context, c *domain.Complaint) (int64, error) {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin complaint tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx,
		`INSERT INTO complaints (ms_order_number, phone, description, actions, status, deadline)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		c.MSOrderNumber, c.Phone, c.Description, c.Actions, string(c.Status), c.Deadline,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert complaint: %w", err)
	}

	if err := insertComplaintItems(ctx, tx, id, c.Items); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit complaint %d: %w", id, err)
	}
	return id, nil
}

// UpdateComplaint обновляет поля обращения и заменяет список товаров
// в одной транзакции. Обращения нет — domain.ErrComplaintNotFound.
func (pg *PGClient) UpdateComplaint(ctx context.Context, c *domain.Complaint) error {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complaint tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE complaints
		 SET ms_order_number = $2, phone = $3, description = $4, actions = $5,
		     status = $6, deadline = $7
		 WHERE id = $1`,
		c.ID, c.MSOrderNumber, c.Phone, c.Description, c.Actions, string(c.Status), c.Deadline,
	)
	if err != nil {
		return fmt.Errorf("update complaint %d: %w", c.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrComplaintNotFound
	}

	if _, err := tx.Exec(ctx, `DELETE FROM complaint_items WHERE complaint_id = $1`, c.ID); err != nil {
		return fmt.Errorf("clear complaint %d items: %w", c.ID, err)
	}
	if err := insertComplaintItems(ctx, tx, c.ID, c.Items); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit complaint %d: %w", c.ID, err)
	}
	return nil
}

// insertComplaintItems вставляет товары обращения (в рамках транзакции).
func insertComplaintItems(ctx context.Context, tx pgx.Tx, complaintID int64, items []domain.ComplaintItem) error {
	for _, it := range items {
		var productID any
		if it.ProductID != "" {
			productID = it.ProductID
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO complaint_items (complaint_id, product_id, product_name)
			 VALUES ($1, $2, $3)`,
			complaintID, productID, it.ProductName,
		); err != nil {
			return fmt.Errorf("insert complaint %d item %q: %w", complaintID, it.ProductName, err)
		}
	}
	return nil
}

// GetComplaint возвращает обращение с товарами.
// Обращения нет — domain.ErrComplaintNotFound.
func (pg *PGClient) GetComplaint(ctx context.Context, id int64) (*domain.Complaint, error) {
	c, err := scanComplaintRow(pg.Pool.QueryRow(ctx,
		`SELECT `+complaintColumns+` FROM complaints WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrComplaintNotFound
		}
		return nil, fmt.Errorf("get complaint %d: %w", id, err)
	}

	items, err := pg.loadComplaintItems(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Items = items
	return c, nil
}

// loadComplaintItems возвращает товары обращения в порядке добавления.
func (pg *PGClient) loadComplaintItems(ctx context.Context, complaintID int64) ([]domain.ComplaintItem, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT product_id, product_name FROM complaint_items
		 WHERE complaint_id = $1 ORDER BY id`, complaintID)
	if err != nil {
		return nil, fmt.Errorf("query complaint %d items: %w", complaintID, err)
	}
	defer rows.Close()

	items := make([]domain.ComplaintItem, 0)
	for rows.Next() {
		var (
			it        domain.ComplaintItem
			productID *string
		)
		if err := rows.Scan(&productID, &it.ProductName); err != nil {
			return nil, fmt.Errorf("scan complaint %d item: %w", complaintID, err)
		}
		if productID != nil {
			it.ProductID = *productID
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate complaint %d items: %w", complaintID, err)
	}
	return items, nil
}

// complaintSummaryColumns — колонки списков обращений (complaints + items).
// Товары жалобы собираются в ComplaintSummary.Items; строки отсортированы
// по дате создания (убывание), затем по id — порядок товаров внутри жалобы
// сохраняется через ORDER BY ci.id.
const complaintSummaryColumns = `c.id, c.ms_order_number, c.created_at, c.phone, c.status,
    ci.product_id, ci.product_name`

// ComplaintSummaryRow — сырая строка списка: complaint + один товар
// (LEFT JOIN). Товары жалобы собирает collectComplaintSummaries.
type ComplaintSummaryRow struct {
	ID            int64
	MSOrderNumber string
	CreatedAt     time.Time
	Phone         string
	Status        domain.ComplaintStatus
	ProductID     *string // NULL — товар удалён из каталога (строки нет в списке товаров)
	ProductName   string  // "" — у жалобы нет строки товара (LEFT JOIN не сработал)
}

// scanComplaintSummaryRow сканирует строку списка в ComplaintSummaryRow
// (порядок complaintSummaryColumns).
func scanComplaintSummaryRow(row pgx.Row) (ComplaintSummaryRow, error) {
	var r ComplaintSummaryRow
	if err := row.Scan(
		&r.ID, &r.MSOrderNumber, &r.CreatedAt, &r.Phone, (*string)(&r.Status),
		&r.ProductID, &r.ProductName,
	); err != nil {
		return r, err
	}
	return r, nil
}

// collectComplaintSummaries собирает строки LEFT JOIN (complaint + товары)
// в список summaries: товары одной жалобы склеиваются в Items.
func collectComplaintSummaries(rows pgx.Rows) ([]domain.ComplaintSummary, error) {
	defer rows.Close()

	summaries := make([]domain.ComplaintSummary, 0)
	indexByID := make(map[int64]int)

	for rows.Next() {
		r, err := scanComplaintSummaryRow(rows)
		if err != nil {
			return nil, err
		}

		idx, ok := indexByID[r.ID]
		if !ok {
			summaries = append(summaries, domain.ComplaintSummary{
				ID:            r.ID,
				MSOrderNumber: r.MSOrderNumber,
				CreatedAt:     r.CreatedAt,
				Phone:         r.Phone,
				Status:        r.Status,
			})
			idx = len(summaries) - 1
			indexByID[r.ID] = idx
		}

		if r.ProductName != "" {
			productID := ""
			if r.ProductID != nil {
				productID = *r.ProductID
			}
			summaries[idx].Items = append(summaries[idx].Items, domain.ComplaintItem{
				ProductID:   productID,
				ProductName: r.ProductName,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

// ListActiveComplaintSummaries — обращения со статусом != «Завершено»
// (главная «Жалобы») по убыванию даты создания.
func (pg *PGClient) ListActiveComplaintSummaries(ctx context.Context) ([]domain.ComplaintSummary, error) {
	return pg.complaintSummaries(ctx, ` WHERE c.status <> 'completed'`)
}

// ListAllComplaintSummaries — полный список обращений (все статусы).
func (pg *PGClient) ListAllComplaintSummaries(ctx context.Context) ([]domain.ComplaintSummary, error) {
	return pg.complaintSummaries(ctx, "")
}

// complaintSummaries — общий запрос списка обращений с товарами (LEFT JOIN)
// по убыванию даты создания; where — готовое WHERE-условие или пустая строка.
func (pg *PGClient) complaintSummaries(ctx context.Context, where string) ([]domain.ComplaintSummary, error) {
	query := `SELECT ` + complaintSummaryColumns + `
		FROM complaints c
		LEFT JOIN complaint_items ci ON ci.complaint_id = c.id` + where + `
		ORDER BY c.created_at DESC, c.id DESC, ci.id`

	rows, err := pg.Pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query complaints list: %w", err)
	}
	return collectComplaintSummaries(rows)
}

// SearchComplaints ищет обращения по фильтру. Пустые поля фильтра не
// ограничивают; заданные поля комбинируются (AND). Phone передаётся уже
// нормализованным (7XXXXXXXXXX); ProductID — точное вхождение товара;
// ProductName — подстрока названия по снимкам товаров (complaint_items,
// ILIKE); From/To — диапазон по дате создания (From включительно, To
// включительно, локальный день сервера).
func (pg *PGClient) SearchComplaints(ctx context.Context, f domain.ComplaintFilter) ([]domain.ComplaintSummary, error) {
	var (
		conds []string
		args  []any
	)

	if f.MSOrderNumber != "" {
		args = append(args, "%"+f.MSOrderNumber+"%")
		conds = append(conds, fmt.Sprintf(`c.ms_order_number ILIKE $%d`, len(args)))
	}
	if f.Phone != "" {
		args = append(args, f.Phone)
		conds = append(conds, fmt.Sprintf(`c.phone = $%d`, len(args)))
	}
	if f.ProductID != "" {
		args = append(args, f.ProductID)
		conds = append(conds, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM complaint_items si WHERE si.complaint_id = c.id AND si.product_id = $%d
		)`, len(args)))
	}
	if f.ProductName != "" {
		// Подстрока названия по снимкам товаров обращения (ILIKE, как
		// ms_order_number выше): поиск по «сырому» тексту без выбора
		// товара из каталога.
		args = append(args, "%"+f.ProductName+"%")
		conds = append(conds, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM complaint_items si WHERE si.complaint_id = c.id AND si.product_name ILIKE $%d
		)`, len(args)))
	}
	if !f.From.IsZero() {
		args = append(args, f.From)
		conds = append(conds, fmt.Sprintf(`c.created_at >= $%d`, len(args)))
	}
	if !f.To.IsZero() {
		// To — конец дня (устанавливает usecase); поиск до него исключительно.
		args = append(args, f.To.Add(24*time.Hour))
		conds = append(conds, fmt.Sprintf(`c.created_at < $%d`, len(args)))
	}

	query := `SELECT ` + complaintSummaryColumns + `
		FROM complaints c
		LEFT JOIN complaint_items ci ON ci.complaint_id = c.id`
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	query += ` ORDER BY c.created_at DESC, c.id DESC, ci.id`

	rows, err := pg.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search complaints: %w", err)
	}
	return collectComplaintSummaries(rows)
}

// DueComplaints возвращает обращения, у которых наступил дедлайн и статус
// ещё не «Завершено» (тикер напоминаний), по возрастанию дедлайна.
func (pg *PGClient) DueComplaints(ctx context.Context, now time.Time) ([]domain.ComplaintDue, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT id, status FROM complaints
		 WHERE status <> 'completed' AND deadline <= $1
		 ORDER BY deadline`, now)
	if err != nil {
		return nil, fmt.Errorf("query due complaints: %w", err)
	}
	defer rows.Close()

	due := make([]domain.ComplaintDue, 0)
	for rows.Next() {
		var d domain.ComplaintDue
		if err := rows.Scan(&d.ID, (*string)(&d.Status)); err != nil {
			return nil, fmt.Errorf("scan due complaint: %w", err)
		}
		due = append(due, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due complaints: %w", err)
	}
	return due, nil
}

// ShiftComplaintDeadline сдвигает дедлайн обращения на сутки вперёд
// (после отправки напоминания). Обращение, завершённое или удалённое
// к моменту сдвига, не трогается (не ошибка).
func (pg *PGClient) ShiftComplaintDeadline(ctx context.Context, id int64) error {
	if _, err := pg.Pool.Exec(ctx,
		`UPDATE complaints SET deadline = deadline + interval '24 hours'
		 WHERE id = $1 AND status <> 'completed'`, id); err != nil {
		return fmt.Errorf("shift complaint %d deadline: %w", id, err)
	}
	return nil
}

// DeleteComplaint удаляет обращение с товарами (каскад). Фото-архив на
// диске не трогается — вызывающий удаляет его сам, если нужно.
func (pg *PGClient) DeleteComplaint(ctx context.Context, id int64) error {
	if _, err := pg.Pool.Exec(ctx, `DELETE FROM complaints WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete complaint %d: %w", id, err)
	}
	return nil
}

// ListComplaintRoleTags возвращает зарегистрированные теги ролей.
func (pg *PGClient) ListComplaintRoleTags(ctx context.Context) ([]domain.ComplaintRoleTag, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT role, tag FROM complaint_role_tags ORDER BY role`)
	if err != nil {
		return nil, fmt.Errorf("query complaint role tags: %w", err)
	}
	defer rows.Close()

	tags := make([]domain.ComplaintRoleTag, 0)
	for rows.Next() {
		var t domain.ComplaintRoleTag
		if err := rows.Scan((*string)(&t.Role), &t.Tag); err != nil {
			return nil, fmt.Errorf("scan complaint role tag: %w", err)
		}
		tags = append(tags, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate complaint role tags: %w", err)
	}
	return tags, nil
}

// SetComplaintRoleTag регистрирует (создаёт или обновляет) тег роли.
func (pg *PGClient) SetComplaintRoleTag(ctx context.Context, role domain.ComplaintRole, tag string) error {
	if _, err := pg.Pool.Exec(ctx,
		`INSERT INTO complaint_role_tags (role, tag) VALUES ($1, $2)
		 ON CONFLICT (role) DO UPDATE SET tag = EXCLUDED.tag`,
		string(role), tag); err != nil {
		return fmt.Errorf("set complaint role tag %s: %w", role, err)
	}
	return nil
}

// DeleteComplaintRoleTag удаляет тег роли; отсутствие тега не ошибка.
func (pg *PGClient) DeleteComplaintRoleTag(ctx context.Context, role domain.ComplaintRole) error {
	if _, err := pg.Pool.Exec(ctx,
		`DELETE FROM complaint_role_tags WHERE role = $1`, string(role)); err != nil {
		return fmt.Errorf("delete complaint role tag %s: %w", role, err)
	}
	return nil
}
