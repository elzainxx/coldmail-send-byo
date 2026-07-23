// stream.go — the Phase-3 persistent box↔Brain control channel (§2.1). A box dials this mTLS
// listener and holds ONE long-lived stream carrying register → register_ack, heartbeat,
// push_job (Brain→box), and report (box→Brain) as newline-delimited JSON frames. This is the
// channel the credential travels on, so it is the strictest transport in the system: mutual TLS
// (RequireAndVerifyClientCert on the listener; the box verifies the Brain's cert on its side),
// token-gated registration bound to a sender.boxes row, and stream-close on any auth failure —
// NO job or credential is ever written to an unregistered/unauthenticated stream (§3.1/§3.3, S2).
//
// Phase 3 is one-job-in-flight-per-box (§2.5). The StreamServer tracks each registered box's
// live connection so the dispatcher can push to it; a report is routed to a handler the
// dispatcher installs. There is NO background scan/reclaim here (that is Phase 6): the server
// only ever updates last_heartbeat_at on register/heartbeat and marks the box's job reclaimable
// via its lease if the stream drops (the dispatcher owns that transition).
package boxlink

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sender-module/brain/internal/boxproto"
	"github.com/sender-module/brain/internal/tlsutil"
	"golang.org/x/crypto/bcrypt"
)

// ReportHandler is invoked for each box `report` frame. The dispatcher installs it; it must not
// block the read loop for long (it does a bounded DB transaction). It is called with the box id
// that produced the report so the outcome handler can attribute provenance.
type ReportHandler func(boxID string, rep boxproto.Report)

// StreamServer accepts box connections on the mTLS boxlink stream port.
type StreamServer struct {
	ln         net.Listener
	db         *pgxpool.Pool
	onReport   ReportHandler
	onRegister func(boxID string) // optional: kick the dispatcher when a box comes online

	mu    sync.Mutex
	boxes map[string]*boxConn // box id -> its live connection (one in-flight job per box)

	closed chan struct{}
}

// SetOnRegister installs a callback fired after a box successfully registers. The dispatcher uses
// it to kick a dispatch attempt when a box comes online (event-driven — NOT a background timer),
// so a job queued before any box connected is picked up on connect. Must not block.
func (s *StreamServer) SetOnRegister(fn func(boxID string)) { s.onRegister = fn }

// boxConn is one registered box's held stream. writes are serialized by wmu so a push_job frame
// is never interleaved with an ack.
type boxConn struct {
	conn     net.Conn
	enc      *json.Encoder
	wmu      sync.Mutex
	boxID    string
	inFlight string // job id currently pushed to this box ("" = idle); guarded by StreamServer.mu
}

// NewStreamServer builds the stream listener. db is the sender_module pool (token verification +
// heartbeat updates). It does not start accepting until Serve is called.
func NewStreamServer(addr string, tlsCfg tlsutil.ServerConfig, db *pgxpool.Pool, onReport ReportHandler) (*StreamServer, error) {
	tc, err := tlsCfg.Build()
	if err != nil {
		return nil, err
	}
	ln, err := tls.Listen("tcp", addr, tc)
	if err != nil {
		return nil, fmt.Errorf("boxlink stream listen %s: %w", addr, err)
	}
	return &StreamServer{
		ln:       ln,
		db:       db,
		onReport: onReport,
		boxes:    make(map[string]*boxConn),
		closed:   make(chan struct{}),
	}, nil
}

// Addr returns the bound address (useful when addr was ":0" in tests).
func (s *StreamServer) Addr() net.Addr { return s.ln.Addr() }

// Serve accepts connections until Close. Each connection is handled in its own goroutine.
func (s *StreamServer) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
				return err
			}
		}
		go s.handle(conn)
	}
}

// Close stops accepting and drops all held connections.
func (s *StreamServer) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	err := s.ln.Close()
	s.mu.Lock()
	for _, bc := range s.boxes {
		_ = bc.conn.Close()
	}
	s.boxes = make(map[string]*boxConn)
	s.mu.Unlock()
	return err
}

