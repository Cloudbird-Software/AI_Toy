#!/usr/bin/env python3
"""MaAI VAP (vap_mc_ch_kyoto, 10hz, 20s) -> fused streaming ONNX export + parity check.

Contract (all fixed shapes, B=1):
  inputs:
    wave1, wave2        [1,1,1920]   16k audio window = 320 left-context + 1600 new samples
    lstm_h1, lstm_c1,
    lstm_h2, lstm_c2    [1,1,256]    CPC AR-LSTM hidden per channel
    past_*  (28 tensors) [1,4,199,64] GPT KV cache (ar1/ar2 1 layer each; cross1/cross2/
                                      cross1_c/cross2_c 3 layers each), newest at the end
    cache_mask          [1,1,1,199]  1=valid past slot, 0=zero padding
  outputs:
    p_now [1,1,2], p_future [1,1,2], vad [1,2], p_bins_now [1,1,2], p_bins_future [1,1,2]
    cache_mask_out [1,1,1,199], lstm_*_out (4), past_*_out (28)

Semantics == Maai.process() streaming loop with use_kv_cache=True:
  cache trimmed to newest 199 after every call; the new frame appended before attention
  (T_total=200 steady state); invalid (padded) slots masked to -inf. ALiBi bias differs
  from the unbounded dynamic cache by a per-row constant, which softmax makes exactly
  equivalent; verified empirically below against the shipped Maai modules.
"""
import sys
sys.path.insert(0, "/root/workspace/t3work")
import stubaudio  # noqa: F401  (pyaudio/pygame/pydub stubs)
sys.path.insert(0, "/root/workspace/datasets/models/maai-vap/MaAI/src")

import copy
import os
import time

import numpy as np
import soundfile as sf
import torch
import torch.nn as nn

from maai.models.vap import VapGPT
from maai.models.config import VapConfig
from maai.modules import MultiHeadAttentionAlibi

torch.manual_seed(0)
np.random.seed(0)

import os as _os
WEIGHTS = _os.environ.get("VAP_WEIGHTS",
    "/root/workspace/datasets/models/maai-vap/weights/vap_mc_ch_kyoto/"
    "vap_mc_state_dict_ch_kyoto_10hz_20000msec.pt")
CPC = os.path.expanduser("~/.cache/cpc/60k_epoch4-d0f474de.pt")
WAV = "/root/workspace/datasets/models/maai-vap/MaAI/example/wav_sample/eng_divesh_16k.wav"
OUT_ONNX = _os.environ.get("VAP_OUT", "/root/workspace/t3work/vap_maai_ch_kyoto_mc_10hz.onnx")

FRAME_HZ = 10.0
CTX_SEC = 20
CTX_FRAMES = int(CTX_SEC * FRAME_HZ)          # 200
PAST = CTX_FRAMES - 1                          # 199 (Maai trims cache to ctx-1)
PAD = 320                                      # left-context samples
NEW = 1600                                     # 16000 / 10hz
WIN = PAD + NEW                                # 1920
HEADS, DH, LAYERS_C, LAYERS_X = 4, 64, 1, 3
NEG_INF = -1e9                                 # finite stand-in for -inf (ONNX-safe)

CACHE_GROUPS = []  # (group, layer_idx)
for _n, _l in (("ar1", LAYERS_C), ("ar2", LAYERS_C),
               ("cross1", LAYERS_X), ("cross2", LAYERS_X),
               ("cross1_c", LAYERS_X), ("cross2_c", LAYERS_X)):
    CACHE_GROUPS += [(_n, i) for i in range(_l)]


def const_alibi_mask(n_heads, total):
    """ALiBi+causal(+context_limit) mask for T_total=total via the original math."""
    m = torch.tensor(MultiHeadAttentionAlibi.get_slopes(n_heads))
    rel = torch.arange(total, dtype=torch.float32).view(1, 1, -1).expand(1, n_heads, -1)
    alibi = rel * m.unsqueeze(0).unsqueeze(-1)          # [1,H,T] key-position bias
    causal = torch.tril(torch.ones(total, total))       # lower triangle = 1
    mask = causal.repeat(1, n_heads, 1, 1)              # [1,H,T,T]
    mask = torch.where(mask == 0, torch.full_like(mask, float(NEG_INF)),
                       torch.zeros(1)) + alibi.unsqueeze(-2)
    ctx = CTX_FRAMES
    for j in range(total):                              # context_limit (no-op for <=200)
        for n in range(0, max(0, j - ctx + 1)):
            mask[..., j, n] = NEG_INF
    return mask


