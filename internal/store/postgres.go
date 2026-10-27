package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Employee struct {
	ID               int64
	TelegramID       int64
	FullName         string
	Role             string
	IsActive         bool
	TelegramUsername string
	DepartmentID     int64
	DepartmentName   string
}

type Report struct {
	FullName       string
	ReportText     string
	SubmittedAt    time.Time
	DepartmentID   int64
	DepartmentName string
}

type Store struct {
	Pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database ping: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) GetEmployee(ctx context.Context, telegramID int64) (Employee, error) {
	var e Employee

	err := s.Pool.QueryRow(ctx, `
		SELECT
			e.id,
			e.telegram_id,
			e.full_name,
			e.role,
			e.is_active,
			COALESCE(e.department_id, 0),
			COALESCE(d.name, ''), e.telegram_username
		FROM employees e
		LEFT JOIN departments d ON d.id = e.department_id
		WHERE e.telegram_id = $1`, telegramID).Scan(
		&e.ID,
		&e.TelegramID,
		&e.FullName,
		&e.Role,
		&e.IsActive,
		&e.DepartmentID,
		&e.DepartmentName,
		&e.TelegramUsername,
	)

	return e, err
}

func (s *Store) UpsertRequest(ctx context.Context, telegramID int64, username, requestedName string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO access_requests (telegram_id, telegram_username, requested_name, department_id,status, requested_at, reviewed_at, reviewed_by)
		VALUES ($1,$2,$3,(SELECT id FROM departments ORDER BY (name='IUTXRvaEQB') DESC,id LIMIT 1),'pending',NOW(),NULL,NULL)
		ON CONFLICT (telegram_id) DO UPDATE SET
			telegram_username=EXCLUDED.telegram_username,
			requested_name=EXCLUDED.requested_name,
			status='pending', requested_at=NOW(), reviewed_at=NULL, reviewed_by=NULL`,
		telegramID, username, requestedName)
	return err
}

type AccessRequest struct {
	RequestedRole  string
	TelegramID     int64
	Username       string
	RequestedName  string
	RequestedAt    time.Time
	DepartmentID   int64
	DepartmentName string
}

func (s *Store) PendingRequests(ctx context.Context) ([]AccessRequest, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.telegram_id, COALESCE(a.telegram_username,''), COALESCE(a.requested_name,''), a.requested_at,COALESCE(a.department_id,0),COALESCE(d.name,''),a.requested_role
		FROM access_requests a LEFT JOIN departments d ON d.id=a.department_id WHERE a.status='pending' ORDER BY a.requested_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessRequest
	for rows.Next() {
		var r AccessRequest
		if err := rows.Scan(&r.TelegramID, &r.Username, &r.RequestedName, &r.RequestedAt, &r.DepartmentID, &r.DepartmentName, &r.RequestedRole); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetAccessRequest(ctx context.Context, telegramID int64) (AccessRequest, error) {
	var r AccessRequest
	err := s.Pool.QueryRow(ctx, `
		SELECT a.telegram_id, COALESCE(a.telegram_username, ''),
		       COALESCE(a.requested_name, ''), a.requested_at,COALESCE(a.department_id,0),COALESCE(d.name,''),a.requested_role
		FROM access_requests a LEFT JOIN departments d ON d.id=a.department_id
		WHERE a.telegram_id = $1 AND a.status = 'pending'`, telegramID).Scan(
		&r.TelegramID, &r.Username, &r.RequestedName, &r.RequestedAt, &r.DepartmentID, &r.DepartmentName, &r.RequestedRole,
	)
	return r, err
}

func (s *Store) Approve(ctx context.Context, telegramID int64, name, role string, reviewerID int64) error {
	return s.approveAccount(ctx, telegramID, name, role, reviewerID, false)
}

// ReportRecipients returns active managers and advisors who receive the automatic daily summary.
func (s *Store) ReportRecipients(ctx context.Context) ([]Employee, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT e.id, e.telegram_id, e.full_name, e.role, e.is_active,
		       COALESCE(e.department_id, 0), COALESCE(d.name, ''), e.telegram_username
		FROM employees e
		LEFT JOIN departments d ON d.id = e.department_id
		WHERE e.is_active = TRUE AND e.role IN ('manager', 'advisor')
		ORDER BY e.role, e.full_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Employee
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.ID, &e.TelegramID, &e.FullName, &e.Role, &e.IsActive, &e.DepartmentID, &e.DepartmentName, &e.TelegramUsername); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) ActiveEmployees(ctx context.Context) ([]Employee, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, telegram_id, full_name, role, is_active FROM employees WHERE is_active=TRUE ORDER BY full_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Employee
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.ID, &e.TelegramID, &e.FullName, &e.Role, &e.IsActive); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) SaveReport(ctx context.Context, telegramID int64, date time.Time, text string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var reportID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO reports (employee_id, report_date, report_text,department_id)
		SELECT id, $2::date, $3,department_id FROM employees WHERE telegram_id=$1 AND is_active=TRUE
		ON CONFLICT (employee_id, report_date) DO UPDATE SET
			report_text=EXCLUDED.report_text,department_id=EXCLUDED.department_id,updated_at=clock_timestamp()
		RETURNING id`,
		telegramID, date.Format("2006-01-02"), text).Scan(&reportID)
	if err != nil {
		return err
	}
	// An edited report belongs to the author's current department. Do not send
	// pending versions with the former department or let cached summaries leak it.
	if _, err := tx.Exec(ctx, `UPDATE notification_jobs SET cancelled_at=NOW(),locked_until=NULL,lease=lease+1
 WHERE kind='report' AND delivered_at IS NULL AND event_key LIKE 'report:'||$1::bigint::text||':%'`, reportID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notification_jobs SET message='',message_scope='',next_part=0,locked_until=NULL,lease=lease+1
 WHERE kind IN ('daily_summary','role_summary') AND delivered_at IS NULL AND cancelled_at IS NULL`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, queueReportsSQL, date.Format("2006-01-02"), reportID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) MyReports(ctx context.Context, telegramID int64, limit int) ([]Report, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT e.full_name, r.report_text, r.submitted_at
		FROM reports r JOIN employees e ON e.id=r.employee_id
		WHERE e.telegram_id=$1 ORDER BY r.report_date DESC LIMIT $2`, telegramID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.FullName, &r.ReportText, &r.SubmittedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ReportsForDate(ctx context.Context, date time.Time) ([]Report, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT e.full_name, r.report_text, r.submitted_at
		FROM reports r JOIN employees e ON e.id=r.employee_id
		WHERE r.report_date=$1::date ORDER BY e.full_name`, date.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.FullName, &r.ReportText, &r.SubmittedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) StatusForDate(ctx context.Context, date time.Time) ([]Employee, []int64, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT e.id, e.telegram_id, e.full_name, e.role, e.is_active,
		       (r.id IS NOT NULL) AS submitted
		FROM employees e
		LEFT JOIN reports r ON r.employee_id=e.id AND r.report_date=$1::date
		WHERE e.is_active=TRUE AND (e.role='employee' OR r.id IS NOT NULL) ORDER BY e.full_name`, date.Format("2006-01-02"))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var submitted, missing []Employee
	for rows.Next() {
		var e Employee
		var done bool
		if err := rows.Scan(&e.ID, &e.TelegramID, &e.FullName, &e.Role, &e.IsActive, &done); err != nil {
			return nil, nil, err
		}
		if done {
			submitted = append(submitted, e)
		} else {
			missing = append(missing, e)
		}
	}
	return submitted, employeeIDs(missing), rows.Err()
}

