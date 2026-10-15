package qa

import "strings"

type Alias struct {
	Name     string
	ChunkID  string
	DocID    string
	Title    string
	Page     int
	Graph    bool
	Parent   string
	Text     string
}

type Decoder struct {
	hold  string
	table map[string]Alias
	used  []Alias
	seen  map[string]bool
	Refuse bool
}

func NewDecoder(aliases []Alias) *Decoder {
	m := make(map[string]Alias, len(aliases))
	for _, a := range aliases {
		m[a.Name] = a
	}
	return &Decoder{table: m, seen: map[string]bool{}}
}

func (d *Decoder) Push(delta string) string {
	d.hold += delta
	var b strings.Builder
	for {
		i := strings.Index(d.hold, "[[")
		if i < 0 {
			if strings.HasSuffix(d.hold, "[") {
				b.WriteString(d.hold[:len(d.hold)-1])
				d.hold = "["
				return b.String()
			}
			b.WriteString(d.hold)
			d.hold = ""
			return b.String()
		}
		b.WriteString(d.hold[:i])
		rest := d.hold[i:]
		j := strings.Index(rest, "]]")
		if j < 0 {
			d.hold = rest
			return b.String()
		}
		inner := rest[2:j]
		d.take(inner)
		d.hold = rest[j+2:]
	}
}

func (d *Decoder) Flush() string {
	rest := d.hold
	d.hold = ""
	if strings.Contains(rest, "[[") {
		return d.Push(rest)
	}
	return rest
}

func (d *Decoder) take(inner string) {
	if inner == "REFUSE" {
		d.Refuse = true
		return
	}
	a, ok := d.table[inner]
	if !ok {
		return
	}
	if d.seen[inner] {
		return
	}
	d.seen[inner] = true
	d.used = append(d.used, a)
}

func (d *Decoder) Used() []Alias { return d.used }

const Refusal = "知识库中未找到相关依据"

func Finalize(visible string, used []Alias, refuse bool) (string, []Alias, string) {
	if refuse || len(used) == 0 {
		return Refusal, nil, "refused"
	}
	return strings.TrimSpace(visible), used, "kb"
}

func Label(title string, page int) string {
	return title + ", 第 " + itoa(page) + " 页"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
