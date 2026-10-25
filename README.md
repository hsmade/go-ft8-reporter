# go-ft8-reporter
A lightweight FT8/FT4 receiver that reports received packets to PSK Reporter.
It takes a rtl-tcp stream as input and can tune inside the delivered I/Q stream.

## Container image

Pushing a Git tag runs the test suite and publishes an amd64/arm64 image to
`ghcr.io/hsmade/go-ft8-reporter` with that exact tag. For example:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The workflow uses `GITHUB_TOKEN` with package-write permission; no separate
registry secret is needed. It does not publish an additional `latest` tag.
Package visibility is managed in the repository owner's GHCR package settings.

Build locally with `docker build -t go-ft8-reporter:local .`. The image runs as
UID/GID 65532 and retains go-ft8's CGO/PocketFFT backend. All receiver arguments
are passed directly after the image name. For example, on a Linux host with the
rtl-proxy or SSH tunnel listening on port 5555:

```bash
docker run --rm --network host ghcr.io/hsmade/go-ft8-reporter:v0.1.0 \
  --addr 127.0.0.1:5555 --sample-rate 2400000 \
  --shift 0.21916666666666668 --mode ft8 \
  --callsign YOURCALL --locator JO21AA --dial-frequency 432174000
```

Metrics listen on port 2112 by default. With Docker bridge networking, publish
it using `-p 2112:2112` and use a source address reachable from the container.
For file replay, mount the IQ directory read-only and pass the container path
to `--iq-file`. `--no-report` disables uploads as with the native binary.

## Architecture
The program connects to a TCP server (rtl-tcp) to receive the I/Q stream.
It doesn't configure rtl-sdr through the stream, to be able to support multiple clients.
It then shifts and decimates the stream to match the desired frequency and bandwidth.
Next it will wait for the right time slot and decode the FT8/FT4 packets.
Finally, it will report the received packets to PSK Reporter.

If you want to decode multiple frequencies, you can just run the program multiple times with
different settings.

The decimator produces 12 ksample/s complex baseband for both FT8 and FT4.
It uses a Blackman low-pass filter with a 3.5 kHz cutoff and a 1 kHz
transition (approximately 3 kHz passband and 4 kHz stopband on either side
of the tuned center). `--sample-rate` defaults to 2400000 Hz and must be
an integer multiple of 12000 Hz, or exactly 1000000 Hz; it must match the input.
At 1 MHz, the receiver decimates to 25 kHz and then uses a band-limited rational
resampler (12/25) to produce exactly 12 kHz.
The receiver does not configure the source sample rate.

At 960 ksample/s and 2.4 Msample/s, the 12 ksample/s receiver uses two FIR
stages (960k → 96k → 12k and 2.4M → 240k → 12k). This avoids applying the
narrow channel filter directly at the full input rate. Filtering remains
allocation-free after initialization, with group delay included in slot timing.
Run `go test ./pkg/sdr -run '^$' -bench BenchmarkReceiver12kPipeline -benchmem`
to compare the old single-stage path with staged mixing/filtering on your CPU.
This benchmark excludes TCP input and FT8/FT4 message decoding.

## Decoding