class FixedMaskMHA(nn.Module):
    """Same math as MultiHeadAttentionAlibi.forward; mask from a constant buffer plus
    an external validity mask (0 on padded cache slots)."""

    def __init__(self, orig, total):
        super().__init__()
        self.query, self.key, self.value, self.proj = orig.query, orig.key, orig.value, orig.proj
        self.scale = orig.scale
        self.num_heads = orig.num_heads
        self.attn_drop, self.resid_drop = orig.attn_drop, orig.resid_drop
        self.register_buffer("const_mask", const_alibi_mask(orig.num_heads, total))

    def unstack_heads(self, x):
        return x.view(x.size(0), x.size(1), self.num_heads, -1).permute(0, 2, 1, 3)

    def stack_heads(self, x):
        return (x.permute(0, 2, 1, 3).contiguous()
                .view(x.size(0), x.size(2), x.size(1) * x.size(3)))

    def forward(self, Q, K, V, mask=None, past_k=None, past_v=None):
        if mask is None:
            mask = getattr(self, "ext_mask", None)  # injected by VapMaaiStep
        k = self.unstack_heads(self.key(K))
        q = self.unstack_heads(self.query(Q))
        v = self.unstack_heads(self.value(V))
        if past_k is not None:
            k = torch.cat([past_k, k], dim=2)
        if past_v is not None:
            v = torch.cat([past_v, v], dim=2)
        att = (q @ k.transpose(-2, -1)) * self.scale    # [B,H,tq,T]
        m = self.const_mask[..., -att.size(-2):, :]
        if mask is not None:
            m = m + (mask.to(m.dtype) - 1.0) * 1e9  # suppress invalid slots (-inf equiv)
        att = att + m
        att = torch.softmax(att, dim=-1)
        y = self.attn_drop(att) @ v
        y = self.stack_heads(y)
        return self.resid_drop(self.proj(y)), att, k, v


def swap_fixed_mha_(root, total):
    mods = [(n, m) for n, m in root.named_modules()
            if type(m) is MultiHeadAttentionAlibi]
    lookup = dict(root.named_modules())
    for name, mod in mods:
        fixed = FixedMaskMHA(mod, total)
        if "." in name:
            parent = lookup[name.rsplit(".", 1)[0]]
            setattr(parent, name.rsplit(".", 1)[-1], fixed)
        else:
            setattr(root, name, fixed)


