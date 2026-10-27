package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var ErrDepartmentBusy = errors.New("department has members or pending registrations")
var ErrProtectedAdmin = errors.New("protected administrator")

func ValidDepartmentName(name string) bool {
	if name != strings.TrimSpace(name) || utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 80 || strings.HasPrefix(name, "/") {
		return false
	}
	if strings.EqualFold(name, "Direktor") || strings.EqualFold(name, "🏢 Direktor") || strings.EqualFold(name, "Bosh direktor maslahatchisi") || strings.EqualFold(name, "🏢 Bosh direktor maslahatchisi") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Removing a department archives it. Historical reports and references survive.
func (s *Store) ManageDepartment(ctx context.Context, actor int64, action string, id int64, name string) error {
	if action != "archive" && !ValidDepartmentName(name) {
		return fmt.Errorf("invalid department name")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72640120)`); err != nil {
		return err
	}
	if action != "add" {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT is_active FROM departments WHERE id=$1 FOR UPDATE`, id).Scan(&active); err != nil {
			return err
		}
		if !active {
			return pgx.ErrNoRows
		}
	}
	switch action {
	case "add", "rename":
		var duplicate bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM departments WHERE lower(name)=lower($1) AND id<>$2)`, name, id).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate {
			return fmt.Errorf("department name already exists")
		}
		if action == "add" {
			err = tx.QueryRow(ctx, `INSERT INTO departments(name) VALUES($1) RETURNING id`, name).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, `UPDATE departments SET name=$2 WHERE id=$1`, id, name)
		}
	case "archive":
		var busy bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employees WHERE department_id=$1 AND is_active)
   OR EXISTS(SELECT 1 FROM access_requests WHERE department_id=$1 AND status='pending')
   OR EXISTS(SELECT 1 FROM registration_drafts WHERE department_id=$1)`, id).Scan(&busy)
		if err != nil {
			return err
		}
		if busy {
			return ErrDepartmentBusy
		}
		_, err = tx.Exec(ctx, `UPDATE departments SET is_active=FALSE WHERE id=$1`, id)
	default:
		return fmt.Errorf("invalid management action")
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target_id,details) VALUES($1,$2,$3,jsonb_build_object('name',$4::text))`, actor, "department_"+action, id, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type AdminDraft struct {
	Action   string
	TargetID int64
	Token    string
	Version  int64
}

func (s *Store) BeginAdminAction(ctx context.Context, actor int64, action string, target, version int64) (AdminDraft, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return AdminDraft{}, err
	}
	draft := AdminDraft{Action: action, TargetID: target, Version: version, Token: hex.EncodeToString(bytes)}
	_, err := s.Pool.Exec(ctx, `INSERT INTO admin_drafts(telegram_id,action,target_id,token,expected_version) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(telegram_id) DO UPDATE SET action=$2,target_id=$3,token=$4,expected_version=$5,updated_at=NOW()`, actor, action, target, draft.Token, version)
	return draft, err
}

func (s *Store) AdminAction(ctx context.Context, actor int64) (AdminDraft, error) {
	var draft AdminDraft
	err := s.Pool.QueryRow(ctx, `SELECT action,target_id,token,expected_version FROM admin_drafts WHERE telegram_id=$1 AND updated_at>NOW()-interval '15 minutes'`, actor).Scan(&draft.Action, &draft.TargetID, &draft.Token, &draft.Version)
	return draft, err
}
func (s *Store) CancelAdminAction(ctx context.Context, actor int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM admin_drafts WHERE telegram_id=$1`, actor)
	return err
}

