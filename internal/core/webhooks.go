package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/netpolicy"
	"github.com/TorstenDittmann/sbx/internal/store"
	"github.com/TorstenDittmann/sbx/internal/version"
)

// EventWebhookTest is the type of the ping sent by POST /v1/webhooks/{id}/test.
// It is never stored and never sent to other webhooks.
const EventWebhookTest = "webhook.test"

// EventTypes lists every lifecycle event a webhook can subscribe to.
var EventTypes = []string{
	EventCreated, EventFailed, EventDeleted, EventExpired,
	EventPaused, EventResumed, EventHibernated, EventNetworkUpdated, EventOOM, EventLost,
}

const (
	maxWebhookURL         = 2048
	maxWebhookDescription = 256
	// webhookBatch is how many events one delivery run reads at a time.
	webhookBatch = 100
	// webhookWorkers caps concurrent deliveries across every webhook.
	webhookWorkers = 8
)

// defaultWebhookBackoff is the wait before each retry of a failed event. An
// event that still fails after the last retry disables the webhook, so a dead
// endpoint does not hold the queue forever. The total is just under 4 hours.
var defaultWebhookBackoff = []time.Duration{
	5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
	30 * time.Minute, time.Hour, 2 * time.Hour,
}

// EventPayload is the JSON body of a lifecycle event, shared by the event
// stream and webhooks.
type EventPayload struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	SandboxID string         `json:"sandbox_id"`
	Time      time.Time      `json:"time"`
	Data      map[string]any `json:"data"`
}

// NewEventPayload converts a stored event.
func NewEventPayload(ev store.Event) EventPayload {
	return EventPayload{ID: ev.Seq, Type: ev.Type, SandboxID: ev.SandboxID, Time: ev.Time, Data: ev.Data}
}

// WebhookInput creates a webhook. Events empty means every event type.
type WebhookInput struct {
	URL         string
	Description string
	Events      []string
}

// WebhookPatch changes a webhook. Nil fields stay as they are.
type WebhookPatch struct {
	URL         *string
	Description *string
	Events      *[]string
	Enabled     *bool
}

// WebhookTestResult is the outcome of a test ping.
type WebhookTestResult struct {
	OK         bool
	StatusCode int
	Error      string
	Duration   time.Duration
}

// webhookState is the dispatcher's in-memory view of each webhook: whether a
// delivery is running, and when a failed event may be retried. Only the
// cursor and the last outcome are stored, so a restart retries at once.
type webhookState struct {
	mu      sync.Mutex
	busy    map[string]bool
	retry   map[string]webhookRetry
	backoff []time.Duration
	client  *http.Client
	sem     chan struct{}
	// kick wakes the dispatcher when a retry falls due.
	kick chan struct{}
}

type webhookRetry struct {
	attempts int
	next     time.Time
}

func newWebhookState() *webhookState {
	return &webhookState{
		busy:    map[string]bool{},
		retry:   map[string]webhookRetry{},
		backoff: defaultWebhookBackoff,
		sem:     make(chan struct{}, webhookWorkers),
		kick:    make(chan struct{}, 1),
	}
}

// SetWebhookBackoff replaces the retry schedule. Tests use it.
func (m *Manager) SetWebhookBackoff(backoff []time.Duration) {
	m.wh.mu.Lock()
	m.wh.backoff = append([]time.Duration{}, backoff...)
	m.wh.mu.Unlock()
}

