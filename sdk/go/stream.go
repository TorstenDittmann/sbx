package sbx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// sseEvent is one Server-Sent Events block.
type sseEvent struct {
	event string
	data  string
	id    string
	hasID bool
}

// readSSE calls fn for each block until the stream ends, fn returns false,
// or a read fails. A clean end of stream returns nil.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var cur sseEvent
	var data []string
	pending := false
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 || err == nil {
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if pending {
					cur.data = strings.Join(data, "\n")
					if !fn(cur) {
						return nil
					}
				}
				cur, data, pending = sseEvent{}, nil, false
			} else if !strings.HasPrefix(line, ":") {
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					cur.event = value
				case "data":
					data = append(data, value)
				case "id":
					cur.id, cur.hasID = value, true
				}
				pending = true
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func execEvent(ev sseEvent) (ExecEvent, bool, error) {
	if ev.event == "" {
		return ExecEvent{}, false, nil
	}
	var raw struct {
		Data       string `json:"data"`
		ExitCode   int    `json:"exit_code"`
		DurationMs int64  `json:"duration_ms"`
		Truncated  bool   `json:"truncated"`
		Code       string `json:"code"`
		Message    string `json:"message"`
	}
	if ev.data != "" {
		if err := json.Unmarshal([]byte(ev.data), &raw); err != nil {
			return ExecEvent{}, false, err
		}
	}
	switch ExecEventType(ev.event) {
	case ExecStdout, ExecStderr:
		return ExecEvent{Type: ExecEventType(ev.event), Data: raw.Data}, true, nil
	case ExecExit:
		return ExecEvent{Type: ExecExit, ExitCode: raw.ExitCode, DurationMs: raw.DurationMs, Truncated: raw.Truncated}, true, nil
	}
	if raw.Code == "" {
		raw.Code = "driver"
	}
	return ExecEvent{Type: ExecError, Code: raw.Code, Message: raw.Message}, true, nil
}

// execEvents turns an exec or process stream into an iterator. An error
// event is yielded as an ExecEvent, not as an error; transport and decode
// failures are yielded as errors.
func (c *Client) execEvents(ctx context.Context, method, path string, body any) iter.Seq2[ExecEvent, error] {
	return func(yield func(ExecEvent, error) bool) {
		resp, err := c.do(ctx, method, path, body, http.Header{"Accept": {"text/event-stream"}})
		if err != nil {
			yield(ExecEvent{}, err)
			return
		}
		defer resp.Body.Close()
		var failed error
		stopped := false
		err = readSSE(resp.Body, func(ev sseEvent) bool {
			event, ok, err := execEvent(ev)
			if err != nil {
				failed = err
				return false
			}
			if ok && !yield(event, nil) {
				stopped = true
				return false
			}
			return true
		})
		if stopped {
			return
		}
		if failed == nil {
			failed = err
		}
		if failed != nil {
			yield(ExecEvent{}, failed)
		}
	}
}

// ExecStream runs a command and yields its output as it arrives, then an
// exit event. Breaking out of the loop closes the stream.
//
//	for ev, err := range client.ExecStream(ctx, id, sbx.Shell("make"), nil) {
//		if err != nil {
//			return err
//		}
//		fmt.Print(ev.Data)
//	}
func (c *Client) ExecStream(ctx context.Context, id string, cmd []string, opts *ExecOptions) iter.Seq2[ExecEvent, error] {
	body, err := execBody(cmd, opts)
	if err != nil {
		return func(yield func(ExecEvent, error) bool) { yield(ExecEvent{}, err) }
	}
	return c.execEvents(ctx, http.MethodPost, sandboxPath(id, "exec")+"?stream=1", body)
}

// StreamProcess follows a background process, starting with output it
// buffered before the call.
func (c *Client) StreamProcess(ctx context.Context, id, procID string) iter.Seq2[ExecEvent, error] {
	return c.execEvents(ctx, http.MethodGet, sandboxPath(id, "processes", procID, "stream"), nil)
}

