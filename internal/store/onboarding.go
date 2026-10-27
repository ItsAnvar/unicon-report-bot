package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type RegistrationDraft struct {
	TelegramID     int64
	Username       string
	DepartmentID   int64
	DepartmentName string
	Step           string
	FullName       string
}

func (s *Store) BeginRegistration(ctx context.Context, id int64, username string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO registration_drafts(telegram_id,telegram_username,step)
 VALUES($1,$2,'name') ON CONFLICT(telegram_id) DO UPDATE SET telegram_username=$2,department_id=NULL,full_name='',step='name',updated_at=NOW()`, id, username)
	return err
}

func (s *Store) RegistrationDraft(ctx context.Context, id int64) (RegistrationDraft, error) {
	var draft RegistrationDraft
	err := s.Pool.QueryRow(ctx, `SELECT r.telegram_id,r.telegram_username,COALESCE(r.department_id,0),COALESCE(d.name,''),r.step,r.full_name
 FROM registration_drafts r LEFT JOIN departments d ON d.id=r.department_id WHERE r.telegram_id=$1`, id).Scan(
		&draft.TelegramID, &draft.Username, &draft.DepartmentID, &draft.DepartmentName, &draft.Step, &draft.FullName)
	return draft, err
}

func (s *Store) SelectRegistrationDepartment(ctx context.Context, id, departmentID int64) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE registration_drafts SET department_id=$2,step='role',updated_at=NOW()
 WHERE telegram_id=$1 AND step='department' AND EXISTS(SELECT 1 FROM departments WHERE id=$2 AND is_active)`, id, departmentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) SaveRegistrationName(ctx context.Context, id int64, name string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE registration_drafts SET full_name=$2,step='department',updated_at=NOW() WHERE telegram_id=$1 AND step='name'`, id, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) SubmitRegistration(ctx context.Context, id int64, role string) error {
	if role != "employee" && role != "manager" && role != "advisor" {
		return fmt.Errorf("invalid requested role")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var username, name, step string
	var departmentID *int64
	if err := tx.QueryRow(ctx, `SELECT telegram_username,full_name,department_id,step FROM registration_drafts
 WHERE telegram_id=$1 FOR UPDATE`, id).Scan(&username, &name, &departmentID, &step); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" || (role == "advisor" && step != "department") || (role != "advisor" && (step != "role" || departmentID == nil)) {
		return pgx.ErrNoRows
	}
	if role == "advisor" {
		departmentID = nil
	} else {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM departments WHERE id=$1 AND is_active)`, departmentID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employees WHERE telegram_id=$1 AND is_active)`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("account already active")
	}
	_, err = tx.Exec(ctx, `INSERT INTO access_requests(telegram_id,telegram_username,requested_name,department_id,requested_role,status)
 VALUES($1,$2,$3,$4,$5,'pending') ON CONFLICT(telegram_id) DO UPDATE SET telegram_username=$2,requested_name=$3,department_id=$4,requested_role=$5,
 status='pending',requested_at=clock_timestamp(),reviewed_at=NULL,reviewed_by=NULL`, id, username, name, departmentID, role)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM registration_drafts WHERE telegram_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CancelRegistration(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM registration_drafts WHERE telegram_id=$1`, id)
	return err
}

func (s *Store) ApprovePending(ctx context.Context, id int64, role string, reviewer, version int64) error {
	return s.approveAccount(ctx, id, "", role, reviewer, true, version)
}

func (s *Store) approveAccount(ctx context.Context, id int64, name, role string, reviewer int64, pendingOnly bool, versions ...int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid account ID")
	}
	switch role {
	case "employee", "manager", "advisor", "admin":
	default:
		return fmt.Errorf("invalid role")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var requestedName, username, status, requestedRole string
	var departmentID *int64
	var requestedAt time.Time
	err = tx.QueryRow(ctx, `SELECT requested_name,COALESCE(telegram_username,''),status,department_id,requested_at,requested_role
	 FROM access_requests WHERE telegram_id=$1 FOR UPDATE`, id).Scan(&requestedName, &username, &status, &departmentID, &requestedAt, &requestedRole)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if pendingOnly && (err == pgx.ErrNoRows || status != "pending" || (role != "advisor" && departmentID == nil) || strings.TrimSpace(requestedName) == "" || role != requestedRole) {
		return pgx.ErrNoRows
	}
	if pendingOnly && (len(versions) != 1 || versions[0] != requestedAt.UnixMicro()) {
		return pgx.ErrNoRows
	}
	if pendingOnly {
		name = requestedName
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name required")
	}
	if status != "pending" {
		departmentID = nil
	}
	if role == "advisor" {
		departmentID = nil
	}
	if pendingOnly && departmentID != nil {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT is_active FROM departments WHERE id=$1 FOR SHARE`, departmentID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return fmt.Errorf("department inactive")
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO employees(telegram_id,full_name,role,is_active,department_id,telegram_username,updated_at)
 VALUES($1,$2,$3,TRUE,CASE WHEN $3='advisor' THEN NULL ELSE COALESCE($4::bigint,(SELECT id FROM departments WHERE is_active ORDER BY id LIMIT 1)) END,$5,NOW())
 ON CONFLICT(telegram_id) DO UPDATE SET full_name=$2,role=$3,is_active=TRUE,
 department_id=CASE WHEN $3='advisor' THEN NULL ELSE COALESCE($4::bigint,employees.department_id,EXCLUDED.department_id) END,
 telegram_username=COALESCE(NULLIF($5,''),employees.telegram_username),updated_at=NOW()`, id, name, role, departmentID, username)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO access_requests(telegram_id,requested_name,status,reviewed_at,reviewed_by,department_id)
 VALUES($1,$2,'approved',NOW(),$3,COALESCE($4::bigint,(SELECT department_id FROM employees WHERE telegram_id=$1)))
 ON CONFLICT(telegram_id) DO UPDATE SET requested_name=$2,status='approved',reviewed_at=NOW(),reviewed_by=$3,
 department_id=COALESCE($4::bigint,access_requests.department_id)`, id, name, reviewer, departmentID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM registration_drafts WHERE telegram_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RejectPending(ctx context.Context, id, reviewer, version int64) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE access_requests SET status='rejected',reviewed_at=NOW(),reviewed_by=$2
	 WHERE telegram_id=$1 AND status='pending' AND (extract(epoch FROM requested_at)*1000000)::bigint=$3`, id, reviewer, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) ActiveAdminIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT telegram_id FROM employees WHERE is_active AND role='admin'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
