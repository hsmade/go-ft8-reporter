package sdr

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func TestParseRTLTCPHeader(t *testing.T) {
	var header bytes.Buffer
	header.WriteString("RTL0")
	if err := binary.Write(&header, binary.BigEndian, uint32(6)); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&header, binary.BigEndian, uint32(29)); err != nil {
		t.Fatal(err)
	}

	got, err := ParseRTLTCPHeader(&header)

	if err != nil {
		t.Fatal(err)
	}
	if got != (RTLTCPHeader{TunerType: 6, GainCount: 29}) {
		t.Fatalf("unexpected header: %#v", got)
	}
}

func TestU8IQReaderConvertsUnsignedBytesToComplex64(t *testing.T) {
	reader := NewU8IQReader(bytes.NewReader([]byte{
		0, 255,
		128, 128,
		255, 0,
	}), 3)
	out := make([]complex64, 3)

	n, err := reader.ReadIQ(out)

	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 samples, got %d", n)
	}
	requireInDelta(t, -1.0, float64(real(out[0])), 1e-6)
	requireInDelta(t, 1.0, float64(imag(out[0])), 1e-6)
	requireInDelta(t, 0.0039215686, float64(real(out[1])), 1e-6)
	requireInDelta(t, 0.0039215686, float64(imag(out[1])), 1e-6)
	requireInDelta(t, 1.0, float64(real(out[2])), 1e-6)
	requireInDelta(t, -1.0, float64(imag(out[2])), 1e-6)
}

func TestDialRTLTCPReadsHeaderAndSamples(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		var payload bytes.Buffer
		payload.WriteString("RTL0")
		_ = binary.Write(&payload, binary.BigEndian, uint32(7))
		_ = binary.Write(&payload, binary.BigEndian, uint32(31))
		payload.Write([]byte{
			0, 255,
			255, 0,
		})
		_, err = io.Copy(conn, &payload)
		done <- err
	}()

	consumer, err := DialRTLTCP(listener.Addr().String(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	if consumer.Header != (RTLTCPHeader{TunerType: 7, GainCount: 31}) {
		t.Fatalf("unexpected header: %#v", consumer.Header)
	}

	out := make([]complex64, 2)
	n, err := consumer.ReadIQ(out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 samples, got %d", n)
	}
	requireInDelta(t, -1.0, float64(real(out[0])), 1e-6)
	requireInDelta(t, 1.0, float64(imag(out[0])), 1e-6)
	requireInDelta(t, 1.0, float64(real(out[1])), 1e-6)
	requireInDelta(t, -1.0, float64(imag(out[1])), 1e-6)

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
