package ident

import (
	"fmt"
	"strings"
	"time"
)

func IssueRequestUID(child string) (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	if len(child) != 26 || child[0] > '7' {
		return "", fmt.Errorf("invalid issue UID")
	}
	var ms int64
	for i, c := range child {
		n := strings.IndexRune(alphabet, c)
		if n < 0 {
			return "", fmt.Errorf("invalid issue UID")
		}
		if i < 10 {
			ms = ms*32 + int64(n)
		}
	}
	return RunUID(time.UnixMilli(ms), "issue-request:"+child), nil
}
