package api

import (
	"os"
	"strconv"
	"strings"
)

var osReadFile = os.ReadFile

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
