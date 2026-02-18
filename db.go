package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// OpenDB opens (or creates) a SQLite database at path with WAL mode and
// busy timeout, runs migrations, and returns the *sql.DB handle.
func OpenDB(path string) (*sql.DB, error) {
	// Ensure parent directory exists.
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS subscriptions (
			id         TEXT PRIMARY KEY,
			topic      TEXT NOT NULL DEFAULT '',
			endpoint   TEXT NOT NULL,
			key_p256dh TEXT NOT NULL,
			key_auth   TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(endpoint)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_subscriptions_topic ON subscriptions(topic)`,
		`CREATE TABLE IF NOT EXISTS delivery_log (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			subscription_id TEXT NOT NULL,
			sent_at         TEXT NOT NULL DEFAULT (datetime('now')),
			status_code     INTEGER NOT NULL,
			error           TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_delivery_log_sent_at ON delivery_log(sent_at)`,
	}
	for _, s := range statements {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("exec %q: %w", s[:40], err)
		}
	}
	return nil
}

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Subscription represents a stored push subscription.
type Subscription struct {
	ID        string `json:"id"`
	Topic     string `json:"topic"`
	Endpoint  string `json:"endpoint"`
	KeyP256dh string `json:"key_p256dh,omitempty"`
	KeyAuth   string `json:"key_auth,omitempty"`
	CreatedAt string `json:"created_at"`
}

// UpsertSubscription inserts or updates a subscription by endpoint.
// Returns the subscription ID and whether it was newly created.
func UpsertSubscription(db *sql.DB, topic, endpoint, p256dh, auth string) (id string, created bool, err error) {
	newID := randomID()

	result, err := db.Exec(`
		INSERT INTO subscriptions (id, topic, endpoint, key_p256dh, key_auth)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET
			topic = excluded.topic,
			key_p256dh = excluded.key_p256dh,
			key_auth = excluded.key_auth
	`, newID, topic, endpoint, p256dh, auth)
	if err != nil {
		return "", false, fmt.Errorf("upsert subscription: %w", err)
	}

	rows, _ := result.RowsAffected()
	// With ON CONFLICT DO UPDATE, RowsAffected is always 1.
	// Check if our newID was actually inserted by querying back.
	var actualID string
	err = db.QueryRow(`SELECT id FROM subscriptions WHERE endpoint = ?`, endpoint).Scan(&actualID)
	if err != nil {
		return "", false, fmt.Errorf("lookup subscription id: %w", err)
	}

	created = (actualID == newID) && rows > 0
	return actualID, created, nil
}

// GetSubscriptionsByTopic returns subscriptions matching the given topic.
// If topic is empty, returns all subscriptions.
func GetSubscriptionsByTopic(db *sql.DB, topic string) ([]Subscription, error) {
	var rows *sql.Rows
	var err error
	if topic == "" {
		rows, err = db.Query(`SELECT id, topic, endpoint, key_p256dh, key_auth, created_at FROM subscriptions`)
	} else {
		rows, err = db.Query(`SELECT id, topic, endpoint, key_p256dh, key_auth, created_at FROM subscriptions WHERE topic = ?`, topic)
	}
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.Topic, &s.Endpoint, &s.KeyP256dh, &s.KeyAuth, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

// DeleteSubscriptionByEndpoint removes a subscription by its endpoint URL.
func DeleteSubscriptionByEndpoint(db *sql.DB, endpoint string) error {
	_, err := db.Exec(`DELETE FROM subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// DeleteSubscriptionByID removes a subscription by its ID.
func DeleteSubscriptionByID(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM subscriptions WHERE id = ?`, id)
	return err
}

// LogDelivery records a delivery attempt in the delivery_log table.
func LogDelivery(db *sql.DB, subscriptionID string, statusCode int, errMsg string) error {
	_, err := db.Exec(`INSERT INTO delivery_log (subscription_id, status_code, error) VALUES (?, ?, ?)`,
		subscriptionID, statusCode, errMsg)
	return err
}

// PurgeDeliveryLog deletes delivery_log entries older than the given duration.
// Returns the number of rows deleted.
func PurgeDeliveryLog(db *sql.DB, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format("2006-01-02 15:04:05")
	result, err := db.Exec(`DELETE FROM delivery_log WHERE sent_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListSubscriptionsAdmin returns subscriptions for the admin listing (no keys).
func ListSubscriptionsAdmin(db *sql.DB, topic string) ([]Subscription, error) {
	var rows *sql.Rows
	var err error
	if topic == "" {
		rows, err = db.Query(`SELECT id, topic, endpoint, created_at FROM subscriptions`)
	} else {
		rows, err = db.Query(`SELECT id, topic, endpoint, created_at FROM subscriptions WHERE topic = ?`, topic)
	}
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.Topic, &s.Endpoint, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}
