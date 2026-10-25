// Package reporter submits reception reports to PSK Reporter using its IPFIX
// UDP protocol: https://pskreporter.info/pskdev.html.
//
// New starts a five-minute timer relative to process activity, not UTC slot
// boundaries. Add queues validated reports, with one-hour deduplication per
// callsign/band/mode and a five-minute minimum per callsign across band changes.
// Packets are sent earlier only when full. No packets are sent for empty queues.
package reporter

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	DefaultAddress = "report.pskreporter.info:4739"
	Interval       = 5 * time.Minute
	DedupInterval  = time.Hour
)

var (
	ErrClosed      = errors.New("PSK reporter is closed")
	ErrRateLimited = errors.New("PSK reporter: last send was less than five minutes ago")
	callPattern    = regexp.MustCompile(`^[A-Z0-9]+(/[A-Z0-9]+)*$`)
	gridPattern    = regexp.MustCompile(`^[A-R]{2}[0-9]{2}([A-X]{2}([0-9]{2})?)?$`)
)

type Receiver struct {
	Callsign string
	Locator  string // Maidenhead locator: 4, 6 or 8 characters
	Software string // include application name and version
	Antenna  string // optional description
}

type Config struct {
	Receiver Receiver
	Address  string // default report.pskreporter.info:4739; override for local tests
	DomainID uint32 // optional persisted ID; zero generates a random session ID
	Test     bool   // sets the informationSource test bit on every report
}

// Spot identifies the transmitting station, not the station being called.
// FrequencyHz is the actual RF frequency (USB dial + decoded audio frequency),
// not the SDR center frequency. Locator may be empty when not transmitted.
type Spot struct {
	Callsign    string
	Locator     string
	FrequencyHz uint64
	SNR         int       // dB, -128..127; go-ft8's SNR uses the 2500 Hz reference bandwidth
	Mode        string    // FT8 or FT4
	Time        time.Time // transmission start time; never replaced with upload time
}

type transport interface {
	Write([]byte) (int, error)
	SetWriteDeadline(time.Time) error
	Close() error
}

// Reporter is safe for concurrent Add, Flush and Close calls.
type Reporter struct {
	mu                     sync.Mutex
	conn                   transport
	cfg                    Config
	receiver               []byte
	records                [][]byte
	queuedBytes            int
	sequence               uint32
	packets                uint64
	lastSend, lastTemplate time.Time
	seen                   map[spotKey]time.Time
	lastCall               map[string]time.Time
	pruneAt                time.Time
	now                    func() time.Time
	closed                 bool
	stop, done             chan struct{}
	errors                 chan error
	stats                  Stats
}

type spotKey struct {
	call, mode string
	band       uint64
}

// New validates the receiver, opens one UDP socket for the session, and starts
// background batching. It sends nothing until reports have been added.
func New(cfg Config) (*Reporter, error) {
	var err error
	cfg, err = normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("udp", cfg.Address, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial PSK Reporter: %w", err)
	}
	r := newReporter(cfg, conn, time.Now)
	go r.run()
	return r, nil
}

func newReporter(cfg Config, conn transport, now func() time.Time) *Reporter {
	return &Reporter{cfg: cfg, conn: conn, receiver: receiverData(cfg.Receiver),
		seen: make(map[spotKey]time.Time), lastCall: make(map[string]time.Time),
		now: now, stop: make(chan struct{}), done: make(chan struct{}), errors: make(chan error, 1)}
}

// Errors reports background flush failures. Delivery is best effort (buffer 1);
// synchronous Add and Flush failures are returned directly. Failed packets stay
// queued for retry. A successful UDP write does not acknowledge server receipt.
func (r *Reporter) Errors() <-chan error { return r.errors }

func (r *Reporter) run() {
	defer close(r.done)
	defer close(r.errors)
	timer := time.NewTimer(Interval)
	defer timer.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-timer.C:
			if err := r.Flush(); err != nil && !errors.Is(err, ErrRateLimited) && !errors.Is(err, ErrClosed) {
				select {
				case r.errors <- err:
				default:
				}
			}
			// Reset relative to completion, so blocking sends never bunch timers.
			timer.Reset(Interval)
		}
	}
}

