package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server holds shared dependencies for all HTTP handlers.
type Server struct {
	DB              *sql.DB
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDContact    string
	AdminKey        string
	WelcomeMessage  string
	WG              sync.WaitGroup
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// HandleGetVAPIDPublicKey returns the server's VAPID public key.
func (s *Server) HandleGetVAPIDPublicKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"vapidPublicKey": s.VAPIDPublicKey})
}

// HandlePostSubscription registers or updates a push subscription.
func (s *Server) HandlePostSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Topic        string `json:"topic"`
		Subscription struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256dh string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
		} `json:"subscription"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.Subscription.Endpoint == "" || body.Subscription.Keys.P256dh == "" || body.Subscription.Keys.Auth == "" {
		writeError(w, http.StatusBadRequest, "subscription.endpoint, subscription.keys.p256dh, and subscription.keys.auth are required")
		return
	}

	id, created, err := UpsertSubscription(s.DB, body.Topic, body.Subscription.Endpoint, body.Subscription.Keys.P256dh, body.Subscription.Keys.Auth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save subscription")
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]string{"id": id})

	if created && s.WelcomeMessage != "" {
		sub := Subscription{
			ID:        id,
			Endpoint:  body.Subscription.Endpoint,
			KeyP256dh: body.Subscription.Keys.P256dh,
			KeyAuth:   body.Subscription.Keys.Auth,
		}
		s.WG.Add(1)
		go func() {
			defer s.WG.Done()
			time.Sleep(1 * time.Second)
			sendToSubscriptions(s.DB, []Subscription{sub}, NotifyRequest{Title: s.WelcomeMessage}, s.VAPIDPublicKey, s.VAPIDPrivateKey, s.VAPIDContact)
		}()
	}
}

// HandleDeleteSubscriptionByEndpoint removes a subscription by endpoint (public).
func (s *Server) HandleDeleteSubscriptionByEndpoint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}

	if err := DeleteSubscriptionByEndpoint(s.DB, body.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete subscription")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleListSubscriptions returns all subscriptions (admin, no keys).
func (s *Server) HandleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	topic := r.URL.Query().Get("topic")
	subs, err := ListSubscriptionsAdmin(s.DB, topic)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}
	if subs == nil {
		subs = []Subscription{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

// HandleDeleteSubscriptionByID removes a subscription by ID (admin).
func (s *Server) HandleDeleteSubscriptionByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "subscription id is required")
		return
	}

	if err := DeleteSubscriptionByID(s.DB, id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete subscription")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleNotify sends push notifications to matching subscriptions (admin).
func (s *Server) HandleNotify(w http.ResponseWriter, r *http.Request) {
	var req NotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	result := SendNotifications(s.DB, req, s.VAPIDPublicKey, s.VAPIDPrivateKey, s.VAPIDContact, &s.WG)
	writeJSON(w, http.StatusOK, result)
}

// HandleTopicNotify sends push notifications to a topic's subscribers (public).
// The topic name acts as a capability token — knowing the topic grants permission to notify it.
func (s *Server) HandleTopicNotify(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	if topic == "" {
		writeError(w, http.StatusBadRequest, "topic is required")
		return
	}

	var req NotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	req.Topic = topic
	result := SendNotifications(s.DB, req, s.VAPIDPublicKey, s.VAPIDPrivateKey, s.VAPIDContact, &s.WG)
	writeJSON(w, http.StatusOK, result)
}

var durationRe = regexp.MustCompile(`^(\d+)([dhm])$`)

func parseDuration(s string) (time.Duration, error) {
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 30d, 24h, 60m)", s)
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	}
	return 0, fmt.Errorf("invalid duration unit %q", m[2])
}

// HandlePurgeDeliveryLog deletes old delivery log entries (admin).
func (s *Server) HandlePurgeDeliveryLog(w http.ResponseWriter, r *http.Request) {
	olderThan := r.URL.Query().Get("older_than")
	if olderThan == "" {
		olderThan = "30d"
	}

	dur, err := parseDuration(olderThan)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	deleted, err := PurgeDeliveryLog(s.DB, dur)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to purge delivery log")
		return
	}

	writeJSON(w, http.StatusOK, map[string]int64{"deleted": deleted})
}

// requireAuth wraps a handler with bearer token authentication.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.AdminKey {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}
