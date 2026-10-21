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


def bandpass(x, freq, q):
    # RBJ biquad band-pass: the resonance of a keycap and the plate under it.
    w = 2 * np.pi * freq / RATE
    alpha = np.sin(w) / (2 * q)
    b0, b2 = alpha, -alpha
    a0, a1, a2 = 1 + alpha, -2 * np.cos(w), 1 - alpha
    y = np.zeros_like(x)
    x1 = x2 = y1 = y2 = 0.0
    for i, v in enumerate(x):
        out = (b0 * v + b2 * x2 - a1 * y1 - a2 * y2) / a0
        x2, x1, y2, y1 = x1, v, y1, out
        y[i] = out
    return y


def key(body, click, seed):
    # A mechanical key: a short bright contact, a damped "thock" from the case, and a
    # quieter second hit when the key bottoms out a few milliseconds later.
    r = np.random.default_rng(seed)
    s = t(0.07)
    impulse = r.standard_normal(len(s))
    contact = bandpass(impulse * np.exp(-s / 0.0012), click, 1.4) * 0.5
    thock = bandpass(impulse * np.exp(-s / 0.004), body, 6.0) * 1.6
    bottom = np.zeros_like(s)
    lag = int(RATE * 0.009)
    bottom[lag:] = thock[: len(s) - lag] * 0.45
    x = contact + thock + bottom
    return lowpass(x * np.exp(-s / 0.018), 6500)


def enter():
    s = t(0.12)
    noise = rng.standard_normal(len(s)) * np.exp(-s / 0.008)
    body = np.sin(2 * np.pi * 170 * s) * np.exp(-s / 0.03)
    return lowpass(noise * 0.4, 3000) + body


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


for i, (body, click, seed) in enumerate([(310, 3200, 1), (360, 3600, 2), (285, 2900, 3), (335, 3400, 4)]):
    save(f"key-{i + 1}.wav", key(body, click, seed))
save("enter.wav", enter())
save("draw.wav", draw())
save("plink-a.wav", plink(1318.5))
save("plink-b.wav", plink(987.8))
save("plink-c.wav", plink(1174.7))
save("switch.wav", switch())
save("reap.wav", reap())
save("thump.wav", thump())
save("pad.wav", pad())