class VapMaaiStep(nn.Module):
    """Fused (CPC encoder x2 + VAP GPT + heads) single streaming step, fixed shapes."""

    def __init__(self, vap: VapGPT):
        super().__init__()
        self.enc1, self.enc2 = vap.encoder1, vap.encoder2
        self.ar_channel, self.ar = vap.ar_channel, vap.ar
        self.va_classifier, self.vap_head = vap.va_classifier, vap.vap_head
        self.objective = vap.objective
        swap_fixed_mha_(self, CTX_FRAMES)
        for p in self.parameters():
            p.requires_grad_(False)
        self.eval()

    def encode_channel(self, enc, wave, h, c):
        z = enc.encoder.gEncoder(wave)
        z = z.transpose(1, 2)                    # b c n -> b n c
        z = z[:, 1:-1, :]
        z, (h, c) = enc.encoder.gAR.baseNet(z, (h.contiguous(), c.contiguous()))
        z = enc.downsample(z)
        return z, h, c

    @staticmethod
    def _trim(t):
        return t[..., -PAST:, :]

    def forward(self, wave1, wave2, h1, c1, h2, c2, cache_mask, *past):
        e1, h1o, c1o = self.encode_channel(self.enc1, wave1, h1, c1)
        e2, h2o, c2o = self.encode_channel(self.enc2, wave2, h2, c2)

        pd = {g: (past[2 * j], past[2 * j + 1]) for j, g in enumerate(CACHE_GROUPS)}
        get = lambda g, i, kv: pd[(g, i)][kv]  # noqa: E731

        cm = torch.cat([cache_mask.to(torch.float32),
                        torch.ones(1, 1, 1, 1)], dim=-1)      # [1,1,1,200]
        for m in self.ar_channel.modules():
            if isinstance(m, FixedMaskMHA):
                m.ext_mask = cm
        o1 = self.ar_channel(e1, past_kv=([get("ar1", 0, 0)], [get("ar1", 0, 1)]))
        o2 = self.ar_channel(e2, past_kv=([get("ar2", 0, 0)], [get("ar2", 0, 1)]))
        x1, x2 = o1["x"], o2["x"]
        new = {}
        for i, layer in enumerate(self.ar.layers):
            # TransformerStereoLayer semantics: cross-attention K/V come from the
            # layer INPUTS (pre-update), not from the updated towers.
            x1_in, x2_in = x1, x2
            z = layer.ln_self_attn(x1)
            y, _, k1, v1 = layer.mha(z, z, z, cm, get("cross1", i, 0), get("cross1", i, 1))
            x1 = x1 + layer.dropout(y)
            z = layer.ln_self_attn(x2)
            y, _, k2, v2 = layer.mha(z, z, z, cm, get("cross2", i, 0), get("cross2", i, 1))
            x2 = x2 + layer.dropout(y)
            z = layer.ln_src_attn(x1)
            y, _, k1c, v1c = layer.mha_cross(z, x2_in, x2_in, cm,
                                             get("cross1_c", i, 0), get("cross1_c", i, 1))
            x1 = x1 + layer.dropout(y)
            z = layer.ln_src_attn(x2)
            y, _, k2c, v2c = layer.mha_cross(z, x1_in, x1_in, cm,
                                             get("cross2_c", i, 0), get("cross2_c", i, 1))
            x2 = x2 + layer.dropout(y)
            x1 = x1 + layer.dropout(layer.ffnetwork(layer.ln_ffnetwork(x1)))
            x2 = x2 + layer.dropout(layer.ffnetwork(layer.ln_ffnetwork(x2)))
            new[("cross1", i)] = (k1, v1)
            new[("cross2", i)] = (k2, v2)
            new[("cross1_c", i)] = (k1c, v1c)
            new[("cross2_c", i)] = (k2c, v2c)

        out_x = self.ar.combinator(x1, x2)
        vad = torch.cat([torch.sigmoid(self.va_classifier(x1)),
                         torch.sigmoid(self.va_classifier(x2))], dim=-1)[..., 0, :]
        probs = torch.softmax(self.vap_head(out_x), dim=-1)
        p_now = self.objective.probs_next_speaker_aggregate(probs, from_bin=0, to_bin=1)
        p_future = self.objective.probs_next_speaker_aggregate(probs, from_bin=2, to_bin=3)
        p_bins = self.objective.probs_speaker_bin_aggregate(
            probs, from_bin=0, to_bin=self.objective.n_bins - 1)
        p_bins_now = p_bins[..., 0:2].sum(dim=-1) * 0.5
        p_bins_future = p_bins[..., 2:4].sum(dim=-1) * 0.5

        outs = [p_now, p_future, vad, p_bins_now, p_bins_future, cm[..., -PAST:],
                h1o, c1o, h2o, c2o,
                self._trim(o1["past_k"][0]), self._trim(o1["past_v"][0]),
                self._trim(o2["past_k"][0]), self._trim(o2["past_v"][0])]
        for g in ("cross1", "cross2", "cross1_c", "cross2_c"):
            for i in range(LAYERS_X):
                k, v = new[(g, i)]
                outs += [self._trim(k), self._trim(v)]
        return tuple(outs)


def build_model():
    conf = VapConfig()
    conf.frame_hz = FRAME_HZ
    conf.context_limit = CTX_FRAMES
    conf.encoder_type = "cpc"
    vap = VapGPT(conf)
    vap.load_encoder(cpc_model=CPC)
    vap.eval()
    sd = torch.load(WEIGHTS, map_location="cpu", weights_only=False)
    if "state_dict" in sd:
        sd = sd["state_dict"]
    vap.load_state_dict(sd, strict=False)
    ds = vap.encoder1.downsample
    ds[1].weight = nn.Parameter(sd["encoder.downsample.1.weight"])
    ds[1].bias = nn.Parameter(sd["encoder.downsample.1.bias"])
    ds[2].ln.weight = nn.Parameter(sd["encoder.downsample.2.ln.weight"])
    ds[2].ln.bias = nn.Parameter(sd["encoder.downsample.2.ln.bias"])
    for enc in (vap.encoder1, vap.encoder2):
        enc.downsample[1].weight = ds[1].weight
        enc.downsample[1].bias = ds[1].bias
        enc.downsample[2].ln.weight = ds[2].ln.weight
        enc.downsample[2].ln.bias = ds[2].ln.bias
    return vap


