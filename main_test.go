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

	// Upsert same endpoint+topic should return same ID, created=false.
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

func TestMultiTopicSubscription(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	endpoint := "https://push.example.com/multi"

	// Subscribe same endpoint to two different topics.
	id1, created1, err := UpsertSubscription(db, "topicA", endpoint, "key", "auth")
	if err != nil {
		t.Fatalf("UpsertSubscription topicA: %v", err)
	}
	if !created1 {
		t.Error("expected created=true for topicA")
	}

	id2, created2, err := UpsertSubscription(db, "topicB", endpoint, "key", "auth")
	if err != nil {
		t.Fatalf("UpsertSubscription topicB: %v", err)
	}
	if !created2 {
		t.Error("expected created=true for topicB")
	}
	if id1 == id2 {
		t.Error("expected different IDs for different topics")
	}

	// Both topics should have the subscription.
	subsA, _ := GetSubscriptionsByTopic(db, "topicA")
	subsB, _ := GetSubscriptionsByTopic(db, "topicB")
	if len(subsA) != 1 {
		t.Errorf("expected 1 subscription for topicA, got %d", len(subsA))
	}
	if len(subsB) != 1 {
		t.Errorf("expected 1 subscription for topicB, got %d", len(subsB))
	}

	// Delete only topicA subscription.
	if err := DeleteSubscriptionByEndpoint(db, endpoint, "topicA"); err != nil {
		t.Fatalf("DeleteSubscriptionByEndpoint topicA: %v", err)
	}

	subsA, _ = GetSubscriptionsByTopic(db, "topicA")
	subsB, _ = GetSubscriptionsByTopic(db, "topicB")
	if len(subsA) != 0 {
		t.Errorf("expected 0 subscriptions for topicA after delete, got %d", len(subsA))
	}
	if len(subsB) != 1 {
		t.Errorf("expected 1 subscription for topicB after topicA delete, got %d", len(subsB))
	}

	// Delete all subscriptions for endpoint (no topic).
	// Re-add topicA first.
	UpsertSubscription(db, "topicA", endpoint, "key", "auth")
	if err := DeleteSubscriptionByEndpoint(db, endpoint, ""); err != nil {
		t.Fatalf("DeleteSubscriptionByEndpoint all: %v", err)
	}
	all, _ := GetSubscriptionsByTopic(db, "")
	for _, s := range all {
		if s.Endpoint == endpoint {
			t.Error("expected all subscriptions for endpoint to be deleted")
		}
	}
}

func TestPushPayload(t *testing.T) {
	t.Run("TitleOnly", func(t *testing.T) {
		data, err := pushPayload(NotifyRequest{Title: "Hello"})
		if err != nil {
			t.Fatalf("pushPayload: %v", err)
		}
		var got map[string]any
		json.Unmarshal(data, &got)

		if got["web_push"] != float64(8030) {
			t.Errorf("expected web_push=8030, got %v", got["web_push"])
		}
		notif, ok := got["notification"].(map[string]any)
		if !ok {
			t.Fatal("expected notification object")
		}
		if notif["title"] != "Hello" {
			t.Errorf("expected title=Hello, got %v", notif["title"])
		}
		// Optional fields should be absent.
		for _, key := range []string{"body", "icon", "badge", "tag", "lang", "silent", "data"} {
			if _, exists := notif[key]; exists {
				t.Errorf("expected %q to be absent, but it was present", key)
			}
		}
	})

	t.Run("AllFields", func(t *testing.T) {
		silent := true
		data, err := pushPayload(NotifyRequest{
			Title:  "Title",
			Body:   "Body",
			Icon:   "/icon.png",
			Badge:  "/badge.png",
			Tag:    "msg-1",
			Lang:   "en",
			Silent: &silent,
			Data:   map[string]any{"url": "/page"},
		})
		if err != nil {
			t.Fatalf("pushPayload: %v", err)
		}
		var got map[string]any
		json.Unmarshal(data, &got)

		if got["web_push"] != float64(8030) {
			t.Errorf("expected web_push=8030, got %v", got["web_push"])
		}
		notif := got["notification"].(map[string]any)
		if notif["title"] != "Title" {
			t.Errorf("title: got %v", notif["title"])
		}
		if notif["body"] != "Body" {
			t.Errorf("body: got %v", notif["body"])
		}
		if notif["icon"] != "/icon.png" {
			t.Errorf("icon: got %v", notif["icon"])
		}
		if notif["badge"] != "/badge.png" {
			t.Errorf("badge: got %v", notif["badge"])
		}
		if notif["tag"] != "msg-1" {
			t.Errorf("tag: got %v", notif["tag"])
		}
		if notif["lang"] != "en" {
			t.Errorf("lang: got %v", notif["lang"])
		}
		if notif["silent"] != true {
			t.Errorf("silent: got %v", notif["silent"])
		}
		dataObj := notif["data"].(map[string]any)
		if dataObj["url"] != "/page" {
			t.Errorf("data.url: got %v", dataObj["url"])
		}
	})
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

	// POST /topics/{topic}/notify — public topic notify
	t.Run("TopicNotify", func(t *testing.T) {
		// Subscribe to topic "test" (already created above, but create fresh one to be safe).
		payload := `{"topic":"topictest","subscription":{"endpoint":"https://push.example.com/topictest","keys":{"p256dh":"dGVzdA","auth":"dGVzdA"}}}`
		resp, err := client.Post(ts.URL+"/subscriptions", "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("POST /subscriptions: %v", err)
		}
		resp.Body.Close()

		// Send notification via public topic endpoint.
		notifyPayload := `{"title":"Hello topic"}`
		resp, err = client.Post(ts.URL+"/topics/topictest/notify", "application/json", strings.NewReader(notifyPayload))
		if err != nil {
			t.Fatalf("POST /topics/topictest/notify: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var result struct {
			Sent         int `json:"sent"`
			Failed       int `json:"failed"`
			StaleRemoved int `json:"stale_removed"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		// The push will fail (fake endpoint) but we should get a valid response structure.
		if result.Sent+result.Failed+result.StaleRemoved < 1 {
			t.Errorf("expected at least 1 delivery attempt, got sent=%d failed=%d stale_removed=%d", result.Sent, result.Failed, result.StaleRemoved)
		}
	})

	// POST /topics//notify — missing topic returns 404 (no route match)
	t.Run("TopicNotifyNoTopic", func(t *testing.T) {
		resp, err := client.Post(ts.URL+"/topics//notify", "application/json", strings.NewReader(`{"title":"x"}`))
		if err != nil {
			t.Fatalf("POST /topics//notify: %v", err)
		}
		defer resp.Body.Close()
		// Go's ServeMux won't match an empty {topic} segment, so 404.
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", resp.StatusCode)
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
		resp2, err := client.Do(req2)
		if err != nil {
			t.Fatalf("GET /subscriptions (verify delete): %v", err)
		}
		defer resp2.Body.Close()
		var body struct {
			Subscriptions []Subscription `json:"subscriptions"`
		}
		json.NewDecoder(resp2.Body).Decode(&body)
		// The "topictest" subscription from TopicNotify test still exists.
		for _, sub := range body.Subscriptions {
			if sub.Endpoint == "https://push.example.com/test" {
				t.Error("expected deleted subscription to be gone")
			}
		}
	})
}