// Add returns true when a spot is queued, or false for a suppressed duplicate.
// A full packet may be sent synchronously; writes have a five-second timeout.
// If that send fails, the old packet is retained and this new spot is rejected.
func (r *Reporter) Add(spot Spot) (accepted bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		if err != nil {
			r.stats.Rejected++
		} else if accepted {
			r.stats.Queued++
		} else {
			r.stats.Duplicates++
		}
	}()
	spot, err = normalizeSpot(spot)
	if err != nil {
		return false, err
	}
	if r.closed {
		return false, ErrClosed
	}
	now := r.now()
	r.prune(now)
	key := spotKey{spot.Callsign, spot.Mode, band(spot.FrequencyHz)}
	if last, ok := r.lastCall[spot.Callsign]; ok && now.Sub(last) < Interval {
		return false, nil
	}
	if last, ok := r.seen[key]; ok && now.Sub(last) < DedupInterval {
		return false, nil
	}
	record := senderData(spot, r.cfg.Test)
	if r.packetSize(len(record)) > maxDatagram {
		return false, fmt.Errorf("spot does not fit a %d-byte datagram", maxDatagram)
	}
	// Reserve template space even for packets that will omit templates. This
	// bounds the queue and leaves space if an hourly refresh becomes due.
	if r.packetSize(r.queuedBytes+len(record)) > maxDatagram {
		if err := r.flushLocked(now); err != nil {
			return false, err
		}
	}
	r.records = append(r.records, record)
	r.queuedBytes += len(record)
	r.seen[key], r.lastCall[spot.Callsign] = now, now
	return true, nil
}

func (r *Reporter) packetSize(senderBytes int) int {
	return 16 + len(templates()) + len(r.receiver) + ((4 + senderBytes + 3) &^ 3)
}

// Flush sends pending reports if at least five minutes have elapsed since the
// last send. The first explicit Flush is permitted immediately. Empty Flush is
// a no-op. Full-buffer sends from Add are exempt from this timing restriction.
func (r *Reporter) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if len(r.records) == 0 {
		return nil
	}
	now := r.now()
	if !r.lastSend.IsZero() && now.Sub(r.lastSend) < Interval {
		return ErrRateLimited
	}
	return r.flushLocked(now)
}

func (r *Reporter) flushLocked(now time.Time) (err error) {
	if len(r.records) == 0 {
		return nil
	}
	defer func() {
		if err != nil {
			r.stats.SendErrors++
		}
	}()
	withTemplates := r.packets < 3 || now.Sub(r.lastTemplate) >= time.Hour
	b := packet(r.receiver, r.records, withTemplates, now, r.sequence, r.cfg.DomainID)
	if err := r.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	n, err := r.conn.Write(b)
	if err != nil {
		return fmt.Errorf("send PSK Reporter packet: %w", err)
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	// The PSK Reporter header counts reception reports, not packets.
	r.sequence += uint32(len(r.records))
	r.packets++
	r.stats.Exported += uint64(len(r.records))
	r.stats.Datagrams++
	r.stats.Bytes += uint64(len(b))
	r.lastSend = now
	if withTemplates {
		r.lastTemplate = now
	}
	r.records = nil
	r.queuedBytes = 0
	return nil
}

// Close stops the worker and closes the socket. It does not flush: call Flush
// first if a final packet is due. Pending spots are discarded on Close, rather
// than violating the collector's five-minute rate limit. Close is idempotent.
func (r *Reporter) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return nil
	}
	r.closed = true
	close(r.stop)
	err := r.conn.Close()
	r.stats.Discarded += uint64(len(r.records))
	r.records = nil
	r.mu.Unlock()
	<-r.done
	return err
}