class MaaiRefStream:
    """Faithful Maai.process() streaming reference (dynamic cache, shipped modules)."""

    def __init__(self, vap):
        self.vap = vap
        self.cache = None
        self.b1 = np.zeros(PAD, dtype=np.float32)
        self.b2 = np.zeros(PAD, dtype=np.float32)
        for name in ("encoder1", "encoder2"):
            getattr(vap, name).encoder.gAR.hidden = None

    def step(self, chunk1, chunk2):
        self.b1 = np.concatenate([self.b1, chunk1])[-WIN:]
        self.b2 = np.concatenate([self.b2, chunk2])[-WIN:]
        x1 = torch.from_numpy(self.b1.copy()).float()[None, None]
        x2 = torch.from_numpy(self.b2.copy()).float()[None, None]
        with torch.inference_mode():
            e1, e2 = self.vap.encode_audio(x1, x2)
            out, self.cache = self.vap.forward(e1, e2, cache=self.cache)
            if self.cache is not None:
                trimmed = {}
                for key, (kl, vl) in self.cache.items():
                    trimmed[key] = ([t[..., -PAST:, :] for t in kl],
                                    [t[..., -PAST:, :] for t in vl])
                self.cache = trimmed
        return out


def load_wav_chunks(path=WAV):
    audio, sr = sf.read(path, dtype="float32", always_2d=True)
    assert sr == 16000, sr
    mono = audio.mean(axis=1)
    n = len(mono) // NEW
    c1 = [mono[i * NEW:(i + 1) * NEW].copy() for i in range(n)]
    c2 = [np.zeros(NEW, dtype=np.float32) for _ in range(n)]
    return c1, c2


def initial_inputs():
    past = []
    for _ in CACHE_GROUPS:
        past += [torch.zeros(1, HEADS, PAST, DH), torch.zeros(1, HEADS, PAST, DH)]
    return [torch.zeros(1, 1, WIN), torch.zeros(1, 1, WIN),
            torch.zeros(1, 1, 256), torch.zeros(1, 1, 256),
            torch.zeros(1, 1, 256), torch.zeros(1, 1, 256),
            torch.zeros(1, 1, 1, PAST)] + past


def out_names():
    head = ["p_now", "p_future", "vad", "p_bins_now", "p_bins_future", "cache_mask_out",
            "lstm_h1_out", "lstm_c1_out", "lstm_h2_out", "lstm_c2_out"]
    tail = []
    for g, i in CACHE_GROUPS:
        tail += [f"past_{g}_{i}_k_out", f"past_{g}_{i}_v_out"]
    return head + tail


def in_names():
    head = ["wave1", "wave2", "lstm_h1", "lstm_c1", "lstm_h2", "lstm_c2", "cache_mask"]
    tail = []
    for g, i in CACHE_GROUPS:
        tail += [f"past_{g}_{i}_k", f"past_{g}_{i}_v"]
    return head + tail


def roll(outs, state):
    got = dict(zip(out_names(), outs))
    for k in ("lstm_h1", "lstm_c1", "lstm_h2", "lstm_c2"):
        state[k] = torch.from_numpy(np.asarray(got[f"{k}_out"]))
    state["cache_mask"] = torch.from_numpy(np.asarray(got["cache_mask_out"]))
    for idx, (g, i) in enumerate(CACHE_GROUPS):
        state[f"past_{g}_{i}_k"] = torch.from_numpy(np.asarray(outs[10 + 2 * idx]))
        state[f"past_{g}_{i}_v"] = torch.from_numpy(np.asarray(outs[11 + 2 * idx]))
    return got


