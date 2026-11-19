package workbench

import (
	"bytes"
	"errors"
)

// boundedStatusWriter collects a guest command's reply and fails the command
// once the reply exceeds its budget.
type boundedStatusWriter struct {
	buffer    *bytes.Buffer
	remaining int
}

func (w *boundedStatusWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, errors.New("客机状态超过限制")
	}
	w.remaining -= len(data)
	return w.buffer.Write(data)
}
