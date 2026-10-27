package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"unicon-report-bot/migrations"
)

type NotificationPayload struct {
	Date           string    `json:"date"`
	At             time.Time `json:"at"`
	EmployeeName   string    `json:"employee_name"`
	DepartmentName string    `json:"department_name"`
	Text           string    `json:"text"`
}

type NotificationJob struct {
	ID           int64
	RecipientID  int64
	Kind         string
	DepartmentID int64
	Payload      NotificationPayload
	Message      string
	MessageScope string
	NextPart     int
	Attempts     int
	Lease        int64
}

func (s *Store) EnsureDeliveryQueue(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72640119)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS unicon_schema_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		return err
	}
	for _, migration := range []struct {
		version int
		sql     string
	}{{4, migrations.DeliverySchema}, {5, migrations.OnboardingSchema}, {6, migrations.ManagementSchema}, {7, migrations.EriKormSchema}} {
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unicon_schema_migrations WHERE version=$1)`, migration.version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(migration.sql), "BEGIN;"), "COMMIT;")
		if _, err := tx.Exec(ctx, body); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO unicon_schema_migrations(version) VALUES($1)`, migration.version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RememberUsername(ctx context.Context, id int64, username string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE employees SET telegram_username=$2 WHERE telegram_id=$1`, id, username); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE access_requests SET telegram_username=$2 WHERE telegram_id=$1`, id, username); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// All report recipients are recorded in the same transaction as the report.
// The revision key makes startup reconciliation and repeated retries idempotent.
const queueReportsSQL = `
INSERT INTO notification_jobs(event_key,recipient_telegram_id,kind,department_id,payload)
SELECT 'report:'||r.id::text||':'||(extract(epoch FROM r.updated_at)*1000000)::bigint::text||':'||recipient.telegram_id::text,
 recipient.telegram_id,'report',r.department_id,
 jsonb_build_object('date',r.report_date::text,'at',r.updated_at,'employee_name',author.full_name,
                    'department_name',COALESCE(d.name,''),'text',r.report_text)
FROM reports r JOIN employees author ON author.id=r.employee_id
LEFT JOIN departments d ON d.id=r.department_id
JOIN employees recipient ON recipient.is_active AND
 (recipient.role='advisor' OR (recipient.role='manager' AND recipient.department_id=r.department_id))
WHERE author.is_active AND r.report_date=$1::date AND ($2::bigint=0 OR r.id=$2)
ON CONFLICT(event_key) DO UPDATE SET cancelled_at=NULL,department_id=EXCLUDED.department_id,
 payload=EXCLUDED.payload,message='',message_scope='',next_part=0,next_attempt_at=NOW(),locked_until=NULL
 WHERE notification_jobs.cancelled_at IS NOT NULL`

func (s *Store) QueueReportsForDate(ctx context.Context, date time.Time) error {
	_, err := s.Pool.Exec(ctx, queueReportsSQL, date.Format("2006-01-02"), int64(0))
	return err
}

func (s *Store) QueueLeaderSummary(ctx context.Context, recipient Employee, date time.Time, kind string) error {
	if !recipient.IsActive || (recipient.Role != "manager" && recipient.Role != "advisor") {
		return nil
	}
	scope := int64(0)
	if recipient.Role == "manager" {
		if recipient.DepartmentID == 0 {
			return fmt.Errorf("manager has no department")
		}
		scope = recipient.DepartmentID
	}
	key := fmt.Sprintf("%s:%s:%d:%d", kind, date.Format("2006-01-02"), recipient.TelegramID, scope)
	if kind == "role_summary" {
		version, err := s.EmployeeVersion(ctx, recipient.TelegramID)
		if err != nil {
			return err
		}
		key += fmt.Sprintf(":%d", version)
	}
	payload, err := json.Marshal(NotificationPayload{Date: date.Format("2006-01-02")})
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO notification_jobs(event_key,recipient_telegram_id,kind,department_id,payload)
 VALUES($1,$2,$3,NULLIF($4,0),$5::jsonb) ON CONFLICT(event_key) DO UPDATE SET cancelled_at=NULL,message='',message_scope='',next_part=0,next_attempt_at=NOW(),locked_until=NULL
 WHERE notification_jobs.cancelled_at IS NOT NULL AND notification_jobs.delivered_at IS NULL`, key, recipient.TelegramID, kind, scope, string(payload))
	return err
}

func (s *Store) ClaimNotification(ctx context.Context) (NotificationJob, error) {
	return s.ClaimNotificationFor(ctx, 0)
}

func (s *Store) ClaimNotificationFor(ctx context.Context, recipientID int64) (NotificationJob, error) {
	var job NotificationJob
	var payload []byte
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM notification_jobs WHERE delivered_at IS NULL AND cancelled_at IS NULL
	 AND ($1::bigint=0 OR recipient_telegram_id=$1)
 AND next_attempt_at<=NOW() AND (locked_until IS NULL OR locked_until<NOW())
 ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE notification_jobs j SET locked_until=NOW()+INTERVAL '2 minutes',lease=lease+1
 FROM candidate c WHERE j.id=c.id
	 RETURNING j.id,j.recipient_telegram_id,j.kind,COALESCE(j.department_id,0),j.payload,j.message,j.message_scope,j.next_part,j.attempts,j.lease`, recipientID).Scan(
		&job.ID, &job.RecipientID, &job.Kind, &job.DepartmentID, &payload, &job.Message, &job.MessageScope, &job.NextPart, &job.Attempts, &job.Lease)
	if err != nil {
		return job, err
	}
	err = json.Unmarshal(payload, &job.Payload)
	return job, err
}

