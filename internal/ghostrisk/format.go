package ghostrisk

import (
	"strconv"
	"strings"
)

func plural(n int, tmpl string) string {
	s := ""
	if n != 1 {
		s = "s"
	}
	out := strings.Replace(tmpl, "%d", strconv.Itoa(n), 1)
	return strings.Replace(out, "%s", s, 1)
}

func itoa1(tmpl string, a int) string {
	return strings.Replace(tmpl, "%d", strconv.Itoa(a), 1)
}

func itoa2(tmpl string, a, b int) string {
	out := strings.Replace(tmpl, "%d", strconv.Itoa(a), 1)
	return strings.Replace(out, "%d", strconv.Itoa(b), 1)
}
