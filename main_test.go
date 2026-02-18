package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateAndParseVAPIDKeys(t *testing.T) {
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}
	if pub == "" || priv == "" {
		t.Fatal("expected non-empty keys")
	}

	key, err := ParseVAPIDKeys(pub, priv)
	if err != nil {
		t.Fatalf("ParseVAPIDKeys: %v", err)
	}
	if key.PublicKey.X == nil || key.PublicKey.Y == nil {
		t.Fatal("parsed key has nil coordinates")
	}
}

func TestOpenDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Verify the subscriptions table exists.
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='subscriptions'`).Scan(&name)
	if err != nil {
		t.Fatalf("subscriptions table not found: %v", err)
	}
}

func TestUpsertSubscription(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// First insert should be created.
	id1, created, err := UpsertSubscription(db, "news", "https://push.example.com/sub1", "p256dh-key", "auth-key")
	if err != nil {
		t.Fatalf("UpsertSubscription (insert): %v", err)
	}
	if !created {
		t.Error("expected created=true for new subscription")
	}
	if id1 == "" {
		t.Error("expected non-empty id")
	}

	// Upsert same endpoint should return same ID, created=false.
	id2, created, err := UpsertSubscription(db, "news", "https://push.example.com/sub1", "p256dh-key-updated", "auth-key-updated")
	if err != nil {
		t.Fatalf("UpsertSubscription (update): %v", err)
	}
	if created {
		t.Error("expected created=false for existing subscription")
	}
	if id2 != id1 {
		t.Errorf("expected same id %q, got %q", id1, id2)
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}

	return &Server{
		DB:              db,
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		VAPIDContact:    "mailto:test@example.com",
		AdminKey:        "test-admin-key",
	}
}

func TestHandlers(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.NewRouter("*"))
	defer ts.Close()

	client := ts.Client()

	// GET /vapid-public-key
	t.Run("GetVAPIDPublicKey", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/vapid-public-key")
		if err != nil {
			t.Fatalf("GET /vapid-public-key: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["vapidPublicKey"] != srv.VAPIDPublicKey {
			t.Errorf("expected vapidPublicKey=%q, got %q", srv.VAPIDPublicKey, body["vapidPublicKey"])
		}
	})

	// POST /subscriptions — create a subscription
	t.Run("PostSubscription", func(t *testing.T) {
		payload := `{"topic":"test","subscription":{"endpoint":"https://push.example.com/test","keys":{"p256dh":"dGVzdA","auth":"dGVzdA"}}}`
		resp, err := client.Post(ts.URL+"/subscriptions", "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("POST /subscriptions: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.StatusCode)
		}
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["id"] == "" {
			t.Error("expected non-empty id in response")
		}
	})

	// GET /subscriptions — requires admin auth
	t.Run("ListSubscriptionsUnauthorized", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/subscriptions")
		if err != nil {
			t.Fatalf("GET /subscriptions: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("ListSubscriptionsAuthorized", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"/subscriptions", nil)
		req.Header.Set("Authorization", "Bearer test-admin-key")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /subscriptions (auth): %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var body struct {
			Subscriptions []Subscription `json:"subscriptions"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		if len(body.Subscriptions) != 1 {
			t.Fatalf("expected 1 subscription, got %d", len(body.Subscriptions))
		}
	})

	// DELETE /subscriptions — remove by endpoint
	t.Run("DeleteSubscription", func(t *testing.T) {
		payload := `{"endpoint":"https://push.example.com/test"}`
		req, _ := http.NewRequest("DELETE", ts.URL+"/subscriptions", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("DELETE /subscriptions: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", resp.StatusCode)
		}

		// Verify it's gone.
		req2, _ := http.NewRequest("GET", ts.URL+"/subscriptions", nil)
		req2.Header.Set("Authorization", "Bearer test-admin-key")
		resp2, _ := client.Do(req2)
		defer resp2.Body.Close()
		var body struct {
			Subscriptions []Subscription `json:"subscriptions"`
		}
		json.NewDecoder(resp2.Body).Decode(&body)
		if len(body.Subscriptions) != 0 {
			t.Errorf("expected 0 subscriptions after delete, got %d", len(body.Subscriptions))
		}
	})
}
