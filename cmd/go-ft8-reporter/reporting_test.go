package main

import (
	"math"
	"testing"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft8"
)

func TestSpotFromMessage(t *testing.T) {
	slot := time.Date(2026, 10, 25, 12, 0, 15, 0, time.UTC)
	for _, test := range []struct{ text, call, grid string }{
		{"CQ K1ABC FN42", "K1ABC", "FN42"},
		{"CQ DX K1ABC FN42", "K1ABC", "FN42"},
		{"CQ DX K1ABC", "K1ABC", ""},
		{"W9XYZ K1ABC -10", "K1ABC", ""},
		{"W9XYZ K1ABC R-08", "K1ABC", ""},
		{"W9XYZ K1ABC RR73", "K1ABC", ""},
		{"W9XYZ K1ABC R FN42", "K1ABC", "FN42"},
		{"CQ K1ABC/P FN42", "K1ABC/P", "FN42"},
		{"CQ PJ4/K1ABC", "PJ4/K1ABC", ""},
		{"<W9XYZ> PJ4/K1ABC RR73", "PJ4/K1ABC", ""},
	} {
		t.Run(test.text, func(t *testing.T) {
			encoded, err := ft8.EncodeStandardMessage(test.text)
			if err != nil {
				t.Fatal(err)
			}
			msg := ft8.DecodedMessage{Text: encoded.Text, Payload: ft8.Payload77FromBits(encoded.Bits77),
				ParseStatus: ft8.ParseStatusParsed, FreqHz: 1500.6, SNR: -12}
			spot, ok := spotFromMessage("ft8", slot, msg, 432174000)
			if !ok || spot.Callsign != test.call || spot.Locator != test.grid || spot.FrequencyHz != 432175501 || spot.Mode != "FT8" || spot.SNR != -12 || !spot.Time.Equal(slot) {
				t.Fatalf("wrong transmitter spot: %+v, accepted=%v", spot, ok)
			}
		})
	}
}

func TestSkipUnreportableMessages(t *testing.T) {
	e, err := ft8.EncodeStandardMessage("CQ K1ABC FN42")
	if err != nil {
		t.Fatal(err)
	}
	base := ft8.DecodedMessage{Text: e.Text, Payload: ft8.Payload77FromBits(e.Bits77), ParseStatus: ft8.ParseStatusParsed, FreqHz: 1500}
	for _, mutate := range []func(*ft8.DecodedMessage){
		func(m *ft8.DecodedMessage) { m.Payload = ft8.Payload77{} }, // free text cannot identify a sender
		func(m *ft8.DecodedMessage) { m.ParseStatus = ft8.ParseStatusUnsupported },
		func(m *ft8.DecodedMessage) { m.Text = "W9XYZ <...> -10" },
		func(m *ft8.DecodedMessage) { m.Text = "W9XYZ <K1ABC> -10" },
		func(m *ft8.DecodedMessage) { m.Text = "CQ RR73" },
		func(m *ft8.DecodedMessage) { m.Text = "" },
		func(m *ft8.DecodedMessage) { m.FreqHz = math.NaN() },
		func(m *ft8.DecodedMessage) { m.FreqHz = -1 },
	} {
		m := base
		mutate(&m)
		if spot, ok := spotFromMessage("ft8", time.Now(), m, 432174000); ok {
			t.Fatalf("reported %+v as %+v", m, spot)
		}
	}
}