def compare(o_ref, got):
    e = 0.0
    for k in ("p_now", "p_future", "vad", "p_bins_now", "p_bins_future"):
        a = np.asarray(o_ref[k], dtype=np.float64).reshape(-1)
        b = np.asarray(got[k], dtype=np.float64).reshape(-1)
        e = max(e, float(np.abs(a - b).max()))
    return e


def run_stream(sess_fn, ref, c1, c2, state, n_steps):
    """sess_fn(wave1, wave2, state) -> outputs; state mutated in place.
    Maintains the rolling 320+1600-sample window (Maai.process buffer semantics)."""
    err, worst = 0.0, -1
    buf1 = np.zeros(PAD, dtype=np.float32)
    buf2 = np.zeros(PAD, dtype=np.float32)
    t0 = time.time()
    for i in range(n_steps):
        o_ref = ref.step(c1[i], c2[i])
        w1 = np.concatenate([buf1, c1[i]])
        w2 = np.concatenate([buf2, c2[i]])
        buf1, buf2 = w1[-PAD:].copy(), w2[-PAD:].copy()
        outs = sess_fn(w1, w2, state)
        got = roll(outs, state)
        e = compare(o_ref, got)
        if e > err:
            err, worst = e, i
    dt = time.time() - t0
    return err, worst, dt


def main(quick=False):
    vap = build_model()
    wrapper = VapMaaiStep(copy.deepcopy(vap))  # deepcopy: keep pristine modules for ref
    c1, c2 = load_wav_chunks()
    n_steps = min(300, len(c1))
    names = in_names()

    # ---- eager parity vs shipped-Maai streaming reference -------------------
    ref = MaaiRefStream(vap)
    state = {n: t.clone() for n, t in zip(names, initial_inputs())}

    def eager_fn(w1, w2, st):
        args = [torch.from_numpy(w1).float()[None, None],
                torch.from_numpy(w2).float()[None, None]] + \
               [st[n] for n in names[2:]]
        with torch.inference_mode():
            return wrapper(*args)

    if not quick:
        err, worst, _ = run_stream(eager_fn, ref, c1, c2, state, n_steps)
        print(f"[eager] wrapper vs Maai reference: max abs err = {err:.3e} "
              f"(worst step {worst}) over {n_steps} steps")
        assert err < 1e-4, "eager parity broken"

    # ---- export -------------------------------------------------------------
    torch.onnx.export(
        wrapper, tuple(initial_inputs()), OUT_ONNX,
        input_names=names, output_names=out_names(),
        opset_version=17, do_constant_folding=True,
    )
    print("[export] wrote", OUT_ONNX, os.path.getsize(OUT_ONNX) // 1024, "KiB")

    # ---- ORT parity vs shipped-Maai reference -------------------------------
    import onnxruntime as ort
    so = ort.SessionOptions()
    so.intra_op_num_threads = 2
    so.inter_op_num_threads = 1
    so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    sess = ort.InferenceSession(OUT_ONNX, so, providers=["CPUExecutionProvider"])

    ref = MaaiRefStream(vap)
    state = {n: t.clone() for n, t in zip(names, initial_inputs())}

    def ort_fn(w1, w2, st):
        feed = {"wave1": w1[None, None], "wave2": w2[None, None]}
        for n in names[2:]:
            feed[n] = st[n].numpy()
        return sess.run(None, feed)

    if not quick:
        err, worst, _ = run_stream(ort_fn, ref, c1, c2, state, n_steps)
        print(f"[ort] ONNX vs Maai reference: max abs err = {err:.3e} "
              f"(worst step {worst}) over {n_steps} steps")
        assert err < 1e-3, "ONNX parity requirement (<1e-3) not met"

    # ---- RTF over the full wav ----------------------------------------------
    ref_states = None
    state = {n: t.clone() for n, t in zip(names, initial_inputs())}
    _, _, dt = run_stream(ort_fn, MaaiRefStream(vap), c1, c2, state, len(c1))
    audio_sec = len(c1) * NEW / 16000
    print(f"[rtf] {len(c1)} frames, {audio_sec:.1f}s audio in {dt:.2f}s -> "
          f"RTF={dt / audio_sec:.4f} ({dt / len(c1) * 1000:.1f} ms/frame)")


if __name__ == "__main__":
    main(quick=_os.environ.get("VAP_QUICK") == "1")
