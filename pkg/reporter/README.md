# PSK Reporter

This package implements the UDP/IPFIX upload protocol in
[PSK Reporter's developer specification](https://pskreporter.info/pskdev.html).
It is independent of the SDR and decoder; callers provide explicit `Spot`
values rather than raw decoded message text.

```go
r, err := reporter.New(reporter.Config{
    Receiver: reporter.Receiver{
        Callsign: "PA3XYZ",
        Locator:  "JO21AA",
        Software: "go-ft8-reporter 0.1.0",
        Antenna:  "70 cm vertical",
    },
})
if err != nil {
    return err
}
defer r.Close()

accepted, err := r.Add(reporter.Spot{
    Callsign:    "G4ABC", // transmitter, not the called station
    Locator:     "IO91",  // empty if unknown
    FrequencyHz: 432175000, // RF: 432.174 MHz USB dial + 1000 Hz audio
    SNR:         -17,
    Mode:        "FT8",
    Time:        slotStart, // transmission time, not time.Now() at upload
})
// accepted=false, err=nil means the report was suppressed as a duplicate.
// Handle err here and consume r.Errors() for background send failures.
_, _ = accepted, err
```

Only validated transmitter identities should be submitted. Do not guess a
callsign from an unresolved hash, free text, or the first token of a QSO message.
The package normalizes callsigns, locators and modes but cannot determine which
station transmitted an arbitrary decoded message.

## Scheduling and transport

- Production destination: `report.pskreporter.info:4739`, over one UDP socket
  retained for the lifetime of the reporter.
- A timer starts at construction and runs at five-minute intervals relative to
  the application, without alignment to UTC. No empty packets are sent.
- Packets are limited to 1400 bytes. When another spot would overflow the batch,
  the existing batch is sent immediately, as allowed for full packets.
- Duplicate callsigns on the same band and mode are suppressed for one hour.
  Changes of band or mode are eligible after a five-minute per-callsign minimum.
  This state is kept in memory and expires automatically.
- Each packet includes receiver identity. Both format templates are included
  in the first three successful sends and the next send at least an hour after
  the last template refresh. Sequence numbers count reception reports.
- Reports contain the sender callsign, unsigned 40-bit RF frequency, signed
  8-bit SNR, mode, automatic source flag, optional sender locator, and original
  Unix transmission time. Receiver records contain callsign, locator, software
  and optional antenna description. Strings are UTF-8, at most 254 bytes.
- `DomainID` can be a caller-persisted identifier. The default is a random
  identifier, stable for this reporter session.

`Add` may flush a full packet synchronously. UDP writes have a five-second
deadline. A failed write preserves the existing queue and sequence number;
the triggering new spot is rejected so the caller can retry. Background errors
are delivered through `Errors()` on a best-effort, one-element buffered channel.
UDP provides no delivery acknowledgement from the collector.

`Flush()` is available for explicit sends. It returns `ErrRateLimited` if a
previous packet was sent less than five minutes ago. The first flush can be
immediate, and an empty flush sends nothing. `Close()` stops the background
worker and discards pending spots; it does **not** force an early final packet.
Call `Flush()` before closing when a final packet is due.

## Testing

Tests compare templates and record bytes with independently written wire
expectations and check batching, padding, sequence numbers, template refresh,
duplicate expiry, rate limiting, microwave frequencies, failures and concurrent
shutdown. The transport test uses a local UDP listener.

For a local collector, set `Config.Address` to its host and UDP port. Set
`Config.Test=true` to mark generated reports with the protocol's test bit.
The main decoder uses this package for live reception. `--no-report` disables
uploads, and `--iq-file` always disables them so synthetic replay stays local.
