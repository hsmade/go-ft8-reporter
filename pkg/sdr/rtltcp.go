package sdr

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
)

const rtltcpHeaderSize = 12

type RTLTCPHeader struct {
	TunerType uint32
	GainCount uint32
}

type U8IQReader struct {
	reader io.Reader
	rawBuf []byte
}

func NewU8IQReader(reader io.Reader, blockSize int) *U8IQReader {
	if blockSize <= 0 {
		blockSize = 1024
	}
	return &U8IQReader{
		reader: reader,
		rawBuf: make([]byte, blockSize*2),
	}
}

func (r *U8IQReader) ReadIQ(out []complex64) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}

	needBytes := len(out) * 2
	if cap(r.rawBuf) < needBytes {
		r.rawBuf = make([]byte, needBytes)
	}
	buf := r.rawBuf[:needBytes]

	nBytes, err := io.ReadAtLeast(r.reader, buf, 2)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return decodeIQBytes(buf[:nBytes], out), io.EOF
		}
		return 0, err
	}

	return decodeIQBytes(buf[:nBytes], out), nil
}

type RTLTCPConsumer struct {
	conn   net.Conn
	Header RTLTCPHeader
	reader *U8IQReader
}

func DialRTLTCP(addr string, blockSize int) (*RTLTCPConsumer, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	header, err := ParseRTLTCPHeader(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &RTLTCPConsumer{
		conn:   conn,
		Header: header,
		reader: NewU8IQReader(conn, blockSize),
	}, nil
}

func (c *RTLTCPConsumer) ReadIQ(out []complex64) (int, error) {
	return c.reader.ReadIQ(out)
}

func (c *RTLTCPConsumer) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func ParseRTLTCPHeader(r io.Reader) (RTLTCPHeader, error) {
	var raw [rtltcpHeaderSize]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return RTLTCPHeader{}, err
	}
	if string(raw[:4]) != "RTL0" {
		return RTLTCPHeader{}, errors.New("invalid rtl_tcp header magic")
	}
	return RTLTCPHeader{
		TunerType: binary.BigEndian.Uint32(raw[4:8]),
		GainCount: binary.BigEndian.Uint32(raw[8:12]),
	}, nil
}

func decodeIQBytes(in []byte, out []complex64) int {
	samples := len(in) / 2
	if samples > len(out) {
		samples = len(out)
	}

	for i := 0; i < samples; i++ {
		base := i * 2
		iPart := float32(float64(in[base])/(255.0/2.0) - 1.0)
		qPart := float32(float64(in[base+1])/(255.0/2.0) - 1.0)
		out[i] = complex(iPart, qPart)
	}

	return samples
}
