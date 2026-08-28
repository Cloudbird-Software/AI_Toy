package journeys

import (
	"encoding/json"
	"io"
	"os"
)

// Emit 将报告序列化为两空格缩进 JSON：outPath 非空写入该文件，否则打印到 stdout。
func Emit(rep *Report, outPath string, stdout io.Writer) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if outPath != "" {
		return os.WriteFile(outPath, data, 0o644)
	}
	_, err = stdout.Write(data)
	return err
}