// A lease prevents another worker from acknowledging or advancing this job.
func (s *Store) updateNotification(ctx context.Context, job NotificationJob, sql string, args ...any) error {
	params := []any{job.ID, job.Lease}
	params = append(params, args...)
	tag, err := s.Pool.Exec(ctx, sql, params...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) SetNotificationMessage(ctx context.Context, job NotificationJob, message, scope string) error {
	return s.updateNotification(ctx, job, `UPDATE notification_jobs SET message=$3,message_scope=$4,next_part=0 WHERE id=$1 AND lease=$2 AND delivered_at IS NULL AND cancelled_at IS NULL`, message, scope)
}
func (s *Store) AdvanceNotification(ctx context.Context, job NotificationJob, next int) error {
	return s.updateNotification(ctx, job, `UPDATE notification_jobs SET next_part=$3,locked_until=NOW()+INTERVAL '2 minutes' WHERE id=$1 AND lease=$2 AND delivered_at IS NULL AND cancelled_at IS NULL`, next)
}
func (s *Store) CompleteNotification(ctx context.Context, job NotificationJob) error {
	return s.updateNotification(ctx, job, `UPDATE notification_jobs SET delivered_at=NOW(),locked_until=NULL,last_error='' WHERE id=$1 AND lease=$2 AND cancelled_at IS NULL`)
}
func (s *Store) CancelNotification(ctx context.Context, job NotificationJob) error {
	return s.updateNotification(ctx, job, `UPDATE notification_jobs SET cancelled_at=NOW(),locked_until=NULL,last_error='recipient_scope_changed' WHERE id=$1 AND lease=$2 AND delivered_at IS NULL`)
}
func (s *Store) RetryNotification(ctx context.Context, job NotificationJob, code string, delay time.Duration) error {
	return s.updateNotification(ctx, job, `UPDATE notification_jobs SET attempts=attempts+1,next_attempt_at=NOW()+$4::double precision*INTERVAL '1 second',locked_until=NULL,last_error=$3 WHERE id=$1 AND lease=$2 AND delivered_at IS NULL AND cancelled_at IS NULL`, code, delay.Seconds())
}
func (s *Store) WakeNotifications(ctx context.Context, recipientID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE notification_jobs SET next_attempt_at=NOW() WHERE delivered_at IS NULL AND cancelled_at IS NULL AND ($1::bigint=0 OR recipient_telegram_id=$1)`, recipientID)
	return err
}

type DeliveryStatus struct {
	Name, Role, Department     string
	Pending, Delivered, Failed int
	LastError                  string
}

func (s *Store) DeliveryStatuses(ctx context.Context) ([]DeliveryStatus, error) {
	rows, err := s.Pool.Query(ctx, `SELECT e.full_name,e.role,COALESCE(d.name,''),
 count(j.id) FILTER(WHERE j.delivered_at IS NULL AND j.cancelled_at IS NULL),
 count(j.id) FILTER(WHERE j.delivered_at IS NOT NULL),
 count(j.id) FILTER(WHERE j.delivered_at IS NULL AND j.cancelled_at IS NULL AND j.last_error<>''),
 COALESCE((SELECT last_error FROM notification_jobs x WHERE x.recipient_telegram_id=e.telegram_id AND x.delivered_at IS NULL AND x.cancelled_at IS NULL AND x.last_error<>'' ORDER BY x.id DESC LIMIT 1),'')
 FROM employees e LEFT JOIN departments d ON d.id=e.department_id
 LEFT JOIN notification_jobs j ON j.recipient_telegram_id=e.telegram_id
 WHERE e.is_active AND e.role IN ('manager','advisor') GROUP BY e.telegram_id,e.full_name,e.role,d.name ORDER BY e.role,e.full_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DeliveryStatus
	for rows.Next() {
		var entry DeliveryStatus
		if err := rows.Scan(&entry.Name, &entry.Role, &entry.Department, &entry.Pending, &entry.Delivered, &entry.Failed, &entry.LastError); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}
