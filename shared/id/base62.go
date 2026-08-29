package id

import (
	"fmt"
)

const base62Chars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func Encode(n uint64) string {
	ans := make([]byte, 0)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		ans = append(ans, base62Chars[n%62])
		n /= 62
	}
	for i, j := 0, len(ans)-1; i < j; i, j = i+1, j-1 {
		ans[i], ans[j] = ans[j], ans[i]

	}
	return string(ans)
}

func Decode(s string) (uint64, error) {
	var n uint64

	if len(s) == 0 {
		return 0, fmt.Errorf("cannot decode empty string")
	}

	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*62 + uint64(c-'0')
		} else if c >= 'a' && c <= 'z' {
			n = n*62 + uint64(c-'a'+10)
		} else if c >= 'A' && c <= 'Z' {
			n = n*62 + uint64(c-'A'+36)
		} else {
			return 0, fmt.Errorf("invalid character in base62 string: %q", c)
		}
	}
	return n, nil
}
