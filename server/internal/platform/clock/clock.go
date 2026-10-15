package clock

import "time"

type Clock interface {
	Now() time.Time
}

type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

var Shanghai = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()

func Format(t time.Time) string {
	return t.In(Shanghai).Format(time.RFC3339)
}
