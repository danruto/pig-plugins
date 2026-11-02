package store

import (
	"crypto/sha256"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
)

type Coordination struct {
	Database, Key           string
	WaitMS, PollMS, StaleMS float64
	RenewInterval           time.Duration
}

type RecoveryLockOptions struct {
	WaitMS, PollMS, StaleMS *float64
}

func MutationCoordination(document string) (Coordination, error) {
	identity, err := paths.Canonical(document)
	if err != nil {
		return Coordination{}, err
	}
	return Coordination{
		Database: filepath.Join(filepath.Dir(filepath.Dir(identity)), ".pi-hermes-locks.sqlite"),
		Key:      "mutation:" + identity, WaitMS: 5000, PollMS: 10, StaleMS: 300000,
	}, nil
}

func ConsolidationCoordination(agentRoot, document, target, toolTarget string) (Coordination, error) {
	identity, err := paths.Canonical(document)
	if err != nil {
		return Coordination{}, err
	}
	root := trimJSWhitespace(os.Getenv("PI_HERMES_CONSOLIDATION_LOCK_DIR"))
	if root == "" {
		root = filepath.Join(agentRoot, "pi-hermes-memory", ".consolidation-locks")
	}
	database, err := paths.Canonical(filepath.Join(root, "locks.sqlite"))
	if err != nil {
		return Coordination{}, err
	}
	waitMS := 5000.0
	if configured, present := os.LookupEnv("PI_HERMES_CONSOLIDATION_LOCK_WAIT_MS"); present {
		waitMS = consolidationWaitMS(configured)
	}
	return Coordination{
		Database: database,
		Key:      fmt.Sprintf("%s:%s:%x", sanitizeLockPart(toolTarget), sanitizeLockPart(target), sha256.Sum256([]byte(identity))),
		WaitMS:   waitMS, PollMS: 50, StaleMS: 45000,
		RenewInterval: 10 * time.Second,
	}, nil
}

func RecoveryCoordination(database string, options RecoveryLockOptions) (Coordination, error) {
	identity, err := paths.Canonical(database)
	if err != nil {
		return Coordination{}, err
	}
	result := Coordination{
		Database: filepath.Join(filepath.Dir(identity), ".pi-hermes-locks.sqlite"),
		Key:      "recovery:" + identity, WaitMS: 5000, PollMS: 50, StaleMS: 300000,
	}
	for _, override := range []struct {
		from *float64
		to   *float64
	}{
		{options.WaitMS, &result.WaitMS}, {options.PollMS, &result.PollMS}, {options.StaleMS, &result.StaleMS},
	} {
		if override.from != nil {
			*override.to = *override.from
		}
	}
	result.WaitMS = math.Max(0, result.WaitMS)
	result.PollMS = math.Max(0, result.PollMS)
	return result, nil
}

var unsafeLockPart = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeLockPart(value string) string {
	part := unsafeLockPart.ReplaceAllString(value, "_")
	if len(part) > 80 {
		part = part[:80]
	}
	if part == "" {
		return "unknown"
	}
	return part
}

func trimJSWhitespace(value string) string {
	return strings.Trim(value, "\u0009\u000a\u000b\u000c\u000d\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

var decimalJSNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var radixJSNumber = regexp.MustCompile(`^0(?:[xX][0-9a-fA-F]+|[bB][01]+|[oO][0-7]+)$`)

func consolidationWaitMS(value string) float64 {
	value = trimJSWhitespace(value)
	if value == "" {
		return 0
	}
	var number float64
	var err error
	switch {
	case radixJSNumber.MatchString(value):
		number, err = radixNumber(value)
	case decimalJSNumber.MatchString(value):
		number, err = strconv.ParseFloat(value, 64)
	default:
		return 5000
	}
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 5000
	}
	return number
}

func radixNumber(value string) (float64, error) {
	base := 16
	switch value[1] {
	case 'b', 'B':
		base = 2
	case 'o', 'O':
		base = 8
	}
	integer, ok := new(big.Int).SetString(value[2:], base)
	if !ok {
		return 0, fmt.Errorf("invalid radix number %q", value)
	}
	number, _ := integer.Float64()
	return number, nil
}