// Watch yields create, write, and remove events under path. The agent uses
// inotify and polls when inotify is unavailable. Breaking out of the loop
// closes the stream.
func (c *Client) Watch(ctx context.Context, id, path string, recursive bool) iter.Seq2[FileEvent, error] {
	return func(yield func(FileEvent, error) bool) {
		values := filesQuery(path)
		if recursive {
			values.Set("recursive", "1")
		}
		resp, err := c.do(ctx, http.MethodGet, withQuery(sandboxPath(id, "files", "watch"), values), nil, http.Header{"Accept": {"text/event-stream"}})
		if err != nil {
			yield(FileEvent{}, err)
			return
		}
		defer resp.Body.Close()
		var failed error
		stopped := false
		err = readSSE(resp.Body, func(ev sseEvent) bool {
			switch ev.event {
			case "change":
				var fe FileEvent
				if err := json.Unmarshal([]byte(ev.data), &fe); err != nil {
					failed = err
					return false
				}
				if !yield(fe, nil) {
					stopped = true
					return false
				}
			case "error":
				var raw struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				_ = json.Unmarshal([]byte(ev.data), &raw)
				if raw.Code == "" {
					raw.Code = "driver"
				}
				if raw.Message == "" {
					raw.Message = "watch failed"
				}
				failed = &Error{Status: http.StatusBadGateway, Code: raw.Code, Message: raw.Message}
				return false
			}
			return true
		})
		if stopped {
			return
		}
		if failed == nil {
			failed = err
		}
		if failed != nil {
			yield(FileEvent{}, failed)
		}
	}
}

// EventsOptions configures Events.
type EventsOptions struct {
	// After is the last event id already seen. Nil starts with the next
	// event; a pointer to 0 replays everything the server keeps.
	After *int64
	// Sandbox limits the stream to one sandbox.
	Sandbox string
	// NoReconnect ends the stream when the connection drops. By default it
	// reconnects after a second and resumes after the last id.
	NoReconnect bool
}

// Events streams sandbox lifecycle events instead of polling. A sandbox key
// sees its own sandboxes; an admin key sees all. API errors end the stream;
// dropped connections are retried unless NoReconnect is set. Cancel ctx or
// break out of the loop to stop.
func (c *Client) Events(ctx context.Context, o EventsOptions) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		var after *int64
		if o.After != nil {
			v := *o.After
			after = &v
		}
		for ctx.Err() == nil {
			values := url.Values{}
			if after != nil {
				values.Set("after", strconv.FormatInt(*after, 10))
			}
			if o.Sandbox != "" {
				values.Set("sandbox", o.Sandbox)
			}
			stopped := false
			var failed error
			resp, err := c.do(ctx, http.MethodGet, withQuery("/v1/events", values), nil, http.Header{"Accept": {"text/event-stream"}})
			if err == nil {
				err = readSSE(resp.Body, func(ev sseEvent) bool {
					// The stream opens with a bare id line: the cursor it starts from.
					if ev.hasID {
						if n, perr := strconv.ParseInt(strings.TrimSpace(ev.id), 10, 64); perr == nil && n >= 0 {
							after = &n
						}
					}
					if ev.event == "" || ev.data == "" {
						return true
					}
					var event Event
					if derr := json.Unmarshal([]byte(ev.data), &event); derr != nil {
						failed = derr
						return false
					}
					if !yield(event, nil) {
						stopped = true
						return false
					}
					return true
				})
				resp.Body.Close()
			}
			if stopped || ctx.Err() != nil {
				return
			}
			if failed != nil {
				yield(Event{}, failed)
				return
			}
			var apiErr *Error
			if err != nil && (o.NoReconnect || errors.As(err, &apiErr)) {
				yield(Event{}, err)
				return
			}
			if o.NoReconnect {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}
