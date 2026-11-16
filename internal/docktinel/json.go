package docktinel

import (
	"encoding/json/v2"
	"errors"
	"io"
	"strconv"
	"time"
)

// Keep the HTTP API's duration representation in nanoseconds. JSON v2
// deliberately has no default representation for time.Duration.
var durationMarshalers = json.WithMarshalers(json.MarshalFunc(func(d time.Duration) ([]byte, error) {
	return strconv.AppendInt(nil, int64(d), 10), nil
}))

var durationUnmarshalers = json.WithUnmarshalers(json.UnmarshalFunc(func(b []byte, d *time.Duration) error {
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err == nil {
		*d = time.Duration(n)
	}
	return err
}))

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
