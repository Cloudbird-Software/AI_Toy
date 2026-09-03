// fbank 对拍测试：Go 移植 vs kaldi-native-fbank 参考特征（knf 1.22.3 Python 包
// 现算后落盘 testdata/fbank_1wav_golden.bin：1.wav int16 波形 → knf fbank(80) →
// encoder 元数据 CMVN；二进制 = uint32 行 + uint32 列 + 行主序 float32 LE）。
// 参考 python 会话与再生成命令见 /root/workspace/datasets/jobs/m2-prep-ref/。
package inference

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// readTestWav16k 读 PCM16 单声道 wav，返回 PCM 字节与采样率（仅解 RIFF/fmt/data 块）。
func readTestWav16k(t *testing.T, path string) ([]byte, int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("测试 wav 缺失（基础设施面）: %v", err)
	}
	if string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("非 RIFF/WAVE: %s", path)
	}
	off := 12
	var rate int
	var pcm []byte
	for off+8 <= len(raw) {
		id := string(raw[off : off+4])
		size := int(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		body := raw[off+8 : off+8+size]
		switch id {
		case "fmt ":
			rate = int(binary.LittleEndian.Uint32(body[4:8])) // sample rate
			if binary.LittleEndian.Uint16(body[0:2]) != 1 || binary.LittleEndian.Uint16(body[14:16]) != 16 {
				t.Fatalf("非 PCM16 wav: %s", path)
			}
			if binary.LittleEndian.Uint16(body[2:4]) != 1 {
				t.Fatalf("非单声道 wav: %s", path)
			}
		case "data":
			pcm = body
		}
		off += 8 + size + size%2
	}
	if pcm == nil {
		t.Fatalf("无 data 块: %s", path)
	}
	return pcm, rate
}

func loadGoldenFbank(t *testing.T) (rows, cols int, data []float32) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fbank_1wav_golden.bin"))
	if err != nil {
		t.Skipf("golden 特征缺失（基础设施面）: %v", err)
	}
	rows = int(binary.LittleEndian.Uint32(raw[0:4]))
	cols = int(binary.LittleEndian.Uint32(raw[4:8]))
	f32 := make([]float32, rows*cols)
	for i := range f32 {
		f32[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[8+4*i:]))
	}
	return rows, cols, f32
}

func TestFbankParityKnf(t *testing.T) {
	rows, cols, golden := loadGoldenFbank(t)
	pcm, rate := readTestWav16k(t, filepath.Join(DefaultASRModelDir(), "test_wavs", "1.wav"))
	if rate != fbankSampleRate {
		t.Skipf("参考 wav 非 16kHz（口径外）: %d", rate)
	}
	fb := newFbank()
	got, n, err := fb.compute(pcm16ToInt16Domain(pcm))
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if n != rows {
		t.Fatalf("帧数不齐: got %d, want %d", n, rows)
	}
	if len(got) != rows*cols {
		t.Fatalf("维度不齐: got %d 值, want %d", len(got), rows*cols)
	}
	maxDiff, sumDiff := float32(0), float64(0)
	for i := range got {
		d := got[i] - golden[i]
		if d < 0 {
			d = -d
		}
		if d > maxDiff {
			maxDiff = d
		}
		sumDiff += float64(d)
	}
	meanDiff := float32(sumDiff / float64(len(got)))
	t.Logf("fbank 对拍 %d 帧: max|Δ|=%.2e mean|Δ|=%.2e", rows, maxDiff, meanDiff)
	if maxDiff > 5e-3 {
		t.Errorf("fbank 与 knf 参考最大偏差 %.2e > 5e-3（移植失真）", maxDiff)
	}
}