func (r *Reporter) prune(now time.Time) {
	if now.Before(r.pruneAt) {
		return
	}
	for key, last := range r.seen {
		if now.Sub(last) >= DedupInterval {
			delete(r.seen, key)
		}
	}
	for call, last := range r.lastCall {
		if now.Sub(last) >= Interval {
			delete(r.lastCall, call)
		}
	}
	r.pruneAt = now.Add(Interval)
}

func validText(name, value string, required bool) error {
	if (required && strings.TrimSpace(value) == "") || len(value) > 254 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid %s: require UTF-8, no NUL, at most 254 bytes", name)
	}
	return nil
}

func validCall(call string) bool {
	return call != "RR73" && len(call) <= 254 && callPattern.MatchString(call) && strings.ContainsAny(call, "0123456789") && strings.ContainsAny(call, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func normalizeConfig(cfg Config) (Config, error) {
	r := &cfg.Receiver
	r.Callsign = strings.ToUpper(strings.TrimSpace(r.Callsign))
	r.Locator = strings.ToUpper(strings.TrimSpace(r.Locator))
	if !validCall(r.Callsign) {
		return cfg, fmt.Errorf("invalid receiver callsign %q", r.Callsign)
	}
	if !gridPattern.MatchString(r.Locator) {
		return cfg, fmt.Errorf("invalid receiver locator %q", r.Locator)
	}
	if err := validText("software", r.Software, true); err != nil {
		return cfg, err
	}
	if err := validText("antenna", r.Antenna, false); err != nil {
		return cfg, err
	}
	if cfg.Address == "" {
		cfg.Address = DefaultAddress
	}
	if cfg.DomainID == 0 {
		var id [4]byte
		if _, err := rand.Read(id[:]); err != nil {
			return cfg, err
		}
		cfg.DomainID = binary.BigEndian.Uint32(id[:])
	}
	return cfg, nil
}

func normalizeSpot(s Spot) (Spot, error) {
	s.Callsign = strings.ToUpper(strings.TrimSpace(s.Callsign))
	s.Locator = strings.ToUpper(strings.TrimSpace(s.Locator))
	s.Mode = strings.ToUpper(strings.TrimSpace(s.Mode))
	if !validCall(s.Callsign) {
		return s, fmt.Errorf("invalid sender callsign %q", s.Callsign)
	}
	if s.Locator != "" && !gridPattern.MatchString(s.Locator) {
		return s, fmt.Errorf("invalid sender locator %q", s.Locator)
	}
	if s.Mode != "FT8" && s.Mode != "FT4" {
		return s, fmt.Errorf("unsupported mode %q", s.Mode)
	}
	if s.FrequencyHz == 0 || s.FrequencyHz >= 1<<40 {
		return s, fmt.Errorf("frequency must fit unsigned 40-bit Hz")
	}
	if s.SNR < -128 || s.SNR > 127 {
		return s, fmt.Errorf("SNR must fit signed 8-bit dB")
	}
	if s.Time.IsZero() || s.Time.Unix() < 0 || s.Time.Unix() > 1<<32-1 {
		return s, fmt.Errorf("time must fit unsigned 32-bit Unix seconds")
	}
	return s, nil
}

// Broad amateur allocations cover regional differences. Outside these bands,
// group by MHz, rather than treating all unknown frequencies as the same band.
func band(hz uint64) uint64 {
	for i, limits := range [][2]uint64{
		{135700, 137800}, {472000, 479000}, {1800000, 2000000},
		{3500000, 4000000}, {5250000, 5450000}, {7000000, 7300000},
		{10100000, 10150000}, {14000000, 14350000}, {18068000, 18168000},
		{21000000, 21450000}, {24890000, 24990000}, {28000000, 29700000},
		{40000000, 41000000}, {50000000, 54000000}, {70000000, 71000000},
		{144000000, 148000000}, {219000000, 225000000}, {420000000, 450000000},
		{902000000, 928000000}, {1240000000, 1300000000}, {2300000000, 2450000000},
		{3300000000, 3500000000}, {5650000000, 5925000000}, {10000000000, 10500000000},
	} {
		if hz >= limits[0] && hz <= limits[1] {
			return uint64(i)
		}
	}
	return 1000 + hz/1000000
}
