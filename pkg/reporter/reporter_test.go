package reporter

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryConn struct {
	packets [][]byte
	err     error
	short   bool
	closed  bool
}

func (c *memoryConn) Write(b []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.short {
		return len(b) - 1, nil
	}
	c.packets = append(c.packets, bytes.Clone(b))
	return len(b), nil
}
func (c *memoryConn) SetWriteDeadline(time.Time) error { return nil }
func (c *memoryConn) Close() error                     { c.closed = true; return nil }

func testConfig() Config {
	return Config{Receiver: Receiver{Callsign: "N1DQ", Locator: "FN42HN", Software: "Homebrew v5.6", Antenna: "Dipole"}, DomainID: 0x12345678}
}

func testSpot() Spot {
	return Spot{Callsign: "K1ABC", Locator: "FN42", FrequencyHz: 144175000, SNR: -17, Mode: "FT8", Time: time.Unix(1200960084, 0)}
}

func newTestReporter(t *testing.T) (*Reporter, *memoryConn, *time.Time) {
	t.Helper()
	now := time.Unix(1200960114, 0)
	c := &memoryConn{}
	r := newReporter(testConfig(), c, func() time.Time { return now })
	go r.run()
	t.Cleanup(func() { _ = r.Close() })
	return r, c, &now
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTemplateWireFormat(t *testing.T) {
	// Receiver descriptor exactly matches the site's four-field example.
	// Sender descriptor declares our 40-bit frequency / SNR / locator layout.
	want := unhex(t, `
	  0003 002c 9992 0004 0001
	  8002 ffff 0000768f 8004 ffff 0000768f
	  8008 ffff 0000768f 8009 ffff 0000768f 0000
	  0002 003c 9994 0007
	  8001 ffff 0000768f 8005 0005 0000768f
	  8006 0001 0000768f 800a ffff 0000768f
	  800b 0001 0000768f 8003 ffff 0000768f 0096 0004`)
	if !bytes.Equal(templates(), want) {
		t.Fatalf("templates:\ngot  %x\nwant %x", templates(), want)
	}
}

func TestSenderWireFormat(t *testing.T) {
	spot := Spot{Callsign: "N1DQ", FrequencyHz: 14070567, SNR: -17, Mode: "FT8", Locator: "FN42", Time: time.Unix(1200960084, 0)}
	// Independently transcribed fields, signed SNR, source=automatic and timestamp.
	want := unhex(t, `04 4e314451 00 00d6b327 ef 03 465438 01 04 464e3432 47953254`)
	if got := senderData(spot, false); !bytes.Equal(got, want) {
		t.Fatalf("sender %x want %x", got, want)
	}
	want[15] = 0x81 // informationSource is marked as a test report
	if got := senderData(spot, true); !bytes.Equal(got, want) {
		t.Fatalf("test sender %x want %x", got, want)
	}
}

type decodedPacket struct {
	sequence, domain uint32
	templates        bool
	receiver         []byte
	senders          []byte
}

func inspectPacket(t *testing.T, p []byte) decodedPacket {
	t.Helper()
	if len(p) > maxDatagram || len(p)%4 != 0 || binary.BigEndian.Uint16(p) != 10 || int(binary.BigEndian.Uint16(p[2:])) != len(p) {
		t.Fatalf("invalid IPFIX length/header: %x", p)
	}
	d := decodedPacket{sequence: binary.BigEndian.Uint32(p[8:]), domain: binary.BigEndian.Uint32(p[12:])}
	for offset := 16; offset < len(p); {
		id := binary.BigEndian.Uint16(p[offset:])
		length := int(binary.BigEndian.Uint16(p[offset+2:]))
		if length < 4 || length%4 != 0 || offset+length > len(p) {
			t.Fatalf("invalid set length %d", length)
		}
		switch id {
		case 2, 3:
			d.templates = true
		case receiverTemplateID:
			d.receiver = p[offset+4 : offset+length]
		case senderTemplateID:
			d.senders = p[offset+4 : offset+length]
		default:
			t.Fatalf("unexpected set id %x", id)
		}
		offset += length
	}
	if len(d.receiver) == 0 || len(d.senders) == 0 {
		t.Fatal("missing receiver/senders")
	}
	return d
}

func TestBatchingSequenceAndTemplateRefresh(t *testing.T) {
	r, c, now := newTestReporter(t)
	for batch := 0; batch < 5; batch++ {
		for i := 0; i < 2; i++ {
			s := testSpot()
			s.Callsign = fmt.Sprintf("K%dABC", batch*2+i)
			if ok, err := r.Add(s); !ok || err != nil {
				t.Fatalf("Add: %v %v", ok, err)
			}
		}
		if len(c.packets) != batch {
			t.Fatal("sent before flush")
		}
		if err := r.Flush(); err != nil {
			t.Fatal(err)
		}
		d := inspectPacket(t, c.packets[batch])
		if d.sequence != uint32(2*batch) || d.domain != testConfig().DomainID {
			t.Fatalf("sequence/domain: %+v", d)
		}
		if d.templates != (batch < 3 || batch == 4) {
			t.Fatalf("template refresh at batch %d", batch)
		}
		if binary.BigEndian.Uint32(c.packets[batch][4:]) != uint32(now.Unix()) {
			t.Fatal("incorrect export time")
		}
		if !bytes.Contains(d.senders, unhex(t, "47953254")) {
			t.Fatal("spot timestamp replaced with send time")
		}
		if batch == 3 {
			*now = now.Add(time.Hour)
		} else {
			*now = now.Add(Interval)
		}
	}
}

func TestDeduplicationAndCadence(t *testing.T) {
	r, c, now := newTestReporter(t)
	s := testSpot()
	if ok, err := r.Add(s); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Callsign = " k1abc "
	s.FrequencyHz += 100
	if ok, err := r.Add(s); ok || err != nil {
		t.Fatal("duplicate accepted", ok, err)
	}
	s.Mode = "ft4"
	if ok, err := r.Add(s); ok || err != nil {
		t.Fatal("mode change bypassed five-minute suppression")
	}
	*now = now.Add(Interval)
	s.Mode = "FT8"
	if ok, err := r.Add(s); ok || err != nil {
		t.Fatal("same band reported within hour")
	}
	s.FrequencyHz = 432175000
	if ok, err := r.Add(s); !ok || err != nil {
		t.Fatal("band change suppressed", ok, err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Callsign = "W9XYZ"
	if ok, err := r.Add(s); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := r.Flush(); !errors.Is(err, ErrRateLimited) {
		t.Fatal("early flush", err)
	}
	if len(c.packets) != 2 {
		t.Fatal("rate limit did not prevent packet")
	}
	*now = now.Add(time.Hour)
	s = testSpot()
	if ok, err := r.Add(s); !ok || err != nil {
		t.Fatal("expired duplicate suppressed", ok, err)
	}
}

func TestFullPacketsAndWriteFailure(t *testing.T) {
	r, c, now := newTestReporter(t)
	s := testSpot()
	for i := 0; len(c.packets) == 0; i++ {
		if i > 100 {
			t.Fatal("packet never filled")
		}
		s.Callsign = fmt.Sprintf("K%d%s", i, strings.Repeat("A", 50))
		if ok, err := r.Add(s); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	first := inspectPacket(t, c.packets[0])
	if first.sequence != 0 || r.sequence < 2 {
		t.Fatal("wrong full packet sequence")
	}
	// A second full packet may be sent without waiting five minutes.
	for i := 101; len(c.packets) == 1; i++ {
		s.Callsign = fmt.Sprintf("K%d%s", i, strings.Repeat("A", 50))
		if ok, err := r.Add(s); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	inspectPacket(t, c.packets[1])
	*now = now.Add(Interval)
	queued, seq := len(r.records), r.sequence
	c.err = errors.New("offline")
	if err := r.Flush(); err == nil {
		t.Fatal("write failure ignored")
	}
	if len(r.records) != queued || r.sequence != seq {
		t.Fatal("failed write lost queue/sequence")
	}
	c.err = nil
	c.short = true
	if err := r.Flush(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	c.short = false
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(r.records) != 0 || r.sequence != seq+uint32(queued) {
		t.Fatal("retry failed")
	}
}

func TestValidation(t *testing.T) {
	for _, mutate := range []func(*Spot){
		func(s *Spot) { s.Callsign = "<K1ABC>" },
		func(s *Spot) { s.Callsign = "RR73"; s.Locator = "RR73" },
		func(s *Spot) { s.Callsign = "..." },
		func(s *Spot) { s.Locator = "ZZ99" },
		func(s *Spot) { s.Mode = "PSK" },
		func(s *Spot) { s.SNR = -129 },
		func(s *Spot) { s.SNR = 128 },
		func(s *Spot) { s.FrequencyHz = 1 << 40 },
		func(s *Spot) { s.FrequencyHz = 0 },
		func(s *Spot) { s.Time = time.Time{} },
		func(s *Spot) { s.Time = time.Unix(1<<32, 0) },
	} {
		s := testSpot()
		mutate(&s)
		if _, err := normalizeSpot(s); err == nil {
			t.Fatalf("accepted invalid spot %+v", s)
		}
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Receiver.Callsign = "" },
		func(c *Config) { c.Receiver.Locator = "" },
		func(c *Config) { c.Receiver.Software = "" },
		func(c *Config) { c.Receiver.Antenna = strings.Repeat("x", 255) },
		func(c *Config) { c.Receiver.Software = "bad\x00name" },
	} {
		cfg := testConfig()
		mutate(&cfg)
		if _, err := normalizeConfig(cfg); err == nil {
			t.Fatalf("accepted invalid config %+v", cfg)
		}
	}
	s := testSpot()
	s.Callsign = " pa3xyz/p "
	s.Locator = "jo21aa"
	s.Mode = "ft8"
	s.FrequencyHz = 10489740000
	got, err := normalizeSpot(s)
	if err != nil || got.Callsign != "PA3XYZ/P" || got.Locator != "JO21AA" || got.Mode != "FT8" {
		t.Fatal(got, err)
	}
	b := senderData(got, false)
	offset := 1 + len(got.Callsign)
	var frequency uint64
	for _, v := range b[offset : offset+5] {
		frequency = frequency<<8 | uint64(v)
	}
	if frequency != s.FrequencyHz {
		t.Fatal("microwave frequency truncated", frequency)
	}
}

func TestLocalUDP(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := testConfig()
	cfg.Address = listener.LocalAddr().String()
	cfg.Test = true
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var source string
	for batch := 0; batch < 2; batch++ {
		s := testSpot()
		s.Callsign = fmt.Sprintf("K%dABC", batch)
		if _, err := r.Add(s); err != nil {
			t.Fatal(err)
		}
		// Advance only the cadence clock; real UDP deadlines still use wall time.
		r.mu.Lock()
		r.lastSend = time.Time{}
		r.mu.Unlock()
		if err := r.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 2048)
		n, addr, err := listener.ReadFrom(b)
		if err != nil {
			t.Fatal(err)
		}
		d := inspectPacket(t, b[:n])
		if batch == 0 {
			source = addr.String()
		} else if source != addr.String() {
			t.Fatal("UDP source port changed")
		}
		if !bytes.Contains(d.senders, []byte{0x81}) {
			t.Fatal("test flag missing")
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(testSpot()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := r.Flush(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestConcurrentAddAndClose(t *testing.T) {
	r, _, _ := newTestReporter(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = r.Add(testSpot()); _ = r.Flush(); _ = r.Close() }()
	}
	wg.Wait()
}
