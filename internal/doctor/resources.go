package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

// ResourceChecks validates local startup resources without contacting Kata or Herdr.
func ResourceChecks(c config.Config) []CheckResult {
	checks := make([]CheckResult, 0, len(c.Checkouts)+1)
	add := func(name, path string, err error) {
		detail := path
		if err != nil {
			detail = fmt.Sprintf("%s: %s; fix %s in %s", path, err, strings.Replace(name, "checkout ", "checkouts.", 1), config.DefaultPath())
		}
		checks = append(checks, CheckResult{Name: name, Passed: err == nil, Detail: detail})
	}
	_, err := exec.LookPath(c.Kata.Bin)
	add("kata.bin", c.Kata.Bin, err)
	names := make([]string, 0, len(c.Checkouts))
	for name := range c.Checkouts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := c.Checkouts[name]
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("%s: checkout is not a directory", path)
		}
		add("checkout "+name, path, err)
	}
	return checks
}