func (s *Store) ManagedUsers(ctx context.Context, offset int) ([]Employee, error) {
	if offset < 0 {
		return nil, fmt.Errorf("invalid offset")
	}
	rows, err := s.Pool.Query(ctx, `SELECT e.telegram_id,e.full_name,e.role,e.is_active,COALESCE(e.department_id,0),COALESCE(d.name,'')
 FROM employees e LEFT JOIN departments d ON d.id=e.department_id ORDER BY e.full_name,e.telegram_id LIMIT 9 OFFSET $1`, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Employee
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.TelegramID, &e.FullName, &e.Role, &e.IsActive, &e.DepartmentID, &e.DepartmentName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EmployeeVersion(ctx context.Context, id int64) (int64, error) {
	var version int64
	err := s.Pool.QueryRow(ctx, `SELECT (extract(epoch FROM updated_at)*1000000)::bigint FROM employees WHERE telegram_id=$1`, id).Scan(&version)
	return version, err
}

// The action token and account revision prevent stale menus from changing another request.
func (s *Store) CompleteRoleAction(ctx context.Context, actor int64, token string, departmentID int64) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72640120)`); err != nil {
		return 0, err
	}
	var draft AdminDraft
	if err = tx.QueryRow(ctx, `SELECT action,target_id,expected_version FROM admin_drafts WHERE telegram_id=$1 AND token=$2 AND updated_at>NOW()-interval '15 minutes' FOR UPDATE`, actor, token).Scan(&draft.Action, &draft.TargetID, &draft.Version); err != nil {
		return 0, err
	}
	var oldRole string
	var currentVersion int64
	if err = tx.QueryRow(ctx, `SELECT role,(extract(epoch FROM updated_at)*1000000)::bigint FROM employees WHERE telegram_id=$1 FOR UPDATE`, draft.TargetID).Scan(&oldRole, &currentVersion); err != nil {
		return 0, err
	}
	if currentVersion != draft.Version {
		return 0, pgx.ErrNoRows
	}
	if draft.TargetID == actor {
		return 0, ErrProtectedAdmin
	}
	if oldRole == "admin" {
		var others int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM employees WHERE role='admin' AND is_active AND telegram_id<>$1`, draft.TargetID).Scan(&others); err != nil {
			return 0, err
		}
		if others == 0 {
			return 0, ErrProtectedAdmin
		}
	}
	role := strings.TrimPrefix(draft.Action, "role:")
	if role == "revoke" {
		_, err = tx.Exec(ctx, `UPDATE employees SET is_active=FALSE,updated_at=clock_timestamp() WHERE telegram_id=$1`, draft.TargetID)
	} else {
		if role != "employee" && role != "manager" && role != "advisor" {
			return 0, fmt.Errorf("invalid role")
		}
		var dept *int64
		if role != "advisor" {
			var active bool
			if err = tx.QueryRow(ctx, `SELECT is_active FROM departments WHERE id=$1 FOR SHARE`, departmentID).Scan(&active); err != nil {
				return 0, err
			}
			if !active {
				return 0, pgx.ErrNoRows
			}
			dept = &departmentID
		}
		_, err = tx.Exec(ctx, `UPDATE employees SET role=$2,department_id=$3,is_active=TRUE,updated_at=clock_timestamp() WHERE telegram_id=$1`, draft.TargetID, role, dept)
	}
	if err != nil {
		return 0, err
	}
	// Old pending approvals must not undo the administrator's newer decision.
	if _, err = tx.Exec(ctx, `UPDATE access_requests SET status='rejected',reviewed_at=NOW(),reviewed_by=$2 WHERE telegram_id=$1 AND status='pending'`, draft.TargetID, actor); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM registration_drafts WHERE telegram_id=$1`, draft.TargetID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE notification_jobs SET cancelled_at=NOW(),locked_until=NULL WHERE recipient_telegram_id=$1 AND delivered_at IS NULL`, draft.TargetID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target_id,details) VALUES($1,$2,$3,jsonb_build_object('old_role',$4::text,'department_id',$5::bigint))`, actor, draft.Action, draft.TargetID, oldRole, departmentID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM admin_drafts WHERE telegram_id=$1`, actor); err != nil {
		return 0, err
	}
	return draft.TargetID, tx.Commit(ctx)
}
