package main

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
	"golang.org/x/crypto/blake2b"
)

// NotifyRequest is the JSON body for POST /notify.
type NotifyRequest struct {
	Topic  string         `json:"topic"`
	Title  string         `json:"title"`
	Body   string         `json:"body"`
	Icon   string         `json:"icon,omitempty"`
	Badge  string         `json:"badge,omitempty"`
	Tag    string         `json:"tag,omitempty"`
	Lang   string         `json:"lang,omitempty"`
	Silent *bool          `json:"silent,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
	Legacy bool           `json:"legacy,omitempty"`
	// Mutable, when present, sets the top-level "mutable" flag on the declarative
	// payload, which makes Safari also fire the service worker push event (so it
	// can localize/transform the notification or run custom logic). A pointer so
	// the flag is emitted only when the caller explicitly includes it.
	Mutable *bool `json:"mutable,omitempty"`
}

// NotifyResult is the JSON response for POST /notify.
type NotifyResult struct {
	Sent         int `json:"sent"`
	Failed       int `json:"failed"`
	StaleRemoved int `json:"stale_removed"`
}

// pushPayload builds the JSON payload sent to the browser.
//
// By default it uses the Declarative Web Push format (RFC 8030) so that
// Safari 18.4+ can display the notification natively without waking the
// service worker.  Other browsers ignore the "web_push" key; the service
// worker unwraps payload.notification to extract the fields.
//
// When req.Legacy is true, the payload omits the "web_push" key so the
// service worker is always woken up to handle the push event.
//
// In declarative mode the notification gets a "navigate" member (the URL Safari
// opens on click, required by the spec), derived from data.url and defaulting to
// "/". The same URL stays in data.url for the service-worker path used by other
// browsers. If req.Mutable is set, the top-level "mutable" flag is emitted.
func pushPayload(req NotifyRequest) ([]byte, error) {
	notification := map[string]any{
		"title": req.Title,
	}
	if req.Body != "" {
		notification["body"] = req.Body
	}
	if req.Icon != "" {
		notification["icon"] = req.Icon
	}
	if req.Badge != "" {
		notification["badge"] = req.Badge
	}
	if req.Tag != "" {
		notification["tag"] = req.Tag
	}
	if req.Lang != "" {
		notification["lang"] = req.Lang
	}
	if req.Silent != nil {
		notification["silent"] = *req.Silent
	}
	if len(req.Data) > 0 {
		notification["data"] = req.Data
	}
	if req.Legacy {
		return json.Marshal(notification)
	}
	navigate := "/"
	if u, ok := req.Data["url"].(string); ok && u != "" {
		navigate = u
	}
	notification["navigate"] = navigate
	payload := map[string]any{
		"web_push":     8030,
		"notification": notification,
	}
	if req.Mutable != nil {
		payload["mutable"] = *req.Mutable
	}
	return json.Marshal(payload)
}

// collapseTopic derives an RFC 8030 Topic header value from a notification tag.
//
// The Topic header de-dupes messages that are still QUEUED at the push service:
// a newer message with the same Topic replaces an earlier, still-undelivered one
// (e.g. several notifications sent while the device is offline collapse to the
// latest on reconnect). It does NOT merge a notification that has already been
// delivered and displayed — that is the job of the notification `tag`, applied
// by the user agent. On Apple's gateway this Topic maps to apns-collapse-id, but
// note the displayed-notification merge by `tag` is currently broken on Safari /
// iOS (WebKit bug 258922), so on those platforms same-tag notifications received
// while online still stack. This header only helps the offline-backlog case. It
// mirrors the in-payload `tag` so the two stay consistent.
//
// Note: this is unrelated to NotifyRequest.Topic, which is this server's routing
// key (which subscriptions to fan out to). The collapse key is derived only from
// the tag. The header is constrained to <=32 base64url-safe characters and is set
// verbatim by the library (no validation), so we hash to a guaranteed-valid token
// rather than passing user input through. blake2b size 8 -> 16 hex chars.
func collapseTopic(tag string) string {
	if tag == "" {
		return ""
	}
	h, _ := blake2b.New(8, nil)
	h.Write([]byte(tag))
	return hex.EncodeToString(h.Sum(nil))
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
				Topic:           collapseTopic(req.Tag),
				TTL:             86400,
				Urgency:         webpush.UrgencyHigh,
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
