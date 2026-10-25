package reporter

import (
	"encoding/binary"
	"time"
)

const (
	enterprise         = 30351
	receiverTemplateID = 0x9992
	// Distinct from the cookie-cutter layouts: 5-byte frequency and no IMD.
	senderTemplateID = 0x9994
	maxDatagram      = 1400
)

func append16(b []byte, n uint16) []byte { return binary.BigEndian.AppendUint16(b, n) }
func append32(b []byte, n uint32) []byte { return binary.BigEndian.AppendUint32(b, n) }
func text(b []byte, s string) []byte     { return append(append(b, byte(len(s))), s...) }

func set(id uint16, data []byte) []byte {
	b := append16(nil, id)
	b = append16(b, uint16((4+len(data)+3)&^3))
	b = append(b, data...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func field(b []byte, id, length uint16) []byte {
	if id == 150 {
		return append16(append16(b, id), length)
	}
	return append32(append16(append16(b, id|0x8000), length), enterprise)
}

func templates() []byte {
	// Receiver Options Template: receiver callsign is the single scope field.
	r := append16(nil, receiverTemplateID)
	r = append16(r, 4)
	r = append16(r, 1)
	for _, id := range []uint16{2, 4, 8, 9} {
		r = field(r, id, 0xffff)
	}
	b := set(3, r)
	s := append16(nil, senderTemplateID)
	s = append16(s, 7)
	for _, f := range [][2]uint16{{1, 0xffff}, {5, 5}, {6, 1}, {10, 0xffff}, {11, 1}, {3, 0xffff}, {150, 4}} {
		s = field(s, f[0], f[1])
	}
	return append(b, set(2, s)...)
}

func receiverData(r Receiver) []byte {
	b := text(nil, r.Callsign)
	b = text(b, r.Locator)
	b = text(b, r.Software)
	return set(receiverTemplateID, text(b, r.Antenna))
}

func senderData(s Spot, test bool) []byte {
	b := text(nil, s.Callsign)
	// Unsigned 40-bit RF frequency, supporting microwave bands as well.
	for shift := 32; shift >= 0; shift -= 8 {
		b = append(b, byte(s.FrequencyHz>>shift))
	}
	b = append(b, byte(int8(s.SNR)))
	b = text(b, s.Mode)
	source := byte(1) // automatically extracted
	if test {
		source |= 0x80
	}
	b = append(b, source)
	b = text(b, s.Locator)
	return append32(b, uint32(s.Time.Unix()))
}

func packet(receiver []byte, records [][]byte, includeTemplates bool, now time.Time, sequence, domain uint32) []byte {
	b := append16(nil, 10)
	b = append16(b, 0)
	b = append32(b, uint32(now.Unix()))
	b = append32(b, sequence)
	b = append32(b, domain)
	if includeTemplates {
		b = append(b, templates()...)
	}
	b = append(b, receiver...)
	var senders []byte
	for _, record := range records {
		senders = append(senders, record...)
	}
	b = append(b, set(senderTemplateID, senders)...)
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	return b
}
