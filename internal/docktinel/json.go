package docktinel

import (
	"errors"
	"io"
)

func readJSONBytes(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxJSONSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxJSONSize {
		return nil, errors.New("JSON document exceeds 1 MiB")
	}
	return data, nil
}