// handle runs one box connection: TLS handshake (implicit on first read), then the register
// handshake, then the read loop. On ANY auth failure the stream is closed and no work flows.
func (s *StreamServer) handle(conn net.Conn) {
	defer conn.Close()

	// Bound the register handshake so a silent peer cannot hold a slot forever.
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)

	// First frame MUST be register (§3.1).
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var reg boxproto.Register
	if err := json.Unmarshal(line, &reg); err != nil || reg.Type != boxproto.TypeRegister {
		_ = enc.Encode(boxproto.RegisterAck{Type: boxproto.TypeRegisterAck, RequestID: reg.RequestID, Accepted: false, Code: "bad_request"})
		return
	}

	if !s.authorizeBox(reg.BoxID, reg.Token) {
		// Two-vocabulary rule: the box learns only the clean code; the reason is logged internally.
		log.Printf("boxlink stream: register REJECTED id=%q (token/cert mismatch) — stream closed, no job/credential pushed", reg.BoxID)
		_ = enc.Encode(boxproto.RegisterAck{Type: boxproto.TypeRegisterAck, RequestID: reg.RequestID, Accepted: false, Code: "unauthenticated"})
		return
	}

	// Upsert-by-identity (§3.1/P3-4): status→active, last_heartbeat_at=NOW(), merge box_metadata.
	if err := s.markActive(reg); err != nil {
		log.Printf("boxlink stream: register accepted but DB update failed for id=%q: %v — closing", reg.BoxID, err)
		_ = enc.Encode(boxproto.RegisterAck{Type: boxproto.TypeRegisterAck, RequestID: reg.RequestID, Accepted: false, Code: "internal_error"})
		return
	}

	bc := &boxConn{conn: conn, enc: enc, boxID: reg.BoxID}
	// Register the live connection (replacing a stale one for the same id — a reconnect).
	s.mu.Lock()
	if old := s.boxes[reg.BoxID]; old != nil && old.conn != conn {
		_ = old.conn.Close()
	}
	s.boxes[reg.BoxID] = bc
	s.mu.Unlock()
	defer s.dropConn(reg.BoxID, bc)

	if err := s.writeFrame(bc, boxproto.RegisterAck{Type: boxproto.TypeRegisterAck, RequestID: reg.RequestID, Accepted: true}); err != nil {
		return
	}
	log.Printf("boxlink stream: box registered id=%q (token verified, mTLS client cert)", reg.BoxID)
	if s.onRegister != nil {
		go s.onRegister(reg.BoxID) // kick the dispatcher (a queued job may be waiting for a box)
	}

	// Read loop: heartbeats + reports. No further register expected. Clear the handshake deadline
	// (a healthy held stream is the liveness signal; idle is fine, heartbeats refresh the row).
	_ = conn.SetReadDeadline(time.Time{})
	for {
		lineBytes, err := br.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("boxlink stream: box id=%q stream closed (%v)", reg.BoxID, err)
			}
			return
		}
		s.dispatchFrame(reg.BoxID, lineBytes)
	}
}

// dispatchFrame decodes one inbound frame and routes it (heartbeat | report).
func (s *StreamServer) dispatchFrame(boxID string, line []byte) {
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &probe) != nil {
		return // ignore garbage frames; never crash the loop
	}
	switch probe.Type {
	case boxproto.TypeHeartbeat:
		if err := s.touchHeartbeat(boxID); err != nil {
			log.Printf("boxlink stream: heartbeat DB update failed for id=%q: %v", boxID, err)
		}
	case boxproto.TypeReport:
		var rep boxproto.Report
		if json.Unmarshal(line, &rep) != nil {
			return
		}
		// Clear in-flight marker (the box has reported; it can take the next job).
		s.mu.Lock()
		if bc := s.boxes[boxID]; bc != nil {
			bc.inFlight = ""
		}
		s.mu.Unlock()
		if s.onReport != nil {
			s.onReport(boxID, rep)
		}
	default:
		// Unknown/`register`-after-handshake: ignore (defensive; never leak, never crash).
	}
}

