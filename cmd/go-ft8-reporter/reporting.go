package main

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft8"
	"github.com/hsmade/go-ft8-reporter/pkg/reporter"
)

var senderCallPattern = regexp.MustCompile(`^[A-Z0-9]+(/[A-Z0-9]+)*$`)
var senderGridPattern = regexp.MustCompile(`^[A-R]{2}[0-9]{2}$`)

// Only standard, portable and nonstandard-call families have the call ordering
// used here. Free text and contest/DXpedition families must not be guessed from
// their text. In a QSO call2 is the transmitter; call1 is the addressee.
func spotFromMessage(mode string, slot time.Time, msg ft8.DecodedMessage, dial uint64) (reporter.Spot, bool) {
	if msg.ParseStatus != ft8.ParseStatusParsed {
		return reporter.Spot{}, false
	}
	typ := msg.Payload.MessageType().I3
	if typ != 1 && typ != 2 && typ != 4 {
		return reporter.Spot{}, false
	}
	fields := strings.Fields(msg.Text)
	if len(fields) < 2 {
		return reporter.Spot{}, false
	}
	index := 1
	grid := ""
	last := fields[len(fields)-1]
	if last != "RR73" && senderGridPattern.MatchString(last) && typ != 4 {
		grid = last
	}
	if fields[0] == "CQ" {
		index = len(fields) - 1
		if grid != "" {
			index--
		}
	}
	call := fields[index]
	if !senderCallPattern.MatchString(call) || call == "RR73" ||
		!strings.ContainsAny(call, "0123456789") || !strings.ContainsAny(call, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return reporter.Spot{}, false
	}
	if math.IsNaN(msg.FreqHz) || math.IsInf(msg.FreqHz, 0) || msg.FreqHz < 0 || msg.FreqHz > pcmRate/2 || dial == 0 || dial >= (1<<40)-pcmRate/2 {
		return reporter.Spot{}, false
	}
	return reporter.Spot{Callsign: call, Locator: grid, FrequencyHz: dial + uint64(math.Round(msg.FreqHz)),
		SNR: msg.SNR, Mode: strings.ToUpper(mode), Time: slot}, true
}
