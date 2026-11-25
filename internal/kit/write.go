package kit

import (
	"encoding/json"
	"os"
)

func AppendLine(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n')) // one write: a reader never sees half a line
	return err
}

func WriteWholeJSON(path string, v any) error {
	b, _ := json.Marshal(v)
	return WriteWhole(path, string(b))
}
