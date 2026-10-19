package agent

import (
	"bufio"
	"context"
	"io"
	"sync"
)

// Mux multiplexes frames over one connection. A client mux allocates stream
// ids. A server mux accepts them and calls onStream for each new one.
type Mux struct {
	bw       *bufio.Writer
	writeMu  sync.Mutex
	mu       sync.Mutex
	streams  map[uint32]*Stream
	closed   map[uint32]struct{}
	onStream func(*Stream)
	client   bool
	next     uint32
	done     chan struct{}
	err      error
}

// Stream is one multiplexed conversation.
type Stream struct {
	id     uint32
	mux    *Mux
	ch     chan Frame
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// NewMux wraps w. client muxes allocate stream ids; server muxes do not.
func NewMux(w io.Writer, client bool) *Mux {
	return &Mux{
		bw:      bufio.NewWriterSize(w, 64<<10),
		streams: map[uint32]*Stream{},
		closed:  map[uint32]struct{}{},
		client:  client,
		done:    make(chan struct{}),
	}
}

// Done is closed when the read loop stops.
func (m *Mux) Done() <-chan struct{} { return m.done }

// ReadLoop reads frames until the connection fails. It does not return otherwise.
func (m *Mux) ReadLoop(r io.Reader) {
	defer m.fail(io.EOF)
	for {
		f, err := ReadFrame(r)
		if err != nil {
			m.fail(err)
			return
		}
		if f.Stream == 0 {
			continue
		}
		m.mu.Lock()
		s, ok := m.streams[f.Stream]
		if !ok {
			if _, done := m.closed[f.Stream]; done || m.client || m.onStream == nil {
				m.mu.Unlock()
				continue
			}
			s = newStream(m, f.Stream)
			m.streams[f.Stream] = s
			go m.onStream(s)
		}
		m.mu.Unlock()
		s.deliver(f)
	}
}

// Open allocates a client stream. It is not used by the server.
func (m *Mux) Open() (*Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.done:
		return nil, ErrClosed
	default:
	}
	m.next++
	if m.next == 0 {
		m.next = 1
	}
	s := newStream(m, m.next)
	m.streams[m.next] = s
	return s, nil
}

// Send writes one frame and flushes it.
func (m *Mux) Send(stream uint32, typ uint8, data []byte) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	select {
	case <-m.done:
		return ErrClosed
	default:
	}
	if err := WriteFrame(m.bw, stream, typ, data); err != nil {
		return err
	}
	return m.bw.Flush()
}

func (m *Mux) remove(id uint32) {
	m.mu.Lock()
	s := m.streams[id]
	delete(m.streams, id)
	m.closed[id] = struct{}{}
	m.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

func (m *Mux) fail(err error) {
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return
	}
	m.err = err
	close(m.done)
	streams := make([]*Stream, 0, len(m.streams))
	for _, s := range m.streams {
		streams = append(streams, s)
	}
	m.mu.Unlock()
	for _, s := range streams {
		s.cancel()
	}
}

func newStream(m *Mux, id uint32) *Stream {
	ctx, cancel := context.WithCancel(context.Background())
	return &Stream{id: id, mux: m, ch: make(chan Frame, 64), ctx: ctx, cancel: cancel}
}

func (s *Stream) deliver(f Frame) {
	if f.Type == MsgKill {
		s.cancel()
	}
	select {
	case s.ch <- f:
	case <-s.ctx.Done():
	}
}

// Recv waits for the next frame.
func (s *Stream) Recv(ctx context.Context) (Frame, error) {
	select {
	case f := <-s.ch:
		return f, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-s.ctx.Done():
		select {
		case f := <-s.ch:
			return f, nil
		default:
			return Frame{}, ErrClosed
		}
	case <-s.mux.done:
		select {
		case f := <-s.ch:
			return f, nil
		default:
			return Frame{}, ErrClosed
		}
	}
}

// Send writes a frame on this stream.
func (s *Stream) Send(typ uint8, data []byte) error {
	return s.mux.Send(s.id, typ, data)
}

// Close signals the peer and forgets the stream.
func (s *Stream) Close() {
	s.once.Do(func() {
		s.cancel()
		_ = s.mux.Send(s.id, MsgKill, nil)
		s.mux.remove(s.id)
	})
}