`--mode ft8` collects UTC-aligned 15-second slots (00, 15, 30, 45 seconds).
`--mode ft4` collects 7.5-second slots (00, 07.5, 15, ... seconds).
The partial startup slot is discarded. Each complete slot, including its quiet
margins, is converted from IQ to mono PCM16 and passed to a persistent
[go-ft8](https://github.com/ColonelBlimp/go-ft8) v0.9.0 decoder. Decoding runs
in a separate worker; if it falls behind, completed slots are dropped and logged
rather than blocking reception. Partial slots at EOF are discarded; queued
complete slots finish decoding.

Tune the frequency shifter to the USB dial frequency, so the desired signals
appear at positive audio frequencies within approximately 0–3 kHz. The real IQ
component is used as audio, so negative-frequency signals also fold into the
audio band. Decoded messages and slot counts go to stderr through the UTC logger,
including slot timestamp, SNR, audio frequency, time offset, parse status and text.

Keep the host clock synchronized. rtl_tcp provides no capture timestamps, so
slot timing is estimated from the first received block and then maintained by
sample count (assuming a continuous stream at the declared rate). Use
`--stream-latency 100ms`, for example, to subtract known upstream buffering delay.
Unreported sample loss and source clock drift can affect alignment over long runs.

## PSK Reporter

Live reception uploads to PSK Reporter by default. Supply your receiver identity
and the **USB dial frequency**, matching the channel selected by `--shift`:

```bash
go run ./cmd/go-ft8-reporter --addr localhost:5555 \
  --sample-rate 2400000 --shift 0.21916666666666668 --mode ft8 \
  --callsign YOURCALL --locator JO21AA --dial-frequency 432174000 \
  --antenna "70 cm vertical"
```

Use **`--no-report`** for decoding and logging without uploads; receiver identity
and dial-frequency arguments are then optional. **File replay (`--iq-file`)
always disables uploads**, including when identity arguments are supplied.

Reports use the transmitting callsign from parsed standard/portable and
compound-call messages, the USB dial plus decoded audio frequency, the received
SNR, and the slot timestamp. Locators are included when transmitted. Free text,
unresolved or bracketed sender hashes, and other message families remain logged
but are skipped for reporting. Callsign/band/mode duplicates are suppressed for
one hour, with a five-minute per-callsign minimum between changes. Batches upload
every five minutes or when full. `--reporter-address` can select a local UDP
collector for integration tests; its default is `report.pskreporter.info:4739`.

Ctrl-C/SIGTERM finishes queued decodes and flushes reports if a send is due.
Reports still within the collector's minimum send interval are discarded on
shutdown with a log message. Upload errors are logged and reception continues.
See [pkg/reporter](pkg/reporter/README.md) for protocol and lifecycle details.

## Prometheus metrics

Metrics are served at **`http://localhost:2112/metrics`** by default. Set
`--metrics-address 127.0.0.1:9210` to choose another listener (use different ports
for multiple receivers), or `--metrics-address ""` to disable the endpoint.
Metrics work with `--no-report` and file replay too. Go runtime and process
collectors are included.

Application metrics use the `go_ft8_reporter_` prefix and a `mode` label:

| Metric suffix | Meaning |
|---|---|
| `decode_slots_total{result="success\|empty\|error"}` | Slots with messages, no messages, or a checked decoder error |
| `decoded_messages_total{status="parsed\|unsupported\|unknown_type\|invalid\|unset"}` | Unique CRC-verified messages returned per slot, grouped by parse status |
| `ldpc_attempts_total{result="success\|failure"}` | Codeword decode attempts; success does not necessarily pass all later acceptance checks |
| `decode_candidates_total` | Coarse synchronization candidates found |
| `decode_duration_seconds` | Histogram of wall time spent decoding each slot |
| `dropped_slots_total` | Live slots dropped because the decode queue was full |
| `iq_samples_total`, `iq_read_errors_total` | Input IQ sample count and read failures (excluding EOF/shutdown) |
| `reporting_enabled` | Whether uploads are enabled, 0 or 1 |
| `reports_skipped_total` | Decoded messages that could not safely be converted to a transmitter report while reporting was enabled |
| `reports_queued_total`, `reports_duplicate_total`, `reports_rejected_total` | Export queue admission, duplicate suppression, and rejected Add calls |
| `reports_exported_total` | Individual reception reports in successful UDP writes |
| `export_datagrams_total`, `export_bytes_total`, `export_errors_total` | Successful UDP datagrams/bytes and failed send attempts |
| `reports_pending` | Current number of reports waiting for export |
| `reports_discarded_total` | Pending reports discarded at exporter shutdown |
| `last_export_timestamp_seconds` | Last successful UDP write time, or zero before any export |

An empty slot is normal when nobody transmits. Failed LDPC attempts can come
from noise or repeated attempts at the same signal: they are **not** a count of
known on-air packets that were missed. UDP export success means the local write
succeeded; PSK Reporter does not acknowledge delivery.

Example scrape configuration:

```yaml
scrape_configs:
  - job_name: ft8-receiver
    static_configs:
      - targets: ["localhost:2112"]
```

## Offline FT8 test

A generated fixture is available at `testdata/rf-datagen/ft8-minute-1mhz.cu8` after running
the generator below. It covers one minute of FT8 slots, with one second of guard
samples at either end: 62 seconds of **1 MHz unsigned 8-bit interleaved I/Q**
(124 MB, raw data without an rtl_tcp header). The JSON sidecar records its timing,
expected message, RF offset, generator revision, random seed and SHA256 hash.
The large IQ file is ignored by Git and can be regenerated.

Replay it through the same shifter, filter, resampler, slot collector and decoder
used for live reception:

```bash
go run ./cmd/go-ft8-reporter \
  --iq-file testdata/rf-datagen/ft8-minute-1mhz.cu8 \
  --start-time 2026-10-24T23:59:59Z \
  --sample-rate 1000000 --shift 0.125 --mode ft8
```

The file starts one second before a complete UTC minute. The signals share a dial offset
of **-125 kHz**, with distinct FT8 base tones relative to that dial. Mixing
by `+125000/1000000 = 0.125` brings it into the decoder's audio passband.

| UTC slot | Expected messages | Audio base frequencies |
|---|---|---|
| 00:00:00 | `CQ PA3XYZ JO21` | 1000 Hz |
| 00:00:15 | `CQ K1ABC FN42`; `CQ W9XYZ EN37` | 700; 1900 Hz |
| 00:00:30 | `CQ PA3XYZ JO21`; `CQ G4ABC IO91`; `CQ DL1ABC JO62` | 700; 1500; 2500 Hz |
| 00:00:45 | `K1ABC W9XYZ -10`; `W9XYZ K1ABC R-08` | 1000; 2200 Hz |

Expect decoded counts of **1, 2, 3, 2**. Signals in the same slot overlap in time
at different audio frequencies, have different amplitudes, and include timing
offsets from -100 to +120 ms. The repeated CQ in a later slot also checks that
adjacent slots are processed independently. Replaying with `--shift 0` or
`--shift -0.125` should produce `decoded=0` for all four slots.

File replay runs as fast as processing permits and waits for the decoder if its
queue fills. `--start-time` is the timestamp of the first IQ sample, not the
start of the FT8 transmission; filter delay is accounted for automatically.

To regenerate, provide Python 3.11+, Go, and WSJT-X's `ft8code` on PATH (on
Debian it comes with `wsjtx`). Install the pinned rf-datagen revision in a venv:

```bash
git clone https://github.com/Quantum-Serendipity/rf-datagen.git /tmp/go-ft8-rf-datagen
git -C /tmp/go-ft8-rf-datagen checkout 7ca4983fd964e300f79b49a472aeda9624f67b32
python3 -m venv /tmp/go-ft8-datagen-venv
/tmp/go-ft8-datagen-venv/bin/pip install -e /tmp/go-ft8-rf-datagen
/tmp/go-ft8-datagen-venv/bin/python scripts/generate-ft8-iq.py --verify
```

`--verify` tests the actual CLI with correct, zero and reversed shifts. It checks
all eight messages against their expected slot, frequency (within 3 Hz), and time
offset (within 80 ms), and rejects missing, extra or repeated decodes. The
generator uses rf-datagen's **WSJT-X-backed `encode_ft8`** path and analytic IQ
conversion, then SciPy rational upsampling and frequency translation. Its basic
`synth_ft8` path generates random data tones and cannot serve as a valid decoded
message fixture. The test encoder is independent of the Go decoder. A small,
seeded noise component exercises IQ quantization; this fixture checks receiver
wiring and tuning, rather than weak-signal sensitivity.