// PushJob writes a push_job frame to the given box on its held stream (§2.3). It fails if the box
// is not currently connected/registered or already has a job in flight (one-in-flight, §2.5) — the
// dispatcher treats either as "cannot push now" and leaves the job claimable. The push is the
// point at which the credential leaves the Brain; it is only ever written to a registered stream.
func (s *StreamServer) PushJob(pj boxproto.PushJob, boxID string) error {
	s.mu.Lock()
	bc := s.boxes[boxID]
	if bc == nil {
		s.mu.Unlock()
		return fmt.Errorf("box %q not connected", boxID)
	}
	if bc.inFlight != "" {
		s.mu.Unlock()
		return fmt.Errorf("box %q already has job %q in flight", boxID, bc.inFlight)
	}
	bc.inFlight = pj.JobID
	s.mu.Unlock()

	if err := s.writeFrame(bc, pj); err != nil {
		// Failed to hand off: clear the marker so a later attempt can retry.
		s.mu.Lock()
		if bc2 := s.boxes[boxID]; bc2 == bc {
			bc.inFlight = ""
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// Connected reports whether a box currently holds a registered stream (used by the dispatcher to
// pick a box and by tests).
func (s *StreamServer) Connected(boxID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	bc := s.boxes[boxID]
	return bc != nil
}

// AnyConnectedBox returns the id of some currently-connected box, or "" if none. Phase 3's trivial
// dispatcher uses this to find a box to push to (boxes are interchangeable — §2.2).
func (s *StreamServer) AnyConnectedBox() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, bc := range s.boxes {
		if bc.inFlight == "" {
			return id
		}
	}
	return ""
}

func (s *StreamServer) dropConn(boxID string, bc *boxConn) {
	s.mu.Lock()
	if cur := s.boxes[boxID]; cur == bc {
		delete(s.boxes, boxID)
	}
	s.mu.Unlock()
}

// writeFrame serializes one frame to the box, serialized per-connection. A short write deadline
// stops a wedged peer from blocking the pusher forever.
func (s *StreamServer) writeFrame(bc *boxConn, v any) error {
	bc.wmu.Lock()
	defer bc.wmu.Unlock()
	_ = bc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := bc.enc.Encode(v); err != nil {
		return err
	}
	_ = bc.conn.SetWriteDeadline(time.Time{})
	return nil
}

// authorizeBox verifies (id, token) against sender.boxes: the token is the authorization
// AUTHORITY (bcrypt vs token_hash), the client cert is transport (already verified by TLS). A
// missing/disabled box, an empty token_hash (decommissioned), or a token mismatch → reject.
func (s *StreamServer) authorizeBox(boxID, token string) bool {
	if boxID == "" || token == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hash, status string
	err := s.db.QueryRow(ctx,
		`SELECT token_hash, status::text FROM sender.boxes WHERE id = $1`, boxID).Scan(&hash, &status)
	if err != nil {
		return false // unknown box id (or DB error): fail closed
	}
	if status == "disabled" || hash == "" {
		return false // decommissioned: refused on next connect regardless of cert
	}
	// The id is a PK lookup; bcrypt.CompareHashAndPassword is constant-time against the stored
	// hash, so a wrong token for a real box id does not leak timing.
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)) == nil
}

// markActive upserts the box's liveness on register (§3.1 step 3). box_metadata is merged from
// the optional diagnostics fields (§3.5); nothing here is read by dispatch.
func (s *StreamServer) markActive(reg boxproto.Register) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta := boxMetadata(reg)
	_, err := s.db.Exec(ctx,
		`UPDATE sender.boxes
		   SET status = 'active',
		       last_heartbeat_at = NOW(),
		       box_metadata = COALESCE($2, box_metadata)
		 WHERE id = $1`, reg.BoxID, meta)
	return err
}

// touchHeartbeat refreshes last_heartbeat_at and returns a stale box to active (§3.2).
func (s *StreamServer) touchHeartbeat(boxID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.Exec(ctx,
		`UPDATE sender.boxes
		   SET last_heartbeat_at = NOW(),
		       status = CASE WHEN status = 'stale' THEN 'active'::sender.box_status ELSE status END
		 WHERE id = $1`, boxID)
	return err
}

// boxMetadata builds the optional diagnostics JSON (§3.5) or returns nil if the box sent none.
func boxMetadata(reg boxproto.Register) []byte {
	if reg.BoxVersion == "" && reg.Hostname == "" && reg.StartedAt == "" {
		return nil
	}
	m := map[string]string{}
	if reg.BoxVersion != "" {
		m["box_version"] = reg.BoxVersion
	}
	if reg.Hostname != "" {
		m["hostname"] = reg.Hostname
	}
	if reg.StartedAt != "" {
		m["started_at"] = reg.StartedAt
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
