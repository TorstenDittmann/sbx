"""Synthesizes the sound effects in media/ so the video has no third-party audio.

    python3 scripts/sfx.py

Needs numpy. Every file is mono, 48 kHz, 16-bit, and seeded, so reruns are identical.
"""

import pathlib
import wave

import numpy as np

RATE = 48_000
OUT = pathlib.Path(__file__).resolve().parent.parent / "media"
rng = np.random.default_rng(7)


def t(seconds):
    return np.arange(int(RATE * seconds)) / RATE


def lowpass(x, cutoff):
    # One-pole filter, run twice for a softer slope.
    a = np.exp(-2 * np.pi * cutoff / RATE)
    for _ in range(2):
        y = np.empty_like(x)
        acc = 0.0
        for i, v in enumerate(x):
            acc = (1 - a) * v + a * acc
            y[i] = acc
        x = y
    return x


def save(name, x, peak=0.9):
    x = x / max(np.max(np.abs(x)), 1e-9) * peak
    fade = min(len(x), int(RATE * 0.004))
    x[-fade:] *= np.linspace(1, 0, fade)
    with wave.open(str(OUT / name), "wb") as f:
        f.setnchannels(1)
        f.setsampwidth(2)
        f.setframerate(RATE)
        f.writeframes((x * 32767).astype("<i2").tobytes())


def draw():
    # A breath that rises while the outline closes, then stops.
    s = t(0.9)
    env = (s / s[-1]) ** 2 * np.exp(-np.maximum(s - 0.78, 0) / 0.03)
    noise = lowpass(rng.standard_normal(len(s)), 1400) * env
    sweep = np.sin(2 * np.pi * np.cumsum(260 + 260 * (s / s[-1]) ** 2) / RATE) * env * 0.25
    return noise + sweep


def plink(freq):
    s = t(0.5)
    tone = np.sin(2 * np.pi * freq * s) + 0.25 * np.sin(2 * np.pi * freq * 2.01 * s)
    attack = np.minimum(s / 0.003, 1)
    return tone * attack * np.exp(-s / 0.11)


def switch():
    s = t(0.16)
    tone = np.sin(2 * np.pi * (660 + 220 * np.exp(-s / 0.02)) * s)
    return tone * np.minimum(s / 0.002, 1) * np.exp(-s / 0.04)


def reap():
    s = t(0.18)
    freq = 900 * np.exp(-s / 0.09)
    tone = np.sin(2 * np.pi * np.cumsum(freq) / RATE)
    return tone * np.minimum(s / 0.002, 1) * np.exp(-s / 0.05)


def thump():
    # The logo lands: a falling sub, and an A major chord that rings out.
    s = t(2.6)
    sub_freq = 48 + 50 * np.exp(-s / 0.08)
    sub = np.sin(2 * np.pi * np.cumsum(sub_freq) / RATE) * np.exp(-s / 0.35)
    chord = sum(
        np.sin(2 * np.pi * f * s) * a for f, a in [(220.0, 0.5), (329.63, 0.4), (554.37, 0.3), (880.0, 0.12)]
    )
    chord *= np.minimum(s / 0.02, 1) * np.exp(-s / 0.9) * 0.55
    return sub + chord


def pad():
    # A quiet bed as long as the intro (17 s): A, E and B with slow beating.
    s = t(17.0)
    x = np.zeros_like(s)
    for f, a in [(110.0, 0.5), (164.81, 0.35), (246.94, 0.22), (329.63, 0.1)]:
        x += a * np.sin(2 * np.pi * f * s) + a * 0.6 * np.sin(2 * np.pi * (f * 1.003) * s)
    x *= 0.75 + 0.25 * np.sin(2 * np.pi * s / 6.0)
    x *= np.minimum(s / 1.5, 1) * np.minimum((s[-1] - s) / 2.5, 1)
    return lowpass(x, 900)


save("draw.wav", draw())
save("plink-a.wav", plink(1318.5))
save("plink-b.wav", plink(987.8))
save("plink-c.wav", plink(1174.7))
save("switch.wav", switch())
save("reap.wav", reap())
save("thump.wav", thump())
save("pad.wav", pad())