func employeeIDs(es []Employee) []int64 {
	ids := make([]int64, 0, len(es))
	for _, e := range es {
		ids = append(ids, e.TelegramID)
	}
	return ids
}

// StatusEmployeesByDepartment returns the employees who submitted and did not submit a report.
// departmentID == 0 means all departments.
func (s *Store) StatusEmployeesByDepartment(ctx context.Context, date time.Time, departmentID int64) ([]Employee, []Employee, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT e.id, e.telegram_id, e.full_name, e.role, e.is_active,
		       COALESCE(r.department_id,e.department_id, 0), COALESCE(d.name, ''),
		       (r.id IS NOT NULL) AS submitted
		FROM employees e
		LEFT JOIN reports r ON r.employee_id = e.id AND r.report_date = $1::date
		LEFT JOIN departments d ON d.id = COALESCE(r.department_id,e.department_id)
		WHERE (e.is_active = TRUE OR r.id IS NOT NULL) AND (e.role = 'employee' OR r.id IS NOT NULL) AND ($2 = 0 OR COALESCE(r.department_id,e.department_id) = $2)
		ORDER BY d.name, e.full_name`, date.Format("2006-01-02"), departmentID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var submitted, missing []Employee
	for rows.Next() {
		var e Employee
		var done bool
		if err := rows.Scan(&e.ID, &e.TelegramID, &e.FullName, &e.Role, &e.IsActive, &e.DepartmentID, &e.DepartmentName, &done); err != nil {
			return nil, nil, err
		}
		if done {
			submitted = append(submitted, e)
		} else {
			missing = append(missing, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return submitted, missing, nil
}

func (s *Store) HasReport(ctx context.Context, telegramID int64, date time.Time) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM reports r JOIN employees e ON e.id=r.employee_id
			WHERE e.telegram_id=$1 AND r.report_date=$2::date
		)`, telegramID, date.Format("2006-01-02")).Scan(&exists)
	return exists, err
}

