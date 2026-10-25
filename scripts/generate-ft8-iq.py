#!/usr/bin/env python3
"""Generate an independently encoded FT8 fixture. Requires rf-datagen and ft8code."""

import argparse
import hashlib
import inspect
import json
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path
import subprocess

import numpy as np
from scipy.signal import resample_poly
from rf_datagen.dsp import audio_to_iq
from rf_datagen.generators.wsjtx import encode_ft8


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=Path("testdata/rf-datagen/ft8-minute-1mhz.cu8"))
    parser.add_argument("--verify", action="store_true", help="test correct, zero and reversed shifts")
    args = parser.parse_args()
    revision = subprocess.run(
        ["git", "-C", str(Path(inspect.getfile(encode_ft8)).parent), "rev-parse", "HEAD"],
        capture_output=True, text=True,
    )
    # Four slots spanning a minute, plus 1s of guard samples at either end.
    # Multiple stations share a dial offset, but occupy distinct audio tones.
    schedule = [
        (0, "CQ PA3XYZ JO21", 1000, 0.00, 0.20),
        (15, "CQ K1ABC FN42", 700, -0.10, 0.20),
        (15, "CQ W9XYZ EN37", 1900, 0.10, 0.14),
        (30, "CQ PA3XYZ JO21", 700, 0.00, 0.20),
        (30, "CQ G4ABC IO91", 1500, -0.08, 0.14),
        (30, "CQ DL1ABC JO62", 2500, 0.12, 0.10),
        (45, "K1ABC W9XYZ -10", 1000, 0.05, 0.20),
        (45, "W9XYZ K1ABC R-08", 2200, -0.05, 0.14),
    ]
    minute = datetime(2026, 10, 25, tzinfo=timezone.utc)
    samples = np.zeros(62 * 12000, dtype=np.complex128)
    expected = []
    for slot_seconds, message, frequency, dt, amplitude in schedule:
        audio = encode_ft8(message)
        if audio is None:
            raise SystemExit("rf-datagen could not encode FT8; ensure WSJT-X ft8code is on PATH")
        baseband = audio_to_iq(audio, 12000, target_fs=12000)
        t = np.arange(len(baseband)) / 12000
        baseband *= amplitude * np.exp(2j * np.pi * (frequency - 1000) * t)
        start = round((1 + slot_seconds + 0.5 + dt) * 12000)
        samples[start:start + len(baseband)] += baseband
        expected.append({
            "slot_time": (minute + timedelta(seconds=slot_seconds)).isoformat().replace("+00:00", "Z"),
            "message": message, "audio_frequency_hz": frequency,
            "dt_seconds": dt, "amplitude": amplitude,
        })
    rng = np.random.default_rng(20261025)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    digest = hashlib.sha256()
    with args.output.open("wb") as output:
        # Upsample one second at a time with real neighbors on both sides.
        # 60 input guard samples exceed SciPy's filter half-length and map
        # to exactly 5000 output samples. Memory stays bounded at 1 MHz.
        padded = np.pad(samples, (60, 60))
        for second in range(62):
            low = padded[second * 12000:(second + 1) * 12000 + 120]
            wide = resample_poly(low, 250, 3)[5000:1005000]
            t = np.arange(second * 1000000, (second + 1) * 1000000) / 1000000
            iq = wide * np.exp(-2j * np.pi * 125000 * t)
            iq += 0.005 * (rng.standard_normal(len(iq)) + 1j * rng.standard_normal(len(iq)))
            raw = np.empty((len(iq), 2), dtype=np.uint8)
            raw[:, 0] = np.clip(np.rint((iq.real + 1) * 127.5), 0, 255)
            raw[:, 1] = np.clip(np.rint((iq.imag + 1) * 127.5), 0, 255)
            if np.any(np.abs(iq.real) >= 1) or np.any(np.abs(iq.imag) >= 1):
                raise SystemExit("Fixture clipped before IQ quantization")
            data = raw.tobytes()
            output.write(data)
            digest.update(data)
    metadata = {
        "generator": "https://github.com/Quantum-Serendipity/rf-datagen",
        "generator_commit": revision.stdout.strip() if revision.returncode == 0 else "unknown (installed package)",
        "encoder": "WSJT-X ft8code via rf_datagen.generators.wsjtx.encode_ft8",
        "expected": expected, "mode": "ft8", "format": "cu8", "sample_rate": 1000000,
        "duration_seconds": 62, "start_time": "2026-10-24T23:59:59Z",
        "dial_offset_hz": -125000, "shift": 0.125, "seed": 20261025,
        "noise_sigma_per_component": 0.005, "sha256": digest.hexdigest(),
    }
    args.output.with_suffix(".json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(f"Wrote {args.output}: {args.output.stat().st_size} bytes, SHA256 {digest.hexdigest()}", flush=True)
    if args.verify:
        for shift in (0.125, 0, -0.125):
            command = ["go", "run", "./cmd/go-ft8-reporter", "--iq-file", str(args.output),
                       "--start-time", metadata["start_time"], "--sample-rate", "1000000",
                       "--mode", "ft8", "--shift", str(shift), "--metrics-address", ""]
            result = subprocess.run(command, capture_output=True, text=True)
            if result.returncode:
                raise SystemExit(f"Receiver failed for shift={shift}:\n{result.stderr}")
            print(f"shift={shift}:\n{result.stderr}", flush=True)
            rows = re.findall(r'slot=(\S+) snr=.*?freq=([\d.]+)Hz dt=([+\-\d.]+)s .*?text="([^"]*)"', result.stderr)
            counts = re.findall(r'slot=(\S+) decoded=(\d+)', result.stderr)
            slot_times = sorted({item["slot_time"] for item in expected})
            if [stamp for stamp, _ in counts] != slot_times or "dropping" in result.stderr:
                raise SystemExit("Replay did not process all four expected UTC slots")
            if shift == 0.125:
                if len(rows) != len(expected):
                    raise SystemExit(f"Got {len(rows)} messages, expected {len(expected)}")
                for item in expected:
                    matches = [row for row in rows if row[0] == item["slot_time"] and row[3] == item["message"]]
                    if len(matches) != 1:
                        raise SystemExit(f"Missing or repeated decode: {item}")
                    _, frequency, dt, _ = matches[0]
                    if abs(float(frequency) - item["audio_frequency_hz"]) > 3 or abs(float(dt) - item["dt_seconds"]) > 0.08:
                        raise SystemExit(f"Wrong frequency or timing: {matches[0]}")
            elif rows or any(int(count) for _, count in counts):
                raise SystemExit(f"Unexpected decode for wrong shift={shift}")
        print("PASS: all 8 messages recovered across 4 slots, including simultaneous signals; wrong shifts decoded nothing.")


if __name__ == "__main__":
    main()
