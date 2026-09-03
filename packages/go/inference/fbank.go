// kaldi-native-fbank（knf 1.22.x）兼容的 80 维 fbank——FireRedASR2 训练口径
// （FireRedASR2S fireredasr2/data/asr_feat.py：kaldiio 读 wav 得 int16 量纲波形 →
// knf fbank → kaldi 全局 CMVN）。逐项对应 knf csrc 默认值：
//   - 分帧：25ms 窗（400 样本）/ 10ms 移（160 样本）/ snip_edges=true；
//   - 加窗链：remove_dc_offset → preemphasis 0.97（含首样本 x0-=c*x0）→ povey 窗；
//   - 频谱：round_to_power_of_two → 512 点 FFT，功率谱不做 1/N 归一化；
//   - mel：kaldi 刻度（1127*ln(1+f/700)），80 bin，low 20Hz / high 0（=奈奎斯特 8kHz）；
//   - 输出：log(max(mel_energy, FLT_EPSILON))，无 dither、无能量帧。
//
// 量纲纪律：输入必须保持 int16 量纲的 float32（不做 [-1,1] 归一化）——模型元数据
// cmvn_mean≈+10~14 即该量纲下的 log-mel 统计；喂归一化浮点会被判为静音（<sil>）。
package inference

import (
	"fmt"
	"math"
)

const (
	fbankSampleRate  = 16000
	fbankFrameShift  = 160 // 10ms
	fbankFrameLength = 400 // 25ms
	fbankFFTSize     = 512 // 400 向上取 2 的幂
	fbankNumBins     = 80
	fbankMelLowFreq  = 20.0
	fbankMelHighFreq = 0.0 // ≤0 → 奈奎斯特
	fbankPreemph     = 0.97
	fbankFloatEps    = 1.1920929e-7 // FLT_EPSILON（knf log 下限）
)

// fbank 特征提取器（预生成窗与 mel 权重，compute 可反复调用；单线程）。
type fbank struct {
	window  []float32
	melBins []melBin
	frame   []float32 // 512 点帧缓冲
	pow     []float32 // 功率谱缓冲（257 点）
	fftBuf  []complex128
	fftPerm []int
	twiddle []complex128
}

type melBin struct {
	first int
	w     []float32
}

func newFbank() *fbank {
	f := &fbank{}
	// povey 窗：(0.5 - 0.5*cos(2π*i/(N-1)))^0.85
	a := 2 * math.Pi / float64(fbankFrameLength-1)
	f.window = make([]float32, fbankFrameLength)
	for i := range f.window {
		f.window[i] = float32(math.Pow(0.5-0.5*math.Cos(a*float64(i)), 0.85))
	}
	// kaldi 刻度 mel 滤波器组（knf InitKaldiMelBanks）
	nyquist := 0.5 * fbankSampleRate
	lowFreq := fbankMelLowFreq
	highFreq := nyquist + fbankMelHighFreq // 0 → 奈奎斯特
	melLow := melScale(lowFreq)
	melHigh := melScale(highFreq)
	delta := (melHigh - melLow) / float64(fbankNumBins+1)
	binWidth := float64(fbankSampleRate) / float64(fbankFFTSize)
	numFFTBins := fbankFFTSize / 2
	f.melBins = make([]melBin, fbankNumBins)
	for b := 0; b < fbankNumBins; b++ {
		leftM := melLow + float64(b)*delta
		centerM := melLow + float64(b+1)*delta
		rightM := melLow + float64(b+2)*delta
		first := -1
		w := make([]float32, 0, numFFTBins)
		for i := 0; i < numFFTBins; i++ {
			mel := melScale(binWidth * float64(i))
			if mel > leftM && mel < rightM {
				if mel <= centerM {
					w = append(w, float32((mel-leftM)/(centerM-leftM)))
				} else {
					w = append(w, float32((rightM-mel)/(rightM-centerM)))
				}
				if first == -1 {
					first = i
				}
			}
		}
		f.melBins[b] = melBin{first: first, w: w}
	}
	f.frame = make([]float32, fbankFFTSize)
	f.pow = make([]float32, fbankFFTSize/2+1)
	newFFT(fbankFFTSize, &f.fftBuf, &f.fftPerm, &f.twiddle)
	return f
}

func melScale(freqHz float64) float64 { return 1127.0 * math.Log(1.0+freqHz/700.0) }

