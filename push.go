package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// NotifyRequest is the JSON body for POST /notify.
type NotifyRequest struct {
	Topic string `json:"topic"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Icon  string `json:"icon,omitempty"`
	Badge string `json:"badge,omitempty"`
	Tag   string `json:"tag,omitempty"`
	URL   string `json:"url,omitempty"`
}

// NotifyResult is the JSON response for POST /notify.
type NotifyResult struct {
	Sent         int `json:"sent"`
	Failed       int `json:"failed"`
	StaleRemoved int `json:"stale_removed"`
}

// pushPayload builds the JSON payload sent to the browser's service worker.
func pushPayload(req NotifyRequest) ([]byte, error) {
	payload := map[string]any{
		"title": req.Title,
	}
	if req.Body != "" {
		payload["body"] = req.Body
	}
	if req.Icon != "" {
		payload["icon"] = req.Icon
	}
	if req.Badge != "" {
		payload["badge"] = req.Badge
	}
	if req.Tag != "" {
		payload["tag"] = req.Tag
	}
	if req.URL != "" {
		payload["data"] = map[string]string{"url": req.URL}
	}
	return json.Marshal(payload)
}

const pushConcurrency = 10

// SendNotifications fetches subscriptions by topic and delivers to all of them.
// It uses context.Background() so delivery survives HTTP request cancellation.
// The provided wg is incremented/decremented for graceful shutdown tracking.
func SendNotifications(db *sql.DB, req NotifyRequest, vapidPublicKey, vapidPrivateKey, vapidContact string, wg *sync.WaitGroup) NotifyResult {
	wg.Add(1)
	defer wg.Done()

	subs, err := GetSubscriptionsByTopic(db, req.Topic)
	if err != nil {
		log.Printf("error fetching subscriptions: %v", err)
		return NotifyResult{}
	}

	return sendToSubscriptions(db, subs, req, vapidPublicKey, vapidPrivateKey, vapidContact)
}

// sendToSubscriptions fans out push delivery to the given subscriptions.
func sendToSubscriptions(db *sql.DB, subs []Subscription, req NotifyRequest, vapidPublicKey, vapidPrivateKey, vapidContact string) NotifyResult {
	payload, err := pushPayload(req)
	if err != nil {
		log.Printf("error building push payload: %v", err)
		return NotifyResult{}
	}

	type result struct {
		sent         bool
		staleRemoved bool
	}

	results := make(chan result, len(subs))
	sem := make(chan struct{}, pushConcurrency)

	for _, sub := range subs {
		sem <- struct{}{} // acquire slot
		go func(s Subscription) {
			defer func() { <-sem }() // release slot

			wpSub := &webpush.Subscription{
				Endpoint: s.Endpoint,
				Keys: webpush.Keys{
					P256dh: s.KeyP256dh,
					Auth:   s.KeyAuth,
				},
			}

			resp, err := webpush.SendNotification(payload, wpSub, &webpush.Options{
				VAPIDPublicKey:  vapidPublicKey,
				VAPIDPrivateKey: vapidPrivateKey,
				Subscriber:      vapidContact,
				TTL:             86400,
			})

			var statusCode int
			var errMsg string
			if err != nil {
				errMsg = err.Error()
				statusCode = 0
			} else {
				statusCode = resp.StatusCode
				resp.Body.Close()
			}

			// Log delivery attempt.
			if logErr := LogDelivery(db, s.ID, statusCode, errMsg); logErr != nil {
				log.Printf("error logging delivery for %s: %v", s.ID, logErr)
			}

			// Remove stale subscriptions (404 or 410).
			stale := statusCode == http.StatusNotFound || statusCode == http.StatusGone
			if stale {
				if delErr := DeleteSubscriptionByID(db, s.ID); delErr != nil {
					log.Printf("error deleting stale subscription %s: %v", s.ID, delErr)
				}
			}

			sent := err == nil && statusCode >= 200 && statusCode < 300
			results <- result{sent: sent, staleRemoved: stale}
		}(sub)
	}

	var nr NotifyResult
	for range len(subs) {
		r := <-results
		if r.sent {
			nr.Sent++
		} else if !r.staleRemoved {
			nr.Failed++
		}
		if r.staleRemoved {
			nr.StaleRemoved++
			nr.Failed++ // stale also counts as failed delivery
		}
	}

	fmt.Printf("notify topic=%q: sent=%d failed=%d stale_removed=%d\n", req.Topic, nr.Sent, nr.Failed, nr.StaleRemoved)
	return nr
}