// CreateWebhook registers a URL for the caller's lifecycle events. The
// returned webhook carries its signing secret; it starts after the newest
// event, so only events from now on are sent.
func (m *Manager) CreateWebhook(ctx context.Context, actor Actor, in WebhookInput) (store.Webhook, error) {
	limit := m.cfg.Webhooks.MaxPerKey
	if limit == 0 {
		return store.Webhook{}, unsupported("webhooks are disabled on this server")
	}
	rawURL, err := m.validWebhookURL(in.URL)
	if err != nil {
		return store.Webhook{}, err
	}
	events, err := validWebhookEvents(in.Events)
	if err != nil {
		return store.Webhook{}, err
	}
	if len(in.Description) > maxWebhookDescription {
		return store.Webhook{}, invalid(fmt.Sprintf("description is longer than %d bytes", maxWebhookDescription))
	}
	n, err := m.store.CountWebhooks(ctx, actor.KeyID)
	if err != nil {
		return store.Webhook{}, err
	}
	if n >= limit {
		return store.Webhook{}, quota(fmt.Sprintf("this key already has %d webhooks", n))
	}
	cursor, err := m.store.LastEventSeq(ctx)
	if err != nil {
		return store.Webhook{}, err
	}
	now := m.now()
	wh := store.Webhook{
		ID: id.Webhook(), OwnerKeyID: actor.KeyID, URL: rawURL, Description: in.Description,
		Events: events, Secret: id.WebhookSecret(), Enabled: true, Cursor: cursor,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.InsertWebhook(ctx, wh); err != nil {
		return store.Webhook{}, err
	}
	return wh, nil
}

// ListWebhooks returns the caller's webhooks, or every webhook for an admin.
func (m *Manager) ListWebhooks(ctx context.Context, actor Actor) ([]store.Webhook, error) {
	owner := ""
	if !actor.Admin {
		owner = actor.KeyID
	}
	return m.store.ListWebhooks(ctx, owner)
}

// GetWebhook loads one webhook the caller may see.
func (m *Manager) GetWebhook(ctx context.Context, actor Actor, webhookID string) (store.Webhook, error) {
	wh, err := m.store.GetWebhook(ctx, webhookID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !actor.Admin && wh.OwnerKeyID != actor.KeyID) {
		return store.Webhook{}, notFound("webhook not found")
	}
	return wh, err
}

// UpdateWebhook applies a patch. Re-enabling a webhook resumes after the
// last event it handled, so events that arrived while it was off are sent
// as long as they are still kept.
func (m *Manager) UpdateWebhook(ctx context.Context, actor Actor, webhookID string, p WebhookPatch) (store.Webhook, error) {
	wh, err := m.GetWebhook(ctx, actor, webhookID)
	if err != nil {
		return store.Webhook{}, err
	}
	if p.URL != nil {
		if wh.URL, err = m.validWebhookURL(*p.URL); err != nil {
			return store.Webhook{}, err
		}
	}
	if p.Description != nil {
		if len(*p.Description) > maxWebhookDescription {
			return store.Webhook{}, invalid(fmt.Sprintf("description is longer than %d bytes", maxWebhookDescription))
		}
		wh.Description = *p.Description
	}
	if p.Events != nil {
		if wh.Events, err = validWebhookEvents(*p.Events); err != nil {
			return store.Webhook{}, err
		}
	}
	if p.Enabled != nil {
		wh.Enabled = *p.Enabled
	}
	wh.UpdatedAt = m.now()
	if err := m.store.UpdateWebhook(ctx, wh); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Webhook{}, notFound("webhook not found")
		}
		return store.Webhook{}, err
	}
	m.clearWebhookRetry(wh.ID)
	return m.store.GetWebhook(ctx, wh.ID)
}

// RotateWebhookSecret replaces the signing secret and returns the webhook
// with the new one. Deliveries already in flight may still use the old one.
func (m *Manager) RotateWebhookSecret(ctx context.Context, actor Actor, webhookID string) (store.Webhook, error) {
	wh, err := m.GetWebhook(ctx, actor, webhookID)
	if err != nil {
		return store.Webhook{}, err
	}
	wh.Secret = id.WebhookSecret()
	wh.UpdatedAt = m.now()
	if err := m.store.UpdateWebhook(ctx, wh); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Webhook{}, notFound("webhook not found")
		}
		return store.Webhook{}, err
	}
	return wh, nil
}

// DeleteWebhook removes a webhook. A delivery in flight may still finish.
func (m *Manager) DeleteWebhook(ctx context.Context, actor Actor, webhookID string) error {
	if _, err := m.GetWebhook(ctx, actor, webhookID); err != nil {
		return err
	}
	if err := m.store.DeleteWebhook(ctx, webhookID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("webhook not found")
		}
		return err
	}
	m.clearWebhookRetry(webhookID)
	return nil
}

// TestWebhook sends a signed webhook.test event right away and reports the
// response. It does not touch the cursor or the delivery status.
func (m *Manager) TestWebhook(ctx context.Context, actor Actor, webhookID string) (WebhookTestResult, error) {
	wh, err := m.GetWebhook(ctx, actor, webhookID)
	if err != nil {
		return WebhookTestResult{}, err
	}
	body, err := json.Marshal(EventPayload{
		Type: EventWebhookTest, Time: m.now(), Data: map[string]any{"webhook_id": wh.ID},
	})
	if err != nil {
		return WebhookTestResult{}, err
	}
	start := time.Now()
	status, err := m.sendWebhook(ctx, wh, "test_"+strings.TrimPrefix(id.Webhook(), "wh_"), body)
	res := WebhookTestResult{OK: err == nil, StatusCode: status, Duration: time.Since(start)}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

func (m *Manager) validWebhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", invalid("url is required")
	}
	if len(raw) > maxWebhookURL {
		return "", invalid(fmt.Sprintf("url is longer than %d bytes", maxWebhookURL))
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return "", invalid("url must be an absolute http or https URL")
	}
	if u.Fragment != "" {
		return "", invalid("url must not have a fragment")
	}
	if addr, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil && m.webhookBlocked(addr) {
		return "", invalid("url points at a private or metadata address; see webhooks.allow_private")
	}
	return u.String(), nil
}

func validWebhookEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(events))
	for _, ev := range events {
		known := false
		for _, t := range EventTypes {
			if ev == t {
				known = true
				break
			}
		}
		if !known {
			return nil, invalid(fmt.Sprintf("unknown event type %q", ev))
		}
		if !seen[ev] {
			seen[ev] = true
			out = append(out, ev)
		}
	}
	return out, nil
}

func webhookWants(wh store.Webhook, typ string) bool {
	if len(wh.Events) == 0 {
		return true
	}
	for _, t := range wh.Events {
		if t == typ {
			return true
		}
	}
	return false
}

// webhookBlocked applies the operator baseline to a delivery address. It
// runs on the address actually dialed, so DNS cannot point a public name at
// a private one after the URL was accepted.
func (m *Manager) webhookBlocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip.IsLoopback() && m.cfg.Webhooks.AllowPrivate {
		// ::1 sits with the metadata ranges for sandboxes; for webhooks it
		// is loopback like 127.0.0.1.
		return false
	}
	base := netpolicy.Baseline{BlockPrivate: !m.cfg.Webhooks.AllowPrivate, BlockMetadata: true}
	for _, raw := range m.cfg.Network.PrivateAllow {
		if p, err := netip.ParsePrefix(raw); err == nil {
			base.PrivateAllow = append(base.PrivateAllow, p)
		} else if a, err := netip.ParseAddr(raw); err == nil {
			base.PrivateAllow = append(base.PrivateAllow, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return netpolicy.Blocked(ip, base)
}

// webhookClient dials only addresses the baseline allows, ignores proxy
// settings so the check sees the real destination, and follows no
// redirects: a 3xx is a failed delivery.
func (m *Manager) webhookClient() *http.Client {
	m.wh.mu.Lock()
	defer m.wh.mu.Unlock()
	if m.wh.client != nil {
		return m.wh.client
	}
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			if m.webhookBlocked(ip) {
				return fmt.Errorf("webhook destination %s is blocked", ip)
			}
			return nil
		},
	}
	m.wh.client = &http.Client{
		Timeout: time.Duration(m.cfg.Webhooks.TimeoutSeconds) * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: time.Duration(m.cfg.Webhooks.TimeoutSeconds) * time.Second,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return m.wh.client
}

// SignWebhook returns the Standard Webhooks signature header value for one
// message: "v1," and the base64 HMAC-SHA256 of "<msgID>.<timestamp>.<body>",
// keyed with the base64-decoded part of the secret after "whsec_".
func SignWebhook(secret, msgID string, ts int64, body []byte) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return "", fmt.Errorf("invalid webhook secret: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s.%d.", msgID, ts)
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// sendWebhook POSTs one signed message. Any 2xx is a success.
func (m *Manager) sendWebhook(ctx context.Context, wh store.Webhook, msgID string, body []byte) (int, error) {
	ts := m.now().Unix()
	sig, err := SignWebhook(wh.Secret, msgID, ts, body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sbx-webhooks/"+version.Version)
	req.Header.Set("webhook-id", msgID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("webhook-signature", sig)
	resp, err := m.webhookClient().Do(req)
	if err != nil {
		return 0, scrubURLError(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("endpoint answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// scrubURLError drops the URL from client errors, since it may carry
// credentials, and keeps the cause.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("request timed out")
		}
		return ue.Err
	}
	return err
}

func (m *Manager) clearWebhookRetry(webhookID string) {
	m.wh.mu.Lock()
	delete(m.wh.retry, webhookID)
	m.wh.mu.Unlock()
}

// RunWebhooks delivers lifecycle events to webhooks until ctx ends. Like the
// event stream, it reads the events table after each webhook's cursor
// whenever an event is written, so the store is the only queue. Each webhook
// gets its events in order, one at a time; a failed event is retried on the
// backoff schedule and holds back later ones until it succeeds or the
// webhook is disabled. Delivery is at least once: receivers should dedupe
// on the webhook-id header.
func (m *Manager) RunWebhooks(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		wake := m.eventWake()
		m.dispatchWebhooks(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-m.wh.kick:
		case <-tick.C:
		}
	}
}

func (m *Manager) kickWebhooks() {
	select {
	case m.wh.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) dispatchWebhooks(ctx context.Context, wg *sync.WaitGroup) {
	hooks, err := m.store.ListEnabledWebhooks(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("webhook list failed", "err", err)
		}
		return
	}
	if len(hooks) == 0 {
		return
	}
	last, err := m.store.LastEventSeq(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, wh := range hooks {
		if wh.Cursor >= last {
			continue
		}
		m.wh.mu.Lock()
		r := m.wh.retry[wh.ID]
		if m.wh.busy[wh.ID] || now.Before(r.next) {
			m.wh.mu.Unlock()
			continue
		}
		select {
		case m.wh.sem <- struct{}{}:
		default:
			// Every worker is busy; the next tick picks this one up.
			m.wh.mu.Unlock()
			return
		}
		m.wh.busy[wh.ID] = true
		m.wh.mu.Unlock()
		wg.Add(1)
		go func(wh store.Webhook) {
			defer wg.Done()
			defer func() {
				m.wh.mu.Lock()
				delete(m.wh.busy, wh.ID)
				m.wh.mu.Unlock()
				<-m.wh.sem
			}()
			m.deliverWebhook(ctx, wh)
		}(wh)
	}
}

// deliverWebhook sends wh the events after its cursor, in order, until one
// fails or none are left.
func (m *Manager) deliverWebhook(ctx context.Context, wh store.Webhook) {
	key, err := m.store.KeyByID(ctx, wh.OwnerKeyID)
	if err != nil || !key.Usable(m.now()) {
		// A revoked or expired key's webhooks stay quiet. Revoking a key
		// is how an admin cuts off everything it set up.
		return
	}
	f := store.EventFilter{After: wh.Cursor, Limit: webhookBatch}
	if !key.Admin() {
		f.OwnerID = wh.OwnerKeyID
	}
	for ctx.Err() == nil {
		// Reload so an owner's change or delete takes effect between batches.
		cur, err := m.store.GetWebhook(ctx, wh.ID)
		if err != nil || !cur.Enabled {
			return
		}
		wh = cur
		f.After = max(f.After, wh.Cursor)
		// Read the newest seq first: every event of this owner up to it is
		// in the list below, so a short list lets the cursor skip to it past
		// other keys' events.
		last, err := m.store.LastEventSeq(ctx)
		if err != nil {
			return
		}
		items, err := m.store.ListEvents(ctx, f)
		if err != nil {
			if ctx.Err() == nil {
				m.log.Warn("webhook event read failed", "webhook", wh.ID, "err", err)
			}
			return
		}
		for _, ev := range items {
			if webhookWants(wh, ev.Type) && !m.deliverEvent(ctx, wh, ev) {
				m.advanceWebhook(ctx, wh.ID, f.After)
				return
			}
			f.After = ev.Seq
		}
		if len(items) < f.Limit {
			m.advanceWebhook(ctx, wh.ID, max(f.After, last))
			return
		}
		m.advanceWebhook(ctx, wh.ID, f.After)
	}
}

// deliverEvent makes one attempt at ev. It reports whether delivery may go
// on to the next event: true after a success, or after the last retry
// failed and the webhook was disabled past this event.
func (m *Manager) deliverEvent(ctx context.Context, wh store.Webhook, ev store.Event) bool {
	body, err := json.Marshal(NewEventPayload(ev))
	if err != nil {
		return true
	}
	status, err := m.sendWebhook(ctx, wh, fmt.Sprintf("evt_%d", ev.Seq), body)
	if ctx.Err() != nil {
		return false
	}
	rec := store.WebhookDelivery{At: m.now(), Status: status, OK: err == nil}
	if err != nil {
		rec.Error = err.Error()
	}
	if serr := m.store.RecordWebhookDelivery(context.WithoutCancel(ctx), wh.ID, rec); serr != nil {
		m.log.Warn("webhook status write failed", "webhook", wh.ID, "err", serr)
	}
	m.wh.mu.Lock()
	defer m.wh.mu.Unlock()
	if err == nil {
		delete(m.wh.retry, wh.ID)
		return true
	}
	r := m.wh.retry[wh.ID]
	r.attempts++
	if r.attempts <= len(m.wh.backoff) {
		delay := m.wh.backoff[r.attempts-1]
		r.next = time.Now().Add(delay)
		m.wh.retry[wh.ID] = r
		time.AfterFunc(delay, m.kickWebhooks)
		m.log.Info("webhook delivery failed", "webhook", wh.ID, "event", ev.Seq, "attempt", r.attempts, "err", err)
		return false
	}
	delete(m.wh.retry, wh.ID)
	reason := fmt.Sprintf("disabled after %d failed attempts to deliver event %d: %s", r.attempts, ev.Seq, rec.Error)
	m.log.Warn("webhook disabled", "webhook", wh.ID, "event", ev.Seq, "err", err)
	// The event is dropped: move past it so re-enabling resumes after it.
	_ = m.store.AdvanceWebhook(context.WithoutCancel(ctx), wh.ID, ev.Seq)
	_ = m.store.DisableWebhook(context.WithoutCancel(ctx), wh.ID, reason, m.now())
	return false
}

func (m *Manager) advanceWebhook(ctx context.Context, webhookID string, cursor int64) {
	if err := m.store.AdvanceWebhook(context.WithoutCancel(ctx), webhookID, cursor); err != nil {
		m.log.Warn("webhook cursor write failed", "webhook", webhookID, "err", err)
	}
}