// compute 波形 → 特征矩阵（行主序 [T*80]，T = 1+(n-400)/160，样本不足一帧时报错）。
func (f *fbank) compute(pcm []float32) ([]float32, int, error) {
	if len(pcm) < fbankFrameLength {
		return nil, 0, fmt.Errorf("inference: fbank: 样本数 %d < 一帧 %d", len(pcm), fbankFrameLength)
	}
	numFrames := 1 + (len(pcm)-fbankFrameLength)/fbankFrameShift
	feats := make([]float32, 0, numFrames*fbankNumBins)
	for fr := 0; fr < numFrames; fr++ {
		frame := f.frame
		copy(frame[:fbankFrameLength], pcm[fr*fbankFrameShift:])
		for i := fbankFrameLength; i < fbankFFTSize; i++ {
			frame[i] = 0
		}
		f.processWindow(frame)
		f.runFFT(frame)
		// 功率谱：[re0, re_N/2, re1, im1, ...] → p[k]=|X_k|²（kaldi 无 1/N 归一化）
		pow := f.pow
		pow[0] = frame[0] * frame[0]
		pow[fbankFFTSize/2] = frame[1] * frame[1]
		for i := 1; i < fbankFFTSize/2; i++ {
			re, im := frame[2*i], frame[2*i+1]
			pow[i] = re*re + im*im
		}
		for _, bin := range f.melBins {
			var e float32
			for j, w := range bin.w {
				e += w * pow[bin.first+j]
			}
			if e < fbankFloatEps {
				e = fbankFloatEps
			}
			feats = append(feats, float32(math.Log(float64(e))))
		}
	}
	return feats, numFrames, nil
}

// processWindow 对 400 点帧做 remove_dc_offset → preemphasize → povey 加窗。
func (f *fbank) processWindow(frame []float32) {
	var sum float32
	for i := 0; i < fbankFrameLength; i++ {
		sum += frame[i]
	}
	mean := sum / fbankFrameLength
	for i := 0; i < fbankFrameLength; i++ {
		frame[i] -= mean
	}
	for i := fbankFrameLength - 1; i > 0; i-- {
		frame[i] -= fbankPreemph * frame[i-1]
	}
	frame[0] -= fbankPreemph * frame[0]
	for i := 0; i < fbankFrameLength; i++ {
		frame[i] *= f.window[i]
	}
}

// runFFT 512 点基-2 迭代 FFT（float64 中间精度），按 kaldi 实数 FFT 布局回写：
// buf[0]=X_0.re、buf[1]=X_{N/2}.re、buf[2k]=X_k.re、buf[2k+1]=X_k.im。
func (f *fbank) runFFT(frame []float32) {
	buf := f.fftBuf
	for i := 0; i < fbankFFTSize; i++ {
		buf[f.fftPerm[i]] = complex(float64(frame[i]), 0)
	}
	for size := 2; size <= fbankFFTSize; size <<= 1 {
		half := size / 2
		step := fbankFFTSize / size
		for i := 0; i < fbankFFTSize; i += size {
			for j := 0; j < half; j++ {
				w := f.twiddle[j*step]
				u := buf[i+j]
				v := buf[i+j+half] * w
				buf[i+j] = u + v
				buf[i+j+half] = u - v
			}
		}
	}
	// 复数谱 → kaldi 实数布局
	re0 := real(buf[0])
	reHalf := real(buf[fbankFFTSize/2])
	frame[0] = float32(re0)
	frame[1] = float32(reHalf)
	for k := 1; k < fbankFFTSize/2; k++ {
		frame[2*k] = float32(real(buf[k]))
		frame[2*k+1] = float32(imag(buf[k]))
	}
}

// newFFT 预生成位反转表与旋转因子。
func newFFT(n int, buf *[]complex128, perm *[]int, twiddle *[]complex128) {
	*buf = make([]complex128, n)
	*perm = make([]int, n)
	bits := 0
	for 1<<bits < n {
		bits++
	}
	for i := range *perm {
		r := 0
		for b := 0; b < bits; b++ {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		(*perm)[i] = r
	}
	*twiddle = make([]complex128, n/2)
	for j := 0; j < n/2; j++ {
		ang := -2 * math.Pi * float64(j) / float64(n)
		(*twiddle)[j] = complex(math.Cos(ang), math.Sin(ang))
	}
}