var _ = pgx.ErrNoRows

type Department struct {
	ID   int64
	Name string
}

func (s *Store) Departments(ctx context.Context) ([]Department, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name
		FROM departments
		WHERE is_active
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Department

	for rows.Next() {
		var d Department

		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}

		result = append(result, d)
	}

	return result, rows.Err()
}

func (s *Store) AssignDepartment(
	ctx context.Context,
	telegramID int64,
	departmentID int64,
) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE employees
		SET department_id = $2,
		    updated_at = NOW()
		WHERE telegram_id = $1
		  AND is_active = TRUE
		  AND EXISTS (
		      SELECT 1
		      FROM departments
		      WHERE id = $2 AND is_active
		  )`, telegramID, departmentID)

	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf(
			"xodim topilmadi yoki bo‘lim ID noto‘g‘ri",
		)
	}

	return nil
}

func (s *Store) ReportsForDateByDepartment(
	ctx context.Context,
	date time.Time,
	departmentID int64,
) ([]Report, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT
			e.full_name,
			r.report_text,
			r.submitted_at,
			COALESCE(r.department_id, 0),
			COALESCE(d.name, '')
		FROM reports r
		JOIN employees e ON e.id = r.employee_id
		LEFT JOIN departments d ON d.id = r.department_id
		WHERE r.report_date = $1::date
		  AND ($2 = 0 OR r.department_id = $2)
		  
		ORDER BY d.name, e.full_name
	`, date.Format("2006-01-02"), departmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(
			&r.FullName,
			&r.ReportText,
			&r.SubmittedAt,
			&r.DepartmentID,
			&r.DepartmentName,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}

	return result, rows.Err()
}

func (s *Store) StatusForDateByDepartment(
	ctx context.Context,
	date time.Time,
	departmentID int64,
) ([]Employee, []int64, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT
			e.id,
			e.telegram_id,
			e.full_name,
			e.role,
			e.is_active,
			COALESCE(e.department_id, 0),
			COALESCE(d.name, ''),
			(r.id IS NOT NULL) AS submitted
		FROM employees e
		LEFT JOIN departments d ON d.id = e.department_id
		LEFT JOIN reports r
			ON r.employee_id = e.id
			AND r.report_date = $1::date
		WHERE e.is_active = TRUE
		  AND ($2 = 0 OR e.department_id = $2)
		ORDER BY d.name, e.full_name
	`, date.Format("2006-01-02"), departmentID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var submitted, missing []Employee

	for rows.Next() {
		var e Employee
		var done bool

		if err := rows.Scan(
			&e.ID,
			&e.TelegramID,
			&e.FullName,
			&e.Role,
			&e.IsActive,
			&e.DepartmentID,
			&e.DepartmentName,
			&done,
		); err != nil {
			return nil, nil, err
		}

		if done {
			submitted = append(submitted, e)
		} else {
			missing = append(missing, e)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	return submitted, employeeIDs(missing), nil
}
