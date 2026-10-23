// Package version is CK Karaoke's version. It is kept in the VERSION file
// beside this one, which every build reads: the Go binaries embed it, and the
// Android, iOS and desktop builds take their version from it. A release tag must
// match it.
package version

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed VERSION
var raw string

// Version is the version, such as "1.2.3" or "1.2.3-rc1".
var Version = strings.TrimSpace(raw)

var pattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

// Numeric is the version without any suffix, such as "1.2.3". App stores and
// installers accept only this form.
func Numeric() string {
	n, _, _ := strings.Cut(Version, "-")
	return n
}

// Code is the Android version code, MAJOR*10000 + MINOR*100 + PATCH, which
// rises with every release.
func Code() int {
	c, err := code(Version)
	if err != nil {
		panic(err)
	}
	return c
}

func code(v string) (int, error) {
	m := pattern.FindStringSubmatch(v)
	if m == nil {
		return 0, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH[-SUFFIX]", v)
	}
	var n [3]int
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	if n[1] > 99 || n[2] > 99 {
		return 0, fmt.Errorf("version %q: minor and patch must be below 100", v)
	}
	return n[0]*10000 + n[1]*100 + n[2], nil
}
