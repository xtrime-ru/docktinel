package docktinel

import (
	"encoding/json/v2"
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
